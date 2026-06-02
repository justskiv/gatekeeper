//nolint:wsl_v5 // Integration-style tests keep each scenario in one block.
package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"

	"github.com/justskiv/gatekeeper/internal/admission"
	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/engine"
	"github.com/justskiv/gatekeeper/internal/store"
)

const tributeTestKey = "tribute-api-key"

func TestTributeWebhookValidSignatureUsesRawBodyAndRedactsPayload(t *testing.T) {
	db := newWebhookTestDB(t)
	handler := &TributeHandler{
		DB:     db,
		APIKey: tributeTestKey,
		Engine: engine.New(nil),
	}

	raw := []byte(`{
		"sent_at":"2026-06-02T10:00:01Z",
		"name":"new_subscription",
		"created_at":"2026-06-02T10:00:00Z",
		"payload":{
			"subscription_name":"Gold",
			"subscription_id":1644,
			"period_id":1547,
			"telegram_user_id":123,
			"telegram_username":"durov",
			"email":"durov@example.com",
			"web_app_link":"https://t.me/app?startapp=secret",
			"expires_at":"2026-07-02T10:00:00Z"
		}
	}`)

	resp := postTribute(t, handler, raw, signTribute(raw))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", resp.Code, resp.Body.String())
	}

	sub, ok, err := store.NewSubscriptions(db).GetActive(
		context.Background(), 123, domain.PlatformTribute)
	if err != nil || !ok {
		t.Fatalf("active subscription = (%+v, %v, %v), want active", sub, ok, err)
	}

	if sub.ExternalID != "1644" ||
		sub.PeriodID != "1547" ||
		sub.Tier != "Gold" ||
		sub.LastSignal != "webhook" ||
		sub.ExpiresAt == nil {
		t.Fatalf("subscription = %+v, want Tribute webhook fields", sub)
	}

	var payload string
	if err := db.QueryRowContext(context.Background(),
		`SELECT payload_json FROM tribute_events`).Scan(&payload); err != nil {
		t.Fatalf("read payload: %v", err)
	}

	for _, forbidden := range []string{"durov@example.com", "startapp=secret"} {
		if strings.Contains(payload, forbidden) {
			t.Fatalf("payload contains %q: %s", forbidden, payload)
		}
	}
}

func TestTributeWebhookInvalidSignatureIsAuditedWithoutDomainChanges(t *testing.T) {
	db := newWebhookTestDB(t)
	handler := &TributeHandler{
		DB:     db,
		APIKey: tributeTestKey,
		Engine: engine.New(nil),
	}

	raw := tributeSubscriptionPayload("new_subscription",
		"2026-06-02T10:00:00Z", "2026-07-02T10:00:00Z")
	resp := postTribute(t, handler, raw, "bad")
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.Code)
	}

	var eventCount, invalidSignatures, rejectedAudit int
	if err := db.QueryRowContext(context.Background(), `
		SELECT count(*), sum(CASE WHEN signature_valid = 0 THEN 1 ELSE 0 END)
		FROM tribute_events
		WHERE status = 'failed'`,
	).Scan(&eventCount, &invalidSignatures); err != nil {
		t.Fatalf("count failed tribute events: %v", err)
	}

	if eventCount != 1 || invalidSignatures != 1 {
		t.Fatalf("failed events=%d invalid=%d, want one invalid failed event",
			eventCount, invalidSignatures)
	}

	if err := db.QueryRowContext(context.Background(), `
		SELECT count(*)
		FROM audit_log
		WHERE kind = 'webhook_rejected'`,
	).Scan(&rejectedAudit); err != nil {
		t.Fatalf("count rejected audit: %v", err)
	}

	if rejectedAudit != 1 {
		t.Fatalf("webhook_rejected audit = %d, want 1", rejectedAudit)
	}

	if _, ok, err := store.NewSubscriptions(db).GetActive(
		context.Background(), 123, domain.PlatformTribute,
	); err != nil || ok {
		t.Fatalf("active subscription = (_, %v, %v), want absent", ok, err)
	}
}

func TestTributeWebhookInvalidSignatureDoesNotOverwriteExistingEvent(t *testing.T) {
	db := newWebhookTestDB(t)
	handler := &TributeHandler{
		DB:     db,
		APIKey: tributeTestKey,
		Engine: engine.New(nil),
	}

	raw := []byte(`{
		"name":"new_subscription",
		"created_at":"2026-06-02T10:00:00Z",
		"sent_at":"2026-06-02T10:00:00Z",
		"payload":{
			"subscription_name":"Gold",
			"subscription_id":1644,
			"telegram_user_id":123,
			"expires_at":"2026-07-02T10:00:00Z"
		}
	}`)
	resp := postTribute(t, handler, raw, signTribute(raw))
	if resp.Code != http.StatusOK {
		t.Fatalf("valid status = %d body=%s, want 200",
			resp.Code, resp.Body.String())
	}

	resp = postTribute(t, handler, raw, "bad")
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("invalid retry status = %d, want 401", resp.Code)
	}

	var status string
	if err := db.QueryRowContext(context.Background(), `
		SELECT status
		FROM tribute_events
		WHERE dedup_key = ?`,
		rawDedupKey(raw)).Scan(&status); err != nil {
		t.Fatalf("read tribute event status: %v", err)
	}

	if status != string(store.TributeEventProcessed) {
		t.Fatalf("status = %s, want processed", status)
	}

	var rejectedAudit int
	if err := db.QueryRowContext(context.Background(), `
		SELECT count(*)
		FROM audit_log
		WHERE kind = 'webhook_rejected'`,
	).Scan(&rejectedAudit); err != nil {
		t.Fatalf("count rejected audit: %v", err)
	}

	if rejectedAudit != 0 {
		t.Fatalf("webhook_rejected audit = %d, want 0", rejectedAudit)
	}
}

func TestTributeWebhookDedupIgnoredFailedAndOrdering(t *testing.T) {
	db := newWebhookTestDB(t)
	handler := &TributeHandler{
		DB:     db,
		APIKey: tributeTestKey,
		Engine: engine.New(nil),
	}

	initial := tributeSubscriptionPayload("new_subscription",
		"2026-06-02T10:00:00Z", "2026-07-02T10:00:00Z")
	for range 2 {
		resp := postTribute(t, handler, initial, signTribute(initial))
		if resp.Code != http.StatusOK {
			t.Fatalf("initial status = %d, want 200", resp.Code)
		}
	}

	var events, activated int
	if err := db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM tribute_events`).Scan(&events); err != nil {
		t.Fatalf("count events: %v", err)
	}

	if err := db.QueryRowContext(context.Background(), `
		SELECT count(*)
		FROM audit_log
		WHERE kind = 'subscription_activated'`,
	).Scan(&activated); err != nil {
		t.Fatalf("count activations: %v", err)
	}

	if events != 1 || activated != 1 {
		t.Fatalf("events=%d activations=%d, want dedup no-op", events, activated)
	}

	ignored := []byte(`{
		"name":"physical_order_created",
		"created_at":"2026-06-02T11:00:00Z",
		"sent_at":"2026-06-02T11:00:01Z",
		"payload":{"order_id":1}
	}`)
	resp := postTribute(t, handler, ignored, signTribute(ignored))
	if resp.Code != http.StatusOK {
		t.Fatalf("ignored status = %d, want 200", resp.Code)
	}

	var ignoredRows int
	if err := db.QueryRowContext(context.Background(), `
		SELECT count(*)
		FROM tribute_events
		WHERE status = 'ignored'`,
	).Scan(&ignoredRows); err != nil {
		t.Fatalf("count ignored: %v", err)
	}

	if ignoredRows != 1 {
		t.Fatalf("ignored rows = %d, want 1", ignoredRows)
	}

	malformed := []byte(`{
		"name":"new_subscription",
		"created_at":"2026-06-02T12:00:00Z",
		"sent_at":"2026-06-02T12:00:01Z",
		"payload":{"subscription_id":1644,"period_id":2000}
	}`)
	resp = postTribute(t, handler, malformed, signTribute(malformed))
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("malformed status = %d, want 400", resp.Code)
	}

	renewed := tributeSubscriptionPayload("renewed_subscription",
		"2026-06-03T10:00:00Z", "2026-08-02T10:00:00Z")
	resp = postTribute(t, handler, renewed, signTribute(renewed))
	if resp.Code != http.StatusOK {
		t.Fatalf("renewed status = %d, want 200", resp.Code)
	}

	stale := tributeSubscriptionPayload("new_subscription",
		"2026-06-01T10:00:00Z", "2026-06-15T10:00:00Z")
	resp = postTribute(t, handler, stale, signTribute(stale))
	if resp.Code != http.StatusOK {
		t.Fatalf("stale status = %d, want 200", resp.Code)
	}

	sub, ok, err := store.NewSubscriptions(db).GetActive(
		context.Background(), 123, domain.PlatformTribute)
	if err != nil || !ok || sub.ExpiresAt == nil {
		t.Fatalf("active subscription = (%+v, %v, %v), want active", sub, ok, err)
	}

	if got := sub.ExpiresAt.Format("2006-01-02"); got != "2026-08-02" {
		t.Fatalf("expires_at = %s, want renewed date", got)
	}
}

func TestTributeWebhookSubsecondOrderingKeepsNewestEvent(t *testing.T) {
	db := newWebhookTestDB(t)
	handler := &TributeHandler{
		DB:     db,
		APIKey: tributeTestKey,
		Engine: engine.New(nil),
	}

	newer := tributeSubscriptionPayload("new_subscription",
		"2026-06-02T10:00:00.900Z", "2026-08-02T10:00:00Z")
	older := tributeSubscriptionPayload("renewed_subscription",
		"2026-06-02T10:00:00.100Z", "2026-07-02T10:00:00Z")
	for _, raw := range [][]byte{newer, older} {
		resp := postTribute(t, handler, raw, signTribute(raw))
		if resp.Code != http.StatusOK {
			t.Fatalf("webhook status = %d body=%s, want 200",
				resp.Code, resp.Body.String())
		}
	}

	sub, ok, err := store.NewSubscriptions(db).GetActive(
		context.Background(), 123, domain.PlatformTribute)
	if err != nil || !ok || sub.ExpiresAt == nil || sub.LastEventAt == nil {
		t.Fatalf("active subscription = (%+v, %v, %v), want active", sub, ok, err)
	}

	if got := sub.ExpiresAt.Format("2006-01-02"); got != "2026-08-02" {
		t.Fatalf("expires_at = %s, want newest event expiry", got)
	}

	if got := sub.LastEventAt.Nanosecond(); got != 900_000_000 {
		t.Fatalf("last_event_at nanos = %d, want 900000000", got)
	}
}

func TestTributePayloadInt64RejectsTrailingText(t *testing.T) {
	envelope := tributeEnvelope{
		Payload: map[string]any{"telegram_user_id": "123abc"},
	}

	if got, ok := envelope.payloadInt64("telegram_user_id"); ok {
		t.Fatalf("payloadInt64 = (%d, true), want false", got)
	}
}

func TestTributeWebhookCancelDefaultAndImmediateOverride(t *testing.T) {
	defaultDB := newWebhookTestDB(t)
	defaultHandler := &TributeHandler{
		DB:     defaultDB,
		APIKey: tributeTestKey,
		Engine: engine.New(nil),
	}

	create := tributeSubscriptionPayload("new_subscription",
		"2026-06-02T10:00:00Z", "2026-07-02T10:00:00Z")
	cancel := tributeSubscriptionPayload("cancelled_subscription",
		"2026-06-03T10:00:00Z", "2026-07-02T10:00:00Z")
	for _, raw := range [][]byte{create, cancel} {
		resp := postTribute(t, defaultHandler, raw, signTribute(raw))
		if resp.Code != http.StatusOK {
			t.Fatalf("default cancel status = %d, want 200", resp.Code)
		}
	}

	if _, ok, err := store.NewSubscriptions(defaultDB).GetActive(
		context.Background(), 123, domain.PlatformTribute,
	); err != nil || !ok {
		t.Fatalf("default cancel active = %v err=%v, want active kept", ok, err)
	}

	immediateDB := newWebhookTestDB(t)
	immediateHandler := &TributeHandler{
		DB:              immediateDB,
		APIKey:          tributeTestKey,
		Engine:          engine.New(nil),
		CancelImmediate: true,
	}
	for _, raw := range [][]byte{create, cancel} {
		resp := postTribute(t, immediateHandler, raw, signTribute(raw))
		if resp.Code != http.StatusOK {
			t.Fatalf("immediate cancel status = %d, want 200", resp.Code)
		}
	}

	if _, ok, err := store.NewSubscriptions(immediateDB).GetActive(
		context.Background(), 123, domain.PlatformTribute,
	); err != nil || ok {
		t.Fatalf("immediate cancel active = %v err=%v, want expired", ok, err)
	}
}

func TestTributeWebhookLedgerAllowsStartGrantAccess(t *testing.T) {
	db := newWebhookTestDB(t)
	handler := &TributeHandler{
		DB:     db,
		APIKey: tributeTestKey,
		Engine: engine.New(nil),
	}

	raw := tributeSubscriptionPayload("new_subscription",
		"2026-06-02T10:00:00Z", "2026-07-02T10:00:00Z")
	resp := postTribute(t, handler, raw, signTribute(raw))
	if resp.Code != http.StatusOK {
		t.Fatalf("webhook status = %d, want 200", resp.Code)
	}

	seedSharedInvite(t, db, domain.ResourceChat, "https://t.me/+chat")
	seedSharedInvite(t, db, domain.ResourceChannel, "https://t.me/+channel")

	statusEngine := engine.New(nil)
	decision, err := statusEngine.PersistedDecision(context.Background(), engine.Store{
		Users:         store.NewUsers(db),
		Subscriptions: store.NewSubscriptions(db),
		Whitelist:     store.NewWhitelist(db),
	}, 123)
	if err != nil {
		t.Fatalf("PersistedDecision: %v", err)
	}

	admissionHandler := admission.New(admission.Deps{
		Users:         store.NewUsers(db),
		Subscriptions: store.NewSubscriptions(db),
		Grants:        store.NewGrants(db),
		Invites:       store.NewInvites(db),
		Outbox:        store.NewOutbox(db),
		Audit:         store.NewAudit(db),
		Alerts:        store.NewAlerts(db),
		Whitelist:     store.NewWhitelist(db),
		Revocations:   store.NewRevocations(db),
		StatusEngine:  statusEngine,
	}, admission.Config{
		InviteMode: domain.InviteSharedJoinRequest,
		Resources: []admission.ResourceConfig{
			{Resource: domain.ResourceChat, ChatID: -1001},
			{Resource: domain.ResourceChannel, ChatID: -1002},
		},
	})

	if err := admissionHandler.HandleAccessRequest(context.Background(),
		admission.AccessRequest{
			User: domain.User{TGID: 123, FirstName: "Subscriber"},
			Snapshot: &engine.Snapshot{
				TGID:     123,
				Decision: decision,
			},
			Trigger: "start",
		}); err != nil {
		t.Fatalf("HandleAccessRequest: %v", err)
	}

	var pendingGrants, dmActions int
	if err := db.QueryRowContext(context.Background(), `
		SELECT count(*)
		FROM access_grants
		WHERE tg_id = 123 AND state = 'pending'`,
	).Scan(&pendingGrants); err != nil {
		t.Fatalf("count pending grants: %v", err)
	}

	if err := db.QueryRowContext(context.Background(), `
		SELECT count(*)
		FROM access_actions
		WHERE action_type = 'send_dm'`,
	).Scan(&dmActions); err != nil {
		t.Fatalf("count dm actions: %v", err)
	}

	if pendingGrants != 2 || dmActions != 1 {
		t.Fatalf("pending grants=%d dm actions=%d, want start grant links",
			pendingGrants, dmActions)
	}
}

func postTribute(
	t *testing.T,
	handler *TributeHandler,
	raw []byte,
	signature string,
) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost, "/webhooks/tribute",
		strings.NewReader(string(raw)))
	req.Header.Set(tributeSignatureHeader, signature)
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	return resp
}

func signTribute(raw []byte) string {
	mac := hmac.New(sha256.New, []byte(tributeTestKey))
	_, _ = mac.Write(raw)

	return hex.EncodeToString(mac.Sum(nil))
}

func tributeSubscriptionPayload(name, createdAt, expiresAt string) []byte {
	return []byte(`{
		"name":"` + name + `",
		"created_at":"` + createdAt + `",
		"sent_at":"` + createdAt + `",
		"payload":{
			"subscription_name":"Gold",
			"subscription_id":1644,
			"period_id":1547,
			"telegram_user_id":123,
			"telegram_username":"durov",
			"expires_at":"` + expiresAt + `"
		}
	}`)
}

func seedSharedInvite(
	t *testing.T,
	db *sql.DB,
	resource domain.Resource,
	url string,
) {
	t.Helper()

	if _, err := store.NewInvites(db).SaveCreated(context.Background(),
		store.InviteLinkInput{
			Resource:           resource,
			Mode:               domain.InviteSharedJoinRequest,
			InviteLink:         url,
			InviteLinkHash:     "hash:" + string(resource),
			CreatesJoinRequest: true,
		}); err != nil {
		t.Fatalf("seed shared invite: %v", err)
	}
}

func newWebhookTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}

	t.Cleanup(func() { _ = db.Close() })

	provider, err := goose.NewProvider(
		goose.DialectSQLite3, db, os.DirFS(webhookMigrationsDir(t)))
	if err != nil {
		t.Fatalf("new goose provider: %v", err)
	}

	if _, err := provider.Up(context.Background()); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}

	return db
}

func webhookMigrationsDir(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}

	return filepath.Join(filepath.Dir(file), "..", "..", "migrations")
}

package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justskiv/gatekeeper/internal/admission"
	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/engine"
	"github.com/justskiv/gatekeeper/internal/store"
	"github.com/justskiv/gatekeeper/internal/testutil"
)

const tributeTestKey = "tribute-api-key"

func TestTributeWebhookValidSignatureUsesRawBodyAndRedactsPayload(t *testing.T) {
	db := testutil.NewDB(t)
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
	require.Equalf(t, http.StatusOK, resp.Code, "body=%s", resp.Body.String())

	sub, ok, err := store.NewSubscriptions(db).GetActive(
		context.Background(), 123, domain.PlatformTribute)
	require.NoError(t, err, "GetActive")
	require.True(t, ok, "subscription must be active")

	assert.Equal(t, "1644", sub.ExternalID)
	assert.Equal(t, "1547", sub.PeriodID)
	assert.Equal(t, "Gold", sub.Tier)
	assert.Equal(t, "webhook", sub.LastSignal)
	assert.NotNil(t, sub.ExpiresAt)

	var payload string
	require.NoError(t, db.QueryRowContext(context.Background(),
		`SELECT payload_json FROM tribute_events`).Scan(&payload), "read payload")

	for _, forbidden := range []string{"durov@example.com", "startapp=secret"} {
		assert.NotContainsf(t, payload, forbidden, "payload must redact %q", forbidden)
	}
}

func TestTributeWebhookInvalidSignatureIsAuditedWithoutDomainChanges(t *testing.T) {
	db := testutil.NewDB(t)
	handler := &TributeHandler{
		DB:     db,
		APIKey: tributeTestKey,
		Engine: engine.New(nil),
	}

	raw := tributeSubscriptionPayload("new_subscription",
		"2026-06-02T10:00:00Z", "2026-07-02T10:00:00Z")
	resp := postTribute(t, handler, raw, "bad")
	require.Equal(t, http.StatusUnauthorized, resp.Code, "invalid signature status")

	var eventCount, invalidSignatures, rejectedAudit int
	require.NoError(t, db.QueryRowContext(context.Background(), `
		SELECT count(*), sum(CASE WHEN signature_valid = 0 THEN 1 ELSE 0 END)
		FROM tribute_events
		WHERE status = 'failed'`,
	).Scan(&eventCount, &invalidSignatures), "count failed tribute events")

	assert.Equal(t, 1, eventCount, "one failed event")
	assert.Equal(t, 1, invalidSignatures, "one invalid signature")

	require.NoError(t, db.QueryRowContext(context.Background(), `
		SELECT count(*)
		FROM audit_log
		WHERE kind = 'webhook_rejected'`,
	).Scan(&rejectedAudit), "count rejected audit")
	assert.Equal(t, 1, rejectedAudit, "one webhook_rejected audit entry")

	_, ok, err := store.NewSubscriptions(db).GetActive(
		context.Background(), 123, domain.PlatformTribute)
	require.NoError(t, err, "GetActive")
	assert.False(t, ok, "no active subscription must be created")
}

func TestTributeWebhookInvalidSignatureDoesNotOverwriteExistingEvent(t *testing.T) {
	db := testutil.NewDB(t)
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
	require.Equalf(t, http.StatusOK, resp.Code, "valid body=%s", resp.Body.String())

	resp = postTribute(t, handler, raw, "bad")
	require.Equal(t, http.StatusUnauthorized, resp.Code, "invalid retry status")

	var status string
	require.NoError(t, db.QueryRowContext(context.Background(), `
		SELECT status
		FROM tribute_events
		WHERE dedup_key = ?`,
		rawDedupKey(raw)).Scan(&status), "read tribute event status")
	assert.Equal(t, string(store.TributeEventProcessed), status)

	var rejectedAudit int
	require.NoError(t, db.QueryRowContext(context.Background(), `
		SELECT count(*)
		FROM audit_log
		WHERE kind = 'webhook_rejected'`,
	).Scan(&rejectedAudit), "count rejected audit")
	assert.Equal(t, 0, rejectedAudit, "duplicate must not add a rejected audit entry")
}

func TestTributeWebhookDedupIgnoredFailedAndOrdering(t *testing.T) {
	db := testutil.NewDB(t)
	handler := &TributeHandler{
		DB:     db,
		APIKey: tributeTestKey,
		Engine: engine.New(nil),
	}

	initial := tributeSubscriptionPayload("new_subscription",
		"2026-06-02T10:00:00Z", "2026-07-02T10:00:00Z")
	for range 2 {
		resp := postTribute(t, handler, initial, signTribute(initial))
		require.Equal(t, http.StatusOK, resp.Code, "initial status")
	}

	var events, activated int
	require.NoError(t, db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM tribute_events`).Scan(&events), "count events")

	require.NoError(t, db.QueryRowContext(context.Background(), `
		SELECT count(*)
		FROM audit_log
		WHERE kind = 'subscription_activated'`,
	).Scan(&activated), "count activations")

	assert.Equal(t, 1, events, "duplicate webhook must dedupe to one event")
	assert.Equal(t, 1, activated, "duplicate webhook must activate once")

	ignored := []byte(`{
		"name":"physical_order_created",
		"created_at":"2026-06-02T11:00:00Z",
		"sent_at":"2026-06-02T11:00:01Z",
		"payload":{"order_id":1}
	}`)
	resp := postTribute(t, handler, ignored, signTribute(ignored))
	require.Equal(t, http.StatusOK, resp.Code, "ignored status")

	var ignoredRows int
	require.NoError(t, db.QueryRowContext(context.Background(), `
		SELECT count(*)
		FROM tribute_events
		WHERE status = 'ignored'`,
	).Scan(&ignoredRows), "count ignored")
	assert.Equal(t, 1, ignoredRows, "one ignored event")

	malformed := []byte(`{
		"name":"new_subscription",
		"created_at":"2026-06-02T12:00:00Z",
		"sent_at":"2026-06-02T12:00:01Z",
		"payload":{"subscription_id":1644,"period_id":2000}
	}`)
	resp = postTribute(t, handler, malformed, signTribute(malformed))
	require.Equal(t, http.StatusBadRequest, resp.Code, "malformed status")

	renewed := tributeSubscriptionPayload("renewed_subscription",
		"2026-06-03T10:00:00Z", "2026-08-02T10:00:00Z")
	resp = postTribute(t, handler, renewed, signTribute(renewed))
	require.Equal(t, http.StatusOK, resp.Code, "renewed status")

	stale := tributeSubscriptionPayload("new_subscription",
		"2026-06-01T10:00:00Z", "2026-06-15T10:00:00Z")
	resp = postTribute(t, handler, stale, signTribute(stale))
	require.Equal(t, http.StatusOK, resp.Code, "stale status")

	sub, ok, err := store.NewSubscriptions(db).GetActive(
		context.Background(), 123, domain.PlatformTribute)
	require.NoError(t, err, "GetActive")
	require.True(t, ok, "subscription must be active")
	require.NotNil(t, sub.ExpiresAt)
	assert.Equal(t, "2026-08-02", sub.ExpiresAt.Format("2006-01-02"),
		"expires_at must track the renewed date")
}

func TestTributeWebhookSubsecondOrderingKeepsNewestEvent(t *testing.T) {
	db := testutil.NewDB(t)
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
		require.Equalf(t, http.StatusOK, resp.Code, "body=%s", resp.Body.String())
	}

	sub, ok, err := store.NewSubscriptions(db).GetActive(
		context.Background(), 123, domain.PlatformTribute)
	require.NoError(t, err, "GetActive")
	require.True(t, ok, "subscription must be active")
	require.NotNil(t, sub.ExpiresAt)
	require.NotNil(t, sub.LastEventAt)

	assert.Equal(t, "2026-08-02", sub.ExpiresAt.Format("2006-01-02"),
		"expires_at must track the newest event expiry")
	assert.Equal(t, 900_000_000, sub.LastEventAt.Nanosecond(),
		"last_event_at must keep the subsecond precision of the newest event")
}

func TestTributePayloadInt64RejectsTrailingText(t *testing.T) {
	envelope := tributeEnvelope{
		Payload: map[string]any{"telegram_user_id": "123abc"},
	}

	_, ok := envelope.payloadInt64("telegram_user_id")
	assert.False(t, ok, "payloadInt64 must reject trailing text")
}

func TestTributeWebhookCancelDefaultAndImmediateOverride(t *testing.T) {
	defaultDB := testutil.NewDB(t)
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
		require.Equal(t, http.StatusOK, resp.Code, "default cancel status")
	}

	_, ok, err := store.NewSubscriptions(defaultDB).GetActive(
		context.Background(), 123, domain.PlatformTribute)
	require.NoError(t, err, "GetActive default")
	assert.True(t, ok, "default cancel must keep access active")

	immediateDB := testutil.NewDB(t)
	immediateHandler := &TributeHandler{
		DB:              immediateDB,
		APIKey:          tributeTestKey,
		Engine:          engine.New(nil),
		CancelImmediate: true,
	}

	for _, raw := range [][]byte{create, cancel} {
		resp := postTribute(t, immediateHandler, raw, signTribute(raw))
		require.Equal(t, http.StatusOK, resp.Code, "immediate cancel status")
	}

	_, ok, err = store.NewSubscriptions(immediateDB).GetActive(
		context.Background(), 123, domain.PlatformTribute)
	require.NoError(t, err, "GetActive immediate")
	assert.False(t, ok, "immediate cancel must expire access")
}

func TestTributeWebhookLedgerAllowsStartGrantAccess(t *testing.T) {
	db := testutil.NewDB(t)
	handler := &TributeHandler{
		DB:     db,
		APIKey: tributeTestKey,
		Engine: engine.New(nil),
	}

	raw := tributeSubscriptionPayload("new_subscription",
		"2026-06-02T10:00:00Z", "2026-07-02T10:00:00Z")
	resp := postTribute(t, handler, raw, signTribute(raw))
	require.Equal(t, http.StatusOK, resp.Code, "webhook status")

	seedSharedInvite(t, db, domain.ResourceChat, "https://t.me/+chat")
	seedSharedInvite(t, db, domain.ResourceChannel, "https://t.me/+channel")

	statusEngine := engine.New(nil)
	decision, err := statusEngine.PersistedDecision(context.Background(), engine.Store{
		Users:         store.NewUsers(db),
		Subscriptions: store.NewSubscriptions(db),
		Whitelist:     store.NewWhitelist(db),
	}, 123)
	require.NoError(t, err, "PersistedDecision")

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

	require.NoError(t, admissionHandler.HandleAccessRequest(context.Background(),
		admission.AccessRequest{
			User: domain.User{TGID: 123, FirstName: "Subscriber"},
			Snapshot: &engine.Snapshot{
				TGID:     123,
				Decision: decision,
			},
			Trigger: "start",
		}), "HandleAccessRequest")

	var pendingGrants, dmActions int
	require.NoError(t, db.QueryRowContext(context.Background(), `
		SELECT count(*)
		FROM access_grants
		WHERE tg_id = 123 AND state = 'pending'`,
	).Scan(&pendingGrants), "count pending grants")

	require.NoError(t, db.QueryRowContext(context.Background(), `
		SELECT count(*)
		FROM access_actions
		WHERE action_type = 'send_dm'`,
	).Scan(&dmActions), "count dm actions")

	assert.Equal(t, 2, pendingGrants, "start must grant pending chat and channel")
	assert.Equal(t, 1, dmActions, "start must enqueue one DM with links")
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

	_, err := store.NewInvites(db).SaveCreated(context.Background(),
		store.InviteLinkInput{
			Resource:           resource,
			Mode:               domain.InviteSharedJoinRequest,
			InviteLink:         url,
			InviteLinkHash:     "hash:" + string(resource),
			CreatesJoinRequest: true,
		})
	require.NoError(t, err, "seed shared invite")
}

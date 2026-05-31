package invite

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/pressly/goose/v3"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/store"
	"github.com/justskiv/gatekeeper/internal/telegram"
)

type fakeLinkManager struct {
	calls  int
	params []telegram.CreateChatInviteLinkParams
}

func (m *fakeLinkManager) CreateChatInviteLink(
	_ context.Context,
	params telegram.CreateChatInviteLinkParams,
) (*models.ChatInviteLink, error) {
	m.calls++
	m.params = append(m.params, params)

	return &models.ChatInviteLink{
		InviteLink:         fmt.Sprintf("https://t.me/+invite-%d", m.calls),
		CreatesJoinRequest: params.CreatesJoinRequest,
		Name:               params.Name,
	}, nil
}

func (m *fakeLinkManager) RevokeChatInviteLink(
	context.Context,
	int64,
	string,
) (*models.ChatInviteLink, error) {
	return &models.ChatInviteLink{IsRevoked: true}, nil
}

func TestSharedInviteIsReused(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	manager := &fakeLinkManager{}
	service := New(manager, store.NewInvites(db), Config{
		Mode:       domain.InviteSharedJoinRequest,
		ClubChatID: -1001,
	})

	first, err := service.Ensure(ctx, EnsureRequest{Resource: domain.ResourceChat})
	if err != nil {
		t.Fatalf("ensure first: %v", err)
	}

	second, err := service.Ensure(ctx, EnsureRequest{Resource: domain.ResourceChat})
	if err != nil {
		t.Fatalf("ensure second: %v", err)
	}

	if first.ID != second.ID {
		t.Fatalf("ids = %d/%d, want shared reuse", first.ID, second.ID)
	}

	if manager.calls != 1 {
		t.Fatalf("create calls = %d, want 1", manager.calls)
	}

	if !manager.params[0].CreatesJoinRequest {
		t.Fatal("shared link was not created as join-request link")
	}
}

func TestSharedModeCreatesChatAndChannelActiveLinks(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	manager := &fakeLinkManager{}
	service := New(manager, store.NewInvites(db), Config{
		Mode:          domain.InviteSharedJoinRequest,
		ClubChatID:    -1001,
		ClubChannelID: -1002,
	})

	for _, resource := range []domain.Resource{
		domain.ResourceChat,
		domain.ResourceChannel,
	} {
		if _, err := service.Ensure(ctx, EnsureRequest{
			Resource: resource,
		}); err != nil {
			t.Fatalf("ensure %s: %v", resource, err)
		}
	}

	var links int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM invite_links
		WHERE status = 'created'
		  AND creates_join_request = 1
		  AND mode = 'shared_join_request'`,
	).Scan(&links); err != nil {
		t.Fatalf("count shared links: %v", err)
	}

	if links != 2 {
		t.Fatalf("shared links = %d, want chat and channel", links)
	}
}

func TestTelegramInviteNamesFitBotAPILimit(t *testing.T) {
	nonce := "0123456789abcdef"

	for _, mode := range []domain.InviteMode{
		domain.InviteSharedJoinRequest,
		domain.InvitePersonalJoinRequest,
		domain.InviteDirect,
	} {
		for _, resource := range []domain.Resource{
			domain.ResourceChat,
			domain.ResourceChannel,
		} {
			name := telegramName(mode, resource, nonce)
			if len(name) > maxTelegramInviteNameLen {
				t.Fatalf("telegram name %q length = %d, want <= %d",
					name, len(name), maxTelegramInviteNameLen)
			}
		}
	}
}

func TestPersonalInviteIsReusedUntilTTL(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tgID := int64(3001)
	now := time.Unix(1_700_000_000, 0)

	if err := store.NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}); err != nil {
		t.Fatalf("upsert user: %v", err)
	}

	manager := &fakeLinkManager{}
	service := New(manager, store.NewInvites(db), Config{
		Mode:       domain.InvitePersonalJoinRequest,
		TTL:        time.Hour,
		ClubChatID: -1001,
	}, WithClock(func() time.Time { return now }), WithNonce(func() (string, error) {
		return "nonce", nil
	}))

	first, err := service.Ensure(ctx, EnsureRequest{
		TGID:     &tgID,
		Resource: domain.ResourceChat,
	})
	if err != nil {
		t.Fatalf("ensure first: %v", err)
	}

	second, err := service.Ensure(ctx, EnsureRequest{
		TGID:     &tgID,
		Resource: domain.ResourceChat,
	})
	if err != nil {
		t.Fatalf("ensure second: %v", err)
	}

	if first.ID != second.ID || manager.calls != 1 {
		t.Fatalf("reuse = ids %d/%d calls %d, want same id and one call",
			first.ID, second.ID, manager.calls)
	}

	if second.ExpiresAt == nil || !second.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("expires_at = %v, want %v", second.ExpiresAt, now.Add(time.Hour))
	}
}

func TestDirectInviteTTLGuardAndParams(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tgID := int64(3002)

	if err := store.NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}); err != nil {
		t.Fatalf("upsert user: %v", err)
	}

	manager := &fakeLinkManager{}
	guarded := New(manager, store.NewInvites(db), Config{
		Mode:       domain.InviteDirect,
		TTL:        2 * time.Hour,
		ClubChatID: -1001,
	})

	if _, err := guarded.Ensure(ctx, EnsureRequest{
		TGID:     &tgID,
		Resource: domain.ResourceChat,
	}); err == nil {
		t.Fatal("direct invite with ttl > 1h succeeded, want error")
	}

	allowed := New(manager, store.NewInvites(db), Config{
		Mode:       domain.InviteDirect,
		TTL:        time.Hour,
		ClubChatID: -1001,
	})

	if _, err := allowed.Ensure(ctx, EnsureRequest{
		TGID:     &tgID,
		Resource: domain.ResourceChat,
	}); err != nil {
		t.Fatalf("direct ensure: %v", err)
	}

	got := manager.params[len(manager.params)-1]
	if got.CreatesJoinRequest || got.MemberLimit != 1 {
		t.Fatalf("direct params = %+v, want no join request and member limit 1", got)
	}
}

func TestInviteServiceLogsHashWithoutFullURL(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	var buf bytes.Buffer

	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	manager := &fakeLinkManager{}
	service := New(manager, store.NewInvites(db), Config{
		Mode:       domain.InviteSharedJoinRequest,
		ClubChatID: -1001,
	}, WithLogger(logger), WithNonce(func() (string, error) {
		return "nonce", nil
	}))

	link, err := service.Ensure(ctx, EnsureRequest{Resource: domain.ResourceChat})
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}

	logLine := buf.String()
	if strings.Contains(logLine, link.InviteLink) ||
		strings.Contains(logLine, "https://t.me/") {
		t.Fatalf("log leaks invite URL: %s", logLine)
	}

	if !strings.Contains(logLine, link.InviteLinkHash) {
		t.Fatalf("log = %s, want invite_link_hash %s", logLine, link.InviteLinkHash)
	}
}

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}

	t.Cleanup(func() { _ = db.Close() })

	provider, err := goose.NewProvider(
		goose.DialectSQLite3, db, os.DirFS(migrationsDir(t)))
	if err != nil {
		t.Fatalf("new goose provider: %v", err)
	}

	if _, err := provider.Up(context.Background()); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}

	return db
}

func migrationsDir(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}

	return filepath.Join(filepath.Dir(file), "..", "..", "migrations")
}

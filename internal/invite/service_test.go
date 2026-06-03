package invite

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/brianvoe/gofakeit/v7"
	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/lib/random"
	"github.com/justskiv/gatekeeper/internal/store"
	"github.com/justskiv/gatekeeper/internal/testutil"
)

type fakeLinkManager struct {
	calls  int
	params []CreateChatInviteLinkParams
}

func (m *fakeLinkManager) CreateChatInviteLink(
	_ context.Context,
	params CreateChatInviteLinkParams,
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
	db := testutil.NewDB(t)
	ctx := context.Background()
	manager := &fakeLinkManager{}
	service := New(manager, store.NewInvites(db), Config{
		Mode:       domain.InviteSharedJoinRequest,
		ClubChatID: -1001,
	})

	first, err := service.Ensure(ctx, EnsureRequest{Resource: domain.ResourceChat})
	require.NoError(t, err, "ensure first")

	second, err := service.Ensure(ctx, EnsureRequest{Resource: domain.ResourceChat})
	require.NoError(t, err, "ensure second")

	assert.Equal(t, first.ID, second.ID, "shared reuse")
	assert.Equal(t, 1, manager.calls, "create calls")
	assert.True(t, manager.params[0].CreatesJoinRequest,
		"shared link must be a join-request link")
}

func TestSharedModeCreatesChatAndChannelActiveLinks(t *testing.T) {
	db := testutil.NewDB(t)
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
		_, err := service.Ensure(ctx, EnsureRequest{Resource: resource})
		require.NoError(t, err, "ensure %s", resource)
	}

	var links int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM invite_links
		WHERE status = 'created'
		  AND creates_join_request = 1
		  AND mode = 'shared_join_request'`,
	).Scan(&links), "count shared links")
	assert.Equal(t, 2, links, "chat and channel links")
}

func TestTelegramInviteNamesFitBotAPILimit(t *testing.T) {
	nonce := gofakeit.LetterN(16)

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
			assert.LessOrEqualf(t, len(name), maxTelegramInviteNameLen,
				"telegram name %q length", name)
		}
	}
}

func TestPersonalInviteIsReusedUntilTTL(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	tgID := random.TGID()
	now := time.Unix(1_700_000_000, 0)

	require.NoError(t, store.NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}),
		"upsert user")

	manager := &fakeLinkManager{}
	service := New(manager, store.NewInvites(db), Config{
		Mode:       domain.InvitePersonalJoinRequest,
		TTL:        time.Hour,
		ClubChatID: -1001,
	}, WithClock(func() time.Time { return now }), WithNonce(func() (string, error) {
		return gofakeit.LetterN(8), nil
	}))

	first, err := service.Ensure(ctx, EnsureRequest{
		TGID:     &tgID,
		Resource: domain.ResourceChat,
	})
	require.NoError(t, err, "ensure first")

	second, err := service.Ensure(ctx, EnsureRequest{
		TGID:     &tgID,
		Resource: domain.ResourceChat,
	})
	require.NoError(t, err, "ensure second")

	assert.Equal(t, first.ID, second.ID, "personal reuse keeps one id")
	assert.Equal(t, 1, manager.calls, "create calls")

	require.NotNil(t, second.ExpiresAt)
	assert.True(t, second.ExpiresAt.Equal(now.Add(time.Hour)),
		"expires_at tracks ttl")
}

func TestDirectInviteTTLGuardAndParams(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	tgID := random.TGID()

	require.NoError(t, store.NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}),
		"upsert user")

	manager := &fakeLinkManager{}
	guarded := New(manager, store.NewInvites(db), Config{
		Mode:       domain.InviteDirect,
		TTL:        2 * time.Hour,
		ClubChatID: -1001,
	})

	_, err := guarded.Ensure(ctx, EnsureRequest{
		TGID:     &tgID,
		Resource: domain.ResourceChat,
	})
	require.Error(t, err, "direct invite with ttl > 1h must fail")

	allowed := New(manager, store.NewInvites(db), Config{
		Mode:       domain.InviteDirect,
		TTL:        time.Hour,
		ClubChatID: -1001,
	})

	_, err = allowed.Ensure(ctx, EnsureRequest{
		TGID:     &tgID,
		Resource: domain.ResourceChat,
	})
	require.NoError(t, err, "direct ensure")

	got := manager.params[len(manager.params)-1]
	assert.False(t, got.CreatesJoinRequest, "direct link must not join-request")
	assert.Equal(t, 1, got.MemberLimit, "direct link member limit")
}

func TestInviteServiceLogsHashWithoutFullURL(t *testing.T) {
	db := testutil.NewDB(t)
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
		return gofakeit.LetterN(8), nil
	}))

	link, err := service.Ensure(ctx, EnsureRequest{Resource: domain.ResourceChat})
	require.NoError(t, err, "ensure")

	logLine := buf.String()
	assert.NotContains(t, logLine, link.InviteLink, "log must not leak invite URL")
	assert.NotContains(t, logLine, "https://t.me/", "log must not leak invite URL")
	assert.Contains(t, logLine, link.InviteLinkHash, "log must record invite_link_hash")
}

func TestResolveJoinRequestSharedMissingLinkFallback(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	service := New(nil, store.NewInvites(db), Config{
		Mode:       domain.InviteSharedJoinRequest,
		ClubChatID: -1001,
	})

	rawLink := "https://t.me/+" + gofakeit.LetterN(12)

	_, err := store.NewInvites(db).SaveCreated(ctx, store.InviteLinkInput{
		Resource:           domain.ResourceChat,
		Mode:               domain.InviteSharedJoinRequest,
		InviteLink:         rawLink,
		InviteLinkHash:     HashInviteLink(rawLink),
		TelegramName:       gofakeit.Username(),
		CreatesJoinRequest: true,
	})
	require.NoError(t, err, "save shared invite")

	result, err := service.ResolveJoinRequest(ctx, ResolveRequest{
		TGID:     random.TGID(),
		Resource: domain.ResourceChat,
		Mode:     domain.InviteSharedJoinRequest,
	})
	require.NoError(t, err, "ResolveJoinRequest")

	require.True(t, result.Accepted(), "missing-link shared request must be accepted")
	assert.Equal(t, ResolveSafeFallback, result.Status)
}

func TestResolveJoinRequestPersonalMisuseMarksAttemptedBy(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	ownerID := random.TGID()
	requesterID := random.TGID()

	users := store.NewUsers(db)
	for _, tgID := range []int64{ownerID, requesterID} {
		require.NoError(t, users.Upsert(ctx, domain.User{TGID: tgID}),
			"upsert user %d", tgID)
	}

	rawLink := "https://t.me/+" + gofakeit.LetterN(12)

	link, err := store.NewInvites(db).SaveCreated(ctx, store.InviteLinkInput{
		TGID:               &ownerID,
		Resource:           domain.ResourceChat,
		Mode:               domain.InvitePersonalJoinRequest,
		InviteLink:         rawLink,
		InviteLinkHash:     HashInviteLink(rawLink),
		TelegramName:       gofakeit.Username(),
		CreatesJoinRequest: true,
	})
	require.NoError(t, err, "save personal invite")

	service := New(nil, store.NewInvites(db), Config{
		Mode:       domain.InvitePersonalJoinRequest,
		ClubChatID: -1001,
	})

	result, err := service.ResolveJoinRequest(ctx, ResolveRequest{
		TGID:       requesterID,
		Resource:   domain.ResourceChat,
		Mode:       domain.InvitePersonalJoinRequest,
		InviteLink: rawLink,
	})
	require.NoError(t, err, "ResolveJoinRequest")

	assert.Equal(t, ResolveUsedByOther, result.Status)
	assert.False(t, result.Accepted(), "personal misuse must not be accepted")

	got, err := store.NewInvites(db).GetByID(ctx, link.ID)
	require.NoError(t, err, "get invite")
	assert.Equal(t, domain.InviteUsedByOther, got.Status)
	require.NotNil(t, got.AttemptedBy, "misuse must record the attempting user")
	assert.Equal(t, requesterID, *got.AttemptedBy)
}

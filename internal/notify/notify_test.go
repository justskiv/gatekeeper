package notify

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/lib/random"
	"github.com/justskiv/gatekeeper/internal/messages"
	"github.com/justskiv/gatekeeper/internal/store"
	"github.com/justskiv/gatekeeper/internal/testutil"
)

type fakeSender struct {
	calls int
	err   error
}

func (s *fakeSender) SendMessage(context.Context, int64, string) error {
	s.calls++

	return s.err
}

type fakeOutbox struct {
	calls int
	input store.AccessActionInput
}

func (o *fakeOutbox) Enqueue(
	_ context.Context,
	input store.AccessActionInput,
) (domain.AccessAction, bool, error) {
	o.calls++
	o.input = input

	return domain.AccessAction{ID: int64(o.calls)}, true, nil
}

type blockedDMError struct{}

func (blockedDMError) Error() string {
	return "blocked"
}

func (blockedDMError) TelegramCategory() string {
	return "dm_blocked"
}

func TestSendDMSkipsKnownBlockedUser(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	tgID := random.TGID()

	users := store.NewUsers(db)
	require.NoError(t, users.Upsert(ctx, domain.User{
		TGID:    tgID,
		DMState: domain.DMBlocked,
	}), "upsert user")

	sender := &fakeSender{}

	require.NoError(t, New(users, sender, nil).SendDM(ctx, tgID, "hello"), "SendDM")
	assert.Equal(t, 0, sender.calls, "send calls")
}

func TestDurableSendDMEnqueuesInsteadOfSending(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	tgID := random.TGID()

	users := store.NewUsers(db)
	require.NoError(t, users.Upsert(ctx, domain.User{
		TGID:    tgID,
		DMState: domain.DMOpen,
	}), "upsert user")

	outbox := &fakeOutbox{}
	require.NoError(t, NewDurable(users, outbox, nil).SendDurableDM(
		ctx, tgID, "hello", "update:1:0",
	), "SendDurableDM")

	assert.Equal(t, 1, outbox.calls, "enqueue calls")
	assert.Equal(t, domain.ActionSendDM, outbox.input.Type)
	require.NotNil(t, outbox.input.TGID)
	assert.Equal(t, tgID, *outbox.input.TGID)
}

func TestDurableFormattedDMEnqueuesParseMode(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	tgID := random.TGID()

	users := store.NewUsers(db)
	require.NoError(t, users.Upsert(ctx, domain.User{
		TGID:    tgID,
		DMState: domain.DMOpen,
	}), "upsert user")

	outbox := &fakeOutbox{}
	require.NoError(t, NewDurable(users, outbox, nil).SendFormattedDurableDM(
		ctx, tgID, "<b>hello</b>", messages.ParseModeHTML, "update:1:1",
	), "SendFormattedDurableDM")

	var payload struct {
		Text      string `json:"text"`
		ParseMode string `json:"parse_mode"`
	}
	require.NoError(t, json.Unmarshal(outbox.input.PayloadJSON, &payload),
		"decode payload")

	assert.Equal(t, "<b>hello</b>", payload.Text)
	assert.Equal(t, messages.ParseModeHTML, payload.ParseMode)
}

// TestAlertOwnerDMLinksAlertAndDedupes covers both effects of the alert-keyed
// marker: the row carries the link resolve-time cancellation needs, and a
// repeated failure for the same alert reuses one idempotency key instead of
// queueing a second copy of the same message.
func TestAlertOwnerDMLinksAlertAndDedupes(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	ownerID := random.TGID()
	alertID := int64(7)

	users := store.NewUsers(db)
	require.NoError(t, users.Upsert(ctx, domain.User{
		TGID:    ownerID,
		DMState: domain.DMOpen,
	}), "upsert owner")

	outbox := &fakeOutbox{}
	notifier := NewDurable(users, outbox, nil)

	require.NoError(t, notifier.SendFormattedAlertOwners(
		ctx, []int64{ownerID}, alertID, "<b>rights lost</b>",
	), "first SendFormattedAlertOwners")

	require.NotNil(t, outbox.input.AlertID, "the row must carry the alert link")
	assert.Equal(t, alertID, *outbox.input.AlertID)

	firstKey := outbox.input.IdempotencyKey

	require.NoError(t, notifier.SendFormattedAlertOwners(
		ctx, []int64{ownerID}, alertID, "<b>rights lost</b>",
	), "second SendFormattedAlertOwners")

	assert.Equal(t, firstKey, outbox.input.IdempotencyKey,
		"a repeated failure for the same alert must reuse the key")
}

// TestOwnerDMWithoutAlertKeepsUniqueMarker guards the other half of dmMarker:
// a message with no dedupe identity of its own must stay unique, or distinct
// notifications would silently collapse into one.
func TestOwnerDMWithoutAlertKeepsUniqueMarker(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	ownerID := random.TGID()

	users := store.NewUsers(db)
	require.NoError(t, users.Upsert(ctx, domain.User{
		TGID:    ownerID,
		DMState: domain.DMOpen,
	}), "upsert owner")

	outbox := &fakeOutbox{}
	notifier := NewDurable(users, outbox, nil)

	require.NoError(t, notifier.SendFormattedOwners(
		ctx, []int64{ownerID}, "first",
	), "first SendFormattedOwners")

	firstKey := outbox.input.IdempotencyKey

	require.NoError(t, notifier.SendFormattedOwners(
		ctx, []int64{ownerID}, "second",
	), "second SendFormattedOwners")

	assert.Nil(t, outbox.input.AlertID, "an unlinked message carries no alert")
	assert.NotEqual(t, firstKey, outbox.input.IdempotencyKey,
		"unlinked owner messages must not collapse into one row")
}

func TestDurableSendDMSkipsKnownBlockedUser(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	tgID := random.TGID()

	users := store.NewUsers(db)
	require.NoError(t, users.Upsert(ctx, domain.User{
		TGID:    tgID,
		DMState: domain.DMBlocked,
	}), "upsert user")

	outbox := &fakeOutbox{}
	require.NoError(t, NewDurable(users, outbox, nil).SendDurableDM(
		ctx, tgID, "hello", "update:1:0",
	), "SendDurableDM")

	assert.Equal(t, 0, outbox.calls, "enqueue calls for blocked user")
}

func TestSendDMMarksBlockedWithoutRetry(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	tgID := random.TGID()

	users := store.NewUsers(db)
	require.NoError(t, users.Upsert(ctx, domain.User{
		TGID:    tgID,
		DMState: domain.DMOpen,
	}), "upsert user")

	sender := &fakeSender{err: blockedDMError{}}

	require.NoError(t, New(users, sender, nil).SendDM(ctx, tgID, "hello"), "SendDM")
	assert.Equal(t, 1, sender.calls, "send calls")

	user, err := users.Get(ctx, tgID)
	require.NoError(t, err, "get user")
	assert.Equal(t, domain.DMBlocked, user.DMState)
}

func TestSendDMReturnsNonBlockedErrors(t *testing.T) {
	db := testutil.NewDB(t)

	err := New(store.NewUsers(db), &fakeSender{err: errors.New("network")}, nil).
		SendDM(context.Background(), random.TGID(), "hello")
	require.Error(t, err, "SendDM must return non-blocked error")
}

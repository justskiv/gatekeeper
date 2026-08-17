package enforcer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/lib/random"
	"github.com/justskiv/gatekeeper/internal/store"
	"github.com/justskiv/gatekeeper/internal/telegram"
	"github.com/justskiv/gatekeeper/internal/testutil"
)

// slowTelegramHardCap bounds a call the enforcer failed to bound itself. It is
// far longer than any lease used here, so it never fires while the execution
// deadline works.
const slowTelegramHardCap = 2 * time.Second

var (
	// errRestartBudget is the failure a worker cannot recover from on its own;
	// the restart-policy tests assert on identity, so it is declared once here.
	errRestartBudget = errors.New("database is wedged")

	// errUncappedTelegramCall is returned when the hard cap fires, i.e. when
	// the enforcer let a Telegram call run without a deadline of its own.
	errUncappedTelegramCall = errors.New(
		"telegram call ran without an execution deadline")
)

// slowTelegram blocks inside SendMessage until the context it was handed is
// done, which is how a wedged Telegram call behaves, and records the deadline
// that context carried. The deadline is the invariant under test: without one,
// the call can still be in flight when another worker reclaims the row.
type slowTelegram struct {
	*fakeTelegram

	mu        sync.Mutex
	sends     int
	deadlines []time.Time
}

func newSlowTelegram() *slowTelegram {
	return &slowTelegram{fakeTelegram: &fakeTelegram{}}
}

func (t *slowTelegram) SendMessage(ctx context.Context, _ int64, _ string) error {
	t.mu.Lock()
	t.sends++

	if deadline, ok := ctx.Deadline(); ok {
		t.deadlines = append(t.deadlines, deadline)
	}

	t.mu.Unlock()

	// The hard cap exists so a missing deadline fails an assertion instead of
	// hanging the suite: a test that only ever proves the fix by deadlocking
	// without it is not a test.
	timer := time.NewTimer(slowTelegramHardCap)
	defer timer.Stop()

	select {
	case <-ctx.Done():
	case <-timer.C:
		return telegram.NormalizeError("sendMessage",
			errUncappedTelegramCall)
	}

	return telegram.NormalizeError("sendMessage",
		fmt.Errorf("telegram never answered: %w", ctx.Err()))
}

func (t *slowTelegram) sendCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.sends
}

func (t *slowTelegram) firstDeadline() (time.Time, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if len(t.deadlines) == 0 {
		return time.Time{}, false
	}

	return t.deadlines[0], true
}

// TestEnforcerExecutionCannotOutliveItsLease covers the half of the
// double-execution problem fencing cannot reach. Fencing picks the winner of
// the terminal write; it cannot un-send a DM. So the execution itself must end
// before the lease does, or the reclaiming worker performs the effect a second
// time while the first call is still open.
func TestEnforcerExecutionCannotOutliveItsLease(t *testing.T) {
	const lease = 200 * time.Millisecond

	leasedUntil := time.Now().Add(lease)
	outbox := &fakeOutbox{action: leasedAction(sendDMAction(4242, 0), leasedUntil)}
	tg := newSlowTelegram()

	enf := newLeaseTestEnforcer(outbox, tg, lease)

	worked, err := enf.runOnce(context.Background())
	require.NoError(t, err,
		"an execution timeout is a failed attempt, not a worker error")
	assert.True(t, worked, "the action was leased and handled")

	deadline, ok := tg.firstDeadline()
	require.True(t, ok, "the Telegram call must run under a deadline")
	assert.True(t, deadline.Before(leasedUntil),
		"execution must end before the lease it runs under expires")

	// The deadline is a failure like any other: it goes through handleFailure,
	// the action is rescheduled, and nothing is treated as a shutdown.
	state := outbox.snapshot()
	assert.NotEmpty(t, state.retryError, "the expiry must be recorded and retried")
	assert.False(t, state.retryRunAt.IsZero(), "retry must be scheduled")
	assert.False(t, state.done, "an unfinished action must not be marked done")
	assert.False(t, state.dead, "one expiry must not kill the action")
	assert.False(t, state.released,
		"the lease is released on shutdown, not on an execution timeout")
}

// TestEnforcerWorkerSurvivesExecutionDeadline is the same guard at the worker
// level, and it is the trap in this change: the execution deadline lives on a
// derived context, and reading that context to decide "are we shutting down"
// would stop the pool on an ordinary slow call — the 2026-08-15 outage again,
// through a new door.
func TestEnforcerWorkerSurvivesExecutionDeadline(t *testing.T) {
	const lease = 60 * time.Millisecond

	outbox := &fakeOutbox{
		action: leasedAction(sendDMAction(4242, 0), time.Now().Add(lease)),
		repeat: true,
	}
	tg := newSlowTelegram()

	enf := newLeaseTestEnforcer(outbox, tg, lease,
		WithWorkers(1),
		WithSleep(func(context.Context, time.Duration) error { return nil }))

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)

	go func() { runErr <- enf.Run(ctx) }()

	require.Eventually(t, func() bool {
		return outbox.snapshot().leaseCount >= 3
	}, 5*time.Second, 5*time.Millisecond,
		"the worker must keep leasing across repeated execution timeouts")

	select {
	case err := <-runErr:
		cancel()
		t.Fatalf("Run returned while the context was live: %v", err)
	default:
	}

	cancel()

	select {
	case err := <-runErr:
		require.NoError(t, err, "cancellation is a clean stop")
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

// TestEnforcerReclaimedActionIsNotExecutedTwice runs the real queue against a
// real pool and counts Telegram calls rather than terminal writes: the row can
// only ever be written once, so counting writes would have passed before this
// change while the subject was receiving the effect twice.
func TestEnforcerReclaimedActionIsNotExecutedTwice(t *testing.T) {
	// A whole second, because access_actions.locked_until is stored with
	// second precision: a sub-second lease would not survive the round trip
	// and the reclaim this test is about would never be scheduled.
	const lease = time.Second

	db := testutil.NewDB(t)
	ctx := context.Background()
	tgID := random.TGID()
	outbox := store.NewOutbox(db)

	require.NoError(t, store.NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}),
		"upsert user")

	_, _, err := outbox.Enqueue(ctx, store.AccessActionInput{
		Type:           domain.ActionSendDM,
		TGID:           &tgID,
		IdempotencyKey: "reclaim-send-dm",
		PayloadJSON:    []byte(`{"text":"hello"}`),
	})
	require.NoError(t, err, "enqueue send_dm")

	tg := newSlowTelegram()
	enf := New(Stores{
		Outbox: outbox,
		Users:  store.NewUsers(db),
		Alerts: store.NewAlerts(db),
	}, tg, &fakeInvites{}, Config{
		Workers:       2,
		LeaseDuration: lease,
		IdleDelay:     5 * time.Millisecond,
		ClubChatID:    -1001,
		ClubChannelID: -1002,
	}, WithRateLimiter(noopLimiter{}))

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	runErr := make(chan error, 1)
	go func() { runErr <- enf.Run(runCtx) }()

	require.Eventually(t, func() bool {
		return tg.sendCount() >= 1
	}, 5*time.Second, 5*time.Millisecond, "the action must be picked up")

	// Three lease windows: long enough for a reclaiming worker to start the
	// second call the old behaviour produced, short enough to stay well inside
	// the retry backoff of the first attempt.
	time.Sleep(3 * lease)

	assert.Equal(t, 1, tg.sendCount(),
		"an action whose execution outran its lease must not be sent twice")

	// The row is back in the queue with exactly one attempt spent, which is
	// what "the first worker gave up before its lease expired" looks like in
	// the database.
	action, err := outbox.GetByIdempotencyKey(ctx, "reclaim-send-dm")
	require.NoError(t, err, "read the action back")
	assert.Equal(t, domain.ActionQueued, action.Status,
		"the timed-out action must return to the queue")
	assert.Equal(t, 1, action.Attempts, "exactly one attempt was spent")

	cancel()
	<-runErr
}

// cancellingTelegram cancels the worker context the instant the Telegram call
// returns: the effect has happened and the process is stopping, which is the
// window in which a confirmed success used to be lost.
type cancellingTelegram struct {
	*fakeTelegram

	cancel context.CancelFunc
}

func (t *cancellingTelegram) SendMessage(context.Context, int64, string) error {
	t.cancel()

	return nil
}

// TestEnforcerFinalizesConfirmedSuccessDespiteCancellation pins the write that
// must not be lost. The DM is gone; if the row stays `running`, the next
// process leases it again and the subject is messaged twice.
func TestEnforcerFinalizesConfirmedSuccessDespiteCancellation(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	tgID := random.TGID()
	outbox := store.NewOutbox(db)

	require.NoError(t, store.NewUsers(db).Upsert(ctx, domain.User{TGID: tgID}),
		"upsert user")

	_, _, err := outbox.Enqueue(ctx, store.AccessActionInput{
		Type:           domain.ActionSendDM,
		TGID:           &tgID,
		IdempotencyKey: "finalize-send-dm",
		PayloadJSON:    []byte(`{"text":"hello"}`),
	})
	require.NoError(t, err, "enqueue send_dm")

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	tg := &cancellingTelegram{fakeTelegram: &fakeTelegram{}, cancel: cancel}
	enf := New(Stores{
		Outbox: outbox,
		Users:  store.NewUsers(db),
		Alerts: store.NewAlerts(db),
	}, tg, &fakeInvites{}, Config{
		ClubChatID:    -1001,
		ClubChannelID: -1002,
	}, WithRateLimiter(noopLimiter{}))

	worked, err := enf.runOnce(runCtx)
	require.NoError(t, err, "a cancelled context must not fail the write")
	assert.True(t, worked)

	action, err := outbox.GetByIdempotencyKey(ctx, "finalize-send-dm")
	require.NoError(t, err, "read the action back")
	assert.Equal(t, domain.ActionDone, action.Status,
		"a delivered message must leave the row terminal, never `running`")
}

// fakeClock is a hand-advanced clock. It is read by the supervisor goroutine
// and advanced by the test, so every access is guarded.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}

// TestEnforcerRestartBudgetRollsWithTheWindow proves the budget is a rate and
// not a lifetime total. Each run here dies immediately — so no single run ever
// survives the window — while the restarts are spaced far wider than the
// window. Under a counter that only resets on a long-lived run, the eleventh
// rare failure of the day killed the process; under a rolling window nothing
// accumulates.
func TestEnforcerRestartBudgetRollsWithTheWindow(t *testing.T) {
	const window = 5 * time.Minute

	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	outbox := &fakeOutbox{
		leaseErr:      errRestartBudget,
		leaseErrTimes: -1,
	}

	enf := newTestEnforcer(outbox, &fakeTelegram{}, &fakeUsers{}, &fakeAlerts{},
		WithWorkers(1),
		WithClock(clock.Now),
		WithSleep(func(context.Context, time.Duration) error {
			// The gap between restarts, not the length of a run: the worker
			// keeps dying at once, but hours apart.
			clock.advance(2 * window)

			return nil
		}),
		WithWorkerRestartPolicy(time.Millisecond, time.Millisecond, window, 3))

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)

	go func() { runErr <- enf.Run(ctx) }()

	require.Eventually(t, func() bool {
		return enf.Health().Restarts >= 10
	}, 5*time.Second, 5*time.Millisecond,
		"restarts spread wider than the window must never exhaust the budget")

	select {
	case err := <-runErr:
		cancel()
		t.Fatalf("Run gave up on a worker that fails once per window: %v", err)
	default:
	}

	cancel()

	select {
	case err := <-runErr:
		require.NoError(t, err, "cancellation is a clean stop")
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

// TestEnforcerRestartBudgetStillEscalatesWithinTheWindow is the other side of
// the same policy: restarts packed into one window must still stop the process
// rather than spin forever.
func TestEnforcerRestartBudgetStillEscalatesWithinTheWindow(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	outbox := &fakeOutbox{
		leaseErr:      errRestartBudget,
		leaseErrTimes: -1,
	}

	enf := newTestEnforcer(outbox, &fakeTelegram{}, &fakeUsers{}, &fakeAlerts{},
		WithWorkers(1),
		WithClock(clock.Now),
		WithSleep(func(context.Context, time.Duration) error {
			clock.advance(time.Second)

			return nil
		}),
		WithWorkerRestartPolicy(
			time.Millisecond, time.Millisecond, 5*time.Minute, 3))

	err := enf.Run(context.Background())
	require.Error(t, err, "a worker that cannot run must escalate")
	assert.ErrorIs(t, err, errRestartBudget,
		"the escalated error must carry the cause")
}

func newLeaseTestEnforcer(
	outbox OutboxStore,
	tg TelegramClient,
	lease time.Duration,
	opts ...Option,
) *Enforcer {
	allOpts := append([]Option{WithRateLimiter(noopLimiter{})}, opts...)

	return New(Stores{
		Outbox: outbox,
		Users:  &fakeUsers{},
		Alerts: &fakeAlerts{},
	}, tg, &fakeInvites{}, Config{
		LeaseDuration: lease,
		IdleDelay:     time.Millisecond,
		ClubChatID:    -1001,
		ClubChannelID: -1002,
	}, allOpts...)
}

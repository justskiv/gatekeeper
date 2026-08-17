package enforcer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/telegram"
)

// clientTimeoutError reproduces the exact error shape that killed the worker
// pool on 2026-08-15: go-telegram/bot wraps the transport failure with %w, and
// net/http's Client.Timeout reports itself as context.DeadlineExceeded.
func clientTimeoutError(method string) error {
	return telegram.NormalizeError(method, fmt.Errorf(
		"error do request for method %s, %w", method, &url.Error{
			Op:  "Post",
			URL: "https://api.telegram.org/bot***/" + method,
			Err: context.DeadlineExceeded,
		}))
}

func leasedAction(action domain.AccessAction, until time.Time) domain.AccessAction {
	action.LockedUntil = &until

	return action
}

// TestClientTimeoutErrorMatchesIncidentShape pins the premise of the whole
// regression: if net/http ever stopped reporting Client.Timeout as a deadline,
// the tests below would silently stop testing the incident.
func TestClientTimeoutErrorMatchesIncidentShape(t *testing.T) {
	err := clientTimeoutError("sendMessage")

	require.ErrorIs(t, err, context.DeadlineExceeded,
		"client timeout must still carry context.DeadlineExceeded")
	assert.True(t, telegram.IsTimeout(err), "must classify as timeout")
	assert.False(t, telegram.IsRateLimited(err), "must not look rate limited")
}

// TestEnforcerRequestTimeoutRetriesInsteadOfOrphaning is the incident
// regression at the action level: a request timeout while the context is live
// is an ordinary retryable failure, not a shutdown signal.
func TestEnforcerRequestTimeoutRetriesInsteadOfOrphaning(t *testing.T) {
	lease := time.Now().Add(2 * time.Minute)
	outbox := &fakeOutbox{action: leasedAction(sendDMAction(4242, 0), lease)}
	tg := &fakeTelegram{sendErr: clientTimeoutError("sendMessage")}

	enf := newTestEnforcer(outbox, tg, &fakeUsers{}, &fakeAlerts{})

	worked, err := enf.runOnce(context.Background())
	require.NoError(t, err, "a request timeout must not surface as a worker error")
	assert.True(t, worked, "the action was leased and handled")

	state := outbox.snapshot()
	assert.NotEmpty(t, state.retryError, "the timeout must be recorded and retried")
	assert.False(t, state.retryRunAt.IsZero(), "retry must be scheduled")
	assert.False(t, state.done, "a timed-out action must not be marked done")
	assert.False(t, state.dead, "one timeout must not kill the action")
	assert.False(t, state.released,
		"the lease is only released on shutdown, not on failure")
}

// TestEnforcerWorkerSurvivesRequestTimeoutWhileContextLive is the same
// regression at the worker level: the pool kept draining instead of exiting.
func TestEnforcerWorkerSurvivesRequestTimeoutWhileContextLive(t *testing.T) {
	lease := time.Now().Add(2 * time.Minute)
	outbox := &fakeOutbox{
		action: leasedAction(sendDMAction(4242, 0), lease),
		repeat: true,
	}
	tg := &fakeTelegram{sendErr: clientTimeoutError("sendMessage")}

	enf := newTestEnforcer(outbox, tg, &fakeUsers{}, &fakeAlerts{},
		WithWorkers(1),
		WithSleep(func(context.Context, time.Duration) error { return nil }))

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)

	go func() { runErr <- enf.Run(ctx) }()

	require.Eventually(t, func() bool {
		return outbox.snapshot().leaseCount >= 3
	}, 2*time.Second, 5*time.Millisecond,
		"worker must keep leasing across repeated timeouts")

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

// TestEnforcerRunReturnsErrorWhenWorkersExitWhileContextLive guards the
// invariant whose breach cost 9.5 hours: a dead pool must never look like a
// clean shutdown.
func TestEnforcerRunReturnsErrorWhenWorkersExitWhileContextLive(t *testing.T) {
	outbox := &fakeOutbox{
		leaseErr:      errors.New("database is wedged"),
		leaseErrTimes: -1,
	}

	enf := newTestEnforcer(outbox, &fakeTelegram{}, &fakeUsers{}, &fakeAlerts{},
		WithWorkers(1),
		WithSleep(func(context.Context, time.Duration) error { return nil }),
		WithWorkerRestartPolicy(time.Millisecond, time.Millisecond, time.Hour, 1))

	err := enf.Run(context.Background())
	require.Error(t, err, "a worker that cannot run must escalate")
	assert.Contains(t, err.Error(), "database is wedged",
		"the escalated error must carry the cause")
}

// TestEnforcerRestartsCrashedWorkerAndLogs covers the recoverable half of the
// policy: a worker that dies once comes back, loudly.
func TestEnforcerRestartsCrashedWorkerAndLogs(t *testing.T) {
	lease := time.Now().Add(2 * time.Minute)
	outbox := &fakeOutbox{
		action:        leasedAction(sendDMAction(4242, 0), lease),
		leaseErr:      errors.New("transient database error"),
		leaseErrTimes: 2,
	}

	logs := &syncBuffer{}
	enf := newTestEnforcer(outbox, &fakeTelegram{}, &fakeUsers{}, &fakeAlerts{},
		WithWorkers(1),
		WithLogger(slog.New(slog.NewTextHandler(logs, nil))),
		WithSleep(func(context.Context, time.Duration) error { return nil }),
		WithWorkerRestartPolicy(time.Millisecond, time.Millisecond, time.Hour, 5))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runErr := make(chan error, 1)
	go func() { runErr <- enf.Run(ctx) }()

	require.Eventually(t, func() bool {
		return outbox.snapshot().done
	}, 2*time.Second, 5*time.Millisecond,
		"the pool must recover and process the action")

	assert.Contains(t, logs.String(), "enforcer worker exited; restarting",
		"every worker exit must be visible in the log")

	cancel()
	<-runErr
}

// TestEnforcerShutdownReleasesLeaseWithoutBurningAttempt checks that stopping
// mid-flight returns the action to the queue instead of stranding it in
// running until the lease expires, and without charging it an attempt.
func TestEnforcerShutdownReleasesLeaseWithoutBurningAttempt(t *testing.T) {
	lease := time.Now().Add(2 * time.Minute)
	outbox := &fakeOutbox{action: leasedAction(sendDMAction(4242, 0), lease)}
	tg := &fakeTelegram{sendErr: context.Canceled}

	enf := newTestEnforcer(outbox, tg, &fakeUsers{}, &fakeAlerts{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	worked, err := enf.runOnce(ctx)
	require.NoError(t, err, "shutdown is not a failure")
	assert.True(t, worked)

	state := outbox.snapshot()
	assert.True(t, state.released, "the lease must be handed back on shutdown")
	assert.Empty(t, state.retryError, "shutdown must not count as a failed attempt")
	assert.False(t, state.dead)
	assert.False(t, state.done)
}

// TestEnforcerLostLeaseDoesNotDoubleWrite covers the fencing contract: an
// action that outlived its lease belongs to whoever reclaimed it.
func TestEnforcerLostLeaseDoesNotDoubleWrite(t *testing.T) {
	lease := time.Now().Add(2 * time.Minute)
	outbox := &fakeOutbox{
		action:    leasedAction(sendDMAction(4242, 0), lease),
		leaseLost: true,
	}

	enf := newTestEnforcer(outbox, &fakeTelegram{}, &fakeUsers{}, &fakeAlerts{})

	worked, err := enf.runOnce(context.Background())
	require.NoError(t, err,
		"a reclaimed lease is an expected race, not a worker error")
	assert.True(t, worked)
	assert.False(t, outbox.snapshot().done, "the result belongs to the new owner")
}

// syncBuffer is a race-free sink for slog output in concurrent tests.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

// TestEnforcerHealthTracksWorkers checks the liveness snapshot the readiness
// probe and metrics are built on.
func TestEnforcerHealthTracksWorkers(t *testing.T) {
	lease := time.Now().Add(2 * time.Minute)
	outbox := &fakeOutbox{
		action: leasedAction(sendDMAction(4242, 0), lease),
		repeat: true,
	}

	enf := newTestEnforcer(outbox, &fakeTelegram{}, &fakeUsers{}, &fakeAlerts{},
		WithWorkers(2),
		WithSleep(func(context.Context, time.Duration) error { return nil }))

	require.True(t, enf.Alive(), "a freshly built pool is not stale")

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)

	go func() { runErr <- enf.Run(ctx) }()

	require.Eventually(t, func() bool {
		return enf.Health().WorkersAlive == 2
	}, 2*time.Second, 5*time.Millisecond, "both workers must report alive")

	health := enf.Health()
	assert.Equal(t, 2, health.WorkersConfigured)
	assert.Zero(t, health.WorkersStale, "a turning loop is never stale")
	assert.Less(t, health.MaxCycleAge, time.Minute)
	assert.True(t, enf.Alive())

	cancel()
	<-runErr

	assert.Zero(t, enf.Health().WorkersAlive, "workers are gone after shutdown")
}

// backdateHeartbeat rewinds one worker's heartbeat by age, simulating a worker
// wedged inside a call it never returns from. Worker IDs are 1-based, matching
// the IDs Run hands to superviseWorker.
func backdateHeartbeat(e *Enforcer, workerID int, age time.Duration) {
	e.heartbeats[workerID-1].Store(e.now().Add(-age).UnixNano())
}

// TestEnforcerAliveDetectsStuckWorker is the point of per-worker heartbeats: a
// single wedged worker must surface even while the other one keeps working.
func TestEnforcerAliveDetectsStuckWorker(t *testing.T) {
	enf := newTestEnforcer(&fakeOutbox{}, &fakeTelegram{}, &fakeUsers{},
		&fakeAlerts{}, WithWorkers(2))

	// Worker 1 keeps its fresh construction heartbeat; worker 2 is wedged
	// inside a call it never returns from.
	backdateHeartbeat(enf, 2, 10*time.Minute)

	health := enf.Health()
	assert.Equal(t, 1, health.WorkersStale, "the wedged worker must be counted")
	assert.Greater(t, health.MaxCycleAge, cycleStaleAfter)
	assert.False(t, enf.Alive(),
		"one stuck worker is enough to fail liveness, even if the other turns")
}

package enforcer

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/engine"
	"github.com/justskiv/gatekeeper/internal/invite"
	"github.com/justskiv/gatekeeper/internal/messages"
	"github.com/justskiv/gatekeeper/internal/source"
	"github.com/justskiv/gatekeeper/internal/store"
	"github.com/justskiv/gatekeeper/internal/telegram"
)

const (
	defaultWorkers       = 2
	defaultLeaseDuration = 2 * time.Minute
	defaultIdleDelay     = 250 * time.Millisecond
	defaultRetryBackoff  = 5 * time.Second

	// leaseReleaseTimeout bounds the detached write that returns an in-flight
	// action to the queue while the process is already shutting down.
	leaseReleaseTimeout = 2 * time.Second

	// terminalWriteTimeout bounds the detached write that records the outcome
	// of an effect Telegram has already performed. It is more generous than
	// leaseReleaseTimeout because it has no fallback: a released lease that
	// fails to write simply expires and the action is picked up again, while a
	// confirmed success that fails to write leaves a `running` row that the
	// next process executes a second time.
	terminalWriteTimeout = 5 * time.Second

	// executionBudgetNum/executionBudgetDen is the share of the lease a single
	// execution may spend talking to Telegram; the rest is the margin the
	// terminal write runs in.
	executionBudgetNum = 3
	executionBudgetDen = 4

	// Worker restart policy. A worker that returns is cheap to bring back, so
	// the pool self-heals; a worker that cannot make progress at all escalates
	// instead of spinning, because a busy-loop hides the very class of failure
	// this supervision exists to surface.
	workerRestartBackoff    = time.Second
	workerRestartBackoffMax = 30 * time.Second
	workerRestartWindow     = 5 * time.Minute
	maxWorkerRestarts       = 10

	// cycleStaleAfter is how long a worker may go without completing a loop
	// iteration before it counts as stuck. A single action can legitimately
	// take the rate limiter plus one 60s HTTP timeout, so the threshold sits
	// well above that and far below the hours an unnoticed outage costs.
	cycleStaleAfter = 5 * time.Minute

	// alertCancelReason is recorded on an action retired because the alert it
	// reports on resolved before a worker got to it.
	alertCancelReason = "alert resolved before delivery"
)

// Health is a point-in-time view of the worker pool.
type Health struct {
	// WorkersConfigured is the pool size the enforcer was built with.
	WorkersConfigured int
	// WorkersAlive counts goroutines currently running a worker loop. A hung
	// worker still counts here, which is why it is not the liveness signal.
	WorkersAlive int
	// WorkersStale counts workers whose last completed loop is older than
	// cycleStaleAfter. This catches both a dead worker and a wedged one.
	WorkersStale int
	// MaxCycleAge is the age of the oldest heartbeat across all workers.
	MaxCycleAge time.Duration
	// Restarts is how many times any worker has been restarted.
	Restarts int64
}

// ProcessedResult is the terminal outcome recorded for one executed action.
// The set is deliberately small and closed: it is a metric label, so its
// cardinality must be a property of the code rather than of the traffic.
type ProcessedResult string

const (
	// ResultDone — the action was executed and Telegram accepted it.
	ResultDone ProcessedResult = "done"
	// ResultRetried — the attempt failed transiently and the row went back to
	// the queue. The same action can be counted `retried` several times before
	// it settles; that repetition is the signal, not an accounting error.
	ResultRetried ProcessedResult = "retried"
	// ResultDead — the failure is permanent or the attempt budget ran out. The
	// row is retired and an operator alert is raised.
	ResultDead ProcessedResult = "dead"
	// ResultCancelled — the row was retired undelivered because the alert it
	// reported on resolved first. Nothing was attempted and nothing failed.
	ResultCancelled ProcessedResult = "cancelled"
	// ResultNoop — the row was retired without any effect being needed: either
	// Telegram reports the desired state already holds (a user already banned,
	// a join request already gone), or the subject blocked the bot's DMs so the
	// message can never be delivered. Neither is a failure worth alerting on,
	// and neither deserves a retry, so both would be lies as `done`.
	ResultNoop ProcessedResult = "noop"
)

// ProcessedCount is one throughput counter slot.
type ProcessedCount struct {
	Type   domain.ActionType
	Result ProcessedResult
	Count  int64
}

// processedKey is the bounded (type, result) counter slot.
type processedKey struct {
	actionType domain.ActionType
	result     ProcessedResult
}

// restartPolicy controls how a dead worker is brought back and when giving up
// becomes the better answer.
//
// max and window together are a rate — "at most max restarts per window" — not
// a lifetime allowance. A process that restarts a worker once an hour for a
// week is healthy and must not be killed by the sum of those restarts.
type restartPolicy struct {
	backoff    time.Duration
	backoffMax time.Duration
	window     time.Duration
	max        int
}

// Config controls worker execution and resource mapping.
type Config struct {
	Workers       int
	LeaseDuration time.Duration
	IdleDelay     time.Duration
	ClubChatID    int64
	ClubChannelID int64
}

// Enforcer leases and executes durable Telegram actions.
type Enforcer struct {
	stores  Stores
	tg      TelegramClient
	invites InviteService
	cfg     Config
	logger  *slog.Logger
	limiter RateLimiter
	now     func() time.Time
	sleep   func(context.Context, time.Duration) error
	restart restartPolicy

	// heartbeats holds one unix-nano timestamp per worker, bumped on every
	// completed loop iteration including idle ones: the point is proving the
	// loop turns, not that there was work to do. Per-worker on purpose — a
	// single shared timestamp would stay fresh while one worker is wedged.
	heartbeats   []atomic.Int64
	workersAlive atomic.Int64
	restarts     atomic.Int64

	// processedMu guards processed. A mutex rather than atomics because the
	// slots are keyed, not fixed: the map is written once per settled action
	// and read once per metrics scrape, so contention is not a concern.
	processedMu sync.Mutex
	// processed counts actions by terminal outcome since process start. It is
	// the only honest throughput signal the bot has: the row counts in
	// access_actions shrink when retention reaps them, so a rate over those
	// invents work that never happened.
	processed map[processedKey]int64
}

// Option configures Enforcer.
type Option func(*Enforcer)

// WithLogger sets the worker logger.
func WithLogger(logger *slog.Logger) Option {
	return func(e *Enforcer) {
		if logger != nil {
			e.logger = logger
		}
	}
}

// WithRateLimiter replaces the default Telegram limiter.
func WithRateLimiter(limiter RateLimiter) Option {
	return func(e *Enforcer) {
		if limiter != nil {
			e.limiter = limiter
		}
	}
}

// WithClock replaces time.Now for tests.
func WithClock(now func() time.Time) Option {
	return func(e *Enforcer) {
		e.now = now
	}
}

// WithSleep replaces worker sleep for tests.
func WithSleep(sleep func(context.Context, time.Duration) error) Option {
	return func(e *Enforcer) {
		e.sleep = sleep
	}
}

// WithWorkers overrides the pool size. Tests use it to pin the worker count at
// construction time: `cfg.Workers` and the heartbeat slice are sized together
// inside New, before any worker goroutine exists, so the two can never drift.
func WithWorkers(workers int) Option {
	return func(e *Enforcer) {
		if workers > 0 {
			e.cfg.Workers = workers
		}
	}
}

// WithWorkerRestartPolicy overrides restart backoff and escalation. Tests use
// it to reach the escalation branch without waiting out real backoff.
func WithWorkerRestartPolicy(
	backoff, backoffMax, window time.Duration,
	maxRestarts int,
) Option {
	return func(e *Enforcer) {
		e.restart = restartPolicy{
			backoff:    backoff,
			backoffMax: backoffMax,
			window:     window,
			max:        maxRestarts,
		}
	}
}

// New returns an Enforcer.
func New(
	stores Stores,
	tg TelegramClient,
	invites InviteService,
	cfg Config,
	opts ...Option,
) *Enforcer {
	if cfg.Workers <= 0 {
		cfg.Workers = defaultWorkers
	}

	if cfg.LeaseDuration <= 0 {
		cfg.LeaseDuration = defaultLeaseDuration
	}

	if cfg.IdleDelay <= 0 {
		cfg.IdleDelay = defaultIdleDelay
	}

	e := &Enforcer{
		stores:    stores,
		tg:        tg,
		invites:   invites,
		cfg:       cfg,
		logger:    slog.Default(),
		limiter:   NewRateLimiter(),
		now:       time.Now,
		sleep:     sleepContext,
		processed: map[processedKey]int64{},
		restart: restartPolicy{
			backoff:    workerRestartBackoff,
			backoffMax: workerRestartBackoffMax,
			window:     workerRestartWindow,
			max:        maxWorkerRestarts,
		},
	}
	for _, opt := range opts {
		opt(e)
	}

	// Seed heartbeats so the pool does not read as stuck between construction
	// and the first loop iteration. Sized from e.cfg so an Option that changes
	// the pool size is reflected here rather than silently ignored.
	e.heartbeats = make([]atomic.Int64, e.cfg.Workers)
	for i := range e.heartbeats {
		e.heartbeats[i].Store(e.now().UnixNano())
	}

	return e
}

// Health reports the current state of the worker pool.
func (e *Enforcer) Health() Health {
	now := e.now()
	health := Health{
		WorkersConfigured: len(e.heartbeats),
		WorkersAlive:      int(e.workersAlive.Load()),
		Restarts:          e.restarts.Load(),
	}

	for i := range e.heartbeats {
		age := now.Sub(time.Unix(0, e.heartbeats[i].Load()))
		if age > health.MaxCycleAge {
			health.MaxCycleAge = age
		}

		if age > cycleStaleAfter {
			health.WorkersStale++
		}
	}

	return health
}

// Processed returns throughput counters by action type and terminal outcome,
// ordered by type then result so the exposition is stable between reads.
//
// The counters are in-process and monotonic. They reset on restart, which is
// normal for a counter and is precisely why they are trustworthy where the
// queue-table counts are not: a row that retention deletes never subtracts from
// what already happened.
func (e *Enforcer) Processed() []ProcessedCount {
	e.processedMu.Lock()
	defer e.processedMu.Unlock()

	out := make([]ProcessedCount, 0, len(e.processed))
	for key, count := range e.processed {
		out = append(out, ProcessedCount{
			Type:   key.actionType,
			Result: key.result,
			Count:  count,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}

		return out[i].Result < out[j].Result
	})

	return out
}

func (e *Enforcer) recordProcessed(
	actionType domain.ActionType,
	result ProcessedResult,
) {
	e.processedMu.Lock()
	defer e.processedMu.Unlock()

	e.processed[processedKey{actionType: actionType, result: result}]++
}

// Alive reports whether every worker is still turning its loop. It keys on
// heartbeat staleness rather than on goroutine count, because a worker wedged
// inside a Telegram call is still a live goroutine while doing nothing.
func (e *Enforcer) Alive() bool {
	return e.Health().WorkersStale == 0
}

func (e *Enforcer) beat(workerID int) {
	if idx := workerID - 1; idx >= 0 && idx < len(e.heartbeats) {
		e.heartbeats[idx].Store(e.now().UnixNano())
	}
}

// Run starts worker goroutines and blocks until ctx is cancelled or the worker
// pool cannot be sustained. It never reports success while ctx is still live:
// a pool that quietly stops draining the outbox is an outage, not a shutdown.
func (e *Enforcer) Run(ctx context.Context) error {
	if e.stores.Outbox == nil {
		return errors.New("outbox store is nil")
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, e.cfg.Workers)
	done := make(chan struct{})

	e.logger.Info("enforcer worker pool started",
		slog.Int("workers", e.cfg.Workers))

	var wg sync.WaitGroup

	for i := range e.cfg.Workers {
		workerID := i + 1

		wg.Go(func() {
			if err := e.superviseWorker(runCtx, workerID); err != nil {
				select {
				case errCh <- err:
					cancel()
				default:
				}
			}
		})
	}

	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-ctx.Done():
		cancel()
		<-done
		e.logger.Info("enforcer worker pool stopped")

		return nil
	case err := <-errCh:
		cancel()
		<-done

		return err
	case <-done:
		if ctx.Err() != nil {
			e.logger.Info("enforcer worker pool stopped")

			return nil
		}

		return errors.New(
			"enforcer: all workers exited while the context is live")
	}
}

// superviseWorker keeps one worker running for the lifetime of ctx. Every exit
// is logged with its reason; a worker that cannot be kept alive escalates into
// a returned error so the errgroup can tear the process down.
func (e *Enforcer) superviseWorker(ctx context.Context, workerID int) error {
	backoff := e.restart.backoff

	// exits holds the restart timestamps still inside the policy window. A
	// counter cannot express "N per window": it only ever grows, so rare
	// transient failures accumulate until an hour's worth of them kills a
	// process that was never unhealthy.
	exits := make([]time.Time, 0, e.restart.max+1)

	for {
		startedAt := e.now()

		e.beat(workerID)
		e.workersAlive.Add(1)

		err := e.runWorker(ctx, workerID)

		e.workersAlive.Add(-1)

		if ctx.Err() != nil {
			e.logger.Info("enforcer worker stopped",
				slog.Int("worker_id", workerID),
				slog.String("reason", "context_done"))

			return nil
		}

		if err == nil {
			err = errors.New("worker loop returned without an error")
		}

		exitedAt := e.now()

		// Backoff reset and budget expiry answer different questions and are
		// deliberately decided apart. A run that lasted a whole window proves
		// the worker can stay up, so the next restart starts from the base
		// delay again. The budget asks how *often* restarts happen, so it only
		// drops the exits that have aged out of the window.
		if exitedAt.Sub(startedAt) >= e.restart.window {
			backoff = e.restart.backoff
		}

		exits = append(withinWindow(exits, exitedAt.Add(-e.restart.window)),
			exitedAt)
		e.restarts.Add(1)

		if len(exits) > e.restart.max {
			return fmt.Errorf(
				"enforcer worker %d: giving up after %d restarts within %s: %w",
				workerID, len(exits)-1, e.restart.window, err)
		}

		e.logger.Error("enforcer worker exited; restarting",
			slog.Int("worker_id", workerID),
			slog.Int("restarts_in_window", len(exits)),
			slog.Duration("backoff", backoff),
			slog.Any("error", err))

		if sleepErr := e.sleep(ctx, backoff); sleepErr != nil {
			return nil
		}

		if backoff *= 2; backoff > e.restart.backoffMax {
			backoff = e.restart.backoffMax
		}
	}
}

// withinWindow keeps the timestamps strictly newer than cutoff, preserving
// order. It filters in place: the slice is the supervisor's own and is
// rebuilt on every exit.
func withinWindow(times []time.Time, cutoff time.Time) []time.Time {
	kept := times[:0]

	for _, at := range times {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}

	return kept
}

func (e *Enforcer) runWorker(ctx context.Context, workerID int) error {
	for {
		if ctx.Err() != nil {
			return nil
		}

		ok, err := e.runOnce(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}

			return fmt.Errorf("enforcer worker %d: %w", workerID, err)
		}

		// Bump on every completed iteration, idle ones included: the loop
		// turning is the signal, not whether there was work.
		e.beat(workerID)

		if ok {
			continue
		}

		if err := e.sleep(ctx, e.cfg.IdleDelay); err != nil {
			return nil
		}
	}
}

func (e *Enforcer) runOnce(ctx context.Context) (bool, error) {
	action, ok, err := e.stores.Outbox.LeaseReady(
		ctx, e.now(), e.cfg.LeaseDuration)
	if err != nil || !ok {
		return ok, err
	}

	// Resolve-time cancellation already emptied the queue of this alert's
	// deliveries; this catches the row leased in the moments before that
	// commit. It cannot catch an alert that resolves after this check — that
	// race is inherent and the notification is best-effort by design.
	if e.alertNoLongerOpen(ctx, action) {
		e.logger.Info("cancelling notification for a resolved alert",
			slog.Int64("action_id", action.ID),
			slog.Int64("alert_id", *action.AlertID))

		return true, e.settle(action, ResultCancelled,
			e.stores.Outbox.MarkCancelled(
				ctx, action.ID, leaseOf(action), alertCancelReason))
	}

	err = e.executeWithin(ctx, action)
	if err == nil {
		return true, e.settle(action, ResultDone, e.markDone(ctx, action))
	}

	// Shutdown is decided by the worker context alone — never by the execution
	// context above, whose expiry is an ordinary request timeout, and never by
	// the identity of the error, which is how a client timeout came to look
	// like a shutdown in the first place.
	if ctx.Err() != nil {
		//nolint:contextcheck // The worker context is already cancelled; the
		// release deliberately runs on a detached one.
		return true, e.releaseLease(action)
	}

	result, failErr := e.handleFailure(ctx, action, err)

	return true, e.settle(action, result, failErr)
}

// executeWithin runs one action under a deadline strictly shorter than its
// lease.
//
// Fencing decides which worker writes the result; it cannot un-send a DM or
// un-ban a user. An execution that outlives its lease has the row reclaimed
// underneath it and performed a second time, and the subject sees both effects
// — two messages, two bans. Cutting execution short of the lease is what keeps
// the side effect single.
//
// The derived context never leaves this function. A worker that consulted it to
// decide "are we shutting down" would read an ordinary execution timeout as a
// stop signal, which is precisely the confusion that cost 9.5 hours.
func (e *Enforcer) executeWithin(
	ctx context.Context,
	action domain.AccessAction,
) error {
	execCtx, cancel := context.WithTimeout(ctx, e.executionBudget())
	defer cancel()

	return e.execute(execCtx, action)
}

// executionBudget is how long one action may spend on Telegram. The remainder
// of the lease is the margin the terminal write needs: that write must still
// land inside the lease, or fencing rejects it and the outcome is lost.
//
// At the default lease this is 90s against a 60s HTTP client timeout, so a
// single stalled request always fits and a multi-call action (soft_kick makes
// three) is cut short only when Telegram is answering pathologically slowly.
// Being cut short costs a retry with backoff; not being cut short would cost a
// second ban. Raising cfg.LeaseDuration raises this budget with it.
func (e *Enforcer) executionBudget() time.Duration {
	return e.cfg.LeaseDuration * executionBudgetNum / executionBudgetDen
}

// markDone records a completed action on a context detached from the worker's.
//
// By the time it runs, Telegram has already acted. A cancellation arriving
// between the confirmed side effect and this write would leave the row
// `running`, and the next process would execute it again — most visibly as a
// duplicate send_dm. The write is therefore bounded by its own short timeout
// rather than by the lifetime of the worker context, exactly as releaseLease
// already does for the opposite case.
func (e *Enforcer) markDone(
	ctx context.Context,
	action domain.AccessAction,
) error {
	writeCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx), terminalWriteTimeout)
	defer cancel()

	return e.stores.Outbox.MarkDone(writeCtx, action.ID, leaseOf(action))
}

// settle records the outcome of a terminal transition that actually landed.
//
// The counter is bumped only when the write succeeded: a lost lease means
// another worker owns this action's outcome and will count it there, and
// counting it in both places would double the throughput of every action that
// outlived its lease.
func (e *Enforcer) settle(
	action domain.AccessAction,
	result ProcessedResult,
	err error,
) error {
	if err != nil {
		return e.finish(err, action)
	}

	e.recordProcessed(action.Type, result)

	return nil
}

// alertNoLongerOpen reports whether the action reports on an alert that has
// since resolved. A read failure answers "still open" on purpose: losing a
// notification to a transient database error is a worse outcome than
// delivering one that turns out to be a few seconds stale.
func (e *Enforcer) alertNoLongerOpen(
	ctx context.Context,
	action domain.AccessAction,
) bool {
	if action.AlertID == nil || e.stores.Alerts == nil {
		return false
	}

	open, err := e.stores.Alerts.IsOpen(ctx, *action.AlertID)
	if err != nil {
		e.logger.Warn("failed to read alert state before delivery",
			slog.Int64("action_id", action.ID),
			slog.Int64("alert_id", *action.AlertID),
			slog.Any("error", err))

		return false
	}

	return !open
}

// finish swallows ErrLeaseLost: the action outlived its lease and another
// worker owns the outcome now, so writing it twice is exactly what fencing is
// there to prevent.
func (e *Enforcer) finish(err error, action domain.AccessAction) error {
	if err == nil || !errors.Is(err, store.ErrLeaseLost) {
		return err
	}

	e.logger.Warn("outbox lease lost before the result was written",
		slog.Int64("action_id", action.ID),
		slog.String("action_type", string(action.Type)))

	return nil
}

// releaseLease returns an in-flight action to the queue during shutdown, so the
// next process picks it up at once instead of waiting out locked_until. It runs
// on a detached context because the worker context is already cancelled.
func (e *Enforcer) releaseLease(action domain.AccessAction) error {
	relCtx, cancel := context.WithTimeout(
		context.WithoutCancel(context.Background()), leaseReleaseTimeout)
	defer cancel()

	err := e.stores.Outbox.ReleaseLease(relCtx, action.ID, leaseOf(action))
	if err != nil && !errors.Is(err, store.ErrLeaseLost) {
		// Lease expiry remains the fallback, so a failed release is not fatal.
		e.logger.Warn("failed to release outbox lease on shutdown",
			slog.Int64("action_id", action.ID),
			slog.Any("error", err))
	}

	return nil
}

// leaseOf returns the fencing token the worker received when it leased action.
// A missing token never matches a live row, so terminal transitions fail closed.
func leaseOf(action domain.AccessAction) time.Time {
	if action.LockedUntil == nil {
		return time.Time{}
	}

	return *action.LockedUntil
}

func (e *Enforcer) execute(ctx context.Context, action domain.AccessAction) error {
	switch action.Type {
	case domain.ActionEnsureInvite:
		return e.ensureInvite(ctx, action)
	case domain.ActionSendInvite:
		return e.sendInvite(ctx, action)
	case domain.ActionApproveJoin:
		return e.approveJoin(ctx, action)
	case domain.ActionDeclineJoin:
		return e.declineJoin(ctx, action)
	case domain.ActionSoftKick:
		return e.softKick(ctx, action)
	case domain.ActionHardBan:
		return e.hardBan(ctx, action)
	case domain.ActionUnban:
		return e.unban(ctx, action)
	case domain.ActionSendDM:
		return e.sendDM(ctx, action)
	case domain.ActionEditMessage:
		return e.editMessage(ctx, action)
	case domain.ActionVerifyMember:
		return e.verifyMember(ctx, action)
	case domain.ActionRevokeInvite:
		return e.revokeInvite(ctx, action)
	default:
		return fmt.Errorf("unsupported action type %q", action.Type)
	}
}

func (e *Enforcer) ensureInvite(
	ctx context.Context,
	action domain.AccessAction,
) error {
	resource, err := requiredResource(action)
	if err != nil {
		return err
	}

	chatID, err := e.chatID(resource)
	if err != nil {
		return err
	}

	if err := e.wait(ctx, requestKindDefault, chatID); err != nil {
		return err
	}

	_, err = e.invites.Ensure(ctx, invite.EnsureRequest{
		TGID:     action.TGID,
		Resource: resource,
	})

	return err
}

func (e *Enforcer) sendInvite(
	ctx context.Context,
	action domain.AccessAction,
) error {
	tgID, err := requiredTGID(action)
	if err != nil {
		return err
	}

	resource, err := requiredResource(action)
	if err != nil {
		return err
	}

	chatID, err := e.chatID(resource)
	if err != nil {
		return err
	}

	var payload sendInvitePayload
	if err := decodePayload(action, &payload); err != nil {
		return err
	}

	if err := e.wait(ctx, requestKindDefault, chatID); err != nil {
		return err
	}

	link, err := e.invites.Ensure(ctx, invite.EnsureRequest{
		TGID:     &tgID,
		Resource: resource,
	})
	if err != nil {
		return err
	}

	text := payload.Text
	if text == "" {
		text = link.InviteLink
		payload.ParseMode = ""
		payload.Plain = true
	}

	if err := e.wait(ctx, requestKindMessage, tgID); err != nil {
		return err
	}

	err = e.sendPayloadMessage(ctx, tgID, text, payload.ParseMode, payload.Plain, nil)
	if err != nil {
		return err
	}

	if err := e.invites.MarkSent(ctx, link); err != nil {
		e.logger.Warn("failed to mark invite link sent",
			slog.Int64("action_id", action.ID),
			slog.Int64("invite_link_id", link.ID),
			slog.String("invite_link_hash", link.InviteLinkHash),
			slog.Any("error", err))
	}

	return nil
}

func (e *Enforcer) approveJoin(
	ctx context.Context,
	action domain.AccessAction,
) error {
	tgID, chatID, err := e.tgTarget(action)
	if err != nil {
		return err
	}

	if err := e.wait(ctx, requestKindDefault, chatID); err != nil {
		return err
	}

	if err := e.tg.ApproveChatJoinRequest(ctx, chatID, tgID); err != nil {
		return classifyNoop(string(action.Type), err)
	}

	return nil
}

func (e *Enforcer) declineJoin(
	ctx context.Context,
	action domain.AccessAction,
) error {
	tgID, chatID, err := e.tgTarget(action)
	if err != nil {
		return err
	}

	if err := e.wait(ctx, requestKindDefault, chatID); err != nil {
		return err
	}

	if err := e.tg.DeclineChatJoinRequest(ctx, chatID, tgID); err != nil {
		return classifyNoop(string(action.Type), err)
	}

	return nil
}

func (e *Enforcer) softKick(
	ctx context.Context,
	action domain.AccessAction,
) error {
	tgID, chatID, err := e.tgTarget(action)
	if err != nil {
		return err
	}

	if err := e.wait(ctx, requestKindGetMember, chatID); err != nil {
		return err
	}

	member, err := e.tg.GetChatMember(ctx, chatID, tgID)
	if err != nil {
		return classifyNoop(string(action.Type), err)
	}

	if memberIsPrivileged(member) {
		return expectedNoopError{
			err: fmt.Errorf("soft_kick target %d is %s", tgID, member.Type),
		}
	}

	if err := e.wait(ctx, requestKindDefault, chatID); err != nil {
		return err
	}

	if err := e.tg.BanChatMember(ctx, chatID, tgID); err != nil {
		return classifyNoop(string(action.Type), err)
	}

	if err := e.wait(ctx, requestKindDefault, chatID); err != nil {
		return err
	}

	if err := e.tg.UnbanChatMember(ctx, chatID, tgID, true); err != nil {
		return classifyNoop(string(action.Type), err)
	}

	return nil
}

func (e *Enforcer) hardBan(
	ctx context.Context,
	action domain.AccessAction,
) error {
	tgID, chatID, err := e.tgTarget(action)
	if err != nil {
		return err
	}

	if err := e.wait(ctx, requestKindGetMember, chatID); err != nil {
		return err
	}

	member, err := e.tg.GetChatMember(ctx, chatID, tgID)
	if err != nil {
		return classifyNoop(string(action.Type), err)
	}

	if memberIsPrivileged(member) {
		return expectedNoopError{
			err: fmt.Errorf("hard_ban target %d is %s", tgID, member.Type),
		}
	}

	if err := e.wait(ctx, requestKindDefault, chatID); err != nil {
		return err
	}

	if err := e.tg.BanChatMember(ctx, chatID, tgID); err != nil {
		return classifyNoop(string(action.Type), err)
	}

	return nil
}

func (e *Enforcer) unban(
	ctx context.Context,
	action domain.AccessAction,
) error {
	tgID, chatID, err := e.tgTarget(action)
	if err != nil {
		return err
	}

	if err := e.wait(ctx, requestKindDefault, chatID); err != nil {
		return err
	}

	if err := e.tg.UnbanChatMember(ctx, chatID, tgID, true); err != nil {
		return classifyNoop(string(action.Type), err)
	}

	return nil
}

func (e *Enforcer) sendDM(
	ctx context.Context,
	action domain.AccessAction,
) error {
	tgID, err := requiredTGID(action)
	if err != nil {
		return err
	}

	var payload sendDMPayload
	if err := decodePayload(action, &payload); err != nil {
		return err
	}

	if payload.Text == "" {
		return errors.New("send_dm payload text is required")
	}

	chatID := tgID
	if payload.ChatID != 0 {
		chatID = payload.ChatID
	}

	if err := e.wait(ctx, requestKindMessage, chatID); err != nil {
		return err
	}

	if payload.RetryButton {
		if sender, ok := e.tg.(replyMarkupSender); ok {
			return e.sendPayloadMessage(
				ctx, chatID, payload.Text, payload.ParseMode, payload.Plain,
				retryKeyboardWithSender(sender))
		}
	}

	if len(payload.Buttons) > 0 {
		if sender, ok := e.tg.(replyMarkupSender); ok {
			return e.sendPayloadMessage(
				ctx, chatID, payload.Text, payload.ParseMode, payload.Plain,
				keyboardWithSender(sender, inlineKeyboard(payload.Buttons)))
		}
	}

	return e.sendPayloadMessage(ctx, chatID, payload.Text, payload.ParseMode, payload.Plain, nil)
}

func (e *Enforcer) sendPayloadMessage(
	ctx context.Context,
	chatID int64,
	text string,
	parseMode string,
	plain bool,
	replyMarkup replyMarkupSend,
) error {
	if parseMode == "" || plain {
		if replyMarkup != nil {
			return replyMarkup.SendPlain(ctx, chatID, text)
		}

		return e.tg.SendMessage(ctx, chatID, text)
	}

	if replyMarkup != nil {
		return replyMarkup.SendFormatted(ctx, chatID, text, parseMode)
	}

	return e.tg.SendFormattedMessage(ctx, chatID, text, parseMode)
}

func (e *Enforcer) editMessage(
	ctx context.Context,
	action domain.AccessAction,
) error {
	var payload editMessagePayload
	if err := decodePayload(action, &payload); err != nil {
		return err
	}

	if payload.ChatID == 0 || payload.MessageID == 0 {
		return errors.New("edit_message payload requires chat_id and message_id")
	}

	if payload.Text == "" {
		return errors.New("edit_message payload text is required")
	}

	if err := e.wait(ctx, requestKindMessage, payload.ChatID); err != nil {
		return err
	}

	// A non-retry result (e.g. access granted) clears the keyboard so the
	// "checking" button does not linger. The empty inline keyboard must be a
	// non-nil slice: a nil slice marshals to JSON null, which Telegram rejects
	// with `field "inline_keyboard" must be of type Array`.
	markup := models.ReplyMarkup(models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{},
	})
	if payload.RetryButton {
		markup = retryKeyboard()
	}

	return e.tg.EditMessageText(
		ctx, payload.ChatID, payload.MessageID, payload.Text, markup)
}

func (e *Enforcer) verifyMember(
	ctx context.Context,
	action domain.AccessAction,
) error {
	tgID, err := requiredTGID(action)
	if err != nil {
		return err
	}

	payload := verifyMemberPayload{}
	if err := decodePayload(action, &payload); err != nil {
		return err
	}

	chatID := payload.ChatID
	if chatID == 0 {
		resource, err := requiredResource(action)
		if err != nil {
			return err
		}

		chatID, err = e.chatID(resource)
		if err != nil {
			return err
		}

		payload.Resource = string(resource)
		payload.Kind = "club"
	}

	if err := e.wait(ctx, requestKindGetMember, chatID); err != nil {
		return err
	}

	member, err := e.tg.GetChatMember(ctx, chatID, tgID)
	if err != nil {
		return err
	}

	return e.applyVerifiedMember(ctx, tgID, member, payload)
}

func (e *Enforcer) applyVerifiedMember(
	ctx context.Context,
	tgID int64,
	member *models.ChatMember,
	payload verifyMemberPayload,
) error {
	joined := source.MemberInChat(member)
	observedAt := e.now()

	switch payload.Kind {
	case "source":
		platform := domain.Platform(payload.Platform)
		if platform == "" {
			return errors.New("verify_member source payload platform is required")
		}

		if e.stores.Users != nil {
			if err := e.stores.Users.Upsert(ctx, userFromMember(tgID, member)); err != nil {
				return err
			}
		}

		if e.stores.Subscriptions == nil {
			return nil
		}

		if joined {
			if _, err := e.stores.Subscriptions.UpsertActive(ctx, domain.Subscription{
				TGID:          tgID,
				Platform:      platform,
				Status:        domain.SubActive,
				StartedAt:     observedAt,
				LastSignal:    "reconcile",
				LastCheckedAt: &observedAt,
			}); err != nil {
				return err
			}
		} else if _, err := e.stores.Subscriptions.ExpireActive(
			ctx, tgID, platform, observedAt, "reconcile",
		); err != nil {
			return err
		}

		return e.recompute(ctx, tgID)
	case "club", "":
		resource := domain.Resource(payload.Resource)
		if resource == "" {
			switch payload.ChatID {
			case e.cfg.ClubChatID:
				resource = domain.ResourceChat
			case e.cfg.ClubChannelID:
				resource = domain.ResourceChannel
			}
		}

		if resource == "" {
			return errors.New("verify_member club payload resource is required")
		}

		if e.stores.Users != nil {
			if err := e.stores.Users.Upsert(ctx, userFromMember(tgID, member)); err != nil {
				return err
			}
		}

		if e.stores.Grants == nil {
			return nil
		}

		if joined {
			return e.stores.Grants.MarkJoined(ctx, tgID, resource, "bot")
		}

		_, err := e.stores.Grants.MarkLeftUnlessRevoked(ctx, tgID, resource)

		return err
	default:
		return fmt.Errorf("unknown verify_member kind %q", payload.Kind)
	}
}

func (e *Enforcer) recompute(ctx context.Context, tgID int64) error {
	if e.stores.StatusEngine == nil ||
		e.stores.Users == nil ||
		e.stores.Subscriptions == nil ||
		e.stores.Audit == nil ||
		e.stores.Revocations == nil ||
		e.stores.Whitelist == nil {
		return nil
	}

	users, ok := e.stores.Users.(engine.UserStore)
	if !ok {
		return nil
	}

	subscriptions, ok := e.stores.Subscriptions.(engine.SubscriptionStore)
	if !ok {
		return nil
	}

	grants, ok := e.stores.Grants.(engine.GrantStore)
	if !ok {
		return nil
	}

	outbox, ok := e.stores.Outbox.(engine.OutboxStore)
	if !ok {
		return nil
	}

	_, err := e.stores.StatusEngine.RecomputeAccess(ctx, engine.Store{
		Users:         users,
		Subscriptions: subscriptions,
		Audit:         e.stores.Audit,
		Revocations:   e.stores.Revocations,
		Whitelist:     e.stores.Whitelist,
		Grants:        grants,
		Outbox:        outbox,
		Alerts:        e.stores.Alerts,
		Members: telegram.NewClubMemberChecker(
			e.tg, e.cfg.ClubChatID, e.cfg.ClubChannelID),
		OperatorLog: e.stores.OperatorLog,
	}, tgID)

	return err
}

func userFromMember(tgID int64, member *models.ChatMember) domain.User {
	user := memberUser(member)
	if user == nil {
		return domain.User{TGID: tgID}
	}

	return domain.User{
		TGID:         user.ID,
		Username:     user.Username,
		FirstName:    user.FirstName,
		LastName:     user.LastName,
		LanguageCode: user.LanguageCode,
		IsBot:        user.IsBot,
	}
}

func memberUser(member *models.ChatMember) *models.User {
	if member == nil {
		return nil
	}

	switch member.Type {
	case models.ChatMemberTypeOwner:
		if member.Owner == nil {
			return nil
		}

		return member.Owner.User
	case models.ChatMemberTypeAdministrator:
		if member.Administrator == nil {
			return nil
		}

		return &member.Administrator.User
	case models.ChatMemberTypeMember:
		if member.Member == nil {
			return nil
		}

		return member.Member.User
	case models.ChatMemberTypeRestricted:
		if member.Restricted == nil {
			return nil
		}

		return member.Restricted.User
	case models.ChatMemberTypeLeft:
		if member.Left == nil {
			return nil
		}

		return member.Left.User
	case models.ChatMemberTypeBanned:
		if member.Banned == nil {
			return nil
		}

		return member.Banned.User
	default:
		return nil
	}
}

func (e *Enforcer) revokeInvite(
	ctx context.Context,
	action domain.AccessAction,
) error {
	resource, err := requiredResource(action)
	if err != nil {
		return err
	}

	var payload revokeInvitePayload
	if err := decodePayload(action, &payload); err != nil {
		return err
	}

	if payload.InviteLink == "" {
		return errors.New("revoke_invite payload invite_link is required")
	}

	chatID, err := e.chatID(resource)
	if err != nil {
		return err
	}

	if err := e.wait(ctx, requestKindDefault, chatID); err != nil {
		return err
	}

	err = e.invites.Revoke(ctx, domain.InviteLink{
		ID:         payload.InviteLinkID,
		Resource:   resource,
		InviteLink: payload.InviteLink,
	})
	if err != nil {
		return classifyNoop(string(action.Type), err)
	}

	return nil
}

// handleFailure applies the failure policy and reports which terminal outcome
// it recorded, so the caller can count throughput without re-deriving the
// decision from the error.
func (e *Enforcer) handleFailure(
	ctx context.Context,
	action domain.AccessAction,
	cause error,
) (ProcessedResult, error) {
	var noop expectedNoopError
	if errors.As(cause, &noop) {
		e.logger.Warn("outbox action expected no-op",
			slog.Int64("action_id", action.ID),
			slog.String("action_type", string(action.Type)),
			slog.Any("error", cause))

		// Telegram was called and reported the desired state already holds, so
		// this too is a terminal write behind a completed external effect: it
		// is finalized detached for the same reason as a success.
		return ResultNoop, e.markDone(ctx, action)
	}

	if actionCanBlockDM(action.Type) && telegram.IsDMBlocked(cause) {
		// A 403 on a group/feed target (negative payload chat_id, e.g.
		// EVENT_LOG_CHAT_ID or ADMIN_LOG_CHAT_ID) is a lost-posting-rights
		// failure of the bot, not the subject blocking their DMs. It MUST NOT
		// mark action.TGID dm_state='blocked' and MUST NOT count as delivered:
		// route it to the permanent-failure path so the dead-action alert
		// surfaces the unreachable feed chat instead of losing it silently.
		if isGroupFeedTarget(action) {
			return ResultDead, e.markDead(ctx, action, cause)
		}

		if err := e.markDMBlocked(ctx, action); err != nil {
			return ResultNoop, err
		}

		// The row is retired as `done` because nothing is owed a retry, but the
		// message was never delivered, so the throughput counter says `noop`.
		return ResultNoop, e.markDone(ctx, action)
	}

	// Attempts stores previous failed executions; +1 accounts for this failure.
	if telegram.IsPermanentRights(cause) ||
		telegram.IsForbidden(cause) ||
		action.Attempts+1 >= action.MaxAttempts {
		return ResultDead, e.markDead(ctx, action, cause)
	}

	if retryAfter, ok := telegram.RetryAfter(cause); ok {
		_, err := e.stores.Outbox.Retry(
			ctx, action.ID, leaseOf(action), e.now().Add(retryAfter),
			cause.Error())

		return ResultRetried, err
	}

	wait := e.retryBackoff(action)

	_, err := e.stores.Outbox.Retry(
		ctx, action.ID, leaseOf(action), e.now().Add(wait), cause.Error())

	return ResultRetried, err
}

func actionCanBlockDM(actionType domain.ActionType) bool {
	return actionType == domain.ActionSendDM || actionType == domain.ActionSendInvite
}

// isGroupFeedTarget reports whether the action sends to an explicit group or
// channel target rather than a user's private chat. Private-DM sends carry no
// chat_id or a positive user chat_id (including the user_chat_id admission
// sets on a join-request grant DM); a negative chat_id is a group/supergroup/
// channel such as a configured feed chat. send_invite always targets the
// user's DM, so it is never a group-feed target.
func isGroupFeedTarget(action domain.AccessAction) bool {
	if action.Type != domain.ActionSendDM {
		return false
	}

	var payload sendDMPayload
	if err := decodePayload(action, &payload); err != nil {
		return false
	}

	return payload.ChatID < 0
}

func (e *Enforcer) markDMBlocked(
	ctx context.Context,
	action domain.AccessAction,
) error {
	tgID, err := requiredTGID(action)
	if err != nil {
		return err
	}

	if e.stores.Users == nil {
		return errors.New("users store is nil")
	}

	err = e.stores.Users.SetDMState(ctx, tgID, domain.DMBlocked)
	if err == nil {
		return nil
	}

	if !errors.Is(err, store.ErrNotFound) {
		return err
	}

	return e.stores.Users.Upsert(ctx, domain.User{
		TGID:    tgID,
		DMState: domain.DMBlocked,
	})
}

func (e *Enforcer) markDead(
	ctx context.Context,
	action domain.AccessAction,
	cause error,
) error {
	err := e.stores.Outbox.MarkDead(
		ctx, action.ID, leaseOf(action), cause.Error())
	if err != nil {
		return err
	}

	if e.stores.Alerts == nil {
		return nil
	}

	_, err = e.stores.Alerts.Create(ctx, store.AlertInput{
		Severity: "error",
		Kind:     "outbox_action_dead",
		Title:    "outbox action dead",
		Detail: fmt.Sprintf(
			"action_id=%d action_type=%s error=%s",
			action.ID, action.Type, cause.Error()),
		TGID: action.TGID,
	})

	return err
}

func (e *Enforcer) retryBackoff(action domain.AccessAction) time.Duration {
	step := max(action.Attempts+1, 1)

	backoff := defaultRetryBackoff * time.Duration(1<<min(step-1, 5))
	jitterLimit := big.NewInt(int64(defaultRetryBackoff))

	jitterN, err := cryptorand.Int(cryptorand.Reader, jitterLimit)
	if err != nil {
		return backoff
	}

	jitter := time.Duration(jitterN.Int64())

	return backoff + jitter
}

func (e *Enforcer) tgTarget(action domain.AccessAction) (int64, int64, error) {
	tgID, err := requiredTGID(action)
	if err != nil {
		return 0, 0, err
	}

	resource, err := requiredResource(action)
	if err != nil {
		return 0, 0, err
	}

	chatID, err := e.chatID(resource)
	if err != nil {
		return 0, 0, err
	}

	return tgID, chatID, nil
}

func (e *Enforcer) chatID(resource domain.Resource) (int64, error) {
	switch resource {
	case domain.ResourceChat:
		return e.cfg.ClubChatID, nil
	case domain.ResourceChannel:
		return e.cfg.ClubChannelID, nil
	default:
		return 0, fmt.Errorf("unknown action resource %q", resource)
	}
}

func (e *Enforcer) wait(ctx context.Context, kind requestKind, chatID int64) error {
	if e.limiter == nil {
		return nil
	}

	return e.limiter.Wait(ctx, kind, chatID)
}

func requiredTGID(action domain.AccessAction) (int64, error) {
	if action.TGID == nil {
		return 0, fmt.Errorf("%s action requires tg_id", action.Type)
	}

	return *action.TGID, nil
}

func requiredResource(action domain.AccessAction) (domain.Resource, error) {
	if action.Resource == nil {
		return "", fmt.Errorf("%s action requires resource", action.Type)
	}

	return *action.Resource, nil
}

func decodePayload(action domain.AccessAction, dest any) error {
	payload := action.PayloadJSON
	if len(payload) == 0 {
		payload = []byte(`{}`)
	}

	if err := json.Unmarshal(payload, dest); err != nil {
		return fmt.Errorf("decode %s payload: %w", action.Type, err)
	}

	return nil
}

func classifyNoop(action string, err error) error {
	if err == nil {
		return nil
	}

	if telegram.IsExpectedNoop(action, err) {
		return expectedNoopError{err: err}
	}

	return err
}

func memberIsPrivileged(member *models.ChatMember) bool {
	if member == nil {
		return false
	}

	return member.Type == models.ChatMemberTypeOwner ||
		member.Type == models.ChatMemberTypeAdministrator
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type expectedNoopError struct {
	err error
}

func (e expectedNoopError) Error() string {
	return e.err.Error()
}

func (e expectedNoopError) Unwrap() error {
	return e.err
}

type sendDMPayload struct {
	Text        string `json:"text"`
	ParseMode   string `json:"parse_mode,omitempty"`
	Plain       bool   `json:"plain,omitempty"`
	ChatID      int64  `json:"chat_id,omitempty"`
	RetryButton bool   `json:"retry_button,omitempty"`
	Buttons     [][]struct {
		Text         string `json:"text"`
		CallbackData string `json:"callback_data"`
	} `json:"buttons,omitempty"`
}

type editMessagePayload struct {
	ChatID      int64  `json:"chat_id"`
	MessageID   int    `json:"message_id"`
	Text        string `json:"text"`
	RetryButton bool   `json:"retry_button,omitempty"`
}

type sendInvitePayload struct {
	Text      string `json:"text"`
	ParseMode string `json:"parse_mode,omitempty"`
	Plain     bool   `json:"plain,omitempty"`
}

type revokeInvitePayload struct {
	InviteLinkID int64  `json:"invite_link_id"`
	InviteLink   string `json:"invite_link"`
}

type verifyMemberPayload struct {
	Kind     string `json:"kind,omitempty"` // source|club
	Platform string `json:"platform,omitempty"`
	Resource string `json:"resource,omitempty"`
	ChatID   int64  `json:"chat_id,omitempty"`
}

type replyMarkupSender interface {
	SendMessageWithReplyMarkup(
		ctx context.Context,
		chatID int64,
		text string,
		replyMarkup models.ReplyMarkup,
	) error
	SendFormattedMessageWithReplyMarkup(
		ctx context.Context,
		chatID int64,
		text string,
		parseMode string,
		replyMarkup models.ReplyMarkup,
	) error
}

type replyMarkupSend interface {
	SendPlain(ctx context.Context, chatID int64, text string) error
	SendFormatted(ctx context.Context, chatID int64, text string, parseMode string) error
}

type replyMarkupDelivery struct {
	sender      replyMarkupSender
	replyMarkup models.ReplyMarkup
}

func retryKeyboardWithSender(sender replyMarkupSender) replyMarkupDelivery {
	return keyboardWithSender(sender, retryKeyboard())
}

func keyboardWithSender(
	sender replyMarkupSender,
	replyMarkup models.ReplyMarkup,
) replyMarkupDelivery {
	return replyMarkupDelivery{sender: sender, replyMarkup: replyMarkup}
}

func (d replyMarkupDelivery) SendPlain(
	ctx context.Context,
	chatID int64,
	text string,
) error {
	return d.sender.SendMessageWithReplyMarkup(ctx, chatID, text, d.replyMarkup)
}

func (d replyMarkupDelivery) SendFormatted(
	ctx context.Context,
	chatID int64,
	text string,
	parseMode string,
) error {
	return d.sender.SendFormattedMessageWithReplyMarkup(
		ctx, chatID, text, parseMode, d.replyMarkup)
}

func retryKeyboard() models.InlineKeyboardMarkup {
	return models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{{
			{
				Text:         messages.RetryAccessButtonText,
				CallbackData: messages.RetryAccessCallbackData,
			},
		}},
	}
}

func inlineKeyboard(buttons [][]struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}) models.InlineKeyboardMarkup {
	keyboard := make([][]models.InlineKeyboardButton, 0, len(buttons))
	for _, row := range buttons {
		outRow := make([]models.InlineKeyboardButton, 0, len(row))
		for _, button := range row {
			outRow = append(outRow, models.InlineKeyboardButton{
				Text:         button.Text,
				CallbackData: button.CallbackData,
			})
		}

		if len(outRow) > 0 {
			keyboard = append(keyboard, outRow)
		}
	}

	return models.InlineKeyboardMarkup{InlineKeyboard: keyboard}
}

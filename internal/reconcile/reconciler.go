// Package reconcile contains background consistency checks.
package reconcile

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/engine"
	"github.com/justskiv/gatekeeper/internal/operatorlog"
	"github.com/justskiv/gatekeeper/internal/store"
)

const (
	defaultInterval = time.Hour
	defaultLimit    = 1000

	auditReconcilePass = "reconcile_pass"
)

// SourceChat maps a subscription source to a Telegram chat.
type SourceChat struct {
	Platform domain.Platform
	ChatID   int64
	Enabled  bool
}

// ResourceChat maps a managed club resource to a Telegram chat.
type ResourceChat struct {
	Resource domain.Resource
	ChatID   int64
}

// Config controls reconciliation behavior.
type Config struct {
	Interval        time.Duration
	CleanupInterval time.Duration
	RawRetention    time.Duration
	AuditRetention  time.Duration
	InviteMode      domain.InviteMode
	Sources         []SourceChat
	Resources       []ResourceChat
	OwnerIDs        []int64
	AdminLogChatID  *int64
	Limit           int
}

// InviteMaintainer is the invite-link service surface used by Reconciler.
type InviteMaintainer interface {
	ActiveShared(
		ctx context.Context,
		resource domain.Resource,
	) (domain.InviteLink, bool, error)
}

// Reconciler executes idempotent consistency passes.
type Reconciler struct {
	// db is the connection pool. The reconciler is pool-bound and always owns
	// its own short transactions (via store.WithTx); it must never be nested
	// inside a caller's transaction — that would deadlock the single SQLite
	// connection during the live decide phase.
	db      *sql.DB
	engine  *engine.Engine
	invites InviteMaintainer
	cfg     Config
	logger  *slog.Logger
	now     func() time.Time
	sleep   func(context.Context, time.Duration) error
	health  func(context.Context) error
	members engine.MemberChecker
	opLog   *operatorlog.Writer
}

// Option configures Reconciler.
type Option func(*Reconciler)

// WithClock replaces time.Now for tests.
func WithClock(now func() time.Time) Option {
	return func(r *Reconciler) {
		r.now = now
	}
}

// WithSleep replaces sleep for tests.
func WithSleep(sleep func(context.Context, time.Duration) error) Option {
	return func(r *Reconciler) {
		r.sleep = sleep
	}
}

// WithHealthCheck runs chat health during every pass.
func WithHealthCheck(check func(context.Context) error) Option {
	return func(r *Reconciler) {
		r.health = check
	}
}

// WithMemberChecker attaches live club membership checks for revoke safety.
func WithMemberChecker(checker engine.MemberChecker) Option {
	return func(r *Reconciler) {
		r.members = checker
	}
}

// WithOperatorLog attaches the operator event-log writer so reconcile-driven
// revocations emit access lifecycle events.
func WithOperatorLog(writer *operatorlog.Writer) Option {
	return func(r *Reconciler) {
		r.opLog = writer
	}
}

// New returns a Reconciler. db must be the connection pool (*sql.DB): the
// reconciler owns its own transactions and must not run inside a caller's tx.
func New(
	db *sql.DB,
	statusEngine *engine.Engine,
	invites InviteMaintainer,
	cfg Config,
	logger *slog.Logger,
	opts ...Option,
) *Reconciler {
	if cfg.Interval <= 0 {
		cfg.Interval = defaultInterval
	}

	if cfg.Limit <= 0 {
		cfg.Limit = defaultLimit
	}

	if logger == nil {
		logger = slog.Default()
	}

	r := &Reconciler{
		db:      db,
		engine:  statusEngine,
		invites: invites,
		cfg:     cfg,
		logger:  logger,
		now:     time.Now,
		sleep:   sleepContext,
	}
	for _, opt := range opts {
		opt(r)
	}

	return r
}

// Run executes reconciliation periodically until ctx is cancelled.
func (r *Reconciler) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if _, err := r.RunOnce(ctx); err != nil {
				if ctx.Err() != nil {
					return nil
				}

				r.logger.Warn("reconcile pass failed", slog.Any("error", err))
			}
		}
	}
}

// RunOnce performs one idempotent reconciliation pass.
func (r *Reconciler) RunOnce(ctx context.Context) (Summary, error) {
	summary := Summary{}

	due, err := store.NewRevocations(r.db).ListDue(ctx, r.now(), r.cfg.Limit)
	if err != nil {
		return summary, err
	}

	for _, pending := range due {
		if r.engine == nil {
			continue
		}

		if err := r.revokeDue(ctx, pending); err != nil {
			summary.Failed++

			r.logger.Warn("due revocation failed",
				slog.Int64("tg_id", pending.TGID),
				slog.Any("error", err))

			continue
		}

		summary.DueRevocations++
	}

	marker := r.cycleMarker()

	verifyCount, verifyFailed, err := r.enqueueVerifyCandidates(ctx, marker)
	if err != nil {
		return summary, err
	}

	summary.VerifyActions = verifyCount
	summary.Failed += verifyFailed

	inviteCount, err := r.maintainInvites(ctx, marker)
	if err != nil {
		return summary, err
	}

	summary.InviteActions = inviteCount

	if r.health != nil {
		if err := r.health(ctx); err != nil {
			summary.Failed++

			r.logger.Warn("reconcile health check failed",
				slog.Any("error", err))

			if alertErr := r.createHealthFailureAlert(ctx, err); alertErr != nil {
				r.logger.Warn("failed to create reconcile health alert",
					slog.Any("error", alertErr))
			}
		}
	}

	if err := store.NewMeta(r.db).Set(ctx, "reconcile.last_run_at",
		r.now().UTC().Format(time.RFC3339)); err != nil {
		return summary, err
	}

	if err := store.NewAudit(r.db).Append(ctx, store.AuditEntry{
		Kind:   auditReconcilePass,
		Actor:  "job",
		Detail: summary.String(),
	}); err != nil {
		return summary, err
	}

	return summary, nil
}

func (r *Reconciler) revokeDue(
	ctx context.Context,
	pending domain.PendingRevocation,
) error {
	// Hold the per-user lock across BOTH phases so a concurrent event can't
	// interleave between decide and apply (see engine.WithUserLock).
	return r.engine.WithUserLock(pending.TGID, func() error {
		// DECIDE on the pool: live sources + Telegram protection, NO tx open.
		plan, err := r.engine.RevocationDecision(
			ctx, r.liveStore(), pending.TGID, pending.Reason)
		if err != nil {
			return err
		}

		// APPLY inside a short tx with tx-scoped repos and Members == nil, so
		// the write critical section performs no live/pool/network I/O.
		return store.WithTx(ctx, r.db, func(tx store.DBTX) error {
			_, err := r.engine.ApplyRevocation(
				ctx, r.txStore(tx), pending.TGID, plan)

			return err
		})
	})
}

func (r *Reconciler) createHealthFailureAlert(ctx context.Context, err error) error {
	outbox := store.NewOutbox(r.db)
	_, _, alertErr := store.NewAlertsWithDelivery(
		r.db, outbox, r.cfg.OwnerIDs, r.cfg.AdminLogChatID,
	).CreateOpenIfMissing(
		ctx,
		store.AlertInput{
			Severity:  "warning",
			Kind:      "reconcile_health_failed",
			Title:     "reconcile health check failed",
			Detail:    err.Error(),
			DedupeKey: "reconcile_health_failed",
		},
	)

	return alertErr
}

// RunUser performs reconciliation work for one user.
func (r *Reconciler) RunUser(ctx context.Context, tgID int64) (Summary, error) {
	summary := Summary{}

	marker := "manual:" + r.now().UTC().Format(time.RFC3339Nano)

	for _, source := range r.cfg.Sources {
		if !source.Enabled || source.ChatID == 0 {
			continue
		}

		if err := r.enqueueVerify(ctx, tgID, nil, marker, verifyPayload{
			Kind:     "source",
			Platform: string(source.Platform),
			ChatID:   source.ChatID,
		}); err != nil {
			return summary, err
		}

		summary.VerifyActions++
	}

	for _, resource := range r.cfg.Resources {
		if resource.ChatID == 0 {
			continue
		}

		res := resource.Resource
		if err := r.enqueueVerify(ctx, tgID, &res, marker, verifyPayload{
			Kind:     "club",
			Resource: string(resource.Resource),
			ChatID:   resource.ChatID,
		}); err != nil {
			return summary, err
		}

		summary.VerifyActions++
	}

	if err := store.NewMeta(r.db).Set(ctx, "reconcile.last_run_at",
		r.now().UTC().Format(time.RFC3339)); err != nil {
		return summary, err
	}

	if err := store.NewAudit(r.db).Append(ctx, store.AuditEntry{
		TGID:   &tgID,
		Kind:   auditReconcilePass,
		Actor:  "job",
		Detail: summary.String(),
	}); err != nil {
		return summary, err
	}

	return summary, nil
}

func (r *Reconciler) engineStore(
	db store.DBTX, members engine.MemberChecker,
) engine.Store {
	outbox := store.NewOutbox(db)

	return engine.Store{
		Users:         store.NewUsers(db),
		Subscriptions: store.NewSubscriptions(db),
		Audit:         store.NewAudit(db),
		Revocations:   store.NewRevocations(db),
		Whitelist:     store.NewWhitelist(db),
		Grants:        store.NewGrants(db),
		Outbox:        outbox,
		Alerts: store.NewAlertsWithDelivery(
			db, outbox, r.cfg.OwnerIDs, r.cfg.AdminLogChatID),
		Members:     members,
		OperatorLog: r.opLog,
	}
}

// liveStore is bound to the pool and carries the live member checker. Use it
// only for the DECIDE phase (RevocationDecision) — never inside a transaction.
func (r *Reconciler) liveStore() engine.Store {
	return r.engineStore(r.db, r.members)
}

// txStore is bound to a transaction and carries NO member checker, so the
// APPLY phase (ApplyRevocation) performs only tx-scoped DB writes.
func (r *Reconciler) txStore(tx store.DBTX) engine.Store {
	return r.engineStore(tx, nil)
}

func (r *Reconciler) enqueueVerifyCandidates(
	ctx context.Context,
	marker string,
) (int, int, error) {
	tgIDs, err := r.candidates(ctx)
	if err != nil {
		return 0, 0, err
	}

	count := 0
	failed := 0

	for _, tgID := range tgIDs {
		for _, source := range r.cfg.Sources {
			if !source.Enabled || source.ChatID == 0 {
				continue
			}

			if err := r.enqueueVerify(ctx, tgID, nil, marker, verifyPayload{
				Kind:     "source",
				Platform: string(source.Platform),
				ChatID:   source.ChatID,
			}); err != nil {
				failed++

				r.logger.Warn("enqueue source verify failed",
					slog.Int64("tg_id", tgID),
					slog.Any("error", err))

				continue
			}

			count++
		}

		for _, resource := range r.cfg.Resources {
			if resource.ChatID == 0 {
				continue
			}

			res := resource.Resource
			if err := r.enqueueVerify(ctx, tgID, &res, marker, verifyPayload{
				Kind:     "club",
				Resource: string(resource.Resource),
				ChatID:   resource.ChatID,
			}); err != nil {
				failed++

				r.logger.Warn("enqueue club verify failed",
					slog.Int64("tg_id", tgID),
					slog.String("resource", string(resource.Resource)),
					slog.Any("error", err))

				continue
			}

			count++
		}
	}

	return count, failed, nil
}

func (r *Reconciler) candidates(ctx context.Context) ([]int64, error) {
	seen := map[int64]struct{}{}
	out := make([]int64, 0)

	addAll := func(ids []int64) {
		for _, id := range ids {
			if _, ok := seen[id]; ok {
				continue
			}

			seen[id] = struct{}{}
			out = append(out, id)
		}
	}

	subs, err := store.NewSubscriptions(r.db).ListActiveTGIDs(ctx, r.cfg.Limit)
	if err != nil {
		return nil, err
	}

	addAll(subs)

	grants, err := store.NewGrants(r.db).ListBotAdmittedJoinedTGIDs(ctx, r.cfg.Limit)
	if err != nil {
		return nil, err
	}

	addAll(grants)

	whitelist, err := store.NewWhitelist(r.db).ListTGIDs(ctx, r.cfg.Limit)
	if err != nil {
		return nil, err
	}

	addAll(whitelist)

	return out, nil
}

func (r *Reconciler) enqueueVerify(
	ctx context.Context,
	tgID int64,
	resource *domain.Resource,
	marker string,
	payload verifyPayload,
) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode verify payload: %w", err)
	}

	_, _, err = store.NewOutbox(r.db).Enqueue(ctx, store.AccessActionInput{
		Type:     domain.ActionVerifyMember,
		TGID:     &tgID,
		Resource: resource,
		IdempotencyKey: domain.AccessActionKey(
			domain.ActionVerifyMember,
			&tgID,
			resource,
			fmt.Sprintf("%s:%s:%s:%d:%s",
				payload.Kind, payload.Platform, payload.Resource, payload.ChatID, marker),
		),
		PayloadJSON: raw,
	})
	if err != nil {
		return fmt.Errorf("enqueue verify_member for %d: %w", tgID, err)
	}

	return nil
}

func (r *Reconciler) maintainInvites(ctx context.Context, marker string) (int, error) {
	count := 0

	if r.cfg.InviteMode == domain.InviteSharedJoinRequest {
		for _, resource := range r.cfg.Resources {
			if r.invites != nil {
				if _, ok, err := r.invites.ActiveShared(
					ctx, resource.Resource,
				); err != nil {
					return count, err
				} else if ok {
					continue
				}
			}

			res := resource.Resource

			_, _, err := store.NewOutbox(r.db).Enqueue(ctx, store.AccessActionInput{
				Type:     domain.ActionEnsureInvite,
				Resource: &res,
				IdempotencyKey: domain.AccessActionKey(
					domain.ActionEnsureInvite, nil, &res,
					"reconcile:shared_join_request:"+string(res)+":"+marker),
			})
			if err != nil {
				return count, err
			}

			count++
		}
	}

	expired, err := store.NewInvites(r.db).ListExpiredActive(ctx, r.now(), r.cfg.Limit)
	if err != nil {
		return count, err
	}

	for _, link := range expired {
		if err := store.NewCleanup(r.db).ExpireInvite(ctx, link.ID); err != nil {
			return count, err
		}

		if link.InviteLink != "" {
			payload, err := json.Marshal(struct {
				InviteLinkID int64  `json:"invite_link_id"`
				InviteLink   string `json:"invite_link"`
			}{InviteLinkID: link.ID, InviteLink: link.InviteLink})
			if err != nil {
				return count, err
			}

			res := link.Resource

			_, _, err = store.NewOutbox(r.db).Enqueue(ctx, store.AccessActionInput{
				Type:     domain.ActionRevokeInvite,
				TGID:     link.TGID,
				Resource: &res,
				IdempotencyKey: domain.AccessActionKey(
					domain.ActionRevokeInvite, link.TGID, &res,
					fmt.Sprintf("expired:%d", link.ID)),
				PayloadJSON: payload,
			})
			if err != nil {
				return count, err
			}
		}

		count++
	}

	return count, nil
}

func (r *Reconciler) cycleMarker() string {
	interval := r.cfg.Interval
	if interval <= 0 {
		interval = defaultInterval
	}

	return r.now().UTC().Truncate(interval).Format(time.RFC3339)
}

type verifyPayload struct {
	Kind     string `json:"kind,omitempty"`
	Platform string `json:"platform,omitempty"`
	Resource string `json:"resource,omitempty"`
	ChatID   int64  `json:"chat_id,omitempty"`
}

// Summary contains machine-readable pass counters.
type Summary struct {
	DueRevocations int
	VerifyActions  int
	InviteActions  int
	Failed         int
}

func (s Summary) String() string {
	return fmt.Sprintf(
		"due_revocations=%d verify_actions=%d invite_actions=%d failed=%d",
		s.DueRevocations, s.VerifyActions, s.InviteActions, s.Failed)
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

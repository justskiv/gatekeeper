package reconcile

import (
	"context"
	"log/slog"
	"time"

	"github.com/justskiv/gatekeeper/internal/store"
)

// CleanupService runs retention and SQLite maintenance.
type CleanupService struct {
	db     store.DBTX
	cfg    Config
	logger *slog.Logger
	now    func() time.Time
}

// NewCleanupService returns a cleanup loop.
func NewCleanupService(db store.DBTX, cfg Config, logger *slog.Logger) *CleanupService {
	if cfg.CleanupInterval <= 0 {
		cfg.CleanupInterval = 24 * time.Hour
	}

	if cfg.RawRetention <= 0 {
		cfg.RawRetention = 30 * 24 * time.Hour
	}

	if cfg.AuditRetention <= 0 {
		cfg.AuditRetention = 365 * 24 * time.Hour
	}

	if logger == nil {
		logger = slog.Default()
	}

	return &CleanupService{
		db:     db,
		cfg:    cfg,
		logger: logger,
		now:    time.Now,
	}
}

// Run starts periodic cleanup until ctx is cancelled.
func (s *CleanupService) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.cfg.CleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := s.RunOnce(ctx); err != nil {
				if isContextDone(ctx, err) {
					return nil
				}

				s.logger.Warn("cleanup pass failed", slog.Any("error", err))
			}
		}
	}
}

// RunOnce applies retention and maintenance once.
func (s *CleanupService) RunOnce(ctx context.Context) error {
	cleanup := store.NewCleanup(s.db)
	rawCutoff := s.now().Add(-s.cfg.RawRetention)
	auditCutoff := s.now().Add(-s.cfg.AuditRetention)

	if _, err := cleanup.DeleteTerminalTelegramUpdates(ctx, rawCutoff); err != nil {
		return err
	}

	if _, err := cleanup.DeleteTerminalTributeEvents(ctx, rawCutoff); err != nil {
		return err
	}

	if _, err := cleanup.DeleteDoneActions(ctx, rawCutoff); err != nil {
		return err
	}

	if _, err := cleanup.DeleteResolvedAlerts(ctx, rawCutoff); err != nil {
		return err
	}

	if _, err := cleanup.DeleteAudit(ctx, auditCutoff); err != nil {
		return err
	}

	if err := cleanup.WALCheckpoint(ctx); err != nil {
		s.logger.Warn("cleanup wal checkpoint failed", slog.Any("error", err))

		outbox := store.NewOutbox(s.db)
		if _, alertErr := store.NewAlertsWithDelivery(
			s.db, outbox, s.cfg.OwnerIDs, s.cfg.AdminLogChatID,
		).Create(ctx, store.AlertInput{
			Severity: "warning",
			Kind:     "cleanup_failed",
			Title:    "cleanup wal checkpoint failed",
			Detail:   err.Error(),
		}); alertErr != nil {
			s.logger.Warn("failed to create cleanup alert",
				slog.Any("error", alertErr))
		}

		return err
	}

	return nil
}

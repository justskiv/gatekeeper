-- +goose Up
-- Rebuild access_actions with three related changes.
--
-- 1. New `alert_id` column linking an outbox row to the admin_alerts row it
--    was created for. Without it nothing connects an alert to the deliveries
--    it queued, so resolving an alert cannot retire its pending notifications
--    and an owner still receives a DM about a problem that is already over.
--    `ON DELETE SET NULL` is mandatory, not decorative: store.Open enables
--    `_pragma=foreign_keys(ON)` and Cleanup.DeleteResolvedAlerts deletes
--    resolved alerts on retention. A plain reference would make that delete
--    fail with a FK violation forever as soon as one linked action outlives
--    its alert, and cleanup would never complete again.
-- 2. `cancelled` added to the status CHECK. Retiring an undelivered row needs
--    a state that is neither `done` (nothing was sent) nor `dead` (nothing
--    failed), so backlog and metrics keep telling the truth.
-- 3. `failed` dropped from the status CHECK. No code has ever written it:
--    the queue moves to `dead` on permanent failure. Keeping a status the
--    application cannot produce invites callers to handle a state that does
--    not exist. Rows are mapped `failed` -> `dead` on copy so an older
--    database cannot violate the narrower constraint.
--
-- SQLite cannot ALTER a CHECK constraint, so the table is rebuilt the same way
-- 0002 rebuilt it: rename, recreate with the new shape, copy rows, drop the old
-- table, restore the indexes.

ALTER TABLE access_actions RENAME TO access_actions_old;

CREATE TABLE access_actions (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    action_type     TEXT    NOT NULL CHECK (action_type IN (
                        'ensure_invite','send_invite','approve_join','decline_join',
                        'soft_kick','hard_ban','unban','send_dm',
                        'verify_member','revoke_invite','edit_message')),
    tg_id           INTEGER REFERENCES users(tg_id),
    resource        TEXT    CHECK (resource IN ('chat','channel')),
    alert_id        INTEGER REFERENCES admin_alerts(id) ON DELETE SET NULL,
    idempotency_key TEXT    NOT NULL UNIQUE,
    payload_json    TEXT    NOT NULL DEFAULT '{}',
    status          TEXT    NOT NULL DEFAULT 'queued'
                    CHECK (status IN ('queued','running','done','dead','cancelled')),
    run_after       TEXT    NOT NULL,
    attempts        INTEGER NOT NULL DEFAULT 0,
    max_attempts    INTEGER NOT NULL DEFAULT 8,
    locked_until    TEXT,
    last_error      TEXT    NOT NULL DEFAULT '',
    created_at      TEXT    NOT NULL,
    updated_at      TEXT    NOT NULL
);

INSERT INTO access_actions (
        id, action_type, tg_id, resource, alert_id, idempotency_key,
        payload_json, status, run_after, attempts, max_attempts,
        locked_until, last_error, created_at, updated_at)
    SELECT id, action_type, tg_id, resource, NULL, idempotency_key,
           payload_json,
           CASE status WHEN 'failed' THEN 'dead' ELSE status END,
           run_after, attempts, max_attempts, locked_until, last_error,
           created_at, updated_at
    FROM access_actions_old;

DROP TABLE access_actions_old;

CREATE INDEX idx_access_actions_ready ON access_actions(status, run_after);
-- Partial index: only linked rows are indexed. Almost every action carries no
-- alert, and resolve-time cancellation only ever looks up linked ones.
CREATE INDEX idx_access_actions_alert
    ON access_actions(alert_id) WHERE alert_id IS NOT NULL;

-- +goose Down
-- Rebuild the table with the pre-0003 shape: drop `alert_id`, restore `failed`
-- in the status CHECK and drop `cancelled` from it. Rows that were cancelled
-- map to `done`: the old constraint has no state for "retired undelivered",
-- and `done` is the only terminal value that does not fabricate a failure.

ALTER TABLE access_actions RENAME TO access_actions_old;

CREATE TABLE access_actions (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    action_type     TEXT    NOT NULL CHECK (action_type IN (
                        'ensure_invite','send_invite','approve_join','decline_join',
                        'soft_kick','hard_ban','unban','send_dm',
                        'verify_member','revoke_invite','edit_message')),
    tg_id           INTEGER REFERENCES users(tg_id),
    resource        TEXT    CHECK (resource IN ('chat','channel')),
    idempotency_key TEXT    NOT NULL UNIQUE,
    payload_json    TEXT    NOT NULL DEFAULT '{}',
    status          TEXT    NOT NULL DEFAULT 'queued'
                    CHECK (status IN ('queued','running','done','failed','dead')),
    run_after       TEXT    NOT NULL,
    attempts        INTEGER NOT NULL DEFAULT 0,
    max_attempts    INTEGER NOT NULL DEFAULT 8,
    locked_until    TEXT,
    last_error      TEXT    NOT NULL DEFAULT '',
    created_at      TEXT    NOT NULL,
    updated_at      TEXT    NOT NULL
);

INSERT INTO access_actions (
        id, action_type, tg_id, resource, idempotency_key, payload_json,
        status, run_after, attempts, max_attempts, locked_until, last_error,
        created_at, updated_at)
    SELECT id, action_type, tg_id, resource, idempotency_key, payload_json,
           CASE status WHEN 'cancelled' THEN 'done' ELSE status END,
           run_after, attempts, max_attempts, locked_until, last_error,
           created_at, updated_at
    FROM access_actions_old;

DROP TABLE access_actions_old;

CREATE INDEX idx_access_actions_ready ON access_actions(status, run_after);

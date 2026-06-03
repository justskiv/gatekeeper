-- +goose Up
-- Add 'edit_message' to the access_actions action_type CHECK constraint.
-- SQLite cannot ALTER a CHECK constraint, so the table is rebuilt: rename,
-- recreate with the wider constraint, copy rows, drop the old table, and
-- restore the index.

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

INSERT INTO access_actions
    SELECT id, action_type, tg_id, resource, idempotency_key, payload_json,
           status, run_after, attempts, max_attempts, locked_until, last_error,
           created_at, updated_at
    FROM access_actions_old;

DROP TABLE access_actions_old;

CREATE INDEX idx_access_actions_ready ON access_actions(status, run_after);

-- +goose Down
-- Rebuild the table with the original constraint. Drops any queued
-- edit_message rows, which cannot satisfy the narrower constraint.

ALTER TABLE access_actions RENAME TO access_actions_old;

CREATE TABLE access_actions (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    action_type     TEXT    NOT NULL CHECK (action_type IN (
                        'ensure_invite','send_invite','approve_join','decline_join',
                        'soft_kick','hard_ban','unban','send_dm',
                        'verify_member','revoke_invite')),
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

INSERT INTO access_actions
    SELECT id, action_type, tg_id, resource, idempotency_key, payload_json,
           status, run_after, attempts, max_attempts, locked_until, last_error,
           created_at, updated_at
    FROM access_actions_old
    WHERE action_type <> 'edit_message';

DROP TABLE access_actions_old;

CREATE INDEX idx_access_actions_ready ON access_actions(status, run_after);

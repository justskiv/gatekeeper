-- +goose Up
-- Migration 0001_init: initial schema.

-- ── Domain state ─────────────────────────────────────────────────────

-- Every Telegram user known to the system. Identity is the tg_id.
CREATE TABLE users (
    tg_id         INTEGER PRIMARY KEY,
    username      TEXT    NOT NULL DEFAULT '',  -- without @; display cache
    first_name    TEXT    NOT NULL DEFAULT '',
    last_name     TEXT    NOT NULL DEFAULT '',
    language_code TEXT    NOT NULL DEFAULT '',
    is_bot        INTEGER NOT NULL DEFAULT 0,
    dm_state      TEXT    NOT NULL DEFAULT 'unknown'
                  CHECK (dm_state IN ('unknown','open','blocked')),
    banned        INTEGER NOT NULL DEFAULT 0,    -- hard-ban by the owner
    banned_reason TEXT    NOT NULL DEFAULT '',
    notes         TEXT    NOT NULL DEFAULT '',   -- free-form admin notes
    created_at    TEXT    NOT NULL,
    updated_at    TEXT    NOT NULL,
    last_seen_at  TEXT    NOT NULL
);
CREATE INDEX idx_users_username ON users(username);

-- Subscriptions: one row per subscription period on one platform.
-- History is kept: a new period adds a new row, an expired one gets
-- status='expired' and ended_at; rows are never deleted.
CREATE TABLE subscriptions (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    tg_id              INTEGER NOT NULL REFERENCES users(tg_id),
    platform           TEXT    NOT NULL
                       CHECK (platform IN ('boosty','tribute','manual')),
    status             TEXT    NOT NULL
                       CHECK (status IN ('active','expired')),
    external_id        TEXT    NOT NULL DEFAULT '', -- tribute subscription_id, etc.
    external_period_id TEXT    NOT NULL DEFAULT '', -- tribute period_id
    tier               TEXT    NOT NULL DEFAULT '', -- subscription_name / tier
    started_at         TEXT    NOT NULL,
    expires_at         TEXT,                        -- NULL when the date is unknown
    ended_at           TEXT,                        -- set when status becomes expired
    last_signal        TEXT    NOT NULL DEFAULT '', -- event|webhook|reconcile|on_demand|manual
    last_event_at      TEXT,                        -- created_at of the last webhook
    last_checked_at    TEXT,
    created_at         TEXT    NOT NULL,
    updated_at         TEXT    NOT NULL
);
CREATE INDEX idx_subscriptions_user    ON subscriptions(tg_id);
CREATE INDEX idx_subscriptions_status  ON subscriptions(status);
CREATE INDEX idx_subscriptions_expires ON subscriptions(expires_at);
-- At most one ACTIVE subscription per (user, platform):
CREATE UNIQUE INDEX idx_subscriptions_active_unique
    ON subscriptions(tg_id, platform) WHERE status = 'active';

-- Access to club resources: one row per (user, resource) pair.
CREATE TABLE access_grants (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    tg_id          INTEGER NOT NULL REFERENCES users(tg_id),
    resource       TEXT    NOT NULL CHECK (resource IN ('chat','channel')),
    state          TEXT    NOT NULL
                   CHECK (state IN ('pending','joined','left','revoked')),
    admitted_by    TEXT    NOT NULL DEFAULT 'bot'
                   CHECK (admitted_by IN ('bot','external')),
    joined_at      TEXT,
    revoked_at     TEXT,
    revoked_reason TEXT    NOT NULL DEFAULT '',
    created_at     TEXT    NOT NULL,
    updated_at     TEXT    NOT NULL
);
CREATE UNIQUE INDEX idx_access_grants_unique ON access_grants(tg_id, resource);
CREATE INDEX idx_access_grants_state ON access_grants(resource, state);

-- Scheduled access revocations (the grace-period queue).
CREATE TABLE pending_revocations (
    tg_id        INTEGER PRIMARY KEY REFERENCES users(tg_id),
    reason       TEXT    NOT NULL DEFAULT '',
    scheduled_at TEXT    NOT NULL,            -- when the revocation takes effect
    notified     INTEGER NOT NULL DEFAULT 0,  -- whether a warning was sent
    created_at   TEXT    NOT NULL
);
CREATE INDEX idx_pending_revocations_due ON pending_revocations(scheduled_at);

-- Permanent whitelist: owner, moderators, free access.
CREATE TABLE whitelist (
    tg_id      INTEGER PRIMARY KEY REFERENCES users(tg_id),
    reason     TEXT    NOT NULL DEFAULT '',
    added_by   INTEGER NOT NULL,           -- tg_id of the admin
    created_at TEXT    NOT NULL
);

-- Invite links created by the bot.
-- shared_join_request: tg_id=NULL, one active link per resource.
-- personal_join_request/direct: tg_id required, the link is personal.
CREATE TABLE invite_links (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    tg_id                INTEGER REFERENCES users(tg_id),
    resource             TEXT    NOT NULL CHECK (resource IN ('chat','channel')),
    mode                 TEXT    NOT NULL
                         CHECK (mode IN (
                             'shared_join_request',
                             'personal_join_request',
                             'direct'
                         )),
    invite_link          TEXT    NOT NULL UNIQUE,
    invite_link_hash     TEXT    NOT NULL UNIQUE,
    telegram_name        TEXT    NOT NULL DEFAULT '',
    nonce                TEXT    NOT NULL DEFAULT '',
    status               TEXT    NOT NULL DEFAULT 'created'
                         CHECK (status IN (
                             'created','sent','used','used_by_other',
                             'revoked','expired','failed'
                         )),
    creates_join_request INTEGER NOT NULL DEFAULT 1
                         CHECK (creates_join_request IN (0, 1)),
    expires_at           TEXT,
    sent_at              TEXT,
    used_at              TEXT,
    revoked_at           TEXT,
    attempted_by         INTEGER REFERENCES users(tg_id),
    last_error           TEXT    NOT NULL DEFAULT '',
    created_at           TEXT    NOT NULL,
    updated_at           TEXT    NOT NULL,
    CHECK (
        (mode = 'shared_join_request' AND tg_id IS NULL) OR
        (mode IN ('personal_join_request','direct') AND tg_id IS NOT NULL)
    )
);
CREATE UNIQUE INDEX idx_invite_links_shared_active
    ON invite_links(resource, mode)
    WHERE mode = 'shared_join_request'
      AND status IN ('created','sent');
CREATE UNIQUE INDEX idx_invite_links_personal_active
    ON invite_links(tg_id, resource, mode)
    WHERE mode IN ('personal_join_request','direct')
      AND status IN ('created','sent');
CREATE INDEX idx_invite_links_user_resource
    ON invite_links(tg_id, resource, status);
CREATE INDEX idx_invite_links_expires
    ON invite_links(expires_at, status);

-- ── Idempotency and durable execution ────────────────────────────────

-- Durable inbox for incoming Telegram updates with the state machine
-- pending -> processed | ignored | failed (see SPEC §16.3).
CREATE TABLE telegram_updates (
    update_id    INTEGER PRIMARY KEY,
    update_type  TEXT    NOT NULL,
    chat_id      INTEGER,
    tg_id        INTEGER,
    payload_json TEXT    NOT NULL,
    status       TEXT    NOT NULL DEFAULT 'pending'
                 CHECK (status IN ('pending','processed','ignored','failed')),
    error        TEXT    NOT NULL DEFAULT '',  -- non-empty when status='failed'
    received_at  TEXT    NOT NULL,
    processed_at TEXT                          -- set on the terminal transition
);
-- Partial index: only pending rows are indexed. Terminal rows
-- (processed/ignored/failed) stay out of the index, so the growing
-- inbox tail does not bloat the b-tree.
CREATE INDEX idx_telegram_updates_pending
    ON telegram_updates(update_id) WHERE status = 'pending';

-- Raw Tribute webhooks: idempotency by dedup_key plus audit (mode B).
CREATE TABLE tribute_events (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    dedup_key       TEXT    NOT NULL UNIQUE,
    event_name      TEXT    NOT NULL,
    tg_id           INTEGER,
    subscription_id TEXT    NOT NULL DEFAULT '',
    signature_valid INTEGER NOT NULL DEFAULT 0,
    payload_json    TEXT    NOT NULL,
    status          TEXT    NOT NULL DEFAULT 'received'
                    CHECK (status IN ('received','processed','ignored','failed')),
    received_at     TEXT    NOT NULL,
    processed_at    TEXT,
    error           TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX idx_tribute_events_received ON tribute_events(received_at);

-- Outbox: a durable queue of Telegram actions. The action row is
-- written in the same transaction as the domain-state change.
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
CREATE INDEX idx_access_actions_ready ON access_actions(status, run_after);

-- ── Chronicle and operations ─────────────────────────────────────────

-- Append-only business journal of every significant action and observation.
CREATE TABLE audit_log (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    tg_id      INTEGER,
    kind       TEXT    NOT NULL,
    source     TEXT,                        -- boosty|tribute|manual
    resource   TEXT,                        -- chat|channel
    actor      TEXT    NOT NULL DEFAULT 'system'
               CHECK (actor IN ('system','user','admin','provider','job')),
    detail     TEXT    NOT NULL DEFAULT '',  -- human-readable or JSON
    created_at TEXT    NOT NULL
);
CREATE INDEX idx_audit_tg_time ON audit_log(tg_id, created_at);
CREATE INDEX idx_audit_time    ON audit_log(created_at);
CREATE INDEX idx_audit_kind    ON audit_log(kind, created_at);

-- Durable operational alerts for the administrator.
CREATE TABLE admin_alerts (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    severity    TEXT    NOT NULL
                CHECK (severity IN ('info','warning','error','critical')),
    status      TEXT    NOT NULL DEFAULT 'open'
                CHECK (status IN ('open','resolved')),
    kind        TEXT    NOT NULL,
    title       TEXT    NOT NULL,
    detail      TEXT    NOT NULL DEFAULT '',
    tg_id       INTEGER,
    created_at  TEXT    NOT NULL,
    resolved_at TEXT
);
CREATE INDEX idx_admin_alerts_open ON admin_alerts(status, severity);

-- Service key-value store: poller offset, reconcile time, chat health, etc.
CREATE TABLE meta (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

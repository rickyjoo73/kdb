-- KDB admin user accounts.
--
-- Replaces single-admin .env-based credentials. Phase 4 RBAC will extend
-- the `role` column; for now only 'admin' is recognised.

CREATE TABLE IF NOT EXISTS kwave_kdb_admin_users (
    email           TEXT PRIMARY KEY,
    password_hash   TEXT        NOT NULL,
    name            TEXT,
    role            TEXT        NOT NULL DEFAULT 'admin',
    enabled         BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_login_at   TIMESTAMPTZ,
    failed_attempts INTEGER     NOT NULL DEFAULT 0,
    locked_until    TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS kwave_kdb_admin_users_enabled_idx
    ON kwave_kdb_admin_users(enabled) WHERE enabled;

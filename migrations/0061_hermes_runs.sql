-- 0061_hermes_runs.sql
-- KDB Hermes supervisor audit table (docs/KDB_HERMES_AGENTS_DESIGN.md §C.3).
--
-- kwave_kdb_codex_runs is extraction-shaped (source_domain/locale/rss_title …)
-- and lacks severity / detail / run_id, so Hermes incidents + per-role run
-- accounting need a dedicated table. One row per agent run (plan→run→verify),
-- including item-conservation (leak) counts and the full RunReport as jsonb.
-- bridge_health incidents can also land here with role='Bridge'.
--
-- Additive only; safe to apply while autopilot is running. A -- DOWN block is
-- provided below for manual rollback.

BEGIN;

CREATE TABLE IF NOT EXISTS kwave_kdb_hermes_runs (
  id            bigserial   PRIMARY KEY,
  run_id        uuid        NOT NULL,
  role          text        NOT NULL,          -- Classifier | Enricher | step:* | Bridge | …
  cycle_id      uuid,                          -- correlates all roles run in one sweep cycle
  started_at    timestamptz NOT NULL DEFAULT now(),
  finished_at   timestamptz,
  status        text        NOT NULL,          -- ok | incident | retried | leak
  severity      text,                          -- info | warning | critical
  items_in      int         NOT NULL DEFAULT 0,
  items_out     int         NOT NULL DEFAULT 0,
  items_dropped int         NOT NULL DEFAULT 0,
  retries       int         NOT NULL DEFAULT 0,
  metric_before numeric,
  metric_after  numeric,
  error_text    text,
  report        jsonb,                          -- full RunReport + leak + audit detail
  self_check_ok boolean     NOT NULL DEFAULT false,
  resolved      boolean     NOT NULL DEFAULT false,
  created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_kdb_hermes_runs_run_id   ON kwave_kdb_hermes_runs (run_id);
CREATE INDEX IF NOT EXISTS idx_kdb_hermes_runs_cycle_id ON kwave_kdb_hermes_runs (cycle_id);
CREATE INDEX IF NOT EXISTS idx_kdb_hermes_runs_role     ON kwave_kdb_hermes_runs (role, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_kdb_hermes_runs_open     ON kwave_kdb_hermes_runs (created_at DESC)
  WHERE status <> 'ok' AND resolved = false;

COMMENT ON TABLE kwave_kdb_hermes_runs IS
  'Hermes supervisor: one row per audited agent run (plan→run→verify→retry). status/severity/leak counts + full RunReport jsonb. bridge_health incidents use role=Bridge.';

COMMIT;

-- =====================================================================
-- DOWN (rollback) — run manually to revert.
-- =====================================================================
-- DROP TABLE IF EXISTS kwave_kdb_hermes_runs;

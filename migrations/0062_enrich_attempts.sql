-- 0062_enrich_attempts.sql
-- Per-field enrichment attempt ledger for the Hermes "Enricher" role agent
-- (docs/KDB_HERMES_AGENTS_DESIGN.md §B.4). Additive, opt-in (only written when
-- KDB_HERMES_ENABLED=1). One row per (entity, field): how many cascade passes
-- have been spent on that field and whether the sources are exhausted, so the
-- gap-fill loop converges instead of re-trying an unknowable field forever.
--
-- Schema is dictated by the agent's SQL:
--   - write  : internal/kdb/agents/enricher/cascade.go  (INSERT ... ON CONFLICT
--              (entity_id, field) DO UPDATE ... attempts+1, exhausted, last_attempt_at)
--   - select : internal/kdb/agents/enricher/agent.go    (count(*) WHERE
--              entity_id = e.id AND exhausted = true AND last_attempt_at > now()-7d)
BEGIN;

CREATE TABLE IF NOT EXISTS kwave_kdb_enrich_attempts (
    entity_id       UUID        NOT NULL,
    field           TEXT        NOT NULL,
    attempts        INTEGER     NOT NULL DEFAULT 0,
    exhausted       BOOLEAN     NOT NULL DEFAULT false,
    last_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (entity_id, field)
);

-- Select's convergence guard filters on (entity_id, exhausted, last_attempt_at).
CREATE INDEX IF NOT EXISTS idx_kwave_kdb_enrich_attempts_entity_exhausted
    ON kwave_kdb_enrich_attempts (entity_id) WHERE exhausted = true;

COMMIT;

-- 0089: separate request completion from answer/locale outcomes and prevent
-- transient rows from being reclaimed immediately by the fixed research tick.

BEGIN;

ALTER TABLE kwave_entity_research_queue
  ADD COLUMN IF NOT EXISTS resolution_status text NOT NULL DEFAULT 'unknown',
  ADD COLUMN IF NOT EXISTS locale_status text NOT NULL DEFAULT 'unknown',
  ADD COLUMN IF NOT EXISTS last_outcome text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS next_attempt_at timestamptz;

CREATE INDEX IF NOT EXISTS kwave_entity_research_due_idx
  ON kwave_entity_research_queue (status, next_attempt_at, created_at)
  WHERE status='pending';

COMMENT ON COLUMN kwave_entity_research_queue.resolution_status IS
  'Entity answer state: active, candidate, rejected, ambiguous, or unknown.';
COMMENT ON COLUMN kwave_entity_research_queue.locale_status IS
  'Locale discovery state, separate from request status: searching, evidence_queued, no_match, blocked_no_source, complete.';
COMMENT ON COLUMN kwave_entity_research_queue.last_outcome IS
  'Last worker outcome such as applied, noop, transient, permanent, or blocked_no_source.';
COMMENT ON COLUMN kwave_entity_research_queue.next_attempt_at IS
  'Earliest retry time for transient failures; claim skips rows not yet due.';

COMMIT;

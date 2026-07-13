-- 0091: fail-closed proper-noun admission gate for provider research.
--
-- Queue status remains the transport lifecycle (pending/in_progress/done/failed).
-- precheck_status is the independent cost-authorization state.  Only pass or
-- explicit operator approval may be claimed by a research worker.

BEGIN;

ALTER TABLE kwave_entity_research_queue
  ADD COLUMN IF NOT EXISTS precheck_status text NOT NULL DEFAULT 'legacy',
  ADD COLUMN IF NOT EXISTS precheck_reason text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS precheck_flags text[] NOT NULL DEFAULT '{}',
  ADD COLUMN IF NOT EXISTS precheck_rule_version text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS intake_origin text NOT NULL DEFAULT 'unknown',
  ADD COLUMN IF NOT EXISTS intake_normalized_key text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS request_count integer NOT NULL DEFAULT 1,
  ADD COLUMN IF NOT EXISTS last_requested_at timestamptz NOT NULL DEFAULT now();

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
     WHERE conname='kwave_entity_research_precheck_status_check'
       AND conrelid='kwave_entity_research_queue'::regclass
  ) THEN
    ALTER TABLE kwave_entity_research_queue
      ADD CONSTRAINT kwave_entity_research_precheck_status_check
      CHECK (precheck_status IN ('legacy','pass','review','reject','approved'));
  END IF;
END $$;

CREATE INDEX IF NOT EXISTS kwave_entity_research_precheck_due_idx
  ON kwave_entity_research_queue (status, next_attempt_at, created_at)
  WHERE status='pending' AND precheck_status IN ('pass','approved');

UPDATE kwave_entity_research_queue
   SET intake_normalized_key=lower(regexp_replace(btrim(entity_ko), '[[:space:][:punct:]]+', '', 'g'))
 WHERE intake_normalized_key='';

-- Unknown-version live work must never inherit permission. The new worker can
-- only claim pass/approved rows; operators can release genuine legacy names.
UPDATE kwave_entity_research_queue
   SET status='done', finished_at=now(), precheck_status='review',
       precheck_reason='legacy_requires_recheck',
       precheck_rule_version='proper-noun-v1-20260710',
       resolution_status='review_required', locale_status='blocked_precheck',
       last_outcome='precheck_review'
 WHERE status IN ('pending','in_progress') AND precheck_status='legacy';

-- Old NULL source_id semantics admitted duplicate provider work. Keep every
-- row as audit history, but terminally hold all but the oldest live row before
-- installing a normalized-name/type single-flight invariant.
WITH ranked AS (
  SELECT id,
         row_number() OVER (
           PARTITION BY intake_normalized_key, requested_entity_type
           ORDER BY created_at, id
         ) AS rn
    FROM kwave_entity_research_queue
   WHERE status IN ('pending','in_progress')
)
UPDATE kwave_entity_research_queue q
   SET status='done', finished_at=now(), precheck_status='review',
       precheck_reason='duplicate_live_request',
       precheck_rule_version='proper-noun-v1-20260710',
       resolution_status='review_required', locale_status='blocked_precheck',
       last_outcome='precheck_review'
  FROM ranked r
 WHERE q.id=r.id AND r.rn>1 AND q.status IN ('pending','in_progress');

CREATE UNIQUE INDEX IF NOT EXISTS kwave_entity_research_live_term_type_uidx
  ON kwave_entity_research_queue (intake_normalized_key, requested_entity_type)
  WHERE status IN ('pending','in_progress');

-- Rejudge historically treated "has a Wikidata item" as entity proof and
-- reactivated common terms. Reversible quarantine keeps the rows for operator
-- review while removing them from active serving/provider selectors.
UPDATE kwave_entities
   SET status='candidate', needs_disambig=true,
       notes=COALESCE(NULLIF(notes,'') || ' · ','') ||
             '[proper-noun-gate] Wikidata-only term reactivation quarantined',
       updated_at=now()
 WHERE status='active' AND entity_type='term' AND operator_locked=false
   AND COALESCE(notes,'') LIKE '%rejudge:%wikidata%';

COMMENT ON COLUMN kwave_entity_research_queue.precheck_status IS
  'Proper-noun/API cost gate: pass or operator-approved may call providers; review/reject never may.';
COMMENT ON COLUMN kwave_entity_research_queue.precheck_reason IS
  'Stable machine reason emitted by the deterministic intake rule.';
COMMENT ON COLUMN kwave_entity_research_queue.precheck_flags IS
  'Auditable shape/evidence flags emitted by the intake rule.';
COMMENT ON COLUMN kwave_entity_research_queue.precheck_rule_version IS
  'Rule version used for the decision; legacy/unknown rows are re-evaluated fail-closed.';
COMMENT ON COLUMN kwave_entity_research_queue.intake_origin IS
  'Server-assigned intake path such as lookup-miss, prepare, match-miss, direct-api, or rss-observation.';
COMMENT ON COLUMN kwave_entity_research_queue.intake_normalized_key IS
  'NFC/lowercase key with spacing and punctuation removed; identity dedup only, raw spelling is preserved.';
COMMENT ON COLUMN kwave_entity_research_queue.request_count IS
  'Number of duplicate intake requests observed without starting duplicate provider work.';

COMMIT;

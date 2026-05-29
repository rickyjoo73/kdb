-- 0059_last_enriched_at.sql — kdb-api lookup 시 background enrich 중복 방지용.
-- BackgroundEnricher 가 last_enriched_at < now() - 1h 인 row 만 처리.
ALTER TABLE kwave_entities ADD COLUMN IF NOT EXISTS last_enriched_at timestamptz;
CREATE INDEX IF NOT EXISTS kwave_entities_last_enriched_at_idx
  ON kwave_entities (last_enriched_at NULLS FIRST);

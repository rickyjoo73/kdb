-- 0050_kdb_source_priority_function.sql — SQL helper for source priority guard.
--
-- canonical_X UPDATE 시 priority 비교를 CASE WHEN inline 으로 처리하려면
-- SQL function 형태가 가독성 좋음.
--
-- 정공법: internal/kdb/source_priority.go::Priority 와 1:1 매핑.

BEGIN;

CREATE OR REPLACE FUNCTION kdb_source_priority(s text) RETURNS int
LANGUAGE sql IMMUTABLE AS $$
  SELECT CASE s
    WHEN 'operator-locked'      THEN 1
    WHEN 'operator'             THEN 1
    WHEN 'media-consensus'      THEN 2
    WHEN 'wikidata-label'       THEN 3
    WHEN 'wikipedia-langlinks'  THEN 4
    WHEN 'wikipedia-sitelink'   THEN 5
    WHEN 'wikipedia-zh-variant' THEN 5
    ELSE 99
  END
$$;

COMMENT ON FUNCTION kdb_source_priority(text) IS
  'KDB platform source priority enum (낮을수록 우선). cascade UPDATE 의 CASE WHEN 가드에 사용.';

COMMIT;

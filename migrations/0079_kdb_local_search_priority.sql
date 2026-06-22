-- 0079_kdb_local_search_priority.sql
-- 'local-search'(prio 7) 추가 + codex-fallback 7→8.
-- LocalFill/QA 워커의 약증거 검색보강값(local-search)은 검색그라운드라 codex-fallback
-- (LLM 합성)보다 우선해야 한다(검색 > 합성). 단 권위소스(media/api/wikidata/wiki)는
-- 여전히 이를 업그레이드. 강증거 검색값은 local-usage(1)로 승급.
--   wiki 보조(6) < local-search(7) < codex-fallback(8) < unknown(99).
--
-- 0078 을 대체하는 최신 canonical priority 정의(source_priority_test.go 가 이 파일을
-- 파싱해 Go Priority() 와 1:1 대조). 낮은 숫자 = 높은 우선순위.
BEGIN;

CREATE OR REPLACE FUNCTION kdb_source_priority(s text) RETURNS int
LANGUAGE sql IMMUTABLE AS $$
  SELECT CASE
    WHEN s LIKE 'rss-observation%'        THEN 3
    WHEN s = 'operator-locked'            THEN 1
    WHEN s = 'operator'                   THEN 1
    WHEN s = 'local-usage'                THEN 1
    WHEN s = 'media-consensus'            THEN 2
    WHEN s = 'tmdb'                       THEN 4
    WHEN s = 'kofic'                      THEN 4
    WHEN s = 'kmdb'                       THEN 4
    WHEN s = 'musicbrainz'                THEN 4
    WHEN s = 'naver-people'               THEN 4
    WHEN s = 'correction-verified'        THEN 4
    WHEN s = 'wikidata-label'             THEN 5
    WHEN s = 'wikipedia-langlinks'        THEN 6
    WHEN s = 'wikipedia-sitelink'         THEN 6
    WHEN s = 'wikipedia-zh-variant'       THEN 6
    WHEN s = 'local-search'               THEN 7
    WHEN s = 'codex-fallback'             THEN 8
    ELSE 99
  END
$$;

COMMENT ON FUNCTION kdb_source_priority(text) IS
  'KDB source priority (낮을수록 우선). internal/kdb/source_priority.go::Priority 와 1:1. 변경 시 양쪽 + source_priority_test.go 동기화.';

COMMIT;

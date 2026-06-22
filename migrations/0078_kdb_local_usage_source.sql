-- 0078_kdb_local_usage_source.sql
-- 'local-usage'(prio 1) 추가 — 각 언어로 현지에서 실제 쓰이는 표기를 언어별 검색으로
-- 발견하고 강한 증거(다회투표 만장일치 + grounding)로 확정한 값(검색그라운드 현지표기).
-- KDB 핵심 권위. operator-locked/operator 와 동일한 최상위 티어(1)이며, can_replace_canonical
-- 가드가 operator-locked/operator 를 보존하므로 자동 확정값이 사람의 잠금을 덮지는 않는다.
--
-- 2단계: 약한 증거의 검색 보강은 'local-search'(ELSE→99, 빈칸만)로 남고, 강한 증거로
-- 확정된 값만 'local-usage'(1)로 승급된다. internal/kdbapi/qa.go::applyQAFills 참조.
--
-- 0076 을 대체하는 최신 canonical priority 정의(source_priority_test.go 가 이 파일을
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
    WHEN s = 'codex-fallback'             THEN 7
    ELSE 99
  END
$$;

COMMENT ON FUNCTION kdb_source_priority(text) IS
  'KDB source priority (낮을수록 우선). internal/kdb/source_priority.go::Priority 와 1:1. 변경 시 양쪽 + source_priority_test.go 동기화.';

COMMIT;

-- 0068_kdb_source_priority_sync.sql — kdb_source_priority 를 Go enum 과 재정합.
--
-- 0050 의 함수가 internal/kdb/source_priority.go::Priority 와 어긋나 있었다:
--   - tmdb/kofic/kmdb/musicbrainz/naver-people → Go=4 인데 SQL=99 (ELSE)
--   - rss-observation:*                        → Go=3 인데 SQL=99 (ELSE)
--   - codex-fallback                           → Go=7 인데 SQL=99 (ELSE)
--   - wikidata-label                           → Go=5 인데 SQL=3
--   - wikipedia-langlinks/sitelink/zh-variant  → Go=6 인데 SQL=4/5
-- 현재 can_replace_canonical 호출부가 incoming='media-consensus'(prio 2, 양쪽
-- 동일) 뿐이라 라이브 영향은 없었으나, 다른 incoming source 로 호출하거나 수동
-- SQL UPDATE 가 이 함수에 의존하면 즉시 역전된 우선순위를 강제하는 지뢰였다.
--
-- 정공법: Go Priority() 와 1:1. source 별 한 WHEN 으로 풀어써서
-- source_priority_test.go 가 이 파일을 직접 파싱해 Go 와 대조한다 (드리프트 차단).
--
-- 낮은 숫자 = 높은 우선순위. rss-observation 은 "rss-observation:<domain>"
-- 형태이므로 prefix(LIKE) 매칭 (Go isRSSObservation 와 동일).

BEGIN;

CREATE OR REPLACE FUNCTION kdb_source_priority(s text) RETURNS int
LANGUAGE sql IMMUTABLE AS $$
  SELECT CASE
    WHEN s LIKE 'rss-observation%'        THEN 3
    WHEN s = 'operator-locked'            THEN 1
    WHEN s = 'operator'                   THEN 1
    WHEN s = 'media-consensus'            THEN 2
    WHEN s = 'tmdb'                       THEN 4
    WHEN s = 'kofic'                      THEN 4
    WHEN s = 'kmdb'                       THEN 4
    WHEN s = 'musicbrainz'                THEN 4
    WHEN s = 'naver-people'               THEN 4
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

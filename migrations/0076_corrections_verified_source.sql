-- 0076_corrections_verified_source.sql
-- 양방향 정정(클라이언트 ↔ KDB codex 검증 ↔ 확인)을 위한 두 가지:
--   ① kdb_source_priority 에 'correction-verified'(prio 4) 추가 — 클라 정정이 codex
--      검증 + 클라 확인을 통과해 반영된 값. 교차검증 등급이라 codex-fallback/wikipedia/
--      wikidata 를 교체할 수 있으나 operator/consensus/현지매체/권위API 는 보존.
--   ② kwave_kdb_corrections.proposed_value — KDB(codex)가 회신한 수정안. 클라가 확인
--      (confirm)하면 이 값을 반영한다.
--
-- 0068 을 대체하는 최신 canonical priority 정의(source_priority_test.go 가 이 파일을
-- 파싱해 Go Priority() 와 1:1 대조). 낮은 숫자 = 높은 우선순위.
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

ALTER TABLE kwave_kdb_corrections
    ADD COLUMN IF NOT EXISTS proposed_value text NOT NULL DEFAULT '';

COMMIT;

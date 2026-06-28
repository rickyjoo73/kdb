-- 0082: romanization source 를 kdb_source_priority 에 prio 7(local-search/mydramalist 동급,
-- codex-fallback(8)만 교체·권위소스 불가침)로 등록. romanize.go 가 한국 인물의 Latin locale
-- (vi/es/id/pt_br)을 canonical_en(검증 로마자)에서 결정적 재속성할 때 can_replace_canonical 이
-- codex-fallback 을 교체하도록. 로마자 재속성은 verified tier 제외(api.go provenance). Go: source_priority.go 동기.

CREATE OR REPLACE FUNCTION public.kdb_source_priority(s text)
 RETURNS integer
 LANGUAGE sql
 IMMUTABLE
AS $function$
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
    WHEN s = 'netflix'                    THEN 4
    WHEN s = 'disney'                     THEN 4
    WHEN s = 'wikidata-label'             THEN 5
    WHEN s = 'wikipedia-langlinks'        THEN 6
    WHEN s = 'wikipedia-sitelink'         THEN 6
    WHEN s = 'wikipedia-zh-variant'       THEN 6
    WHEN s = 'local-search'               THEN 7
    WHEN s = 'mydramalist'                THEN 7
    WHEN s = 'romanization'               THEN 7
    WHEN s = 'codex-fallback'             THEN 8
    ELSE 99
  END
$function$;

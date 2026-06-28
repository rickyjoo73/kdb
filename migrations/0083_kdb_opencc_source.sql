-- 0083: opencc source(zh↔zh_hant 결정적 변환) → prio 7. Go: source_priority.go(SourceOpenCC) 동기.


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
    WHEN s = 'opencc'                     THEN 7
    WHEN s = 'codex-fallback'             THEN 8
    ELSE 99
  END
$function$;

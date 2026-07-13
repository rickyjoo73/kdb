-- 0088: source pipeline registry additions.
-- Go: internal/kdb/source_priority.go synchronized by source_priority_test.go.

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
    WHEN s = 'itunes'                     THEN 4
    WHEN s = 'discogs'                    THEN 4
    WHEN s = 'cube-official'              THEN 4
    WHEN s = 'warner-japan'               THEN 4
    WHEN s = 'melon'                      THEN 4
    WHEN s = 'genie'                      THEN 4
    WHEN s = 'bugs'                       THEN 4
    WHEN s = 'vibe'                       THEN 4
    WHEN s = 'qq-music'                   THEN 4
    WHEN s = 'netease-music'              THEN 4
    WHEN s = 'tencent-music'              THEN 4
    WHEN s = 'spotify'                    THEN 4
    WHEN s = 'komca'                      THEN 4
    WHEN s = 'official-page'              THEN 4
    WHEN s = 'broadcaster-official'       THEN 4
    WHEN s = 'ott-official'               THEN 4
    WHEN s = 'tving'                      THEN 4
    WHEN s = 'wavve'                      THEN 4
    WHEN s = 'watcha'                     THEN 4
    WHEN s = 'coupang-play'               THEN 4
    WHEN s = 'viki'                       THEN 4
    WHEN s = 'lollapalooza'               THEN 4
    WHEN s = 'yes24-livehall'             THEN 4
    WHEN s = 'wikidata-label'             THEN 5
    WHEN s = 'wikipedia-langlinks'        THEN 6
    WHEN s = 'wikipedia-sitelink'         THEN 6
    WHEN s = 'wikipedia-zh-variant'       THEN 6
    WHEN s = 'local-search'               THEN 7
    WHEN s = 'mydramalist'                THEN 7
    WHEN s = 'romanization'               THEN 7
    WHEN s = 'opencc'                     THEN 7
    WHEN s = 'tvmaze'                     THEN 7
    WHEN s = 'naver-encyc'                THEN 7
    WHEN s = 'naver-search'               THEN 7
    WHEN s = 'kakao-search'               THEN 7
    WHEN s = 'youtube-official'           THEN 7
    WHEN s = 'namuwiki'                   THEN 7
    WHEN s = 'baidu-baike'                THEN 7
    WHEN s = 'gemini-search'              THEN 7
    WHEN s = 'codex-fallback'             THEN 8
    ELSE 99
  END
$function$;

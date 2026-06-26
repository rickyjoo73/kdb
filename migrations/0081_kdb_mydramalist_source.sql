-- 0081: MyDramaList(커뮤니티 작품 DB) source 를 kdb_source_priority 에 prio 7(local-search 동급,
-- codex-fallback(8)만 교체·권위소스 불가침)로 등록. mdl.go DrainMDLWorks 가 source='mydramalist'
-- 로 작품(drama/show/movie)의 ja 현지제목을 MDL "Also Known As" 에서 추출해 쓸 때
-- can_replace_canonical 이 codex-fallback 을 교체하도록. 커뮤니티 편집 출처라 verified tier 제외.
-- Go: source_priority.go(SourceMyDramaList) 와 동기.

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
    WHEN s = 'codex-fallback'             THEN 8
    ELSE 99
  END
$function$;

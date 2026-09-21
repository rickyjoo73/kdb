-- 0153: wikipedia-title — **같은 대상임이 확인된** 영어 위키백과 문서 제목.
--
-- ★왜 (2026-09-21). 소비자가 알려 왔다: 「배윤규」의 canonical_en 이 "Bae Youn-kyu" 인데
--   KDB 자신이 단 출처가 https://en.wikipedia.org/wiki/Bae_Yoon-gyu 다. **출처와 저장값이
--   어긋난다.** 원장 전체에서 같은 모양이 683건(동음이의 괄호를 뗀 정직한 수)이다.
--
--   원인은 우선순위다. wikidata-label(5) 이 wikipedia-sitelink(6) 보다 높아서 라벨이
--   문서 제목을 이겼다. 그런데 영어 위키백과의 문서 제목은 «영어 출처에서 가장 흔한
--   이름»을 쓰라는 규칙으로 정해진다 — en 칸에 대해서는 라벨보다 나은 증거다:
--
--     강부자 Kang Bu-ja → Kang Boo-ja · 하재숙 Ha Jae-suk → Ha Jae-sook
--     김수미 Kim Su-mi  → Kim Soo-mi  · 진이한 Jin I-han   → Jin Yi-han
--
-- ★그러나 출처 URL 을 그냥 믿으면 안 된다. 링크 자체가 엉뚱한 문서를 가리킨다:
--     이수 → "MC the Max"(그의 밴드) · DK → "Dplus KIA"(e스포츠 팀)
--   그래서 이 등급은 **우리 앵커 QID 의 enwiki 사이트링크 제목과 일치할 때만** 붙인다
--   (wiki_title_fix.go). 「그 문서가 이 대상의 것」이 확인된 경우에만 쓰는 출처다.
--
-- ★자리는 4 로 둔다 — 보수적으로. wikidata-label(5) 만 이긴다. musicbrainz·교정검증·
--   tmdb(4) 는 같은 등급이라 못 덮고(can_replace 는 «더 높을 때만»), rss·운영자(1~3)도
--   못 덮는다. 사람이 확인한 값과 권위 API 값은 건드리지 않는다.
--
-- ★함수 교체는 테이블 잠금이 아니지만, 오늘 운영을 세운 교훈대로 기다리지 않는다.
SET lock_timeout = '3s';

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
    WHEN s = 'wikipedia-title'            THEN 4
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
    WHEN s = 'gtranslate'                 THEN 8
    WHEN s = 'codex-fallback'             THEN 8
    ELSE 99
  END
$function$;

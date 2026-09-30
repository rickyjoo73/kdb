-- 0156: 소비자 제안 표기를 **우리 기계값보다 앞**에 둔다 + 빠져 있던 다섯 출처를 SQL 에 싣는다.
--
-- ★왜 (2026-09-30, 오너). "우리가 빈자리라면 gpt-6 sol 이 문맥에 맞게 직역한 거라 우리가
--   직역한 것보다 더 효율적 … 어차피 llm 번역이라면 제안에서 올라온 것이 더 좋을 거야.
--   우리가 찾다가 못 찾으면 우선 사용하는 것을 봐야지."
--
--   소비자(presslocale 등)는 기사 원문을 보고 표기를 만든다. 우리 LLM·구글번역·가나규칙은
--   이름 하나만 보고 만든다. 그런데 consumer-suggestion 은 잠정과 같은 최하위라, 우리
--   LLM 이 먼저 칸을 차지하면 제안은 영영 못 들어갔다.
--
--   새 순서(숫자가 작을수록 우선):
--     1~7  근거 있는 출처 (운영자·매체·권위 API·위키·검색)      — 그대로
--     8    consumer-suggestion                                    — 9 → 8
--     9    gtranslate · codex-fallback · kana-rule (우리 기계값)   — 8 → 9
--     10   gtranslate-raw · llm-provisional (최하위)               — 9 → 10
--   기존 출처끼리의 상대 순서는 **하나도 바뀌지 않는다.** 제안 하나가 그 사이로 들어갈 뿐이다.
--
-- ★빠져 있던 다섯 (Go Priority 에는 있는데 SQL 에서 ELSE 99 로 떨어지던 것).
--     kowiki-hanja(6) · kana-rule · gtranslate-raw · llm-provisional · consumer-suggestion
--   99 이면 can_replace_canonical 이 «무엇이든 덮어도 된다»고 답한다 — kowiki-hanja(6) 로
--   채운 인명 한자를 codex-fallback 이 덮을 수 있었고, consumer-suggestion 을 들고 들어오는
--   쓰기는 무엇도 덮지 못했다. 0050 때와 같은 드리프트다. TestSQLPriorityMatchesGo 가 이제
--   다섯을 함께 본다.
--
-- ★함수 교체는 테이블 잠금이 아니다. 데이터는 바꾸지 않는다 — 기계값 칸을 제안으로 바꾸는
--   일은 suggestion-fill 레인이 스냅샷(dataqa_log verdict=suggestion-backfill)과 함께 한다.
BEGIN;
SET LOCAL lock_timeout = '3s';

-- 제안이 **무엇이라고 물은 것인지**(소비자 type 힌트)를 제안 행에 남긴다. 대상에 붙일 때
-- 유형이 어긋나면 붙이지 않기 위해서다(«좋은 날» 곡 제안이 동명 드라마에 붙는 것을 막는다).
ALTER TABLE kwave_kdb_suggested_names ADD COLUMN IF NOT EXISTS term_type text NOT NULL DEFAULT '';

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
    WHEN s = 'kowiki-hanja'               THEN 6
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
    WHEN s = 'consumer-suggestion'        THEN 8
    WHEN s = 'gtranslate'                 THEN 9
    WHEN s = 'codex-fallback'             THEN 9
    WHEN s = 'kana-rule'                  THEN 9
    WHEN s = 'gtranslate-raw'             THEN 10
    WHEN s = 'llm-provisional'            THEN 10
    ELSE 99
  END
$function$;

COMMIT;

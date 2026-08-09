-- 0113 — canonical 값에 섞인 폭 0 서식문자 13칸을 제거한다 (2026-08-09)
--
-- 45차 §6-5 의 `correction-verified` 신뢰도 점검(세 세션 미해결)을 하다 나왔다. 그 표시가
-- 붙은 값을 훑던 중 `빌리` en 이 `Billlie` 가 아니라 `Billlie‎`(끝에 U+200E)인 게 보였고,
-- 전 컬럼으로 확대하니 **소스를 가리지 않고 13칸**이 나왔다.
--
--   U+FEFF (BOM/ZWNBSP)      8칸  ja  — wikidata-label 6 · tmdb 2
--   U+200E (LRM)             2칸  en(correction-verified) · es(wikidata-label)
--   U+202C (PDF, bidi 종료)  2칸  수윤 zh_hant(wikidata-label) → zh(opencc)
--   U+200B (ZWSP)            1칸  vi(tmdb)
--
-- 전부 폭 0 서식문자다. 이름에 들어갈 이유가 없고, 보이지 않아서 눈으로는 영영 못 찾는다.
-- 실해는 조용하다 — 같은 이름이 문자열 비교에서 안 맞고, 중복 탐지·별칭 매칭·소비자 검색이
-- 전부 빗나간다.
--
-- ★`수윤` 은 증폭 사례다. wikidata-label 이 zh_hant 에 U+202C 를 넣었고 opencc 가 그걸
-- 그대로 zh 로 복사했다. **파생 레인은 오염도 함께 복사한다** — 44차 §1 이 앵커에서,
-- 45차 §2 가 로마자 전파에서 밟은 것과 같은 구조다.
--
-- ★NBSP(U+00A0)·narrow NBSP 는 검사했지만 **한 건도 없었다.** 있었다면 제거가 아니라 일반
-- 공백으로 치환해야 했다(지우면 단어가 붙는다). 지금은 폭 0 문자뿐이라 제거가 정답이다.
--
-- ⚠ 처음 조회할 때 정규식 문자클래스에 리터럴 문자를 넣었다가 **일반 공백이 섞여 수천 건이
-- 걸리는 오탐**을 냈다. 보이지 않는 문자는 소스에 직접 쓰지 말고 코드포인트로 다뤄야 한다 —
-- 아래도 chr() 로 짠다.
--
-- 값이 바뀌므로 fill_input_hash 가 갱신되고 관련 판정이 자동으로 재개된다.

BEGIN;

CREATE TEMP TABLE zw AS SELECT string_agg(chr(x), '') AS chars FROM unnest(ARRAY[
  8203, 8204, 8205, 8206, 8207,   -- ZWSP ZWNJ ZWJ LRM RLM
  8234, 8235, 8236, 8237, 8238,   -- bidi 제어 LRE RLE PDF LRO RLO
  8239, 8288, 65279               -- narrow NBSP, word joiner, BOM
]) x;
-- ★U+00A0 NBSP 는 일부러 뺐다 — 실측 0건이고, 있더라도 제거가 아니라 공백 치환이 맞다.

CREATE TEMP TABLE zw_before AS
SELECT id, canonical_ko, canonical_ja, canonical_zh, canonical_zh_hant,
       canonical_en, canonical_vi, canonical_es, canonical_id, canonical_pt_br
  FROM kwave_entities e, zw
 WHERE e.status='active' AND (
       translate(COALESCE(canonical_ko,''),      zw.chars,'') <> COALESCE(canonical_ko,'')
    OR translate(COALESCE(canonical_ja,''),      zw.chars,'') <> COALESCE(canonical_ja,'')
    OR translate(COALESCE(canonical_zh,''),      zw.chars,'') <> COALESCE(canonical_zh,'')
    OR translate(COALESCE(canonical_zh_hant,''), zw.chars,'') <> COALESCE(canonical_zh_hant,'')
    OR translate(COALESCE(canonical_en,''),      zw.chars,'') <> COALESCE(canonical_en,'')
    OR translate(COALESCE(canonical_vi,''),      zw.chars,'') <> COALESCE(canonical_vi,'')
    OR translate(COALESCE(canonical_es,''),      zw.chars,'') <> COALESCE(canonical_es,'')
    OR translate(COALESCE(canonical_id,''),      zw.chars,'') <> COALESCE(canonical_id,'')
    OR translate(COALESCE(canonical_pt_br,''),   zw.chars,'') <> COALESCE(canonical_pt_br,''));

UPDATE kwave_entities e SET
  canonical_ko      = translate(e.canonical_ko,      zw.chars, ''),
  canonical_ja      = translate(e.canonical_ja,      zw.chars, ''),
  canonical_zh      = translate(e.canonical_zh,      zw.chars, ''),
  canonical_zh_hant = translate(e.canonical_zh_hant, zw.chars, ''),
  canonical_en      = translate(e.canonical_en,      zw.chars, ''),
  canonical_vi      = translate(e.canonical_vi,      zw.chars, ''),
  canonical_es      = translate(e.canonical_es,      zw.chars, ''),
  canonical_id      = translate(e.canonical_id,      zw.chars, ''),
  canonical_pt_br   = translate(e.canonical_pt_br,   zw.chars, ''),
  updated_at = now()
FROM zw WHERE e.id IN (SELECT id FROM zw_before);

DO $$
DECLARE n int; r record;
BEGIN
  SELECT count(*) INTO n FROM zw_before;
  RAISE NOTICE '0113: 폭0 문자 제거 대상 엔티티 %건', n;
  FOR r IN SELECT canonical_ko FROM kwave_entities WHERE id IN (SELECT id FROM zw_before) ORDER BY 1
  LOOP RAISE NOTICE '  · %', r.canonical_ko; END LOOP;

  SELECT count(*) INTO n FROM kwave_entities e, zw
   WHERE e.status='active'
     AND (translate(COALESCE(canonical_ja,''), zw.chars,'') <> COALESCE(canonical_ja,'')
       OR translate(COALESCE(canonical_en,''), zw.chars,'') <> COALESCE(canonical_en,'')
       OR translate(COALESCE(canonical_zh,''), zw.chars,'') <> COALESCE(canonical_zh,''));
  RAISE NOTICE '0113: 남은 폭0 오염 %건 (0이어야 정상)', n;
END $$;

COMMIT;

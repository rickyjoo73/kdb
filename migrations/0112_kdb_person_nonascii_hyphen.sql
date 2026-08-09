-- 0112 — 인명 en 의 비ASCII 하이픈 2건 교정 (2026-08-09)
--
-- 45차 §3-2 가 "비ASCII 하이픈 en 33건, 전수 분류가 먼저다"로 남긴 것을 분류했다.
-- 결과: **32건 중 실제 결함은 2건뿐이다.**
--
--   ’ U+2019 곡선 아포스트로피  18건  CHAN’S · It’s 2PM · ’Till I Die   ← 정상(제목 표기)
--   – U+2013 en dash             7건  … Fan Meeting Tour – The Secret Library ← 정상
--   ‘ ’ 따옴표쌍                 5건  … TOUR ‘PUREFLOW’                  ← 정상
--   ‐ U+2010 HYPHEN              1건  윤아 → Im Yoon‐ah                  ← ★결함
--   ‑ U+2011 비분리 하이픈       1건  시영준 → Si Young‑jun              ← ★결함
--
-- 제목의 곡선따옴표·en dash 는 원문 표기이므로 건드리지 않는다(45차 §1 에서 그 표기를
-- 살리려고 가드를 넓혔다 — 여기서 되돌리면 앞뒤가 맞지 않는다). 결함은 **인명**뿐이다.
--
-- ★인명 하이픈의 DB 관례는 ASCII 다: `canonical_en ~ '[a-z]-[a-z]'` 인 active person 이
-- **2,395건**이다. 이 둘만 예외다. 비분리 하이픈(U+2011)은 눈으로는 같아 보이지만
-- 검색·비교에서 다른 문자라, 같은 이름이 서로 안 맞는다.
--
-- ═══ 둘의 처리가 다르다 — 소스 우선순위가 반대다 ═══
--
-- 시영준: en=wikidata-label(prio 5) 인데 id·es=local-usage(prio 1) 에 `Si Yeong-joon` 이
--   이미 있다. 0111 의 양수경·이수형과 같은 케이스라 **더 높은 값을 승격**한다.
--   (문자만 고치면 `Si Young-jun` 이 되는데, 그건 prio5 표기를 유지하는 것이라 형제
--    locale 의 prio1 과 여전히 어긋난다.)
--
-- 윤아: en=musicbrainz(prio 4) 가 id=wikidata-label(prio 5) **보다 높다.** 값을 바꿀 근거가
--   없으므로 **하이픈 문자만 ASCII 로 정규화**하고 소스는 그대로 둔다. 결과 `Im Yoon-ah` 는
--   마침 id 칸의 값과 같아진다 — 값을 가져온 게 아니라 수렴한 것이다.
--
-- ⚠ 재발 방지 가드는 넣지 않았다. 전수 32건 중 2건이고 둘 다 외부 소스에서 들어온 것이라,
-- 지금 규모로는 레인을 만들 근거가 없다. 늘어나면 그때 판단할 것.

BEGIN;

-- ① 시영준 — local-usage(prio1) 승격
UPDATE kwave_entities
   SET canonical_en = 'Si Yeong-joon', canonical_en_source = 'local-usage', updated_at = now()
 WHERE status='active' AND operator_locked = false
   AND canonical_ko = '시영준' AND canonical_en = 'Si Young' || U&'\2011' || 'jun'
   AND canonical_id = 'Si Yeong-joon';

-- ② 윤아 — 값 유지, U+2010 만 ASCII 하이픈으로. 소스(musicbrainz, prio4)도 유지.
UPDATE kwave_entities
   SET canonical_en = replace(canonical_en, U&'\2010', '-'), updated_at = now()
 WHERE status='active' AND operator_locked = false
   AND canonical_ko = '윤아' AND canonical_en LIKE '%' || U&'\2010' || '%';

DO $$
DECLARE r record; n int;
BEGIN
  FOR r IN
    SELECT canonical_ko, canonical_en, canonical_en_source
      FROM kwave_entities WHERE canonical_ko IN ('시영준','윤아') AND status='active'
     ORDER BY canonical_ko
  LOOP
    RAISE NOTICE '0112: % → en=% (%)', r.canonical_ko, r.canonical_en, r.canonical_en_source;
  END LOOP;
  SELECT count(*) INTO n FROM kwave_entities
   WHERE status='active' AND entity_type='person' AND canonical_en ~ '[‐‑]';
  RAISE NOTICE '0112: 인명 en 에 남은 비ASCII 하이픈 %건 (0이어야 정상)', n;
END $$;

COMMIT;

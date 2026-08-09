-- 0110 — term 으로 잘못 분류된 2건을 실제 타입으로 교정한다 (2026-08-09)
--
-- ★어떻게 발견했나: 45차 §4.1 에서 mt-fill 의 타입제외 판정을 원장에 남기게 했더니,
-- 그 대상 11건 안에 `샤크라` 와 `써클 차트` 가 섞여 있는 게 눈에 띄었다. 기각 사유를
-- 기록하게 만든 변경이 **기각당하면 안 되는 건**을 드러낸 셈이다.
--
-- ★근거 (둘 다 이미 DB 안에 있던 것 — 새로 조사한 게 아니다)
--
-- 샤크라: source_urls 4개가 전부 음악 그룹 문서다.
--   ko.wikipedia /샤크라_(음악_그룹)   en /Chakra_(group)
--   es /Chakra_(grupo_musical)         zh /Chakra_(組合)
--   → 요가 용어 '차크라'와 헷갈릴 수 있어 확인했고, 근거는 만장일치로 그룹이다.
--
-- 써클 차트: 한국 음악 차트(구 가온차트). 같은 성격의 `한터차트` 가 이미
--   entity_type='brand_place' 다 — 관례를 새로 만들지 않고 그것을 따른다.
--   (참고: 엠넷·벅스=channel_outlet, 멜론·지니·플로·한터차트=brand_place)
--
-- ★부수 효과가 이 교정의 진짜 값이다: fill_input_hash 가 entity_type 을 포함하므로(0104)
-- 타입이 바뀌면 지문이 바뀌고, mt-fill 의 'type-excluded' 판정이 **자동으로 풀려**
-- 두 건이 음차채움 대상으로 복귀한다. 원장을 손으로 지울 필요가 없다.
--
-- 값(canonical_*)은 건드리지 않는다. 타입만 바꾼다.

BEGIN;

UPDATE kwave_entities
   SET entity_type = 'group', updated_at = now()
 WHERE canonical_ko = '샤크라' AND entity_type = 'term'
   AND status = 'active' AND operator_locked = false;

UPDATE kwave_entities
   SET entity_type = 'brand_place', updated_at = now()
 WHERE canonical_ko = '써클 차트' AND entity_type = 'term'
   AND status = 'active' AND operator_locked = false;

DO $$
DECLARE r record;
BEGIN
  FOR r IN
    SELECT canonical_ko, entity_type,
           (SELECT count(*) FROM kwave_kdb_enrich_attempts a
             WHERE a.entity_id = e.id AND a.field LIKE 'mt-fill%'
               AND a.input_hash = e.fill_input_hash) AS 유효판정
      FROM kwave_entities e WHERE canonical_ko IN ('샤크라','써클 차트')
     ORDER BY canonical_ko
  LOOP
    -- 유효판정 0 이어야 한다 = 지문이 바뀌어 type-excluded 가 풀렸다는 뜻.
    RAISE NOTICE '0110: % → entity_type=% · 지문일치 mt-fill 판정 %건(0이어야 정상)',
                 r.canonical_ko, r.entity_type, r.유효판정;
  END LOOP;
END $$;

COMMIT;

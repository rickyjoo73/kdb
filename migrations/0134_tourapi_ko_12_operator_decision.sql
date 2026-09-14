-- 0134: tourapi_ko/12 를 location 으로 — 운영자 결정 2026-09-14 (기준선 밖, 근거 아래)
--
-- 0132 의 기준선은 **부모 일치율 99%** 다. tourapi_ko/12 는 96.6% 로 미달이라 hold 였고,
-- 그 hold 하나가 **편집 범위 안 유형 미상 16,476건 중 9,127건**을 덮고 있었다.
--
-- 근거 셋이 모인다 — 0132 의 `aihub_tour/편의오락` 선례와 같은 형식이다.
--   ① 표본 2,033건 중 비-location 은 **70건(3.4%)**
--   ② 미상 대상 표본 300건 중 **주소 299 · 좌표 298**(99.7% / 99.3%) — 장소임을 받친다
--   ③ 같은 원본 표(tourapi_ko)의 다른 코드 7개가 전부 map 이고 6개가 location
--      (39·38·32·28·14·25, 표본 합 5만 이상)
--
-- 선례 대비:  편의오락 표본 362·어긋남 2.2%·주소 99.97%  /  여기 표본 2,033·3.4%·99.7%
--
-- ★어긋나는 3.4%는 **무작위가 아니다.** 70건 중 69건이 `work` 다 —
--   청사진이 예고한 "부동산 유산과 이동 유물 구분"이다(heritage → location.heritage_site
--   또는 work.artifact). 9,127건에 적용하면 **약 310건이 이동 유물인데 장소로 들어간다.**
--   그래도 올리는 이유는 오분류 갈래가 **알려져 있어 되돌릴 대상을 특정할 수 있기** 때문이다
--   (코드 12 + work 후보). 모르는 채 섞이는 것과 다르다.
--
-- 세부유형은 주지 않는다. 이미 분류된 2,033건이 heritage 1,170 · nature 739 로 갈린다 —
-- 코드는 "장소다"까지만 말한다(0132 의 다른 location 코드와 같은 처리).
--
-- 되돌리기: disposition 을 hold 로 되돌리고, 이 코드로 올라간 분류 근거를
--   docs/p4/apply_record_type_classification.sql 의 되돌리기 절차로 코드 단위 회수한다.
BEGIN;

INSERT INTO kentity_record_type_map
 (provider, type_code, disposition, target_entity_type, target_subtype,
  sample_size, parent_pct, subtype_pct, decided_by, reason, policy_version)
VALUES
 ('tourapi_ko','12','map','location',NULL,2033,96.6,57.6,'operator',
  '운영자 결정 2026-09-14. 기준선(부모 99%) 미달이나 근거 셋이 모인다: 표본 2,033건 중 비-location 70건(3.4%, 그중 69건이 work=이동 유물) · 미상 대상의 99.7%가 주소 보유(좌표 99.3%) · 같은 원본 표의 다른 코드 7개가 전부 map 이고 6개가 location(표본 5만+). 세부유형은 heritage/nature 로 갈려 주지 않는다. 적용 대상 9,127건 중 약 310건이 이동 유물로 오분류될 것으로 보며, 갈래가 알려져 있어 코드 단위로 되돌릴 수 있다.',
  'p4-record-type-v1')
ON CONFLICT (provider, type_code) DO UPDATE
   SET disposition = EXCLUDED.disposition, target_entity_type = EXCLUDED.target_entity_type,
       target_subtype = EXCLUDED.target_subtype, decided_by = EXCLUDED.decided_by,
       reason = EXCLUDED.reason, policy_version = EXCLUDED.policy_version;

COMMIT;

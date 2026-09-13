-- 0127_kentity_full_proper_noun_scope — 모든 고유명사 범주를 받을 수 있게 한다
--
-- 승인 근거: 운영자 지시 2026-09-13 — "tdb 의 내용을 모두 흡수해서 kdb 에서 사용될 수
-- 있도록 해야 되고, 또한 새로운 정치인·경제인·스포츠인, 제품명·상표 등 모두 고유명사를
-- 포함할 수 있도록 준비해줘."
--
-- 두 가지를 한다.
--   ① 사전에서 잠겨 있던 유형을 연다 — brand(상표)·character 가 enabled=false 였다.
--      D-14 로 "현행 Go/DB 가 11개만 쓰므로 2개는 닫아 둔다"고 시작했는데, 상표를
--      받으려면 열려 있어야 한다.
--   ② 흡수를 전량으로 넓힌다 — 선매핑에서 보류였던 유형을 **추측 없이** 받는 규칙으로 바꾼다.
--      후보의 부모 유형이 하나로 모이면 그 부모 + subtype NULL,
--      부모가 갈리면 unknown + subtype NULL 이다. 둘 다 "모른다"를 정직하게 적는 것이지
--      그럴듯한 값을 찍는 것이 아니다(분류 규칙 §1 "자유 문자열/other 로 분류 완료를 가장하지 않음").

SET lock_timeout = '5s';
SET statement_timeout = '300s';

BEGIN;

-- ============================================================ ① 잠긴 유형 열기
UPDATE kentity_types SET enabled = true, updated_at = now()
 WHERE code IN ('brand','character') AND NOT enabled;

-- 정치인·경제인·스포츠인을 받을 직군은 이미 전부 열려 있다(26/26).
-- 분야도 politics·government·economy·society·entertainment·sports·travel·culture 8종 전부 열려 있다.
-- 확인만 하고 닫힌 것이 있으면 연다.
UPDATE kentity_role_types SET enabled = true, updated_at = now() WHERE NOT enabled;
UPDATE kentity_domains    SET enabled = true, updated_at = now() WHERE NOT enabled;

-- ============================================================ ② 전량 흡수 규칙
-- 부모 유형이 하나로 모이는 것 → 그 부모 + subtype NULL (세부유형은 근거 확인 후 채운다)
UPDATE kentity_source_type_map
   SET disposition='map', target_entity_type='work', target_subtype=NULL,
       disambig_rule='원천 disambiguator (3,822건 전부 보유)',
       reason='후보가 전부 work.* 이므로 부모 유형은 확실하다. 세부유형(곡/앨범/영화/책)만 미상이라 NULL 로 둔다. 3,822건.'
 WHERE source_system='tdb' AND source_code='work';

UPDATE kentity_source_type_map
   SET disposition='map', target_entity_type='person', target_subtype='real',
       disambig_rule='QID(46%) → 없으면 구분값 없이 검수 대기',
       reason='TDB person 은 인물 목록이다. 유형은 확실하고, 주의사항은 직업 추정 금지와 동일인 판정 선행이다. 이름이 겹치는 3,604건도 각자 UUID 를 받고 자동 병합하지 않는다(I01/I05). 31,378건.'
 WHERE source_system='tdb' AND source_code='person';

-- 부모 유형이 갈리는 것 → unknown + subtype NULL (유형 검수 대기)
UPDATE kentity_source_type_map
   SET disposition='map', target_entity_type='unknown', target_subtype=NULL,
       disambig_rule='원천 disambiguator (106,572/106,602 보유)',
       reason='범주가 불명확하다. unknown 은 "유형 검수 대기"라는 뜻이며, 건수를 줄이려고 일괄 장소화하지 않는다(분류 규칙 §8). 흡수는 하되 분류는 미완으로 명시한다. 106,602건.'
 WHERE source_system='tdb' AND source_code='other';

UPDATE kentity_source_type_map
   SET disposition='map', target_entity_type='unknown', target_subtype=NULL,
       disambig_rule='구분 재료 없음 → 구분값 없이 검수 대기',
       reason='후보의 부모 유형이 갈린다(concept.food_name / product.food_product). 비고유명사 위험도 있어 유형 검수 대기로 받는다. 2,774건.'
 WHERE source_system='tdb' AND source_code='food';

UPDATE kentity_source_type_map
   SET disposition='map', target_entity_type='unknown', target_subtype=NULL,
       disambig_rule='구분 재료 없음 → 구분값 없이 검수 대기',
       reason='후보의 부모 유형이 갈린다(organization / company / team / league). 이름·출처 종류만으로 상세형을 확정하지 않는다. 2,557건.'
 WHERE source_system='tdb' AND source_code='organization';

UPDATE kentity_source_type_map
   SET disposition='map', target_entity_type='unknown', target_subtype=NULL,
       disambig_rule='구분 재료 없음 → 구분값 없이 검수 대기',
       reason='후보의 부모 유형이 갈린다(organization.school / location.campus / company.business). 1,122건.'
 WHERE source_system='tdb' AND source_code='education';

UPDATE kentity_source_type_map
   SET disposition='map', target_entity_type='unknown', target_subtype=NULL,
       disambig_rule='구분 재료 없음 → 구분값 없이 검수 대기',
       reason='후보의 부모 유형이 갈린다(location.station / transit_route / transport_facility / company). 911건.'
 WHERE source_system='tdb' AND source_code='transit';

-- ============================================================ ③ 흡수 상태를 읽는 화면용 인덱스
-- "분류 검수 대기"가 유형별로 몇 건인지 운영자가 바로 볼 수 있어야 한다.
CREATE INDEX IF NOT EXISTS kentity_entities_pending_classification
  ON kentity_entities (entity_type, origin_system)
  WHERE classification_status = 'pending';

COMMIT;

SELECT source_code, disposition, COALESCE(target_entity_type,'-') AS 목표유형,
       COALESCE(target_subtype,'(미상)') AS 세부유형
  FROM kentity_source_type_map WHERE source_system='tdb' ORDER BY disposition, source_code;

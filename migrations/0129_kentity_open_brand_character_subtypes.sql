-- 0129_kentity_open_brand_character_subtypes — 열어 놓고 막아 둔 길을 마저 연다 (D-38)
--
-- 0127 이 운영자 지시("제품명·상표 등 모두 고유명사를 포함할 수 있도록")에 따라
-- kentity_types 에서 brand·character 를 열었다. 그런데 두 유형의 **유일한 세부유형이
-- 여전히 enabled=false** 였다.
--
-- 왜 문제인가. kentity_entities_verified_classification 은 분류 확정에
-- `subtype IS NOT NULL` 을 요구한다. 쓸 수 있는 세부유형이 하나도 없으면
-- 상표를 등록할 수는 있어도 **분류를 영원히 확정할 수 없다.** 유형은 열렸는데
-- 그 유형으로 완결된 대상을 만들 수 없는, "조건은 있는데 참이 될 수 없는" 상태다.
-- 지금까지 brand·character 로 등록된 대상이 0건이라 드러나지 않았을 뿐이다.
--
-- 재발 방지는 TestEnabledTypesHaveUsableSubtype 가 맡는다 — 열린 유형에
-- 쓸 수 있는 세부유형이 없으면 회귀에서 깨진다. unknown 은 예외다(검수 대기라는 뜻이고
-- CHECK 가 애초에 entity_type<>'unknown' 을 요구한다).

SET lock_timeout = '5s';
SET statement_timeout = '60s';

BEGIN;

UPDATE kentity_subtypes SET enabled = true, updated_at = now()
 WHERE (entity_type, code) IN (('brand','commercial_brand'), ('character','fictional'))
   AND NOT enabled;

COMMIT;

SELECT t.code AS 유형, count(s.code) FILTER (WHERE s.enabled) AS 사용가능_세부유형
  FROM kentity_types t LEFT JOIN kentity_subtypes s ON s.entity_type = t.code
 WHERE t.enabled AND t.code <> 'unknown'
 GROUP BY 1 HAVING count(s.code) FILTER (WHERE s.enabled) = 0;

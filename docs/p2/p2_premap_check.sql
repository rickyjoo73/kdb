-- P2.02 선매핑 커버리지 검사 — "빠짐없이 결정됐는가"
--
-- 원장 금지: "추측·상충·비고유명사·권리 문제는 각각 보류/제외로 설명한다."
-- 즉 모든 원천 값은 map/hold/exclude 중 하나로 **명시 결정**돼야 하고, 결정되지 않은 값이
-- 남으면 안 된다. 조용히 빠진 코드가 나중에 unknown 으로 흘러드는 것을 여기서 막는다.
\set ON_ERROR_STOP on

DROP TABLE IF EXISTS p2_premap_results;
CREATE TABLE p2_premap_results(seq serial primary key, 항목 text, 값 text);

INSERT INTO p2_premap_results(항목,값)
SELECT '매핑 결정 수: map/hold/exclude',
       (SELECT count(*) FILTER (WHERE disposition='map')||' / '||
               count(*) FILTER (WHERE disposition='hold')||' / '||
               count(*) FILTER (WHERE disposition='exclude') FROM kentity_source_type_map);

-- ① KDB legacy 유형 중 결정이 없는 값
INSERT INTO p2_premap_results(항목,값)
SELECT '결정 없는 KDB legacy 유형',
       COALESCE(string_agg(t,', '),'없음') FROM (
  SELECT DISTINCT split_part(split_part(classification_reason,'legacy_subtype=',2),';',1) AS t
    FROM kentity_entities
   WHERE classification_reason LIKE 'legacy_subtype=%awaiting_p2_02_premapping%') x
 WHERE NOT EXISTS (SELECT 1 FROM kentity_source_type_map m WHERE m.source_system='kdb' AND m.source_code=x.t);

-- ② primary_role 중 결정이 없는 값
INSERT INTO p2_premap_results(항목,값)
SELECT '결정 없는 직군',
       COALESCE(string_agg(r,', '),'없음') FROM (
  SELECT DISTINCT primary_role::text AS r FROM kwave_entity_person_details WHERE primary_role IS NOT NULL) x
 WHERE NOT EXISTS (SELECT 1 FROM kentity_source_type_map m WHERE m.source_system='kdb_role' AND m.source_code=x.r);

-- ③ 목표가 사전에 없는 매핑 (FK 가 이미 막지만, 조합이 맞는지 한 번 더 본다)
INSERT INTO p2_premap_results(항목,값)
SELECT '사전에 없는 목표 조합',
       COALESCE(string_agg(source_code||'→'||target_entity_type||'.'||COALESCE(target_subtype,'(없음)'),', '),'없음')
  FROM kentity_source_type_map m
 WHERE m.disposition='map' AND m.target_subtype IS NOT NULL
   AND NOT EXISTS (SELECT 1 FROM kentity_subtypes s WHERE s.entity_type=m.target_entity_type AND s.code=m.target_subtype);

-- ④ 보류/제외에 이유가 없는 것
INSERT INTO p2_premap_results(항목,값)
SELECT '이유 없는 보류/제외',
       COALESCE(string_agg(source_system||':'||source_code,', '),'없음')
  FROM kentity_source_type_map WHERE disposition<>'map' AND btrim(reason)='';

-- ⑤ 구분값 규칙이 없는 매핑 — 흡수 시 구분값을 못 만든다(식별 계약 §1.2)
INSERT INTO p2_premap_results(항목,값)
SELECT '구분값 규칙 없는 매핑',
       COALESCE(string_agg(source_system||':'||source_code,', '),'없음')
  FROM kentity_source_type_map WHERE btrim(disambig_rule)='';

-- ⑥ 보류 상태로 남은 원본 행 수 (TDB 는 별도 DB 라 조사 문서의 실측을 쓴다)
INSERT INTO p2_premap_results(항목,값)
SELECT 'KDB 보류 유형이 덮는 행',
       (SELECT count(*)::text FROM kentity_entities e
         WHERE e.classification_reason LIKE 'legacy_subtype=%awaiting_p2_02_premapping%'
           AND EXISTS (SELECT 1 FROM kentity_source_type_map m
                        WHERE m.source_system='kdb' AND m.disposition<>'map'
                          AND m.source_code=split_part(split_part(e.classification_reason,'legacy_subtype=',2),';',1)));
INSERT INTO p2_premap_results(항목,값)
SELECT 'KDB 매핑 유형이 덮는 행',
       (SELECT count(*)::text FROM kentity_entities e
         WHERE e.classification_reason LIKE 'legacy_subtype=%awaiting_p2_02_premapping%'
           AND EXISTS (SELECT 1 FROM kentity_source_type_map m
                        WHERE m.source_system='kdb' AND m.disposition='map'
                          AND m.source_code=split_part(split_part(e.classification_reason,'legacy_subtype=',2),';',1)));

SELECT 항목, 값 FROM p2_premap_results ORDER BY seq;

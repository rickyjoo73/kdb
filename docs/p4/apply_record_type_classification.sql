-- apply_record_type_classification.sql — 원천 코드로 유형 미상을 줄인다 (P4.01)
--
-- 0132 의 kentity_record_type_map 에 따라 `entity_type='unknown'` 인 흡수분의 유형을 올린다.
-- 매핑은 TDB 자신의 분류를 교차집계해 도출한 것이고, 근거(코드·일치율·표본수)를
-- classification 근거 행으로 남겨 **코드 단위로 골라 되돌릴 수 있게** 한다.
--
-- 무엇을 하지 않는가
--   · 세부유형은 매핑이 세부까지 확정한 코드에만 준다. 나머지는 NULL 로 남긴다.
--     "부모는 알고 세부는 모른다"를 그대로 적는 것이지 그럴듯한 값을 찍지 않는다.
--   · classification_status 는 'pending' 그대로 둔다. 유형을 올린 것이지 **분류를
--     확정한 것이 아니다**. 확정은 kentity_entities_verified_classification 이 요구하는
--     세부유형·근거·검수자·시각을 모두 갖췄을 때만이다.
--   · 코드가 여럿이라 목표 유형이 갈리는 대상은 건드리지 않는다(검수 대기).
--   · hold 코드는 적용하지 않는다.
--   · 운영자 잠금 대상은 건드리지 않는다.
--
-- 적재대. 코드는 TDB 쪽에 있으므로 p4_tdb_record_type 적재대를 거친다(P2/P3 와 같은 방식).
--
-- 실행: psql -v ON_ERROR_STOP=1 -v batch=20000 -f apply_record_type_classification.sql

\set ON_ERROR_STOP on
\if :{?batch}
\else
  \set batch 20000
\endif

SET lock_timeout = '5s';
SET statement_timeout = '600s';

BEGIN;

-- ① 목표 유형이 하나로 모이는 미상 대상만 고른다.
-- 창 함수를 섞으면 읽기 어렵고 틀리기도 쉬워서, 집계로 판단하고 대표 코드만 따로 붙인다.
CREATE TEMP TABLE tgt ON COMMIT DROP AS
WITH cand AS (
  SELECT c.entity_id, m.target_entity_type, m.target_subtype,
         m.provider, m.type_code, m.sample_size, m.parent_pct, m.subtype_pct, m.decided_by
    FROM p4_tdb_record_type s
    JOIN kentity_crosswalks c
      ON c.source_system = 'tdb' AND c.source_table = 'tdb_places' AND c.source_id = s.tdb_id::text
    JOIN kentity_entities e
      ON e.id = c.entity_id AND e.entity_type = 'unknown'
     AND e.write_owner = 'native' AND NOT e.operator_locked
    JOIN kentity_record_type_map m
      ON m.provider = s.source_code AND m.type_code = s.type_code AND m.disposition = 'map'
), agg AS (
  SELECT entity_id,
         min(target_entity_type) AS parent,
         count(DISTINCT target_subtype) FILTER (WHERE target_subtype IS NOT NULL) AS subtype_kinds,
         min(target_subtype) FILTER (WHERE target_subtype IS NOT NULL) AS subtype
    FROM cand
   GROUP BY entity_id
  HAVING count(DISTINCT target_entity_type) = 1   -- 부모가 갈리면 건드리지 않는다
)
SELECT a.entity_id,
       a.parent AS target_entity_type,
       -- 세부유형은 후보가 **하나로 모일 때만** 준다. 갈리면 NULL — 모른다를 적는다.
       CASE WHEN a.subtype_kinds = 1 THEN a.subtype ELSE NULL END AS target_subtype,
       b.provider, b.type_code, b.sample_size, b.parent_pct, b.subtype_pct, b.decided_by,
       gen_random_uuid() AS ev_id
  FROM agg a
  JOIN LATERAL (SELECT c.provider, c.type_code, c.sample_size, c.parent_pct, c.subtype_pct, c.decided_by
                  FROM cand c WHERE c.entity_id = a.entity_id
                 ORDER BY c.sample_size DESC, c.provider, c.type_code LIMIT 1) b ON true
 ORDER BY a.entity_id
 LIMIT :batch;

-- ② 분류 근거. 무엇을 보고 올렸는지 한 행에 담는다.
INSERT INTO kentity_evidence
  (id, entity_id, provider, source_record_id, source_url, claim_type, license_code,
   export_allowed, status, verified_by, verified_at, summary,
   claim_fingerprint, source_observation_hash, claim_payload, independent_origin, source_policy_id)
SELECT t.ev_id, t.entity_id, t.provider,
       coalesce(o.source_record_id, ''), coalesce(o.source_url, ''),
       'classification', p.license_code, true, 'verified',
       CASE WHEN t.decided_by = 'operator' THEN 'operator' ELSE 'policy:record-type-map-v1' END,
       now(),
       '원천 레코드 코드로 유형 판정 — TDB 자체 분류 교차집계에서 도출',
       md5(t.provider || '|' || t.type_code || '|' || t.target_entity_type ||
           '|' || coalesce(t.target_subtype, '')),
       coalesce(o.source_observation_hash, ''),
       jsonb_build_object('provider', t.provider::text, 'type_code', t.type_code::text,
                          'target_entity_type', t.target_entity_type::text,
                          'target_subtype', coalesce(t.target_subtype, '')::text,
                          'sample_size', t.sample_size,
                          'parent_pct', t.parent_pct, 'subtype_pct', t.subtype_pct,
                          'decided_by', t.decided_by::text),
       t.provider, p.id
  FROM tgt t
  JOIN kentity_source_policies p
    ON p.provider = t.provider AND p.status = 'approved'
  LEFT JOIN LATERAL (
        SELECT v.source_record_id, v.source_url, v.source_observation_hash
          FROM kentity_evidence v
         WHERE v.entity_id = t.entity_id AND v.claim_type = 'identity' AND v.status = 'verified'
         ORDER BY (v.provider = 'tdb') DESC, v.observed_at
         LIMIT 1) o ON true;

-- ③ 유형을 올린다. classification_status 는 손대지 않는다 — 확정이 아니라 판정이다.
UPDATE kentity_entities e
   SET entity_type              = t.target_entity_type,
       subtype                  = t.target_subtype,
       classification_evidence_id = t.ev_id,
       classification_reason    = '원천 코드 ' || t.provider || ':' || t.type_code ||
                                  ' (표본 ' || t.sample_size || ', 부모 ' || t.parent_pct || '%)',
       revision                 = e.revision + 1,
       updated_at               = now()
  FROM tgt t
 WHERE e.id = t.entity_id
   AND e.entity_type = 'unknown';

SELECT ' 대상' AS 구분, count(*) AS 수 FROM tgt
UNION ALL SELECT ' 세부유형까지', count(*) FROM tgt WHERE target_subtype IS NOT NULL;

COMMIT;

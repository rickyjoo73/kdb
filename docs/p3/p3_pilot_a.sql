-- P3 파일럿 A batch 1 — active person 100명의 직업(person_roles) 채움
--
-- 승인 근거: docs/KDB_P3_FIRST_BATCH_PLAN.md §1 (운영자 승인 2026-09-13).
-- 선행: 파일럿 B batch 1·2 완료, p3_guards.sql 적용.
--
-- **직업은 정체성이 아니다**(식별 계약 §1.1). 한 사람이 배우이자 가수이면 role 행이
-- 둘일 뿐 UUID 는 하나다. 이 작업은 그 여러 행을 만드는 것이지 ID 를 나누는 것이 아니다.
--
-- 하는 것: person_roles 채움. 근거는 운영자가 유지해 온 legacy 인물 상세다.
-- 안 하는 것: 이름·표기 변경 없음. 병합 없음. classification_status 변경 없음
--            (legacy 소유 행이라 감사된 소유권 전환 없이는 쓸 수 없다 — A01).

\set ON_ERROR_STOP on
BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '600s';

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM kentity_migration_runs WHERE request_key='p3-pilot-b-batch-2' AND state='completed') THEN
    RAISE EXCEPTION '파일럿 B 가 끝나지 않았다 — 순서를 건너뛸 수 없다';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM kentity_source_policies WHERE provider='operator' AND status='approved') THEN
    RAISE EXCEPTION 'operator 정책이 승인돼 있지 않다';
  END IF;
END $$;

-- ============================================================ 1. 대상 선정
-- 우선순위: 직군이 'other' 가 아니고(정보 없음을 직업으로 승인하지 않는다),
--           안정 식별자(QID)가 있고, 차단 guard 가 걸리지 않은 사람.
-- 결정적으로 고른다(id 정렬) — 같은 입력이면 같은 100명이 나온다.
CREATE TEMP TABLE pa_target ON COMMIT DROP AS
SELECT e.id AS entity_id, e.canonical_ko,
       d.primary_role::text AS primary_role,
       COALESCE(d.secondary_roles::text[], '{}'::text[]) AS secondary_roles
  FROM kwave_entities e
  JOIN kwave_entity_person_details d ON d.entity_id = e.id
 WHERE e.entity_type = 'person' AND e.status = 'active'
   AND d.primary_role IS NOT NULL AND d.primary_role::text NOT IN ('other','fictional')
   AND EXISTS (SELECT 1 FROM kwave_entity_external_refs r
                WHERE r.entity_id = e.id AND r.provider = 'wikidata')
   -- 감사에서 의심으로 막힌 대상은 넣지 않는다. 막아 놓고 값을 채우면 앞뒤가 안 맞는다.
   AND NOT EXISTS (SELECT 1 FROM kentity_source_guards g
                    WHERE g.rejected_entity_id = e.id AND g.state = 'active')
   AND NOT EXISTS (SELECT 1 FROM kentity_person_roles pr WHERE pr.entity_id = e.id)
 ORDER BY e.id
 LIMIT 100;

-- 사람×직업으로 편다. 주직업과 부직업을 합치고 중복은 없앤다
-- (같은 (사람,직업)이 둘이면 verified EXCLUDE 가 거부한다 — 그게 맞는 동작이다).
CREATE TEMP TABLE pa_role ON COMMIT DROP AS
SELECT DISTINCT t.entity_id, t.canonical_ko, r.role_code, gen_random_uuid() AS evidence_id
  FROM pa_target t
  CROSS JOIN LATERAL (
    SELECT t.primary_role AS role_code
    UNION
    SELECT unnest(t.secondary_roles)
  ) r
 WHERE btrim(COALESCE(r.role_code,'')) <> ''
   AND r.role_code NOT IN ('other','fictional')
   -- 목표 사전에 있는 코드만. 없는 코드는 추측해서 매핑하지 않는다.
   AND EXISTS (SELECT 1 FROM kentity_role_types rt WHERE rt.code = r.role_code);

-- ============================================================ 2. run 등록
INSERT INTO kentity_migration_runs
 (id, owner_key, request_key, request_hash, mode, state,
  mapper_version, canonicalization_version, source_basis, cohort_hash, selection_policy_hash,
  expected_records, expected_entities, max_changed_objects, observed_at,
  approved_by, approved_at, approval_ref)
SELECT '7f00a001-0000-4000-8000-000000000001','operator','p3-pilot-a-batch-1',
       md5(md5('p3-pilot-a-batch-1'))||md5('v1'),'apply','running',
       'kdb-person-roles-v1','nfc-v1',
       jsonb_build_object('source_system','kdb','source_table','kwave_entity_person_details',
                          'rows',(SELECT count(*) FROM pa_target)),
       md5(md5('pilot-a'))||md5('cohort-a1'),
       md5(md5('tdb-premap-v1'))||md5('policy-a1'),
       (SELECT count(*) FROM pa_target), (SELECT count(*) FROM pa_target),
       (SELECT count(*)*2 FROM pa_role), now(),
       'operator', now(), 'KDB_P3_FIRST_BATCH_PLAN.md'
 WHERE NOT EXISTS (SELECT 1 FROM kentity_migration_runs WHERE id='7f00a001-0000-4000-8000-000000000001');

INSERT INTO kentity_migration_records
 (run_id, source_system, source_table, source_pk, source_fingerprint, source_state, source_observed_at,
  disposition, state, target_mode, planned_target_id, target_entity_id,
  expected_entity_revision, expected_identity_revision, expected_owner,
  plan_hash, expected_object_count, reason_code)
SELECT '7f00a001-0000-4000-8000-000000000001','kdb','kwave_entity_person_details',
       jsonb_build_object('entity_id', t.entity_id::text),
       md5(md5(t.entity_id::text))||md5('pilot-a'),
       'present', now(), 'include', 'validated', 'existing', t.entity_id, t.entity_id,
       (SELECT revision FROM kentity_entities WHERE id=t.entity_id),
       (SELECT identity_revision FROM kentity_entities WHERE id=t.entity_id),
       'kdb',
       md5(md5(t.entity_id::text))||md5('roles'),
       (SELECT count(*)*2 FROM pa_role r WHERE r.entity_id=t.entity_id),
       'operator_maintained_person_detail'
  FROM pa_target t
 WHERE NOT EXISTS (SELECT 1 FROM kentity_migration_records r
                    WHERE r.run_id='7f00a001-0000-4000-8000-000000000001'
                      AND r.source_pk->>'entity_id' = t.entity_id::text);

-- ============================================================ 3. 근거 — 직업 주장 하나당 하나
-- 원천은 운영자가 유지해 온 legacy 인물 상세다. 그 기록의 실제 위치를 source_url 로 남긴다.
INSERT INTO kentity_evidence
 (id, entity_id, provider, source_record_id, source_url, claim_type, status,
  license_code, export_allowed, verified_by, verified_at, summary,
  claim_fingerprint, source_observation_hash, independent_origin, claim_payload, source_policy_id)
SELECT r.evidence_id, r.entity_id, 'operator', r.entity_id::text,
       'https://kdb.aiinplanet.com/admin/entities/' || r.entity_id::text,
       'occupation', 'verified', 'internal-operator-review', true, 'operator', now(),
       r.canonical_ko || ' 의 직업: ' || r.role_code,
       md5(r.entity_id::text || 'occupation' || r.role_code),
       md5(r.entity_id::text || r.role_code), 'operator',
       jsonb_build_object('role_code', r.role_code, 'source', 'kwave_entity_person_details'),
       (SELECT id FROM kentity_source_policies WHERE provider='operator' AND status='approved' LIMIT 1)
  FROM pa_role r;

-- ============================================================ 4. 직업 행
-- 기간은 모른다. 관측 시각을 재임 시작으로 넣지 않는다(I12) — precision 은 unknown 이다.
INSERT INTO kentity_person_roles
 (entity_id, entity_type, role_code, status, evidence_id,
  assigned_by, reason, policy_version, verified_by, verified_at)
SELECT r.entity_id, 'person', r.role_code, 'verified', r.evidence_id,
       'operator',
       'P3 파일럿 A: 운영자가 유지해 온 인물 상세의 직군을 공통 모델로 옮긴다. 기간 미상.',
       'kdb-person-roles-v1', 'operator', now()
  FROM pa_role r;

-- ============================================================ 5. 결과 기록
UPDATE kentity_migration_records m
   SET state='applied', applied_at=now(),
       actual_object_count = m.expected_object_count,
       applied_result_hash = md5(md5(m.source_pk->>'entity_id'))||md5('applied'),
       revision = m.revision+1, updated_at=now()
 WHERE m.run_id='7f00a001-0000-4000-8000-000000000001';

UPDATE kentity_migration_runs
   SET state='completed', finished_at=now(),
       expected_records=(SELECT count(*) FROM kentity_migration_records WHERE run_id='7f00a001-0000-4000-8000-000000000001')
 WHERE id='7f00a001-0000-4000-8000-000000000001';

COMMIT;

-- 확인
SELECT '대상 인물' AS 항목, count(DISTINCT entity_id)::text AS 값 FROM kentity_person_roles
UNION ALL SELECT '직업 행', count(*)::text FROM kentity_person_roles
UNION ALL SELECT '  그중 verified', count(*)::text FROM kentity_person_roles WHERE status='verified'
UNION ALL SELECT '겸업(2개 이상)', count(*)::text FROM (SELECT entity_id FROM kentity_person_roles GROUP BY 1 HAVING count(*)>1) x
UNION ALL SELECT '★근거가 다른 사람 것(0이어야)', count(*)::text FROM kentity_person_roles pr JOIN kentity_evidence v ON v.id=pr.evidence_id WHERE v.entity_id<>pr.entity_id;

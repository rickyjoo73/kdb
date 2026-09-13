-- P3 파일럿 B batch 2 — admin_region 나머지 121행 적재
--
-- batch 1 이 계상·표본·제약을 모두 통과한 뒤에만 실행한다(계획 §8-6).
--
-- 승인 근거: docs/KDB_P3_FIRST_BATCH_PLAN.md (운영자 승인 2026-09-13).
-- 선행 조건: 0123/0124 적용, p3_guards.sql 실행, p2_tdb_source 에 admin_region 적재.
--
-- **적재이지 서빙 전환이 아니다.** 엔티티는 candidate 로 들어간다. 소비자 API 는 아직
-- legacy(kwave_entities)를 보므로 서빙에 변화가 없다 — 전환은 P5 의 일이다.
-- active 승격은 운영자가 화면에서 검수한 뒤에 한다.
--
-- 대상 1건당 만드는 객체 7개: 원본 binding(shadow) · 정체성 근거 · 이름 근거 ·
--                            Entity · 이름 · 외부 ID · 연결(crosswalk)

\set ON_ERROR_STOP on
BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '600s';

-- ============================================================ 0. 선행 조건 확인
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM kentity_migration_runs
                  WHERE request_key='p3-pilot-b-batch-1' AND state='completed') THEN
    RAISE EXCEPTION 'batch 1 이 완료되지 않았다 — 순서를 건너뛸 수 없다';
  END IF;
  IF (SELECT count(*) FROM p2_tdb_source WHERE place_type='admin_region') = 0 THEN
    RAISE EXCEPTION '적재대에 admin_region 이 없다 — 원본 추출을 먼저 하라';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM kentity_source_policies WHERE provider='wikidata' AND status='approved' AND name_export_allowed) THEN
    RAISE EXCEPTION 'wikidata 정책이 승인돼 있지 않다 — 이름을 공급할 수 없다';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM kentity_source_guards WHERE guard_kind='rejected_binding') THEN
    RAISE EXCEPTION '보호 규칙이 먼저 실행되지 않았다 — 흡수가 오연결을 승계한다';
  END IF;
END $$;

-- ============================================================ 1. run 등록 (mode=apply, 승인 필수)
INSERT INTO kentity_migration_runs
 (id, owner_key, request_key, request_hash, mode, state,
  mapper_version, canonicalization_version, source_basis, cohort_hash, selection_policy_hash,
  expected_records, expected_entities, max_changed_objects, observed_at,
  approved_by, approved_at, approval_ref)
SELECT '7f000002-0000-4000-8000-000000000002','operator','p3-pilot-b-batch-2',
       md5(md5('p3-pilot-b-batch-2'))||md5('v1'),'apply','running',
       'tdb-places-mapper-v1','nfc-v1',
       jsonb_build_object('source_system','tdb','source_table','tdb_places','place_type','admin_region',
                          'rows',(SELECT count(*) FROM p2_tdb_source WHERE place_type='admin_region')),
       md5(md5('admin_region'))||md5('cohort-b2'),
       md5(md5('tdb-selection-v1'))||md5('policy-b2'),
       (SELECT count(*) FROM p2_tdb_source WHERE place_type='admin_region'), 121, 1000,
       (SELECT max(source_updated_at) FROM p2_tdb_source WHERE place_type='admin_region'),
       'operator', now(), 'KDB_P3_FIRST_BATCH_PLAN.md'
 WHERE NOT EXISTS (SELECT 1 FROM kentity_migration_runs WHERE id='7f000002-0000-4000-8000-000000000002');

-- ============================================================ 2. 전량 계상 (249행 — 보류도 적는다)
CREATE TEMP TABLE b1_decision ON COMMIT DROP AS
WITH base AS (
  SELECT s.*,
         COALESCE(NULLIF(btrim(s.disambiguator),''),
                  COALESCE(NULLIF(btrim(s.sigungu_code),''), '광역자치단체')) AS built_disambig,
         EXISTS (SELECT 1 FROM kwave_entities e WHERE e.status<>'rejected' AND e.canonical_ko=s.name_ko) AS kdb_clash
    FROM p2_tdb_source s WHERE s.place_type='admin_region')
SELECT *,
  CASE WHEN status<>'active' THEN 'exclude_operational'
       WHEN qid='' THEN 'conditional'
       WHEN built_disambig IS NULL THEN 'conditional'
       WHEN kdb_clash THEN 'conditional'
       ELSE 'include' END AS disposition,
  CASE WHEN status<>'active' THEN 'source_not_active'
       WHEN qid='' THEN 'no_stable_identity_anchor'
       WHEN built_disambig IS NULL THEN 'no_disambiguator_cannot_be_built'
       WHEN kdb_clash THEN 'name_clashes_with_existing_kdb_entity'
       ELSE 'mapped_with_disambiguator' END AS reason_code
  FROM base;

-- batch 1 대상: include 중 앞 121건을 **결정적으로** 고른다(tdb_id 정렬).
CREATE TEMP TABLE b1_target ON COMMIT DROP AS
SELECT *, gen_random_uuid() AS new_entity_id, gen_random_uuid() AS shadow_id,
       gen_random_uuid() AS ev_identity_id, gen_random_uuid() AS ev_name_id
  FROM b1_decision d WHERE d.disposition='include'
   -- batch 1 이 이미 가져간 원본은 제외한다. 같은 원본을 두 번 적재하지 않는다.
   AND NOT EXISTS (SELECT 1 FROM kentity_crosswalks c
                    WHERE c.source_system='tdb' AND c.source_table='tdb_places'
                      AND c.source_id = d.tdb_id::text)
   ORDER BY d.tdb_id LIMIT 121;

INSERT INTO kentity_migration_records
 (run_id, source_system, source_table, source_pk, source_fingerprint, source_state, source_observed_at,
  disposition, state, target_mode, planned_target_id,
  expected_entity_revision, expected_identity_revision, expected_owner,
  plan_hash, expected_object_count, reason_code)
SELECT '7f000002-0000-4000-8000-000000000002','tdb','tdb_places',
       jsonb_build_object('id', d.tdb_id::text),
       md5(md5(d.tdb_id::text))||md5(COALESCE(d.source_updated_at,'epoch'::timestamptz)::text),
       'present', d.source_updated_at, d.disposition,
       CASE WHEN t.tdb_id IS NOT NULL THEN 'validated' ELSE 'held' END,
       CASE WHEN t.tdb_id IS NOT NULL THEN 'create' ELSE 'none' END,
       t.new_entity_id,
       CASE WHEN t.tdb_id IS NOT NULL THEN 0 END,
       CASE WHEN t.tdb_id IS NOT NULL THEN 0 END,
       CASE WHEN t.tdb_id IS NOT NULL THEN 'native' END,
       md5(md5(d.tdb_id::text))||md5(d.reason_code),
       CASE WHEN t.tdb_id IS NOT NULL THEN 7 ELSE 0 END,
       CASE WHEN t.tdb_id IS NOT NULL THEN d.reason_code ELSE d.reason_code||'_not_in_batch_2' END
  FROM b1_decision d LEFT JOIN b1_target t ON t.tdb_id = d.tdb_id
 WHERE NOT EXISTS (SELECT 1 FROM kentity_migration_records r
                    WHERE r.run_id='7f000002-0000-4000-8000-000000000002'
                      AND r.source_pk->>'id' = d.tdb_id::text);

-- ============================================================ 3. 객체 생성
-- 3.1 원본 binding — tdb 연결은 이것 없이 만들 수 없다(kentity_tdb_binding_required)
INSERT INTO kentity_tdb_shadows
 (id, tdb_id, qid, source_fingerprint, link_method, link_score, source_observed_at,
  policy_version, state, created_by, reason)
SELECT t.shadow_id, t.tdb_id, t.qid,
       md5(md5(t.tdb_id::text))||md5(t.qid), 'operator', 1.0,
       COALESCE(t.source_updated_at, now()), 'tdb-premap-v1', 'review', 'operator',
       'P3 파일럿 B batch 2 — 운영자 승인 범위'
  FROM b1_target t
 WHERE NOT EXISTS (SELECT 1 FROM kentity_tdb_shadows s WHERE s.tdb_id=t.tdb_id AND s.qid=t.qid);

-- 3.2 Entity — candidate 로 들어간다(적재이지 서빙 전환이 아니다)
INSERT INTO kentity_entities
 (id, entity_type, subtype, canonical_ko, origin_system, write_owner, status,
  classification_status, classification_reason, classified_by, classified_at)
SELECT t.new_entity_id, 'location', 'admin_region', t.name_ko, 'tdb', 'native', 'candidate',
       'pending',
       'P3 파일럿 B: TDB admin_region 선매핑(tdb-premap-v1). 구분값=' || t.built_disambig,
       'operator', now()
  FROM b1_target t;

-- 3.3 정체성 근거 (claim_type=identity)
INSERT INTO kentity_evidence
 (id, entity_id, provider, source_record_id, source_url, claim_type, status,
  license_code, export_allowed, verified_by, verified_at, summary,
  claim_fingerprint, source_observation_hash, independent_origin, source_policy_id)
SELECT t.ev_identity_id, t.new_entity_id, 'wikidata', t.qid,
       'https://www.wikidata.org/wiki/' || t.qid, 'identity', 'verified',
       'CC0-1.0', true, 'operator', now(),
       'TDB admin_region ' || t.tdb_id::text || ' 가 보유한 wikidata 앵커',
       md5(t.qid || 'identity'), md5(t.tdb_id::text || t.qid), 'wikidata',
       (SELECT id FROM kentity_source_policies WHERE provider='wikidata' AND status='approved' LIMIT 1)
  FROM b1_target t;

-- 3.4 이름 근거 (claim_type=name) — 이름 verified 는 이름 주장 근거를 따로 요구한다(S02)
INSERT INTO kentity_evidence
 (id, entity_id, provider, source_record_id, source_url, claim_type, status,
  license_code, export_allowed, verified_by, verified_at, summary,
  claim_fingerprint, source_observation_hash, independent_origin, source_policy_id)
SELECT t.ev_name_id, t.new_entity_id, 'wikidata', t.qid,
       'https://www.wikidata.org/wiki/' || t.qid, 'name', 'verified',
       'CC0-1.0', true, 'operator', now(),
       '원본 한국어 명칭 ' || t.name_ko,
       md5(t.qid || 'name' || t.name_ko), md5(t.tdb_id::text || t.qid || 'name'), 'wikidata',
       (SELECT id FROM kentity_source_policies WHERE provider='wikidata' AND status='approved' LIMIT 1)
  FROM b1_target t;

-- 3.5 이름
INSERT INTO kentity_names
 (entity_id, locale, value, kind, form, status, evidence_id, source_code,
  policy_version, verification_method)
SELECT t.new_entity_id, 'ko', t.name_ko, 'canonical', 'recorded', 'verified',
       t.ev_name_id, 'wikidata-label', 'tdb-premap-v1', 'source-record'
  FROM b1_target t;

-- 3.6 외부 ID (S04 가 단일 소유를 강제한다)
INSERT INTO kentity_external_ids
 (entity_id, provider, external_id, status, evidence_id, policy_version)
SELECT t.new_entity_id, 'wikidata', t.qid, 'verified', t.ev_identity_id, 'tdb-premap-v1'
  FROM b1_target t;

INSERT INTO kentity_id_reservations (provider, external_id, entity_id)
SELECT 'wikidata', t.qid, t.new_entity_id FROM b1_target t
ON CONFLICT (provider, external_id) DO NOTHING;

-- 3.7 연결 — 원본과 공통 대상을 잇는다
INSERT INTO kentity_crosswalks
 (source_system, source_table, source_id, entity_id, status, target_identity_revision,
  mapping_policy_version, reason, evidence_id, decided_by, decided_at,
  source_binding_id, source_state, source_observed_at)
SELECT 'tdb','tdb_places', t.tdb_id::text, t.new_entity_id, 'confirmed',
       (SELECT identity_revision FROM kentity_entities WHERE id=t.new_entity_id),
       'tdb-premap-v1',
       'P3 파일럿 B batch 2: QID 앵커와 행정 코드로 확정. 운영자 승인 범위.',
       t.ev_identity_id, 'operator', now(), t.shadow_id, 'present',
       COALESCE(t.source_updated_at, now())
  FROM b1_target t;

-- ============================================================ 4. 적재 결과 기록
UPDATE kentity_migration_records r
   SET state='applied', applied_at=now(), actual_object_count=7,
       applied_result_hash = md5(md5(r.source_pk->>'id'))||md5('applied'),
       target_entity_id = r.planned_target_id, revision = r.revision+1, updated_at=now()
 WHERE r.run_id='7f000002-0000-4000-8000-000000000002'
   AND r.source_pk->>'id' IN (SELECT tdb_id::text FROM b1_target);

UPDATE kentity_migration_runs
   SET state='completed', finished_at=now(),
       expected_records=(SELECT count(*) FROM kentity_migration_records WHERE run_id='7f000002-0000-4000-8000-000000000002')
 WHERE id='7f000002-0000-4000-8000-000000000002';

COMMIT;

-- ============================================================ 확인
SELECT '적재 Entity' AS 항목, count(*)::text AS 값 FROM kentity_entities WHERE origin_system='tdb'
UNION ALL SELECT '연결', count(*)::text FROM kentity_crosswalks WHERE source_system='tdb'
UNION ALL SELECT '외부 ID', count(*)::text FROM kentity_external_ids x JOIN kentity_entities e ON e.id=x.entity_id WHERE e.origin_system='tdb'
UNION ALL SELECT '기록(전량 계상)', count(*)::text FROM kentity_migration_records WHERE run_id='7f000002-0000-4000-8000-000000000002'
UNION ALL SELECT '그중 applied', count(*)::text FROM kentity_migration_records WHERE run_id='7f000002-0000-4000-8000-000000000002' AND state='applied';

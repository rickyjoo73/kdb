-- P2.03/P2.04 dry-run 변환기 — 승인된 필드와 필요한 근거만 읽는다
--
-- 격리 복원본에서만 실행한다. 운영 DB 와 원본 DB 에 쓰기 없음.
-- 입력: p2_tdb_source (적재대) + kentity_source_type_map (선매핑) + KDB 현행 엔티티
-- 출력: kentity_migration_runs 1행 + kentity_migration_records 전량
--
-- 원칙 셋:
--  1. **전량 계상.** 원본 1행 = 기록 1행. 제외도 기록한다. 안 적으면 누락과 구분이 안 된다.
--  2. **추측 금지.** 목표를 못 정하면 target_mode='none' + held 다. 그럴듯한 유형을 찍지 않는다.
--  3. **구분값 필수.** 흡수 대상이 되려면 구분값이 있어야 한다(식별 계약 §1.2).
--     만들 수 없으면 held — 지어내지 않는다.

\set ON_ERROR_STOP on
BEGIN;

DELETE FROM kentity_migration_records WHERE run_id IN
  (SELECT id FROM kentity_migration_runs WHERE request_key = 'p2-dryrun-tdb-places');
DELETE FROM kentity_migration_runs WHERE request_key = 'p2-dryrun-tdb-places';

-- ============================================================ 1. run 등록
INSERT INTO kentity_migration_runs
 (id, owner_key, request_key, request_hash, mode, state,
  mapper_version, canonicalization_version, source_basis, cohort_hash, selection_policy_hash,
  expected_records, expected_entities, max_changed_objects, observed_at)
SELECT '7d000001-0000-4000-8000-000000000001','operator','p2-dryrun-tdb-places',
       md5(md5('p2-dryrun-tdb-places'))||md5('v1'),   -- 64 hex
       'dry_run','running','tdb-places-mapper-v1','nfc-v1',
       jsonb_build_object('source_system','tdb','source_table','tdb_places',
                          'rows',(SELECT count(*) FROM p2_tdb_source),
                          'max_source_updated_at',(SELECT max(source_updated_at) FROM p2_tdb_source)),
       md5(md5((SELECT count(*)::text FROM p2_tdb_source)))||md5('cohort'),
       md5(md5('tdb-selection-v1'))||md5('policy'),
       (SELECT count(*) FROM p2_tdb_source), 0, 0,
       (SELECT max(source_updated_at) FROM p2_tdb_source);

-- ============================================================ 2. 판정
-- 구분값을 실제로 만들어 본다. 못 만들면 그 사실이 곧 보류 사유가 된다.
CREATE TEMP TABLE p2_decision AS
WITH base AS (
  SELECT s.*,
         m.disposition AS map_disposition,
         m.target_entity_type, m.target_subtype, m.alt_targets,
         -- 구분값: 원천 값 → 없으면 유형별 규칙 → 그래도 없으면 NULL
         COALESCE(
           NULLIF(btrim(s.disambiguator),''),
           CASE s.place_type
             WHEN 'legal_dong'   THEN NULLIF(btrim(s.ldong_code),'')
             WHEN 'admin_region' THEN NULLIF(btrim(s.sigungu_code), '')
             WHEN 'heritage'     THEN NULLIF(btrim(s.heritage_no),'')
             ELSE NULLIF(btrim(split_part(s.addr_ko,' ',1)||' '||split_part(s.addr_ko,' ',2)),'')
           END
         ) AS built_disambig
    FROM p2_tdb_source s
    LEFT JOIN kentity_source_type_map m
      ON m.source_system = 'tdb' AND m.source_code = s.place_type
), judged AS (
  SELECT b.*,
         -- KDB 쪽에 같은 이름이 이미 있는가(동일성 판정이 선행돼야 하는 구간)
         EXISTS (SELECT 1 FROM kwave_entities e
                  WHERE e.status <> 'rejected' AND e.canonical_ko = b.name_ko) AS kdb_name_clash
    FROM base b
)
SELECT tdb_id, place_type, name_ko, built_disambig, qid, source_updated_at,
       target_entity_type, target_subtype, kdb_name_clash,
       CASE
         WHEN status <> 'active'            THEN 'exclude_operational'
         WHEN map_disposition IS NULL       THEN 'conditional'   -- 선매핑에 없는 코드
         WHEN map_disposition = 'exclude'   THEN 'exclude_operational'
         WHEN map_disposition = 'hold'      THEN 'conditional'
         WHEN built_disambig IS NULL        THEN 'conditional'
         WHEN kdb_name_clash                THEN 'conditional'
         ELSE 'include'
       END AS disposition,
       CASE
         WHEN status <> 'active'            THEN 'source_not_active'
         WHEN map_disposition IS NULL       THEN 'no_premap_for_source_type'
         WHEN map_disposition = 'exclude'   THEN 'premap_excluded'
         WHEN map_disposition = 'hold'      THEN 'premap_held_needs_evidence'
         WHEN built_disambig IS NULL        THEN 'no_disambiguator_cannot_be_built'
         WHEN kdb_name_clash                THEN 'name_clashes_with_existing_kdb_entity'
         ELSE 'mapped_with_disambiguator'
       END AS reason_code
  FROM judged;

-- ============================================================ 3. 기록 적재 (전량)
INSERT INTO kentity_migration_records
 (run_id, source_system, source_table, source_pk, source_fingerprint,
  source_state, source_observed_at, disposition, state, target_mode, planned_target_id,
  expected_entity_revision, expected_identity_revision, expected_owner,
  plan_hash, expected_object_count, reason_code)
SELECT '7d000001-0000-4000-8000-000000000001','tdb','tdb_places',
       jsonb_build_object('id', d.tdb_id::text),
       md5(md5(d.tdb_id::text))||md5(COALESCE(d.source_updated_at,'epoch'::timestamptz)::text),
       'present', d.source_updated_at, d.disposition,
       CASE WHEN d.disposition = 'include' THEN 'validated' ELSE 'held' END,
       -- include 만 새 대상을 계획한다. 나머지는 목표를 만들지 않는다(추측 금지).
       CASE WHEN d.disposition = 'include' THEN 'create' ELSE 'none' END,
       CASE WHEN d.disposition = 'include' THEN gen_random_uuid() ELSE NULL END,
       CASE WHEN d.disposition = 'include' THEN 0 ELSE NULL END,
       CASE WHEN d.disposition = 'include' THEN 0 ELSE NULL END,
       CASE WHEN d.disposition = 'include' THEN 'native' ELSE NULL END,
       md5(md5(d.tdb_id::text))||md5(d.reason_code),
       CASE WHEN d.disposition = 'include' THEN 1 ELSE 0 END,
       d.reason_code
  FROM p2_decision d;

UPDATE kentity_migration_runs
   SET expected_entities = (SELECT count(*) FROM p2_decision WHERE disposition='include'),
       max_changed_objects = (SELECT count(*) FROM p2_decision WHERE disposition='include')
 WHERE id = '7d000001-0000-4000-8000-000000000001';

COMMIT;

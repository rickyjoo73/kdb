-- P3.06 확대 — 자체 ID 로 묶어 TDB 를 흡수한다
--
-- 승인 근거: 운영자 지시 2026-09-13 — "kdb 구조를 새롭게 구축했으니 tdb 가 맞게 들어와야지",
--            "wikidata QID 는 보조 역할", "있는 것을 제대로 통합하고 확장에 집중해".
--
-- 앵커는 **원본 자기 ID** 다. (tdb, tdb_places, <uuid>) 의 관측 기록이 1차 binding 이고,
-- wikidata QID 는 **있을 때만** 외부 식별자로 얹는다(보조).
--
-- 사용법: psql -v ptype=legal_dong -v lim=1000 -v runid=<uuid> -f p3_expand.sql
--
-- 이름 취급: 표기는 TDB 가 만든 것이 아니라 상류 원천에서 온다. 그 원천의 정책이 승인되기
-- 전까지 `kentity_names` 는 **unverified** 로 저장한다 — 보관은 하되 공급하지 않는다.
-- 정책이 승인되면 그때 검증 승격한다. 기본 차단을 우회하지 않으면서 통합은 진행한다.

\set ON_ERROR_STOP on
BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '900s';

-- ★psql 변수는 DO 블록 본문 안에서 치환되지 않는다(본문이 서버로 가는 문자열이라서).
-- 매개변수를 임시 표로 넘긴다.
CREATE TEMP TABLE ex_param ON COMMIT DROP AS
SELECT :'ptype'::text AS ptype, :lim::int AS lim, :'runid'::uuid AS runid;

DO $$
DECLARE p text;
BEGIN
  SELECT ptype INTO p FROM ex_param;
  IF (SELECT count(*) FROM p2_tdb_source WHERE place_type = p) = 0 THEN
    RAISE EXCEPTION '적재대에 % 가 없다 — 원본 추출을 먼저 하라', p;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM kentity_source_type_map
                  WHERE source_system='tdb' AND source_code = p AND disposition='map') THEN
    RAISE EXCEPTION '% 는 선매핑에서 map 이 아니다 — 보류/제외 코드는 흡수하지 않는다', p;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM kentity_source_policies WHERE provider='tdb' AND status='approved') THEN
    RAISE EXCEPTION 'tdb 원천 정책이 없다';
  END IF;
END $$;

-- ============================================================ 1. 판정
CREATE TEMP TABLE ex_decision ON COMMIT DROP AS
SELECT s.*, m.target_entity_type, m.target_subtype,
       COALESCE(NULLIF(btrim(s.disambiguator),''),
         CASE s.place_type
           WHEN 'legal_dong'   THEN NULLIF(btrim(s.ldong_code),'')
           WHEN 'admin_region' THEN COALESCE(NULLIF(btrim(s.sigungu_code),''),'광역자치단체')
           WHEN 'heritage'     THEN NULLIF(btrim(s.heritage_no),'')
           ELSE NULLIF(btrim(split_part(s.addr_ko,' ',1)||' '||split_part(s.addr_ko,' ',2)),'')
         END) AS built_disambig,
       EXISTS (SELECT 1 FROM kwave_entities e
                WHERE e.status<>'rejected' AND e.canonical_ko = s.name_ko) AS kdb_clash,
       EXISTS (SELECT 1 FROM kentity_crosswalks c
                WHERE c.source_system='tdb' AND c.source_table='tdb_places'
                  AND c.source_id = s.tdb_id::text) AS already
  FROM p2_tdb_source s
  JOIN kentity_source_type_map m ON m.source_system='tdb' AND m.source_code = s.place_type
 WHERE s.place_type = :'ptype';

CREATE TEMP TABLE ex_target ON COMMIT DROP AS
SELECT d.*, gen_random_uuid() AS new_entity_id,
       gen_random_uuid() AS ev_identity_id, gen_random_uuid() AS record_id
  FROM ex_decision d
 WHERE d.status='active' AND NOT d.already
   AND d.built_disambig IS NOT NULL
   AND NOT d.kdb_clash
 ORDER BY d.tdb_id
 LIMIT :lim;

-- ============================================================ 2. run 과 기록
INSERT INTO kentity_migration_runs
 (id, owner_key, request_key, request_hash, mode, state,
  mapper_version, canonicalization_version, source_basis, cohort_hash, selection_policy_hash,
  expected_records, expected_entities, max_changed_objects, observed_at,
  approved_by, approved_at, approval_ref)
-- ★요청키는 batch 마다 달라야 한다. 건수로 만들면 다음 batch 가 같은 키를 갖고
-- UNIQUE(owner_key, request_key) 에 걸려 통째로 롤백된다(실측).
SELECT :'runid', 'operator', 'p3-expand-' || :'ptype' || '-' || left(:'runid', 8),
       md5(md5(:'runid'))||md5('v1'), 'apply', 'running',
       'tdb-places-mapper-v1','nfc-v1',
       jsonb_build_object('source_system','tdb','source_table','tdb_places','place_type', :'ptype',
                          'rows',(SELECT count(*) FROM ex_target)),
       md5(md5(:'ptype'))||md5(:'runid'), md5(md5('tdb-selection-v1'))||md5(:'runid'),
       (SELECT count(*) FROM ex_target), (SELECT count(*) FROM ex_target),
       (SELECT count(*)*6 FROM ex_target), now(),
       'operator', now(), 'KDB_P3_FIRST_BATCH_PLAN.md';

-- 기록이 곧 **1차 binding** 이다. 원본 자기 ID·지문·관측시각을 여기에 남긴다.
INSERT INTO kentity_migration_records
 (id, run_id, source_system, source_table, source_pk, source_fingerprint, source_state, source_observed_at,
  disposition, state, target_mode, planned_target_id,
  expected_entity_revision, expected_identity_revision, expected_owner,
  plan_hash, expected_object_count, reason_code)
SELECT t.record_id, :'runid', 'tdb','tdb_places',
       jsonb_build_object('id', t.tdb_id::text),
       md5(md5(t.tdb_id::text))||md5(COALESCE(t.source_updated_at,'epoch'::timestamptz)::text),
       'present', t.source_updated_at, 'include', 'validated', 'create', t.new_entity_id,
       0, 0, 'native',
       md5(md5(t.tdb_id::text))||md5('expand'),
       CASE WHEN t.qid <> '' THEN 6 ELSE 4 END,
       'own_id_binding_with_disambiguator'
  FROM ex_target t;

-- ============================================================ 3. Entity
INSERT INTO kentity_entities
 (id, entity_type, subtype, canonical_ko, origin_system, write_owner, status,
  classification_status, classification_reason, classified_by, classified_at)
SELECT t.new_entity_id, t.target_entity_type, t.target_subtype, t.name_ko, 'tdb', 'native', 'candidate',
       'pending',
       'P3 확대: TDB ' || t.place_type || ' 선매핑(tdb-premap-v1). 구분값=' || t.built_disambig,
       'operator', now()
  FROM ex_target t;

-- ============================================================ 4. 정체성 근거 — 원본 자기 관측
-- 외부 식별자가 아니라 **우리 원본의 관측**이 정체성 근거다. QID 는 있을 때 얹는 보조다.
INSERT INTO kentity_evidence
 (id, entity_id, provider, source_record_id, source_url, claim_type, status,
  license_code, export_allowed, verified_by, verified_at, summary,
  claim_fingerprint, source_observation_hash, independent_origin, claim_payload, source_policy_id)
SELECT t.ev_identity_id, t.new_entity_id, 'tdb', t.tdb_id::text,
       'https://kdb.aiinplanet.com/admin/kentity/' || t.new_entity_id::text,
       'identity', 'verified', 'internal-operator-owned', false, 'operator', now(),
       'TDB ' || t.place_type || ' 자체 ID 관측',
       md5(t.tdb_id::text || 'identity'), md5(t.tdb_id::text || COALESCE(t.source_updated_at,'epoch'::timestamptz)::text),
       'tdb',
       jsonb_build_object('place_type', t.place_type, 'disambiguator', t.built_disambig),
       (SELECT id FROM kentity_source_policies WHERE provider='tdb' AND status='approved' LIMIT 1)
  FROM ex_target t;

-- ============================================================ 5. 이름 — 보관하되 공급하지 않는다
-- 상류 원천 정책이 승인되기 전까지 unverified 다. 정책 승인 시 검증 승격한다.
INSERT INTO kentity_names
 (entity_id, locale, value, kind, form, status, source_code, policy_version)
SELECT t.new_entity_id, 'ko', t.name_ko, 'canonical', 'recorded', 'unverified',
       COALESCE(NULLIF(split_part(t.source_codes,',',1),''),'tdb'), 'tdb-premap-v1'
  FROM ex_target t;

-- ============================================================ 6. 외부 식별자 — **있을 때만** (보조)
INSERT INTO kentity_external_ids
 (entity_id, provider, external_id, status, policy_version)
SELECT t.new_entity_id, 'wikidata', t.qid, 'unverified', 'tdb-premap-v1'
  FROM ex_target t WHERE t.qid <> ''
   AND NOT EXISTS (SELECT 1 FROM kentity_id_reservations r
                    WHERE r.provider='wikidata' AND r.external_id=t.qid);

INSERT INTO kentity_id_reservations (provider, external_id, entity_id)
SELECT 'wikidata', t.qid, t.new_entity_id FROM ex_target t WHERE t.qid <> ''
ON CONFLICT (provider, external_id) DO NOTHING;

-- ============================================================ 7. 연결 — 1차 binding 은 자기 ID 기록
INSERT INTO kentity_crosswalks
 (source_system, source_table, source_id, entity_id, status, target_identity_revision,
  mapping_policy_version, reason, evidence_id, decided_by, decided_at,
  basis_record_id, source_state, source_observed_at)
SELECT 'tdb','tdb_places', t.tdb_id::text, t.new_entity_id, 'confirmed',
       (SELECT identity_revision FROM kentity_entities WHERE id=t.new_entity_id),
       'tdb-premap-v1',
       'P3 확대: 원본 자기 ID 관측 위에서 확정. QID 는 보조 식별자로만 기록한다.',
       t.ev_identity_id, 'operator', now(),
       t.record_id, 'present', COALESCE(t.source_updated_at, now())
  FROM ex_target t;

-- ============================================================ 8. 결과
UPDATE kentity_migration_records r
   SET state='applied', applied_at=now(), actual_object_count=r.expected_object_count,
       target_entity_id=r.planned_target_id,
       applied_result_hash=md5(md5(r.source_pk->>'id'))||md5('applied'),
       revision=r.revision+1, updated_at=now()
 WHERE r.run_id = :'runid';

UPDATE kentity_migration_runs
   SET state='completed', finished_at=now(),
       expected_records=(SELECT count(*) FROM kentity_migration_records WHERE run_id = :'runid')
 WHERE id = :'runid';

COMMIT;

SELECT '적재' AS 항목, count(*)::text AS 값 FROM kentity_migration_records WHERE run_id = :'runid' AND state='applied'
UNION ALL SELECT 'QID 얹은 것', count(*)::text FROM kentity_migration_records WHERE run_id = :'runid' AND expected_object_count=6
UNION ALL SELECT 'QID 없이 들어온 것', count(*)::text FROM kentity_migration_records WHERE run_id = :'runid' AND expected_object_count=4;

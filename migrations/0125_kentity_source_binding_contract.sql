-- 0125_kentity_source_binding_contract — 원본 binding 계약 보강
--
-- 승인 근거: 운영자 지시 2026-09-13 — "wikidata QID 는 보조 역할이지 없는 DB 를 채우는
-- 역할이 아니다. 자체적인 id 값을 가지고 있다."
-- 설계 근거: KDB_TARGET_SCHEMA.md — "crosswalk source_system/source_id + TDB shadow 결합 |
--   원본 binding 계약 **보강 예정** | QID 없는 TDB 원본도 구분" / "현재 source_binding_id 는
--   TDB shadow FK. **범용 원천 binding 으로 오인 금지**".
--
-- 무엇이 잘못돼 있었나:
--   0122 는 tdb 연결에 `source_binding_id`(→ kentity_tdb_shadows) 를 필수로 걸었다.
--   그런데 그 표는 `qid NOT NULL` 이다. 결과적으로 **wikidata QID 가 없으면 연결 자체를
--   만들 수 없었다.** TDB 536,322행 중 QID 보유는 17,915행(3.3%) 뿐이므로 96.7% 가
--   구조적으로 흡수 불가였다. 보조여야 할 외부 식별자가 주 앵커 노릇을 한 것이다.
--
-- 고치는 방향:
--   원본은 **자기 ID** 로 묶는다. (source_system, source_table, source_id) 의 관측 기록이
--   1차 binding 이고, 그 기록은 이관 원장(kentity_migration_records)에 이미 있다 —
--   source_pk·source_fingerprint·source_state·source_observed_at 을 전부 갖는다.
--   wikidata shadow 는 **있을 때 얹는 보조 앵커**로 남는다(호환 유지).
--
-- 이 마이그레이션은 **완화만 한다.** 기존 행은 하나도 깨지지 않는다.

SET lock_timeout = '5s';
SET statement_timeout = '300s';

BEGIN;

ALTER TABLE kentity_crosswalks
  ADD COLUMN basis_record_id uuid REFERENCES kentity_migration_records(id) ON DELETE RESTRICT;

COMMENT ON COLUMN kentity_crosswalks.source_binding_id IS
  '보조 앵커: wikidata shadow(kentity_tdb_shadows). QID 가 있을 때만 채워진다. 범용 원천 binding 이 아니다.';
COMMENT ON COLUMN kentity_crosswalks.basis_record_id IS
  '1차 binding: 원본 자기 ID 의 관측 기록(kentity_migration_records). source_table+source_pk+지문+관측시각을 보유한다.';

-- tdb 연결은 **둘 중 하나 이상**의 binding 근거를 가져야 한다.
-- 종전엔 wikidata shadow 만 인정해서 QID 없는 원본을 막았다.
ALTER TABLE kentity_crosswalks
  DROP CONSTRAINT kentity_tdb_binding_required,
  ADD CONSTRAINT kentity_tdb_binding_required CHECK (
    source_system <> 'tdb'
    OR source_binding_id IS NOT NULL
    OR basis_record_id IS NOT NULL);

CREATE INDEX kentity_crosswalk_basis ON kentity_crosswalks (basis_record_id)
  WHERE basis_record_id IS NOT NULL;

-- 원본 시스템 자체를 원천으로 등록한다.
-- **이름 공급은 허용하지 않는다** — TDB 의 표기는 TDB 가 만든 것이 아니라 상류 원천
-- (aihub_tour·ngii_gazetteer·tourapi·seoul_dict·heritage_khs)에서 온다. 그 권리는
-- 원천별로 따로 검토한다. 여기서 허용하는 것은 **정체성 주장의 저장과 검증**뿐이다.
INSERT INTO kentity_source_policies
 (provider, version, status, license_code, storage_allowed, verification_allowed,
  name_export_allowed, excerpt_export_allowed, reviewed_by, reviewed_at, conditions)
SELECT 'tdb', '2026-09-13', 'approved', 'internal-operator-owned',
       true, true, false, false, 'operator', now(),
       '자체 운영 원본. 정체성(자기 ID)의 저장·검증만 허용한다. 표기 공급은 상류 원천 정책이 따로 정한다.'
 WHERE NOT EXISTS (SELECT 1 FROM kentity_source_policies WHERE provider='tdb');

COMMIT;

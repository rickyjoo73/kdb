-- P2 dry-run 구조 — 원본 적재대와 이관 원장 보호선
--
-- 격리 복원본에서만 실행한다. 운영 DB 에 적용하지 않는다.
-- p1_structure.sql 적용 뒤에 올린다. P1.02 는 G1 로 인수된 산출물이므로 건드리지 않고,
-- P2 에서 필요한 것만 여기에 더한다.

\set ON_ERROR_STOP on
BEGIN;

-- ============================================================ 1. 0123 초안 판단 — 수정 채택
--
-- 미러의 0123_kentity_tdb_inventory.sql 은 전량 UUID 원장(runs/inventory/head 3표)과
-- 발행 guard 를 제안했다. 읽고 판단한 결과:
--
--   [폐기] 3표 자체. kentity_migration_runs/kentity_migration_records(P1.02 §10)가 이미
--          같은 인구조사를 담는다 — source_system/source_table/source_pk/source_fingerprint/
--          source_state/source_observed_at/disposition 이 전부 있다. 원장을 둘로 두면
--          "전체가 몇 건인가"에 답이 둘이 된다. 그게 정확히 이 프로젝트가 막으려는 것이다.
--   [폐기] 별도 importer(scripts/import-tdb-inventory.cjs). 같은 이유 — 이관 원장에 직접
--          적재한다. 미검증 초안을 이 TODO 밖에서 실행하지 않는다(원장 금지사항).
--   [채택] 발행 guard 의 **두 가지 발상**은 P1.02 에 없다. 여기서 이관 원장에 붙인다.
--          ① 미완성 조사는 완료로 표시할 수 없다 (계상 수가 맞아야 한다)
--          ② 오래된 조사가 최신 스냅샷을 덮을 수 없다 (단조성)
--
-- ①이 없으면 "536,329건 중 12만건만 읽고 완료"가 가능하고, 그 위에서 세운 흡수 계획은
-- 나머지를 조용히 누락한다. G2 조건이 "전체 조사 미계상 0"인 이유가 이것이다.

CREATE OR REPLACE FUNCTION kentity_guard_migration_run_complete() RETURNS trigger
LANGUAGE plpgsql AS $mr$
DECLARE n bigint;
BEGIN
  IF NEW.state = 'completed' THEN
    SELECT count(*) INTO n FROM kentity_migration_records WHERE run_id = NEW.id;
    IF n <> NEW.expected_records THEN
      RAISE EXCEPTION 'run % cannot complete: % records recorded but % expected',
        NEW.id, n, NEW.expected_records;
    END IF;
    -- 계상되지 않은 처분이 남아 있으면 완료가 아니다.
    IF EXISTS (SELECT 1 FROM kentity_migration_records WHERE run_id = NEW.id AND state = 'planned') THEN
      RAISE EXCEPTION 'run % cannot complete while records are still planned', NEW.id;
    END IF;
  END IF;
  RETURN NEW;
END $mr$;

CREATE CONSTRAINT TRIGGER kentity_migration_run_complete
  AFTER UPDATE OF state ON kentity_migration_runs
  DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION kentity_guard_migration_run_complete();

-- 단조성: 더 오래 관측한 조사가 최신 조사를 대체하지 못한다.
-- 같은 원본(source_basis->>'source_table')에 대해 완료 시각이 앞서는 run 만 최신이 된다.
ALTER TABLE kentity_migration_runs
  ADD COLUMN observed_at timestamptz,
  ADD COLUMN is_current  boolean NOT NULL DEFAULT false,
  ADD CONSTRAINT kentity_migration_runs_current_shape CHECK (
    NOT is_current OR (state = 'completed' AND observed_at IS NOT NULL));
CREATE UNIQUE INDEX kentity_migration_runs_one_current
  ON kentity_migration_runs ((source_basis->>'source_table')) WHERE is_current;

CREATE OR REPLACE FUNCTION kentity_guard_migration_run_monotonic() RETURNS trigger
LANGUAGE plpgsql AS $mo$
DECLARE cur timestamptz;
BEGIN
  IF NEW.is_current AND NOT COALESCE(OLD.is_current, false) THEN
    SELECT observed_at INTO cur FROM kentity_migration_runs
     WHERE is_current AND id <> NEW.id
       AND (source_basis->>'source_table') IS NOT DISTINCT FROM (NEW.source_basis->>'source_table');
    IF cur IS NOT NULL AND NEW.observed_at < cur THEN
      RAISE EXCEPTION 'older survey (%) cannot replace the current snapshot (%)', NEW.observed_at, cur;
    END IF;
  END IF;
  RETURN NEW;
END $mo$;

CREATE TRIGGER kentity_migration_run_monotonic
  BEFORE UPDATE OF is_current ON kentity_migration_runs
  FOR EACH ROW EXECUTE FUNCTION kentity_guard_migration_run_monotonic();

-- ============================================================ 2. 원본 적재대 (staging)
-- 원본은 별도 DB 라 SQL 로 직접 조인할 수 없다. 읽기 전용으로 뽑아 여기에 싣는다.
-- **원본 payload 전체나 과거 로그는 싣지 않는다**(원장 P2.04 금지사항). 변환에 필요한 열만.
CREATE TABLE p2_tdb_source (
  tdb_id       uuid PRIMARY KEY,
  place_type   text NOT NULL,
  name_ko      text NOT NULL,
  disambiguator text NOT NULL DEFAULT '',
  status       text NOT NULL,
  merged_into  uuid,
  operator_locked boolean NOT NULL DEFAULT false,
  addr_ko      text NOT NULL DEFAULT '',
  sido_code    text NOT NULL DEFAULT '',
  sigungu_code text NOT NULL DEFAULT '',
  ldong_code   text NOT NULL DEFAULT '',
  heritage_no  text NOT NULL DEFAULT '',
  lat double precision, lon double precision,
  confidence   numeric,
  qid          text NOT NULL DEFAULT '',
  name_locales text NOT NULL DEFAULT '',   -- 보유 locale 목록(쉼표) — 표기 값은 싣지 않는다
  source_codes text NOT NULL DEFAULT '',   -- 연결된 원천 코드 목록(쉼표)
  source_updated_at timestamptz
);
CREATE INDEX p2_tdb_source_type ON p2_tdb_source (place_type, tdb_id);
CREATE INDEX p2_tdb_source_name ON p2_tdb_source (name_ko, place_type);

COMMIT;

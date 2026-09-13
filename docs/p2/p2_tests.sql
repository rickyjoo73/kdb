-- P2.06 batch 운영 시험 — 재실행·중단·중복·늦은 commit·삭제/병합·이름만 변경·잠금/정책·역순
--
-- 격리 복원본에서만 실행한다. p2_structure.sql · p2_premap.sql · p2_dryrun.sql 적용 뒤에 올린다.
-- 대량 적재가 "한 번 성공"하는 것은 쉽다. 어려운 것은 **두 번째와 중간에 끊긴 첫 번째**다.

\set ON_ERROR_STOP on
BEGIN;

DROP TABLE IF EXISTS p2_test_results;
CREATE TABLE p2_test_results (
  seq serial primary key, tid text, area text, title text,
  expect text, outcome text, detail text
);

CREATE OR REPLACE FUNCTION p2_try(tid text, area text, title text, want text, stmt text)
RETURNS void LANGUAGE plpgsql AS $fn$
BEGIN
  BEGIN
    EXECUTE stmt;
    INSERT INTO p2_test_results(tid,area,title,expect,outcome,detail)
      VALUES (tid,area,title,want, CASE WHEN want='accept' THEN 'PASS' ELSE 'FAIL' END,
              CASE WHEN want='accept' THEN '' ELSE '거부돼야 하는데 통과함' END);
  EXCEPTION WHEN others THEN
    INSERT INTO p2_test_results(tid,area,title,expect,outcome,detail)
      VALUES (tid,area,title,want, CASE WHEN want='reject' THEN 'PASS' ELSE 'FAIL' END, left(SQLERRM,90));
  END;
END $fn$;

CREATE OR REPLACE FUNCTION p2_must(cond boolean) RETURNS int LANGUAGE plpgsql AS $fn$
BEGIN
  IF cond IS NOT TRUE THEN RAISE EXCEPTION '단언이 거짓이다'; END IF; RETURN 1;
END $fn$;

-- 시험용 run 하나. 실제 dry-run(7d000001…)은 건드리지 않는다.
INSERT INTO kentity_migration_runs
 (id, owner_key, request_key, request_hash, mode, state, mapper_version, canonicalization_version,
  source_basis, cohort_hash, selection_policy_hash, expected_records, expected_entities,
  max_changed_objects, observed_at)
VALUES ('7e000001-0000-4000-8000-000000000001','operator','p2-batch-test',repeat('a',64),'dry_run','running',
        'tdb-places-mapper-v1','nfc-v1','{"source_system":"tdb","source_table":"p2_batch_test"}'::jsonb,
        repeat('b',64),repeat('c',64), 2, 0, 0, '2026-09-13 00:00:00+00');

-- ============================================================ 재실행
SELECT p2_try('B01','P2.06','같은 owner·요청키로 run 을 하나 더','reject', $$
INSERT INTO kentity_migration_runs
 (owner_key, request_key, request_hash, mode, state, mapper_version, canonicalization_version,
  source_basis, cohort_hash, selection_policy_hash, expected_records, expected_entities, max_changed_objects)
VALUES ('operator','p2-batch-test',repeat('a',64),'dry_run','planned','tdb-places-mapper-v1','nfc-v1',
        '{}'::jsonb,repeat('b',64),repeat('c',64),2,0,0)$$);

-- ============================================================ 중복
SELECT p2_try('B02','P2.06','같은 run 에 같은 원본 키를 두 번','reject', $$
INSERT INTO kentity_migration_records
 (run_id, source_system, source_table, source_pk, source_fingerprint, disposition, target_mode,
  plan_hash, reason_code)
VALUES ('7e000001-0000-4000-8000-000000000001','tdb','p2_batch_test','{"id":"x1"}',repeat('1',64),
        'include','none',repeat('2',64),'first'),
       ('7e000001-0000-4000-8000-000000000001','tdb','p2_batch_test','{"id":"x1"}',repeat('1',64),
        'include','none',repeat('3',64),'duplicate')$$);

SELECT p2_try('B03','P2.06','원본 2건을 정상 계상','accept', $$
INSERT INTO kentity_migration_records
 (run_id, source_system, source_table, source_pk, source_fingerprint, source_state, disposition,
  state, target_mode, plan_hash, reason_code)
VALUES ('7e000001-0000-4000-8000-000000000001','tdb','p2_batch_test','{"id":"x1"}',repeat('1',64),
        'present','include','validated','none',repeat('2',64),'ok'),
       ('7e000001-0000-4000-8000-000000000001','tdb','p2_batch_test','{"id":"x2"}',repeat('4',64),
        'present','conditional','held','none',repeat('5',64),'held')$$);

-- ============================================================ 중단 — 미완성 조사를 완료로
SELECT p2_try('B04','P2.06','계상이 모자란 채로 완료 표시','reject', $$
UPDATE kentity_migration_runs SET expected_records=3, state='completed', finished_at=now()
 WHERE id='7e000001-0000-4000-8000-000000000001'$$);

SELECT p2_try('B05','P2.06','planned 기록이 남은 채로 완료 표시','reject', $$
INSERT INTO kentity_migration_records
 (run_id, source_system, source_table, source_pk, source_fingerprint, disposition, target_mode, plan_hash, reason_code)
VALUES ('7e000001-0000-4000-8000-000000000001','tdb','p2_batch_test','{"id":"x3"}',repeat('6',64),
        'include','none',repeat('7',64),'still planned');
UPDATE kentity_migration_runs SET expected_records=3, state='completed', finished_at=now()
 WHERE id='7e000001-0000-4000-8000-000000000001'$$);

SELECT p2_try('B06','P2.06','전량 계상하고 처분이 끝나면 완료','accept', $$
UPDATE kentity_migration_records SET state='held', reason_code='resolved'
 WHERE run_id='7e000001-0000-4000-8000-000000000001' AND state='planned';
UPDATE kentity_migration_runs SET expected_records=3, state='completed', finished_at=now()
 WHERE id='7e000001-0000-4000-8000-000000000001'$$);

-- ============================================================ 역순 이벤트 — 오래된 조사가 최신을 덮기
SELECT p2_try('B07','P2.06','완료된 조사를 현재 스냅샷으로','accept', $$
UPDATE kentity_migration_runs SET is_current=true WHERE id='7e000001-0000-4000-8000-000000000001'$$);

SELECT p2_try('B08','P2.06','더 오래 관측한 조사가 현재를 대체','reject', $$
INSERT INTO kentity_migration_runs
 (id, owner_key, request_key, request_hash, mode, state, mapper_version, canonicalization_version,
  source_basis, cohort_hash, selection_policy_hash, expected_records, expected_entities,
  max_changed_objects, observed_at, finished_at)
VALUES ('7e000002-0000-4000-8000-000000000002','operator','p2-batch-older',repeat('d',64),'dry_run','completed',
        'tdb-places-mapper-v1','nfc-v1','{"source_system":"tdb","source_table":"p2_batch_test"}'::jsonb,
        repeat('e',64),repeat('f',64), 0, 0, 0, '2026-09-01 00:00:00+00', now());
UPDATE kentity_migration_runs SET is_current=true WHERE id='7e000002-0000-4000-8000-000000000002'$$);

SELECT p2_try('B09','P2.06','같은 원본에 현재 스냅샷이 둘','reject', $$
UPDATE kentity_migration_runs SET observed_at='2026-09-20 00:00:00+00' WHERE id='7e000002-0000-4000-8000-000000000002';
UPDATE kentity_migration_runs SET is_current=true WHERE id='7e000002-0000-4000-8000-000000000002'$$);

-- ============================================================ 늦은 commit / 삭제·병합 / 이름만 변경
SELECT p2_try('B10','P2.06','원본이 삭제된 기록을 include 로','accept', $$
UPDATE kentity_migration_records SET source_state='deleted', disposition='exclude_operational',
       reason_code='source_deleted_after_survey'
 WHERE run_id='7e000001-0000-4000-8000-000000000001' AND source_pk->>'id'='x2'$$);

SELECT p2_try('B11','P2.06','병합된 원본도 상태로 구분된다','accept', $$
UPDATE kentity_migration_records SET source_state='merged', reason_code='source_merged_after_survey'
 WHERE run_id='7e000001-0000-4000-8000-000000000001' AND source_pk->>'id'='x3'$$);

SELECT p2_try('B12','P2.06','이름만 바뀌면 지문이 바뀌고 대상은 그대로','accept', $$
UPDATE kentity_migration_records SET source_fingerprint=repeat('9',64)
 WHERE run_id='7e000001-0000-4000-8000-000000000001' AND source_pk->>'id'='x1';
SELECT p2_must((SELECT target_mode FROM kentity_migration_records
   WHERE run_id='7e000001-0000-4000-8000-000000000001' AND source_pk->>'id'='x1')='none')$$);

SELECT p2_try('B13','P2.06','지문이 형식을 벗어나면','reject', $$
UPDATE kentity_migration_records SET source_fingerprint='not-a-hash'
 WHERE run_id='7e000001-0000-4000-8000-000000000001' AND source_pk->>'id'='x1'$$);

-- ============================================================ 누락 검사 — 진행 원장 vs 원본 매핑
SELECT p2_try('B14','P2.06','실제 dry-run: 원본 수 = 기록 수, 누락 0','accept', $$
SELECT p2_must(
   (SELECT count(*) FROM p2_tdb_source)
 = (SELECT count(*) FROM kentity_migration_records WHERE run_id='7d000001-0000-4000-8000-000000000001')
 AND NOT EXISTS (SELECT 1 FROM p2_tdb_source s WHERE NOT EXISTS (
     SELECT 1 FROM kentity_migration_records r
      WHERE r.run_id='7d000001-0000-4000-8000-000000000001' AND r.source_pk->>'id'=s.tdb_id::text)))$$);

SELECT p2_try('B15','P2.06','처분 없는 기록이 하나도 없다','accept', $$
SELECT p2_must(NOT EXISTS (SELECT 1 FROM kentity_migration_records
   WHERE run_id='7d000001-0000-4000-8000-000000000001' AND btrim(reason_code)=''))$$);

SELECT p2_try('B16','P2.06','include 인데 목표 계획이 없는 기록','accept', $$
SELECT p2_must(NOT EXISTS (SELECT 1 FROM kentity_migration_records
   WHERE run_id='7d000001-0000-4000-8000-000000000001'
     AND disposition='include' AND planned_target_id IS NULL))$$);

SELECT p2_try('B17','P2.06','보류인데 목표를 만들어 둔 기록 (추측 금지)','accept', $$
SELECT p2_must(NOT EXISTS (SELECT 1 FROM kentity_migration_records
   WHERE run_id='7d000001-0000-4000-8000-000000000001'
     AND disposition<>'include' AND planned_target_id IS NOT NULL))$$);

SELECT '완료' AS done;
COMMIT;

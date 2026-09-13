-- P1.03~P1.06 격리 제약 시험 + M01~M14 인수 사례
--
-- 격리 복원본(kdb-p1-restore-db)에서만 실행한다. 운영 DB 에 적용하지 않는다.
-- 정답은 fixture 구성으로 확정된다(KDB_ACCEPTANCE_FIXTURES.md §1). 이름은 전부 자리표시자다.
-- 각 시험은 "거부돼야 한다 / 허용돼야 한다"를 명시하고 결과를 p1_test_results 에 적는다.
-- 실패한 문장은 savepoint 로 되돌아가므로 트랜잭션을 오염시키지 않는다.

\set ON_ERROR_STOP on
BEGIN;

DROP TABLE IF EXISTS p1_test_results;
CREATE TABLE p1_test_results (
  seq serial primary key, tid text, area text, title text,
  expect text, outcome text, detail text
);

CREATE OR REPLACE FUNCTION p1_try(tid text, area text, title text, want text, stmt text)
RETURNS void LANGUAGE plpgsql AS $fn$
BEGIN
  BEGIN
    EXECUTE stmt;
    INSERT INTO p1_test_results(tid,area,title,expect,outcome,detail)
      VALUES (tid,area,title,want, CASE WHEN want='accept' THEN 'PASS' ELSE 'FAIL' END,
              CASE WHEN want='accept' THEN '' ELSE '거부돼야 하는데 통과함' END);
  EXCEPTION WHEN others THEN
    INSERT INTO p1_test_results(tid,area,title,expect,outcome,detail)
      VALUES (tid,area,title,want, CASE WHEN want='reject' THEN 'PASS' ELSE 'FAIL' END,
              left(SQLERRM,90));
  END;
END $fn$;


-- p1_must — 단언용. 조건이 참이 아니면 예외를 던진다.
-- 조건절만 쓰는 단언(WHERE 가 거짓이면 0행)은 오류가 아니라서, "문장이 성공했는가"만
-- 보는 p1_try 가 거짓 단언을 조용히 PASS 로 적는다(TRG02 에서 실증됐다).
-- 단언은 반드시 이 함수를 통과시킨다.
CREATE OR REPLACE FUNCTION p1_must(cond boolean) RETURNS int LANGUAGE plpgsql AS $fn$
BEGIN
  IF cond IS NOT TRUE THEN RAISE EXCEPTION '단언이 거짓이다'; END IF;
  RETURN 1;
END $fn$;

-- ============================================================ fixture
-- 합성 Entity. status='candidate' 로 만든다(active 는 검증된 identity 근거를 요구하는
-- kentity_guard_legacy_owner 규칙이 있고, 그 규칙 자체는 이 시험의 대상이 아니다).
INSERT INTO kentity_entities (id, entity_type, subtype, canonical_ko, origin_system, write_owner, status) VALUES
 ('aaaa0001-0000-4000-8000-000000000001','person','real','인물-A','native','native','candidate'),
 ('aaaa0002-0000-4000-8000-000000000002','person','real','인물-A','native','native','candidate'),
 ('bbbb0001-0000-4000-8000-000000000001','person','real','인물-B','native','native','candidate'),
 ('bbbb0002-0000-4000-8000-000000000002','person','real','인물-B','native','native','candidate'),
 ('cccc0001-0000-4000-8000-000000000001','person','real','인물-C','native','native','candidate'),
 ('dddd0001-0000-4000-8000-000000000001','person','real','명칭-D','native','native','candidate'),
 ('dddd0002-0000-4000-8000-000000000002','company','corporation','명칭-D','native','native','candidate'),
 ('dddd0003-0000-4000-8000-000000000003','location','district','명칭-D','native','native','candidate'),
 ('dddd0004-0000-4000-8000-000000000004','product','named_product','명칭-D','native','native','candidate'),
 ('eeee0001-0000-4000-8000-000000000001','location','station','장소-E','native','native','candidate'),
 ('eeee0002-0000-4000-8000-000000000002','location','shopping','장소-E','native','native','candidate'),
 ('ffff0001-0000-4000-8000-000000000001','organization','school','장소-F','native','native','candidate'),
 ('ffff0002-0000-4000-8000-000000000002','location','campus','장소-F','native','native','candidate'),
 ('99990001-0000-4000-8000-000000000001','person','real','인물-I','native','native','candidate'),
 ('99990002-0000-4000-8000-000000000002','work','film','작품-I','native','native','candidate'),
 ('77770001-0000-4000-8000-000000000001','character','fictional','캐릭터-P','native','native','candidate'),
 ('66660001-0000-4000-8000-000000000001','person','real','인물-L','native','native','candidate'),
 ('55550001-0000-4000-8000-000000000001','work','album','작품-N','native','native','candidate');

-- 근거. 같은 Entity 에 귀속되며 claim_type 을 구분한다(S02).
INSERT INTO kentity_evidence (id, entity_id, provider, source_record_id, source_url, claim_type, status, summary, claim_fingerprint, source_observation_hash, independent_origin) VALUES
 ('e0a10001-0000-4000-8000-000000000001','aaaa0001-0000-4000-8000-000000000001','prov-x','r1','https://example.invalid/a1','occupation','unverified','A1 직업 근거','fp-a1-occ','soh-a1','origin-x'),
 ('e0a20001-0000-4000-8000-000000000001','aaaa0002-0000-4000-8000-000000000002','prov-y','r2','https://example.invalid/a2','occupation','unverified','A2 직업 근거','fp-a2-occ','soh-a2','origin-y'),
 ('e0a10002-0000-4000-8000-000000000002','aaaa0001-0000-4000-8000-000000000001','prov-x','r3','https://example.invalid/a1i','identity','unverified','A1 정체성 근거','fp-a1-id','soh-a1i','origin-x'),
 ('e0c10001-0000-4000-8000-000000000001','cccc0001-0000-4000-8000-000000000001','prov-x','r4','https://example.invalid/c1','occupation','unverified','C1 직업 근거','fp-c1-occ','soh-c1','origin-x'),
 ('e0b10001-0000-4000-8000-000000000001','bbbb0001-0000-4000-8000-000000000001','prov-x','r5','https://example.invalid/b1','identity','unverified','B1 정체성','fp-b1-id','soh-b1','origin-x'),
 ('e0b20001-0000-4000-8000-000000000001','bbbb0002-0000-4000-8000-000000000002','prov-y','r6','https://example.invalid/b2','identity','unverified','B2 정체성','fp-b2-id','soh-b2','origin-y'),
 ('e01a0001-0000-4000-8000-000000000001','66660001-0000-4000-8000-000000000001','prov-x','r7','https://example.invalid/l1','name','unverified','L 이름 근거','fp-l1-name','soh-l1','origin-x');

-- 지연 제약(redirect guard)은 COMMIT 때 터지므로 p1_try 의 savepoint 로 잡히지 않는다.
-- 시험은 실패를 건별로 잡아야 하므로 즉시 검사로 돌린다. 지연 동작 자체는 M12j 에서 따로 본다.
SET CONSTRAINTS ALL IMMEDIATE;

-- ============================================================ P1.03 통제 코드·FK·유형 제약

SELECT p1_try('C01','P1.03','같은 이름 A1/A2 에 각각 singer/actor','accept', $$
INSERT INTO kentity_person_roles (entity_id, role_code, assigned_by, reason, policy_version)
VALUES ('aaaa0001-0000-4000-8000-000000000001','singer','t','P1.03 fixture','p-v1'),
       ('aaaa0002-0000-4000-8000-000000000002','actor','t','P1.03 fixture','p-v1')$$);

SELECT p1_try('C02','P1.03','A1 의 근거를 A2 의 직업 행에 붙이기','reject', $$
INSERT INTO kentity_person_roles (entity_id, role_code, evidence_id, assigned_by, reason, policy_version)
VALUES ('aaaa0002-0000-4000-8000-000000000002','singer','e0a10001-0000-4000-8000-000000000001','t','cross-entity evidence','p-v1')$$);

SELECT p1_try('C03','P1.03','한 사람(C1)에 singer + actor','accept', $$
INSERT INTO kentity_person_roles (entity_id, role_code, assigned_by, reason, policy_version)
VALUES ('cccc0001-0000-4000-8000-000000000001','singer','t','multi-role','p-v1'),
       ('cccc0001-0000-4000-8000-000000000001','actor','t','multi-role','p-v1')$$);

SELECT p1_try('C04','P1.03','company 에 person 직업 행','reject', $$
INSERT INTO kentity_person_roles (entity_id, role_code, assigned_by, reason, policy_version)
VALUES ('dddd0002-0000-4000-8000-000000000002','executive','t','company role','p-v1')$$);

SELECT p1_try('C05','P1.03','직업이 남은 person 을 company 로 유형 변경','reject', $$
UPDATE kentity_entities SET entity_type='company', subtype='corporation'
 WHERE id='cccc0001-0000-4000-8000-000000000001'$$);

SELECT p1_try('C06','P1.03','person + restaurant 세부유형','reject', $$
UPDATE kentity_entities SET subtype='restaurant' WHERE id='bbbb0001-0000-4000-8000-000000000001'$$);

SELECT p1_try('C07','P1.03','subtype NULL 후보는 수용','accept', $$
UPDATE kentity_entities SET subtype=NULL WHERE id='bbbb0002-0000-4000-8000-000000000002'$$);

SELECT p1_try('C08','P1.03','subtype NULL 인데 분류 완료(verified)','reject', $$
UPDATE kentity_entities SET classification_status='verified'
 WHERE id='bbbb0002-0000-4000-8000-000000000002'$$);

SELECT p1_try('C09','P1.03','사전에 없는 직업 코드','reject', $$
INSERT INTO kentity_person_roles (entity_id, role_code, assigned_by, reason, policy_version)
VALUES ('bbbb0001-0000-4000-8000-000000000001','superstar','t','unknown code','p-v1')$$);

SELECT p1_try('C10','P1.03','직군 자기 자신을 부모로','reject', $$
UPDATE kentity_role_types SET parent_code='singer' WHERE code='singer'$$);

SELECT p1_try('C11','P1.03','없는 부모 직군 지정','reject', $$
UPDATE kentity_role_types SET parent_code='nonexistent_role' WHERE code='model'$$);

SELECT p1_try('C12','P1.03','other 를 직업 코드로 부여','reject', $$
INSERT INTO kentity_person_roles (entity_id, role_code, assigned_by, reason, policy_version)
VALUES ('bbbb0001-0000-4000-8000-000000000001','other','t','other is not a code','p-v1')$$);

SELECT p1_try('C13','P1.03','이름은 전역 UNIQUE 가 아니다(동명 Entity 추가)','accept', $$
INSERT INTO kentity_entities (id, entity_type, subtype, canonical_ko, origin_system, write_owner, status)
VALUES ('aaaa0003-0000-4000-8000-000000000003','person','real','인물-A','native','native','candidate')$$);

SELECT p1_try('C14','P1.03','Entity UUID 변경','reject', $$
UPDATE kentity_entities SET id='0000dead-0000-4000-8000-00000000dead'
 WHERE id='aaaa0003-0000-4000-8000-000000000003'$$);

SELECT p1_try('C15','P1.03','근거 없는 직업 verified 승인','reject', $$
UPDATE kentity_person_roles SET status='verified'
 WHERE entity_id='cccc0001-0000-4000-8000-000000000001' AND role_code='singer'$$);

SELECT p1_try('C16','P1.03','근거 없는 not_applicable 직업 판정','reject', $$
INSERT INTO kentity_person_profiles (entity_id, role_state, role_reason)
VALUES ('bbbb0001-0000-4000-8000-000000000001','not_applicable','직업 없음 주장')$$);

SELECT p1_try('C17','P1.03','생일: 월 없이 일만','reject', $$
INSERT INTO kentity_person_profiles (entity_id, birth_year, birth_day)
VALUES ('bbbb0002-0000-4000-8000-000000000002', 1990, 15)$$);

SELECT p1_try('C18','P1.03','생일: 연도만 (월/일 미상)','accept', $$
INSERT INTO kentity_person_profiles (entity_id, birth_year)
VALUES ('bbbb0001-0000-4000-8000-000000000001', 1990)$$);

-- ============================================================ M01~M14

-- M01 동명 가수/배우: 서로 다른 UUID, 각자의 직업만
SELECT p1_try('M01a','M01','동명 A1/A2 가 각자 직업을 갖는다','accept', $$
SELECT p1_must((SELECT count(*) FROM kentity_person_roles
  WHERE entity_id='aaaa0001-0000-4000-8000-000000000001' AND role_code='singer')=1
 AND (SELECT count(*) FROM kentity_person_roles
  WHERE entity_id='aaaa0002-0000-4000-8000-000000000002' AND role_code='actor')=1)$$);
SELECT p1_try('M01b','M01','동명 두 사람이 같은 직업·같은 기간을 verified 로 가짐','accept', $$
INSERT INTO kentity_evidence (id, entity_id, provider, source_record_id, source_url, claim_type, status, summary, claim_fingerprint, source_observation_hash, independent_origin, verified_by, verified_at)
VALUES ('e0a10003-0000-4000-8000-000000000003','aaaa0001-0000-4000-8000-000000000001','prov-x','m01a','https://example.invalid/m01a','occupation','verified','A1 verified','fp-m01a','soh-m01a','origin-x','op',now()),
       ('e0a20003-0000-4000-8000-000000000003','aaaa0002-0000-4000-8000-000000000002','prov-y','m01b','https://example.invalid/m01b','occupation','verified','A2 verified','fp-m01b','soh-m01b','origin-y','op',now());
INSERT INTO kentity_person_roles (entity_id, role_code, status, evidence_id, valid_from, valid_from_precision, valid_until, valid_until_precision, assigned_by, reason, policy_version, verified_by, verified_at)
VALUES ('aaaa0001-0000-4000-8000-000000000001','rapper','verified','e0a10003-0000-4000-8000-000000000003','2020-01-01','day','2023-12-31','day','t','M01','p-v1','op',now()),
       ('aaaa0002-0000-4000-8000-000000000002','rapper','verified','e0a20003-0000-4000-8000-000000000003','2020-01-01','day','2023-12-31','day','t','M01','p-v1','op',now())$$);
SELECT p1_try('M01c','M01','같은 사람의 같은 직업이 기간 겹침','reject', $$
INSERT INTO kentity_person_roles (entity_id, role_code, status, evidence_id, valid_from, valid_from_precision, valid_until, valid_until_precision, assigned_by, reason, policy_version, verified_by, verified_at)
VALUES ('aaaa0001-0000-4000-8000-000000000001','rapper','verified','e0a10003-0000-4000-8000-000000000003','2022-06-01','day','2025-01-01','day','t','overlap','p-v1','op',now())$$);

-- M02 이름·직업·생년까지 같은 두 사람 → 병합하지 않고 possible_same 보류
SELECT p1_try('M02a','M02','같은 이름·직업·생년 두 사람을 possible_same 으로 보류','accept', $$
INSERT INTO kentity_identity_decisions (left_id, right_id, decision, left_identity_revision, right_identity_revision, actor, reason, policy_version)
VALUES ('bbbb0001-0000-4000-8000-000000000001','bbbb0002-0000-4000-8000-000000000002','possible_same',1,1,'judge','name+role+birth match only','idp-v1')$$);
SELECT p1_try('M02b','M02','근거 없이 confirmed_same 으로 승격','reject', $$
UPDATE kentity_identity_decisions SET decision='confirmed_same'
 WHERE left_id='bbbb0001-0000-4000-8000-000000000001'$$);
SELECT p1_try('M02c','M02','정렬되지 않은 쌍(left>right) 등록','reject', $$
INSERT INTO kentity_identity_decisions (left_id, right_id, decision, left_identity_revision, right_identity_revision, actor, reason, policy_version)
VALUES ('bbbb0002-0000-4000-8000-000000000002','bbbb0001-0000-4000-8000-000000000001','possible_same',1,1,'j','unsorted','idp-v1')$$);

-- M03 겸업: UUID 하나로 두 필터 조회
SELECT p1_try('M03a','M03','C1 이 singer/actor 두 필터에 모두 잡힘','accept', $$
SELECT p1_must((SELECT count(DISTINCT role_code) FROM kentity_person_roles
  WHERE entity_id='cccc0001-0000-4000-8000-000000000001' AND role_code IN ('singer','actor'))=2)$$);

-- M04 같은 명칭의 회사·장소·상품·인물
SELECT p1_try('M04a','M04','명칭-D 네 대상이 서로 다른 UUID·유형','accept', $$
SELECT p1_must((SELECT count(DISTINCT entity_type) FROM kentity_entities WHERE canonical_ko='명칭-D')=4)$$);
SELECT p1_try('M04b','M04','허용표에 없는 관계 조합(product→person)','reject', $$
INSERT INTO kentity_relations (subject_id, subject_type, predicate, object_id, object_type)
VALUES ('dddd0004-0000-4000-8000-000000000004','product','manufactured_by','dddd0001-0000-4000-8000-000000000001','person')$$);
SELECT p1_try('M04c','M04','허용된 관계 조합(product→company)','accept', $$
INSERT INTO kentity_relations (subject_id, subject_type, predicate, object_id, object_type)
VALUES ('dddd0004-0000-4000-8000-000000000004','product','manufactured_by','dddd0002-0000-4000-8000-000000000002','company')$$);
SELECT p1_try('M04d','M04','근거 없이 관계를 verified 로','reject', $$
UPDATE kentity_relations SET status='verified'
 WHERE subject_id='dddd0004-0000-4000-8000-000000000004'$$);

-- M05 역/역 앞 상점, 학교/캠퍼스
SELECT p1_try('M05a','M05','역과 상점이 다른 UUID 로 공존','accept', $$
SELECT p1_must((SELECT count(*) FROM kentity_entities WHERE canonical_ko='장소-E')=2)$$);
SELECT p1_try('M05b','M05','같은 QID 를 두 대상에 verified 로','reject', $$
INSERT INTO kentity_evidence (id, entity_id, provider, source_record_id, source_url, claim_type, status, summary, claim_fingerprint, source_observation_hash, independent_origin, verified_by, verified_at)
VALUES ('e0e10001-0000-4000-8000-000000000001','eeee0001-0000-4000-8000-000000000001','wikidata','q1','https://example.invalid/q1','identity','verified','역 정체성','fp-e1','soh-e1','wikidata','op',now()),
       ('e0e20001-0000-4000-8000-000000000001','eeee0002-0000-4000-8000-000000000002','wikidata','q1','https://example.invalid/q1','identity','verified','상점 정체성','fp-e2','soh-e2','wikidata','op',now());
INSERT INTO kentity_external_ids (entity_id, provider, external_id, status, evidence_id, policy_version)
VALUES ('eeee0001-0000-4000-8000-000000000001','wikidata','Q-FIX-1','verified','e0e10001-0000-4000-8000-000000000001','p-v1'),
       ('eeee0002-0000-4000-8000-000000000002','wikidata','Q-FIX-1','verified','e0e20001-0000-4000-8000-000000000001','p-v1')$$);
SELECT p1_try('M05c','M05','학교(organization)와 캠퍼스(location)가 별개 UUID','accept', $$
SELECT p1_must((SELECT count(DISTINCT entity_type) FROM kentity_entities WHERE canonical_ko='장소-F')=2)$$);
SELECT p1_try('M05d','M05','좌표 한쪽만 입력','reject', $$
INSERT INTO kentity_location_profiles (entity_id, latitude) VALUES ('eeee0002-0000-4000-8000-000000000002', 37.5)$$);
SELECT p1_try('M05e','M05','행정코드가 있는데 namespace 없음','reject', $$
INSERT INTO kentity_location_profiles (entity_id, sido_code) VALUES ('dddd0003-0000-4000-8000-000000000003','11')$$);

-- M08 같은 외부 ID 가 다른 대상에
SELECT p1_try('M08a','M08','person 에 QID verified','accept', $$
INSERT INTO kentity_evidence (id, entity_id, provider, source_record_id, source_url, claim_type, status, summary, claim_fingerprint, source_observation_hash, independent_origin, verified_by, verified_at)
VALUES ('e01b0001-0000-4000-8000-000000000001','99990001-0000-4000-8000-000000000001','wikidata','q2','https://example.invalid/q2','identity','verified','I 정체성','fp-i1','soh-i1','wikidata','op',now());
INSERT INTO kentity_external_ids (entity_id, provider, external_id, status, evidence_id, policy_version)
VALUES ('99990001-0000-4000-8000-000000000001','wikidata','Q-FIX-2','verified','e01b0001-0000-4000-8000-000000000001','p-v1')$$);
SELECT p1_try('M08b','M08','같은 QID 를 work 에도 verified','reject', $$
INSERT INTO kentity_evidence (id, entity_id, provider, source_record_id, source_url, claim_type, status, summary, claim_fingerprint, source_observation_hash, independent_origin, verified_by, verified_at)
VALUES ('e01c0001-0000-4000-8000-000000000001','99990002-0000-4000-8000-000000000002','wikidata','q2','https://example.invalid/q2','identity','verified','W 정체성','fp-i2','soh-i2','wikidata','op',now());
INSERT INTO kentity_external_ids (entity_id, provider, external_id, status, evidence_id, policy_version)
VALUES ('99990002-0000-4000-8000-000000000002','wikidata','Q-FIX-2','verified','e01c0001-0000-4000-8000-000000000001','p-v1')$$);
SELECT p1_try('M08c','M08','충돌 주장은 conflict 상태로 보관','accept', $$
INSERT INTO kentity_external_ids (entity_id, provider, external_id, status, policy_version)
VALUES ('99990002-0000-4000-8000-000000000002','wikidata','Q-FIX-2','conflict','p-v1')$$);

-- M09 개명·이적: UUID 불변, 옛 이름 보존, 연도만 아는 사실에 가짜 날짜 금지
SELECT p1_try('M09a','M09','연도만 아는 직업 기간','accept', $$
INSERT INTO kentity_person_roles (entity_id, role_code, valid_from_precision, valid_from_year, valid_until_precision, valid_until_year, assigned_by, reason, policy_version)
VALUES ('99990001-0000-4000-8000-000000000001','actor','year',2024,'year',2024,'t','year-only','p-v1')$$);
SELECT p1_try('M09b','M09','year 정밀도인데 exact date 도 같이 저장','reject', $$
INSERT INTO kentity_person_roles (entity_id, role_code, valid_from_precision, valid_from_year, valid_from, assigned_by, reason, policy_version)
VALUES ('66660001-0000-4000-8000-000000000001','singer','year',2024,'2024-01-01','t','fabricated day','p-v1')$$);
SELECT p1_try('M09c','M09','month 정밀도인데 연도 없음','reject', $$
INSERT INTO kentity_person_roles (entity_id, role_code, valid_from_precision, valid_from_month, assigned_by, reason, policy_version)
VALUES ('66660001-0000-4000-8000-000000000001','singer','month',6,'t','month without year','p-v1')$$);
SELECT p1_try('M09d','M09','종료일이 시작일보다 앞','reject', $$
INSERT INTO kentity_person_roles (entity_id, role_code, valid_from, valid_from_precision, valid_until, valid_until_precision, assigned_by, reason, policy_version)
VALUES ('66660001-0000-4000-8000-000000000001','singer','2024-01-01','day','2023-01-01','day','t','reversed','p-v1')$$);

-- M11 미확인은 pending 으로 남고 기타로 승인되지 않음
SELECT p1_try('M11a','M11','unknown 유형 Entity 를 후보로 수용','accept', $$
INSERT INTO kentity_entities (id, entity_type, canonical_ko, origin_system, write_owner, status)
VALUES ('1111dead-0000-4000-8000-000000000001','unknown','대상-K','native','native','candidate')$$);
SELECT p1_try('M11b','M11','unknown 유형으로 분류 완료','reject', $$
UPDATE kentity_entities SET classification_status='verified', subtype=NULL
 WHERE id='1111dead-0000-4000-8000-000000000001'$$);

-- M12 철회·병합/분리
SELECT p1_try('M12a','M12','merge operation 계획 등록','accept', $$
INSERT INTO kentity_identity_operations (id, owner_key, request_key, payload_hash, operation, source_id, target_id, status, plan_hash, actor, reason)
VALUES ('0aaa0001-0000-4000-8000-000000000001','op','req-1',repeat('a',64),'merge','bbbb0002-0000-4000-8000-000000000002','bbbb0001-0000-4000-8000-000000000001','planned',repeat('b',64),'operator','merge plan')$$);
SELECT p1_try('M12b','M12','planned 인데 applied_at 이 있음','reject', $$
UPDATE kentity_identity_operations SET applied_at=now() WHERE id='0aaa0001-0000-4000-8000-000000000001'$$);
SELECT p1_try('M12c','M12','planned operation 에 redirect 연결','reject', $$
INSERT INTO kentity_redirects (from_id, to_id, operation_id)
VALUES ('bbbb0002-0000-4000-8000-000000000002','bbbb0001-0000-4000-8000-000000000001','0aaa0001-0000-4000-8000-000000000001')$$);
SELECT p1_try('M12d','M12','applied merge 뒤 redirect 연결','accept', $$
UPDATE kentity_identity_operations SET status='applied', applied_at=now() WHERE id='0aaa0001-0000-4000-8000-000000000001';
INSERT INTO kentity_redirects (from_id, to_id, operation_id)
VALUES ('bbbb0002-0000-4000-8000-000000000002','bbbb0001-0000-4000-8000-000000000001','0aaa0001-0000-4000-8000-000000000001')$$);
SELECT p1_try('M12e','M12','자기 자신으로 redirect','reject', $$
INSERT INTO kentity_redirects (from_id, to_id, operation_id)
VALUES ('cccc0001-0000-4000-8000-000000000001','cccc0001-0000-4000-8000-000000000001','0aaa0001-0000-4000-8000-000000000001')$$);
SELECT p1_try('M12f','M12','retype 인데 source<>target','reject', $$
INSERT INTO kentity_identity_operations (owner_key, request_key, payload_hash, operation, source_id, target_id, plan_hash, actor, reason)
VALUES ('op','req-2',repeat('c',64),'retype','dddd0001-0000-4000-8000-000000000001','dddd0002-0000-4000-8000-000000000002',repeat('d',64),'operator','bad retype')$$);
SELECT p1_try('M12g','M12','withdrawn_name guard 에 claim_fingerprint 없음','reject', $$
INSERT INTO kentity_source_guards (origin_system, origin_table, origin_pk, scope_key, scope_kind, entity_id, identity_revision, locale, guard_kind, reason_code, source_fingerprint)
VALUES ('kdb','kwave_entities','{"id":"x"}','ns1','name_slot','66660001-0000-4000-8000-000000000001',1,'ja','withdrawn_name','withdrawn',repeat('e',64))$$);
SELECT p1_try('M12h','M12','empty_slot guard (fingerprint 없음이 정상)','accept', $$
INSERT INTO kentity_source_guards (origin_system, origin_table, origin_pk, scope_key, scope_kind, entity_id, identity_revision, locale, guard_kind, reason_code, source_fingerprint)
VALUES ('kdb','kwave_entities','{"id":"y"}','ns2','name_slot','66660001-0000-4000-8000-000000000001',1,'ja','empty_slot','operator_emptied',repeat('f',64))$$);
SELECT p1_try('M12i','M12','rejected_binding 인데 대상 UUID 없음','reject', $$
INSERT INTO kentity_source_guards (origin_system, origin_table, origin_pk, scope_key, scope_kind, subject_system, subject_table, subject_pk, guard_kind, reason_code, source_fingerprint)
VALUES ('tdb','tdb_places','{"id":"z"}','ns3','source_binding','tdb','tdb_places','{"id":"z"}','rejected_binding','mislink',repeat('0',64))$$);

-- M13 오래된 job·tenant scope
SELECT p1_try('M13a','M13','fill_waiters 에 readiness 없는 조합 연결','reject', $$
INSERT INTO kentity_fill_waiters (preparation_id, ordinal, locale, job_id, entity_id, identity_revision, entity_revision, scope_key)
VALUES (gen_random_uuid(), 0, 'ja', gen_random_uuid(), 'aaaa0001-0000-4000-8000-000000000001', 1, 1, 'global')$$);
SELECT p1_try('M13b','M13','outbox 에 Entity·정책 이벤트를 동시에','reject', $$
INSERT INTO kentity_invalidation_outbox (entity_id, dependency_epoch, source_policy_id, policy_revision, reason)
VALUES ('aaaa0001-0000-4000-8000-000000000001',1,gen_random_uuid(),1,'both')$$);
SELECT p1_try('M13c','M13','outbox Entity 이벤트 정상','accept', $$
INSERT INTO kentity_invalidation_outbox (entity_id, dependency_epoch, reason)
VALUES ('aaaa0001-0000-4000-8000-000000000001',1,'evidence withdrawn')$$);
SELECT p1_try('M13d','M13','같은 (entity,epoch) 중복 이벤트','reject', $$
INSERT INTO kentity_invalidation_outbox (entity_id, dependency_epoch, reason)
VALUES ('aaaa0001-0000-4000-8000-000000000001',1,'duplicate')$$);

-- ============================================================ M07 동명 span 2개 (S01)
-- 기사 한 건에 동명이인 둘(aaaa0001 가수 / aaaa0002 배우)이 각각 다른 위치에 등장한다.
-- 설계 §14.1 이 지정한 시험 3종을 그대로 건다:
--   ① A item 에 B 이름으로 ready 저장 거부  ② 같은 A 의 다른 bound revision 거부
--   ③ 미해소 item 을 ready 로 만드는 NULL 우회 거부
-- ordinal 2 는 후보가 갈려 아직 묶이지 않은 항목이다(자동 선택 금지 — I06/I07).
INSERT INTO kentity_names (entity_id, locale, value, kind, form, source_code, policy_version) VALUES
 ('aaaa0001-0000-4000-8000-000000000001','ja','表記-A1','canonical','recorded','wikidata-label','p-v1'),
 ('aaaa0002-0000-4000-8000-000000000002','ja','表記-A2','canonical','recorded','wikidata-label','p-v1');

INSERT INTO kentity_preparations (id, owner_key, idempotency_key, payload_hash, policy_version, requested_locales)
VALUES ('70070001-0000-4000-8000-000000000001','operator:m07','idem-m07','ph-m07','common-reviewed-names-v1', ARRAY['ja']);

INSERT INTO kentity_preparation_items
 (preparation_id, ordinal, term, entity_type, supplied_entity_id, resolved_entity_id,
  bound_entity_id, bound_identity_revision, bound_entity_revision, identity_state, span_start, span_end)
SELECT '70070001-0000-4000-8000-000000000001', 0, '인물-A', 'person', e.id, e.id,
       e.id, e.identity_revision, e.revision, 'resolved', 10, 14
  FROM kentity_entities e WHERE e.id = 'aaaa0001-0000-4000-8000-000000000001';
INSERT INTO kentity_preparation_items
 (preparation_id, ordinal, term, entity_type, supplied_entity_id, resolved_entity_id,
  bound_entity_id, bound_identity_revision, bound_entity_revision, identity_state, span_start, span_end)
SELECT '70070001-0000-4000-8000-000000000001', 1, '인물-A', 'person', e.id, e.id,
       e.id, e.identity_revision, e.revision, 'resolved', 80, 84
  FROM kentity_entities e WHERE e.id = 'aaaa0002-0000-4000-8000-000000000002';
INSERT INTO kentity_preparation_items (preparation_id, ordinal, term, entity_type, identity_state, candidate_ids)
VALUES ('70070001-0000-4000-8000-000000000001', 2, '인물-A', 'person', 'ambiguous',
        ARRAY['aaaa0001-0000-4000-8000-000000000001','aaaa0002-0000-4000-8000-000000000002']::uuid[]);

INSERT INTO kentity_locale_readiness (preparation_id, ordinal, locale) VALUES
 ('70070001-0000-4000-8000-000000000001',0,'ja'),
 ('70070001-0000-4000-8000-000000000001',1,'ja'),
 ('70070001-0000-4000-8000-000000000001',2,'ja');

SELECT p1_try('M07a','M07','ordinal 0 을 묶인 UUID·버전으로 확정','accept', $$
UPDATE kentity_locale_readiness l
   SET entity_id = e.id, identity_revision = e.identity_revision, entity_revision = e.revision,
       name_id = (SELECT n.id FROM kentity_names n WHERE n.entity_id = e.id AND n.locale='ja' LIMIT 1)
  FROM kentity_entities e
 WHERE e.id = 'aaaa0001-0000-4000-8000-000000000001'
   AND l.preparation_id = '70070001-0000-4000-8000-000000000001' AND l.ordinal = 0 AND l.locale = 'ja'$$);

SELECT p1_try('M07b','M07','A 의 readiness 에 B 의 이름을 붙이기 (S01 복합 FK)','reject', $$
UPDATE kentity_locale_readiness
   SET name_id = (SELECT n.id FROM kentity_names n
                   WHERE n.entity_id = 'aaaa0002-0000-4000-8000-000000000002' AND n.locale='ja' LIMIT 1)
 WHERE preparation_id = '70070001-0000-4000-8000-000000000001' AND ordinal = 0 AND locale = 'ja'$$);

SELECT p1_try('M07c','M07','ordinal 1 에 다른 ordinal 의 대상 UUID 저장','reject', $$
UPDATE kentity_locale_readiness l
   SET entity_id = e.id, identity_revision = e.identity_revision, entity_revision = e.revision
  FROM kentity_entities e
 WHERE e.id = 'aaaa0001-0000-4000-8000-000000000001'
   AND l.preparation_id = '70070001-0000-4000-8000-000000000001' AND l.ordinal = 1 AND l.locale = 'ja'$$);

SELECT p1_try('M07d','M07','맞는 UUID 지만 묶이지 않은 revision','reject', $$
UPDATE kentity_locale_readiness l
   SET entity_id = e.id, identity_revision = e.identity_revision + 1, entity_revision = e.revision
  FROM kentity_entities e
 WHERE e.id = 'aaaa0002-0000-4000-8000-000000000002'
   AND l.preparation_id = '70070001-0000-4000-8000-000000000001' AND l.ordinal = 1 AND l.locale = 'ja'$$);

SELECT p1_try('M07e','M07','ordinal 1 을 자기 대상으로 확정','accept', $$
UPDATE kentity_locale_readiness l
   SET entity_id = e.id, identity_revision = e.identity_revision, entity_revision = e.revision,
       name_id = (SELECT n.id FROM kentity_names n WHERE n.entity_id = e.id AND n.locale='ja' LIMIT 1)
  FROM kentity_entities e
 WHERE e.id = 'aaaa0002-0000-4000-8000-000000000002'
   AND l.preparation_id = '70070001-0000-4000-8000-000000000001' AND l.ordinal = 1 AND l.locale = 'ja'$$);

SELECT p1_try('M07f','M07','두 span 이 서로 다른 UUID·서로 다른 이름을 든다 (교차오염 0)','accept', $$
SELECT p1_must((
  SELECT count(DISTINCT l.entity_id) = 2 AND count(DISTINCT l.name_id) = 2 AND count(*) = 2
     AND bool_and(n.entity_id = l.entity_id)
    FROM kentity_locale_readiness l JOIN kentity_names n ON n.id = l.name_id
   WHERE l.preparation_id = '70070001-0000-4000-8000-000000000001' AND l.ordinal IN (0,1)))$$);

SELECT p1_try('M07g','M07','미해소 항목을 ready 로 만드는 NULL 우회','reject', $$
UPDATE kentity_locale_readiness l
   SET state='ready', value='表記-A1', ready_at=now(), policy_proof='[{"policy_id":"x","revision":1}]'::jsonb,
       entity_id = e.id, identity_revision = e.identity_revision, entity_revision = e.revision
  FROM kentity_entities e
 WHERE e.id = 'aaaa0001-0000-4000-8000-000000000001'
   AND l.preparation_id = '70070001-0000-4000-8000-000000000001' AND l.ordinal = 2 AND l.locale = 'ja'$$);

-- M14 locale/형식
SELECT p1_try('M14a','M14','사전에 없는 locale 로 이름 저장','reject', $$
INSERT INTO kentity_names (entity_id, locale, value, kind, form, source_code, normalized_value)
VALUES ('55550001-0000-4000-8000-000000000001','zz-ZZ','표기-N','canonical','recorded','prov-x','표기-N')$$);
SELECT p1_try('M14b','M14','정확 태그 zh-Hant 로 저장','accept', $$
INSERT INTO kentity_names (entity_id, locale, value, kind, form, source_code, normalized_value, policy_version)
VALUES ('55550001-0000-4000-8000-000000000001','zh-Hant','표기-N-hant','canonical','translated','opencc','표기-N-hant','p-v1')$$);
SELECT p1_try('M14c','M14','근거 없이 이름 verified','reject', $$
UPDATE kentity_names SET status='verified'
 WHERE entity_id='55550001-0000-4000-8000-000000000001' AND locale='zh-Hant'$$);
SELECT p1_try('M14d','M14','정책 승인 없이 허용 플래그 true','reject', $$
INSERT INTO kentity_source_policies (provider, version, status, name_export_allowed)
VALUES ('gtranslate','v1','unreviewed',true)$$);
SELECT p1_try('M14e','M14','검토자 없이 approved','reject', $$
INSERT INTO kentity_source_policies (provider, version, status)
VALUES ('wikidata','v1','approved')$$);
SELECT p1_try('M14f','M14','검토자·시각 갖춘 approved 정책','accept', $$
INSERT INTO kentity_source_policies (provider, version, status, reviewed_by, reviewed_at, license_code, name_export_allowed)
VALUES ('prov-x','v1','approved','operator',now(),'CC0',true)$$);
-- D-34: 같은 provider 의 승인 정책이 둘이면 어느 권한이 적용되는지 ORDER BY 가 정하게 된다.
SELECT p1_try('M14g','M14','이미 승인된 provider 에 승인 정책을 하나 더','reject', $$
INSERT INTO kentity_source_policies (provider, version, status, reviewed_by, reviewed_at, license_code, name_export_allowed)
VALUES ('wikidata','v-dup','approved','operator',now(),'CC0',true)$$);
-- 내릴 때 허용 플래그도 함께 꺼야 한다(flags_need_approval). 만료된 정책이 권한만
-- 들고 남아 있으면 "승인 없이 허용"이 되는데, 그걸 CHECK 가 이미 막고 있다.
SELECT p1_try('M14h','M14','옛 버전을 내리고 새 버전을 올리는 인계는 허용','accept', $$
UPDATE kentity_source_policies
   SET status='expired', name_export_allowed=false, storage_allowed=false,
       verification_allowed=false, excerpt_export_allowed=false
 WHERE provider='prov-x' AND version='v1';
INSERT INTO kentity_source_policies (provider, version, status, reviewed_by, reviewed_at, license_code, name_export_allowed)
VALUES ('prov-x','v2','approved','operator',now(),'CC0',true)$$);

SELECT p1_try('M12j','M12','같은 transaction 안에서 redirect 를 먼저 넣고 나중에 merge 를 applied 로','accept', $$
SET CONSTRAINTS kentity_redirect_operation_guard DEFERRED;
INSERT INTO kentity_identity_operations (id, owner_key, request_key, payload_hash, operation, source_id, target_id, status, plan_hash, actor, reason)
VALUES ('0aaa0002-0000-4000-8000-000000000002','op','req-3',repeat('2',64),'merge','dddd0003-0000-4000-8000-000000000003','dddd0002-0000-4000-8000-000000000002','planned',repeat('3',64),'operator','deferred order');
INSERT INTO kentity_redirects (from_id, to_id, operation_id)
VALUES ('dddd0003-0000-4000-8000-000000000003','dddd0002-0000-4000-8000-000000000002','0aaa0002-0000-4000-8000-000000000002');
UPDATE kentity_identity_operations SET status='applied', applied_at=now() WHERE id='0aaa0002-0000-4000-8000-000000000002';
SET CONSTRAINTS kentity_redirect_operation_guard IMMEDIATE$$);

SELECT p1_try('M12k','M12','지연 상태로 넣고 merge 를 applied 로 올리지 않으면','reject', $$
SET CONSTRAINTS kentity_redirect_operation_guard DEFERRED;
INSERT INTO kentity_identity_operations (id, owner_key, request_key, payload_hash, operation, source_id, target_id, status, plan_hash, actor, reason)
VALUES ('0aaa0003-0000-4000-8000-000000000003','op','req-4',repeat('4',64),'merge','ffff0002-0000-4000-8000-000000000002','eeee0001-0000-4000-8000-000000000001','planned',repeat('5',64),'operator','never applied');
INSERT INTO kentity_redirects (from_id, to_id, operation_id)
VALUES ('ffff0002-0000-4000-8000-000000000002','eeee0001-0000-4000-8000-000000000001','0aaa0003-0000-4000-8000-000000000003');
SET CONSTRAINTS kentity_redirect_operation_guard IMMEDIATE$$);

-- ============================================================ P1.06 병합·경쟁
SELECT p1_try('R01','P1.06','같은 멱등키로 두 번째 operation','reject', $$
INSERT INTO kentity_identity_operations (owner_key, request_key, payload_hash, operation, source_id, target_id, plan_hash, actor, reason)
VALUES ('op','req-1',repeat('9',64),'merge','dddd0001-0000-4000-8000-000000000001','dddd0002-0000-4000-8000-000000000002',repeat('8',64),'operator','dup key')$$);
INSERT INTO kentity_migration_runs (id, owner_key, request_key, request_hash, mode, mapper_version, canonicalization_version, source_basis, cohort_hash, selection_policy_hash, expected_records, expected_entities, max_changed_objects)
VALUES ('0bbb0001-0000-4000-8000-000000000001','op','run-1',repeat('1',64),'dry_run','m-v1','c-v1','{}',repeat('2',64),repeat('3',64),1,1,10);
SELECT p1_try('R02','P1.06','migration record: create 인데 기대 revision 이 0 이 아님','reject', $$
INSERT INTO kentity_migration_records (run_id, source_system, source_table, source_pk, source_fingerprint, disposition, target_mode, planned_target_id, expected_entity_revision, expected_identity_revision, expected_owner, plan_hash, reason_code)
VALUES ('0bbb0001-0000-4000-8000-000000000001','tdb','tdb_places','{"id":"p1"}',repeat('4',64),'include','create',gen_random_uuid(),1,1,'native',repeat('5',64),'planned')$$);
SELECT p1_try('R03','P1.06','migration record: create 정상(기대 revision 0)','accept', $$
INSERT INTO kentity_migration_records (run_id, source_system, source_table, source_pk, source_fingerprint, disposition, target_mode, planned_target_id, expected_entity_revision, expected_identity_revision, expected_owner, plan_hash, reason_code)
VALUES ('0bbb0001-0000-4000-8000-000000000001','tdb','tdb_places','{"id":"p2"}',repeat('6',64),'include','create',gen_random_uuid(),0,0,'native',repeat('7',64),'planned')$$);
SELECT p1_try('R04','P1.06','apply run 을 승인 없이 running 으로','reject', $$
INSERT INTO kentity_migration_runs (owner_key, request_key, request_hash, mode, state, mapper_version, canonicalization_version, source_basis, cohort_hash, selection_policy_hash, expected_records, expected_entities, max_changed_objects)
VALUES ('op','run-2',repeat('a',64),'apply','running','m-v1','c-v1','{}',repeat('b',64),repeat('c',64),1,1,10)$$);
SELECT p1_try('R05','P1.06','audit: operation_id 만 있고 객체 키/해시 없음','reject', $$
INSERT INTO kentity_audit_events (entity_id, actor, action, reason, operation_id)
VALUES ('bbbb0001-0000-4000-8000-000000000001','op','merge','test','0aaa0001-0000-4000-8000-000000000001')$$);

-- ============================================================ P1.07 공통 writer 보호선 (X01/X03/X11)

SELECT p1_try('X00','P1.07','승인된 원천 정책 13종 확인','accept', $$
SELECT p1_must((SELECT count(DISTINCT provider) FROM kentity_source_policies WHERE status='approved' AND name_export_allowed
                  AND provider IN ('wikidata','operator','correction','media-consensus',
                                   'musicbrainz','tmdb','itunes','kofic','kmdb','discogs','netflix','disney','naver-people'))=13)$$);

SELECT p1_try('X00b','P1.07','미승인 provider 는 여전히 차단','accept', $$
SELECT p1_must(NOT EXISTS (SELECT 1 FROM kentity_source_policies
  WHERE provider IN ('gtranslate','codex-fallback','romanization','opencc','rss-observation') AND status='approved'))$$);

-- fixture: 운영자가 잠근 표기 + 검증된 대표명
SELECT p1_try('X01a','P1.07','운영자 잠금 표기 생성','accept', $$
INSERT INTO kentity_evidence (id, entity_id, provider, source_record_id, source_url, claim_type, status, summary, claim_fingerprint, source_observation_hash, independent_origin, verified_by, verified_at, license_code, export_allowed)
VALUES ('e0ab0001-0000-4000-8000-000000000001','66660001-0000-4000-8000-000000000001','operator','x01','https://example.invalid/x01','name','verified','운영자 확정','fp-x01','soh-x01','operator','op',now(),'internal-operator-review',true);
INSERT INTO kentity_names (entity_id, locale, value, kind, form, status, evidence_id, source_code, operator_locked, policy_version)
VALUES ('66660001-0000-4000-8000-000000000001','ja','표기-잠금','canonical','recorded','verified','e0ab0001-0000-4000-8000-000000000001','operator',true,'p-v1')$$);

SELECT p1_try('X01b','P1.07','잠긴 표기를 자동 출처가 덮어쓰기','reject', $$
UPDATE kentity_names SET value='표기-자동덮어씀', source_code='gtranslate'
 WHERE entity_id='66660001-0000-4000-8000-000000000001' AND locale='ja'$$);

SELECT p1_try('X01c','P1.07','잠긴 표기를 운영자가 정정','accept', $$
UPDATE kentity_names SET value='표기-운영자정정', source_code='operator'
 WHERE entity_id='66660001-0000-4000-8000-000000000001' AND locale='ja'$$);

SELECT p1_try('X03a','P1.07','검증 대표명을 낮은 등급 출처로 교체','reject', $$
UPDATE kentity_names SET operator_locked=false WHERE entity_id='66660001-0000-4000-8000-000000000001' AND locale='ja';
UPDATE kentity_names SET value='표기-기계번역', source_code='gtranslate'
 WHERE entity_id='66660001-0000-4000-8000-000000000001' AND locale='ja'$$);

-- X01c 에서 출처가 operator(1등급)가 됐으므로 correction-verified(4등급)는 실제로 더 낮다.
-- 같은 등급(operator-locked, 1등급)으로 바꾸는 것은 허용돼야 한다.
SELECT p1_try('X03b','P1.07','같은 등급 출처의 교체는 허용','accept', $$
UPDATE kentity_names SET value='표기-정정본', source_code='operator-locked'
 WHERE entity_id='66660001-0000-4000-8000-000000000001' AND locale='ja'$$);

SELECT p1_try('X03c','P1.07','등급 비교가 실제 tier 를 쓰는지 (operator 1 < correction-verified 4)','accept', $$
SELECT p1_must(kdb_source_priority('operator') < kdb_source_priority('correction-verified')
           AND kdb_source_priority('correction-verified') < kdb_source_priority('gtranslate'))$$);

SELECT p1_try('X11a','P1.07','철회된 주장에 guard 를 건다','accept', $$
INSERT INTO kentity_source_guards (origin_system, origin_table, origin_pk, scope_key, scope_kind, entity_id, identity_revision, locale, claim_fingerprint, guard_kind, state, decided_by, decided_at, decision_ref, reason_code, source_fingerprint)
VALUES ('kdb','kwave_entities','{"id":"x11"}','x11-slot','name_slot','55550001-0000-4000-8000-000000000001',1,'ja',
        kentity_name_claim_fingerprint('ja','표기-철회됨','canonical','recorded'),
        'withdrawn_name','active','operator',now(),'ref-x11','withdrawn_by_operator',repeat('a',64))$$);

SELECT p1_try('X11b','P1.07','철회된 그 주장을 재설치','reject', $$
INSERT INTO kentity_names (entity_id, locale, value, kind, form, source_code, normalized_value, policy_version)
VALUES ('55550001-0000-4000-8000-000000000001','ja','표기-철회됨','canonical','recorded','wikidata-label','표기-철회됨','p-v1')$$);

SELECT p1_try('X11c','P1.07','같은 슬롯의 다른 주장은 허용 (슬롯 전체를 막지 않는다)','accept', $$
INSERT INTO kentity_names (entity_id, locale, value, kind, form, source_code, normalized_value, policy_version)
VALUES ('55550001-0000-4000-8000-000000000001','ja','표기-다른값','canonical','recorded','wikidata-label','표기-다른값','p-v1')$$);

SELECT p1_try('X11d','P1.07','운영자가 비운 슬롯에 별칭 자동 승격','reject', $$
INSERT INTO kentity_source_guards (origin_system, origin_table, origin_pk, scope_key, scope_kind, entity_id, identity_revision, locale, guard_kind, state, decided_by, decided_at, decision_ref, reason_code, source_fingerprint)
VALUES ('kdb','kwave_entities','{"id":"x11e"}','x11-empty','name_slot','99990002-0000-4000-8000-000000000002',1,'ja',
        'empty_slot','active','operator',now(),'ref-x11e','operator_emptied',repeat('b',64));
INSERT INTO kentity_names (entity_id, locale, value, kind, form, source_code, normalized_value, policy_version)
VALUES ('99990002-0000-4000-8000-000000000002','ja','표기-자동승격','canonical','recorded','wikidata-label','표기-자동승격','p-v1')$$);

SELECT p1_try('X11e','P1.07','비운 슬롯이어도 별칭(alias)은 허용','accept', $$
INSERT INTO kentity_names (entity_id, locale, value, kind, form, source_code, normalized_value, policy_version)
VALUES ('99990002-0000-4000-8000-000000000002','ja','표기-별칭','alias','recorded','wikidata-label','표기-별칭','p-v1')$$);

SELECT p1_try('X22','P1.07','정규화 트리거가 normalized_value 를 채운다(D-22)','accept', $$
INSERT INTO kentity_names (entity_id, locale, value, kind, form, source_code, policy_version)
VALUES ('55550001-0000-4000-8000-000000000001','en','  Spaced   Name  ','alias','recorded','wikidata-label','p-v1');
SELECT p1_must((SELECT normalized_value FROM kentity_names
   WHERE entity_id='55550001-0000-4000-8000-000000000001' AND locale='en' AND kind='alias')='Spaced Name')$$);

-- ============================================================ TRG 트리거 캐스케이드 (전수 감사 후속)
-- 지금까지 시험은 "한 문장이 제약에 걸리는가"만 봤다. 과거 런타임에서만 터진 4건은 전부
-- 트리거 *본문*이었고, 그중 셋은 다른 트리거를 부르는 **캐스케이드** 경로였다.
-- 여기서는 "근거를 철회하면 무엇이 함께 무효화되는가"를 끝까지 따라간다.
INSERT INTO kentity_entities (id, entity_type, subtype, canonical_ko, origin_system, write_owner, status) VALUES
 ('7a000001-0000-4000-8000-000000000001','person','real','인물-T','native','native','candidate');

INSERT INTO kentity_evidence (id, entity_id, provider, source_record_id, source_url, claim_type, status,
       license_code, export_allowed, verified_by, verified_at, summary,
       claim_fingerprint, source_observation_hash, independent_origin) VALUES
 ('7a00e001-0000-4000-8000-000000000001','7a000001-0000-4000-8000-000000000001','wikidata','Q7000001','https://example.invalid/t-id','identity','verified','CC0-1.0',true,'합성검수',now(),'T 정체성 근거','fp-t-id','soh-t-id','origin-t'),
 ('7a00e002-0000-4000-8000-000000000002','7a000001-0000-4000-8000-000000000001','wikidata','Q7000001','https://example.invalid/t-nm','name','verified','CC0-1.0',true,'합성검수',now(),'T 이름 근거','fp-t-nm','soh-t-nm','origin-t'),
 ('7a00e003-0000-4000-8000-000000000003','7a000001-0000-4000-8000-000000000001','wikidata','Q7000001','https://example.invalid/t-oc','occupation','verified','CC0-1.0',true,'합성검수',now(),'T 직업 근거','fp-t-oc','soh-t-oc','origin-t');

-- 운영자가 잠근 대표명. 실제 운영에서 가장 흔한 상태다.
INSERT INTO kentity_names (entity_id, locale, value, kind, form, status, evidence_id, source_code, operator_locked, policy_version)
VALUES ('7a000001-0000-4000-8000-000000000001','ja','表記-T','canonical','recorded','verified',
        '7a00e002-0000-4000-8000-000000000002','wikidata-label',true,'p-v1');

INSERT INTO kentity_person_roles (entity_id, role_code, status, evidence_id, assigned_by, reason, policy_version, verified_by, verified_at)
VALUES ('7a000001-0000-4000-8000-000000000001','singer','verified','7a00e003-0000-4000-8000-000000000003','t','T 직업','p-v1','합성검수',now());

-- TRG01 ★ 안전장치가 안전장치를 막지 않아야 한다.
-- 철회 전파는 kentity_names 를 blocked 로 내린다. 그런데 이름 보호선(P1.07)은 "운영자가
-- 잠근 이름을 자동 출처가 바꾸는 것"을 막는다. 전파 UPDATE 는 source_code 를 그대로 둔 채
-- status 만 바꾸므로 보호선이 이를 자동 출처의 덮어쓰기로 오인해 예외를 던지고,
-- 철회 트랜잭션 전체가 중단된다. 즉 근거를 철회할 수 없게 된다.
SELECT p1_try('TRG01','TRG','운영자 잠금 이름이 있어도 근거를 철회할 수 있다','accept', $$
UPDATE kentity_evidence SET status='withdrawn'
 WHERE id='7a00e002-0000-4000-8000-000000000002'$$);

SELECT p1_try('TRG02','TRG','철회된 이름 근거의 이름이 verified 로 남지 않는다','accept', $$
SELECT p1_must(NOT EXISTS (SELECT 1 FROM kentity_names
  WHERE evidence_id='7a00e002-0000-4000-8000-000000000002' AND status='verified'))$$);

-- TRG03 ★ 철회가 이름/외부ID 에서 멈춘다. P1 이 넓힌 "verified 는 근거 필수" 면들
-- (직업·분야·분류·이름근거)은 전파 대상에 들어 있지 않아, 철회된 근거를 가리키는
-- verified 행이 그대로 남는다.
SELECT p1_try('TRG03','TRG','직업 근거를 철회하면 그 직업이 verified 로 남지 않는다','accept', $$
UPDATE kentity_evidence SET status='withdrawn' WHERE id='7a00e003-0000-4000-8000-000000000003';
SELECT p1_must(NOT EXISTS (SELECT 1 FROM kentity_person_roles
  WHERE evidence_id='7a00e003-0000-4000-8000-000000000003' AND status='verified'))$$);

-- TRG04 ★ 의존 guard 가 자식 근거를 claim_type='name' 으로 하드코딩한다.
-- P1 이 claim_type 을 7종으로 넓히고 PK 를 (evidence_id, depends_on_id) 로 넓혀 다중 부모를
-- 허용해 놓고, 정작 직업·분류·프로필 근거의 의존은 등록조차 안 된다.
SELECT p1_try('TRG04','TRG','직업 근거도 정체성 근거에 의존을 걸 수 있다','accept', $$
INSERT INTO kentity_evidence (id, entity_id, provider, source_record_id, source_url, claim_type, status,
       license_code, export_allowed, verified_by, verified_at, summary,
       claim_fingerprint, source_observation_hash, independent_origin)
VALUES ('7a00e004-0000-4000-8000-000000000004','7a000001-0000-4000-8000-000000000001','wikidata','Q7000001','https://example.invalid/t-oc2','occupation','verified','CC0-1.0',true,'합성검수',now(),'T 직업 근거2','fp-t-oc2','soh-t-oc2','origin-t');
INSERT INTO kentity_evidence_dependencies (evidence_id, entity_id, depends_on_id)
VALUES ('7a00e004-0000-4000-8000-000000000004','7a000001-0000-4000-8000-000000000001','7a00e001-0000-4000-8000-000000000001')$$);

-- TRG05 ★ "승인 근거는 불변"의 비교 튜플이 10컬럼에서 멈춰 있다. P1 이 추가한
-- claim_fingerprint/source_observation_hash/claim_payload 는 감시 밖인데, 앞의 둘은
-- 새 UNIQUE(kentity_evidence_claim_key)의 구성 컬럼이다 — 주장의 정체성이 조용히 바뀐다.
SELECT p1_try('TRG05','TRG','승인된 근거의 주장 지문을 바꾸기','reject', $$
UPDATE kentity_evidence SET claim_fingerprint='fp-t-id-바뀜'
 WHERE id='7a00e001-0000-4000-8000-000000000001'$$);

-- TRG06 ★ 외부 ID 예약이 주인을 적지 않으면 보호 분기가 도달하지 않는다 (D-35).
-- 같은 QID 를 다른 대상이 가져가려 할 때 막는 조건이 owner_id IS NOT NULL 인데,
-- 예약을 만들 때 entity_id 를 채우지 않으면 그 값은 항상 NULL 이다.
INSERT INTO kwave_entities (id, entity_type, canonical_ko, status) VALUES
 ('7b000001-0000-4000-8000-000000000001','person','외부ID시험 가','candidate'),
 ('7b000002-0000-4000-8000-000000000002','person','외부ID시험 나','candidate');

SELECT p1_try('TRG06a','TRG','첫 대상이 외부 ID 를 가져간다','accept', $$
INSERT INTO kwave_entity_external_refs (entity_id, provider, external_id)
VALUES ('7b000001-0000-4000-8000-000000000001','wikidata','Q7B00001')$$);

SELECT p1_try('TRG06b','TRG','예약에 주인이 적혔다','accept', $$
SELECT p1_must((SELECT entity_id FROM kentity_id_reservations
  WHERE provider='wikidata' AND external_id='Q7B00001')='7b000001-0000-4000-8000-000000000001')$$);

SELECT p1_try('TRG06c','TRG','다른 대상이 같은 외부 ID 를 가져가기','reject', $$
INSERT INTO kwave_entity_external_refs (entity_id, provider, external_id)
VALUES ('7b000002-0000-4000-8000-000000000002','wikidata','Q7B00001')$$);

SELECT p1_try('TRG06d','TRG','같은 대상이 같은 외부 ID 를 다시 넣는 것은 허용','accept', $$
INSERT INTO kwave_entity_external_refs (entity_id, provider, external_id)
VALUES ('7b000001-0000-4000-8000-000000000001','wikidata','Q7B00001')
ON CONFLICT DO NOTHING$$);

SELECT '완료' AS done;
COMMIT;

-- 0105_kdb_attempts_input_hash_trigger — 원장의 input_hash 를 트리거로 강제한다.
--
-- ★왜 필요한가(2026-08-07): 선정 규칙(FillRetryPredicate)은 "원장의 input_hash 가 지금
-- 엔티티 지문과 같으면 재시도하지 않는다"이다. 그런데 기록하는 쪽이 input_hash 를 빠뜨리면
-- ''(기본값)이 들어가 **영영 일치하지 않고**, 그 레인은 같은 엔티티를 매 주기 다시 집는
-- 공회전이 된다. 선정만 고치고 기록을 안 고치면 조용히 이 상태가 된다.
--
-- 실제로 그렇게 됐다. 0104 에서 그라운딩 선정을 지문 규칙으로 바꿨는데 기록하는
-- recordLocalFillAttempt 를 안 고쳐서, 배포 직후 실측에서 enrichground 20건이 전부
-- input_hash='' 로 쌓였다.
--
-- ★호출부를 하나씩 고치는 방식은 또 빠뜨린다. 원장에 INSERT 하는 곳이 **26군데**(20개
-- 파일)이고, 이번 작업 내내 반복한 실패가 정확히 "한 곳을 고치고 다른 곳을 잊는" 것이었다.
-- 그래서 규칙을 코드가 아니라 **테이블에** 둔다. 어느 레인이 새로 생기든, 어떤 SQL 로
-- 쓰든, input_hash 는 항상 그 시점 엔티티 지문이 된다.
--
-- 명시적으로 값을 넣는 호출부(MarkFillAttempt 등)와 충돌하지 않는다 — 둘 다 원하는 값이
-- "지금 이 엔티티의 지문"으로 같다. 트리거가 단일 진실 공급원이 된다.
--
-- 비용: 원장 쓰기 1건당 kwave_entities PK 조회 1회. 실체화 컬럼을 읽을 뿐 해시를 계산하지
-- 않으므로(0104) 사실상 무시할 수준이다.

BEGIN;

CREATE OR REPLACE FUNCTION kdb_attempts_stamp_hash() RETURNS trigger
LANGUAGE plpgsql AS $fn$
BEGIN
  NEW.input_hash := COALESCE(
    (SELECT e.fill_input_hash FROM kwave_entities e WHERE e.id = NEW.entity_id), '');
  RETURN NEW;
END $fn$;

DROP TRIGGER IF EXISTS trg_kdb_attempts_stamp_hash ON kwave_kdb_enrich_attempts;
CREATE TRIGGER trg_kdb_attempts_stamp_hash
  BEFORE INSERT OR UPDATE ON kwave_kdb_enrich_attempts
  FOR EACH ROW EXECUTE FUNCTION kdb_attempts_stamp_hash();

COMMENT ON TRIGGER trg_kdb_attempts_stamp_hash ON kwave_kdb_enrich_attempts IS
  '기록 시점의 엔티티 입력 지문을 항상 남긴다. 호출부가 빠뜨리면 재시도 규칙이 무력화되고 '
  '해당 레인이 매 주기 공회전한다 — 그 실수를 구조적으로 불가능하게 만든다.';

-- ★기존 행은 소급하지 않는다. input_hash='' 인 행은 "지문 없이 기록된 옛 시도"이고,
-- 지금 지문으로 채워 넣으면 **그 시점에 실제로 시도했는지 알 수 없는 것을 시도했다고
-- 단정**하는 셈이다. 빈 값으로 두면 규칙상 "재시도 가능"으로 읽히고, 한 바퀴 돌면서
-- 정직한 지문이 쌓인다. 어제 다른 레인에서 이미 검증된 회복 경로다.

COMMIT;

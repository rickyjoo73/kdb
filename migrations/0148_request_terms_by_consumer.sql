-- 0148: 소비자별 요청 이력 조회 인덱스
--
-- ★왜 (2026-09-16). `GET /v1/my/changes` 가 "이 소비자가 물었던 낱말"을 찾는다:
--     WHERE consumer_id = $1 AND created_at >= $2
--   지금 이 표의 인덱스는 (created_at DESC) 와 (request_group) 뿐이라, 소비자 하나를
--   고르는 데 전체를 훑는다. 7일에 4,338행씩 쌓이므로 연 20만 행이 넘는다 —
--   지금은 빨라도 반년 뒤에 느려지고, 그때는 소비자가 부르는 문이라 소비자가 느려진다.
--
-- ★CONCURRENTLY 를 쓰지 않는다. 이 표는 append-only 이고 현재 12만 행 규모라
--   짧은 락으로 끝난다. 트랜잭션 안에서 만들어 실패 시 되돌아가게 한다.
BEGIN;

CREATE INDEX IF NOT EXISTS kwave_kdb_request_terms_consumer_idx
    ON kwave_kdb_request_terms (consumer_id, created_at DESC);

COMMIT;

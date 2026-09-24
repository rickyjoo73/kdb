-- 0155: 답하지 못한 요청어의 자동 판정 기록 (2026-09-24).
--
-- ★왜. 최근 7일 prepare 이름의 33%(1,416/4,336)가 조회되지 않았다. 발굴 큐가 대부분
--   «review» 로 끝났고 그 검수를 할 사람이 없었다 — review 가 사실상 «무시» 였다.
--   review-judge 레인이 판정 모델(gpt-6-luna)로 가르고 demand-register 경로로 집행한다.
--   이 표는 «이미 물어봤다» 를 적어 같은 이름을 7일 안에 다시 묻지 않게 한다.
CREATE TABLE IF NOT EXISTS kwave_kdb_review_judgments (
    term_ko     text PRIMARY KEY,
    action      text NOT NULL,          -- register|promote|reopen|reject|foreign|skip
    entity_type text NOT NULL DEFAULT '',
    reason      text NOT NULL DEFAULT '',
    model       text NOT NULL DEFAULT '',
    judged_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS kwave_kdb_review_judgments_at_idx ON kwave_kdb_review_judgments (judged_at);

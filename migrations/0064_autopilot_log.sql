-- 0064_autopilot_log.sql
-- autopilot 8-step cycle 결과를 cycle 당 1 row 로 기록 (handoff #4 §3.5).
-- 기존엔 로그(log.Printf)로만 남아 추세 파악 불가 — 이 테이블로 admin
-- dashboard 에 "최근 autopilot cycle" 추세를 보인다. autopilot.Run() 끝에서
-- Report 를 INSERT (plain autopilot 경로; KDB_HERMES_ENABLED 시엔 hermes_runs).
BEGIN;

CREATE TABLE IF NOT EXISTS kwave_kdb_autopilot_log (
    id                BIGSERIAL   PRIMARY KEY,
    ran_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    duration_ms       INTEGER     NOT NULL DEFAULT 0,
    jamo_merged       INTEGER     NOT NULL DEFAULT 0,
    jamo_rejected     INTEGER     NOT NULL DEFAULT 0,
    persons_added     INTEGER     NOT NULL DEFAULT 0,
    entity_type_fixed INTEGER     NOT NULL DEFAULT 0,
    non_entity_reject INTEGER     NOT NULL DEFAULT 0,
    classified        INTEGER     NOT NULL DEFAULT 0,
    classify_deferred INTEGER     NOT NULL DEFAULT 0,
    promoted          INTEGER     NOT NULL DEFAULT 0,
    enriched          INTEGER     NOT NULL DEFAULT 0,
    quality_fixed     INTEGER     NOT NULL DEFAULT 0,
    alias_resolved    INTEGER     NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_kwave_kdb_autopilot_log_ran_at
    ON kwave_kdb_autopilot_log (ran_at DESC);

COMMIT;

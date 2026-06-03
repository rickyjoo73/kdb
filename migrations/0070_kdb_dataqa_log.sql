-- 0070_kdb_dataqa_log.sql — gpt-5.5 데이터 QA 감사/복구 로그.
--
-- dataqa 가 오염(contaminated)으로 판정해 canonical_<loc> 값을 비울 때, 원본 값/출처/
-- 판정/이유를 여기에 먼저 적재한다. LLM 오판 대비 — reverted_at 으로 되돌리기 가능.
-- "문제 발견 시 복구 기능"의 근거 테이블(운영자 신뢰 없이 자동 적용하던 위험 해소).

BEGIN;

CREATE TABLE IF NOT EXISTS kwave_kdb_dataqa_log (
  id          bigserial PRIMARY KEY,
  entity_id   uuid NOT NULL,
  locale      text NOT NULL,                       -- en/vi/es/id/pt_br
  old_value   text NOT NULL,                       -- 비우기 전 값(복구용)
  old_source  text,                                -- 비우기 전 *_source
  verdict     text NOT NULL,                       -- contaminated 등
  reason      text,                                -- gpt-5.5 판단 근거
  model       text NOT NULL DEFAULT 'gpt-5.5',
  created_at  timestamptz NOT NULL DEFAULT now(),
  reverted_at timestamptz                          -- NOT NULL = 복구(되돌림)됨
);

CREATE INDEX IF NOT EXISTS kwave_kdb_dataqa_log_entity_idx ON kwave_kdb_dataqa_log(entity_id);
CREATE INDEX IF NOT EXISTS kwave_kdb_dataqa_log_active_idx  ON kwave_kdb_dataqa_log(created_at) WHERE reverted_at IS NULL;

COMMENT ON TABLE kwave_kdb_dataqa_log IS
  'gpt-5.5 dataqa 가 비운 오염 locale 값의 감사/복구 로그. reverted_at 으로 원복 가능.';

COMMIT;

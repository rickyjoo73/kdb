-- tier-3 상시 판정 레인(kdb-adjudicate.sh)의 감사·쿨다운 추적.
-- 정체된 고유명사(review 큐 aged/exhausted)를 프런티어 지식모델(claude+codex 패널)로
-- 판정한 이력. 재판정 쿨다운(30d)과 revert 근거를 겸한다.
CREATE TABLE IF NOT EXISTS kwave_kdb_adjudication_log (
  id            bigserial PRIMARY KEY,
  queue_id      uuid NOT NULL,
  term_ko       text NOT NULL DEFAULT '',
  verdict       text NOT NULL,                -- approve | reject | keep
  entity_type   text NOT NULL DEFAULT '',
  models        text NOT NULL DEFAULT '',     -- 예: 'claude+codex(agree)' | 'claude-only'
  agreed        boolean NOT NULL DEFAULT false,
  evidence      text NOT NULL DEFAULT '',
  created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS kwave_kdb_adjudication_queue_idx ON kwave_kdb_adjudication_log (queue_id, created_at DESC);
CREATE INDEX IF NOT EXISTS kwave_kdb_adjudication_recent_idx ON kwave_kdb_adjudication_log (created_at DESC);

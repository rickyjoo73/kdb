-- 0097: active 재심 레인(kdb-recheck-active.sh) 감사 로그. drop/retype 을 전건 추적해
-- revert 가능하게 한다(오거부 금칙 — 서빙 값 변경은 항상 되돌릴 수 있어야 한다).
CREATE TABLE IF NOT EXISTS kwave_kdb_recheck_log (
  id          bigserial PRIMARY KEY,
  entity_id   uuid NOT NULL,
  term_ko     text NOT NULL DEFAULT '',
  verdict     text NOT NULL,
  new_type    text NOT NULL DEFAULT '',
  models      text NOT NULL DEFAULT '',
  agreed      boolean NOT NULL DEFAULT false,
  evidence    text NOT NULL DEFAULT '',
  reverted_at timestamptz,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS kwave_kdb_recheck_entity_idx ON kwave_kdb_recheck_log (entity_id, created_at DESC);
CREATE INDEX IF NOT EXISTS kwave_kdb_recheck_recent_idx ON kwave_kdb_recheck_log (created_at DESC);

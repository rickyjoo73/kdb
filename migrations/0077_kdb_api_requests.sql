-- 0077: 클라이언트(소비자) API 요청 로그 — 대시보드 가시화용.
-- /v1/* 호출을 소비자·엔드포인트·상태·지연과 함께 기록. health/docs 제외.
CREATE TABLE IF NOT EXISTS kwave_kdb_api_requests (
  id           bigserial PRIMARY KEY,
  consumer_id  uuid REFERENCES kwave_kdb_api_consumers(id) ON DELETE SET NULL,
  tier         text NOT NULL DEFAULT '',
  method       text NOT NULL DEFAULT '',
  path         text NOT NULL DEFAULT '',
  query        text NOT NULL DEFAULT '',
  status       int  NOT NULL DEFAULT 0,
  duration_ms  int  NOT NULL DEFAULT 0,
  created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS kwave_kdb_api_requests_recent_idx   ON kwave_kdb_api_requests (created_at DESC);
CREATE INDEX IF NOT EXISTS kwave_kdb_api_requests_consumer_idx ON kwave_kdb_api_requests (consumer_id, created_at DESC);
CREATE INDEX IF NOT EXISTS kwave_kdb_api_requests_path_idx     ON kwave_kdb_api_requests (path, created_at DESC);

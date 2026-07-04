-- 0086_kdb_backup_log.sql
-- DB 자동백업 기록. 백업 스크립트(scripts/kdb-db-backup.sh)가 성공 시 1행 INSERT →
-- admin /admin/ops/health 가 마지막 백업 시각을 표시하고 오래되면(>24h) 경고(점검=주기).
-- 백업 자체의 존재뿐 아니라 "실제로 돌고 있는가"를 감시하기 위함.

CREATE TABLE IF NOT EXISTS kwave_kdb_backup_log (
  id         bigserial PRIMARY KEY,
  created_at timestamptz NOT NULL DEFAULT now(),
  file       text NOT NULL,
  size_bytes bigint,
  status     text NOT NULL DEFAULT 'ok'
);

CREATE INDEX IF NOT EXISTS kwave_kdb_backup_log_created_idx
  ON kwave_kdb_backup_log (created_at DESC);

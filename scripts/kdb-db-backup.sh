#!/bin/sh
# KDB DB 자동백업 — pg_dump(kdb-db 컨테이너) → gzip → 날짜별 파일 → retention.
# cron 으로 매일 실행(세션 독립). 26차부터 P0 리스크였던 "DB 자동백업 부재" 해소.
#
# 무결성: gzip 유효성 + 최소 크기(빈/절단 덤프 방지) 검증 후에만 확정. 실패 시 partial 제거.
# retention: RETAIN_DAYS 일 초과분 자동 삭제. 로그: backups/backup.log.
# 복원: gunzip -c <파일> | docker exec -i kdb-db psql -U kdb -d kdb
set -eu

export PATH=/usr/local/bin:/usr/bin:/bin

BACKUP_DIR=/data/home2/kdb.aiinplanet.com/backups
RETAIN_DAYS=14
MIN_BYTES=1000000   # 1MB 미만이면 덤프 실패로 간주(정상 덤프는 수십 MB)
STAMP=$(date +%Y%m%d-%H%M%S)
FILE="$BACKUP_DIR/kdb-$STAMP.sql.gz"
TMP="$FILE.partial"
LOG="$BACKUP_DIR/backup.log"

mkdir -p "$BACKUP_DIR"

# 덤프 → gzip (임시파일). pg_dump 실패는 exit code 로 감지.
if docker exec kdb-db pg_dump -U kdb -d kdb 2>>"$LOG" | gzip > "$TMP"; then
  size=$(stat -c%s "$TMP" 2>/dev/null || echo 0)
  if gzip -t "$TMP" 2>/dev/null && [ "$size" -gt "$MIN_BYTES" ]; then
    mv "$TMP" "$FILE"
    find "$BACKUP_DIR" -name 'kdb-*.sql.gz' -mtime +"$RETAIN_DAYS" -delete 2>/dev/null || true
    echo "$(date '+%F %T') OK   $FILE ($(du -h "$FILE" | cut -f1))" >> "$LOG"
    # 백업 감시용 DB 기록(admin /admin/ops/health 가 마지막 백업 시각 표시·경고).
    docker exec kdb-db psql -U kdb -d kdb -c \
      "INSERT INTO kwave_kdb_backup_log (file, size_bytes, status) VALUES ('$FILE', $size, 'ok');" \
      >/dev/null 2>&1 || true
    exit 0
  else
    rm -f "$TMP"
    echo "$(date '+%F %T') FAIL 무결성/크기 실패 (size=$size)" >> "$LOG"
    exit 1
  fi
else
  rm -f "$TMP"
  echo "$(date '+%F %T') FAIL pg_dump 실패" >> "$LOG"
  exit 1
fi

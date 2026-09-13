#!/usr/bin/env bash
# KDB DB 자동백업 — pg_dump(kdb-db 컨테이너) → gzip → 날짜별 파일 → retention.
# cron 으로 매일 실행(세션 독립). 26차부터 P0 리스크였던 "DB 자동백업 부재" 해소.
#
# 무결성: gzip 유효성 + 최소 크기(빈/절단 덤프 방지) 검증 후에만 확정. 실패 시 partial 제거.
# retention: RETAIN_DAYS 일 초과분 자동 삭제. 로그: backups/backup.log.
# 복원: gunzip -c <파일> | docker exec -i kdb-db psql -U kdb -d kdb
set -euo pipefail

export PATH=/usr/local/bin:/usr/bin:/bin
umask 077

# 서버 이전 후에도 현재 checkout 옆에 저장한다. cron 에서 실행해도 cwd 에 의존하지 않는다.
PROJECT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
BACKUP_DIR=${KDB_BACKUP_DIR:-"$PROJECT_DIR/backups"}
RETAIN_DAYS=${KDB_BACKUP_RETAIN_DAYS:-14}
MIN_BYTES=${KDB_BACKUP_MIN_BYTES:-1000000} # 1MB 미만이면 실패(정상은 수십 MB).
DOCKER_BIN=${KDB_BACKUP_DOCKER_BIN:-docker}
if [[ ! "$RETAIN_DAYS" =~ ^[0-9]+$ || ! "$MIN_BYTES" =~ ^[0-9]+$ || "$BACKUP_DIR" != /* ]]; then
  echo 'KDB backup: absolute KDB_BACKUP_DIR and nonnegative integer limits required' >&2
  exit 1
fi
STAMP=$(date +%Y%m%d-%H%M%S)
FILE="$BACKUP_DIR/kdb-$STAMP.sql.gz"
TMP="$FILE.$$.partial"
LOG="$BACKUP_DIR/backup.log"

mkdir -p "$BACKUP_DIR"

# 동일 초 파일 충돌·동시 cron/수동 실행 방지. 운영 Linux 의 util-linux flock 사용.
if command -v flock >/dev/null 2>&1; then
  exec 9>"$BACKUP_DIR/.backup.lock"
  if ! flock -n 9; then
    echo 'KDB backup: another backup is running' >&2
    exit 1
  fi
fi
if [[ -e "$FILE" ]]; then
  echo "KDB backup: destination already exists: $FILE" >&2
  exit 1
fi

log() {
  printf '%s %s\n' "$(date '+%F %T')" "$*" >> "$LOG"
  printf '%s\n' "$*" >&2
}
phase='pg_dump/gzip failed'
cleanup() {
  result=$?
  rm -f -- "$TMP" || true
  if (( result != 0 )); then
    log "FAIL $phase"
  fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# pipefail 이 없으면 pg_dump 실패를 gzip 의 성공 종료가 숨긴다.
"$DOCKER_BIN" exec kdb-db pg_dump -U kdb -d kdb 2>>"$LOG" | gzip > "$TMP"
phase='gzip integrity/size check failed'
gzip -t "$TMP" 2>>"$LOG"
size=$(wc -c < "$TMP" | tr -d '[:space:]')
if (( size <= MIN_BYTES )); then
  phase="dump too small (size=$size, minimum=$MIN_BYTES)"
  exit 1
fi
phase='could not finalize backup file'
mv -- "$TMP" "$FILE"

# 파일이 있어도 원장 기록 실패를 성공으로 숨기지 않는다. 실패 시 파일·기존 백업 보존.
# psql 변수의 SQL 리터럴 인용으로 경로에 공백/작은따옴표가 있어도 안전하다.
phase="backup saved but DB log failed: $FILE"
"$DOCKER_BIN" exec -i kdb-db psql -X -U kdb -d kdb -v ON_ERROR_STOP=1 \
  -v backup_file="$FILE" -v backup_size="$size" >>"$LOG" 2>&1 <<'SQL'
INSERT INTO kwave_kdb_backup_log (file, size_bytes, status)
VALUES (:'backup_file', :'backup_size'::bigint, 'ok');
SQL

# 새 파일 검증과 원장 기록이 모두 끝난 뒤에만 기존 보존 정책을 적용한다.
phase="backup saved and logged but retention cleanup failed: $FILE"
find "$BACKUP_DIR" -maxdepth 1 -type f -name 'kdb-*.sql.gz' \
  -mtime +"$RETAIN_DAYS" -delete 2>>"$LOG"
log "OK $FILE ($size bytes)"

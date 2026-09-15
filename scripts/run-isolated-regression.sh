#!/usr/bin/env bash
# 격리 회귀 — aiin23 에서 돈다. 운영 DB 를 건드리지 않는다.
#
# ★왜 별도 클론인가 (워크트리가 아니라).
#   워크트리는 같은 저장소의 브랜치를 공유한다. 배포 체크아웃이 main 을 쥐고 있으면
#   워크트리는 main 을 체크아웃할 수 없고 실패한다. 옛 서버에서 그 실패가 `|| true` 에
#   삼켜져 **5커밋 전 코드를 조용히 시험**하고 있었다(2026-09-14 발견).
#   그래서 클론을 따로 두고, 받은 SHA 가 원격과 같은지 **대조해서 다르면 멈춘다**.
#
# ★왜 template 이 운영 전량 사본인가.
#   종전 회귀 template 은 표기 1,249건짜리 옛 스냅샷이었다(운영은 268만). 표도 하나
#   모자랐다. 그런 환경의 "통과"는 현재 스키마·데이터에 대한 통과가 아니다.
#   새로 만들 때는 운영에서 pg_basebackup 으로 통째로 뜬다(같은 장비라 37초).
#
# 사용: bash scripts/run-isolated-regression.sh [빠름]
#   빠름 을 주면 격리 회귀만 돌리고 전체·race 는 건너뛴다.
set -u
export LC_ALL=C
N=kdb-p1-restore-db
W=/home/aiin/kdb/regress
FAST="${1:-}"

command -v docker >/dev/null || { echo "docker 가 없다"; exit 1; }
docker inspect "$N" >/dev/null 2>&1 || {
  echo "회귀 DB 컨테이너 $N 이 없다. docs/KDB_REGRESSION_ENV.md 를 보고 먼저 만든다."; exit 1; }

# 시험할 ref. 기본은 main 이고, 병합 전 브랜치를 검증할 때만 바꾼다.
#   KDB_REGRESS_REF=my-branch bash scripts/run-isolated-regression.sh
# ★SHA 대조는 그대로 둔다 — 이 가드가 없던 때 워크트리 체크아웃 실패가 `|| true` 에
#   삼켜져 5커밋 전 코드를 시험하고 "통과"로 보고한 전례가 있다.
REF="${KDB_REGRESS_REF:-main}"

echo "=== 체크아웃 ($REF) ==="
if [ -d "$W/.git" ]; then
  cd "$W" && git fetch -q origin "$REF" && git reset -q --hard FETCH_HEAD
else
  git clone -q https://github.com/rickyjoo73/kdb.git "$W" && cd "$W" \
    && git fetch -q origin "$REF" && git reset -q --hard FETCH_HEAD
fi
WANT="$(git rev-parse FETCH_HEAD)"; GOT="$(git rev-parse HEAD)"
[ "$WANT" = "$GOT" ] || { echo "!!! SHA 불일치 ($GOT ≠ $WANT) — 옛 코드를 시험할 뻔했다"; exit 1; }
echo "  $(git rev-parse --short HEAD) (원격 $REF 일치 ✓)"

# ★template 이 운영보다 뒤처지면 **멈춘다** (2026-09-15).
#   0135~0139 를 넣은 날, template 은 그 표들이 없는 옛 스냅샷이었다. 그래서 앵커 판정
#   시험이 "표가 없다"는 이유로 **SKIP** 됐고, 회귀는 초록으로 끝났다. 건너뛴 시험은
#   시험이 아니다 — 이 저장소가 `|| true` 에 삼켜진 실패로 이미 한 번 데인 계열이다.
#   운영 원장(kdb_schema_migrations)과 대조해 빠진 게 있으면 새로 고치라고 말하고 멈춘다.
echo "=== template 신선도 ==="
ledger() { docker exec "$1" psql -qAtX -U kdb -d kdb -c \
  "SELECT filename FROM kdb_schema_migrations ORDER BY 1" 2>/dev/null; }
PROD_LEDGER="$(ledger kdb-db)"
TMPL_LEDGER="$(ledger "$N")"
if [ -z "$PROD_LEDGER" ]; then
  echo "!!! 운영 원장을 못 읽었다 — template 이 최신인지 확인할 수 없다"; exit 1
fi
BEHIND="$(comm -23 <(printf '%s\n' "$PROD_LEDGER") <(printf '%s\n' "$TMPL_LEDGER"))"
if [ -n "$BEHIND" ]; then
  echo "!!! template 이 운영보다 뒤처졌다. 빠진 마이그레이션:"
  printf '%s\n' "$BEHIND" | sed 's/^/      /'
  echo "    docs/KDB_REGRESSION_ENV.md §3 의 절차로 template 을 새로 고친 뒤 다시 돌린다."
  exit 1
fi
echo "  운영과 같음 ($(printf '%s\n' "$PROD_LEDGER" | wc -l) 건) ✓"

echo "=== 회귀 DB 재생성 ==="
T0=$(date +%s)
docker exec "$N" psql -qAtX -U kdb -d postgres -c \
  "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname IN ('kdb','kdb_platform_migration_test') AND pid<>pg_backend_pid()" >/dev/null
# ★DROP 의 오류를 삼키지 않는다 (2026-09-15). `2>/dev/null` 로 가려 두었더니 접속이
#   남아 DROP 이 실패했을 때 **다음 줄의 CREATE 가 "이미 있다"로 죽었다** — 진짜 원인은
#   한 줄 위에 있는데 화면에는 안 나왔다. 끊고 다시 시도하되, 끝내 안 되면 그 이유를 말한다.
for attempt in 1 2 3; do
  docker exec "$N" psql -qAtX -U kdb -d postgres -c \
    "SELECT pg_terminate_backend(pid) FROM pg_stat_activity
      WHERE datname = 'kdb_platform_migration_test' AND pid <> pg_backend_pid()" >/dev/null
  if docker exec "$N" psql -qAtX -U kdb -d postgres -c \
       "DROP DATABASE IF EXISTS kdb_platform_migration_test" 2>/tmp/reg_drop_err.txt >/dev/null; then
    break
  fi
  [ "$attempt" = 3 ] && { echo "!!! 회귀 DB 를 지우지 못했다"; cat /tmp/reg_drop_err.txt; exit 1; }
  sleep 3
done
# ★STRATEGY = FILE_COPY. PostgreSQL 15+ 의 기본은 WAL_LOG 인데, template 의 **모든
# 페이지를 WAL 에 기록**한다. 작은 template 엔 안전하고 빠르지만 7GB 에선 대가가 크다.
# FILE_COPY 는 파일을 그대로 복사하고 체크포인트 두 번으로 끝낸다. 회귀 DB 는 매번 새로
# 만들고 버리는 것이라 시점 복구 대상이 아니므로 WAL 기록이 아무 쓸모가 없다.
docker exec "$N" psql -qAtX -U kdb -d postgres -c \
  "CREATE DATABASE kdb_platform_migration_test TEMPLATE kdb OWNER kdb STRATEGY = FILE_COPY" >/dev/null \
  || { echo "!!! 회귀 DB 재생성 실패"; exit 1; }
echo "  $(( $(date +%s)-T0 ))초"

# ★회귀 DB 에 **미적용 마이그레이션을 적용한다** (2026-09-15).
#   종전엔 안 돌았다. 그래서 마이그레이션이 필요한 코드는 회귀에서 **반드시 실패**했고
#   (예: 0140 의 label_en 이 없어 앵커 검수 화면이 500), 그 실패를 "회귀가 원래 그렇다"로
#   넘기는 습관이 생겼다. 넘기는 습관이 생기면 진짜 실패도 같이 넘어간다.
#   배포와 같은 순서로(원장에 없는 파일만 번호순) 적용해, 회귀가 **배포 후의 코드**를 본다.
echo "=== 미적용 마이그레이션 ==="
psqlt() { docker exec -i "$N" psql -v ON_ERROR_STOP=1 -qAtX -U kdb -d kdb_platform_migration_test "$@"; }
MIG_N=0
for f in $(ls "$W"/migrations/*.sql 2>/dev/null | sort); do
  b="$(basename "$f")"
  [ "$(psqlt -c "SELECT 1 FROM kdb_schema_migrations WHERE filename='$b'" </dev/null)" = "1" ] && continue
  printf '  %s ' "$b"
  if psqlt -f - < "$f" >/dev/null; then
    psqlt -c "INSERT INTO kdb_schema_migrations(filename) VALUES ('$b')" </dev/null >/dev/null
    MIG_N=$((MIG_N+1)); echo "✓"
  else
    echo "✗"; echo "!!! $b 적용 실패 — 이 상태로는 회귀가 무엇을 시험하는지 말할 수 없다"; exit 1
  fi
done
echo "  $MIG_N 건 적용"

mkdir -p /home/aiin/kdb/gocache /home/aiin/kdb/gomod
run() { docker run --rm --user "$(id -u):$(id -g)" --network "container:$N" -v "$W":/src:ro \
  -v /home/aiin/kdb/gocache:/gocache -v /home/aiin/kdb/gomod:/gomod \
  -e GOCACHE=/gocache -e GOMODCACHE=/gomod -e GOFLAGS=-buildvcs=false \
  -e KDB_MIGRATION_TEST_DATABASE_URL='postgres://kdb:p1restore_disposable@127.0.0.1:5432/kdb_platform_migration_test?sslmode=disable' \
  -w /src golang:1.23-bookworm "$@"; }

RC=0
echo "=== build / vet ==="
run bash -c 'go build ./... && echo BUILD_OK; go vet ./... && echo VET_OK' 2>&1 | tail -4 | tee /tmp/reg_bv.txt
grep -q BUILD_OK /tmp/reg_bv.txt || RC=1
grep -q VET_OK   /tmp/reg_bv.txt || RC=1

echo "=== 격리 회귀 ==="
run go test ./internal/... -run Restored -count=1 -p 1 2>&1 \
  | grep -vE 'no test files|no tests to run' > /tmp/reg_iso.txt
grep -E '^(ok|FAIL|---)|\.go:[0-9]+:' /tmp/reg_iso.txt | head -14
grep -q '^FAIL' /tmp/reg_iso.txt && RC=1

if [ "$FAST" != "빠름" ]; then
  echo "=== 전체 ==="
  run go test ./... -count=1 -p 1 2>&1 | grep -vE 'no test files' > /tmp/reg_all.txt
  echo "  ok $(grep -c '^ok' /tmp/reg_all.txt) / FAIL $(grep -c '^FAIL' /tmp/reg_all.txt)"
  grep -E '^(FAIL|--- FAIL)' /tmp/reg_all.txt | head -8
  grep -q '^FAIL' /tmp/reg_all.txt && RC=1
  echo "=== race ==="
  run go test ./internal/... -race -count=1 -p 1 2>&1 | grep -vE 'no test files' > /tmp/reg_race.txt
  echo "  ok $(grep -c '^ok' /tmp/reg_race.txt) / FAIL $(grep -c '^FAIL' /tmp/reg_race.txt)"
  grep -E '^(FAIL|--- FAIL)' /tmp/reg_race.txt | head -8
  grep -q '^FAIL' /tmp/reg_race.txt && RC=1
fi

echo "=== 정적 검사 ==="
# 23번엔 node 가 없다. 컨테이너로 돌린다.
docker run --rm -v "$W":/src:ro -w /src node:20-alpine sh -c '
  for s in validate-kdb-classification validate-kdb-absorption-design validate-kdb-control-design \
           validate-kdb-acceptance-fixtures validate-p1-sql-literals validate-kdb-writer-design \
           validate-kdb-runtime-config; do
    printf "  %-38s " "$s"
    node docs/checks/$s.cjs >/dev/null 2>&1 && echo PASS || echo FAIL
  done' 2>&1 | tail -10 | tee /tmp/reg_static.txt
grep -q FAIL /tmp/reg_static.txt && RC=1

echo "════ 결과: $([ $RC -eq 0 ] && echo '전부 통과' || echo '실패 있음') ════"
exit $RC

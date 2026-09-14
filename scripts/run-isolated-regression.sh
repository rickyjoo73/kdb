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

echo "=== 체크아웃 ==="
if [ -d "$W/.git" ]; then
  cd "$W" && git fetch -q origin main && git reset -q --hard origin/main
else
  git clone -q https://github.com/rickyjoo73/kdb.git "$W" && cd "$W"
fi
WANT="$(git rev-parse origin/main)"; GOT="$(git rev-parse HEAD)"
[ "$WANT" = "$GOT" ] || { echo "!!! SHA 불일치 ($GOT ≠ $WANT) — 옛 코드를 시험할 뻔했다"; exit 1; }
echo "  $(git rev-parse --short HEAD) (원격 main 일치 ✓)"

echo "=== 회귀 DB 재생성 ==="
T0=$(date +%s)
docker exec "$N" psql -qAtX -U kdb -d postgres -c \
  "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname IN ('kdb','kdb_platform_migration_test') AND pid<>pg_backend_pid()" >/dev/null
docker exec "$N" psql -qAtX -U kdb -d postgres -c "DROP DATABASE IF EXISTS kdb_platform_migration_test" >/dev/null 2>&1
# ★STRATEGY = FILE_COPY. PostgreSQL 15+ 의 기본은 WAL_LOG 인데, template 의 **모든
# 페이지를 WAL 에 기록**한다. 작은 template 엔 안전하고 빠르지만 7GB 에선 대가가 크다.
# FILE_COPY 는 파일을 그대로 복사하고 체크포인트 두 번으로 끝낸다. 회귀 DB 는 매번 새로
# 만들고 버리는 것이라 시점 복구 대상이 아니므로 WAL 기록이 아무 쓸모가 없다.
docker exec "$N" psql -qAtX -U kdb -d postgres -c \
  "CREATE DATABASE kdb_platform_migration_test TEMPLATE kdb OWNER kdb STRATEGY = FILE_COPY" >/dev/null \
  || { echo "!!! 회귀 DB 재생성 실패"; exit 1; }
echo "  $(( $(date +%s)-T0 ))초"

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

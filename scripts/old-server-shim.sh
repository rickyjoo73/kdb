#!/usr/bin/env bash
# old-server-shim.sh — 옛 서버(114.203.210.38)의 **중계 컨테이너**를 만든다.
#
# 왜 있나. KDB/TDB 는 aiin23 으로 옮겼지만 소비자 6개(edipresso×2·nbntv·trendbiz·
# mediafine·issuetalk)와 nginx 는 한 줄도 안 고쳤다. 그것들은 여전히 옛 서버의
# 도커 망에서 `kdb-app:9100` · `tdb-api:8080` 을 부른다. 중계가 그 이름과 IP 를
# 물려받아 SSH 터널로 23번에 넘긴다.
#
# ★이 파일이 원장이다. 예전엔 임시 디렉터리의 일회용 스크립트에만 있었고, 그래서
#   2026-09-14 에 **망 하나를 빠뜨린 채 재생성**되어 mediafine 쪽에서 `kdb-app`
#   이름이 안 풀렸다. 아래 NETWORKS 배열이 그 사고의 재발 방지다.
#
# 사용:  bash old-server-shim.sh            # 만들거나 다시 만든다
#        bash old-server-shim.sh --verify   # 안 건드리고 확인만 한다
set -euo pipefail

# ────────────────────────────────────────────────────────────────────
# ★옛 kdb-app 컨테이너가 붙어 있던 망 전부. 하나라도 빠지면 그 망에 **단독으로**
#   있는 소비자에서 이름 해석이 실패한다 — 포트도 터널도 멀쩡한 채로.
#   확인법:  docker inspect <옛 컨테이너> --format '{{range $k,$v := .NetworkSettings.Networks}}{{println $k}}{{end}}'
#
#   dockers_backend   … 소비자 대부분. 고정 IP 172.19.0.240 을 nginx 가 참조한다
#   mediafine_default … mediafine 전용 망. codex-bridge·db·redis 가 여기에만 있다
#
#   (kdb-platform_kdb_internal 과 kdb-platform_dockeraiinplanetcom_backend 에도
#    붙어 있었지만 전자는 kdb-db·kdb-searxng 뿐이고 후자는 비어 있어 소비자가 없다.
#    소비자가 생기면 여기에 추가한다.)
KDB_NETWORKS=(
  "dockers_backend:172.19.0.240"
  "mediafine_default:"
)
KDB_ALIAS=kdb-app

verify() {
  local fail=0
  for spec in "${KDB_NETWORKS[@]}"; do
    local net="${spec%%:*}"
    printf '  %-22s ' "$net"
    if docker run --rm --network "$net" alpine:3 \
         sh -c "getent hosts $KDB_ALIAS >/dev/null && wget -qO- -T 6 http://$KDB_ALIAS:9100/v1/health" 2>/dev/null | grep -q '"ok":true'; then
      echo "OK — $KDB_ALIAS:9100/v1/health 200"
    else
      echo "실패 — 이 망의 소비자는 KDB 를 못 부른다"; fail=1
    fi
  done
  return $fail
}

if [ "${1:-}" = "--verify" ]; then
  echo "== 중계 이름 해석 확인 (소비자와 같은 방식) =="
  verify; exit $?
fi

echo "== 중계 재생성 =="
docker rm -f kdb-shim >/dev/null 2>&1 || true

first="${KDB_NETWORKS[0]}"
net="${first%%:*}"; ip="${first##*:}"
docker run -d --name kdb-shim --restart unless-stopped \
  --network "$net" ${ip:+--ip "$ip"} --network-alias "$KDB_ALIAS" \
  kdb-shim:1 >/dev/null
echo "  기동: $net${ip:+ ($ip)}"

# ★나머지 망은 run 으로 한 번에 못 붙인다(도커는 --network 를 하나만 받는다).
#   반드시 connect 로 이어 붙이고, 별칭을 매번 다시 준다.
for spec in "${KDB_NETWORKS[@]:1}"; do
  net="${spec%%:*}"; ip="${spec##*:}"
  docker network connect --alias "$KDB_ALIAS" ${ip:+--ip "$ip"} "$net" kdb-shim
  echo "  연결: $net${ip:+ ($ip)}"
done

sleep 6
echo "== 확인 =="
verify

#!/bin/bash
# KDB 다국어 상시 드레인 (2026-07-26) — active 엔티티의 ja/zh 빈칸을 MT 음차게이트로
# 채우고, zh_hant 를 OpenCC 로 파생하고, en 빈칸을 구글번역 폴백으로 채운다.
#
# 배경: autopilot stepEnrichEmpty 는 배치 10 × 30분(천장 ~480/일, 실측 ~133/일)이라
# 권위소스 무신호(특히 zh) 엔티티가 매 사이클 재등장만 하며 드레인이 정체된다. en 이
# 92% 커버된 건 과거 `translate-fill` 을 수동 실행했기 때문 — 같은 MT 폴백을 ja/zh 에도
# 상시로 돌리면 된다(오너 07-16/07-21 승인 mechanism). source=gtranslate(prio8,
# machine-translation, empty-only, 상위 공식소스 자동 업그레이드, verified_only 제외).
# 음차게이트로 직역은 버려 "빈칸>틀린값" 유지.
#
# 사용: scripts/kdb-locale-fill.sh [ZH_N] [JA_N] [EN_N]   (기본 250/250/120)
set -u
cd "$(dirname "$0")/.."
ZH_N="${1:-150}"; JA_N="${2:-150}"; EN_N="${3:-80}"
# 중복실행 락 — qwen 게이트가 건당 ~6s 라 배치가 길어질 수 있다. 이전 실행이 아직
# 돌면 이번 회차는 스킵(게이트웨이 경합·이중드레인 방지). 즉시 대량드레인과도 배타.
LOCK=/tmp/kdb-locale-fill.lock
exec 9>"$LOCK"
if ! flock -n 9; then echo "[$(date '+%F %T')] locale-fill: 이전 실행 진행중 — 스킵"; exit 0; fi
log(){ echo "[$(date '+%F %T')] locale-fill: $*"; }
run(){ docker exec kdb-app kdb-app "$@" 2>&1 | grep -iE "done|filled|미설정|중단" | tail -3; }

log "시작 zh=$ZH_N ja=$JA_N en=$EN_N"
[ "$ZH_N" -gt 0 ] && { log "zh 음차드레인"; run mt-translit-fill zh "$ZH_N"; }
[ "$JA_N" -gt 0 ] && { log "ja 음차드레인"; run mt-translit-fill ja "$JA_N"; }
log "zh_hant OpenCC 파생"; run opencc-convert
[ "$EN_N" -gt 0 ] && { log "en 구글번역 폴백"; run translate-fill "$EN_N"; }
log "완료"

#!/bin/bash
# KDB 정체 candidate 타입 재판정 (2026-07-30) — autoverify 가 소비자 타입 힌트를 뉴스 1건으로
# 덮어써 잘못 분류돼 candidate 에 갇힌 엔티티를 claude+codex 패널로 재판정·교정한다.
#
# 원인은 typehint-20260729-6(커밋 a97013f)에서 차단했으나 **이미 갇힌 건은 자동으로 안 풀린다**.
# 실측(2026-07-29): autoverify_type_reassigned 1,190건 중 174건이 candidate 정체 = 서빙 불가.
# 오분류 예: 강화도→character, 서울구치소→channel_outlet, 김병걸→character, 하입비스트→(TXT
# 기사 때문에 오분류), "정말"→(스페인 축구 기사에서 딸려온 일반어).
#
# ★무편향 판정: 현재 저장된(잘못된) 타입은 프롬프트에 주지 않는다 — 틀린 값을 보여주면 모델이
# 거기에 끌려간다. 이름 + 기사 문맥만 준다(verify/type_retrace.go 와 같은 원칙).
#
# 안전(over-reject 금칙): 타입 교정·not_entity 강등 모두 **두 모델 합의**만 적용. 불일치·unsure 는
# 현상 유지. 전건 kwave_kdb_recheck_log(verdict=retype-stuck/drop-stuck)로 revert 가능.
#
# 사용: scripts/kdb-retype-stuck.sh [CAP] [CHUNK]   (기본 CAP=200, CHUNK=10)
#   CHUNK 는 10 을 권장 — codex 는 행 수에 비선형으로 느려진다(핸드오프 40차 §6).
set -u
cd "$(dirname "$0")/.."

# ★판정 엔진 PATH 를 스크립트가 직접 세운다(2026-08-25).
#
# 왜: crontab 의 PATH 는 `/usr/local/bin:/usr/bin:/bin` 인데 `claude` 는
# `~/.local/bin/claude` 에 있다. cron 에서는 이름이 안 풀려 판정이 **조용히 0건**을 내고,
# 아래 병합은 빈 입력으로 성공하고, 스크립트는 exit 0 + "완료"로 끝난다.
# 실해: 2026-08-03~08-25 260회 실행 전부 `claude 0 / codex 0`, 마지막 실판정 08-03,
# 대기 pool 508건. **경보가 안 울린 이유가 이 조합이다** — 실패가 성공처럼 로그된다.
# 환경(cron/대화형)에 의존하지 않도록 스크립트가 자기 PATH 를 책임진다.
export PATH="$HOME/.local/bin:/usr/local/bin:$PATH"

# 엔진 가용성은 **쓰기 전에** 확인한다. claude 가 없으면 이 레인은 할 수 있는 일이 없다 —
# 조용히 0건을 내지 말고 실패로 끝내야 다음 사람이 본다. codex 는 폴백이라 없어도 진행한다.
CLAUDE_BIN="$(command -v claude || true)"
CODEX_BIN="$(command -v codex || true)"
if [ -z "$CLAUDE_BIN" ]; then
  echo "[$(date '+%F %T')] ERROR: claude CLI 를 찾을 수 없다 (PATH=$PATH) — 판정 불가, 종료" >&2
  exit 1
fi
[ -z "$CODEX_BIN" ] && echo "[$(date '+%F %T')] codex 없음 — claude 단독으로 진행"
CAP="${1:-200}"; CHUNK="${2:-10}"
WORK="$(mktemp -d /tmp/kdb-retype.XXXXXX)"
trap 'rm -rf "$WORK"' EXIT
log(){ echo "[$(date '+%F %T')] $*"; }
psql(){ docker exec -i kdb-db psql -U kdb -d kdb "$@"; }

LOCK=/tmp/kdb-retype-stuck.lock
exec 9>"$LOCK"
if ! flock -n 9; then log "이전 실행 진행중 — 스킵"; exit 0; fi

# 좀비 codex 정리 — 타임아웃난 프로세스가 남아 뒤 실행을 막는다(핸드오프 40차 §6-4).
pkill -9 -f "codex exec" 2>/dev/null || true

# 1) 대상 선별 — 타입 재할당 이력이 있고 candidate 로 정체된 엔티티. 이미 재판정한 건 제외.
log "선별 중 (CAP=$CAP)"
psql -tA -F$'\t' -c "
SELECT e.id::text, e.canonical_ko, e.entity_type::text,
       regexp_replace(COALESCE(left(q.context_hint,150),''),E'[\n\t]',' ','g')
FROM kwave_entities e
JOIN kwave_entity_research_queue q ON q.entity_ko = e.canonical_ko
WHERE e.status='candidate' AND e.operator_locked=false
  AND (
    'autoverify_type_reassigned' = ANY(q.precheck_flags)
    -- ★2026-07-31 추가: term/unknown 으로 굳어 승급 자체가 불가능한 정체건도 재판정 대상에 넣는다.
    -- promoteCandidateWithOfficialAnchor 는 term/unknown 을 무조건 거부하므로(source_policy 계약)
    -- 이들은 어떤 레인에서도 active 가 될 수 없다. ondemand 레인은 그 사실을 모른 채 8초마다
    -- 재처리해 영구 공회전했고(spinfix-20260731-1 에서 셀렉트에서 제외), 그 결과 담당 레인이
    -- 없어졌다. 타입을 고쳐야 풀리는 문제이므로 재판정 패널이 정확한 주인이다.
    -- 소비자 수요가 확인된(pass/approved) 건만 — 임의 일반어까지 패널에 태우지 않는다.
    OR (e.entity_type::text IN ('term','unknown')
        AND q.precheck_status IN ('pass','approved'))
  )
  AND e.notes NOT LIKE '%[retype-stuck:%'
  AND e.notes NOT LIKE '%[cand-evidence:recleared-%'
GROUP BY e.id, e.canonical_ko, e.entity_type, q.context_hint
LIMIT ${CAP}" > "$WORK/pool.tsv"
N=$(wc -l < "$WORK/pool.tsv")
log "대상 ${N}건"
[ "$N" -eq 0 ] && { log "대상 없음 — 종료"; exit 0; }

# 2) 판정 입력(번호·표기·문맥) — 현재 타입은 의도적으로 제외
awk -F'\t' '{print NR"\t"$2"\t"$4}' "$WORK/pool.tsv" > "$WORK/q.tsv"
split -l "$CHUNK" -d "$WORK/q.tsv" "$WORK/chunk_"

# 3) 판정: claude 는 큰 청크도 견디므로 60행씩, codex 는 CHUNK 행씩(비선형 지연)
SYS="$(cat scripts/retype_system.md)"
split -l 60 -d "$WORK/q.tsv" "$WORK/clchunk_"
for cf in "$WORK"/clchunk_*; do
  f=$(basename "$cf" | sed 's/clchunk_//')
  ( timeout 550 claude -p "$(cat "$cf")" --append-system-prompt "$SYS" --model claude-opus-4-8 2>/dev/null \
      | grep '^{"n"' > "$WORK/claude_$f.jsonl" ) &
done
wait
log "claude 판정: $(cat "$WORK"/claude_*.jsonl 2>/dev/null | grep -c '{')"

# codex — stdin 닫기 + stderr 파이프 + grep 앵커링(핸드오프 40차 §6). 순차 실행(경합 방지).
for cf in "$WORK"/chunk_*; do
  f=$(basename "$cf" | sed 's/chunk_//')
  { echo "[판정 규율 — 반드시 준수]"; echo "$SYS"; echo; echo "[작업] 아래 TSV 각 행을 판정하라. 입력 행수 = 출력 JSONL 행수."; cat "$cf"; } > "$WORK/cx_$f.txt"
  timeout 300 codex exec --skip-git-repo-check "$(cat "$WORK/cx_$f.txt")" < /dev/null 2>&1 \
      | grep '^{"n"' > "$WORK/codex_$f.jsonl"
done
log "codex 판정: $(cat "$WORK"/codex_*.jsonl 2>/dev/null | grep -c '{')"

# 4) 병합 + SQL 생성(두 모델 합의만 적용)
SUMMARY=$(python3 scripts/retype_apply.py "$WORK")
log "병합: $SUMMARY"

# 5) 반영(트랜잭션)
psql -v ON_ERROR_STOP=1 < "$WORK/apply.sql" > /dev/null 2>&1 && log "apply COMMIT" || { log "apply 실패"; cp "$WORK/apply.sql" /tmp/kdb-retype-failed.sql; exit 1; }
log "완료. 잔여 정체: $(psql -tAc "
SELECT count(*) FROM kwave_entities e
 JOIN kwave_entity_research_queue q ON q.entity_ko=e.canonical_ko
 WHERE e.status='candidate' AND 'autoverify_type_reassigned'=ANY(q.precheck_flags)
   AND e.notes NOT LIKE '%[retype-stuck:%' AND e.notes NOT LIKE '%[cand-evidence:recleared-%'")"

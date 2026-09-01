#!/bin/bash
# KDB tier-3 상시 판정 레인 (2026-07-25) — 정체된 고유명사를 프런티어 지식모델(claude+codex
# 패널)로 판정해 발굴 파이프라인에 즉시 투입/종결한다. autoverify(일 600 Naver콜)가 못
# 따라잡는 aged/exhausted review 백로그 전용. cron 주기 실행.
#
# 병합: 두 모델 합의→적용, 불일치→keep (over-reject 금칙). codex 실패시 claude 단독 폴백.
# 안전: 판정 전 pool 스냅샷 불필요(변경은 queue 상태뿐, adjudication_log 로 전건 추적·revert).
#
# 사용: scripts/kdb-adjudicate.sh [CAP] [CHUNK]   (기본 CAP=150, CHUNK=50)
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
CAP="${1:-150}"; CHUNK="${2:-50}"
COOLDOWN_DAYS=30
WORK="$(mktemp -d /tmp/kdb-adj.XXXXXX)"
trap 'rm -rf "$WORK"' EXIT
log(){ echo "[$(date '+%F %T')] $*"; }
psql(){ docker exec -i kdb-db psql -U kdb -d kdb "$@"; }

# 0) 게이트웨이/모델 확인 — 다운이면 스킵(교체중 등)
GK=$(grep -E '^KDB_GEMMA_API_KEY=' .env | cut -d= -f2)
# (게이트웨이는 발굴 enrich 용 — 판정은 claude/codex CLI 라 무관하지만, approve 가 흘러갈
#  파이프라인이 살아있는지 확인. 다운이면 판정만 하고 approve 는 다음 회차로.)

# 1) 대상 선별: review 정체(aged 3d+ OR autoverify_exhausted), 최근 판정 이력 없음, CAP.
log "선별 중 (CAP=$CAP, cooldown=${COOLDOWN_DAYS}d)"
psql -tA -F$'\t' -c "
SELECT q.id, q.entity_ko, q.requested_entity_type::text,
  regexp_replace(COALESCE(q.context_hint,''),E'[\n\t]',' ','g')
FROM kwave_entity_research_queue q
WHERE q.precheck_status='review' AND q.status='done'
  AND NOT ('adjudicated_panel' = ANY(q.precheck_flags))
  AND (q.last_requested_at < now()-interval '3 days'
       OR 'autoverify_exhausted' = ANY(q.precheck_flags))
  AND NOT EXISTS (SELECT 1 FROM kwave_kdb_adjudication_log l
       WHERE l.queue_id=q.id AND l.created_at > now()-interval '${COOLDOWN_DAYS} days')
ORDER BY q.last_requested_at DESC
LIMIT ${CAP}" > "$WORK/pool.tsv"
N=$(wc -l < "$WORK/pool.tsv")
log "대상 ${N}건"
[ "$N" -eq 0 ] && { log "대상 없음 — 종료"; exit 0; }

# 2) 청킹
split -l "$CHUNK" -d "$WORK/pool.tsv" "$WORK/chunk_"

# 3) 판정 엔진: 청크마다 claude -p + codex exec 병렬.
#    ★시스템/사용자 프롬프트 분리(2026-07-25): 판정 규율(role·절대규칙·오거부 금칙·
#    K-scope/non-entity 필터·출력 규율)은 시스템 프롬프트로, TSV 행만 사용자 입력으로.
#    claude=--append-system-prompt, codex=프롬프트 상단 [판정 규율] 블록(exec 는 시스템 슬롯
#    별도 없음 → 지시 블록으로 동등 적용).
SYS="$(cat scripts/adjudicate_system.md)"
USER_HDR="아래 TSV 각 행을 시스템 규율대로 판정하라. 입력 행수와 출력 JSONL 행수를 일치시켜라."
for cf in "$WORK"/chunk_*; do
  f=$(basename "$cf" | sed 's/chunk_//')
  { echo "$USER_HDR"; echo; cat "$cf"; } > "$WORK/user_$f.txt"
  # claude: 시스템은 --append-system-prompt, 사용자는 TSV 행만
  ( claude -p "$(cat "$WORK/user_$f.txt")" --append-system-prompt "$SYS" --model claude-opus-4-8 2>/dev/null \
      | grep '{"id"' > "$WORK/claude_$f.jsonl" ) &
  # codex: exec 는 시스템 슬롯이 없어 지시 블록으로 동등 적용
  { echo "[판정 규율 — 반드시 준수]"; echo "$SYS"; echo; echo "[작업]"; cat "$WORK/user_$f.txt"; } > "$WORK/cxprompt_$f.txt"
  # ★codex 실행 규약(2026-07-29 실측) — recheck 레인과 동일: stdin 을 닫고(`< /dev/null`)
  # stderr 를 파이프로 보내야(`2>&1`) 블로킹 타임아웃이 안 난다. 대신 프롬프트가 stdout 에
  # 에코되므로 grep 을 줄 시작(^)에 고정한다.
  ( timeout 600 codex exec --skip-git-repo-check "$(cat "$WORK/cxprompt_$f.txt")" < /dev/null 2>&1 \
      | grep '^{"id"' > "$WORK/codex_$f.jsonl" ) &
done
wait
NC=$(cat "$WORK"/claude_*.jsonl 2>/dev/null | grep -c '{')
NX=$(cat "$WORK"/codex_*.jsonl 2>/dev/null | grep -c '{')
log "판정 완료: claude $NC / codex $NX"
# ★대상이 있는데 판정이 0건이면 그건 "할 일이 없었다"가 아니라 **엔진이 답을 못 준 것**이다.
# 종전에는 이 상태로 빈 병합을 성공시키고 "완료"를 찍어, 22일간 죽은 레인이 정상으로 보였다.
# 여기서 끊어야 로그와 종료코드가 사실을 말한다(cron 이 실패를 알린다).
if [ "$NC" -eq 0 ] && [ "$NX" -eq 0 ]; then
  log "ERROR: 대상 ${N}건인데 판정 0건 — 엔진 무응답으로 보고 중단(반영 없음)"
  exit 1
fi

# 4) 위치기반 id 복구(에이전트 오전사 대비) — chunk 순서 = out 순서
python3 - "$WORK" <<'PY'
import json,glob,os,sys
W=sys.argv[1]
def norm(s): return "".join((s or "").split()).lower()
for eng in ("claude","codex"):
    for cf in sorted(glob.glob(f"{W}/chunk_*")):
        f=os.path.basename(cf).split("_")[1]
        op=f"{W}/{eng}_{f}.jsonl"
        if not os.path.exists(op): continue
        chunk=[l.rstrip("\n").split("\t") for l in open(cf,encoding="utf-8") if l.strip()]
        outs=[l.strip().strip("`") for l in open(op,encoding="utf-8") if l.strip().startswith("{")]
        if len(chunk)!=len(outs): continue  # 행수 불일치 → 복구 불가, id 매칭에 맡김
        fixed=[]
        for cr,ol in zip(chunk,outs):
            try: v=json.loads(ol)
            except: v={"verdict":"keep"}
            v["id"]=cr[0]; v["ko"]=cr[1]
            fixed.append(json.dumps(v,ensure_ascii=False))
        open(op,"w",encoding="utf-8").write("\n".join(fixed)+"\n")
PY

# 5) 병합 + SQL 생성
SUMMARY=$(python3 scripts/adjudicate_apply.py "$WORK")
log "병합: $SUMMARY"

# 6) 반영(트랜잭션) + approve dedup 승격
psql -v ON_ERROR_STOP=1 < "$WORK/apply.sql" > /dev/null 2>&1 && log "apply COMMIT" || { log "apply 실패"; exit 1; }
# approve 중 (정규화키,타입) 그룹당 pending 없는 것 하나만 발굴 pending 승격(유니크제약 안전)
psql -v ON_ERROR_STOP=1 -c "
WITH a AS (SELECT id,intake_normalized_key nk,requested_entity_type rt,last_requested_at
             FROM kwave_entity_research_queue
            WHERE precheck_status='approved' AND status='done' AND 'adjudicated_panel'=ANY(precheck_flags)
              AND finished_at > now()-interval '1 hour'),
     b AS (SELECT DISTINCT intake_normalized_key nk,requested_entity_type rt
             FROM kwave_entity_research_queue WHERE status IN ('pending','in_progress')),
     pick AS (SELECT DISTINCT ON (a.nk,a.rt) a.id FROM a LEFT JOIN b ON b.nk=a.nk AND b.rt=a.rt
               WHERE b.nk IS NULL ORDER BY a.nk,a.rt,a.last_requested_at DESC)
UPDATE kwave_entity_research_queue q SET status='pending',finished_at=NULL,next_attempt_at=NULL,last_outcome=''
 WHERE q.id IN (SELECT id FROM pick);" > /dev/null 2>&1 && log "approve 발굴승격 완료" || log "approve 승격 스킵"

log "완료. 발굴대기: $(psql -tAc "SELECT count(*) FROM kwave_entity_research_queue WHERE status='pending' AND 'adjudicated_panel'=ANY(precheck_flags)")"

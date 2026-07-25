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

# 3) 판정 엔진: 청크마다 claude -p + codex exec 병렬
RUBRIC="scripts/adjudicate_rubric.md"
for cf in "$WORK"/chunk_*; do
  f=$(basename "$cf" | sed 's/chunk_//')
  { echo "$(cat "$RUBRIC")"; echo; echo "=== 아래 TSV 전 행 판정, JSONL만 출력(행수 일치) ==="; cat "$cf"; } > "$WORK/prompt_$f.txt"
  # claude (headless)
  ( claude -p "$(cat "$WORK/prompt_$f.txt")" --model claude-opus-4-8 2>/dev/null \
      | grep '{"id"' > "$WORK/claude_$f.jsonl" ) &
  # codex (설정 기본 gpt-5.6-sol)
  ( timeout 600 codex exec --skip-git-repo-check "$(cat "$WORK/prompt_$f.txt")" 2>/dev/null \
      | grep '{"id"' > "$WORK/codex_$f.jsonl" ) &
done
wait
log "판정 완료: claude $(cat "$WORK"/claude_*.jsonl 2>/dev/null | grep -c '{') / codex $(cat "$WORK"/codex_*.jsonl 2>/dev/null | grep -c '{')"

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

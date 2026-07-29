#!/bin/bash
# KDB active 재심 레인 (2026-07-29) — 이미 서빙 중인 active 중 "일반어/비-K 의심" 플래그를
# 달았거나 승급 근거가 무효로 밝혀진 항목을 claude+codex 패널로 재심한다.
#
# 배경: Wikidata 의 한국어 인명 요소 항목("만원"=Korean male given name 등)이 승급 앵커로
# 오인돼 일반명사가 active 로 유입됐다(실측 88건). 유입 차단은 코드로 했고(namegate-20260729-3),
# 이 레인은 **이미 들어온 것**을 판정한다.
#
# 안전(over-drop 금칙): drop 은 두 모델 합의일 때만, 그것도 status='candidate' 강등(삭제 아님).
# 단일 모델 판정으로는 서빙 값을 내리지 않는다. 전건 kwave_kdb_recheck_log 로 revert 가능.
#
# 사용: scripts/kdb-recheck-active.sh [CAP] [CHUNK]   (기본 CAP=120, CHUNK=40)
set -u
cd "$(dirname "$0")/.."
CAP="${1:-120}"; CHUNK="${2:-40}"
WORK="$(mktemp -d /tmp/kdb-recheck.XXXXXX)"
trap 'rm -rf "$WORK"' EXIT
log(){ echo "[$(date '+%F %T')] $*"; }
psql(){ docker exec -i kdb-db psql -U kdb -d kdb "$@"; }

LOCK=/tmp/kdb-recheck-active.lock
exec 9>"$LOCK"
if ! flock -n 9; then log "이전 실행 진행중 — 스킵"; exit 0; fi

# 1) 대상 선별 — 위험 순서: ①무효앵커 강등분 ②일반어 플래그 + 근거전무 ③unverified + 근거전무.
#    이미 재심한 것([recheck:...])은 제외.
log "선별 중 (CAP=$CAP)"
psql -tA -F$'\t' -c "
SELECT e.id::text, e.canonical_ko, e.entity_type::text,
       CASE WHEN e.notes LIKE '%[nameanchor:revoked%' THEN 'anchor-revoked'
            WHEN e.notes LIKE '%일반어%' OR e.notes LIKE '%일반 명사%' OR e.notes LIKE '%common-noun%' THEN 'general-word-flag'
            ELSE 'unverified-no-evidence' END
FROM kwave_entities e
WHERE e.status='active' AND e.operator_locked=false
  AND e.notes NOT LIKE '%[recheck:%'
  AND (
    e.notes LIKE '%[nameanchor:revoked%'
    OR ( (e.notes LIKE '%일반어%' OR e.notes LIKE '%일반 명사%' OR e.notes LIKE '%common-noun%')
         AND COALESCE(array_length(e.source_urls,1),0)=0
         AND NOT EXISTS (SELECT 1 FROM kwave_entity_external_refs r WHERE r.entity_id=e.id) )
    OR ( e.verification_tier='unverified' AND COALESCE(array_length(e.source_urls,1),0)=0
         AND NOT EXISTS (SELECT 1 FROM kwave_entity_external_refs r WHERE r.entity_id=e.id) )
  )
ORDER BY (e.notes LIKE '%[nameanchor:revoked%') DESC, e.updated_at DESC
LIMIT ${CAP}" > "$WORK/pool.tsv"
N=$(wc -l < "$WORK/pool.tsv")
log "대상 ${N}건"
[ "$N" -eq 0 ] && { log "대상 없음 — 종료"; exit 0; }

# 2) 청킹
split -l "$CHUNK" -d "$WORK/pool.tsv" "$WORK/chunk_"

# 3) 판정: 청크마다 claude -p + codex exec 병렬(adjudicate 레인과 동일 패턴)
SYS="$(cat scripts/recheck_system.md)"
USER_HDR="아래 TSV 각 행(엔티티ID·표기·현재타입·플래그사유)을 시스템 규율대로 재심하라. 입력 행수와 출력 JSONL 행수를 일치시켜라."
for cf in "$WORK"/chunk_*; do
  f=$(basename "$cf" | sed 's/chunk_//')
  { echo "$USER_HDR"; echo; cat "$cf"; } > "$WORK/user_$f.txt"
  ( claude -p "$(cat "$WORK/user_$f.txt")" --append-system-prompt "$SYS" --model claude-opus-4-8 2>/dev/null \
      | grep '{"id"' > "$WORK/claude_$f.jsonl" ) &
  { echo "[판정 규율 — 반드시 준수]"; echo "$SYS"; echo; echo "[작업]"; cat "$WORK/user_$f.txt"; } > "$WORK/cxprompt_$f.txt"
  # ★codex 실행 규약(2026-07-29 실측): ①`< /dev/null` 로 stdin 을 닫아야 한다 — stdin 이
  # 열린 채 비어 있으면(백그라운드 실행 등) codex 가 응답 없이 블로킹돼 타임아웃난다.
  # ②stderr 를 /dev/null 로 버리면 같은 증상이 나므로 `2>&1` 로 파이프에 보낸다.
  # ③그 대가로 프롬프트가 stdout 에 에코되므로 grep 은 줄 시작(^)에 고정해 스키마 예시
  # 같은 프롬프트 문자열이 결과로 섞이는 것을 막는다.
  ( timeout 600 codex exec --skip-git-repo-check "$(cat "$WORK/cxprompt_$f.txt")" < /dev/null 2>&1 \
      | grep '^{"id"' > "$WORK/codex_$f.jsonl" ) &
done
wait
log "판정 완료: claude $(cat "$WORK"/claude_*.jsonl 2>/dev/null | grep -c '{') / codex $(cat "$WORK"/codex_*.jsonl 2>/dev/null | grep -c '{')"

# 4) 위치기반 id 복구(에이전트 오전사 대비) — chunk 순서 = out 순서
python3 - "$WORK" <<'PY'
import json,glob,os,sys
W=sys.argv[1]
for eng in ("claude","codex"):
    for cf in sorted(glob.glob(f"{W}/chunk_*")):
        f=os.path.basename(cf).split("_")[1]
        op=f"{W}/{eng}_{f}.jsonl"
        if not os.path.exists(op): continue
        chunk=[l.rstrip("\n").split("\t") for l in open(cf,encoding="utf-8") if l.strip()]
        outs=[l.strip().strip("`") for l in open(op,encoding="utf-8") if l.strip().startswith("{")]
        if len(chunk)!=len(outs): continue
        fixed=[]
        for cr,ol in zip(chunk,outs):
            try: v=json.loads(ol)
            except: v={"verdict":"keep"}
            v["id"]=cr[0]; v["ko"]=cr[1]
            fixed.append(json.dumps(v,ensure_ascii=False))
        open(op,"w",encoding="utf-8").write("\n".join(fixed)+"\n")
PY

# 5) 병합 + SQL 생성
SUMMARY=$(python3 scripts/recheck_apply.py "$WORK")
log "병합: $SUMMARY"

# 6) 반영(트랜잭션)
psql -v ON_ERROR_STOP=1 < "$WORK/apply.sql" > /dev/null 2>&1 && log "apply COMMIT" || { log "apply 실패"; exit 1; }
log "완료. 잔여 재심대상: $(psql -tAc "SELECT count(*) FROM kwave_entities WHERE status='active' AND notes LIKE '%[nameanchor:revoked%' AND notes NOT LIKE '%[recheck:%'")"

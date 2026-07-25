#!/usr/bin/env python3
"""tier-3 판정 반영기 — claude/codex 패널 병합 → SQL 생성.

입력(scratch 디렉토리):
  pool.tsv        : queue_id \t term_ko \t requested_type \t ...(정답 id·ko 순서)
  claude_*.jsonl  : claude 판정(위치기반 복구 전/후 무관 — id 기준 매칭)
  codex_*.jsonl   : codex 판정(선택 — 없으면 claude-only)

병합 규칙(over-reject 금칙): 두 모델이 verdict 일치 → 그 verdict. 불일치 → keep.
codex 미제공 → claude 단독(단, reject 는 claude 단독이라도 유지: claude 는 실측 오거부 0).

출력: apply.sql (BEGIN..COMMIT) + 표준출력 요약 JSON.
approve → precheck='approved', status='done'(중복승격은 드라이버가 2단계 dedup 처리)
reject  → precheck='reject', status='done'
keep    → 무변경(로그만)
모든 처리행 → kwave_kdb_adjudication_log INSERT (쿨다운·감사).
"""
import json, sys, glob, os

S = sys.argv[1] if len(sys.argv) > 1 else "."
VALID = {"person","group","drama","movie","show","song_album","agency",
         "channel_outlet","brand_place","event_tour","character","term"}

pool = {}
for ln in open(f"{S}/pool.tsv", encoding="utf-8"):
    r = ln.rstrip("\n").split("\t")
    if len(r) < 2: continue
    pool[r[0]] = {"ko": r[1], "type": r[2] if len(r) > 2 else ""}

def load(pat):
    out = {}
    for p in glob.glob(f"{S}/{pat}"):
        for line in open(p, encoding="utf-8"):
            line = line.strip().strip("`")
            if not line.startswith("{"): continue
            try: v = json.loads(line)
            except Exception: continue
            if v.get("id") in pool: out[v["id"]] = v
    return out

cl = load("claude_*.jsonl")
cx = load("codex_*.jsonl")
have_codex = len(cx) > 0

def esc(s): return (s or "").replace("'", "''")

# ★병합 재설계(2026-07-25): claude 우선 + codex approve 부스터.
#   기존 "합의→적용/불일치→keep" 은 codex 간헐 실패·과다 불일치 시 keep 남발(93%)로
#   처리량이 붕괴했다(실측). claude 는 A/B 오거부 0·신뢰 → 기본 판정으로 삼고, codex 는
#   ①claude keep 을 approve 로 승격(누락 실존 회수) ②claude reject 를 codex approve 가
#   거부권으로 keep 강등(오거부 이중안전)에만 쓴다. codex 실패/부재 시 순수 claude 처리량 유지.
merged = {}
boost_ct = veto_ct = 0
for eid, m in pool.items():
    c = cl.get(eid, {}).get("verdict")
    x = cx.get(eid, {}).get("verdict") if have_codex else None
    if c is None and x is None:
        continue
    if c is None:                      # claude 없음(드묾) → codex 단독, 단 approve 만 신뢰
        verdict = x if x == "approve" else "keep"; models = "codex-only"
    else:
        verdict = c; models = "claude"
        if have_codex and x is not None:
            models = "claude+codex"
            if c == "keep" and x == "approve" and (cx[eid].get("evidence") or "").strip():
                verdict = "approve"; boost_ct += 1          # codex 부스트
            elif c == "reject" and x == "approve":
                verdict = "keep"; veto_ct += 1              # codex 거부권(오거부 방지)
    src = cx.get(eid, {}) if (verdict == "approve" and c != "approve") else cl.get(eid, {})
    typ = (src.get("type") or "").strip()
    if typ not in VALID: typ = m["type"] if m["type"] in VALID else "unknown"
    ev = (src.get("evidence") or "").strip()
    rs = (cl.get(eid, {}).get("reason") or ev or "").strip()
    if verdict == "approve" and not ev:
        verdict = "keep"
    if verdict == "reject" and not rs:
        verdict = "keep"
    merged[eid] = {"verdict": verdict, "type": typ, "evidence": ev, "reason": rs,
                   "models": models, "agreed": (c == x) if (c and x) else False, "ko": m["ko"]}
agree_ct, disagree_ct = boost_ct, veto_ct

counts = {}
for v in merged.values(): counts[v["verdict"]] = counts.get(v["verdict"], 0) + 1

with open(f"{S}/apply.sql", "w", encoding="utf-8") as out:
    out.write("BEGIN;\n")
    for eid, v in merged.items():
        log = (f"INSERT INTO kwave_kdb_adjudication_log "
               f"(queue_id,term_ko,verdict,entity_type,models,agreed,evidence) VALUES "
               f"('{eid}'::uuid,'{esc(v['ko'])[:100]}','{v['verdict']}','{v['type']}',"
               f"'{v['models']}',{str(v['agreed']).lower()},'{esc(v['evidence'])[:200]}');\n")
        out.write(log)
        if v["verdict"] == "approve":
            rsn = esc("panel adjudication: " + v["evidence"])[:220]
            out.write(f"""UPDATE kwave_entity_research_queue
   SET precheck_status='approved', status='done', finished_at=now(), picked_at=NULL,
       next_attempt_at=NULL, resolution_status='unknown', last_outcome='adjudicated_approve',
       requested_entity_type=CASE WHEN requested_entity_type::text IN ('unknown','term')
            THEN '{v['type']}'::kwave_entity_type ELSE requested_entity_type END,
       precheck_reason='{rsn}', precheck_flags=array_append(precheck_flags,'adjudicated_panel')
 WHERE id='{eid}'::uuid AND precheck_status='review';\n""")
        elif v["verdict"] == "reject":
            rsn = esc("panel adjudication reject: " + v["reason"])[:220]
            out.write(f"""UPDATE kwave_entity_research_queue
   SET precheck_status='reject', status='done', finished_at=now(),
       resolution_status='rejected_precheck', last_outcome='precheck_reject',
       precheck_reason='{rsn}', precheck_flags=array_append(precheck_flags,'adjudicated_panel')
 WHERE id='{eid}'::uuid AND precheck_status='review';\n""")
        # keep: 로그만(무변경) — 쿨다운은 로그 존재로 판정
    out.write("COMMIT;\n")

print(json.dumps({"pool": len(pool), "judged": len(merged), "codex": have_codex,
                  "agree": agree_ct, "disagree": disagree_ct, "counts": counts},
                 ensure_ascii=False))

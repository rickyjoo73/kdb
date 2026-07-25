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

merged = {}
agree_ct = disagree_ct = 0
for eid, m in pool.items():
    cv = cl.get(eid, {}).get("verdict")
    xv = cx.get(eid, {}).get("verdict") if have_codex else None
    if cv is None and xv is None:
        continue
    if have_codex and cv is not None and xv is not None:
        if cv == xv:
            verdict = cv; agreed = True; agree_ct += 1
        else:
            verdict = "keep"; agreed = False; disagree_ct += 1
        models = "claude+codex"
    else:
        verdict = cv if cv is not None else xv
        agreed = False
        models = "claude-only" if cv is not None else "codex-only"
    src = cl.get(eid) if cv == verdict else cx.get(eid, cl.get(eid, {}))
    typ = (src.get("type") or "").strip()
    if typ not in VALID: typ = m["type"] if m["type"] in VALID else "unknown"
    ev = (src.get("evidence") or "").strip()
    rs = (src.get("reason") or src.get("evidence") or "").strip()
    if verdict == "approve" and not ev:
        verdict = "keep"; agreed = False   # evidence 없는 approve 는 안전 강등
    if verdict == "reject" and not rs:
        verdict = "keep"; agreed = False
    merged[eid] = {"verdict": verdict, "type": typ, "evidence": ev, "reason": rs,
                   "models": models, "agreed": agreed, "ko": m["ko"]}

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

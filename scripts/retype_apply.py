#!/usr/bin/env python3
"""retype_apply.py — 정체 candidate 의 타입 재판정(claude+codex)을 병합해 apply.sql 을 만든다.

배경: autoverify 가 소비자 타입 힌트를 뉴스 1건으로 덮어써(2026-07-29 typehint-20260729-6 에서
차단) 잘못 분류된 엔티티가 candidate 에 갇혔다. 신규 유입은 막혔지만 기존 건은 자동으로 안 풀린다.

병합 규율(over-reject 금칙):
  - 타입 교정은 **두 모델이 같은 타입**을 낼 때만. 불일치/unsure → 보류(현상 유지).
  - not_entity 는 **두 모델 합의**일 때만 rejected 강등. 단독 판정으로는 내리지 않는다.
  - 교정 시 [cand-evidence:review] 플래그를 걷어 다음 스위프가 재평가하게 한다(정체 해소).
  - homonym 유니크(ko, type, disambig) 충돌 시 스킵 — NOT EXISTS 가드.
전건 kwave_kdb_recheck_log 에 적재(verdict=retype-stuck/drop-stuck)해 revert 가능.
"""
import json, glob, os, sys

W = sys.argv[1]
VALID = {"person","group","drama","movie","show","song_album","agency",
         "channel_outlet","brand_place","event_tour","character"}

def load(eng):
    out = {}
    for p in glob.glob(f"{W}/{eng}_*.jsonl"):
        for line in open(p, encoding="utf-8"):
            line = line.strip().strip("`")
            if not line.startswith("{"):
                continue
            try: v = json.loads(line)
            except Exception: continue
            if "n" in v:
                out[int(v["n"])] = v
    return out

def q(s):
    return "'" + str(s or "").replace("'", "''") + "'"

rows = {}
for l in open(f"{W}/pool.tsv", encoding="utf-8"):
    p = l.rstrip("\n").split("\t")
    if len(p) >= 3:
        rows[len(rows)+1] = {"id": p[0], "ko": p[1], "cur": p[2]}

cl, cx = load("claude"), load("codex")
counts = {"retype":0, "drop":0, "hold":0, "already":0}
sql = ["BEGIN;"]

for n, r in rows.items():
    a, b = cl.get(n), cx.get(n)
    if not (a and b):
        counts["hold"] += 1; continue
    ta, tb = (a.get("type") or "").strip(), (b.get("type") or "").strip()
    if ta != tb:
        counts["hold"] += 1; continue          # 불일치 → 보류
    t = ta
    if t == "unsure":
        counts["hold"] += 1; continue
    ev = ((a.get("why") or "") + " | " + (b.get("why") or ""))[:400]
    if t == "not_entity":
        counts["drop"] += 1
        sql.append("INSERT INTO kwave_kdb_recheck_log (entity_id,term_ko,verdict,new_type,models,agreed,evidence) "
                   f"VALUES ({q(r['id'])}::uuid,{q(r['ko'])},'drop-stuck','','claude+codex',true,{q(ev)});")
        sql.append("UPDATE kwave_entities SET status='rejected', "
                   "notes=COALESCE(notes,'')||' [retype-stuck:not_entity 2026-07-30 panel]', updated_at=now() "
                   f"WHERE id={q(r['id'])}::uuid AND status='candidate';")
        continue
    if t not in VALID:
        counts["hold"] += 1; continue
    if t == r["cur"]:
        counts["already"] += 1
        # 타입은 맞았으나 정체 — review 플래그만 걷어 재평가 유도
        sql.append("UPDATE kwave_entities SET notes=replace(COALESCE(notes,''),'[cand-evidence:review]','[cand-evidence:recleared-20260730]'), "
                   f"updated_at=now() WHERE id={q(r['id'])}::uuid AND status='candidate';")
        continue
    counts["retype"] += 1
    sql.append("INSERT INTO kwave_kdb_recheck_log (entity_id,term_ko,verdict,new_type,models,agreed,evidence) "
               f"VALUES ({q(r['id'])}::uuid,{q(r['ko'])},'retype-stuck',{q(t)},'claude+codex',true,{q(ev)});")
    sql.append(f"UPDATE kwave_entities e SET entity_type={q(t)}::kwave_entity_type, "
               "notes=replace(COALESCE(e.notes,''),'[cand-evidence:review]','[cand-evidence:recleared-20260730]')"
               f"||' [retype-stuck:{t} 2026-07-30 panel]', updated_at=now() "
               f"WHERE e.id={q(r['id'])}::uuid AND e.status='candidate' AND NOT EXISTS ("
               "SELECT 1 FROM kwave_entities x WHERE x.canonical_ko=e.canonical_ko "
               f"AND x.entity_type={q(t)}::kwave_entity_type AND COALESCE(x.disambig,'')=COALESCE(e.disambig,'') AND x.id<>e.id);")
    # 큐 row 의 requested_entity_type 은 건드리지 않는다 — (정규화키, type) 유니크 제약이 있어
    # 같은 키에 이미 그 타입 row 가 있으면 충돌한다. 엔티티 타입만 바로잡으면 다음 스위프가
    # 엔티티 기준으로 재평가하므로 정체 해소에는 충분하다.

sql.append("COMMIT;")
open(f"{W}/apply.sql", "w", encoding="utf-8").write("\n".join(sql) + "\n")
print(json.dumps({"pool": len(rows), "claude": len(cl), "codex": len(cx), "counts": counts}, ensure_ascii=False))

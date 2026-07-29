#!/usr/bin/env python3
"""recheck_apply.py — active 재심 판정(claude+codex)을 병합해 apply.sql 을 만든다.

병합 규율(over-drop 금칙):
  - drop 은 **두 모델 합의**일 때만 적용한다. 한쪽만 drop → keep(안전).
  - retype 은 합의 시 적용, 불일치 시 keep.
  - claude 단독 실행(codex 실패)일 때는 drop 을 적용하지 않는다 — 서빙 중인 값을 단일 판정으로
    내리지 않는다. retype 만 단독 적용 허용(값 손실이 없고 되돌리기 쉬움).

drop 반영 = status를 'candidate'로 강등(서빙에서 빠지되 삭제 아님) + 사유 기록. 전건
kwave_kdb_recheck_log 에 적재해 revert 가능.
"""
import json
import glob
import os
import sys

W = sys.argv[1]


def load(eng):
    out = {}
    for p in glob.glob(f"{W}/{eng}_*.jsonl"):
        for line in open(p, encoding="utf-8"):
            line = line.strip().strip("`")
            if not line.startswith("{"):
                continue
            try:
                v = json.loads(line)
            except Exception:
                continue
            if v.get("id"):
                out[v["id"]] = v
    return out


def q(s):
    return "'" + str(s or "").replace("'", "''") + "'"


cl, cx = load("claude"), load("codex")
has_codex = len(cx) > 0
pool = {}
for r in open(f"{W}/pool.tsv", encoding="utf-8"):
    parts = r.rstrip("\n").split("\t")
    if len(parts) >= 3:
        pool[parts[0]] = parts

counts = {"keep": 0, "retype": 0, "drop": 0}
agree = disagree = 0
sql = ["BEGIN;"]

for eid, row in pool.items():
    a, b = cl.get(eid), cx.get(eid)
    va = (a or {}).get("verdict", "keep")
    vb = (b or {}).get("verdict", "keep")
    primary = a or b or {}
    verdict = "keep"

    if a and b:
        if va == vb:
            agree += 1
            verdict = va
            if va == "retype":
                # 타입까지 같아야 적용
                if (a.get("type") or "") != (b.get("type") or ""):
                    verdict = "keep"
        else:
            disagree += 1
            # 불일치: drop 은 절대 적용 안 함. 한쪽이 retype 이고 다른 쪽이 keep 이면 keep.
            verdict = "keep"
    elif a or b:
        # 단독 판정: drop 금지, retype 만 허용
        verdict = va if a else vb
        if verdict == "drop":
            verdict = "keep"
        primary = a or b

    if verdict == "retype" and not (primary.get("type") or "").strip():
        verdict = "keep"

    counts[verdict] = counts.get(verdict, 0) + 1
    models = ("claude" if a else "") + ("+codex" if b else "")
    ev = primary.get("evidence") or primary.get("reason") or ""

    sql.append(
        "INSERT INTO kwave_kdb_recheck_log (entity_id, term_ko, verdict, new_type, models, agreed, evidence) "
        f"VALUES ({q(eid)}::uuid, {q(row[1])}, {q(verdict)}, {q(primary.get('type',''))}, "
        f"{q(models)}, {'true' if (a and b and va==vb) else 'false'}, {q(ev[:500])});"
    )

    if verdict == "drop":
        sql.append(
            "UPDATE kwave_entities SET status='candidate', "
            "notes = COALESCE(notes,'') || ' [recheck:drop 2026-07-29 panel]', updated_at=now() "
            f"WHERE id={q(eid)}::uuid AND status='active';"
        )
    elif verdict == "retype":
        t = primary["type"].strip()
        # 동일 (ko,type,disambig) 충돌 시 스킵 — homonym 유니크 제약 보호
        sql.append(
            f"UPDATE kwave_entities e SET entity_type={q(t)}::kwave_entity_type, "
            "notes = COALESCE(e.notes,'') || ' [recheck:retype 2026-07-29]', updated_at=now() "
            f"WHERE e.id={q(eid)}::uuid AND e.status='active' AND NOT EXISTS ("
            "SELECT 1 FROM kwave_entities x WHERE x.canonical_ko=e.canonical_ko "
            f"AND x.entity_type={q(t)}::kwave_entity_type AND COALESCE(x.disambig,'')=COALESCE(e.disambig,'') AND x.id<>e.id);"
        )
    else:
        # keep — 재심 통과 표시(같은 항목을 매 회차 다시 태우지 않도록)
        sql.append(
            "UPDATE kwave_entities SET notes = COALESCE(notes,'') || ' [recheck:keep 2026-07-29]', "
            f"updated_at=now() WHERE id={q(eid)}::uuid AND status='active';"
        )

sql.append("COMMIT;")
open(f"{W}/apply.sql", "w", encoding="utf-8").write("\n".join(sql) + "\n")
print(json.dumps({"pool": len(pool), "judged": len(set(cl) | set(cx)), "codex": has_codex,
                  "agree": agree, "disagree": disagree, "counts": counts}, ensure_ascii=False))

package kdb

// catchall_retype — **담을 칸이 없어 잡동사니 유형에 앉은 대상**을 제 유형으로 옮긴다.
//
// ★계기 (2026-09-16). 소비자 재측정에서 둘이 여전히 안 나갔다:
//
//	국민의힘   brand_place  rejected
//	SK하이닉스 brand_place  rejected
//
//   원장 노트에 이유가 그대로 적혀 있다 — 분류기가 스스로 말했다:
//
//	"국민의힘은 한국의 정당명으로, 인물/작품/매체가 아닌 고유 조직명이라
//	 **사용 가능한 분류 중 brand_place가 가장 가깝습니다**"
//
//   틀린 판단이 아니라 **칸이 없어서 눌러 담은 것**이다. 0143·0146 으로 정당·기업·
//   기관·구단·학교·게임 칸이 생겼으니 이제 제자리가 있다.
//
// ★잡동사니 유형에서만 꺼낸다. person·drama·movie 같은 유형은 사람이나 근거가
//   **골라서** 붙인 것이라 P31 하나로 뒤집지 않는다. 여기서 옮기는 것은
//   brand_place·term·unknown — "달리 담을 데가 없을 때" 쓰이던 칸뿐이다.
//
// ★P31 이 **한 유형만** 가리킬 때만 옮긴다. Q4830453("business")처럼 agency 와
//   company 를 못 가르는 클래스는 그냥 둔다. 못 가르는 것을 가른다고 하면
//   틀린 유형을 확신을 갖고 쓰게 된다(D-37).
//
// ★기본 dry-run. 옮겨도 status 는 건드리지 않는다 — 유형이 맞다는 것이 서빙해도
//   된다는 뜻은 아니다. 되살림은 scope-reopen 의 몫이고 승급은 평소 경로의 몫이다.

import (
	"context"
	"log"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
)

// CatchallTypes — "달리 담을 데가 없을 때" 쓰이던 칸. 여기서만 꺼낸다.
var CatchallTypes = []string{"brand_place", "term", "unknown"}

// CatchallRetypeResult — 한 번 돈 결과.
type CatchallRetypeResult struct {
	Checked, Retyped, Ambiguous, NoClass, NoEvidence int
	Samples                                          []string
}

// DrainCatchallRetype — 잡동사니 유형에 앉은 행을 P31 이 가리키는 유형으로 옮긴다.
func DrainCatchallRetype(ctx context.Context, pool *pgxpool.Pool, cl *wikidata.Client, limit int, dry bool) CatchallRetypeResult {
	var r CatchallRetypeResult
	if pool == nil || cl == nil || limit <= 0 {
		return r
	}
	rows, err := pool.Query(ctx, `
SELECT e.id::text, e.canonical_ko, e.entity_type::text, e.status::text, x.external_id
  FROM kwave_entities e
  JOIN kwave_entity_external_refs x ON x.entity_id = e.id AND x.provider = 'wikidata'
 WHERE e.entity_type::text = ANY($1)
   AND e.operator_locked = false
   AND x.external_id ~ '^Q[0-9]+$'
 ORDER BY e.updated_at DESC
 LIMIT $2`, CatchallTypes, limit)
	if err != nil {
		log.Printf("kdb.catchall-retype: select: %v", err)
		return r
	}
	type row struct{ id, ko, typ, status, qid string }
	var items []row
	for rows.Next() {
		var it row
		if rows.Scan(&it.id, &it.ko, &it.typ, &it.status, &it.qid) == nil {
			items = append(items, it)
		}
	}
	rows.Close()

	for _, it := range items {
		ent, ferr := cl.Fetch(ctx, it.qid)
		if ferr != nil || ent == nil {
			r.NoEvidence++
			continue
		}
		r.Checked++
		want, ok := soleAnchorType(ent.InstanceOf)
		switch {
		case !ok:
			r.NoClass++
			continue
		case want == "":
			r.Ambiguous++
			continue
		case want == it.typ:
			continue
		}
		desc := strings.TrimSpace(ent.Descriptions["en"])
		if len(r.Samples) < 40 {
			r.Samples = append(r.Samples, it.ko+"  "+it.typ+" → "+want+"  ("+desc+")")
		}
		log.Printf("  재유형 %-18s %-12s → %-16s %s", it.ko, it.typ, want, desc)
		if dry {
			r.Retyped++
			continue
		}
		// ★subtype 은 함께 비운다 — 옛 유형에 딸린 값이라 새 유형에서는 뜻이 없다
		//   (0137 이 같은 이유로 재유형 시 subtype 을 비우게 했다).
		tag, uerr := pool.Exec(ctx, `
UPDATE kwave_entities
   SET entity_type = $2::kwave_entity_type, subtype = NULL, updated_at = now(),
       notes = COALESCE(NULLIF(notes,'') || ' · ','')
               || '[catchall-retype] ' || $3 || ' → ' || $2
               || ' (wikidata ' || $4 || ' ' || $5 || ')'
 WHERE id = $1 AND entity_type::text = $3 AND operator_locked = false`,
			it.id, want, it.typ, it.qid, desc)
		if uerr != nil {
			log.Printf("kdb.catchall-retype: update %s: %v", it.ko, uerr)
			continue
		}
		if tag.RowsAffected() > 0 {
			r.Retyped++
		}
	}
	return r
}

// soleAnchorType — P31 목록이 **하나의 유형만** 가리키면 그것을. 갈리면 ("",true).
// 아는 클래스가 하나도 없으면 ("",false).
func soleAnchorType(instanceOf []string) (string, bool) {
	seen := map[string]bool{}
	known := false
	for _, q := range instanceOf {
		// ★결정하지 못하는 클래스는 **없는 것처럼** 지나간다 (2026-09-16).
		//   Q43229("organization")은 단체·기업·기관이 전부 갖는다. 이것으로 유형을
		//   정하면 네이버(기업)가 organization 으로 옮겨진다 — 실제로 그렇게 나왔다.
		if genericAnchorClasses[strings.TrimSpace(q)] {
			continue
		}
		types := anchorExpectedType[strings.TrimSpace(q)]
		if len(types) == 0 {
			continue
		}
		known = true
		if len(types) > 1 {
			// 클래스 자체가 못 가른다 — 이 앵커로는 결론을 못 낸다.
			return "", true
		}
		seen[types[0]] = true
	}
	if !known {
		return "", false
	}
	if len(seen) != 1 {
		return "", true
	}
	for t := range seen {
		return t, true
	}
	return "", true
}

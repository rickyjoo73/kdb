package disambiguator

// same_qid_merge — **한 대상이 여러 줄로 있는 것을 하나로 합친다.**
//
// ★운영자 규칙 (2026-09-15): "사람은 id 하나가 한사람이어야 돼. 동일인이라 호칭이
//   다른 건데, 호칭도 아이디에 포함될 수 있도록 해야 돼."
//
// ★실측. 활성 원장에서 62개 QID 를 127개 행이 나눠 갖고 있었다. 거의 전부 같은 사람의
//   다른 호칭이다:
//
//     아이유/이지은 · 뷔/김태형 · RM/김남준 · 지민/박지민 · 제이홉/정호석
//     시우민/김민석 · 도경수/디오 · 피오/표지훈 · 하니/안희연 · 곽준빈/곽튜브
//
//   소비자에게는 이것이 "동명 여럿"으로 나간다 — 고를 수 없는 후보 목록이다.
//   동명이인이 아니라 **한 사람이 두 줄로 있는 것**이라, 구별 정보를 더 가져와도
//   안 풀린다. 합쳐야 풀린다.
//
// ★I03 — 자체 ID 가 주 앵커다. QID 는 보조다.
//
//   처음 쓸 때 이 규칙을 어겼다. QID 가 (1) 두 줄이 같은 대상인지와 (2) 어느 이름이
//   남는지를 **둘 다** 정하게 만들었다. 그러면 위키데이터가 우리 정체성을 정의한다.
//
//   고친 뒤:
//     · 같은 QID 는 **합쳐도 되는가**를 받치는 근거다(mergeEvidenceGate 가 요구한다).
//       근거일 뿐 결정권자가 아니다.
//     · **누가 남는지는 우리 원장이 정한다.** 아래 pickSurvivor 참조 — 외부를 안 본다.
//     · 살아남은 행의 canonical_ko 는 그대로 둔다. 위키데이터 라벨로 바꾸지 않는다.
//       진 쪽의 이름·별칭은 전부 이긴 쪽 별칭으로 옮겨져 **잃는 이름이 없다.**
//
// ★유형이 다르면 합치지 않는다. 같은 QID 인데 유형이 갈리면 둘 중 하나가 틀린 것이고,
//   무엇이 틀렸는지는 이 근거로 못 가른다(육중완/person 과 장미여관/group 이 같은 QID 를
//   쥔 것은 육중완의 앵커가 오링크라는 뜻이지 둘이 같은 대상이라는 뜻이 아니다).
//
// ★기본 dry-run.

import (
	"context"
	"log"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SameQIDMergeResult — 한 번 돈 결과.
type SameQIDMergeResult struct {
	Groups, Merged, Skipped int
	// Review — 근거만으로 못 정한 것. 사람이 본다.
	Review []string
}

// survivorRank — 어느 행이 남을 자격이 있는가. **우리 원장만 본다**(I03).
//
// 순서에 이유가 있다.
//   ① operator_locked — 운영자가 손으로 정한 것을 기계가 뒤집지 않는다.
//   ② 근거 수 — 되짚을 수 있는 행이 남아야 한다.
//   ③ created_at 오름차순 — **가장 오래된 ID 가 남는다.** 소비자가 이미 받아 저장했을
//      가능성이 가장 높은 ID 다. UUID 는 불변이고(I02) 우리가 바꿀 수 없으니, 바깥에
//      나가 있을 확률이 큰 쪽을 살린다.
const survivorRank = `
 ORDER BY e.operator_locked DESC,
          (SELECT count(*) FROM kwave_entity_external_refs r WHERE r.entity_id = e.id) DESC,
          COALESCE(array_length(e.source_urls, 1), 0) DESC,
          e.created_at ASC`

// DrainSameQIDMerge — 같은 QID·같은 유형인 활성 행 무리를 합친다.
func DrainSameQIDMerge(ctx context.Context, pool *pgxpool.Pool, limit int, dry bool) SameQIDMergeResult {
	var r SameQIDMergeResult
	if pool == nil || limit <= 0 {
		return r
	}
	// 무리를 고를 때 **우리 원장 순서로 정렬해 담는다** — 첫 원소가 남을 행이다.
	rows, err := pool.Query(ctx, `
SELECT x.external_id, e.entity_type::text, array_agg(e.id ORDER BY
         e.operator_locked DESC,
         (SELECT count(*) FROM kwave_entity_external_refs r WHERE r.entity_id = e.id) DESC,
         COALESCE(array_length(e.source_urls, 1), 0) DESC,
         e.created_at ASC)
  FROM kwave_entity_external_refs x
  JOIN kwave_entities e ON e.id = x.entity_id
 WHERE x.provider = 'wikidata' AND x.external_id ~ '^Q[0-9]+$'
   AND e.status = 'active'
 GROUP BY x.external_id, e.entity_type
HAVING count(DISTINCT e.id) > 1
 ORDER BY x.external_id
 LIMIT $1`, limit)
	if err != nil {
		log.Printf("kdb.same-qid-merge: select: %v", err)
		return r
	}
	type group struct {
		qid, typ string
		ids      []uuid.UUID
	}
	var groups []group
	for rows.Next() {
		var g group
		if rows.Scan(&g.qid, &g.typ, &g.ids) == nil && len(g.ids) > 1 {
			groups = append(groups, g)
		}
	}
	rows.Close()

	for _, g := range groups {
		r.Groups++
		members, err := readMembers(ctx, pool, g.ids)
		if err != nil || len(members) < 2 {
			r.Skipped++
			continue
		}
		byID := map[uuid.UUID]member{}
		for _, m := range members {
			byID[m.id] = m
		}
		// g.ids[0] 이 우리 순서상 첫 행이다. readMembers 는 순서를 보장하지 않으므로
		// 이름으로 되찾는다 — 정렬을 SQL 에서 한 이유가 이것이다.
		winner, ok := byID[g.ids[0]]
		if !ok {
			r.Skipped++
			continue
		}
		if winner.operatorLockedUnknown() {
			// readMembers 가 잠금 여부를 안 실어 준다. 잠긴 행을 지우지 않는 것은
			// mergeAtomically 의 WHERE 가 보장하므로 여기서는 계속 간다.
			_ = winner
		}
		for _, id := range g.ids[1:] {
			loser, ok := byID[id]
			if !ok {
				continue
			}
			log.Printf("  %s [%s]  %s ← %s", g.qid, g.typ, winner.ko, loser.ko)
			if dry {
				r.Merged++
				continue
			}
			asg := memberResult{
				ID:       loser.id.String(),
				Decision: "merge",
				Reason:   "same wikidata item (보조근거); 생존 ID 는 자체 원장 순서로 정함",
			}
			same := winner.id.String()
			asg.SameAs = &same
			rel := "same"
			asg.Relation = &rel
			if err := mergeAtomically(ctx, pool, loser, winner, asg); err != nil {
				log.Printf("  [보류] %s ← %s: %v", winner.ko, loser.ko, err)
				r.Review = append(r.Review, g.qid+": "+loser.ko+" → "+winner.ko+" — "+err.Error())
				continue
			}
			r.Merged++
		}
	}
	return r
}

// operatorLockedUnknown — member 에 잠금 칸이 없다는 사실을 코드로 드러낸다.
// 잠긴 행 보호는 mergeAtomically 의 UPDATE WHERE 절이 한다.
func (m member) operatorLockedUnknown() bool { return true }

func names(ms []member) string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.ko+"/"+m.entityType)
	}
	return strings.Join(out, " · ")
}

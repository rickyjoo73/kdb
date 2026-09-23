package kdb

// hint_p31_retrace — **소비자 type 힌트로 굳은 유형**을 위키데이터 P31 이 분명히
// 다르게 말할 때만 옮긴다.
//
// ★계기 (2026-09-23, 소비자 신고 3건). 카카오가 event_tour, 케이뱅크·삼성전자·네이버가
//   brand_place 로 서빙되고 있었다. 넷 다 인입 때 소비자가 보낸 type 을 그대로 받았다.
//   분류 순서가 ① 소비자 type ② P31 이기 때문이다(aijudge/classify_evidence.go).
//
// ★인입 순서를 뒤집는 것으로는 못 고친다. 인입 시점에는 P31 이 **없다** —
//   ClassifyInput.InstanceOf 를 채우는 호출자가 하나도 없다(2026-09-23 확인).
//   앵커는 나중에 붙는다. 그래서 고치는 자리는 «앵커가 붙은 뒤» 이 레인이다.
//
// ★전면 역전은 하지 않는다. 기사를 본 쪽의 판단이 맞는 경우가 많고, 앵커가 틀린
//   경우도 있다(두산 베어스에 사람 QID). 셋이 모두 맞을 때만 옮긴다:
//     ① P31 이 **한 유형만** 가리킨다        (soleAnchorType — catchall-retype 과 같은 판정)
//     ② 그 유형이 저장 유형과 다르다
//     ③ 앵커가 **이 대상**이다 — kowiki 문서 제목·ko 라벨·ko 별칭 중 하나가 정본과
//        정규화 일치. 틀린 앵커로 유형을 뒤집는 것이 가장 나쁜 실패다.
//
// ★옮겨 갈 곳은 **기관·기업 계열뿐**이다 (2026-09-23 dry 실측으로 좁혔다).
//   처음엔 person 에서 꺼내는 것만 막았다. dry 124건 중 약 100건이 **사람·작품으로**
//   가는 방향이었고, 거의 전부 **앵커가 틀린 것**이었다 — 미란·미호(character)에
//   성인배우 QID, 매미·미래·솜사탕(곡)에 동명 가수, 단발머리(조용필 곡)에 걸그룹.
//   사람 이름·흔한 낱말은 동명이 많아 «kowiki 제목 = 정본» 으로는 못 거른다.
//   기관·기업 이름은 고유해서 그 확인이 실제로 신원을 가른다. 카카오(event_tour)·
//   케이뱅크(brand_place) 가 이 모양이다. 나머지 어긋남은 **앵커 쪽**의 일이다
//   (anchor-enforce — 판정된 틀린 앵커를 떼고 그 앵커에서 온 표기를 비운다).
//
// ★기본 dry-run. 쓸 때는 type-retrace 와 같은 모양으로 남긴다 — dataqa_log 스냅샷
//   (verdict='retrace-type-fix', model='wikidata-p31', old_value=옛 유형)과
//   `[retrace:type-fix a→b]` 노트. status 는 건드리지 않는다.

import (
	"context"
	"log"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
)

// 판정 결과 — 세는 이유가 곧 설명이다. 가장 앞에서 걸린 이유로 센다.
const (
	hintP31Move        = "move"
	hintP31Same        = "same"         // P31 도 같은 말을 한다
	hintP31NoClass     = "no-class"     // 우리 표가 아는 P31 이 없다 — 판정하지 않는다(D-37)
	hintP31Ambiguous   = "ambiguous"    // P31 이 여러 유형을 가리킨다
	hintP31NameElement = "name-element" // 앵커가 이름 항목이다 — 어떤 유형의 근거도 아니다
	hintP31Identity    = "identity"     // 앵커가 이 대상인지 확인 못 함
	hintP31PersonHeld  = "person-held"  // person 은 꺼내지 않는다
	hintP31NotOrg      = "not-org"      // 기관·기업 계열이 아닌 곳으로는 옮기지 않는다
)

// hintP31OrgTargets — 옮겨 갈 수 있는 유형. 이름이 고유해 앵커 신원 확인이 믿을 만한 것만.
// channel_outlet 은 뺀다 — «MBC 대학가요제»(음악제)가 채널 클래스로 잡혔다.
var hintP31OrgTargets = map[string]bool{
	"company": true, "agency": true, "organization": true, "government_body": true,
	"school": true, "sports_team": true, "political_party": true,
}

// HintP31RetraceResult — 한 번 돈 결과.
type HintP31RetraceResult struct {
	Checked, Moved, NoEvidence int
	ByReason                   map[string]int
	ByMove                     map[string]int // "event_tour→company" 식
	Samples                    []string
}

// hintP31Decide — **순수 판정.** 저장 유형·정본·앵커만 보고 옮길지 정한다.
func hintP31Decide(storedType, ko string, ent *wikidata.Entity) (want, reason string) {
	if nameEl, _ := ent.IsNameElement(); nameEl {
		return "", hintP31NameElement
	}
	t, known := soleAnchorType(ent.InstanceOf)
	switch {
	case !known:
		return "", hintP31NoClass
	case t == "":
		return "", hintP31Ambiguous
	case t == storedType:
		return "", hintP31Same
	case storedType == "person":
		return "", hintP31PersonHeld
	case !hintP31OrgTargets[t]:
		return "", hintP31NotOrg
	}
	if !anchorIsThisName(ent, ko) {
		return "", hintP31Identity
	}
	return t, hintP31Move
}

// anchorIsThisName — 앵커가 우리 정본을 가리키는가. kowiki 제목(동음이의 괄호 제거)·
// ko 라벨·ko 별칭 중 하나라도 정규화 일치하면 그렇다.
func anchorIsThisName(ent *wikidata.Entity, ko string) bool {
	want := wikidata.NormalizeName(ko)
	if want == "" || ent == nil {
		return false
	}
	cands := []string{stripParenSuffix(ent.SiteTitles["kowiki"]), ent.Labels["ko"], ent.SourceLabels["ko"]}
	cands = append(cands, ent.Aliases["ko"]...)
	for _, c := range cands {
		if c = strings.TrimSpace(c); c != "" && wikidata.NormalizeName(c) == want {
			return true
		}
	}
	return false
}

// DrainHintP31Retrace — 소비자 힌트로 유형을 받은 활성 행을 P31 로 다시 본다.
func DrainHintP31Retrace(ctx context.Context, pool *pgxpool.Pool, cl *wikidata.Client, limit int, dry bool) HintP31RetraceResult {
	r := HintP31RetraceResult{ByReason: map[string]int{}, ByMove: map[string]int{}}
	if pool == nil || cl == nil || limit <= 0 {
		return r
	}
	// 위키데이터 앵커가 **하나뿐인** 행만 본다. 둘 이상이면 어느 것이 이 대상인지부터
	// 갈라야 하고, 그건 이 레인의 일이 아니다.
	rows, err := pool.Query(ctx, `
SELECT e.id::text, e.canonical_ko, e.entity_type::text, min(x.external_id)
  FROM kwave_entities e
  JOIN kwave_entity_external_refs x ON x.entity_id = e.id AND x.provider = 'wikidata'
 WHERE e.status = 'active'
   AND e.operator_locked = false
   AND COALESCE(e.notes,'') LIKE '%소비자 type힌트=%'
   AND x.external_id ~ '^Q[0-9]+$'
 GROUP BY e.id, e.canonical_ko, e.entity_type
HAVING count(DISTINCT x.external_id) = 1
 ORDER BY e.canonical_ko
 LIMIT $1`, limit)
	if err != nil {
		log.Printf("kdb.hint-p31-retrace: select: %v", err)
		return r
	}
	type row struct{ id, ko, typ, qid string }
	var items []row
	for rows.Next() {
		var it row
		if rows.Scan(&it.id, &it.ko, &it.typ, &it.qid) == nil {
			items = append(items, it)
		}
	}
	rows.Close()

	for _, it := range items {
		if ctx.Err() != nil {
			break
		}
		ent, ferr := cl.Fetch(ctx, it.qid)
		if ferr != nil || ent == nil {
			r.NoEvidence++ // 조회 실패는 판정이 아니다
			continue
		}
		r.Checked++
		want, why := hintP31Decide(it.typ, it.ko, ent)
		r.ByReason[why]++
		if why != hintP31Move {
			continue
		}
		move := it.typ + "→" + want
		r.ByMove[move]++
		desc := strings.TrimSpace(ent.Descriptions["en"])
		if len(r.Samples) < 60 {
			r.Samples = append(r.Samples, it.ko+"  "+move+"  "+it.qid+" ("+desc+")")
		}
		if dry {
			r.Moved++
			continue
		}
		reason := "wikidata " + it.qid + " P31=" + strings.Join(ent.InstanceOf, ",") + " " + desc
		_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_dataqa_log (entity_id, locale, old_value, old_source, verdict, reason, model)
VALUES ($1, 'entity_type', $2, '', 'retrace-type-fix', $3, 'wikidata-p31')`,
			it.id, it.typ, truncRunes(reason, 200))
		// ★subtype 은 쓰지 않는다 — kwave_entities 에 그 컬럼이 없고, 0137 트리거가 비운다.
		tag, uerr := pool.Exec(ctx, `
UPDATE kwave_entities
   SET entity_type = $2::kwave_entity_type, updated_at = now(),
       notes = COALESCE(NULLIF(notes,'') || ' ','') || $4
 WHERE id = $1 AND entity_type::text = $3 AND operator_locked = false`,
			it.id, want, it.typ, "[retrace:type-fix "+move+"] wikidata "+it.qid+" "+truncRunes(desc, 60))
		if uerr != nil {
			log.Printf("kdb.hint-p31-retrace: update %s: %v", it.ko, uerr)
			continue
		}
		if tag.RowsAffected() > 0 {
			r.Moved++
			log.Printf("  [type-fix] %s: %s (%s %s)", it.ko, move, it.qid, truncRunes(desc, 50))
		}
	}
	return r
}

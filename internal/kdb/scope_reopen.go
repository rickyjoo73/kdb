package kdb

// scope_reopen — **옛 범위로 죽은 한국 대상을 되살린다.**
//
// ★계기 (2026-09-15). 소비자(presslocale)가 문서와 실제가 다르다고 알려 왔다:
//     더불어민주당 · 두산 베어스 · 서울대학교 · 이재명  → 전부 `out_of_scope`
//   문서에는 새 유형이 들어갔는데 서버가 그대로 거절한다.
//
//   원인은 옛 기각이었다. 원장 원문:
//
//     이재명 "한국의 실존 정치인으로 널리 알려진 인명이다"
//            → 기각 "K-엔터테인먼트 인물이 아님"
//     차범근 "한국의 전설적인 축구선수" (위키데이터 Q346751 확인)
//            → 5회 기각, 83일 미결 TTL 만료
//
//   시스템이 "한국 정치인이다"를 **알고서** 그 이유로 기각했다. 그때 범위에서는 옳았고
//   지금은 죽은 이유다. 게이트는 고쳤지만(intake_autoverify), 이미 rejected 로 누운
//   행들은 스스로 못 일어난다.
//
// ★노트를 해석해서 되살리지 않는다. 노트는 사람이 쓴 문장이고 서로 어긋난다 —
//   차범근 노트에는 "한국의 전설적인 축구선수"와 "외국 스포츠 선수"가 **둘 다** 있다.
//   그것으로 가르면 내 해석이 근거가 된다.
//
//   대신 **위키데이터가 한국 대상이라고 말하는가**를 본다. 차범근 Q346751 의 설명은
//   "South Korean association football player" 다. 이건 관측이지 해석이 아니다.
//
// ★되살려도 active 로 올리지 않는다. `candidate` 로 되돌릴 뿐이다 — 승급은 평소 경로가
//   근거를 보고 한다. 범위가 넓어졌다는 것이 "근거 없이 서빙해도 된다"는 뜻은 아니다.
//
// ★기본 dry-run.

import (
	"context"
	"log"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
)

// ScopeReopenResult — 한 번 돈 결과.
type ScopeReopenResult struct {
	Checked, Reopened, StillForeign, NoEvidence int
	// NameElement — 이름 항목(given name)이라 되살리지 않은 것.
	// Concept — 언어·역사 개념이라 되살리지 않은 것.
	// AnchorDropped — 되살리면서 어긋난 앵커를 뗀 것.
	NameElement, Concept, AnchorDropped int
	Samples                             []string
}

// koreanSubjectMarkers — 위키데이터 설명이 **한국 대상**이라고 말하는 표시.
// 영문 설명이 사실상 표준이라 영문만 본다(ko 설명은 비어 있는 경우가 많다).
var koreanSubjectMarkers = []string{
	"south korean", "korean", "south korea", "of korea", "in korea",
}

// conceptMarkers — 대상이 아니라 **개념**을 가리키는 설명. 되살리지 않는다.
var conceptMarkers = []string{
	"language spoken", "language of", "writing system", "alphabet",
	"confederacy", "kingdom of", "dynasty", "historical period", "era of",
	"given name", "family name", "surname",
	"wikimedia", "disambiguation", "list of",
}

// foreignMarkers — 한국 표시가 있어도 **이쪽이 있으면 안 되살린다.**
// "Korean-American", "Japanese-Korean" 같은 겹표기에서 오되살림을 막는다.
var foreignMarkers = []string{
	"japanese", "chinese", "american", "british", "taiwanese", "thai",
	"vietnamese", "indonesian", "north korean", "global",
}

// DrainScopeReopen — 옛 범위로 기각된 행 중, 위키데이터가 한국 대상이라 말하는 것을
// candidate 로 되돌린다.
func DrainScopeReopen(ctx context.Context, pool *pgxpool.Pool, cl *wikidata.Client, limit int, dry bool) ScopeReopenResult {
	var r ScopeReopenResult
	if pool == nil || cl == nil || limit <= 0 {
		return r
	}
	rows, err := pool.Query(ctx, `
SELECT e.id::text, e.canonical_ko, e.entity_type::text, x.external_id
  FROM kwave_entities e
  JOIN kwave_entity_external_refs x ON x.entity_id = e.id AND x.provider = 'wikidata'
 WHERE e.status = 'rejected' AND e.operator_locked = false
   AND x.external_id ~ '^Q[0-9]+$'
   -- 옛 범위 사유로 죽은 것만. 병합·일반어·TTL 만료는 건드리지 않는다 —
   -- 그 판단들은 범위가 넓어져도 그대로 옳다.
   -- ★'비연예' 도 같은 계열이다(2026-09-15). audit-revert 가 남긴 문구로,
   --   위 둘과 명제가 같다 — 연예가 아니라는 것이지 대상이 없다는 것이 아니다.
   -- ★같은 명제의 다섯 가지 표현을 한 패턴으로 모은다(api.go Tombstoned 주석 참조).
   --   비-K(범위밖) · K-엔터테인먼트 · 비연예 · 비-엔터 · K-콘텐츠
   AND COALESCE(e.notes,'') ~ '` + ScopeRejectionNotePattern + `'
   AND COALESCE(e.notes,'') NOT LIKE '%merged into%'
 ORDER BY e.updated_at DESC
 LIMIT $1`, limit)
	if err != nil {
		log.Printf("kdb.scope-reopen: select: %v", err)
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
		ent, ferr := cl.Fetch(ctx, it.qid)
		if ferr != nil || ent == nil {
			r.NoEvidence++
			continue
		}
		desc := strings.ToLower(strings.TrimSpace(ent.Descriptions["en"]))
		if desc == "" {
			r.NoEvidence++
			continue
		}
		r.Checked++

		// ★이름 항목은 되살리지 않는다 (dry-run 이 잡음).
		//   "한국"이 들어간 설명이면 통과시켰더니 이런 것들이 같이 올라왔다:
		//     하림 "Korean unisex given name" · 수성 "Korean male given name"
		//     뉴   "Korean given name element"
		//   이름 항목은 **어떤 대상의 근거도 못 된다** — 오늘 앵커 감사에서 24건을
		//   철회한 바로 그 계열이다. 같은 판정(P31)을 그대로 쓴다.
		if isName, cls := ent.IsNameElement(); isName {
			r.NameElement++
			log.Printf("  [이름항목] %-16s %s (%s)", it.ko, ent.Descriptions["en"], cls)
			continue
		}
		// ★개념·언어·역사 정치체도 아니다. 사람이나 조직이나 작품이어야 한다.
		//     한국어 "language spoken in Korean Peninsula"
		//     가야   "confederacy of territorial polities ... (AD 42-562)"
		//   이것들은 범위가 넓어졌다고 들어오는 대상이 아니다.
		if containsAny(desc, conceptMarkers) {
			r.Concept++
			log.Printf("  [개념]     %-16s %s", it.ko, ent.Descriptions["en"])
			continue
		}
		if !containsAny(desc, koreanSubjectMarkers) {
			r.StillForeign++
			continue
		}
		if containsAny(desc, foreignMarkers) {
			r.StillForeign++
			continue
		}
		// ★앵커가 우리 유형과 어긋나면 **되살리되 앵커를 뗀다**.
		//   dry-run 이 잡았다: 이재명/person 에 "South Korean footballer" QID 가 붙어
		//   있었다. 정치인 이재명이 아니라 **동명이인 축구선수**다. 그대로 되살리면
		//   엉뚱한 사람의 표기를 서빙한다. 대상은 살리고 틀린 근거만 뗀다
		//   (person_anchor_withdraw 와 같은 방침: 틀린 것은 QID 이지 사람이 아니다).
		mismatch := false
		for _, q := range ent.InstanceOf {
			if want, ok := AnchorExpectedType(q); ok && want != it.typ {
				mismatch = true
				break
			}
		}
		if len(r.Samples) < 40 {
			r.Samples = append(r.Samples, it.ko+"/"+it.typ+" — "+ent.Descriptions["en"])
		}
		log.Printf("  되살림 %-16s %-14s %s", it.ko, it.typ, ent.Descriptions["en"])
		if dry {
			r.Reopened++
			continue
		}
		// candidate 로만 되돌린다. 승급은 평소 경로가 근거를 보고 한다.
		note := "[scope-reopen 2026-09-15] 범위 확대(0143)로 옛 기각 사유 소멸 — " + ent.Descriptions["en"]
		if mismatch {
			// 대상은 살리고 틀린 근거만 뗀다.
			if _, derr := pool.Exec(ctx, `
DELETE FROM kwave_entity_external_refs
 WHERE entity_id=$1 AND provider='wikidata' AND external_id=$2`, it.id, it.qid); derr == nil {
				r.AnchorDropped++
				note += " · [앵커철회] " + it.qid + " 가 우리 유형과 어긋나 뗌(동명이인 의심)"
			}
		}
		tag, uerr := pool.Exec(ctx, `
UPDATE kwave_entities
   SET status='candidate', updated_at=now(),
       notes = COALESCE(NULLIF(notes,'') || ' · ','') || $2
 WHERE id=$1 AND status='rejected' AND operator_locked=false`, it.id, note)
		if uerr == nil && tag.RowsAffected() > 0 {
			r.Reopened++
		}
	}
	return r
}

func containsAny(s string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}

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
	"time"

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
   AND COALESCE(e.notes,'') ~ '`+ScopeRejectionNotePattern+`'
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
			if ok, known := AnchorTypeAllowed(q, it.typ); known && !ok {
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
		// ★시계 표시를 반드시 같이 붙인다 (2026-09-16).
		//   candidate_ttl 은 created_at 을 보므로, 표시가 없으면 되살린 행이
		//   **몇 분 만에** "N일 미결"로 다시 기각된다. 오세훈은 25분이었다.
		note := ReopenNote(time.Now(), "[scope-reopen] 범위 확대(0143)로 옛 기각 사유 소멸 — "+ent.Descriptions["en"])
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

// ─── 직업 범위로 묻힌 행 되살리기 ───────────────────────────────────────────

// OccupationScopeRestoreResult — 한 번 돈 결과.
type OccupationScopeRestoreResult struct {
	Checked, Reopened, Promoted, StillForeign, NoEvidence int
	NameElement, Concept, AnchorDropped, TwinActive       int
	// FetchFailed — **물어보지 못한** 건수. NoEvidence(물어봤는데 설명이 없다)와
	// 반드시 나눠 센다.
	//
	// ★첫 dry-run 이 20건 전부 «근거없음» 으로 나왔다(2026-09-20). 행이 나쁜 줄 알았는데
	//   실은 인증서 없는 이미지에서 돌려 HTTPS 가 통째로 실패한 것이었다. 한 칸으로
	//   세면 **전송 실패가 판정으로 세탁된다** — MarkFillAttempt 주석이 경고하는 바로 그것이다.
	FetchFailed int
	Samples     []string
}

// DrainOccupationScopeRestore — `[revert-term:reject]` 를 달았지만 사유가
// «직업이 연예가 아니다»인 행을 되살린다.
//
// ★왜 scope_reopen 과 따로인가. 저쪽은 **기각된** 행만 본다. 이 계열은 2026-07-21
//
//	감사가 active 를 **강등**시킨 것이라 대부분 candidate 로 누워 있다 — 저 레인의
//	`status='rejected'` 에 걸리지 않는다. 실측 727행 중 389 는 active 로 회복됐고
//	334 가 그대로 남았다(candidate 169 · rejected 165). 회복이 도중에 멈춘 것이다.
//
// ★노트를 믿지 않는다. 노트가 "South Korean" 이라 적었어도 **지금 위키데이터에
//
//	물어본다.** scope_reopen 과 같은 가드를 그대로 쓴다 — 이름 항목·개념·해외 대상·
//	앵커 유형 불일치. 근거는 관측이어야 하고, 내 해석이면 안 된다.
//
// ★되살림의 크기를 근거에 맞춘다.
//
//	rejected                         → candidate  (scope_reopen 과 같다)
//	candidate + authoritative + 앵커성립 → active  (강등 이전 자리로 되돌림)
//	candidate + 그 밖               → candidate 유지, 표시만 (TTL 시계 재시작)
//
//	가운데를 active 로 올리는 근거는 «범위가 넓어졌다»가 아니다. 그 행은 등급이 이미
//	authoritative(verification_evidence='wikidata')고, 그 등급은 범위 판단과 무관하게
//	매겨진 것이다. 여기서 하는 일은 **죽은 명제로 내린 강등을 되돌리는 것**이지 새
//	승급이 아니다. 앵커가 우리 유형과 어긋나면 그 근거가 무너지므로 올리지 않는다.
//
// ★confidence 를 같이 올린다. 강등이 0.000 으로 깎아 놓았고, 조회 API 의 기본
//
//	min_confidence 가 0.50 이다 — 올려놓지 않으면 active 로 되돌려도 **여전히 안 나간다**.
//	값은 평소 승급 경로(promoteCandidateWithOfficialAnchor)와 같은 0.72 를 쓴다.
//
// ★기본 dry-run.
func DrainOccupationScopeRestore(ctx context.Context, pool *pgxpool.Pool, cl *wikidata.Client, limit int, dry bool) OccupationScopeRestoreResult {
	var r OccupationScopeRestoreResult
	if pool == nil || cl == nil || limit <= 0 {
		return r
	}
	rows, err := pool.Query(ctx, `
SELECT e.id::text, e.canonical_ko, e.entity_type::text, x.external_id,
       e.status, COALESCE(e.verification_tier,''),
       EXISTS (SELECT 1 FROM kwave_entities a
                WHERE a.status='active' AND a.id <> e.id
                  AND a.canonical_ko = e.canonical_ko)
  FROM kwave_entities e
  JOIN kwave_entity_external_refs x ON x.entity_id = e.id AND x.provider = 'wikidata'
 WHERE e.status IN ('rejected','candidate') AND e.operator_locked = false
   AND e.entity_type::text NOT IN ('unknown','term')
   AND x.external_id ~ '^Q[0-9]+$'
   AND COALESCE(e.notes,'') ~ '`+DeadOccupationScopeNotePattern+`'
   AND COALESCE(e.notes,'') NOT LIKE '%[occup-scope-restore]%'
 ORDER BY e.updated_at DESC
 LIMIT $1`, limit)
	if err != nil {
		log.Printf("kdb.occup-scope: select: %v", err)
		return r
	}
	type row struct {
		id, ko, typ, qid, status, tier string
		twin                           bool
	}
	var items []row
	for rows.Next() {
		var it row
		if rows.Scan(&it.id, &it.ko, &it.typ, &it.qid, &it.status, &it.tier, &it.twin) == nil {
			items = append(items, it)
		}
	}
	rows.Close()

	for _, it := range items {
		// ★같은 이름의 active 가 따로 있으면 건드리지 않는다. 되살리면 같은 이름이
		//   둘이 되어 조회가 모호해진다 — 그건 동명이인 라우팅 몫이다.
		if it.twin {
			r.TwinActive++
			continue
		}
		ent, ferr := cl.Fetch(ctx, it.qid)
		if ferr != nil || ent == nil {
			// 물어보지 못한 것이지 «근거가 없는» 것이 아니다. 원장도 건드리지 않는다.
			r.FetchFailed++
			if r.FetchFailed <= 3 && ferr != nil {
				log.Printf("  [조회실패] %-16s %s: %v", it.ko, it.qid, ferr)
			}
			continue
		}
		desc := strings.ToLower(strings.TrimSpace(ent.Descriptions["en"]))
		if desc == "" {
			r.NoEvidence++
			continue
		}
		r.Checked++

		if isName, cls := ent.IsNameElement(); isName {
			r.NameElement++
			log.Printf("  [이름항목] %-16s %s (%s)", it.ko, ent.Descriptions["en"], cls)
			continue
		}
		if containsAny(desc, conceptMarkers) {
			r.Concept++
			log.Printf("  [개념]     %-16s %s", it.ko, ent.Descriptions["en"])
			continue
		}
		// ★노트가 아니라 **지금 설명**이 한국 대상이라 말해야 한다.
		if !strings.Contains(desc, "south korean") {
			r.StillForeign++
			log.Printf("  [한국아님] %-16s %s", it.ko, ent.Descriptions["en"])
			continue
		}
		if containsAny(desc, foreignMarkers) {
			r.StillForeign++
			continue
		}
		mismatch := false
		for _, q := range ent.InstanceOf {
			if ok, known := AnchorTypeAllowed(q, it.typ); known && !ok {
				mismatch = true
				break
			}
		}
		// 강등을 되돌릴 수 있는가 — 등급과 앵커가 둘 다 성립할 때만.
		toActive := it.status == "candidate" && it.tier == "authoritative" && !mismatch

		if len(r.Samples) < 40 {
			mark := "→candidate"
			if toActive {
				mark = "→active"
			}
			r.Samples = append(r.Samples, it.ko+"/"+it.typ+" "+mark+" — "+ent.Descriptions["en"])
		}
		log.Printf("  되살림 %-16s %-14s %-10s %s", it.ko, it.typ, it.status, ent.Descriptions["en"])
		if dry {
			if toActive {
				r.Promoted++
			} else {
				r.Reopened++
			}
			continue
		}

		note := ReopenNote(time.Now(), "[occup-scope-restore] 직업 범위 기각은 0143 으로 소멸 — "+ent.Descriptions["en"])
		if mismatch {
			if _, derr := pool.Exec(ctx, `
DELETE FROM kwave_entity_external_refs
 WHERE entity_id=$1 AND provider='wikidata' AND external_id=$2`, it.id, it.qid); derr == nil {
				r.AnchorDropped++
				note += " · [앵커철회] " + it.qid + " 가 우리 유형과 어긋나 뗌(동명이인 의심)"
			}
		}

		if toActive {
			tag, uerr := pool.Exec(ctx, `
UPDATE kwave_entities
   SET status='active',
       confidence = GREATEST(confidence, 0.72::numeric),
       notes = COALESCE(NULLIF(notes,'') || ' · ','') || $2,
       updated_at = now()
 WHERE id=$1 AND status='candidate' AND operator_locked=false`, it.id, note)
			if uerr == nil && tag.RowsAffected() > 0 {
				r.Promoted++
				// 평소 승급 경로와 같은 뒷정리. 둘 다 멱등이라 이미 있으면 아무 일도 안 한다.
				if it.typ == "person" {
					_, _ = pool.Exec(ctx, `
INSERT INTO kwave_persons (name_ko, primary_role, confidence, last_verified_at, created_at)
VALUES ($1, 'other'::person_role, 0.500, now(), now())
ON CONFLICT (name_ko) DO NOTHING`, it.ko)
					_, _ = pool.Exec(ctx, `
INSERT INTO kwave_entity_person_details (entity_id, primary_role)
VALUES ($1::uuid, 'other'::person_role)
ON CONFLICT (entity_id) DO NOTHING`, it.id)
				}
			}
			continue
		}
		tag, uerr := pool.Exec(ctx, `
UPDATE kwave_entities
   SET status='candidate', updated_at=now(),
       notes = COALESCE(NULLIF(notes,'') || ' · ','') || $2
 WHERE id=$1 AND status IN ('rejected','candidate') AND operator_locked=false`, it.id, note)
		if uerr == nil && tag.RowsAffected() > 0 {
			r.Reopened++
		}
	}
	return r
}

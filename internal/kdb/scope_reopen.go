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
	"github.com/rickyjoo73/kdb/internal/kdb/commonnoun"
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
	// HomonymHeld — 실존은 확인됐으나 동명이인 표시가 있어 active 로 올리지 않은 것.
	HomonymHeld int
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
       -- ★«같은 이름의 active» 는 **조회가 붙이는 방식으로** 물어야 한다.
       --   canonical_ko 만 비교하면 별칭으로 이미 서빙되는 대상을 못 본다
       --   (데이식스→DAY6). 정규화 키 + 별칭 양쪽 — api.go 와 같은 식이다.
       EXISTS (
         SELECT 1 FROM kwave_entities a
          WHERE a.status='active' AND a.id <> e.id
            AND (
              lower(regexp_replace(btrim(a.canonical_ko), '[[:space:][:punct:]]+', '', 'g'))
                = lower(regexp_replace(btrim(e.canonical_ko), '[[:space:][:punct:]]+', '', 'g'))
              OR EXISTS (SELECT 1 FROM unnest(COALESCE(a.aliases_ko,'{}')) al
                          WHERE lower(regexp_replace(btrim(al), '[[:space:][:punct:]]+', '', 'g'))
                                = lower(regexp_replace(btrim(e.canonical_ko), '[[:space:][:punct:]]+', '', 'g')))
            )
       ),
       COALESCE(e.needs_disambig, false)
  FROM kwave_entities e
  JOIN kwave_entity_external_refs x ON x.entity_id = e.id AND x.provider = 'wikidata'
 WHERE e.status IN ('rejected','candidate') AND e.operator_locked = false
   AND e.entity_type::text NOT IN ('unknown','term')
   AND x.external_id ~ '^Q[0-9]+$'
   AND (
        -- ① 표시가 자기 사유를 부정하는 계열.
        COALESCE(e.notes,'') ~ '`+DeadOccupationScopeNotePattern+`'
        -- ② 죽은 범위 사유로 잡혀 있는 **등급 확정분**.
        --
        -- ★stepScopeReview 는 2026-09-15 에 이미 껐다. 그런데 그때 찍힌 표시는
        --   그대로 남아, 지금도 세 레인이 그 행을 **제외**한다 — itunes_drain ·
        --   discogs_drain · enrich/orchestrator 가 전부 NOT LIKE '%[scope:review]%'.
        --   레인을 끄는 것과 그 레인이 남긴 표시를 걷는 것은 다른 일이고,
        --   뒤쪽을 아무도 안 했다.
        --
        --   실측(2026-09-20): authoritative candidate 83건 · 앵커 80 · 중국어 61.
        --   사유는 전부 같은 명제다 — 류현진 "외국 스포츠 선수" · 정재승 "뇌과학자" ·
        --   전재수 "한국 정치인" · 김주형 "골프 선수".
        --
        -- ★등급이 authoritative 인 것만 본다. 표시를 걷는 근거는 «범위가 넓어졌다»가
        --   아니라 «이 행의 근거는 범위 판단과 무관하게 이미 섰다» 여야 한다.
        --   그리고 아래에서 **지금 위키데이터에 다시 물어** 한국 대상인지 확인한다 —
        --   홍화철("홍콩 배우")처럼 진짜 해외 대상은 거기서 걸린다.
        OR (e.status = 'candidate' AND e.verification_tier = 'authoritative'
            AND COALESCE(e.notes,'') ~ '`+ScopeRejectionNotePattern+`')
      )
   -- ★«이미 봤다» 표시로 영구히 막지 않는다 (2026-09-20, 첫 실행 뒤 발견).
   --
   --   되살림은 **두 걸음**이다: rejected → candidate → active. 첫 실행에서 68건이
   --   첫 걸음만 밟았는데, 처리 표시를 제외 조건으로 그대로 걸어 두어서
   --   **두 번째 걸음이 영원히 막혔다.** 류현진은 柳贤振 를 들고 candidate 에 누운 채
   --   confidence 0.000 으로 남았다 — 되살렸는데 여전히 안 나가는 상태다.
   --
   --   그래서 «더 갈 데가 없는» 것만 막는다. 승급까지 간 행은 status 가 active 라
   --   이 SELECT 에 애초에 안 걸리고, 승급을 못 하는 행에는 아래에서 :보류 를 찍는다.
   AND COALESCE(e.notes,'') NOT LIKE '%[occup-scope-restore:보류]%'
 ORDER BY e.updated_at DESC
 LIMIT $1`, limit)
	if err != nil {
		log.Printf("kdb.occup-scope: select: %v", err)
		return r
	}
	type row struct {
		id, ko, typ, qid, status, tier string
		twin, homonym                  bool
	}
	var items []row
	for rows.Next() {
		var it row
		if rows.Scan(&it.id, &it.ko, &it.typ, &it.qid, &it.status, &it.tier, &it.twin, &it.homonym) == nil {
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
		//
		// ★needs_disambig 는 candidate 에 둔다. 대상이 실존한다는 것과 **그 이름으로
		//   바로 서빙해도 된다**는 것은 다르다. 이 계열의 시작이 2026-07-31 김은정이었고
		//   (컬링 선수) 그 이름은 지금도 이 묶음 안에 있다. 되살리되, 어느 김은정인지는
		//   동명이인 경로가 정한다. 10건뿐이라 되살림이 막히는 양도 크지 않다.
		toActive := it.status == "candidate" && it.tier == "authoritative" && !mismatch && !it.homonym

		if it.homonym && it.status == "candidate" {
			r.HomonymHeld++
		}
		if len(r.Samples) < 40 {
			mark := "→candidate"
			if toActive {
				mark = "→active"
			}
			if it.homonym {
				mark += "(동명이인 보류)"
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

		// ★표시를 걷는다. 안 걷으면 되살려도 세 레인이 계속 제외한다.
		//   지우지 않고 **이름을 바꾼다** — 흔적 없이 지우면 다음에 읽는 사람이
		//   «왜 이 행만 표시가 없나»를 알 길이 없다. 레인의 NOT LIKE 는 정확히
		//   `[scope:review]` 를 보므로 이름이 바뀌면 더는 안 걸린다.
		note += " · [scope:review 해제] 사유가 0143 으로 소멸"

		if toActive {
			tag, uerr := pool.Exec(ctx, `
UPDATE kwave_entities
   SET status='active',
       confidence = GREATEST(confidence, 0.72::numeric),
       notes = replace(COALESCE(NULLIF(notes,'') || ' · ','') || $2,
                       '[scope:review]', '[scope:review-해제됨]'),
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
		// ★더 갈 데가 없으면 그렇다고 적는다. 이미 candidate 인데 승급 조건을 못 갖춘
		//   행은 다음 회차에 또 뽑혀 위키데이터를 다시 부를 뿐이다. rejected 였던 행은
		//   방금 candidate 가 됐고 등급이 서면 다음 회차에 승급할 수 있으므로 열어 둔다.
		if it.status == "candidate" {
			note += " · [occup-scope-restore:보류] 등급·앵커·동명이인 조건 미충족 — 승급은 평소 경로가 본다"
		}
		tag, uerr := pool.Exec(ctx, `
UPDATE kwave_entities
   SET status='candidate', updated_at=now(),
       notes = replace(COALESCE(NULLIF(notes,'') || ' · ','') || $2,
                       '[scope:review]', '[scope:review-해제됨]')
 WHERE id=$1 AND status IN ('rejected','candidate') AND operator_locked=false`, it.id, note)
		if uerr == nil && tag.RowsAffected() > 0 {
			r.Reopened++
		}
	}
	return r
}

// ─── 죽은 범위로 찍힌 검토 표시 걷기 ────────────────────────────────────────

// DeadScopeFlagResult — 한 번 돈 결과.
type DeadScopeFlagResult struct {
	Cleared int
	Samples []string
}

// DrainDeadScopeFlags — `[cand-evidence:review]` 중 **사유가 죽은 범위 명제인 것**의
// 표시를 걷는다. status 는 건드리지 않는다.
//
// ★왜 승급하지 않고 표시만 걷나. 그 표시의 뜻은 «판정기가 오염이라 봤다» 이고,
//
//	판정기의 프롬프트를 오늘 고쳤다(verify_evidence.go — 0143 범위). 그러니 할 일은
//	«내가 대신 판정하기»가 아니라 **고쳐진 판정기가 다시 볼 수 있게 열어 주기**다.
//	CandidateEvidencePass 가 이 표시가 있는 행을 선정에서 제외하므로, 걷지 않으면
//	프롬프트를 고쳐도 이 1,133행에는 영원히 닿지 않는다.
//
// ★오늘 같은 모양을 네 번째로 본다.
//
//	`[revert-term:reject]` · `[scope:review]` · `[occup-scope-restore]`(내가 만든 것) ·
//	그리고 이것. 레인을 고치는 것과 그 레인이 남긴 표시를 걷는 것은 **다른 일**이다.
//
// ★그리고 이 레인 자신이 다섯 번째가 됐다 (2026-09-20 저녁 실측).
//
//	첫 판에서 notes **전체**에 낱말 정규식을 걸었다. 그런데 판정기는 «일반명사다»를
//	죽은 명제와 같은 낱말로 쓴다 — "…일반 명사로서의 '무지개'를 다루고 있으며
//	K-콘텐츠와 무관". 그래서 396건을 걷었는데 그중 118건이 일반명사, 24건이 해외였고,
//	43건은 이미 «걷힘 → 재판정 → 똑같은 결론» 왕복을 마쳤다. 열어 준 값이 없는
//	재판정이라 LLM 호출만 나갔다. 같은 모양을 2026-07-25 에 `[adjudicated:claude]`
//	로 한 번 끊어 놓고도 다시 만들었다(candidate_evidence.go:71 주석).
//
//	고친 방식은 «더 좋은 정규식»이 아니다 — 가를 수 없는 것을 가르려 하지 않는다.
//	  ① 앞으로: 표시에 종류를 같이 적는다(`[rk:scope]` — ReasonKindTag).
//	  ② 옛 행: 그 표시가 붙인 **사유 구간만** 읽고, 살아 있는 명제가 섞였으면 뺀다.
//	  ③ 무엇보다: 일반명사 구간(kwave_kdb_common_nouns)에 등재된 낱말은 열지 않는다.
//
// ★기본 dry-run.
func DrainDeadScopeFlags(ctx context.Context, pool *pgxpool.Pool, limit int, dry bool) DeadScopeFlagResult {
	var r DeadScopeFlagResult
	if pool == nil || limit <= 0 {
		return r
	}
	rows, err := pool.Query(ctx, `
SELECT id::text, canonical_ko, entity_type::text
  FROM kwave_entities e
 WHERE status = 'candidate' AND operator_locked = false
   AND COALESCE(notes,'') LIKE '%[cand-evidence:review]%'
   -- ① 앞으로 찍히는 표시는 종류가 적혀 있다. 죽은 명제(scope)만 연다.
   --    다른 종류(common-noun·foreign·type-mismatch)는 지금도 유효하다.
   AND (CASE WHEN COALESCE(notes,'') ~ '\[rk:[a-z-]+\]'
             THEN COALESCE(notes,'') LIKE '%`+ReasonKindTag(ReasonKindScope)+`%'
   -- ② 종류 표시가 없는 옛 행은 **그 표시가 붙인 사유 구간만** 읽는다.
   --    notes 전체를 보면 다른 레인이 남긴 말에 걸린다(오늘 118건이 그렇게 걷혔다).
             ELSE `+DeadScopeInSegmentSQL("COALESCE(notes,'')", "[cand-evidence:review]")+`
        END)
   -- ③ 그리고 일반명사 구간에 등재된 낱말은 무슨 문구가 적혀 있든 열지 않는다.
   AND `+commonnoun.NotListedSQL("e.canonical_ko")+`
 ORDER BY updated_at DESC
 LIMIT $1`, limit)
	if err != nil {
		log.Printf("kdb.dead-scope-flag: select: %v", err)
		return r
	}
	type row struct{ id, ko, typ string }
	var items []row
	for rows.Next() {
		var it row
		if rows.Scan(&it.id, &it.ko, &it.typ) == nil {
			items = append(items, it)
		}
	}
	rows.Close()

	for _, it := range items {
		if len(r.Samples) < 40 {
			r.Samples = append(r.Samples, it.ko+"/"+it.typ)
		}
		if dry {
			r.Cleared++
			continue
		}
		// 지우지 않고 이름을 바꾼다 — 무엇이 걷혔는지 남아야 한다.
		note := ReopenNote(time.Now(), "[dead-scope-flag] 판정기 범위를 0143 으로 고쳤다 — 재판정 대상으로 연다")
		tag, uerr := pool.Exec(ctx, `
UPDATE kwave_entities
   SET notes = replace(COALESCE(NULLIF(notes,'') || ' · ','') || $2,
                       '[cand-evidence:review]', '[cand-evidence:review-해제됨]'),
       updated_at = now()
 WHERE id=$1 AND status='candidate' AND operator_locked=false`, it.id, note)
		if uerr == nil && tag.RowsAffected() > 0 {
			r.Cleared++
		}
	}
	return r
}

// ─── 기각 더미까지 손이 닿게 한다 ───────────────────────────────────────────

// ScopeRejectedResult — 한 번 돈 결과.
type ScopeRejectedResult struct {
	Checked, Reopened, Retyped int
	Samples                    []string
}

// DrainScopeRejectedTyped — **옛 범위로 rejected 된 행을 candidate 로 되돌린다.**
// 위키데이터 앵커를 요구하지 않는다.
//
// ★왜 또 만드나 (2026-09-20 실측). 되살리는 레인이 이미 셋 있는데 셋 다 이 더미에
//
//	닿지 못한다:
//	  DrainScopeReopen      위키데이터 앵커 JOIN 필수 → 앵커 없는 2,030행이 대상 밖
//	  RejudgeRejects        entity_type IN ('term','unknown') 제외 → 624행 영구 제외
//	  DrainDeadScopeFlags   status='candidate' 만 → rejected 는 아예 안 본다
//	  type_retrace          status IN ('active','candidate') 만
//
//	그래서 소비자가 지금 가장 많이 묻는 것들이 아무에게도 안 걸린 채 죽어 있다:
//	  서울대학교(7일 20건) · 더불어민주당(15) · SK하이닉스(14) · 연세대학교(14) ·
//	  한국사회복지저널(12) · 두산 베어스(11) · 국민의힘(11)
//
// ★근거는 **노트가 스스로 적은 종류**다(InScopeSubjectKind). "일반 교육기관(대학교)" ·
//
//	"반도체 제조 기업" · "한국의 정당" · "KBO 프로야구단" — 0143 이 명시적으로 넣은
//	종류들이다. 기각 사유가 그 대상을 범위 안이라고 적고 있다.
//
// ★그리고 미상 칸(term·unknown)은 그 종류로 **재유형화**한다. 안 하면 되살려도
//
//	cand-evidence 가 term 을 선정에서 빼기 때문에 판정 자체가 안 일어난다
//	(서울대학교·연세대학교가 정확히 그 상태였다).
//
// ★되살려도 active 로 올리지 않는다. candidate 로 돌려 **고쳐진 판정기가 뉴스 근거로**
//
//	보게 한다. 내가 대신 판정하지 않는다.
//
// ★일반명사 구간에 등재된 낱말은 열지 않는다. 요청 수요가 큰 것부터 본다.
//
// ★기본 dry-run.
func DrainScopeRejectedTyped(ctx context.Context, pool *pgxpool.Pool, limit int, dry bool) ScopeRejectedResult {
	var r ScopeRejectedResult
	if pool == nil || limit <= 0 {
		return r
	}
	rows, err := pool.Query(ctx, `
SELECT e.id::text, e.canonical_ko, e.entity_type::text, COALESCE(e.notes,''),
       COALESCE(d.n, 0) AS demand
  FROM kwave_entities e
  LEFT JOIN (SELECT term_ko, count(*) n FROM kwave_kdb_request_terms
              WHERE created_at > now() - interval '30 days' GROUP BY 1) d
         ON d.term_ko = e.canonical_ko
 WHERE e.status = 'rejected' AND e.operator_locked = false
   AND COALESCE(e.notes,'') ~ '`+ScopeRejectionNotePattern+`'
   -- 해외 대상은 범위가 넓어져도 그대로 밖이다.
   AND COALESCE(e.notes,'') !~ '`+ForeignSubjectNotePattern+`'
   -- 노트가 **구체적인 0143 종류**를 이름 붙였을 때만. «일반어» 라는 말만 있는 것은
   -- 열지 않는다 — 옛 판정기는 학교도 «일반어»라고 적었으므로 그 낱말은 근거가
   -- 되지 못하고, 구체적 종류가 그보다 특정적이다.
   AND COALESCE(e.notes,'') ~ '`+InScopeSubjectNotePattern+`'
   AND `+commonnoun.NotListedSQL("e.canonical_ko")+`
   -- 병합·중복은 건드리지 않는다. 같은 이름이 이미 살아 있으면 열 이유가 없다.
   AND COALESCE(e.notes,'') NOT LIKE '%merged into%'
   AND NOT EXISTS (SELECT 1 FROM kwave_entities a
                    WHERE a.canonical_ko = e.canonical_ko AND a.id <> e.id
                      AND a.status IN ('active','candidate'))
   -- 한 번 열었으면 다시 열지 않는다(왕복 금지 — 오늘 그 왕복을 보고 배웠다).
   AND COALESCE(e.notes,'') NOT LIKE '%[scope-rejected-reopen]%'
 ORDER BY demand DESC, e.updated_at DESC
 LIMIT $1`, limit)
	if err != nil {
		log.Printf("kdb.scope-rejected: select: %v", err)
		return r
	}
	type row struct {
		id, ko, typ, notes string
		demand             int
	}
	var items []row
	for rows.Next() {
		var it row
		if rows.Scan(&it.id, &it.ko, &it.typ, &it.notes, &it.demand) == nil {
			items = append(items, it)
		}
	}
	rows.Close()

	for _, it := range items {
		r.Checked++
		kind, ok := InScopeSubjectKind(it.notes)
		if !ok {
			continue // SQL 과 Go 의 판정이 갈리면 열지 않는다(보수적).
		}
		retype := (it.typ == "term" || it.typ == "unknown")
		if len(r.Samples) < 40 {
			s := it.ko + "/" + it.typ + " 요청" + itoaSample(it.demand)
			if retype {
				s += " →" + kind
			}
			r.Samples = append(r.Samples, s)
		}
		if dry {
			r.Reopened++
			if retype {
				r.Retyped++
			}
			continue
		}
		note := ReopenNote(time.Now(), "[scope-rejected-reopen] 사유가 «"+kind+
			"»라고 적혀 있다 — 0143 범위 안. 판정기가 다시 본다")
		var tag int64
		if retype {
			// 재유형화는 되돌릴 수 있게 남긴다.
			_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_dataqa_log (entity_id, locale, old_value, old_source, verdict, reason, model)
VALUES ($1::uuid, 'entity_type', $2, '', 'scope-rejected-retype', $3, 'rule')`,
				it.id, it.typ, "노트가 이름 붙인 종류: "+kind)
			t, uerr := pool.Exec(ctx, `
UPDATE kwave_entities
   SET status='candidate', entity_type=$3::kwave_entity_type,
       notes = COALESCE(NULLIF(notes,'') || ' · ','') || $2, updated_at=now()
 WHERE id=$1 AND status='rejected' AND operator_locked=false`, it.id, note, kind)
			if uerr != nil {
				log.Printf("kdb.scope-rejected: %s 재유형화 실패: %v", it.ko, uerr)
				continue
			}
			tag = t.RowsAffected()
			if tag > 0 {
				r.Retyped++
			}
		} else {
			t, uerr := pool.Exec(ctx, `
UPDATE kwave_entities
   SET status='candidate',
       notes = COALESCE(NULLIF(notes,'') || ' · ','') || $2, updated_at=now()
 WHERE id=$1 AND status='rejected' AND operator_locked=false`, it.id, note)
			if uerr != nil {
				log.Printf("kdb.scope-rejected: %s 되살림 실패: %v", it.ko, uerr)
				continue
			}
			tag = t.RowsAffected()
		}
		if tag > 0 {
			r.Reopened++
		}
	}
	return r
}

// itoaSample — 표본 문자열용(작은 수). strconv 를 이 파일에 들이지 않는다.
func itoaSample(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// ─── 값을 갖고도 못 나가는 행 ────────────────────────────────────────────────

// ValuedButDeadResult — 한 번 돈 결과.
type ValuedButDeadResult struct {
	Checked, Reopened int
	Samples           []string
}

// DrainValuedButDead — **표기를 이미 갖고 있는데 서빙되지 않는 행**을 candidate 로 연다.
//
// ★실측 (2026-09-21). 값 2개 이상을 가진 비활성 행이 1,235개이고, 그중 최근 30일 요청이
//
//	있는 것이 308개다. 어제 상태 문서가 쓴 말이 그대로다 — «답을 갖고도 안 내보낸다».
//
// ★그런데 308 을 손실로 보고하면 **과대계상**이다. 사유로 가르니:
//
//	145  별칭으로 해소됨(병합 대상이 active·별칭 보유) — 무해
//	 33  동명 active 행이 따로 있다 — 무해
//	 57  ★옛 범위 사유로 죽었다 — 이 레인의 대상
//	 35  TTL 만료(설계상 재요청 시 재발굴)
//	 22  동명이인 — 실존 확인과 «그 이름으로 서빙해도 된다»는 다르다
//	  8  병합했는데 별칭을 안 옮겼다 — 다른 결함(여기서 손대지 않는다)
//
//	이 레인은 **57건만** 본다. 값이 있다는 것은 «어느 출처가 이미 답을 냈다»는 뜻이고,
//	사유가 죽은 범위면 그 답을 막을 이유가 없다.
//
// ★그래도 active 로 올리지 않는다. candidate 로 열어 판정기가 뉴스 근거로 보게 한다.
func DrainValuedButDead(ctx context.Context, pool *pgxpool.Pool, limit int, dry bool) ValuedButDeadResult {
	var r ValuedButDeadResult
	if pool == nil || limit <= 0 {
		return r
	}
	rows, err := pool.Query(ctx, `
SELECT e.id::text, e.canonical_ko, e.entity_type::text, COALESCE(d.n,0),
       COALESCE(NULLIF(e.canonical_en,''),'-'), COALESCE(NULLIF(e.canonical_zh,''),'-')
  FROM kwave_entities e
  JOIN (SELECT term_ko, count(*) n FROM kwave_kdb_request_terms
         WHERE created_at > now() - interval '30 days' GROUP BY 1) d
    ON d.term_ko = e.canonical_ko
 WHERE e.status = 'rejected' AND e.operator_locked = false
   -- 표기를 둘 이상 이미 갖고 있다 = 어느 출처가 답을 냈다.
   AND (CASE WHEN COALESCE(e.canonical_en,'') <> '' THEN 1 ELSE 0 END
      + CASE WHEN COALESCE(e.canonical_ja,'') <> '' THEN 1 ELSE 0 END
      + CASE WHEN COALESCE(e.canonical_zh,'') <> '' THEN 1 ELSE 0 END) >= 2
   -- 사유가 죽은 범위 명제여야 한다. 살아 있는 명제는 건드리지 않는다.
   AND COALESCE(e.notes,'') ~ '`+ScopeRejectionNotePattern+`'
   AND COALESCE(e.notes,'') !~ '`+ForeignSubjectNotePattern+`'
   AND COALESCE(e.notes,'') !~ '`+CommonNounNotePattern+`'
   -- 병합·TTL·동명이인은 각각 다른 명제다. 여기서 섞지 않는다.
   AND COALESCE(e.notes,'') NOT LIKE '%merged into%'
   AND COALESCE(e.notes,'') NOT LIKE '%[ttl-expire:reject]%'
   AND COALESCE(e.notes,'') NOT LIKE '%동명이인%'
   AND COALESCE(e.notes,'') NOT LIKE '%[valued-but-dead]%'
   AND `+commonNounNotListedE+`
   -- 같은 이름이 살아 있으면(정본이든 별칭이든) 소비자는 이미 답을 받는다.
   AND NOT EXISTS (SELECT 1 FROM kwave_entities a
                    WHERE a.status='active'
                      AND (a.canonical_ko = e.canonical_ko OR e.canonical_ko = ANY(a.aliases_ko)))
 ORDER BY d.n DESC, e.updated_at DESC
 LIMIT $1`, limit)
	if err != nil {
		log.Printf("kdb.valued-but-dead: select: %v", err)
		return r
	}
	type row struct {
		id, ko, typ, en, zh string
		demand              int
	}
	var items []row
	for rows.Next() {
		var it row
		if rows.Scan(&it.id, &it.ko, &it.typ, &it.demand, &it.en, &it.zh) == nil {
			items = append(items, it)
		}
	}
	rows.Close()

	for _, it := range items {
		r.Checked++
		if len(r.Samples) < 40 {
			r.Samples = append(r.Samples, it.ko+"/"+it.typ+" 요청"+itoaSample(it.demand)+
				" en="+it.en+" zh="+it.zh)
		}
		if dry {
			r.Reopened++
			continue
		}
		note := ReopenNote(time.Now(), "[valued-but-dead] 표기를 이미 갖고 있고 소비자가 묻는다 — "+
			"사유는 죽은 범위다. 판정기가 다시 본다")
		tag, uerr := pool.Exec(ctx, `
UPDATE kwave_entities
   SET status='candidate',
       notes = COALESCE(NULLIF(notes,'') || ' · ','') || $2, updated_at=now()
 WHERE id=$1 AND status='rejected' AND operator_locked=false`, it.id, note)
		if uerr != nil {
			log.Printf("kdb.valued-but-dead: %s 되살림 실패: %v", it.ko, uerr)
			continue
		}
		if tag.RowsAffected() > 0 {
			r.Reopened++
			log.Printf("kdb.valued-but-dead: %s (%s) 요청%d en=%q zh=%q", it.ko, it.typ, it.demand, it.en, it.zh)
		}
	}
	return r
}

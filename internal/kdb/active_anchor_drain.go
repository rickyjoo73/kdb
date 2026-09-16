package kdb

// active_anchor_drain — **이미 서빙 중인 행에 앵커를 붙인다.**
//
// ★왜 필요한가 (실측 2026-09-16 저녁).
//
//	앵커 없는 active 5,137건. 그중 3,819건(74%)이 기계번역 표기를 내보내고 있고,
//	4,206건(82%)은 **앵커를 한 번도 찾아본 적이 없다**.
//
//	앵커가 있고 없고가 표기의 질을 가른다:
//
//	      앵커 있음  8,402행 · 기계번역 16%
//	      앵커 없음  5,140행 · 기계번역 74%
//
//	앵커가 붙으면 위키데이터 라벨(prio 5)이 gtranslate(prio 8)를 자동으로 밀어낸다.
//	오늘 아침 새 유형에서 이미 봤다 — government_body 3→18, 그 18건에 en/ja/vi 가
//	각각 14건씩 따라왔다.
//
// ★왜 지금까지 안 됐나. 앵커를 붙이는 레인이 여럿인데 **전부 `status='candidate'`
//
//	만 본다**(org/kowiki/wdperson/tmdb/mbgroup/itunes). 한번 active 가 되면 대상에서
//	빠진다 — "서빙되고 있으니 됐다"가 되어, 기계가 지어낸 이름이 영구히 남는다.
//	고쳐야 할 것은 판정이 아니라 **누구를 보는가** 하나였다.
//
// ★그래서 판정을 새로 만들지 않는다. findAnchorQID 를 후보 레인과 **공유**한다.
//
//	이름 일치 · P31 유형 일치 · P17/P495 국가 · 이름항목 배제 — 네 관문 그대로다.
//	한쪽만 무르게 하면 같은 낱말이 경로에 따라 다른 답을 받는다.
//
// ★다른 점은 **쓰는 것**뿐이다.
//
//	후보 레인 : 앵커를 붙이고 status 를 active 로 올린다.
//	이 레인   : 앵커만 붙인다. status 는 건드리지 않는다 — 이미 active 다.
//
// ★active 는 candidate 보다 **더 조심해야 한다.**
//
//	지금 나가는 값이 «무해한 추측»이라면, 앵커를 잘못 붙였을 때 «확신에 찬 남의
//	이름»으로 바뀐다. 오늘 본 이재명→Q6514101(1991년생 축구선수)이 그 모양이다.
//	그래서 기본은 dry-run 이고, 한국 근거가 없으면(orgAnchorHold) 아무것도 쓰지 않는다.
//
// `kdb-app active-anchor [n] [go]`.

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
)

// ActiveAnchorTypes — 이 레인이 보는 유형.
//
// ★제 레인이 있는 유형은 넣지 않는다 — person(wdperson) · group/song_album
//
//	(musicbrainz·itunes) · movie/drama(tmdb·kofic·kmdb). 그쪽이 쓰는 카탈로그가
//	이름검색보다 정확하고, 두 레인이 같은 행을 두고 다투면 앵커가 오간다.
//
// ★character 도 넣지 않는다. 오늘 앵커 감사에서 human-on-character 가 139건 나왔다 —
//
//	캐릭터 이름으로 이름검색을 하면 그 배우가 잡히는 계열이다. 위험 대비 이득이 낮다.
//
// 남는 것이 이 레인의 몫이다. 실측 잔량: show 561 · event_tour 482 · agency 285 ·
// channel_outlet 214 · brand_place 187 · organization·government_body·company·school…
var ActiveAnchorTypes = []string{
	"political_party", "government_body", "company", "organization",
	"sports_team", "school", "game", "musical_play", "webtoon", "publication",
	"agency", "channel_outlet", "event_tour", "show", "brand_place",
}

// ActiveAnchorResult — 한 번 돈 결과.
type ActiveAnchorResult struct {
	Checked, Anchored int
	QIDTaken, WriteFailed int
	SearchFailed, NoHit   int
	NameMismatch, TypeMismatch, TypeUnknown, Foreign, NameElement, Held int
	Samples []string
}

// DrainActiveAnchors — 앵커 없는 active 행에 위키데이터 앵커를 붙인다. 승급은 하지 않는다.
func DrainActiveAnchors(ctx context.Context, pool *pgxpool.Pool, cl *wikidata.Client, limit int, dry bool) ActiveAnchorResult {
	var r ActiveAnchorResult
	if pool == nil || cl == nil || limit <= 0 {
		return r
	}
	// ★기계번역을 내보내는 행을 **먼저** 본다. 같은 일을 해도 그쪽이 바뀌는 값이 크다.
	//   출처 있는 값을 이미 내보내는 행은 앵커가 붙어도 표기가 안 바뀐다.
	rows, err := pool.Query(ctx, `
SELECT e.id::text, e.canonical_ko, e.entity_type::text
  FROM kwave_entities e
 WHERE e.status = 'active'
   AND e.operator_locked = false
   AND e.entity_type::text = ANY($2)
   AND COALESCE(e.canonical_ko,'') <> ''
   AND char_length(e.canonical_ko) BETWEEN 2 AND 40
   AND NOT EXISTS (SELECT 1 FROM kwave_entity_external_refs x
                    WHERE x.entity_id = e.id AND COALESCE(x.external_id,'') <> '')
   AND NOT EXISTS (SELECT 1 FROM kwave_kdb_enrich_attempts a
                    WHERE a.entity_id = e.id AND a.field = 'wdactive'
                      AND a.last_attempt_at > now() - interval '30 days')
 ORDER BY (e.canonical_en_source IN ('gtranslate','gtranslate-raw','codex-fallback')) DESC,
          e.updated_at DESC
 LIMIT $1`, limit, ActiveAnchorTypes)
	if err != nil {
		log.Printf("kdb.active-anchor: select: %v", err)
		return r
	}
	type item struct{ id, ko, typ string }
	var items []item
	for rows.Next() {
		var it item
		if rows.Scan(&it.id, &it.ko, &it.typ) == nil {
			items = append(items, it)
		}
	}
	rows.Close()

	for _, it := range items {
		r.Checked++
		if !dry {
			// 쿨다운은 결과와 무관하게 먼저 적는다. 후보 레인과 **다른 칸**(wdactive)을
			// 쓴다 — 같은 칸을 쓰면 한쪽이 본 것을 다른 쪽이 본 것으로 착각한다.
			_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_enrich_attempts (entity_id, field, attempts, last_attempt_at, last_source)
VALUES ($1,'wdactive',1,now(),'wikidata')
ON CONFLICT (entity_id, field) DO UPDATE
   SET attempts = kwave_kdb_enrich_attempts.attempts + 1, last_attempt_at = now()`, it.id)
		}

		qid, detail, why := findAnchorQID(ctx, cl, it.ko, it.typ)
		switch why {
		case "search-failed":
			r.SearchFailed++
			log.Printf("  [검색실패] %-22s [%s]", it.ko, it.typ)
			continue
		case "no-hit":
			r.NoHit++
			continue
		case "hold":
			// 이름도 유형도 맞는데 한국 근거가 없다. **쓰지 않는다** — 후보 레인이
			// 이 자리에서 5건 중 4건을 틀렸다(공군=개념, 레 미제라블=프랑스).
			r.Held++
			log.Printf("  [보류·미기록] %-22s [%s] → %s  한국 근거 없음", it.ko, it.typ, qid)
			continue
		case "name-element":
			r.NameElement++
			continue
		case "type-mismatch":
			r.TypeMismatch++
			continue
		case "type-unknown":
			r.TypeUnknown++
			continue
		case "foreign":
			r.Foreign++
			continue
		case "name-mismatch":
			r.NameMismatch++
			continue
		}
		// 여기 오면 qid 는 네 관문을 다 통과했다.
		var taken bool
		_ = pool.QueryRow(ctx, `
SELECT EXISTS (SELECT 1 FROM kwave_entity_external_refs
                WHERE provider='wikidata' AND external_id=$1 AND entity_id <> $2)`,
			qid, it.id).Scan(&taken)
		if taken {
			r.QIDTaken++
			log.Printf("  [중복QID] %-22s [%s] → %s 를 다른 행이 이미 쓴다(병합 대상)", it.ko, it.typ, qid)
			continue
		}
		if len(r.Samples) < 60 {
			r.Samples = append(r.Samples, it.ko+"["+it.typ+"]→"+qid)
		}
		if dry {
			r.Anchored++
			log.Printf("  [앵커] %-22s [%s] → %s  %s", it.ko, it.typ, qid, detail)
			continue
		}
		if _, err := pool.Exec(ctx, `
INSERT INTO kwave_entity_external_refs (entity_id, provider, external_id, url, confidence, fetched_at)
VALUES ($1,'wikidata',$2,$3,0.75,now())
ON CONFLICT DO NOTHING`, it.id, qid, "https://www.wikidata.org/wiki/"+qid); err != nil {
			// ★한 일만 적는다. 저장 실패를 성공으로 로그하면 다음 판단이 전부 틀린
			//   전제 위에 선다 — 오늘 두 번 고친 계열이다.
			r.WriteFailed++
			log.Printf("  [저장실패] %-22s [%s] → %s: %v", it.ko, it.typ, qid, err)
			continue
		}
		r.Anchored++
		log.Printf("  [앵커] %-22s [%s] → %s  %s", it.ko, it.typ, qid, detail)
		// ★status 도 tier 도 건드리지 않는다. 이 행은 이미 active 이고, 앵커가 붙었다는
		//   것과 표기가 검증됐다는 것은 다르다. 표기는 wikidata_locale_drain 이
		//   이 앵커를 보고 채운다 — 그게 이 레인의 목적이다.
		_, _ = pool.Exec(ctx, `
UPDATE kwave_entities
   SET notes = COALESCE(NULLIF(notes,'') || ' · ','') || $2, updated_at = now()
 WHERE id = $1`, it.id, fmt.Sprintf("[active-anchor] 위키데이터 %s (%s)", qid, detail))
	}
	return r
}

// ActiveAnchorSummary — 로그 한 줄.
func (r ActiveAnchorResult) Summary() string {
	return fmt.Sprintf("조회 %d · 앵커 %d · 보류 %d · 중복QID %d · 저장실패 %d | 이름불일치 %d · 유형어긋남 %d · 유형미상 %d · 해외 %d · 이름항목 %d · 검색실패 %d · 없음 %d",
		r.Checked, r.Anchored, r.Held, r.QIDTaken, r.WriteFailed,
		r.NameMismatch, r.TypeMismatch, r.TypeUnknown, r.Foreign, r.NameElement, r.SearchFailed, r.NoHit)
}

var _ = strings.TrimSpace

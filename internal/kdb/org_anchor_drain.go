package kdb

// org_anchor_drain — **새 유형(정당·기관·기업·단체·구단·학교·게임·뮤지컬·웹툰·출판)의
// 후보에 위키데이터 앵커를 붙인다.**
//
// ★왜 이것이 먼저인가 (실측 2026-09-16).
//
//	새 유형 candidate 103건 · 위키데이터 앵커 보유 **0건**
//	  organization 42 · government_body 24 · school 16 · company 19 · sports_team 7
//	  publication 7 · musical_play 4 · political_party 1 · game 1
//
//	앵커가 없으면 승급이 안 되고, 승급이 안 되면 다국어가 안 채워진다
//	(wikidata_locale_drain 은 `status='active' AND wikidata ref` 를 본다).
//	그래서 정당·기관·기업의 ja/zh/vi 가 **전부 0** 이다. 유형 칸만 늘리고
//	채우는 레인을 안 만들면 빈칸만 늘어난다 — 0143·0146 에서 칸을 만들고
//	0148 까지 와서야 사람이 쓰는 화면에 그 유형이 보였던 것과 같은 실수다.
//
//	고용노동부·서울대학교·대한축구협회·NC 다이노스는 위키데이터에 있다.
//	우리가 찾으러 가지 않았을 뿐이다.
//
// ★person 레인을 그대로 못 쓰는 이유가 둘이다.
//
//	① DrainWikidataPersonCandidates 는 `entity_type='person'` 으로 못박혀 있다.
//	② 그쪽 관문은 **description 문자열**(IsKWaveDescription)이다. 사람은 설명이
//	   "South Korean singer" 처럼 국적을 품지만, 조직은 "국가기록원"처럼 한국어
//	   설명만 있거나 설명이 아예 비어 있는 것이 흔하다. 문자열이 없다고 한국이
//	   아닌 게 아니다 — 그 관문을 그대로 쓰면 멀쩡한 기관이 «근거 없음»이 된다.
//
// ★대신 이 레인은 **더 센 관문 둘**을 쓴다. 사람에겐 없던 것들이다.
//
//	P31 유형 일치 — AnchorTypeAllowed. 감사·분류가 보는 그 표를 그대로 본다.
//	                 "더불어민주당"이 political_party 로 들어왔는데 위키데이터가
//	                 Q7278(정당)이라 말하면, 그건 이름이 같은 다른 것일 수 없다.
//	P17/P495 국가 — 조직엔 P17, 창작물엔 P495 가 붙는다. "한국 것인가"에 직접 답한다.
//
//	person 레인이 이름검색 승급에서 동명이인에 데였던 것(이정후→동명 배우)은
//	사람 이름이 흔하기 때문이다. 유형이 맞물린 조직명은 그렇지 않다.
//
// ★그래도 **모르는 것으로 승급하지 않는다**(D-37).
//
//	P31 이 우리 표에 없으면   → 판정하지 않고 지나간다 (승급도 앵커 저장도 안 한다)
//	P17 이 한국이 아니라 하면 → 앵커도 안 붙인다. 레 미제라블(P495=프랑스)은
//	                            그 QID 가 맞더라도 이 레인이 결정할 일이 아니다.
//	P17 이 아예 없으면        → 앵커는 붙이되 **승급은 안 한다**. QID 는 맞고
//	                            한국 여부만 모르는 상태다 — 운영자가 볼 근거를 남긴다.
//
// ★기본 dry-run. `kdb-app org-anchor [n] [go]`.

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
)

// OrgAnchorTypes — 이 레인이 보는 유형. 0143·0146 으로 늘어난 것들이다.
// person·group·drama 처럼 이미 제 레인이 있는 유형은 넣지 않는다.
var OrgAnchorTypes = []string{
	"political_party", "government_body", "company", "organization",
	"sports_team", "school", "game", "musical_play", "webtoon", "publication",
}

// OrgAnchorResult — 한 번 돈 결과. **왜 못 붙였는지**를 따로 센다.
// 뭉뚱그린 "0건"은 "위키데이터에 없다"와 "우리 표에 없다"를 구분 못 하는데,
// 그 둘은 다음에 할 일이 완전히 다르다.
type OrgAnchorResult struct {
	Checked  int // 조회한 후보 수
	Anchored int // 앵커를 붙인 수
	Promoted int // active 로 올린 수 (Anchored 의 부분집합)
	Held     int // 앵커는 붙였으나 한국 근거가 없어 candidate 로 둔 수

	NoHit        int // 위키데이터에 그 이름이 **없다**
	SearchFailed int // 검색을 **못 했다**(망·TLS·API 오류). 없는 것과 전혀 다르다.
	NameMismatch int // 검색은 됐으나 이름이 일치하는 항목이 없다
	TypeMismatch int // 이름은 맞는데 P31 이 우리 유형과 다르다
	TypeUnknown  int // P31 이 우리 표에 없다 — 판정하지 않는다
	Foreign      int // P17/P495 가 한국이 아니라고 말한다
	NameElement  int // "이름 그 자체" 항목

	Samples []string
}

// orgAnchorDecision — 한 후보 항목에 대한 판정.
type orgAnchorDecision int

const (
	orgAnchorSkip    orgAnchorDecision = iota // 이 항목은 아니다 — 다음 검색 결과를 본다
	orgAnchorHold                             // 앵커는 맞다. 한국 여부를 몰라 승급은 안 한다
	orgAnchorPromote                          // 앵커도 맞고 한국 근거도 있다
)

// orgAnchorVerdict — **순수 함수.** 네트워크도 DB 도 없다.
//
// 반환은 (판정, 사유코드, 사람이 읽을 근거). 사유코드는 집계용이고 근거는 notes 에 남는다.
func orgAnchorVerdict(ko, entityType string, ent *wikidata.Entity) (orgAnchorDecision, string, string) {
	if ent == nil {
		return orgAnchorSkip, "fetch-failed", ""
	}
	// ① "이름 그 자체" 항목은 실재의 근거가 아니다. person 레인이 이 경로로 87건 오염됐다.
	if isName, cls := ent.IsNameElement(); isName {
		return orgAnchorSkip, "name-element", "P31=" + cls
	}
	// ② 이름이 실제로 같아야 한다. 판정은 SearchAndFetch 와 **같은 함수**를 쓴다.
	if !wikidata.EntityMatchesQuery(ko, ent) {
		return orgAnchorSkip, "name-mismatch", ""
	}
	// ③ P31 이 우리 유형을 허용해야 한다. 감사·분류가 보는 그 표다.
	allowed, known := false, false
	for _, q := range ent.InstanceOf {
		ok, k := AnchorTypeAllowed(q, entityType)
		if k {
			known = true
		}
		if ok {
			allowed = true
			break
		}
	}
	switch {
	case allowed:
		// 통과
	case known:
		return orgAnchorSkip, "type-mismatch", "P31=" + strings.Join(ent.InstanceOf, ",")
	default:
		// ★모르는 것으로 승급하지 않는다(D-37). 표에 없는 P31 은 **틀렸다는 뜻이 아니라
		//   우리가 아직 안 적었다는 뜻**이다. 그대로 두고 로그로 알린다 — 표를 늘릴
		//   근거가 여기서 나온다.
		return orgAnchorSkip, "type-unknown", "P31=" + strings.Join(ent.InstanceOf, ",")
	}
	// ④ 한국 근거. 세 갈래다.
	if ent.IsSouthKorean() {
		return orgAnchorPromote, "country-kr", "P17/P495=Q884"
	}
	if len(ent.CountryQIDs) > 0 {
		// 국가가 **적혀 있는데 한국이 아니다.** 이건 "모른다"가 아니라 "아니다"다.
		return orgAnchorSkip, "foreign", "P17/P495=" + strings.Join(ent.CountryQIDs, ",")
	}
	// 국가가 아예 없다. 설명문이 대신 말해 주면 그것으로 인정한다.
	for _, loc := range []string{"en", "ko"} {
		if d := ent.Descriptions[loc]; wikidata.IsKWaveDescription(d) {
			return orgAnchorPromote, "country-desc", "desc=" + d
		}
	}
	// QID 는 맞다. 한국 여부만 모른다 — 앵커는 남기고 승급은 안 한다.
	return orgAnchorHold, "country-unknown", ""
}

// DrainOrgAnchors — 새 유형 candidate 에 위키데이터 앵커를 붙이고, 한국 근거가 있으면 승급한다.
func DrainOrgAnchors(ctx context.Context, pool *pgxpool.Pool, cl *wikidata.Client, limit int, dry bool) OrgAnchorResult {
	var r OrgAnchorResult
	if pool == nil || cl == nil || limit <= 0 {
		return r
	}
	rows, err := pool.Query(ctx, `
SELECT e.id::text, e.canonical_ko, e.entity_type::text
  FROM kwave_entities e
 WHERE e.status = 'candidate'
   AND e.operator_locked = false
   AND e.entity_type::text = ANY($2)
   AND COALESCE(e.canonical_ko,'') <> ''
   AND char_length(e.canonical_ko) BETWEEN 2 AND 40
   AND NOT EXISTS (SELECT 1 FROM kwave_entity_external_refs x
                    WHERE x.entity_id = e.id AND x.provider = 'wikidata')
   AND NOT EXISTS (SELECT 1 FROM kwave_kdb_enrich_attempts a
                    WHERE a.entity_id = e.id AND a.field = 'wdorg'
                      AND a.last_attempt_at > now() - interval '30 days')
 ORDER BY e.updated_at DESC
 LIMIT $1`, limit, OrgAnchorTypes)
	if err != nil {
		log.Printf("kdb.org-anchor: select: %v", err)
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
			// 쿨다운은 **결과와 무관하게** 먼저 적는다. 무매칭을 30일마다 한 번만 다시 본다.
			_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_enrich_attempts (entity_id, field, attempts, last_attempt_at, last_source)
VALUES ($1,'wdorg',1,now(),'wikidata')
ON CONFLICT (entity_id, field) DO UPDATE
   SET attempts = kwave_kdb_enrich_attempts.attempts + 1, last_attempt_at = now()`, it.id)
		}
		// filterKWave=false — 조직 설명엔 국적 문자열이 없는 게 흔하다. 대신 아래에서
		// P31·P17 로 더 세게 거른다.
		cands, serr := cl.Search(ctx, it.ko, "ko", 5, false)
		time.Sleep(300 * time.Millisecond) // 위키데이터 예의
		// ★오류와 "없음"을 갈라 센다 (2026-09-16).
		//   처음엔 `serr != nil || len(cands) == 0` 을 한 줄로 묶었다. 그래서 첫
		//   dry-run 이 **서울대학교·고용노동부·FC서울·쿠팡을 포함해 120건 전부**
		//   «검색없음» 으로 보고했다. 실제로는 컨테이너에 CA 인증서가 없어 한 번도
		//   위키데이터에 닿지 못한 것이었다.
		//
		//   이 저장소가 이미 데인 계열이다(4e14f6f "네 곳이 전부 조용한 0건을
		//   성공으로 로그하고 있었다"). 못 한 것을 없다고 적으면, 그 다음에 하는
		//   판단이 전부 틀린 전제 위에 선다 — "위키데이터에 없으니 다른 출처를
		//   붙이자"는 결론까지 갔을 것이다.
		if serr != nil {
			r.SearchFailed++
			log.Printf("  [검색실패] %-22s [%s] %v", it.ko, it.typ, serr)
			continue
		}
		if len(cands) == 0 {
			r.NoHit++
			log.Printf("  [없음] %-22s [%s]", it.ko, it.typ)
			continue
		}
		decided := false
		lastWhy := ""
		for i, cand := range cands {
			if i >= 3 || strings.TrimSpace(cand.QID) == "" {
				break
			}
			ent, ferr := cl.Fetch(ctx, cand.QID)
			time.Sleep(250 * time.Millisecond)
			if ferr != nil {
				continue
			}
			dec, why, detail := orgAnchorVerdict(it.ko, it.typ, ent)
			lastWhy = why
			if dec == orgAnchorSkip {
				continue
			}
			decided = true
			note := fmt.Sprintf("[org-anchor] 위키데이터 %s 가 %s 유형과 일치(%s)", cand.QID, it.typ, detail)
			log.Printf("  [%s] %-22s [%s] → %s  %s", map[orgAnchorDecision]string{
				orgAnchorPromote: "승급", orgAnchorHold: "앵커만",
			}[dec], it.ko, it.typ, cand.QID, detail)
			if len(r.Samples) < 60 {
				r.Samples = append(r.Samples, it.ko+"["+it.typ+"]→"+cand.QID+" "+why)
			}
			if dry {
				r.Anchored++
				if dec == orgAnchorPromote {
					r.Promoted++
				} else {
					r.Held++
				}
				break
			}
			if _, err := pool.Exec(ctx, `
INSERT INTO kwave_entity_external_refs (entity_id, provider, external_id, url, confidence, raw_payload, fetched_at)
VALUES ($1,'wikidata',$2,$3,$4,$5,now())
ON CONFLICT DO NOTHING`, it.id, cand.QID,
				"https://www.wikidata.org/wiki/"+cand.QID,
				anchorConfidence(dec),
				fmt.Sprintf(`{"label":%q,"description":%q}`, cand.Label, cand.Description)); err != nil {
				log.Printf("  [보류] %s 앵커 저장 실패: %v", it.ko, err)
				break
			}
			r.Anchored++
			if dec != orgAnchorPromote {
				// 앵커만 남기고 candidate 로 둔다. 시계는 다시 시작시킨다 — 안 그러면
				// 근거를 막 붙인 행이 TTL 에 걸려 그대로 죽는다(2026-09-16 에 겪었다).
				_, _ = pool.Exec(ctx, `
UPDATE kwave_entities
   SET updated_at = now(),
       notes = COALESCE(NULLIF(notes,'') || ' · ','') || $2
 WHERE id = $1 AND status = 'candidate' AND operator_locked = false`,
					it.id, ReopenNote(time.Now(), note+" — 한국 근거를 못 찾아 후보로 둔다"))
				r.Held++
				break
			}
			tag, _ := pool.Exec(ctx, `
UPDATE kwave_entities
   SET status = 'active', confidence = GREATEST(confidence, 0.75), updated_at = now(),
       notes = COALESCE(NULLIF(notes,'') || ' · ','') || $2
 WHERE id = $1 AND status = 'candidate' AND operator_locked = false`, it.id, note+" 승급")
			if tag.RowsAffected() > 0 {
				r.Promoted++
			}
			break
		}
		if decided {
			continue
		}
		switch lastWhy {
		case "name-element":
			r.NameElement++
		case "type-mismatch":
			r.TypeMismatch++
		case "type-unknown":
			r.TypeUnknown++
		case "foreign":
			r.Foreign++
		default:
			r.NameMismatch++
		}
		log.Printf("  [건너뜀:%s] %-22s [%s]", lastWhy, it.ko, it.typ)
	}
	return r
}

// anchorConfidence — 승급까지 간 앵커와 보류 앵커는 확신도가 다르다. 같은 값을 주면
// 나중에 둘을 가릴 수 없다.
func anchorConfidence(d orgAnchorDecision) float64 {
	if d == orgAnchorPromote {
		return 0.75
	}
	return 0.55
}

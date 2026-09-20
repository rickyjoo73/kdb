package kdb

// institution_anchor — **기관·조직의 공식 표기를 공신력 출처에서 찾아 채운다.**
//
// ★오너 지시 (2026-09-20): "못 채운 것은 방법을 찾아 채워야지. 우리는 채워서 제공해
// 주는 시스템이야. 공신력 있는 소스 확보하고, 채워야지."
//
// ★무엇이 비어 있었나. 0143 으로 정치·경제·시사·스포츠를 받게 됐는데, 그 유형들에는
// **앵커를 붙여 주는 레인이 없었다.** person·group 은 musicbrainz·tmdb·kofic 이 있고
// 작품은 discogs·itunes 가 있는데, 정당·부처·대학·구단·기업은 아무것도 없다.
// 그래서 소비자가 가장 많이 묻는 것들이 빈칸으로 남았다(7일 기준):
//
//	더불어민주당 15 · SK하이닉스 14 · 국민의힘 11 · FC서울 11 · 이재용 10 …
//
// ★출처는 있다. 실측으로 15개 표본 중 14개가 위키데이터에 en·ja·zh 라벨을 갖고 있고
// 전부 kowiki 문서가 있다. 기관은 이름이 고유하고 문서가 잘 정비돼 있어 커버리지가 높다.
//
// ★그런데 **이름으로 찾기만 하면 사고가 난다.** 같은 표본에서 함정이 셋 나왔다:
//
//	조국   → Q642555   "nation of one's 'fathers'"      ← 일반명사 조국(祖國)
//	교육부 → Q861556   "United States Department of …"  ← **미국** 교육부
//	FC서울 → Q27951528 "reserve team of FC Seoul"       ← 2군 팀
//
//	이름이 같다는 것은 같은 대상이라는 증거가 아니다 — 에반 사고(희승의 라벨이 에반에게
//	박힌 일)가 가르친 그대로다. 그래서 이 레인은 **세 가지 독립 근거가 모두 맞을 때만**
//	앵커를 붙인다. 하나라도 어긋나면 빈칸으로 둔다(빈칸 > 틀린값).
//
//	① 종류가 맞다      P31 이 우리 유형과 어긋나지 않는다(AnchorTypeAllowed)
//	② 한국 것이다      P17/P495 가 대한민국이거나 영문 설명이 한국을 말한다
//	③ 우리 이름이다    kowiki 문서 제목이 우리 canonical_ko 와 정규화 일치
//
//	③이 특히 세다. 위 셋 중 조국은 ①에서, 미국 교육부는 ②에서, FC서울 2군은 ③에서
//	걸린다(그 문서 제목은 "FC 서울 B" 다).
//
// ★사람은 이 레인이 다루지 않는다. 동명이인이 너무 흔하고, P17 은 사람에게 비는 것이
// 정상이라 ②가 성립하지 않는다. 기관·조직만 본다.
//
// ★앵커만 붙이고 값 채움은 기존 레인(wd-locale)에 맡긴다 — 그쪽이 문자셋 가드·
// 소스 우선순위·괄호 주석 걷기를 이미 지키고 있다. 여기서 또 쓰면 그 규칙을 두 벌 적게 된다.

import (
	"context"
	"log"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb/commonnoun"
	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
)

// institutionTypes — 이 레인이 앵커를 찾아 주는 유형. **사람은 없다.**
var institutionTypes = []string{
	"political_party", "government_body", "company", "organization",
	"sports_team", "school", "publication", "agency", "channel_outlet",
}

// InstitutionAnchorResult — 한 번 돈 결과.
type InstitutionAnchorResult struct {
	Checked, Anchored int
	// 왜 안 붙였는지를 칸을 나눠 센다. 한 칸으로 세면 «근거 없음»이 전송 실패까지
	// 삼킨다(MarkFillAttempt 주석이 경고하는 그것).
	NoCandidate, TypeMismatch, NotKorean, TitleMismatch, FetchFailed int
	Samples                                                          []string
}

// DrainInstitutionAnchors — 기관·조직 행에 위키데이터 앵커를 붙인다. 기본 dry-run.
func DrainInstitutionAnchors(ctx context.Context, pool *pgxpool.Pool, cl *wikidata.Client, limit int, dry bool) InstitutionAnchorResult {
	var r InstitutionAnchorResult
	if pool == nil || cl == nil || limit <= 0 {
		return r
	}
	rows, err := pool.Query(ctx, `
SELECT e.id::text, e.canonical_ko, e.entity_type::text, COALESCE(d.n,0)
  FROM kwave_entities e
  LEFT JOIN (SELECT term_ko, count(*) n FROM kwave_kdb_request_terms
              WHERE created_at > now() - interval '30 days' GROUP BY 1) d
         ON d.term_ko = e.canonical_ko
 WHERE e.status IN ('active','candidate') AND e.operator_locked = false
   AND e.entity_type::text = ANY($2)
   -- 채울 것이 남아 있는 행만.
   AND (COALESCE(e.canonical_en,'') = '' OR COALESCE(e.canonical_ja,'') = ''
        OR COALESCE(e.canonical_zh,'') = '')
   -- 이미 앵커가 있으면 wd-locale 이 알아서 채운다. 여기 올 이유가 없다.
   AND NOT EXISTS (SELECT 1 FROM kwave_entity_external_refs x
                    WHERE x.entity_id = e.id AND x.provider = 'wikidata')
   -- 일반명사 구간에 등재된 낱말은 대상이 아니다.
   AND `+commonNounNotListedE+`
   -- 한 번 물어본 것은 30일 쉬었다 다시 본다(무존재 재조회 방지).
   AND NOT EXISTS (SELECT 1 FROM kwave_kdb_enrich_attempts g
                    WHERE g.entity_id = e.id AND g.field = 'inst-anchor'
                      AND g.last_attempt_at > now() - interval '30 days')
 -- 소비자가 많이 묻는 것부터.
 ORDER BY COALESCE(d.n,0) DESC, e.updated_at DESC
 LIMIT $1`, limit, institutionTypes)
	if err != nil {
		log.Printf("kdb.inst-anchor: select: %v", err)
		return r
	}
	type row struct {
		id, ko, typ string
		demand      int
	}
	var items []row
	for rows.Next() {
		var it row
		if rows.Scan(&it.id, &it.ko, &it.typ, &it.demand) == nil {
			items = append(items, it)
		}
	}
	rows.Close()

	for _, it := range items {
		r.Checked++
		ent, _, ferr := cl.SearchAndFetch(ctx, it.ko)
		if !dry {
			// 물어본 사실을 먼저 남긴다 — 실패해도 쿨다운이 걸려야 매 사이클 다시 묻지 않는다.
			_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_enrich_attempts (entity_id, field, attempts, last_attempt_at, last_source)
VALUES ($1::uuid,'inst-anchor',1,now(),'wikidata')
ON CONFLICT (entity_id, field) DO UPDATE
   SET attempts = kwave_kdb_enrich_attempts.attempts + 1, last_attempt_at = now()`, it.id)
		}
		if ferr != nil {
			r.FetchFailed++
			continue
		}
		if ent == nil {
			r.NoCandidate++
			continue
		}
		why, ok := institutionAnchorOK(ent, it.ko, it.typ)
		if !ok {
			switch why {
			case "type":
				r.TypeMismatch++
			case "korea":
				r.NotKorean++
			default:
				r.TitleMismatch++
			}
			if len(r.Samples) < 40 {
				r.Samples = append(r.Samples, "✗ "+it.ko+"/"+it.typ+" ← "+ent.QID+
					" ("+why+": "+truncRunes(ent.Descriptions["en"], 40)+")")
			}
			continue
		}
		if len(r.Samples) < 40 {
			r.Samples = append(r.Samples, "✓ "+it.ko+"/"+it.typ+" ← "+ent.QID+
				" en="+truncRunes(ent.Labels["en"], 20)+" ja="+truncRunes(ent.Labels["ja"], 12)+
				" zh="+truncRunes(ent.Labels["zh"], 12)+" 요청"+itoaSample(it.demand))
		}
		if dry {
			r.Anchored++
			continue
		}
		tag, uerr := pool.Exec(ctx, `
INSERT INTO kwave_entity_external_refs (entity_id, provider, external_id, confidence, source_url)
VALUES ($1::uuid,'wikidata',$2,0.90,'https://www.wikidata.org/wiki/'||$2)
ON CONFLICT DO NOTHING`, it.id, ent.QID)
		if uerr != nil {
			log.Printf("kdb.inst-anchor: %s 앵커 적재 실패: %v", it.ko, uerr)
			continue
		}
		if tag.RowsAffected() > 0 {
			r.Anchored++
			_, _ = pool.Exec(ctx, `
UPDATE kwave_entities
   SET notes = COALESCE(NULLIF(notes,'') || ' · ','') || $2, updated_at = now()
 WHERE id = $1::uuid`, it.id,
				"[inst-anchor] "+ent.QID+" — 종류·한국·문서제목 셋 다 일치")
			log.Printf("kdb.inst-anchor: %s (%s) ← %s  en=%q", it.ko, it.typ, ent.QID, ent.Labels["en"])
		}
	}
	return r
}

// institutionAnchorOK — **세 가지 독립 근거**가 모두 맞는가. 반환: (어긋난 이유, 통과 여부).
//
// 순서가 곧 설명력이다 — 왜 안 붙였는지 셀 때 가장 앞의 이유로 세어야 원인이 보인다.
func institutionAnchorOK(ent *wikidata.Entity, ko, typ string) (string, bool) {
	// ① 종류. P31 이 우리 유형과 **어긋난다고 알려진** 경우만 거른다(모르는 것은 통과).
	for _, q := range ent.InstanceOf {
		if allowed, known := AnchorTypeAllowed(q, typ); known && !allowed {
			return "type", false
		}
	}
	// ② 한국 것인가. P17/P495 가 1순위, 없으면 영문 설명이 한국을 말하는지 본다 —
	//    설명이 비었다고 해외로 읽으면 안 되지만, **미국 교육부**처럼 다른 나라를
	//    명시한 것은 여기서 걸러야 한다.
	desc := strings.ToLower(strings.TrimSpace(ent.Descriptions["en"]))
	korean := ent.IsSouthKorean() ||
		strings.Contains(desc, "south korea") || strings.Contains(desc, "korean") ||
		strings.Contains(desc, "in korea") || strings.Contains(desc, "of korea")
	if !korean {
		return "korea", false
	}
	// ★설명이 다른 나라를 **명시**하면 한국 표시가 있어도 거른다(겹표기 방어).
	for _, foreign := range []string{
		"united states", "u.s. federal", "japanese", "chinese", "taiwan",
		"north korean", "american", "british", "vietnamese",
	} {
		if strings.Contains(desc, foreign) {
			return "korea", false
		}
	}
	// ③ 우리 이름인가. **kowiki 문서 제목**이 우리 정본과 정규화 일치해야 한다.
	//    라벨 일치는 SearchAndFetch 가 이미 봤지만, 그것만으로는 2군 팀·분리 문서를
	//    못 거른다(FC서울 → "FC 서울 B"). 문서 제목은 그 구분을 담고 있다.
	title := strings.TrimSpace(ent.SiteTitles["kowiki"])
	if title == "" {
		return "title", false // 한국어 문서가 없으면 «우리 이름»을 확인할 길이 없다
	}
	if wikidata.NormalizeName(stripParenSuffix(title)) != wikidata.NormalizeName(ko) {
		return "title", false
	}
	return "", true
}

// stripParenSuffix — 위키 문서 제목의 동음이의 괄호를 뗀다("아이유 (가수)" → "아이유").
// 괄호가 이름의 일부인 경우(f(x))를 건드리지 않도록 **끝에 붙은 것만** 본다.
func stripParenSuffix(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasSuffix(s, ")") {
		return s
	}
	if i := strings.LastIndex(s, " ("); i > 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

func truncRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n])
}

// commonNounNotListedE — `kwave_entities e` 별칭용. 원시 SQL 에 끼우려면 값이 필요하다.
var commonNounNotListedE = commonnoun.NotListedSQL("e.canonical_ko")

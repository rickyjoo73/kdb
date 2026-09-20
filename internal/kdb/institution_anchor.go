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
	"net/http"
	"strings"
	"time"

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
	// ViaKowiki — 위키데이터 검색이 못 찾아 ko.wikipedia 제목으로 찾은 건수.
	ViaKowiki int
	Samples   []string
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
		// K-웨이브 설명 필터를 끄고 찾는다 — 기관 설명은 그 필터에 안 걸린다.
		ent, _, ferr := cl.SearchAndFetchScoped(ctx, it.ko, false)
		via := "wd-search"
		// ★검색이 못 찾으면 **ko.wikipedia 제목**으로 찾는다 (2026-09-20 실측).
		//
		//   필터를 끄고도 60건 중 45건이 «후보없음» 이었다. wbsearchentities 의 순위가
		//   한국 기관명에 약하다 — `FC서울` 의 상위 후보는 **곤충 속**과 이름요소 셋,
		//   그리고 2군 팀이었다. 정작 ko.wikipedia 는 `FC서울` 한 번에 맞는 문서를 준다
		//   (langlink: FC Seoul · FCソウル · 首爾足球俱樂部).
		//
		//   이 경로가 나은 근본 이유: **제목이 곧 우리가 묻는 이름**이라 검색 순위가
		//   끼어들 자리가 없다. 그리고 문서에 붙은 wikibase_item 이 곧 앵커다.
		//
		//   가드는 기존 것을 그대로 쓴다(koWikiLookup 이 리다이렉트를 해소하고
		//   동음이의 플래그를 준다):
		//     농협   → 「농업협동조합」으로 리다이렉트 = 다른 이름 → 거부
		//              (일반 개념의 영어 "Agricultural cooperative" 가 들어올 뻔했다)
		//     국세청 → ja 문서가 「国税庁 (曖昧さ回避)」 동음이의 → 거부
		if ferr == nil && ent == nil {
			if e2, v2 := koWikiAnchorFor(ctx, cl, it.ko); e2 != nil {
				ent, via = e2, v2
			}
		}
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
		// ★kowiki 경로로 온 것은 «우리 이름인가»가 **이미** 확인됐다 — 그 제목으로
		//   문서를 찾았거나(①②), 위키백과 리다이렉트와 위키데이터 별칭이 함께
		//   우리 이름을 그 항목으로 보냈다(③). 거기서 또 제목을 보면 자기가 확인한
		//   것을 자기가 부정한다. 실제로 그렇게 「티빙(TVING)」이 맞는 항목을
		//   찾아 놓고 떨어졌다.
		why, ok := institutionAnchorOK(ent, it.ko, it.typ, strings.HasPrefix(via, "kowiki"))
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
			r.Samples = append(r.Samples, "✓ "+it.ko+"/"+it.typ+" ← "+ent.QID+"["+via+"]"+
				" en="+truncRunes(ent.Labels["en"], 20)+" ja="+truncRunes(ent.Labels["ja"], 12)+
				" zh="+truncRunes(ent.Labels["zh"], 12)+" 요청"+itoaSample(it.demand))
		}
		// ★dry 에서도 **쓰기를 실제로 해 본다**(넣고 되돌린다).
		//
		//   2026-09-20: dry 는 판정만 보고 INSERT 를 건너뛰었다. 그래서 컬럼 이름이
		//   틀린 것(`source_url` — 실제로는 `url`)을 못 잡았고, 본 실행에서 **18건을
		//   찾아 놓고 한 건도 못 넣었다.** 결정이 맞는지와 쓸 수 있는지는 다른 물음이다.
		if dry {
			tx, terr := pool.Begin(ctx)
			if terr == nil {
				_, werr := tx.Exec(ctx, instAnchorInsertSQL, it.id, ent.QID)
				_ = tx.Rollback(ctx)
				if werr != nil {
					log.Printf("kdb.inst-anchor: ★쓰기 예행 실패 — 본 실행도 실패한다: %v", werr)
					r.FetchFailed++
					continue
				}
			}
			r.Anchored++
			if strings.HasPrefix(via, "kowiki") {
				r.ViaKowiki++
			}
			continue
		}
		tag, uerr := pool.Exec(ctx, instAnchorInsertSQL, it.id, ent.QID)
		if uerr != nil {
			log.Printf("kdb.inst-anchor: %s 앵커 적재 실패: %v", it.ko, uerr)
			continue
		}
		if tag.RowsAffected() > 0 {
			r.Anchored++
			if strings.HasPrefix(via, "kowiki") {
				r.ViaKowiki++
			}
			_, _ = pool.Exec(ctx, `
UPDATE kwave_entities
   SET notes = COALESCE(NULLIF(notes,'') || ' · ','') || $2, updated_at = now()
 WHERE id = $1::uuid`, it.id,
				"[inst-anchor:"+via+"] "+ent.QID+" — 종류·한국·문서제목 셋 다 일치")
			log.Printf("kdb.inst-anchor: %s (%s) ← %s  en=%q", it.ko, it.typ, ent.QID, ent.Labels["en"])
		}
	}
	return r
}

// institutionAnchorOK — **세 가지 독립 근거**가 모두 맞는가. 반환: (어긋난 이유, 통과 여부).
//
// 순서가 곧 설명력이다 — 왜 안 붙였는지 셀 때 가장 앞의 이유로 세어야 원인이 보인다.
func institutionAnchorOK(ent *wikidata.Entity, ko, typ string, identityKnown bool) (string, bool) {
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
	// ③ 우리 이름인가. 이미 확인된 경로로 왔으면 건너뛴다(위 주석 참조).
	if identityKnown {
		return "", true
	}
	// **kowiki 문서 제목**이 우리 정본과 정규화 일치해야 한다.
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

// koWikiHTTP — 이 레인 전용 클라이언트. ko.wikipedia 는 키가 없고 쿼터도 없지만
// 무한정 기다리면 배치가 멈춘다.
var koWikiHTTP = &http.Client{Timeout: 20 * time.Second}

// koWikiAnchorFor — ko.wikipedia 제목으로 앵커 후보를 찾는다. 반환: (엔티티, 경로표시).
//
// ★세 가지 이름으로 물어본다 (2026-09-20 실측으로 하나씩 늘렸다).
//
//	① 정본 그대로            FC서울 → 맞는 문서(검색이 못 찾던 것)
//	② 괄호 병기를 뗀 이름     「티빙(TVING)」 → 「티빙」. 우리 정본에 영문을 괄호로
//	                        병기한 것이 많은데 그 제목의 문서는 위키에 없다.
//	③ 약칭 리다이렉트        「심평원」→「건강보험심사평가원」·「가톨릭대」→「가톨릭대학교」
//	                        소비자는 약칭으로 묻고 위키는 정식명으로 문서를 둔다.
//
// ★③이 위험한 이유와 그 값. 「농협」은 「농업협동조합」(**일반 개념**) 으로 리다이렉트되고
//
//	그 문서의 영문은 "Agricultural cooperative" 다 — 기업 농협이 아니다. 리다이렉트를
//	그냥 받으면 일반 개념의 영어 단어가 기업 칸에 들어간다.
//
//	그래서 리다이렉트는 **두 출처가 같은 말을 할 때만** 받는다: 위키백과가 우리 이름을
//	그 문서로 보내고(리다이렉트), **위키데이터도 그 항목의 한국어 별칭에 우리 이름을**
//	갖고 있어야 한다. 심평원·가톨릭대는 별칭에 있고, 일반 개념 항목에는 없다.
func koWikiAnchorFor(ctx context.Context, cl *wikidata.Client, ko string) (*wikidata.Entity, string) {
	tried := map[string]bool{}
	for i, name := range []string{ko, stripParenAnnotation(ko)} {
		name = strings.TrimSpace(name)
		if name == "" || tried[name] {
			continue
		}
		tried[name] = true
		p, err := koWikiLookup(ctx, koWikiHTTP, name)
		if err != nil || p == nil || len(p.Missing) > 0 ||
			p.PageProps.Disambiguation != nil || p.PageProps.WikibaseItem == "" {
			continue
		}
		ent, ferr := cl.Fetch(ctx, p.PageProps.WikibaseItem)
		if ferr != nil || ent == nil {
			continue
		}
		via := "kowiki"
		if i == 1 {
			via = "kowiki-괄호뗌"
		}
		// 제목이 그대로면 바로 쓴다 — «우리 이름인가»가 구조적으로 답해진 경우다.
		if koWikiTitleMatches(name, p.Title) {
			return ent, via
		}
		// 리다이렉트로 다른 이름이 됐다 → 위키데이터 별칭이 같은 말을 해야 받는다.
		if hasKoAlias(ent, name) {
			return ent, via + "-약칭"
		}
	}
	return nil, ""
}

// hasKoAlias — 위키데이터 항목의 한국어 라벨/별칭에 이 이름이 있는가(정규화 비교).
func hasKoAlias(ent *wikidata.Entity, name string) bool {
	want := wikidata.NormalizeName(name)
	if want == "" || ent == nil {
		return false
	}
	if wikidata.NormalizeName(ent.Labels["ko"]) == want {
		return true
	}
	for _, a := range ent.Aliases["ko"] {
		if wikidata.NormalizeName(a) == want {
			return true
		}
	}
	return false
}

// stripParenAnnotation — 정본에 병기된 괄호를 뗀다("티빙(TVING)" → "티빙").
// **괄호가 이름의 일부인 것**은 건드리지 않는다: 여는 괄호 앞에 남는 글자가 없으면
// (`f(x)`처럼 한 글자만 남는 경우 포함) 그대로 둔다.
func stripParenAnnotation(s string) string {
	s = strings.TrimSpace(s)
	i := strings.IndexAny(s, "(（")
	if i <= 1 { // 없거나, 앞이 한 글자 이하 → f(x)·ALL(H)OURS 류
		return s
	}
	if !strings.HasSuffix(s, ")") && !strings.HasSuffix(s, "）") {
		return s
	}
	return strings.TrimSpace(s[:i])
}

// instAnchorInsertSQL — 앵커 적재. **한 자리에 둔다** — dry 의 예행과 본 실행이 같은
// 문장을 써야 예행이 의미가 있다(둘이 갈리면 예행은 통과하고 본 실행만 깨진다).
//
// 컬럼은 `url` 이다. `source_url` 로 적었다가 18건을 찾아 놓고 전부 못 넣었다.
const instAnchorInsertSQL = `
INSERT INTO kwave_entity_external_refs (entity_id, provider, external_id, url, confidence)
VALUES ($1::uuid,'wikidata',$2,'https://www.wikidata.org/wiki/'||$2,0.90)
ON CONFLICT (entity_id, provider) DO NOTHING`

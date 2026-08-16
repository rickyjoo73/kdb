package kdb

// kowiki_anchor_drain.go — ko.wikipedia 문서가 있는 unverified 엔티티에 **위키데이터
// 앵커를 붙인다**. 키 없음·쿼터 없음·엔티티당 API 1회.
//
// ★왜 만들었나(2026-08-16). 등급을 정직하게 고친 뒤 unverified 가 2,980건 남았고, 뉴스검색
// 승급률이 꼬리로 갈수록 무너졌다(초반 60건에 23 → 후반 150건에 1). 오너가 "새벽에 모두
// 해결"을 요구했지만 **시간을 더 쓴다고 나오지 않는 구간**이었다 — 뉴스에 안 실리는
// 엔티티라서다. 그런데 뉴스에 없어도 **위키백과에는 있는** 계층이 있다.
//
// 실측 수율(unverified·ref 전무 80건 표본):
//
//	ko.wikipedia 정확 제목 존재  26건 (32%)
//	  └ 가드 통과                8건 (10%)  ← 전수 검수해 8/8 정확
//
// 2,980건에 적용하면 대략 300건이 **authoritative** 로 올라간다(QID 는 권위 앵커다).
//
// ★가드가 셋인 이유는 정확일치만으로는 정밀도가 38%(26건 중 10건)였기 때문이다.
// 실패는 전부 탐지 가능한 유형이었다:
//
//	①동음이의어 문서 11건 — `베드` `와일드카드` `파이어` `루빈` 이 각각 그 낱말의
//	  동음이의 문서에 걸린다. pageprops.disambiguation 으로 결정적으로 걸러진다.
//	②다른 이름으로 리다이렉트 5건 — `벨리타`→벨리타 모레노(미국 배우),
//	  `산보`→산책(walking), `파리바게뜨`→파리크라상(모회사). 해소된 제목이 원어와
//	  같아야 한다(`육사오`→`육사오(6/45)` 처럼 괄호 주석만 붙는 건 허용).
//	③의미 불일치 — `홍가`→중국 전한의 연호, `시스터 액트`→1992년 미국 영화.
//	  도입부에 한국 대중문화 맥락이 있어야 한다.
//
// ③을 wikidata.IsKWaveDescription 으로 하려다 말았다 — 그 함수는 wbsearchentities 의
// 영문 description("South Korean singer")용이라, 여기 통과분 10건 중 9건(정답 8건 포함)을
// 죽인다(`육사오`의 설명은 "2022년 코미디 영화"라 K 키워드가 없다). 설명문이 아니라
// **문서 도입부**를 봐야 한다 — 정보량이 다르다.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb/httpx"
)

// koWikiAnchorField — 원장 field. 성공·기각 모두 남긴다(안 남기면 경보에 영원히 미판정).
const koWikiAnchorField = "kowiki-anchor"

// koWikiCtxRe — 한국 대중문화 맥락. 도입부에서 하나라도 잡히면 통과.
//
// 방송사·플랫폼 약어를 넣은 이유: `별이 빛나는 밤에` 도입부는 "MBC FM4U에서 …방송되는"
// 이라 '한국'이라는 낱말이 없다. 반대로 `홍가`(중국 연호)·`시스터 액트`(미국 영화)는
// 어느 것도 안 걸린다.
//
// ★장르어(드라마·예능·영화제)를 뺐다(2026-08-16, 실전 오탐으로 배웠다). 처음엔 넣었는데
// **`라이크 크레이지`(지민의 곡)가 2011년 미국 영화 Q598752 에 붙었다** — 그 문서 도입부의
// "로맨틱 **드라마** 영화로"가 걸린 것이다. 장르어는 나라를 가리지 않으므로 한국 맥락의
// 신호가 못 된다. 빼도 참인 8건은 전부 유지된다(각각 대한민국·넷플릭스·한국·KBS·서울·
// MBC·아이돌로 걸린다) — 표본으로 확인했다.
//
// 외국 국적 수식("미국의")을 명시 기각하는 안도 재봤는데 버렸다. Go 정규식(RE2)에
// lookahead 가 없어 `태국의 (?!가수)` 를 못 쓰고, 단순 목록으로 하면 `뱀뱀`(태국 출신
// K-pop 아이돌)과 `샘 해밍턴`(오스트레일리아 출신 대한민국 방송인)이 함께 죽는다.
// **긍정 신호를 좁히는 쪽이 부정 목록을 늘리는 쪽보다 안전하다.**
var koWikiCtxRe = regexp.MustCompile(`(?i)대한민국|한국|남한|KBS|MBC|SBS|JTBC|tvN|ENA|Mnet|채널A|OCN|` +
	`넷플릭스|왓챠|티빙|웨이브|디즈니\+|케이팝|K-?POP|아이돌|보이그룹|걸그룹|` +
	`소속사|엔터테인먼트|서울|부산`)

// koWikiParenRe — 제목 꼬리의 괄호 주석(`육사오(6/45)`, `베드 (동음이의)`).
var koWikiParenRe = regexp.MustCompile(`[(（].*$`)

// koWikiNormTitle — 제목 비교용 정규화. 공백·중점·구두점을 접는다
// (`크리스영` vs `크리스 영` 을 같게 보되, 아래 맥락 게이트가 최종 판정한다).
func koWikiNormTitle(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if unicode.IsSpace(r) {
			continue
		}
		switch r {
		case '·', '・', '-', '–', '—', '_', '\'', '"', '’', ':', '：', ',', '.', '!', '?':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// koWikiTitleMatches — 해소된 문서 제목이 우리 canonical_ko 와 같은가.
// 괄호 주석만 덧붙은 형태는 허용한다(위키백과의 동음이의 구분 관행).
func koWikiTitleMatches(ko, title string) bool {
	nk := koWikiNormTitle(ko)
	if nk == "" {
		return false
	}
	if nk == koWikiNormTitle(title) {
		return true
	}
	return nk == koWikiNormTitle(koWikiParenRe.ReplaceAllString(title, ""))
}

type koWikiPage struct {
	Title string `json:"title"`
	// ★Missing 은 json.RawMessage 여야 한다(2026-08-16 실측). MediaWiki 는 문서가 없을 때
	// `"missing": ""`(빈 **문자열**)을 준다 — `*struct{}` 로 받으면 unmarshal 이 실패하고,
	// 그 실패가 호출측에서 **전송실패로 읽혀 기각이 원장에 안 남는다.** 그러면 문서 없는
	// 엔티티를 매 회차 다시 조회한다(실측: 150건 중 ~120건이 이 상태였다).
	// 존재 여부만 보면 되므로 타입을 고정하지 않는다.
	Missing   json.RawMessage `json:"missing"`
	FullURL   string          `json:"fullurl"`
	Extract   string          `json:"extract"`
	PageProps struct {
		WikibaseItem   string  `json:"wikibase_item"`
		Disambiguation *string `json:"disambiguation"`
	} `json:"pageprops"`
}

// koWikiLookup — 제목 하나를 조회한다(리다이렉트 해소 포함). API 1회.
func koWikiLookup(ctx context.Context, cl *http.Client, title string) (*koWikiPage, error) {
	u := "https://ko.wikipedia.org/w/api.php?action=query&format=json&redirects=1" +
		"&prop=pageprops|extracts|info&inprop=url&exintro=1&explaintext=1&exchars=300" +
		"&titles=" + url.QueryEscape(title)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "kdb-anchor/1.0 (+https://aiinad.com)")
	resp, err := httpx.Do(cl, req, 3)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kowiki: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	var out struct {
		Query struct {
			Pages map[string]koWikiPage `json:"pages"`
		} `json:"query"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	for _, p := range out.Query.Pages {
		p := p
		return &p, nil
	}
	return nil, fmt.Errorf("kowiki: empty response")
}

// koWikiVerdict — 페이지가 이 엔티티의 앵커로 쓸 만한가. ok=false 면 (사유, 상세).
func koWikiVerdict(ko string, p *koWikiPage) (ok bool, verdict, reason string) {
	switch {
	case p == nil || len(p.Missing) > 0:
		return false, "no-article", "ko.wikipedia 문서 없음"
	case p.PageProps.Disambiguation != nil:
		return false, "disambiguation", "동음이의어 문서: " + p.Title
	case !koWikiTitleMatches(ko, p.Title):
		return false, "title-mismatch", "다른 이름으로 해소됨: " + p.Title
	case p.PageProps.WikibaseItem == "":
		return false, "no-qid", "문서에 위키데이터 항목 없음: " + p.Title
	case !koWikiCtxRe.MatchString(p.Extract):
		return false, "foreign-topic", "도입부에 한국 대중문화 맥락 없음: " + firstRunes(p.Extract, 60)
	}
	return true, "", ""
}

// firstRunes — 사유 문자열용 앞머리 잘라내기(룬 기준 — 한글이 깨지지 않게).
func firstRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}

// DrainKoWikiAnchors — unverified 엔티티에 ko.wikipedia 유래 위키데이터 앵커를 붙인다.
// 반환 (붙인 수, 조회한 수).
func DrainKoWikiAnchors(ctx context.Context, pool *pgxpool.Pool, limit int) (anchored, checked int) {
	if pool == nil {
		return 0, 0
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := pool.Query(ctx, `
SELECT id::text, canonical_ko FROM kwave_entities e
 WHERE status='active' AND operator_locked=false
   AND COALESCE(canonical_ko,'') <> ''
   AND verification_tier='unverified'
   AND NOT EXISTS (SELECT 1 FROM kwave_entity_external_refs r
                    WHERE r.entity_id=e.id AND r.provider='wikidata')
   AND `+FillRetryPredicate("e", "$2")+`
 -- 오래 방치된 것부터. confidence 는 우리가 매긴 값이라 순서 근거로 약하다.
 ORDER BY e.created_at ASC
 LIMIT $1`, limit, koWikiAnchorField)
	if err != nil {
		log.Printf("kdb.kowiki-anchor: 선정: %v", err)
		return 0, 0
	}
	type item struct{ id, ko string }
	var todo []item
	for rows.Next() {
		var it item
		if rows.Scan(&it.id, &it.ko) == nil {
			todo = append(todo, it)
		}
	}
	rows.Close()
	if len(todo) == 0 {
		return 0, 0
	}
	cl := &http.Client{Timeout: 20 * time.Second}
	lim := httpx.NewLimiter(300 * time.Millisecond) // 위키백과 예의(무키 공개 API)

	for _, it := range todo {
		if ctx.Err() != nil {
			break
		}
		if lim.Wait(ctx) != nil {
			break
		}
		checked++
		p, lerr := koWikiLookup(ctx, cl, it.ko)
		if lerr != nil {
			// ★전송실패는 판정이 아니다 — 마킹 없이 다음 회차로. 이걸 기록하면 위키백과가
			// 잠깐 죽은 동안 멀쩡한 후보가 90일 낙인을 받는다(이 저장소가 다섯 번 밟은 계열).
			log.Printf("kdb.kowiki-anchor: 조회 실패 %q — 마킹 없이 넘김 (%v)", it.ko, lerr)
			continue
		}
		ok, verdict, reason := koWikiVerdict(it.ko, p)
		if !ok {
			MarkFillAttempt(ctx, pool, it.id, koWikiAnchorField, verdict, reason)
			continue
		}
		qid := p.PageProps.WikibaseItem
		// 앵커 적재 — 이미 다른 provider ref 가 있어도 wikidata 슬롯만 채운다.
		if _, err := pool.Exec(ctx, `
INSERT INTO kwave_entity_external_refs (entity_id, provider, external_id, url, confidence, raw_payload, fetched_at)
VALUES ($1::uuid,'wikidata',$2,$3,0.85,'{}'::jsonb,now())
ON CONFLICT (entity_id, provider) DO NOTHING`,
			it.id, qid, "https://www.wikidata.org/wiki/"+qid); err != nil {
			log.Printf("kdb.kowiki-anchor: ref 적재 %q: %v", it.ko, err)
			continue
		}
		// 문서 URL 도 근거로 남긴다 — QID 만으로는 "왜 이 QID 인지"를 되짚을 수 없다.
		if p.FullURL != "" {
			_, _ = pool.Exec(ctx, `
UPDATE kwave_entities
   SET source_urls = (SELECT ARRAY(SELECT DISTINCT u
                        FROM unnest(COALESCE(source_urls,'{}'::text[]) || ARRAY[$2::text]) u
                       WHERE u <> '')),
       updated_at = now()
 WHERE id = $1::uuid`, it.id, p.FullURL)
		}
		ClearFillAttempt(ctx, pool, it.id, koWikiAnchorField)
		anchored++
		log.Printf("kdb.kowiki-anchor: %q → %s (%s)", it.ko, qid, firstRunes(p.Extract, 40))
	}
	log.Printf("kdb.kowiki-anchor: 완료 anchored=%d /%d", anchored, checked)
	return anchored, checked
}

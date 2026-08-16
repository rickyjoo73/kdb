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
	"unicode/utf8"

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
// ★도시명은 서울·부산만 남겼다(2026-08-16 실측). `대전`을 넣었더니 **`마타하리`가
// "제1차 세계 **대전**"에 걸렸다.** 한국어는 띄어쓰기가 불규칙해 짧은 지명이 낱말
// 내부에서 잡힌다 — 부분문자열 매칭이 곧 오탐이다.
//
// ★`기획사`도 뺐다 — 일본 기획사 `아뮤즈`의 도입부 "일본의 연예 **기획사**"가 걸린다.
// `엔터테인먼트`는 남겼다(국내 소속사 표기 관행이라 실측 오탐이 없었다).
//
// ★역사국가(조선·신라·고구려)를 넣는 안은 버렸다. `위화랑`·`장녹수` 4건을 되찾지만
// `대군`(왕자 작호 문서 ↔ 우리 드라마)·`서인`(붕당 문서 ↔ 우리 인물)을 함께 들인다.
// **"한국 주제"와 "K-콘텐츠 엔티티"는 다르다** — 이 레인이 재는 건 후자다.
var koWikiCtxRe = regexp.MustCompile(`(?i)대한민국|한국|남한|` +
	`KBS|MBC|SBS|JTBC|tvN|ENA|Mnet|채널A|채널S|OCN|EBS|MBN|TV ?CHOSUN|TV조선|` +
	`문화방송|한국방송공사|에스비에스|교육방송|종합편성|지상파|` +
	`넷플릭스|왓챠|티빙|웨이브|쿠팡플레이|디즈니\+|` +
	`케이팝|K-?POP|아이돌|보이그룹|걸그룹|트로트|한류|` +
	`소속사|엔터테인먼트|서울|부산|` +
	`멜론|지니뮤직|한터차트|가온차트|서클차트`)

// koWikiLangRe — **언어·문자를 가리키는 `한국…`은 국적 신호가 아니다.**
//
// ★2026-08-16 실전 오탐 두 건이 정확히 이것이었다. `산`(래퍼)이 **지형으로서의 산**
// (Q8502)에 붙었고 — 도입부 "**한국어** 고유어로는, 뫼 또는 메라고" — `WANNABE`(그룹)가
// 스파이스 걸스의 노래(Q418833)에 붙었다 — "〈Wannabe〉(**한국어**: 워너비)는 영국의
// 걸 그룹…". 위키백과는 외국 주제에도 한국어 표기를 병기하므로, 이 표기를 지운 뒤에
// 맥락을 봐야 한다.
//
// 대가가 없지는 않다: `On The Ground`(로제 솔로곡)는 도입부의 유일한 한국 신호가
// "(한국어: 온 더 그라운드)"라서 함께 기각된다. authoritative 를 주는 레인이라
// **거짓 승급보다 거짓 기각을 택한다** — 기각은 다음 소스가 다시 주울 수 있다.
var koWikiLangRe = regexp.MustCompile(`한국어|한국말|한국식 한자음|한국 한자음`)

// koWikiNameSet — **우리 DB 가 이미 가진 한국 엔티티 이름 사전.**
//
// ★왜 필요한가(2026-08-16 실측). 맥락 정규식만으로는 도입부가 아티스트 이름만 대는
// 문서를 못 잡는다: `붐바야`→"**블랙핑크**의 …첫 싱글 음반", `호르몬 전쟁`→"**방탄소년단**의
// 첫 번째 정규 음반", `꽃갈피 셋`→"**아이유**의 세번째 리메이크 앨범", `APT.`→"레코드
// 레이블은 **더블랙레이블**". 이 계층은 song_album 에 몰려 있다.
//
// 아티스트 이름을 키워드 목록에 넣기 시작하면 그 목록은 썩는다. **우리가 이미
// authoritative 로 확정해 둔 이름들이 곧 그 목록이다** — DB 가 자라면 사전도 자란다.
//
// 규칙 둘, 둘 다 실측으로 정했다:
//
//	①한글 4자 이상. 3자로 하면 `라리사`(우리 DB 의 인물명이자 그리스 도시)가 그리스
//	  국가 문서에 걸려 뮤지컬 `그리스`를 오탐시킨다. 인명 대부분(3자)을 잃는 대신
//	  되찾은 8건 전부가 정답이 된다(3자면 10건 중 1건 오답).
//	②한글 경계. 부분문자열 매칭이 곧 오탐이다 — `에스파`⊂`에스파냐`(스페인)가
//	  `보고타`를, `이시아`⊂`동남아시아`·`말레이시아`가 `로미`·`봉선화`를 오탐시켰다.
//
// 되먹임 우려(이 레인이 준 authoritative 가 다시 사전이 된다)는 남아 있으나, 4자 이상
// 한글 인물·그룹·소속사 이름이 틀릴 확률은 낮다. 짧은 보통명사(`산`)는 애초에 못 들어온다.
type koWikiNameSet struct{ names []string }

// loadKoWikiNames — 사전을 한 번 읽어 온다(드레인 1회당 1 쿼리).
func loadKoWikiNames(ctx context.Context, pool *pgxpool.Pool) *koWikiNameSet {
	s := &koWikiNameSet{}
	rows, err := pool.Query(ctx, `
SELECT canonical_ko FROM kwave_entities
 WHERE status='active' AND verification_tier='authoritative'
   AND entity_type IN ('person','group','agency','channel_outlet')
   AND canonical_ko ~ '^[가-힣]{4,}$'`)
	if err != nil {
		log.Printf("kdb.kowiki-anchor: 이름사전 적재 실패 — 맥락 정규식만으로 진행 (%v)", err)
		return s
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil && n != "" {
			s.names = append(s.names, n)
		}
	}
	return s
}

// isHangul — 한글 음절인가(경계 판정용).
func isHangul(r rune) bool { return r >= 0xAC00 && r <= 0xD7A3 }

// koParticleHead — 이름 **뒤**에 곧바로 올 수 있는 조사의 첫 음절.
//
// ★왼쪽 경계와 오른쪽 경계를 다르게 다뤄야 한다. 한국어는 조사가 이름에 붙어 쓰이므로
// (`방탄소년단의`, `블랙핑크는`, `아이유가`) 오른쪽을 "한글 금지"로 두면 **정상 문장이
// 전부 막힌다** — 실제로 이 테스트가 먼저 깨졌다. 반대로 왼쪽은 붙여 쓸 이유가 없으니
// 엄격히 막는다. 오탐이었던 `이시아`⊂`동남아시아`·`말레이시아`는 **왼쪽**(남·레)에서
// 잡히고, `에스파`⊂`에스파냐`는 오른쪽 `냐`가 조사가 아니라서 잡힌다.
var koParticleHead = map[rune]bool{
	'의': true, '은': true, '는': true, '이': true, '가': true, '을': true, '를': true,
	'에': true, '와': true, '과': true, '도': true, '만': true, '로': true, '으': true,
	'라': true, '인': true, '님': true, '씨': true, '께': true, '부': true, '까': true,
}

// hit — 도입부에 사전의 이름이 **낱말 경계를 지켜** 나타나면 그 이름을 준다.
func (s *koWikiNameSet) hit(text string) string {
	if s == nil || text == "" {
		return ""
	}
	for _, n := range s.names {
		for off := 0; off < len(text); {
			i := strings.Index(text[off:], n)
			if i < 0 {
				break
			}
			i += off
			okBefore := true
			if i > 0 {
				r, _ := utf8.DecodeLastRuneInString(text[:i])
				okBefore = !isHangul(r)
			}
			okAfter := true
			if j := i + len(n); j < len(text) {
				r, _ := utf8.DecodeRuneInString(text[j:])
				okAfter = !isHangul(r) || koParticleHead[r]
			}
			if okBefore && okAfter {
				return n
			}
			off = i + len(n)
		}
	}
	return ""
}

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
//
// known 은 nil 이어도 된다 — 그러면 맥락 정규식만으로 판정한다(사전 적재가 실패해도
// 레인이 멈추지 않아야 한다).
func koWikiVerdict(ko string, p *koWikiPage, known *koWikiNameSet) (ok bool, verdict, reason string) {
	switch {
	case p == nil || len(p.Missing) > 0:
		return false, "no-article", "ko.wikipedia 문서 없음"
	case p.PageProps.Disambiguation != nil:
		return false, "disambiguation", "동음이의어 문서: " + p.Title
	case !koWikiTitleMatches(ko, p.Title):
		return false, "title-mismatch", "다른 이름으로 해소됨: " + p.Title
	case p.PageProps.WikibaseItem == "":
		return false, "no-qid", "문서에 위키데이터 항목 없음: " + p.Title
	}
	// 신호 둘 중 하나면 통과. 한국어 표기 병기는 지우고 본다(위 koWikiLangRe 참조).
	body := koWikiLangRe.ReplaceAllString(p.Extract, "")
	if koWikiCtxRe.MatchString(body) {
		return true, "", ""
	}
	if n := known.hit(body); n != "" {
		return true, "", ""
	}
	return false, "foreign-topic", "도입부에 한국 대중문화 맥락 없음: " + firstRunes(p.Extract, 60)
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
	known := loadKoWikiNames(ctx, pool)
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
		ok, verdict, reason := koWikiVerdict(it.ko, p, known)
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

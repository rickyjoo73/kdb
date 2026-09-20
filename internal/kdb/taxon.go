package kdb

// taxon.go — **종·분류군 표기 레인.**
//
// ★계기 (2026-09-20). 번역 쪽에서 다섯 건이 한꺼번에 틀렸다. 전부 같은 결함이다:
//
//	전어     → zh 鲭鱼(고등어)      실제 窩斑鰶 / 窝斑鰶
//	광어     → zh 平鱼(병어)        실제 扁口鱼 (표준 한국명은 넙치)
//	우럭     → zh 石首鱼(조기)      실제 許氏平鮋 (표준 한국명은 조피볼락)
//	도라지   → es bellota(도토리)   실제 Platycodon grandiflorus
//	거위벌레 → ja ゴキブリ(바퀴벌레) 실제 オトシブミ科
//
//	기계번역은 **뜻**을 옮긴다. 그런데 종명은 뜻으로 옮기면 반드시 틀린다 — 전어의
//	뜻을 옮기면 다른 물고기가 나온다. 소리로 옮겨도 틀린다(ジョノ 는 일본어 표기가 아니다).
//	종명에는 제3의 규칙이 있다: **학명이 앵커고, 각 언어판 위키의 표제가 그 언어의
//	표준 통용명이다.**
//
// ★그래서 학명(P225)을 게이트로 쓴다. 위키데이터 항목에 P225 가 있으면 그것은
//	분류군이고, 이 레인이 답할 수 있다. 없으면 **손대지 않는다** — 일반 낱말에
//	이 규칙을 적용하면 엉뚱한 곳에서 위키 표제를 끌어온다.
//
// ★통칭과 표준명이 다르다. 기사는 `광어`·`우럭`이라고 쓰는데 표준 한국명은 `넙치`·
//	`조피볼락`이고 위키 문서도 그쪽에 있다. ko.wikipedia 의 **리다이렉트가 그 대응을
//	이미 알고 있다** — 요청어로 찾아 표준명으로 해소되면 요청어는 별칭이다.
//	이 연결이 없으면 원천을 붙여도 서로 만나지 못한다.
//
// ★값은 지어내지 않는다. 언어판 문서가 없으면 그 칸은 비운다. 라틴권에서 학명을
//	그대로 쓰는 것은 정상이지만(es/pt 위키가 실제로 그렇다), 일본어·중국어 칸에
//	라틴 학명이 들어오는 것은 표기가 아니라 결손이다 — 버린다.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode"

	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
)

// PropTaxonName — 위키데이터 「학명」 속성. 이 레인의 게이트.
const PropTaxonName = "P225"

// ErrNotTaxon — 위키데이터 항목에 학명이 없다. 분류군이 아니므로 이 레인은 답하지 않는다.
var ErrNotTaxon = errors.New("taxon: P225 없음 — 분류군이 아니다")

// ErrNoKoWikiArticle — ko.wikipedia 문서가 없다. 표준 한국명을 알 수 없다.
var ErrNoKoWikiArticle = errors.New("taxon: ko.wikipedia 문서 없음")

// TaxonLookup — 분류군 한 건의 조회 결과.
type TaxonLookup struct {
	// Term — 요청받은 말. 통칭일 수 있다(광어).
	Term string
	// StandardKO — ko.wikipedia 가 해소한 표준 한국명(넙치). Term 과 같을 수 있다.
	StandardKO string
	// AliasKO — Term 이 StandardKO 와 다를 때의 통칭. 별칭으로 실어야 다음 요청이 만난다.
	AliasKO string
	// QID·Scientific — 앵커. Scientific 이 비면 이 레인은 답하지 않는다.
	QID        string
	Scientific string
	// ByLocale — KDB locale 키(en/ja/vi/zh_hant/es/id/pt_br) → 표기.
	ByLocale map[string]string
	// Notes — 사람이 읽어야 할 단서(층위 불일치 등). 값 판단을 대신하지 않는다.
	Notes []string
}

// taxonSiteLocale — 위키 사이트 코드 → KDB locale.
//
// zhwiki 는 번체 표제를 쓰므로 zh_hant 다(wikidata 패키지의 sitelinkLocale 과 같은 판단).
// 간체 zh 는 여기서 만들지 않는다 — 보유 zh_hant 에서 opencc 가 결정적으로 변환한다.
// 두 자리가 서로 다른 규칙을 갖지 않게 한 곳에서만 정한다.
var taxonSiteLocale = map[string]string{
	"enwiki": "en",
	"jawiki": "ja",
	"viwiki": "vi",
	"eswiki": "es",
	"idwiki": "id",
	"ptwiki": "pt_br",
	"zhwiki": "zh_hant",
}

// cjkLocales — 라틴 학명이 표기가 될 수 없는 칸.
var cjkLocales = map[string]bool{"ja": true, "zh": true, "zh_hant": true}

// TaxonLocaleNames — 분류군 항목에서 로케일별 표기를 고른다. **외부 호출 없음.**
//
// 고르는 순서는 언어판 문서 제목 → 위키데이터 라벨이다. 문서 제목을 위에 두는 이유는
// 제목이 그 언어판 편집자들이 합의한 표제이기 때문이다(9/19 zhwiki 레인과 같은 판단).
//
// 버리는 것:
//   - 한글이 든 값 — ko 표제가 잘못 실려 온 것이다.
//   - 요청 한국어와 같은 값 — 옮겨지지 않았다.
//   - ja/zh/zh_hant 칸의 라틴 전용 값 — 학명이 들어온 것이고 그 언어의 표기가 아니다.
//
// 남기는 것: es/vi/id/pt_br/en 의 학명. 라틴권 위키가 실제로 학명을 표제로 쓴다
// (도라지 eswiki = Platycodon grandiflorus). 그 언어에서 그게 정답이다.
func TaxonLocaleNames(koTerm, sci string, siteTitles, labels map[string]string) (map[string]string, []string) {
	out := map[string]string{}
	var notes []string

	pick := func(locale, raw string) {
		v := wikidata.CleanDisambiguator(strings.TrimSpace(raw))
		if v == "" {
			return
		}
		if containsHangul(v) {
			return
		}
		if v == koTerm {
			return
		}
		if cjkLocales[locale] && isLatinOnly(v) {
			return
		}
		if _, dup := out[locale]; dup {
			return
		}
		out[locale] = v
	}

	for site, title := range siteTitles {
		if loc := taxonSiteLocale[site]; loc != "" {
			pick(loc, title)
		}
	}
	for loc, label := range labels {
		if loc == "ko" {
			continue
		}
		pick(loc, label)
	}

	// 층위 불일치 — 판정하지 않고 알리기만 한다. 기사 문맥이 「벌레 한 마리」면
	// オトシブミ 가 맞고 분류군이면 オトシブミ科 가 맞다. 그건 이 함수가 알 수 없다.
	if ja := out["ja"]; ja != "" && strings.HasSuffix(ja, "科") && !strings.HasSuffix(koTerm, "과") {
		notes = append(notes, fmt.Sprintf("층위 확인: ja 표제가 과(科) 단위다(%s) — 기사가 개체를 가리키면 통칭을 써야 한다", ja))
	}
	if sci != "" {
		for _, loc := range []string{"es", "pt_br", "id", "en", "vi"} {
			if out[loc] == sci {
				notes = append(notes, fmt.Sprintf("%s 는 학명 그대로다(%s) — 그 언어판 표제가 학명이다", loc, sci))
			}
		}
	}
	return out, notes
}

// containsHangul — 한글 음절/자모가 들어 있는가.
func containsHangul(s string) bool {
	for _, r := range s {
		if (r >= 0xAC00 && r <= 0xD7A3) || (r >= 0x1100 && r <= 0x11FF) || (r >= 0x3130 && r <= 0x318F) {
			return true
		}
	}
	return false
}

// isLatinOnly — 글자가 전부 라틴 문자인가(공백·부호·숫자는 무시).
func isLatinOnly(s string) bool {
	seen := false
	for _, r := range s {
		if !unicode.IsLetter(r) {
			continue
		}
		seen = true
		if !unicode.In(r, unicode.Latin) {
			return false
		}
	}
	return seen
}

// TaxonClient — ko.wikipedia + 위키데이터 두 곳을 묶은 조회기.
type TaxonClient struct {
	http *http.Client
	wd   *wikidata.Client
}

// NewTaxonClient — 기본 조회기.
func NewTaxonClient(cl *http.Client) *TaxonClient {
	if cl == nil {
		cl = &http.Client{}
	}
	return &TaxonClient{http: cl, wd: wikidata.New()}
}

// Lookup — 한국어 말 하나를 분류군으로 조회한다.
//
// 경로: ko.wikipedia(리다이렉트 해소 → 표준명 + QID) → 위키데이터(P225 게이트 → 표기).
// 분류군이 아니면 ErrNotTaxon 으로 **아무 값도 돌려주지 않는다.**
func (c *TaxonClient) Lookup(ctx context.Context, term string) (*TaxonLookup, error) {
	term = strings.TrimSpace(term)
	if term == "" {
		return nil, errors.New("taxon: 빈 요청어")
	}
	page, err := koWikiLookup(ctx, c.http, term)
	if err != nil {
		return nil, err
	}
	if page == nil || len(page.Missing) > 0 || page.PageProps.WikibaseItem == "" {
		return nil, ErrNoKoWikiArticle
	}
	// 동음이의 문서는 대상이 아니다 — 어느 종인지 모른 채 표기를 실으면 틀린 종이 나간다.
	if page.PageProps.Disambiguation != nil {
		return nil, fmt.Errorf("taxon: 동음이의 문서 %q", page.Title)
	}
	qid := page.PageProps.WikibaseItem

	// ★BatchClaims 가 아니다. 학명은 문자열 claim 이라 그쪽 문으로는 한 건도 안 나온다
	//   (2026-09-20 실측: 이 자리를 BatchClaims 로 두고 끝단을 돌렸더니 전어·광어·우럭·
	//   도라지·거위벌레 다섯 건 전부 "분류군이 아니다"로 막혔다).
	claims, err := c.wd.BatchStringClaims(ctx, []string{qid}, []string{PropTaxonName})
	if err != nil {
		return nil, err
	}
	sci := firstClaim(claims[qid][PropTaxonName])
	if sci == "" {
		return nil, ErrNotTaxon
	}

	ent, err := c.wd.Fetch(ctx, qid)
	if err != nil {
		return nil, err
	}
	var siteTitles, labels map[string]string
	if ent != nil {
		siteTitles, labels = ent.SiteTitles, ent.Labels
	}
	byLocale, notes := TaxonLocaleNames(term, sci, siteTitles, labels)

	out := &TaxonLookup{
		Term:       term,
		StandardKO: page.Title,
		QID:        qid,
		Scientific: sci,
		ByLocale:   byLocale,
		Notes:      notes,
	}
	if page.Title != term {
		out.AliasKO = term
		out.Notes = append(out.Notes,
			fmt.Sprintf("통칭→표준명: %s → %s (요청어를 별칭으로 실어야 다음 요청이 만난다)", term, page.Title))
	}
	return out, nil
}

// firstClaim — 값 목록의 첫 항목. 비면 빈 문자열.
func firstClaim(vals []string) string {
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

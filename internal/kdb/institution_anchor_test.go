package kdb

import (
	"testing"

	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
)

// TestInstitutionAnchorRefusesTheThreeTraps — **위키데이터에서 실제로 돌아온 함정 셋.**
//
// 2026-09-20 실측. 이름으로만 찾으면 이런 것들이 정답인 척 돌아온다:
//
//	조국   → Q642555   "nation of one's 'fathers', 'forefathers'"  (일반명사 祖國)
//	교육부 → Q861556   "United States Department of Education"      (미국 부처)
//	FC서울 → Q27951528 "reserve team of FC Seoul"                   (2군, 문서는 "FC 서울 B")
//
// 셋 다 **이름은 정확히 일치한다.** 그래서 이름 일치는 근거가 못 된다.
func TestInstitutionAnchorRefusesTheThreeTraps(t *testing.T) {
	cases := []struct {
		name, ko, typ string
		ent           *wikidata.Entity
		wantWhy       string
	}{
		{
			name: "조국 — 일반명사(祖國) 항목",
			ko:   "조국", typ: "person",
			ent: &wikidata.Entity{
				QID:          "Q642555",
				Labels:       map[string]string{"ko": "조국", "en": "fatherland"},
				Descriptions: map[string]string{"en": "nation of one's 'fathers', 'forefathers'"},
				SiteTitles:   map[string]string{"kowiki": "조국"},
			},
			wantWhy: "korea", // 한국 대상이라는 근거가 어디에도 없다
		},
		{
			name: "교육부 — 미국 교육부",
			ko:   "교육부", typ: "government_body",
			ent: &wikidata.Entity{
				QID:          "Q861556",
				Labels:       map[string]string{"ko": "교육부", "en": "United States Department of Education"},
				Descriptions: map[string]string{"en": "United States federal government department"},
				SiteTitles:   map[string]string{"kowiki": "미국 교육부"},
			},
			wantWhy: "korea",
		},
		{
			name: "FC서울 — 2군 팀",
			ko:   "FC서울", typ: "sports_team",
			ent: &wikidata.Entity{
				QID:          "Q27951528",
				Labels:       map[string]string{"ko": "FC서울", "en": "FC Seoul B"},
				Descriptions: map[string]string{"en": "reserve team of FC Seoul, South Korea"},
				SiteTitles:   map[string]string{"kowiki": "FC 서울 B"},
			},
			wantWhy: "title", // 한국 것은 맞지만 **우리가 묻는 그 팀이 아니다**
		},
		{
			name: "한국어 문서가 없다 — 확인할 길이 없으므로 붙이지 않는다",
			ko:   "한국사회복지저널", typ: "publication",
			ent: &wikidata.Entity{
				QID:          "Q999999",
				Labels:       map[string]string{"ko": "한국사회복지저널"},
				Descriptions: map[string]string{"en": "South Korean journal"},
				SiteTitles:   map[string]string{},
			},
			wantWhy: "title",
		},
	}
	for _, c := range cases {
		why, ok := institutionAnchorOK(c.ent, c.ko, c.typ, false)
		if ok {
			t.Errorf("%s: 앵커를 붙이려 한다 — 틀린 값이 서빙된다", c.name)
			continue
		}
		if why != c.wantWhy {
			t.Errorf("%s: 걸린 이유가 %q, 기대 %q — 원인 집계가 틀어진다", c.name, why, c.wantWhy)
		}
	}
}

// TestInstitutionAnchorAcceptsTheRealOne — 막기만 하면 채우지 못한다.
func TestInstitutionAnchorAcceptsTheRealOne(t *testing.T) {
	ok := []struct {
		ko, typ string
		ent     *wikidata.Entity
	}{
		{"더불어민주당", "political_party", &wikidata.Entity{
			QID:          "Q15978686",
			Labels:       map[string]string{"ko": "더불어민주당", "en": "Democratic Party", "ja": "共に民主党", "zh": "共同民主黨"},
			Descriptions: map[string]string{"en": "South Korean political party"},
			SiteTitles:   map[string]string{"kowiki": "더불어민주당"},
		}},
		{"SK하이닉스", "company", &wikidata.Entity{
			QID:          "Q370719",
			Labels:       map[string]string{"ko": "SK하이닉스", "en": "SK Hynix"},
			Descriptions: map[string]string{"en": "South Korean memory semiconductor supplier"},
			SiteTitles:   map[string]string{"kowiki": "SK하이닉스"},
		}},
		// 문서 제목의 동음이의 괄호는 떼고 본다.
		{"서울대학교", "school", &wikidata.Entity{
			QID:          "Q39913",
			Labels:       map[string]string{"ko": "서울대학교", "en": "Seoul National University"},
			Descriptions: map[string]string{"en": "national research university in Seoul, South Korea"},
			SiteTitles:   map[string]string{"kowiki": "서울대학교 (법인)"},
		}},
	}
	for _, c := range ok {
		if why, pass := institutionAnchorOK(c.ent, c.ko, c.typ, false); !pass {
			t.Errorf("%s: 붙여야 하는데 %q 로 막혔다 — 채우지 못한다", c.ko, why)
		}
	}
}

// TestStripParenSuffixKeepsNamesThatContainParens — `f(x)` 를 `f` 로 만들면 안 된다.
func TestStripParenSuffixKeepsNamesThatContainParens(t *testing.T) {
	cases := map[string]string{
		"아이유 (가수)":   "아이유",
		"서울대학교 (법인)": "서울대학교",
		"f(x)":       "f(x)", // 괄호 앞에 공백이 없다 = 이름의 일부
		"ALL(H)OURS": "ALL(H)OURS",
		"더불어민주당":     "더불어민주당",
	}
	for in, want := range cases {
		if got := stripParenSuffix(in); got != want {
			t.Errorf("stripParenSuffix(%q) = %q, 기대 %q", in, got, want)
		}
	}
}

// TestStripParenAnnotationKeepsStructuralParens — 「티빙(TVING)」은 떼고 `f(x)` 는 둔다.
func TestStripParenAnnotationKeepsStructuralParens(t *testing.T) {
	cases := map[string]string{
		"티빙(TVING)":   "티빙",
		"iNKODE(인코드)": "iNKODE",
		"아이브(IVE)":    "아이브",
		"f(x)":        "f(x)",       // 앞이 한 글자 → 이름의 일부
		"ALL(H)OURS":  "ALL(H)OURS", // 괄호가 끝이 아니다
		"FC서울":        "FC서울",
	}
	for in, want := range cases {
		if got := stripParenAnnotation(in); got != want {
			t.Errorf("stripParenAnnotation(%q) = %q, 기대 %q", in, got, want)
		}
	}
}

// TestAbbrevRedirectNeedsTwoSourcesAgreeing — 약칭 리다이렉트는 두 출처가 같은 말을 할 때만.
//
// ★「농협」은 ko.wikipedia 에서 「농업협동조합」(일반 개념)으로 리다이렉트되고 그 영문은
// "Agricultural cooperative" 다. 리다이렉트만 믿으면 일반 개념의 영어 단어가 기업 칸에
// 들어간다. 위키데이터 별칭이 같은 말을 해 줄 때만 받는다.
func TestAbbrevRedirectNeedsTwoSourcesAgreeing(t *testing.T) {
	generic := &wikidata.Entity{ // 일반 개념 — 「농협」을 별칭으로 갖지 않는다
		QID:    "Q4118088",
		Labels: map[string]string{"ko": "농업협동조합"},
	}
	if hasKoAlias(generic, "농협") {
		t.Error("일반 개념 항목을 약칭으로 받아들인다 — 「Agricultural cooperative」가 기업 칸에 들어간다")
	}
	real := &wikidata.Entity{ // 정식 기관 — 약칭이 별칭에 있다
		QID:     "Q490382",
		Labels:  map[string]string{"ko": "건강보험심사평가원"},
		Aliases: map[string][]string{"ko": {"심평원", "심사평가원"}},
	}
	if !hasKoAlias(real, "심평원") {
		t.Error("정식 기관의 약칭을 못 알아본다 — 소비자가 쓰는 이름으로는 영영 못 찾는다")
	}
	if hasKoAlias(real, "전혀 다른 이름") {
		t.Error("아무 이름이나 별칭으로 인정한다")
	}
}

// TestIdentityKnownSkipsOnlyTheNameGate — 확인된 경로라도 **종류·국가는 그대로 본다.**
//
// kowiki 경로는 «우리 이름인가»만 답해 준다. 그 문서가 한국 것인지, 우리 유형과 맞는지는
// 여전히 물어야 한다 — 안 그러면 확인된 경로가 모든 가드를 통과하는 뒷문이 된다.
func TestIdentityKnownSkipsOnlyTheNameGate(t *testing.T) {
	// 이름은 확인됐지만 **미국** 기관.
	usa := &wikidata.Entity{
		QID:          "Q861556",
		Descriptions: map[string]string{"en": "United States federal government department"},
		SiteTitles:   map[string]string{"kowiki": "교육부"},
	}
	if why, ok := institutionAnchorOK(usa, "교육부", "government_body", true); ok {
		t.Error("확인된 경로라고 미국 기관까지 통과시킨다")
	} else if why != "korea" {
		t.Errorf("걸린 이유가 %q — 국가 가드여야 한다", why)
	}
	// 이름이 정본과 달라도(약칭·괄호) 확인된 경로면 통과한다.
	ok3 := &wikidata.Entity{
		QID:          "Q12621401",
		Descriptions: map[string]string{"en": "South Korean online streaming service"},
		SiteTitles:   map[string]string{"kowiki": "티빙"},
	}
	if why, ok := institutionAnchorOK(ok3, "티빙(TVING)", "channel_outlet", true); !ok {
		t.Errorf("괄호 병기 때문에 %q 로 막혔다 — 찾아 놓고 버린다", why)
	}
}

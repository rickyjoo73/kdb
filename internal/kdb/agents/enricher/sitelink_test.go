package enricher

import "testing"

func TestWikiTitleFromURL(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want string
	}{
		{"ja 퍼센트인코딩", "https://ja.wikipedia.org/wiki/%E6%9D%8E%E7%9F%A5%E6%81%A9", "李知恩"},
		{"zh 번체", "https://zh.wikipedia.org/wiki/%E9%BB%91%E6%9A%97%E6%A6%AE%E8%80%80", "黑暗榮耀"},
		{"en 밑줄은 공백으로", "https://en.wikipedia.org/wiki/Squid_Game", "Squid Game"},
		{"제목 안의 슬래시는 보존", "https://en.wikipedia.org/wiki/AC%2FDC", "AC/DC"},
		// 동음이의 한정어는 이름의 일부가 아니지만, 진짜 괄호를 가진 제목과 구분할
		// 방법이 없다. 떼어내는 규칙을 세우면 "Love (Inst.)" 를 훼손한다 → 통째로 버린다.
		{"괄호 한정어는 버림", "https://ja.wikipedia.org/wiki/%E6%9D%8E%E7%9F%A5%E6%81%A9_(%E6%AD%8C%E6%89%8B)", ""},
		{"전각 괄호도 버림", "https://zh.wikipedia.org/wiki/%E5%91%A8%E6%9D%B0%E5%80%AB%EF%BC%88%E6%AD%8C%E6%89%8B%EF%BC%89", ""},
		{"경로 없음", "https://ja.wikipedia.org", ""},
		{"빈 문자열", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := wikiTitleFromURL(c.url); got != c.want {
				t.Errorf("wikiTitleFromURL(%q) = %q, want %q", c.url, got, c.want)
			}
		})
	}
}

func TestSitelinkSpellings(t *testing.T) {
	in := map[string]string{
		"jawiki":           "https://ja.wikipedia.org/wiki/%E6%9D%8E%E7%9F%A5%E6%81%A9",
		"zhwiki":           "https://zh.wikipedia.org/wiki/IU",
		"kowiki":           "https://ko.wikipedia.org/wiki/%EC%95%84%EC%9D%B4%EC%9C%A0", // 대상 아님
		"zh_yuewiki":       "https://zh-yue.wikipedia.org/wiki/IU",                      // 광둥어 — 제외
		"zh_classicalwiki": "https://zh-classical.wikipedia.org/wiki/IU",                // 문언문 — 제외
	}
	got := sitelinkSpellings(in)
	if len(got) != 2 {
		t.Fatalf("로케일 2개(ja, zh)만 나와야 한다: %v", got)
	}
	if v := got["ja"]; len(v) != 1 || v[0] != "李知恩" {
		t.Errorf("ja = %v, want [李知恩]", v)
	}
	if v := got["zh"]; len(v) != 1 || v[0] != "IU" {
		t.Errorf("zh = %v, want [IU]", v)
	}
	// zh_hant 는 여기서 추측하지 않는다 — 바로 뒤 L3.5(OpenCC)가 결정적으로 파생한다.
	if _, has := got["zh_hant"]; has {
		t.Error("zh_hant 는 sitelink 에서 만들면 안 된다 (OpenCC 담당)")
	}
}

// fieldsCoveredBy 가 없으면 applyLocaleMap 이 want 전체에 tried 를 찍어, 소스가 값을
// 갖지도 않은 칸이 "시도했으나 실패"로 원장에 남는다 — a022567 이 고친 결함과 같은 것.
func TestFieldsCoveredBy_소스가_값을_가진_칸만(t *testing.T) {
	want := []string{"canonical_ja", "canonical_zh", "canonical_vi", "aliases_ko"}
	m := map[string][]string{"ja": {"李知恩"}}
	got := fieldsCoveredBy(want, m)
	if len(got) != 1 || got[0] != "canonical_ja" {
		t.Fatalf("fieldsCoveredBy = %v, want [canonical_ja]", got)
	}
}

func TestFieldsCoveredBy_빈값은_제외(t *testing.T) {
	want := []string{"canonical_ja"}
	if got := fieldsCoveredBy(want, map[string][]string{"ja": {}}); len(got) != 0 {
		t.Fatalf("빈 값 리스트는 커버로 치면 안 된다: %v", got)
	}
}

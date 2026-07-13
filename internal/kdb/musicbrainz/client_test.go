package musicbrainz

import "testing"

func TestNormalizeName(t *testing.T) {
	cases := map[string]string{
		"Park Bo-gum": "parkbogum",
		"park bo gum": "parkbogum",
		"パク・ボゴム":      "パクボゴム",
		" BTS ":       "bts",
		"방탄소년단":       "방탄소년단",
	}
	for in, want := range cases {
		if got := normalizeName(in); got != want {
			t.Errorf("normalizeName(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestNameMatches(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"LE SSERAFIM", "le-sserafim", true},
		{"(G)I-DLE", "G I DLE", false},
		{"MONSTER", "MONSTER", true},
		{"MONSTER", "Monster X", false},
		{"", "", false},
	} {
		if got := NameMatches(tc.a, tc.b); got != tc.want {
			t.Errorf("NameMatches(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func mkDetail(name string, aliases ...[2]string) *artistDetail {
	d := &artistDetail{Name: name}
	for _, a := range aliases {
		d.Aliases = append(d.Aliases, struct {
			Name    string `json:"name"`
			Locale  string `json:"locale"`
			Type    string `json:"type"`
			Primary bool   `json:"primary"`
		}{Name: a[0], Locale: a[1]})
	}
	return d
}

func TestMatchesQuery(t *testing.T) {
	// 진짜: 검색한 한국어 이름이 alias(ko) 로 존재 → 일치.
	right := mkDetail("IU", [2]string{"아이유", "ko"}, [2]string{"Lee Ji-eun", "en"})
	if !right.matchesQuery(normalizeName("아이유")) {
		t.Error("right artist (ko alias 아이유) should match query 아이유")
	}
	// primary name 으로 일치 (영문 질의).
	if !right.matchesQuery(normalizeName("iu")) {
		t.Error("right artist should match primary name query IU")
	}
	// 오매칭: 동명 검색에 엉뚱한 아티스트가 top hit — 어떤 표기도 query 와 불일치.
	wrong := mkDetail("Some Other Band", [2]string{"다른그룹", "ko"})
	if wrong.matchesQuery(normalizeName("아이유")) {
		t.Error("unrelated artist must NOT match query 아이유 (mis-match guard)")
	}
	// 빈 query 는 항상 false.
	if right.matchesQuery("") {
		t.Error("empty query must never match")
	}
}

func TestByLocale(t *testing.T) {
	d := mkDetail("BTS",
		[2]string{"방탄소년단", "ko"},
		[2]string{"防弾少年団", "ja"},
		[2]string{"Irrelevant", "fr"}, // 무관 locale → drop
	)
	out := d.byLocale()
	if got := out["ko"]; len(got) != 1 || got[0] != "방탄소년단" {
		t.Errorf("ko alias = %v; want [방탄소년단]", got)
	}
	if got := out["ja"]; len(got) != 1 || got[0] != "防弾少年団" {
		t.Errorf("ja alias = %v; want [防弾少年団]", got)
	}
	// primary name 은 en 으로 보존.
	if got := out["en"]; len(got) != 1 || got[0] != "BTS" {
		t.Errorf("en (primary) = %v; want [BTS]", got)
	}
	// fr 은 매핑되지 않아 drop.
	if _, ok := out["fr"]; ok {
		t.Error("fr locale should be dropped")
	}
}

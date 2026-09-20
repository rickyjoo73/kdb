package kdb

import (
	"strings"
	"testing"
)

// 아래 표는 2026-09-20 에 ko.wikipedia / wikidata 를 **직접 불러 받은 실제 값**이다.
// 손으로 지어낸 표본이 아니다 — 지어낸 표본으로 시험하면 시험만 통과한다.

func TestTaxonLocaleNames_우럭(t *testing.T) {
	site := map[string]string{
		"kowiki": "조피볼락",
		"jawiki": "クロソイ",
		"zhwiki": "許氏平鮋",
		"enwiki": "Sebastes schlegelii",
	}
	labels := map[string]string{"ko": "조피볼락", "ja": "クロソイ", "en": "Sebastes schlegelii"}

	got, _ := TaxonLocaleNames("우럭", "Sebastes schlegelii", site, labels)

	for locale, want := range map[string]string{
		"ja":      "クロソイ",
		"zh_hant": "許氏平鮋",
		"en":      "Sebastes schlegelii",
	} {
		if got[locale] != want {
			t.Errorf("%s = %q, want %q", locale, got[locale], want)
		}
	}
	if _, ok := got["ko"]; ok {
		t.Errorf("ko 칸은 채우지 않는다(원장의 canonical_ko 가 주인이다): %q", got["ko"])
	}
}

func TestTaxonLocaleNames_도라지_학명은_라틴권에서_정답(t *testing.T) {
	site := map[string]string{
		"kowiki": "도라지",
		"jawiki": "キキョウ",
		"zhwiki": "桔梗",
		"eswiki": "Platycodon grandiflorus",
		"idwiki": "Platycodon grandiflorus",
		"viwiki": "Cát cánh",
	}
	got, notes := TaxonLocaleNames("도라지", "Platycodon grandiflorus", site, nil)

	if got["es"] != "Platycodon grandiflorus" {
		t.Errorf("es = %q — eswiki 표제가 학명이면 그대로 둔다(bellota 사고의 반대편)", got["es"])
	}
	if got["ja"] != "キキョウ" || got["zh_hant"] != "桔梗" || got["vi"] != "Cát cánh" {
		t.Errorf("ja/zh_hant/vi 가 어긋났다: %+v", got)
	}
	if !hasNoteAbout(notes, "학명 그대로") {
		t.Errorf("학명을 그대로 실을 때는 단서를 남겨야 한다: %v", notes)
	}
}

func TestTaxonLocaleNames_거위벌레_층위_단서(t *testing.T) {
	site := map[string]string{
		"kowiki": "거위벌레",
		"jawiki": "オトシブミ科",
		"zhwiki": "卷叶象鼻虫科",
		"eswiki": "Attelabidae",
	}
	got, notes := TaxonLocaleNames("거위벌레", "Attelabidae", site, nil)

	if got["ja"] != "オトシブミ科" {
		t.Errorf("ja = %q, want オトシブミ科", got["ja"])
	}
	if !hasNoteAbout(notes, "층위 확인") {
		t.Errorf("ko 는 과(科)로 안 끝나는데 ja 표제가 科 면 단서를 남겨야 한다: %v", notes)
	}
}

func TestTaxonLocaleNames_한글값은_버린다(t *testing.T) {
	// 실제로 겪는 일이다 — langlink 에 ko 표제가 섞여 들어온다.
	site := map[string]string{"jawiki": "조피볼락", "zhwiki": "許氏平鮋"}
	got, _ := TaxonLocaleNames("우럭", "Sebastes schlegelii", site, nil)

	if v, ok := got["ja"]; ok {
		t.Errorf("한글이 든 값은 일본어 표기가 아니다. 버려야 하는데 %q 가 들어왔다", v)
	}
	if got["zh_hant"] != "許氏平鮋" {
		t.Errorf("한 칸이 틀렸다고 나머지를 버리면 안 된다: %+v", got)
	}
}

func TestTaxonLocaleNames_CJK칸의_라틴학명은_표기가_아니다(t *testing.T) {
	// jawiki 문서가 없으면 위키데이터 ja 라벨이 학명인 경우가 있다. 그건 결손이지 표기가 아니다.
	labels := map[string]string{"ja": "Sebastes schlegelii", "zh": "Sebastes schlegelii", "es": "Sebastes schlegelii"}
	got, _ := TaxonLocaleNames("우럭", "Sebastes schlegelii", nil, labels)

	if _, ok := got["ja"]; ok {
		t.Errorf("ja 칸에 라틴 학명을 실으면 안 된다: %q", got["ja"])
	}
	if _, ok := got["zh"]; ok {
		t.Errorf("zh 칸에 라틴 학명을 실으면 안 된다: %q", got["zh"])
	}
	if got["es"] != "Sebastes schlegelii" {
		t.Errorf("es 는 학명이 정답이다: %+v", got)
	}
}

func TestTaxonLocaleNames_요청어와_같은_값은_옮겨진_것이_아니다(t *testing.T) {
	site := map[string]string{"jawiki": "전어"}
	got, _ := TaxonLocaleNames("전어", "Konosirus punctatus", site, nil)
	if v, ok := got["ja"]; ok {
		t.Errorf("요청어와 같은 값은 버려야 한다: %q", v)
	}
}

func TestTaxonLocaleNames_문서제목이_라벨을_이긴다(t *testing.T) {
	// 9/19 zhwiki 레인과 같은 판단 — 언어판 표제가 그 언어 편집자들의 합의다.
	site := map[string]string{"zhwiki": "許氏平鮋"}
	labels := map[string]string{"zh_hant": "黑鮋"}
	got, _ := TaxonLocaleNames("우럭", "Sebastes schlegelii", site, labels)
	if got["zh_hant"] != "許氏平鮋" {
		t.Errorf("zh_hant = %q, want 許氏平鮋(문서 제목)", got["zh_hant"])
	}
}

func hasNoteAbout(notes []string, frag string) bool {
	for _, n := range notes {
		if strings.Contains(n, frag) {
			return true
		}
	}
	return false
}

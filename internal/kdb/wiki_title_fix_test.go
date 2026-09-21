package kdb

import (
	"os"
	"strings"
	"testing"
)

// TestEnwikiTitleFromURL — 원장 출처 URL 에서 문서 제목을 꺼낸다.
func TestEnwikiTitleFromURL(t *testing.T) {
	cases := map[string]string{
		"https://en.wikipedia.org/wiki/Bae_Yoon-gyu":           "Bae Yoon-gyu", // 실제 사례
		"https://en.wikipedia.org/wiki/IU_(singer)":            "IU",           // 동음이의 괄호
		"https://en.wikipedia.org/wiki/Kim_Soo-mi#Filmography": "Kim Soo-mi",   // 앵커 버림
		"https://en.wikipedia.org/wiki/f(x)_(group)":           "f(x)",         // 이름 속 괄호는 남긴다
		"https://en.wikipedia.org/wiki/Sassy_Girl_Chun-hyang":  "Sassy Girl Chun-hyang",
		"https://ko.wikipedia.org/wiki/배윤규":                    "", // enwiki 가 아니다
		"https://en.wikipedia.org/wiki/%EB%B0%B0":              "배",
	}
	for in, want := range cases {
		if got := enwikiTitleFromURL(in); got != want {
			t.Errorf("enwikiTitleFromURL(%q) = %q, 기대 %q", in, got, want)
		}
	}
}

// TestWikiTitleFixRequiresSameEntity — 출처 URL 을 **그냥 믿지 않는다.**
//
// ★실측 표본에서 링크 자체가 틀린 것이 나왔다:
//
//	이수 → "MC the Max"(그의 밴드 문서) · DK → "Dplus KIA"(e스포츠 팀 문서)
//
// 그대로 쓰면 사람 이름 칸에 밴드·게임팀 이름이 들어간다. 우리 앵커의 enwiki 문서가
// 곧 그 URL 문서일 때만 고친다.
func TestWikiTitleFixRequiresSameEntity(t *testing.T) {
	b, err := os.ReadFile("wiki_title_fix.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []struct{ frag, why string }{
		{`SiteTitles["enwiki"]`, "앵커의 enwiki 문서와 대조하지 않는다 — 틀린 링크를 그대로 믿는다"},
		{"r.NotSameEntity++", "다른 대상의 문서를 거르지 않는다"},
		{"can_replace_canonical", "우선순위를 안 본다 — 교정검증·권위 API·운영자 값을 덮는다"},
		{"kwave_kdb_dataqa_log", "되돌릴 기록을 안 남긴다"},
		{"tx.Rollback", "dry 가 쓰기를 실제로 해 보지 않는다 — 18건을 찾아 놓고 전부 못 넣은 일이 반복된다"},
	} {
		if !strings.Contains(src, want.frag) {
			t.Errorf("%s (없는 조각: %q)", want.why, want.frag)
		}
	}
}

// TestWikipediaTitleOutranksOnlyTheLabel — 새 출처는 **라벨만** 이긴다.
func TestWikipediaTitleOutranksOnlyTheLabel(t *testing.T) {
	if !(Priority(SourceWikipediaTitle) < Priority(SourceWikidataLabel)) {
		t.Error("wikipedia-title 이 wikidata-label 을 못 이긴다 — 배윤규가 안 고쳐진다")
	}
	for _, s := range []Source{SourceMusicBrainz, SourceCorrectionVerified, SourceTMDb} {
		if Priority(SourceWikipediaTitle) < Priority(s) {
			t.Errorf("wikipedia-title 이 %s 를 덮는다 — 권위 API·교정검증 값을 건드리면 안 된다", s)
		}
	}
	if Priority(SourceWikipediaTitle) <= Priority(SourceOperatorLocked) {
		t.Error("wikipedia-title 이 운영자 잠금 이상이다")
	}
}

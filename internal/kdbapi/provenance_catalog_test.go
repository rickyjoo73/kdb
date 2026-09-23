package kdbapi

import (
	"strings"
	"testing"
)

// localeProvenanceLabel 이 낼 수 있는 라벨은 **전부 목록에 있어야 한다.**
//
// ★이 시험이 있는 이유. 2026-09-23 에 소비자 세 곳이 «문서에 없는 라벨이 온다»고
// 각자 신고했다. 라벨은 늘었는데 문서는 안 늘었기 때문이고, 그 이유는 둘이 따로
// 적혀 있었기 때문이다. 이제 문서가 provenanceCatalog 에서 만들어지므로, 여기서
// 「코드가 내는 라벨 ⊆ 목록」만 지키면 문서는 저절로 맞는다.
func TestEveryServedProvenanceLabelIsCatalogued(t *testing.T) {
	// localeProvenanceLabel 이 분기하는 raw source 를 모두 넣는다. 새 source 를
	// 추가하면서 이 목록에 안 넣으면, 적어도 새 라벨은 아래 catalog 검사가 잡는다.
	sources := []string{
		"operator-locked", "operator", "wikidata-label",
		"tmdb", "musicbrainz", "kofic", "kmdb", "naver-people", "correction-verified",
		"netflix", "disney", "itunes", "discogs",
		"media-consensus", "romanization", "kana-rule", "opencc", "mydramalist",
		"gtranslate", "gtranslate-raw", "codex-fallback", "llm-provisional",
		"wikipedia-langlinks", "wikipedia-ko", "rss-observation", "", "모르는-소스",
	}
	known := map[string]bool{}
	for _, g := range provenanceCatalog {
		known[g.Label] = true
	}
	for _, s := range sources {
		label := localeProvenanceLabel(Entity{}, s)
		if !known[label] {
			t.Errorf("source %q 가 목록에 없는 라벨 %q 를 낸다 — 소비자는 이 값을 «모르는 값»으로 받는다", s, label)
		}
	}
	// operator_locked 엔티티는 source 와 무관하게 operator-locked 다.
	if got := localeProvenanceLabel(Entity{OperatorLocked: true}, "gtranslate"); got != "operator-locked" {
		t.Errorf("operator_locked 인데 %q", got)
	}
}

// 문서의 «검증 여부» 칸과 실제 게이트가 갈리면, 문서를 믿고 짠 게이팅이 틀린다.
func TestCatalogVerifiedMatchesTheGate(t *testing.T) {
	for _, g := range provenanceCatalog {
		if g.Verified != provenanceIsVerified(g.Label) {
			t.Errorf("%s: 문서는 verified=%v 라 하는데 게이트는 %v 다", g.Label, g.Verified, provenanceIsVerified(g.Label))
		}
	}
	// 게이트가 통과시키는 라벨이 목록 밖에 있으면 «검증인데 문서에 없는 값»이 된다.
	for label := range verifiedProvenances {
		found := false
		for _, g := range provenanceCatalog {
			if g.Label == label {
				found = true
			}
		}
		if !found {
			t.Errorf("검증 통과 라벨 %q 가 문서 목록에 없다", label)
		}
	}
}

// 렌더된 문서에 라벨이 전부 나오는지 — 자리표가 치환되지 않으면 여기서 걸린다.
func TestDocsRenderTheWholeProvenanceCatalog(t *testing.T) {
	page := strings.Replace(docsHTML, provenanceSlot, renderProvenanceHTML(), 1)
	if strings.Contains(page, provenanceSlot) {
		t.Fatal("출처 등급 자리표가 그대로 남았다 — 문서에 표가 안 나간다")
	}
	for _, g := range provenanceCatalog {
		if !strings.Contains(page, "<code>"+g.Label+"</code>") {
			t.Errorf("문서에 %q 라벨이 없다", g.Label)
		}
	}
}

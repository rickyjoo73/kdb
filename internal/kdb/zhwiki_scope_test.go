package kdb

import (
	"os"
	"strings"
	"testing"
)

// TestZhWikiSelectAndUpdateAgree — 고르는 조건과 쓰는 조건이 같은지.
//
// ★iTunes 레인에서 데인 자리다(2026-09-18). SELECT 과 UPDATE 중 한쪽만 넓히면
// 행은 뽑히는데 한 건도 안 써진다 — 로그에는 «0건 성공»으로 남는다. 실측으로
// 1,025건이 그렇게 버려지고 있었다. 두 자리가 같은 목록을 보는지 못 박는다.
func TestZhWikiSelectAndUpdateAgree(t *testing.T) {
	src, err := os.ReadFile("zhwiki_title_drain.go")
	if err != nil {
		t.Fatalf("zhwiki_title_drain.go 읽기 실패: %v", err)
	}
	body := string(src)
	const sel = `AND (COALESCE(e.canonical_zh,'') = '' OR COALESCE(e.canonical_zh_source,'') = ANY($2::text[]))`
	const upd = `OR (COALESCE(canonical_zh_source,'') = ANY($3::text[]) AND $2 ~ '[一-鿿]')`
	if !strings.Contains(body, sel) {
		t.Error("SELECT 이 기계값을 보지 않는다 — zh.wikipedia 제목이 있는데도 기계값이 남는다")
	}
	if !strings.Contains(body, upd) {
		t.Error("UPDATE 가 기계값을 덮지 않는다 — 뽑아만 놓고 아무것도 안 쓴다(조용한 0건)")
	}
	// 두 자리 모두 같은 목록(MachineFilledSourcesWeakerThan)을 인자로 받아야 한다.
	if strings.Count(body, "MachineFilledSourcesWeakerThan(SourceWikipediaSitelink)") < 2 {
		t.Error("SELECT·UPDATE 가 같은 기계값 목록을 쓰지 않는다")
	}
}

// TestZhWikiUpgradesOnlyWeakerSources — 권위 값을 덮지 않는지.
//
// wikipedia-sitelink 는 prio 6 이다. 그보다 강한 출처(wikidata-label·tmdb·
// operator-locked)를 덮으면 «권위를 기계로 되돌리는》 사고가 된다.
func TestZhWikiUpgradesOnlyWeakerSources(t *testing.T) {
	weaker := MachineFilledSourcesWeakerThan(SourceWikipediaSitelink)
	for _, s := range []string{"gtranslate", "codex-fallback", "romanization", "opencc"} {
		found := false
		for _, w := range weaker {
			if w == s {
				found = true
			}
		}
		if !found {
			t.Errorf("%q 가 교체 대상에서 빠졌다 — zh.wikipedia 제목이 더 정확하다", s)
		}
	}
	for _, s := range []string{"wikidata-label", "tmdb", "operator-locked", "wikipedia-sitelink"} {
		for _, w := range weaker {
			if w == s {
				t.Errorf("%q 를 교체 대상에 넣었다 — 권위 값을 덮는다", s)
			}
		}
	}
}

// TestZhWikiNeverReplacesHanWithLatin — 한자 칸을 로마자로 덮지 않는지.
//
// 실측(2026-09-19): 기계값까지 교체 대상으로 넓혔더니 26건이 망가졌다. 중국어
// 위키백과는 K-팝 그룹 문서를 라틴 제목으로 달기 때문이다(RIIZE·GOT7·ITZY).
//
//	빅뱅    zh=BIGBANG   ← 우리 번체는 大爆炸樂隊
//	시우민  zh=Xiumin    ← 우리 번체는 金珉錫
//	악뮤    zh=AKMU      ← 우리 번체는 樂童音樂家
//
// 하루 전 en-fallback 을 뺀 것과 같은 이유다 — 한자 칸의 로마자는 «못 찾았다»가
// 아니라 «틀린 표기»다. 빈칸을 채우는 것은 종전대로 둔다.
func TestZhWikiNeverReplacesHanWithLatin(t *testing.T) {
	src, err := os.ReadFile("zhwiki_title_drain.go")
	if err != nil {
		t.Fatalf("zhwiki_title_drain.go 읽기 실패: %v", err)
	}
	body := string(src)
	if !strings.Contains(body, `AND $2 ~ '[一-鿿]'`) {
		t.Error("교체 경로에 한자 조건이 없다 — 한자 값을 로마자로 덮는다")
	}
	// 빈칸 채우기는 조건 없이 유지돼야 한다.
	if !strings.Contains(body, `COALESCE(canonical_zh,'')=''`) {
		t.Error("빈칸 채우기 경로가 사라졌다")
	}
}


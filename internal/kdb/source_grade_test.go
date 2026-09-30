package kdb

import "testing"

// gtranslate-raw 는 **가장 약한 등급**이어야 한다 (2026-09-14 방침).
// 게이트가 흠을 잡은 기계번역이므로, 흠 없는 기계번역조차 이것을 덮어야 한다.
// 등급이 뒤집히면 약한 값이 강한 값을 밀어내고 그 순간 원장이 나빠진다.
func TestGTranslateRawIsWeakestGrade(t *testing.T) {
	raw := Priority(SourceGTranslateRaw)
	if raw != 10 {
		t.Fatalf("Priority(gtranslate-raw) = %d, want 10", raw)
	}
	for _, s := range []Source{
		SourceGTranslate, SourceCodexFallback, SourceKanaRule, SourceRomanization,
		SourceWikidataLabel, SourceTMDb, SourceOperatorLocked, SourceMediaConsensus,
		SourceLocalSearch, SourceOpenCC, SourceConsumerSuggestion,
	} {
		if Priority(s) >= raw {
			t.Errorf("%s(%d) 가 gtranslate-raw(%d) 를 업그레이드하지 못한다", s, Priority(s), raw)
		}
	}
	// unknown 보다는 나아야 한다 — 출처가 적혀 있는 값이니까.
	if raw >= Priority(SourceUnknown) {
		t.Errorf("gtranslate-raw(%d) 가 unknown(%d) 보다 낮다", raw, Priority(SourceUnknown))
	}
}

// ★소비자 제안은 근거 있는 출처와 우리 기계값 **사이**다 (2026-09-30, 오너).
//
//	"우리가 찾다가 못 찾으면 우선 사용" — 근거 있는 출처(1~7)는 제안을 덮고, 제안은
//	우리가 이름만 보고 만든 값(LLM·기계번역·가나규칙)을 덮는다. 어느 쪽이 뒤집혀도
//	오너가 정한 순서가 깨진다.
func TestConsumerSuggestionSitsBetweenEvidenceAndOurGuesses(t *testing.T) {
	cs := Priority(SourceConsumerSuggestion)
	for _, s := range []Source{
		SourceOperatorLocked, SourceMediaConsensus, SourceTMDb, SourceWikidataLabel,
		SourceWikipediaLanglinks, SourceKoWikiHanja, SourceLocalSearch, SourceRomanization,
		SourceOpenCC, SourceMyDramaList,
	} {
		if Priority(s) >= cs {
			t.Errorf("근거 있는 출처 %s(%d) 가 소비자 제안(%d) 을 덮지 못한다", s, Priority(s), cs)
		}
	}
	for _, s := range []Source{
		SourceCodexFallback, SourceGTranslate, SourceKanaRule, SourceLLMProvisional, SourceGTranslateRaw,
	} {
		if Priority(s) <= cs {
			t.Errorf("우리 기계값 %s(%d) 가 소비자 제안(%d) 보다 앞이다", s, Priority(s), cs)
		}
		if rep, _ := ShouldReplace(s, "ours", SourceConsumerSuggestion, "theirs"); !rep {
			t.Errorf("소비자 제안이 %s 값을 덮지 못한다", s)
		}
	}
	// 제안이 덮을 수 있는 칸 목록 = 우리 기계값 전부, 제안 자신은 빠진다.
	weaker := MachineFilledSourcesWeakerThan(SourceConsumerSuggestion)
	want := map[string]bool{"codex-fallback": true, "gtranslate": true, "gtranslate-raw": true,
		"kana-rule": true, "llm-provisional": true}
	if len(weaker) != len(want) {
		t.Errorf("제안이 덮을 칸 = %v, want %v", weaker, want)
	}
	for _, w := range weaker {
		if !want[w] {
			t.Errorf("제안이 %s 를 덮는다 — 근거 있는 출처다", w)
		}
	}
}

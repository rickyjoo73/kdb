package kdb

import "testing"

// gtranslate-raw 는 **가장 약한 등급**이어야 한다 (2026-09-14 방침).
// 게이트가 흠을 잡은 기계번역이므로, 흠 없는 기계번역조차 이것을 덮어야 한다.
// 등급이 뒤집히면 약한 값이 강한 값을 밀어내고 그 순간 원장이 나빠진다.
func TestGTranslateRawIsWeakestGrade(t *testing.T) {
	raw := Priority(SourceGTranslateRaw)
	if raw != 9 {
		t.Fatalf("Priority(gtranslate-raw) = %d, want 9", raw)
	}
	for _, s := range []Source{
		SourceGTranslate, SourceCodexFallback, SourceKanaRule, SourceRomanization,
		SourceWikidataLabel, SourceTMDb, SourceOperatorLocked, SourceMediaConsensus,
		SourceLocalSearch, SourceOpenCC,
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

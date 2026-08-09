package kdb

import "testing"

// 폭 0 서식문자 회귀 — **전부 2026-08-09 DB 실측값**이다(0113 으로 제거한 13칸).
// 리터럴로 쓰면 보이지 않으므로 테스트에서도 코드포인트로 조립한다.
func TestZeroWidthRejected(t *testing.T) {
	cases := []struct {
		loc, base string
		bad       rune
		why       string
	}{
		{"en", "Billlie", '\u200E', "빌리 en — LRM (correction-verified 로 들어옴)"},
		{"es", "Won Woo", '\u200E', "원우 es — LRM (wikidata-label)"},
		{"ja", "ザ・エイトショー", '\uFEFF', "더 에이트 쇼 ja — BOM (wikidata-label)"},
		{"ja", "トンネル", '\uFEFF', "tmdb 유입분과 같은 유형"},
		{"zh", "秀允", '\u202C', "수윤 zh_hant — bidi PDF, opencc 가 zh 로 복사해 증폭"},
		{"vi", "Oh My Baby", '\u200B', "오! 마이 베이비 vi — ZWSP (tmdb)"},
	}
	for _, c := range cases {
		clean := c.base
		dirty := c.base + string(c.bad)
		if !IsValidSpellingForLocale(c.loc, clean) {
			t.Errorf("깨끗한 값 %q(%s)가 거부됐다 — 가드가 정상값을 죽인다: %s", clean, c.loc, c.why)
		}
		if IsValidSpellingForLocale(c.loc, dirty) {
			t.Errorf("폭0 문자 U+%04X 가 섞인 %q(%s)가 통과했다: %s", c.bad, dirty, c.loc, c.why)
		}
	}
}

// 일반 공백은 막으면 안 된다 — 조회 정규식에 일반 공백이 섞여 수천 건 오탐을 낸 적이 있다.
func TestOrdinarySpaceStillAllowed(t *testing.T) {
	for _, s := range []string{"Song Kang-ho", "Oh My Girl", "1% of Anything"} {
		if !IsValidSpellingForLocale("en", s) {
			t.Errorf("일반 공백이 든 %q 가 거부됐다 — 폭0 가드가 과잉이다", s)
		}
	}
	// NBSP(U+00A0)는 목록에 없다 — 실측 0건이고, 있더라도 제거가 아니라 공백 치환이 맞다.
	if !IsValidSpellingForLocale("en", "Song Kang-ho") {
		t.Error("NBSP 를 거부하면 안 된다(0113 주석 참조) — 치환 대상이지 거부 대상이 아니다")
	}
}

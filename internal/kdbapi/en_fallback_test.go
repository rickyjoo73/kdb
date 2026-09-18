package kdbapi

import "testing"

// TestEnFallbackNeverEntersCJK — 영어를 한자·가나 칸에 넣지 않는지.
//
// 실측(2026-09-18): verified_only 로 서울대학교를 물으면 이렇게 나갔다.
//
//	"zh": "Seoul National University"   provenance "en-fallback"
//
// 중국어 소비자에게 영어가 중국어라고 나간 것이다. 우리 채움 프롬프트는 영어를 다른
// 로케일에 복사하면 «TRANSLATION FAILURE》라고 못 박아 두고 서빙에서 그걸 하고 있었다.
func TestEnFallbackNeverEntersCJK(t *testing.T) {
	for _, loc := range []string{"ja", "zh", "zh_hant"} {
		if isFallbackLocale(loc) {
			t.Errorf("%s 가 영어 폴백 대상이다 — 한자·가나 칸에 로마자가 나간다", loc)
		}
	}
	// 라틴 로케일은 로마자가 실제 통용 표기라 폴백이 옳다.
	for _, loc := range []string{"vi", "es", "id", "pt_br"} {
		if !isFallbackLocale(loc) {
			t.Errorf("%s 가 폴백에서 빠졌다 — 라틴권은 로마자가 자연 표기다", loc)
		}
	}
	if isFallbackLocale("en") {
		t.Error("en 이 자기 자신으로 폴백한다")
	}
}

// TestCJKFallsBackToBlankNotEnglish — 실제 서빙 경로에서 CJK 가 빈칸으로 빠지는지.
//
// 오너 지시(2026-09-17): "우리쪽에서 공식을 사용을 못찾으면 그대로 놔두어야
// llm 직번역이라도 할수 있도록". 영어를 채워 두면 번역 쪽이 자기 번역을 할 기회를 뺏는다.
func TestCJKFallsBackToBlankNotEnglish(t *testing.T) {
	e := Entity{
		CanonicalKO:       "서울대학교",
		CanonicalEN:       "Seoul National University",
		CanonicalENSource: "wikidata-label",
		// zh/ja 는 비어 있다.
	}
	values, prov, missing := localeValuesAndGaps(e, []string{"en", "ja", "zh", "vi"}, false)

	if got := values["zh"]; got != "" {
		t.Errorf("zh 에 %q 가 들어갔다 — 빈칸이어야 한다 (prov=%q)", got, prov["zh"])
	}
	if got := values["ja"]; got != "" {
		t.Errorf("ja 에 %q 가 들어갔다 — 빈칸이어야 한다 (prov=%q)", got, prov["ja"])
	}
	if values["vi"] != "Seoul National University" {
		t.Errorf("vi 폴백이 사라졌다: %q — 라틴권은 로마자가 옳다", values["vi"])
	}
	var sawZh, sawJa bool
	for _, m := range missing {
		if m == "zh" {
			sawZh = true
		}
		if m == "ja" {
			sawJa = true
		}
	}
	if !sawZh || !sawJa {
		t.Errorf("CJK 빈칸이 missing 으로 통지되지 않았다: %v", missing)
	}
}

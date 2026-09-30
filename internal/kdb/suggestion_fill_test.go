package kdb

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// 소비자가 보내는 로케일 키는 제각각이다 — 우리 칸 이름으로 모여야 한다.
func TestNormalizeSuggestionLocale(t *testing.T) {
	for in, want := range map[string]string{
		"en": "en", "JA": "ja", "zh": "zh", "zh-Hans": "zh", "zh-CN": "zh",
		"zh-hant": "zh_hant", "zh_hant": "zh_hant", "zh-TW": "zh_hant",
		"pt-BR": "pt_br", "pt_br": "pt_br", "vi": "vi", "id": "id", "es": "es",
		"ko": "", "fr": "", "": "",
	} {
		if got := NormalizeSuggestionLocale(in); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}

// 인명의 직역은 틀린 값이다 — 음역 제안만 쓴다.
func TestPickSuggestionRefusesLiteralPersonName(t *testing.T) {
	c := []suggestionCand{{value: "River East Garden", basis: "literal", producer: "p1", seen: 5}}
	if got, why := pickSuggestion("en", "person", c); got.value != "" || why != "literal-name" {
		t.Errorf("인명 직역을 골랐다: %+v %q", got, why)
	}
	// 작품 제목은 직역이 맞는 자리다.
	if got, _ := pickSuggestion("en", "drama", c); got.value != "River East Garden" {
		t.Errorf("작품 제목 직역을 버렸다: %+v", got)
	}
	// 같은 인명이라도 음역이면 쓴다.
	c = append(c, suggestionCand{value: "Kang Dong-won", basis: "transliteration", producer: "p2", seen: 1})
	if got, _ := pickSuggestion("en", "person", c); got.value != "Kang Dong-won" {
		t.Errorf("음역을 고르지 않았다: %+v", got)
	}
}

// 우리가 일부러 지운 값은 소비자 제안으로 다시 들어오지 않는다.
func TestPickSuggestionRefusesRemovedValue(t *testing.T) {
	c := []suggestionCand{{value: "Bob Girls", basis: "official", producer: "p1", seen: 9, removed: true}}
	if got, why := pickSuggestion("en", "song_album", c); got.value != "" || why != "removed-before" {
		t.Errorf("지운 값을 되살렸다: %+v %q", got, why)
	}
}

// 간체 칸에 번체, 표기에 한글 — 문자셋이 어긋나면 쓰지 않는다.
func TestPickSuggestionChecksCharset(t *testing.T) {
	if got, why := pickSuggestion("zh", "drama", []suggestionCand{{value: "我獨自生活", basis: "literal", producer: "p"}}); got.value != "" || why != "bad-charset" {
		t.Errorf("간체 칸에 번체를 골랐다: %+v %q", got, why)
	}
	if got, _ := pickSuggestion("ja", "drama", []suggestionCand{{value: "사랑이 온다", basis: "literal", producer: "p"}}); got.value != "" {
		t.Errorf("일본어 칸에 한글을 골랐다: %+v", got)
	}
}

// 순위: 방식(official > transliteration > literal) → 제작처 합의 → 본 횟수.
func TestPickSuggestionRanking(t *testing.T) {
	c := []suggestionCand{
		{value: "Love Is Coming", basis: "literal", producer: "a", seen: 50},
		{value: "Love Comes", basis: "literal", producer: "b", seen: 1},
		{value: "Love Comes", basis: "literal", producer: "c", seen: 1},
	}
	if got, _ := pickSuggestion("en", "drama", c); got.value != "Love Comes" {
		t.Errorf("두 제작처가 합의한 값을 고르지 않았다: %+v", got)
	}
	c = append(c, suggestionCand{value: "Love Is Here", basis: "official", producer: "d", seen: 1})
	if got, _ := pickSuggestion("en", "drama", c); got.value != "Love Is Here" {
		t.Errorf("공식 표기를 고르지 않았다: %+v", got)
	}
}

// 레인은 기본으로 켜져 있고, 배선 목록에 올라 있어야 한다.
func TestSuggestionFillWired(t *testing.T) {
	t.Setenv("KDB_SUGGESTION_FILL_ENABLED", "")
	if !SuggestionFillEnabled() {
		t.Error("기본값이 꺼짐이다")
	}
	t.Setenv("KDB_SUGGESTION_FILL_ENABLED", "0")
	if SuggestionFillEnabled() {
		t.Error("0 으로 꺼지지 않는다")
	}
	found := false
	for _, l := range WiredLanes {
		found = found || l == "suggestion-fill"
	}
	if !found {
		t.Error("suggestion-fill 이 WiredLanes 에 없다")
	}
	b, err := os.ReadFile("../../cmd/kdb/main.go")
	if err != nil {
		t.Fatalf("main.go 를 못 읽었다: %v", err)
	}
	if strings.Count(string(b), "runAutonomousSuggestionFill(ctx, pool)") < 2 {
		t.Error("autopilot 분리·일반 두 경로에 다 붙지 않았다")
	}
}

// 판정 모델은 소비자 제안을 재료로 봐야 한다 — 없으면 처음부터 짓는다.
func TestReviewJudgePromptCarriesSuggestions(t *testing.T) {
	p := buildReviewJudgePrompt(reviewTerm{Ko: "사랑이 온다", Requests: 3,
		Suggest: "ja=ラブ・イズ・カミング (literal, presslocale)"})
	for _, w := range []string{"ラブ・イズ・カミング", "presslocale", "소비자 제안 표기는", "그대로 써라"} {
		if !strings.Contains(p, w) {
			t.Errorf("프롬프트에 %q 가 없다", w)
		}
	}
}

// 제안이 들어갈 수 있는 칸: 빈 칸, 그리고 우리가 이름만 보고 만든 기계값. 근거 있는 칸은 아니다.
func TestSuggestionReplaceable(t *testing.T) {
	for _, src := range []string{"codex-fallback", "gtranslate", "gtranslate-raw", "kana-rule", "llm-provisional"} {
		if !suggestionReplaceable("x", src) {
			t.Errorf("%s 칸을 제안으로 못 바꾼다 — 오너 순서(제안 > 우리 기계값)가 깨진다", src)
		}
	}
	for _, src := range []string{"operator-locked", "tmdb", "wikidata-label", "local-search", "romanization",
		"opencc", "media-consensus", "consumer-suggestion", "wikipedia-langlinks"} {
		if suggestionReplaceable("x", src) {
			t.Errorf("%s 칸을 제안이 덮는다 — 근거 있는 값이다", src)
		}
	}
	if !suggestionReplaceable("", "tmdb") || !suggestionReplaceable("  ", "") {
		t.Error("빈 칸을 못 채운다")
	}
}

// SQL 의 칸 식이 Go 정규화와 같은 표에서 나와야 한다 — 로케일 변형이 SQL 에서 빠지면 조용히 0건.
func TestSuggestionCellSQLCoversEveryKey(t *testing.T) {
	v, src := suggestionCellSQL(false), suggestionCellSQL(true)
	for _, k := range []string{"'en'", "'ja'", "'vi'", "'zh'", "'zh_hans'", "'zh_cn'", "'zh_hant'", "'zh_tw'",
		"'es'", "'id'", "'pt_br'", "'pt'"} {
		if !strings.Contains(v, k) {
			t.Errorf("칸 식에 %s 가 없다", k)
		}
	}
	if !strings.Contains(v, "e.canonical_zh_hant,") || !strings.Contains(src, "e.canonical_zh_hant_source") {
		t.Errorf("번체 칸 식이 틀렸다:\n%s\n%s", v, src)
	}
	if strings.Contains(v, "_source") {
		t.Error("값 식에 출처 칸이 섞였다")
	}
	// ★단순형 CASE 의 `WHEN 'a','b'` 는 Postgres 문법이 아니다 — 격리 DB 에서 문법 오류로
	//   잡혔다(배포됐으면 레인 선정이 매 회차 실패). 검색형 `WHEN x IN (…)` 만 허용한다.
	if regexp.MustCompile(`WHEN '[^']*',`).MatchString(v) || !strings.Contains(v, " IN ('zh','zh_cn','zh_hans','zh_sg')") {
		t.Errorf("칸 식이 검색형 CASE 가 아니다:\n%s", v)
	}
}

// LLM 이 칸을 채우는 네 자리가 모두 제안을 **먼저** 본다 — 한 자리라도 빠지면 그 경로로는
// 우리 LLM 값이 먼저 들어간다(뒤에 레인이 바꾸긴 하지만 GPT 호출을 버린다).
func TestSuggestionsAppliedBeforeOurLLM(t *testing.T) {
	for f, before := range map[string]string{
		"agents/enricher/layers.go":     "// L4 LLM(gemma)",
		"enrich/orchestrator.go":        "// L4: Codex LLM fallback",
		"agents/enricher/demand_cjk.go": "role.BuildPrompt(",
	} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("%s 를 못 읽었다: %v", f, err)
		}
		src := string(b)
		i, j := strings.Index(src, "ApplySuggestionsToEntity("), strings.Index(src, before)
		if i < 0 || j < 0 || i > j {
			t.Errorf("%s: 제안 적용이 LLM(%q) 보다 앞에 없다", f, before)
		}
	}
	b, err := os.ReadFile("../kdbapi/prepare_suggestions.go")
	if err != nil {
		t.Fatalf("prepare_suggestions.go: %v", err)
	}
	if !strings.Contains(string(b), "kdb.ApplySuggestionsToEntity(") {
		t.Error("prepare 즉시 채움이 공용 규칙을 쓰지 않는다 — 규칙이 두 벌이 된다")
	}
}

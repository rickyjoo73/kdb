package codexcli

import (
	"os"
	"strings"
	"testing"
)

func TestWithEffort(t *testing.T) {
	base := &Runner{Effort: "high"}
	got := base.WithEffort("low")
	if got.Effort != "low" {
		t.Fatalf("WithEffort(low) = %q, want low", got.Effort)
	}
	if base.Effort != "high" {
		t.Fatalf("WithEffort mutated receiver: %q", base.Effort)
	}
	if base.WithEffort("").Effort != "high" {
		t.Fatal("WithEffort(\"\") must keep the original effort")
	}
	var nilR *Runner
	if nilR.WithEffort("low") != nil {
		t.Fatal("WithEffort on nil must stay nil")
	}
}

func TestRoleEffort(t *testing.T) {
	if got := RoleEffort("EXTRACT", "low"); got != "low" {
		t.Fatalf("default = %q, want low", got)
	}
	t.Setenv("CODEX_EFFORT_EXTRACT", "minimal")
	if got := RoleEffort("EXTRACT", "low"); got != "minimal" {
		t.Fatalf("env override = %q, want minimal", got)
	}
	os.Unsetenv("CODEX_EFFORT_EXTRACT")
}

// ★공급자 라우팅 계약 (2026-09-16 저녁에 한 번 더 바뀌었다).
//
//	9-15: gemma 로만 간다 — codex 가 실제로 죽어 있었는데(브리지 없음 · auth.json 없음 ·
//	      7일간 호출 0) 그리로 폴백해서 gemma 장애가 "분류 보류"로 조용히 삼켜졌다.
//	9-16 낮: 실행 경로까지 걷어냈다. 껍데기가 사람을 속였기 때문이다.
//	9-16 저녁: **판정 역할 하나에 한해** 되살렸다(운영자 지시). 정정 검증은 근거를
//	      하나도 안 주는 순수 지식 과제라 모델 힘이 직접 늘고, 하루 21건 · 프롬프트
//	      300~400 토큰으로 짧고 적다.
//
//	그래서 지금 계약은 «절대 안 간다》가 아니라 **«기본은 gemma, 역할이 가리킬 때만
//	codex, 그것도 일일 상한 안에서》** 다. 그리고 어느 쪽이 답했는지 감추지 않는다.
func TestRoutingDefaultsToGemmaAndFollowsRole(t *testing.T) {
	os.Unsetenv("KDB_LLM_TESTROLE")
	defer func() { GemmaDown, CodexDown = nil, nil }()

	// 기본은 gemma. 역할 설정이 없으면 codex 로 가지 않는다.
	if got := RoleProvider("TESTROLE", "gemma"); got != "gemma" {
		t.Errorf("기본 gemma → %q", got)
	}
	// 역할이 가리키면 따른다 — 그게 이번에 되살린 것이다.
	t.Setenv("KDB_LLM_TESTROLE", "codex")
	if got := RoleProvider("TESTROLE", "gemma"); got != "codex" {
		t.Errorf("env=codex → %q, 라우팅이 안 먹는다", got)
	}
	os.Unsetenv("KDB_LLM_TESTROLE")
}

func TestCapRunes(t *testing.T) {
	if got := capRunes("abc", 5); got != "abc" {
		t.Fatalf("short string changed: %q", got)
	}
	// 멀티바이트(한글)도 rune 기준으로 잘려야 한다(바이트 중간 절단 금지).
	long := strings.Repeat("가", 10)
	got := capRunes(long, 4)
	if r := []rune(got); len(r) != 5 || string(r[:4]) != strings.Repeat("가", 4) {
		t.Fatalf("capRunes rune-truncate failed: %q", got)
	}
}

// zh/zh-hant 라틴 금지 규칙이 fill 프롬프트에 명시돼야 한다(레거시 오염 재발 방지).
func TestFillLocalePromptHasZhScriptRule(t *testing.T) {
	p := BuildFillLocalePrompt("박보검", "person", "actor", nil,
		map[string]string{"en": "Park Bo-gum"}, []string{"zh", "zh-hant"}, nil, nil)
	if !strings.Contains(p, "Simplified Chinese") || !strings.Contains(p, "Traditional Chinese") {
		t.Fatal("prompt must state zh=Simplified, zh-hant=Traditional Han requirement")
	}
	if !strings.Contains(p, "NEVER output a Latin romanization for zh") {
		t.Fatal("prompt must forbid Latin romanization for zh/zh-hant")
	}
}

// 비서비스 위키 sitelink 는 프롬프트에서 제외돼 토큰을 아껴야 한다.
func TestFillLocalePromptFiltersIrrelevantWiki(t *testing.T) {
	sl := map[string]string{
		"jawiki":  "https://ja.wikipedia.org/wiki/X",
		"frwiki":  "https://fr.wikipedia.org/wiki/X",
		"ruwiki":  "https://ru.wikipedia.org/wiki/X",
	}
	p := BuildFillLocalePrompt("X", "person", "", nil, nil, []string{"ja"}, nil, sl)
	if !strings.Contains(p, "jawiki") {
		t.Fatal("service-locale wiki (jawiki) must be kept")
	}
	if strings.Contains(p, "frwiki") || strings.Contains(p, "ruwiki") {
		t.Fatal("non-service wiki sitelinks must be filtered out")
	}
}

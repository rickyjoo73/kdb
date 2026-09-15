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

// TestRoleProviderSelfHealMesh locks the two-way self-healing routing:
// Gemma down → codex; Codex down → gemma (only when gemma is configured).
// 공급자는 **절대 codex 로 가지 않는다** (운영자 지시 2026-09-15 "codex 를 걷어내").
//
// ★이 시험은 종전에 정반대를 고정하고 있었다 — gemma 가 죽으면 codex 로 폴백하고
//   codex 가 죽으면 gemma 로 인계하는 그물. 그런데 codex 는 실제로 죽어 있었다
//   (브리지 컨테이너 없음 · auth.json 없음 · 7일간 호출 0). **죽은 곳으로 폴백**했고,
//   그래서 gemma 장애가 "분류 보류(합성 unknown)"로 조용히 삼켜졌다.
//
//   지금 계약: gemma 로만 간다. 설정이 codex 를 가리켜도 따르지 않는다.
func TestProviderNeverRoutesToCodex(t *testing.T) {
	os.Unsetenv("KDB_LLM_TESTROLE")
	defer func() { GemmaDown, CodexDown = nil, nil }()

	if got := RoleProvider("TESTROLE", "codex"); got != "gemma" {
		t.Errorf("기본값 codex → %q, gemma 여야 한다", got)
	}
	t.Setenv("KDB_LLM_TESTROLE", "codex")
	if got := RoleProvider("TESTROLE", "gemma"); got != "gemma" {
		t.Errorf("env=codex → %q, gemma 여야 한다", got)
	}
	os.Unsetenv("KDB_LLM_TESTROLE")

	// gemma 가 죽었다고 **죽은 곳으로 넘기지 않는다.** 조용한 폴백보다 시끄러운 실패가 낫다.
	GemmaDown = func() bool { return true }
	if got := RoleProvider("TESTROLE", "gemma"); got == "codex" {
		t.Error("gemma 장애를 codex 로 넘겼다 — 그쪽은 걷어냈다")
	}
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

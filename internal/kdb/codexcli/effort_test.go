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
func TestRoleProviderSelfHealMesh(t *testing.T) {
	// isolate from any KDB_LLM_* env the host set.
	os.Unsetenv("KDB_LLM_TESTROLE")
	defer func() { GemmaDown, CodexDown = nil, nil }()

	// baseline: no hooks → returns def verbatim.
	GemmaDown, CodexDown = nil, nil
	if got := RoleProvider("TESTROLE", "codex"); got != "codex" {
		t.Fatalf("baseline codex = %q, want codex", got)
	}

	// Gemma down → gemma-routed role falls back to codex.
	GemmaDown = func() bool { return true }
	if got := RoleProvider("TESTROLE", "gemma"); got != "codex" {
		t.Fatalf("gemma-down fallback = %q, want codex", got)
	}
	GemmaDown = nil

	// Codex down + gemma configured → codex-routed role falls back to gemma.
	t.Setenv("KDB_GEMMA_BASE_URL", "http://gemma.local")
	t.Setenv("KDB_GEMMA_API_KEY", "test-key")
	CodexDown = func() bool { return true }
	if got := RoleProvider("TESTROLE", "codex"); got != "gemma" {
		t.Fatalf("codex-down fallback = %q, want gemma", got)
	}

	// Codex down but gemma NOT configured → stay codex (let caller error path run).
	os.Unsetenv("KDB_GEMMA_BASE_URL")
	os.Unsetenv("KDB_GEMMA_API_KEY")
	if got := RoleProvider("TESTROLE", "codex"); got != "codex" {
		t.Fatalf("codex-down without gemma = %q, want codex", got)
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

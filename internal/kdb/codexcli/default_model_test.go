package codexcli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ★운영은 GPT-6 이다 (2026-09-30). 기본 모델이 두 자리로 갈려 있었고 한쪽이 "gpt-5.5" 였다 —
//
//	설정이 비는 순간 낡은 이름으로 호출해 전부 400 → 조용히 gemma. 기본값은 하나여야 하고
//	ChatGPT 계정 경로가 받는 접미사 달린 이름이어야 한다.
func TestDefaultModelIsGPT6(t *testing.T) {
	if !strings.HasPrefix(DefaultModel, "gpt-6-") {
		t.Errorf("기본 모델이 %q — gpt-6-<접미사> 여야 한다", DefaultModel)
	}
	t.Setenv("CODEX_MODEL", "")
	t.Setenv("CODEX_BRIDGE_MODEL", "")
	if m := NewRunner().Model; m != DefaultModel {
		t.Errorf("설정이 비었을 때 NewRunner 가 %q 를 쓴다 — DefaultModel(%q)과 다르다", m, DefaultModel)
	}
}

// 에이전트 결과 이름표에 모델 이름을 박지 않는다 — 답한 쪽은 라우팅·상한에 따라 바뀐다.
func TestAgentsDoNotHardcodeModelName(t *testing.T) {
	for _, f := range []string{
		"../agents/disambiguator/decide.go",
		"../agents/gatekeeper/agent.go",
		"../agents/personextractor/agent.go",
		"codexcli.go",
	} {
		b, err := os.ReadFile(filepath.FromSlash(f))
		if err != nil {
			t.Fatalf("%s 를 못 읽었다: %v", f, err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			code := line
			if i := strings.Index(code, "//"); i >= 0 {
				code = code[:i]
			}
			if strings.Contains(code, `"gpt-5.5"`) {
				t.Errorf("%s 에 낡은 모델 이름이 박혀 있다: %s", f, strings.TrimSpace(line))
			}
		}
	}
}

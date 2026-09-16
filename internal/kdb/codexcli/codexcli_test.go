package codexcli

import (
	"context"
	"os"
	"strings"
	"testing"
)

// ★codex 를 폐기했다 (운영자 지시 2026-09-16: "codex 사용은 폐기해, 사용이 안 되도록").
//
//	2026-09-15 에 라우팅만 gemma 로 돌리고 `KDB_CODEX_ALLOW=1` 이라는 문을 남겼는데,
//	실행 코드·CLI·인증 마운트가 전부 남아 **쓰는 것처럼 보였다.** 실제로 그 착시에
//	한 번 걸렸다 — 컨테이너의 codex 0.146.0 을 손으로 불러 401 을 받고는 "앱이 codex 를
//	부르는데 전부 실패한다"고 보고했다. 앱은 codex 를 부르지 않는다.
//
//	이 시험이 지키는 것은 **그 껍데기가 다시 자라지 않는 것**이다.
//	여기 있던 옛 시험들(토큰 만료 게이트·flock 직렬화)은 지킬 코드가 없어졌으므로 뺐다.
func TestCodexExecPathIsGone(t *testing.T) {
	b, err := os.ReadFile("codexcli.go")
	if err != nil {
		t.Fatalf("codexcli.go 를 못 읽었다: %v", err)
	}
	src := string(b)
	for _, gone := range []struct{ token, why string }{
		{"exec.Command", "codex 프로세스를 띄우는 코드"},
		{"os/exec", "프로세스 실행 import"},
		{"KDB_CODEX_ALLOW", "옛 경로를 되살리는 문"},
		{"--skip-git-repo-check", "codex CLI 인자"},
		{"output-last-message", "codex CLI 인자"},
		{"CODEX_BIN", "codex 실행 파일 지정"},
		{"CODEX_HOME", "codex 인증 디렉터리"},
		{"auth.json", "codex 인증 파일"},
	} {
		if strings.Contains(src, gone.token) {
			t.Errorf("%s 가 남아 있다(%s) — 폐기가 덜 됐고, 다음 사람이 살아 있다고 읽는다",
				gone.token, gone.why)
		}
	}
}

// gemma 가 없으면 **없다고 말한다.** 조용히 폴백할 곳이 없고, 있어서도 안 된다 —
// 죽은 곳으로 넘기면 장애가 "분류 보류"로 삼켜져 품질만 조용히 떨어진다.
func TestRunFailsLoudlyWithoutGemma(t *testing.T) {
	t.Setenv("KDB_GEMMA_BASE_URL", "")
	t.Setenv("GEMMA_BASE_URL", "")
	// 옛 문을 열어 봐도 codex 로 가지 않는다.
	t.Setenv("KDB_CODEX_ALLOW", "1")
	r := &Runner{Provider: "codex"}
	_, err := r.Run(context.Background(), "prompt", []byte(`{}`))
	if err == nil {
		t.Fatal("gemma 가 없는데 오류를 안 냈다 — 어딘가로 조용히 넘어갔다는 뜻이다")
	}
	if !strings.Contains(err.Error(), "gemma") {
		t.Errorf("오류가 이유를 말하지 않는다: %v", err)
	}
}

// RoleProvider 는 설정이 아직 codex 를 가리켜도 gemma 로 돌린다.
// 배포에 남은 KDB_LLM_* 환경변수를 다 걷어낼 때까지의 안전판이다.
func TestRoleProviderNeverReturnsCodex(t *testing.T) {
	t.Setenv("KDB_LLM_DISAMBIG", "codex")
	if got := RoleProvider("DISAMBIG", "codex"); got != "gemma" {
		t.Errorf("RoleProvider=%q — 설정이 codex 를 가리켜도 gemma 여야 한다", got)
	}
	if got := RoleProvider("NOSUCHROLE", "codex"); got != "gemma" {
		t.Errorf("기본값이 codex 일 때 %q — gemma 여야 한다", got)
	}
}

// Package codexcli — direct codex CLI transport + prompt builders.
//
// This package replaces the former Node codex-bridge (scripts/codex_bridge/server.mjs):
// it execs the `codex` CLI directly with the same flags and reads the
// --output-last-message file. It MUST NOT import anything from internal/kdb
// to avoid an import cycle (internal/kdb and internal/kdb/aijudge both import it).
//
// The Run method replicates server.mjs runCodex exactly. The three Build*Prompt
// functions port buildPrompt / buildClassifyPrompt / buildFillLocalePrompt
// verbatim — only JS template interpolation is translated to Go.
package codexcli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// codexGate serializes ALL codex CLI invocations in this process to a single
// concurrent run. 운영자 방침: 인증은 codex CLI(ChatGPT 로그인)만 쓰고 API 키는
// 절대 쓰지 않는다. ChatGPT OAuth 의 refresh token 은 1회용(rotating)이라, 같은
// auth.json 을 공유하는 codex 프로세스가 동시에 토큰을 refresh 하면 한쪽이
// `refresh_token_reused` 401 로 무효화되어 백엔드 전체가 죽는다 (2026-06-03 발생).
// OpenAI 공식 가이드: "use one auth.json per SERIALIZED workflow stream; do not
// share across concurrent jobs." → codex exec 를 직렬화해 동시 refresh 자체를
// 없앤다. 반드시 크기 1 (동시성 2 이상이면 refresh 경쟁이 다시 생긴다).
var codexGate = make(chan struct{}, 1)

// Runner — codex CLI invoker. Mirrors the env knobs server.mjs read.
type Runner struct {
	Bin        string        // CODEX_BIN, default "codex"
	Model      string        // CODEX_MODEL or CODEX_BRIDGE_MODEL, default "gpt-5.5"
	Timeout    time.Duration // CODEX_BRIDGE_TIMEOUT_MS ms, default 90s
	ForceModel bool          // CODEX_BRIDGE_FORCE_MODEL == "1"
}

// NewRunner — reads CODEX_BIN, CODEX_MODEL/CODEX_BRIDGE_MODEL,
// CODEX_BRIDGE_TIMEOUT_MS, CODEX_BRIDGE_FORCE_MODEL.
func NewRunner() *Runner {
	bin := os.Getenv("CODEX_BIN")
	if bin == "" {
		bin = "codex"
	}
	model := os.Getenv("CODEX_MODEL")
	if model == "" {
		model = os.Getenv("CODEX_BRIDGE_MODEL")
	}
	if model == "" {
		model = "gpt-5.5"
	}
	timeout := 90 * time.Second
	if v := os.Getenv("CODEX_BRIDGE_TIMEOUT_MS"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			timeout = time.Duration(ms) * time.Millisecond
		}
	}
	return &Runner{
		Bin:        bin,
		Model:      model,
		Timeout:    timeout,
		ForceModel: os.Getenv("CODEX_BRIDGE_FORCE_MODEL") == "1",
	}
}

// Run — exec `codex exec ...`, feed prompt on stdin, return the parsed
// last-message JSON. Replicates server.mjs runCodex exactly.
func (r *Runner) Run(ctx context.Context, prompt string, schema []byte) (json.RawMessage, error) {
	if r == nil {
		return nil, fmt.Errorf("codexcli: nil runner")
	}
	bin := r.Bin
	if bin == "" {
		bin = "codex"
	}
	model := r.Model
	if model == "" {
		model = "gpt-5.5"
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 90 * time.Second
	}

	workDir, err := os.MkdirTemp("", "codex-bridge-")
	if err != nil {
		return nil, fmt.Errorf("codexcli: mkdtemp: %w", err)
	}
	defer os.RemoveAll(workDir)

	schemaPath := filepath.Join(workDir, "schema.json")
	if err := os.WriteFile(schemaPath, schema, 0o600); err != nil {
		return nil, fmt.Errorf("codexcli: write schema: %w", err)
	}
	lastMsgFile := filepath.Join(workDir, "last.txt")

	// 직렬화 게이트 획득 (동시 토큰 refresh 방지). 대기는 부모 ctx 를 존중하고,
	// per-run 타임아웃은 게이트 확보 이후에 시작한다 (대기 중 타임아웃 소진 방지).
	select {
	case codexGate <- struct{}{}:
		defer func() { <-codexGate }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	args := []string{
		"exec",
		"--model", model,
		"--sandbox", "read-only",
		"--skip-git-repo-check",
		"--ephemeral",
		"--ignore-user-config",
		"--ignore-rules",
		"--output-schema", schemaPath,
		"--output-last-message", lastMsgFile,
		"--color", "never",
		"-C", workDir,
		"-",
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, bin, args...)
	cmd.Env = os.Environ()
	cmd.Stdin = strings.NewReader(prompt)
	// Drain stdout; we read the last-message file instead.
	cmd.Stdout = nil
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	// Own process group so the timeout can kill the whole tree.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Kill the entire process group (negative pid).
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}

	runErr := cmd.Run()
	if runCtx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("codex timeout after %dms", timeout.Milliseconds())
	}
	if runErr != nil {
		tail := lastStderrLines(stderr.String(), 5)
		if tail == "" {
			tail = "(no stderr)"
		}
		return nil, fmt.Errorf("codex exit %s: %s", exitCode(runErr), tail)
	}

	raw, err := os.ReadFile(lastMsgFile)
	if err != nil {
		return nil, fmt.Errorf("codex produced no last-message file")
	}
	txt := strings.TrimSpace(string(raw))
	if txt == "" {
		return nil, fmt.Errorf("codex last-message file empty")
	}
	if !json.Valid([]byte(txt)) {
		return nil, fmt.Errorf("codex last-message not valid JSON")
	}
	return json.RawMessage(txt), nil
}

func exitCode(err error) string {
	if ee, ok := err.(*exec.ExitError); ok {
		return strconv.Itoa(ee.ExitCode())
	}
	return err.Error()
}

// lastStderrLines — last n non-empty lines joined with " | " (mirrors
// server.mjs: stderr.split('\n').filter(Boolean).slice(-5).join(' | ')).
func lastStderrLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	nonEmpty := make([]string, 0, len(lines))
	for _, ln := range lines {
		if ln != "" {
			nonEmpty = append(nonEmpty, ln)
		}
	}
	if len(nonEmpty) > n {
		nonEmpty = nonEmpty[len(nonEmpty)-n:]
	}
	return strings.Join(nonEmpty, " | ")
}

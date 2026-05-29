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

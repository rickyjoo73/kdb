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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/rickyjoo73/kdb/internal/kdb/gemma"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// warnNoFlockOnce — cross-process codex 직렬화가 비활성된 경우 1회만 경고.
var warnNoFlockOnce sync.Once

// 운영자 방침: 인증은 codex CLI(ChatGPT 로그인)만 쓰고 API 키는 절대 안 쓴다.
// 그리고 codex gpt-5.5 에이전트들은 동시에 각자 일을 해야 한다(번역 외 모든 작업이
// codex). 과거엔 전역 채널게이트 크기 1 로 ALL codex 호출을 직렬화했는데, 이는
// "ChatGPT OAuth refresh token 이 1회용이라 동시 refresh 시 401 무효화"(2026-06-03
// 사고)를 막기 위함이었다. 그러나 실측 결과 access token 의 JWT exp 는 발급 후 약
// 9일(예: 2026-06-13)로 매우 길어, 평소엔 codex 가 refresh 를 아예 하지 않는다
// (auth.json mtime 불변 + 동시 2호출 둘 다 성공으로 확인, 2026-06-04). 따라서 위험은
// "만료 임박 순간의 동시 refresh" 뿐 — exec 동시성 자체가 아니다.
//
// 정책:
//   - 평상시(만료까지 여유): codexSem 으로 N 동시 실행 허용(KDB_CODEX_CONCURRENCY).
//   - 만료 임박(refresh 윈도우 이내) 또는 exp 판독 불가: codexRefreshGate(크기 1)
//   - CODEX_HOME flock 으로 단일화 → 단 한 프로세스/고루틴만 refresh 하게 해
//     refresh_token_reused 경쟁을 차단. refresh 가 일어나면 exp 가 연장되어 자동으로
//     평상시 모드(N 동시)로 복귀한다.
var (
	codexSem         = make(chan struct{}, codexConcurrency())
	codexRefreshGate = make(chan struct{}, 1)
)

// codexConcurrency — KDB_CODEX_CONCURRENCY (기본 4, 최소 1). 평상시 동시 codex 수.
func codexConcurrency() int {
	if v := os.Getenv("KDB_CODEX_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			return n
		}
	}
	return 4
}

// codexRefreshWindow — 만료까지 이 시간 이내면 단일화 모드. 기본 6h
// (KDB_CODEX_REFRESH_WINDOW_HOURS).
func codexRefreshWindow() time.Duration {
	if v := os.Getenv("KDB_CODEX_REFRESH_WINDOW_HOURS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Hour
		}
	}
	return 6 * time.Hour
}

// access token exp 캐시 (auth.json 매 호출 재파싱 방지, 5분 TTL).
var (
	expMu       sync.Mutex
	cachedExp   time.Time
	cachedExpAt time.Time
)

// accessTokenExp — CODEX_HOME/auth.json 의 access_token(JWT) exp 를 읽는다.
// 토큰값은 절대 반환/로깅하지 않고 만료시각만 본다. 판독 실패 시 ok=false.
func accessTokenExp() (time.Time, bool) {
	expMu.Lock()
	defer expMu.Unlock()
	if !cachedExp.IsZero() && time.Since(cachedExpAt) < 5*time.Minute {
		return cachedExp, true
	}
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		return time.Time{}, false
	}
	b, err := os.ReadFile(filepath.Join(home, "auth.json"))
	if err != nil {
		return time.Time{}, false
	}
	var a struct {
		Tokens struct {
			AccessToken string `json:"access_token"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(b, &a); err != nil {
		return time.Time{}, false
	}
	exp, ok := jwtExp(a.Tokens.AccessToken)
	if !ok {
		return time.Time{}, false
	}
	cachedExp, cachedExpAt = exp, time.Now()
	return exp, true
}

// jwtExp — JWT 의 payload 에서 exp claim 만 디코드. 서명검증 없음(만료시각 참고용).
func jwtExp(tok string) (time.Time, bool) {
	parts := strings.Split(tok, ".")
	if len(parts) < 2 {
		return time.Time{}, false
	}
	p := parts[1]
	if m := len(p) % 4; m != 0 {
		p += strings.Repeat("=", 4-m)
	}
	raw, err := base64.URLEncoding.DecodeString(p)
	if err != nil {
		return time.Time{}, false
	}
	var c struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(raw, &c); err != nil || c.Exp == 0 {
		return time.Time{}, false
	}
	return time.Unix(c.Exp, 0), true
}

// acquireCodexSlot — 토큰 만료 상태에 따라 동시(N) 또는 단일(refresh 보호) 슬롯을
// 잡고 해제 함수를 돌려준다. ctx 취소를 존중한다.
func acquireCodexSlot(ctx context.Context) (func(), error) {
	nearRefresh := true // exp 판독 불가 시 보수적으로 단일화.
	if exp, ok := accessTokenExp(); ok {
		nearRefresh = time.Until(exp) < codexRefreshWindow()
	}
	if nearRefresh {
		// 만료 임박/불명: 단 하나만 진행시켜 동시 refresh 경쟁을 차단.
		select {
		case codexRefreshGate <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		unlock, err := acquireCodexFileLock(ctx)
		if err != nil {
			<-codexRefreshGate
			return nil, err
		}
		return func() { unlock(); <-codexRefreshGate }, nil
	}
	// 평상시: N 동시. access token 이 fresh 라 codex 가 refresh 하지 않는다.
	select {
	case codexSem <- struct{}{}:
		return func() { <-codexSem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Runner — codex CLI invoker. Mirrors the env knobs server.mjs read.
type Runner struct {
	Bin        string        // CODEX_BIN, default "codex"
	Model      string        // CODEX_MODEL or CODEX_BRIDGE_MODEL, default "gpt-5.5"
	Timeout    time.Duration // CODEX_BRIDGE_TIMEOUT_MS ms, default 90s
	ForceModel bool          // CODEX_BRIDGE_FORCE_MODEL == "1"
	// Effort — CODEX_REASONING_EFFORT (minimal|low|medium|high|xhigh). 빈 값이면
	// codex 기본값. --ignore-user-config 로 config.toml 을 무시하므로 reasoning
	// effort 는 반드시 `-c model_reasoning_effort=` 로 명시 전달해야 적용된다
	// (이전엔 어디서도 안 넘겨 medium env 가 no-op 이었음).
	Effort string
	// Provider — 이 runner 의 LLM 백엔드 강제("codex"|"gemma"). 빈 값이면 전역
	// KDB_LLM_PROVIDER. role 별 하이브리드 라우팅용(고난도=codex, 대량=gemma).
	Provider string
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
		Effort:     os.Getenv("CODEX_REASONING_EFFORT"),
	}
}

// WithEffort — Effort 만 다른 shallow copy 를 반환한다. effort 가 비면 그대로.
// role 별 reasoning effort 차등(토큰 절감)용 — 동시성 세마포어/락은 패키지
// 전역이라 복사본도 공유한다.
func (r *Runner) WithEffort(effort string) *Runner {
	if r == nil || strings.TrimSpace(effort) == "" {
		return r
	}
	cp := *r
	cp.Effort = effort
	return &cp
}

// WithProvider — Provider 만 다른 shallow copy. role 별 백엔드 라우팅용.
func (r *Runner) WithProvider(p string) *Runner {
	if r == nil || strings.TrimSpace(p) == "" {
		return r
	}
	cp := *r
	cp.Provider = p
	return &cp
}

// GemmaDown — 자율 폴백 훅(2026-06-20). Gemma 게이트웨이 헬스 모니터(internal/kdb
// gemma_health.go)가 연속 실패로 breaker 를 열면 이 함수가 true 를 돌려준다. import
// cycle 회피를 위해 hook 패턴(main.go 가 kdb.GemmaHealthy 로 와이어). nil 이면 평소처럼
// 동작(폴백 없음). RoleProvider 가 gemma 라우팅 role 을 codex 로 폴백하는 데 쓴다.
var GemmaDown func() bool

// RoleProvider — role 별 LLM 백엔드 결정. KDB_LLM_<ROLE> env > def. 고난도 role
// (DISAMBIG/DATAQA/FILL 등)은 codex, 대량/단순은 gemma 로 라우팅하는 데 쓴다.
// Gemma 가 다운(GemmaDown)이면 gemma 라우팅을 codex 로 자동 폴백(완전자율 자가복구).
func RoleProvider(role, def string) string {
	p := def
	if v := strings.TrimSpace(os.Getenv("KDB_LLM_" + role)); v != "" {
		p = v
	}
	if strings.EqualFold(p, "gemma") && GemmaDown != nil && GemmaDown() {
		return "codex" // Gemma 게이트웨이 장애 — Codex 로 폴백(복구되면 자동 환원)
	}
	return p
}

// RoleEffort — role 별 reasoning effort 결정. 우선순위:
// CODEX_EFFORT_<ROLE> env > def(코드 기본값) > ""(호출측이 WithEffort 에 ""
// 를 넘기면 전역 CODEX_REASONING_EFFORT 유지). 단순 추출/이진 분류 role 은
// 낮은 effort 로도 품질이 유지되어 토큰을 크게 아낀다.
func RoleEffort(role, def string) string {
	if v := strings.TrimSpace(os.Getenv("CODEX_EFFORT_" + role)); v != "" {
		return v
	}
	return def
}

// Run — exec `codex exec ...`, feed prompt on stdin, return the parsed
// last-message JSON. Replicates server.mjs runCodex exactly.
func (r *Runner) Run(ctx context.Context, prompt string, schema []byte) (json.RawMessage, error) {
	if r == nil {
		return nil, fmt.Errorf("codexcli: nil runner")
	}
	// LLM provider 디스패치(하이브리드 라우팅): runner.Provider 우선, 없으면 전역
	// KDB_LLM_PROVIDER. "gemma" 면 gemma 게이트웨이(빠름·대량), 그 외/codex 면 codex
	// CLI(고난도 판단·공식명/번역). 고난도 role(disambig/dataqa/fill)은 WithProvider
	// ("codex")로 codex 강제. 신뢰는 호출측 가드가 보장 — 동일 등급으로 다룬다.
	provider := strings.TrimSpace(r.Provider)
	if provider == "" {
		provider = strings.TrimSpace(os.Getenv("KDB_LLM_PROVIDER"))
	}
	if strings.EqualFold(provider, "gemma") && gemma.Configured() {
		return gemma.Complete(ctx, prompt, schema)
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

	// 동시성 슬롯 획득. 평상시엔 N 동시(codexSem), 토큰 만료 임박 시엔 단일화
	// (codexRefreshGate + flock)해 동시 refresh 경쟁만 차단한다. 대기는 부모 ctx 를
	// 존중하고, per-run 타임아웃은 슬롯 확보 이후에 시작한다(대기 중 소진 방지).
	release, err := acquireCodexSlot(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	args := []string{
		"exec",
		"--model", model,
		"--sandbox", "read-only",
		"--skip-git-repo-check",
		"--ephemeral",
		"--ignore-user-config",
		"--ignore-rules",
	}
	// reasoning effort: --ignore-user-config 로 config.toml 이 무시되므로 CLI 로
	// 명시 전달해야 적용된다. 빈 값이면 codex 기본값 사용(플래그 미부착).
	if r.Effort != "" {
		args = append(args, "-c", "model_reasoning_effort="+r.Effort)
	}
	args = append(args,
		"--output-schema", schemaPath,
		"--output-last-message", lastMsgFile,
		"--color", "never",
		"-C", workDir,
		"-",
	)

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, bin, args...)
	// codex 자식에게 부모 전체 env 를 그대로 넘기지 않는다: 우리 비밀(KDB_* —
	// DB 비번/세션 시크릿/소비자 키/외부 API 키)은 codex 가 쓸 일이 없으므로 제거.
	// OPENAI_API_KEY 도 제거 — 운영자 방침상 API 키 미사용이며, 존재 시 codex 가
	// ChatGPT 로그인 대신 API 모드로 전환할 수 있어 명시적으로 차단한다.
	cmd.Env = sanitizedEnv()
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

// sanitizedEnv — os.Environ() 에서 codex 가 불필요한 우리 비밀을 걸러낸다.
// codex 에 필요한 PATH/HOME/CODEX_HOME/CODEX_* 등은 그대로 유지.
func sanitizedEnv() []string {
	src := os.Environ()
	out := make([]string, 0, len(src))
	for _, kv := range src {
		name := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			name = kv[:i]
		}
		if strings.HasPrefix(name, "KDB_") || name == "OPENAI_API_KEY" {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// acquireCodexFileLock — CODEX_HOME/.codex-exec.lock 에 배타적 flock 을 건다.
// 같은 CODEX_HOME 을 공유하는 모든 프로세스(워커 + one-shot 서브커맨드) 간 codex
// 실행을 직렬화해 동시 토큰 refresh 를 막는다. LOCK_NB + 짧은 폴링으로 ctx 취소 존중.
// CODEX_HOME 미설정/lock 불가 시엔 nil unlock(프로세스 내 채널 게이트로만 보호).
func acquireCodexFileLock(ctx context.Context) (func(), error) {
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		// CODEX_HOME 없으면 프로세스-간 직렬화 불가 → 별도 프로세스가 동시에 codex 를
		// 띄우면 토큰 race 재발 가능. 채널 게이트(프로세스 내)만으로 진행하되 경고.
		warnNoFlockOnce.Do(func() {
			log.Printf("codexcli: WARNING CODEX_HOME unset — cross-process codex 직렬화 비활성(in-process gate only); 별도 one-shot 과 동시 실행 시 토큰 race 위험")
		})
		return func() {}, nil
	}
	path := filepath.Join(home, ".codex-exec.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		warnNoFlockOnce.Do(func() {
			log.Printf("codexcli: WARNING lock 파일 open 실패(%s): %v — cross-process 직렬화 비활성", path, err)
		})
		return func() {}, nil
	}
	for {
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			return func() {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				_ = f.Close()
			}, nil
		}
		select {
		case <-time.After(200 * time.Millisecond):
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		}
	}
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

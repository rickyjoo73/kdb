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

// CodexDown — GemmaDown 의 거울(2026-06-22). Codex CLI/bridge 가 연속 실패해
// circuit breaker(internal/kdb bridge_health.go BreakerIsOpen)가 열리면 true.
// import cycle 회피로 hook 패턴(main.go 가 kdb.BreakerIsOpen 으로 와이어). codex
// 라우팅 role 을 로컬 상시가동 gemma 로 자동 인계(자가복구) — 양방향 메시.
// 오너 방침(2026-06-22): codex 사용률은 최대한 낮추고 품질 critical role(동명이인/
// 정정검증)만 codex, 그 외엔 gemma. Codex 장애 시엔 critical 도 gemma 로 인계해
// 파이프라인이 멈추지 않게 한다(degraded-but-done, 복구되면 자동 환원).
var CodexDown func() bool

// RoleProvider — role 별 LLM 백엔드 결정. KDB_LLM_<ROLE> env > def. 품질 critical
// role(DISAMBIG/CORRECTION 등)만 codex, 대량/단순은 gemma 로 라우팅한다.
// 양방향 자가복구: Gemma 다운(GemmaDown)→codex, Codex 다운(CodexDown)→gemma.
// (둘 다 다운인 극단 케이스는 원래 def 를 유지해 호출측이 정상 에러 경로를 타게 둠.)
// ★codex 를 걷어낸다 (운영자 지시 2026-09-15).
//
//   "codex 를 걷어내. 사용하지 않고, 실제로 고유명사를 prepare 에 제공하는 것이
//    codex gpt-5.6-sol 이라 신뢰해도 돼. 차라리 그것을 활용해야지."
//
//   고유명사를 뽑아 보내는 일은 **소비자 쪽 GPT 가 이미 한다.** KDB 가 같은 일을
//   자기 안에서 또 하면 판단 주체가 둘이 되고, 둘이 어긋나면 어느 쪽이 맞는지 가릴
//   근거가 없다. KDB 는 받은 고유명사에 **표기와 근거**를 붙이는 쪽이다.
//
// ★그리고 이 경로는 실제로 죽어 있었다(2026-09-15 실측):
//     codex-bridge 컨테이너 없음 · CODEX_HOME/auth.json 없음 · 7일간 호출 로그 없음
//   그런데 아래 폴백이 **gemma 가 죽으면 codex 로 넘겼다.** 죽은 곳으로 넘긴 것이다.
//   그래서 gemma 장애가 "분류 보류(합성 unknown)"로 조용히 삼켜졌다.
//
//   지금은 gemma 로만 간다. gemma 가 죽으면 **죽었다고 말한다** — 조용한 폴백보다
//   시끄러운 실패가 낫다. 이 저장소가 조용한 실패로 이미 여러 번 데였다.
func RoleProvider(role, def string) string {
	p := def
	if v := strings.TrimSpace(os.Getenv("KDB_LLM_" + role)); v != "" {
		p = v
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
// Run — 호환 유지용. 어느 공급자가 답했는지 알아야 하면 RunP 를 쓴다.
func (r *Runner) Run(ctx context.Context, prompt string, schema []byte) (json.RawMessage, error) {
	raw, _, err := r.RunP(ctx, prompt, schema)
	return raw, err
}

// RunP — Run 과 같되 **실제로 답한 공급자 이름을 함께 돌려준다.**
//
// ★왜 필요한가 (2026-09-16). 원장에 적는 이름표가 사실과 달라 608건이 "codex 검증"
//
//	이라 적혀 있었는데 판정한 것은 gemma 였다. 이름표가 거짓이면 다음 사람이 그것을
//	믿고 엉뚱한 곳을 판다 — 그날 내가 그 착시에 걸렸다. 폴백이 일어나는 순간부터
//	라우팅 설정과 실제가 갈리므로, **답한 쪽을 호출자에게 알려 주는 것**이 유일하게
//	정직한 방법이다.
func (r *Runner) RunP(ctx context.Context, prompt string, schema []byte) (json.RawMessage, string, error) {
	if r == nil {
		return nil, "", fmt.Errorf("codexcli: nil runner")
	}
	// ★codex 를 **판정 역할에 한해** 되살렸다 (운영자 지시 2026-09-16 저녁).
	//
	//   낮에 폐기했던 이유는 그대로 유효하다 — 실행 껍데기만 남아 있으면 "쓰는 것처럼"
	//   보여서 사람을 속인다(그날 내가 그 착시에 걸려 잘못 보고했다). 그래서 이번엔
	//   **라우팅이 가리킬 때만** 실행한다. 기본값은 여전히 gemma 다.
	//
	// ★되살린 이유는 자리가 다르기 때문이다 (실측 2026-09-16).
	//
	//   정정 검증(corrections/verify.go)은 근거를 **하나도** 주지 않고 "이 로케일의
	//   매체가 실제로 쓰는 표기가 무엇인가"를 묻는다 — 검색도 스니펫도 없는 순수
	//   지식 과제다. 뉴스근거 판정(cand-evidence)은 스니펫을 읽는 과제라 모델을 바꿔도
	//   소용이 없었지만(그쪽은 근거를 두껍게 해서 고쳤다), 이 자리는 모델 힘이 직접 는다.
	//
	// ★어느 쪽이 답했는지는 **감춰지지 않는다.** 폴백하면 그 사실이 로그와 원장에 남는다.
	//   조용한 폴백은 장애를 "판정 보류"로 삼켜 품질만 소리 없이 떨어뜨린다.
	provider := strings.TrimSpace(r.Provider)
	if provider == "" {
		provider = strings.TrimSpace(os.Getenv("KDB_LLM_PROVIDER"))
	}
	useCodex := strings.EqualFold(provider, "codex")
	if useCodex && !codexBudgetTake() {
		// 상한 소진 — 멈추지 않고 gemma 로 내려간다. **내려간 사실은 감추지 않는다.**
		used, limit := CodexBudgetSnapshot()
		log.Printf("codexcli: 일일 상한 소진(%d/%d) — gemma 로 내려간다", used, limit)
		useCodex = false
	}
	if !useCodex {
		if gemma.Configured() {
			raw, err := gemma.Complete(ctx, prompt, schema)
			return raw, "gemma", err
		}
		return nil, "", fmt.Errorf("codexcli: gemma 미구성 (provider=%q)", provider)
	}
	bin := r.Bin
	if bin == "" {
		bin = "codex"
	}
	model := r.Model
	if model == "" {
		model = strings.TrimSpace(os.Getenv("CODEX_MODEL"))
	}
	if model == "" {
		// ★접미사 없는 이름을 기본값으로 두면 안 된다. ChatGPT 계정 경로에서는
		//   gpt-5.6 / gpt-5 / gpt-5-codex 가 전부 400 으로 거부된다. 여기 "gpt-5.6"
		//   이 박혀 있었는데, 설정이 비는 순간 조용히 전부 실패하고 gemma 로 내려간다.
		model = "gpt-6-luna"
	}
	// ★원장에 적을 이름에 **모델까지 담는다** (2026-09-17 저녁).
	//
	//   "codex" 한 단어로는 sol 이 판정한 것과 luna 가 판정한 것을 구분할 수 없다.
	//   오늘 모델을 sol → luna 로 바꿨는데, 바꾸기 전후 판정이 원장에서 똑같이
	//   «codex 검증》으로 보이면 나중에 품질을 되짚을 수가 없다. 이 저장소는 이미
	//   이름표가 거짓이라 608건을 잘못 읽은 적이 있다.
	answeredBy := "codex(" + model + ")"
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 90 * time.Second
	}

	workDir, err := os.MkdirTemp("", "codex-bridge-")
	if err != nil {
		return nil, answeredBy, fmt.Errorf("codexcli: mkdtemp: %w", err)
	}
	defer os.RemoveAll(workDir)

	schemaPath := filepath.Join(workDir, "schema.json")
	if err := os.WriteFile(schemaPath, schema, 0o600); err != nil {
		return nil, answeredBy, fmt.Errorf("codexcli: write schema: %w", err)
	}
	lastMsgFile := filepath.Join(workDir, "last.txt")

	// 동시성 슬롯 획득. 평상시엔 N 동시(codexSem), 토큰 만료 임박 시엔 단일화
	// (codexRefreshGate + flock)해 동시 refresh 경쟁만 차단한다. 대기는 부모 ctx 를
	// 존중하고, per-run 타임아웃은 슬롯 확보 이후에 시작한다(대기 중 소진 방지).
	release, err := acquireCodexSlot(ctx)
	if err != nil {
		return nil, answeredBy, err
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
		return nil, answeredBy, fmt.Errorf("codex timeout after %dms", timeout.Milliseconds())
	}
	if runErr != nil {
		tail := lastStderrLines(stderr.String(), 5)
		if tail == "" {
			tail = "(no stderr)"
		}
		return nil, answeredBy, fmt.Errorf("codex exit %s: %s", exitCode(runErr), tail)
	}

	raw, err := os.ReadFile(lastMsgFile)
	if err != nil {
		return nil, answeredBy, fmt.Errorf("codex produced no last-message file")
	}
	txt := strings.TrimSpace(string(raw))
	if txt == "" {
		return nil, answeredBy, fmt.Errorf("codex last-message file empty")
	}
	if !json.Valid([]byte(txt)) {
		return nil, answeredBy, fmt.Errorf("codex last-message not valid JSON")
	}
	return json.RawMessage(txt), answeredBy, nil
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


// ── codex 일일 호출 상한 ────────────────────────────────────────────────────

// ★왜 상한인가 (운영자 지시 2026-09-16 저녁).
//
//	"검수내용이 많아 너무 많이 사용되면 gpt 감당못하고, 간단히 짧게 사용하는
//	내용이면 사용할수 있지."
//
//	그래서 codex 는 **짧고 적은 자리에만** 쓴다. 지금 그 자리는 정정 검증 하나다 —
//	실측 하루 평균 21건, 최대 49건, 프롬프트 300~400 토큰(근거를 안 주는 과제라 짧다).
//	반대로 뉴스근거 판정(하루 342건, 스니펫 5건)과 유입 분류는 gemma 로 둔다.
//
//	라우팅만으로는 부족하다. 새 레인이 실수로 CORRECTION 역할을 쓰거나 재시도가
//	폭주하면 조용히 늘어난다. 상한은 그것을 **숫자로** 막는다.
//
// ★상한을 넘어도 판정을 멈추지 않는다. gemma 로 내려가고, **내려갔다는 사실이
//
//	호출자에게 돌아간다**(RunP 의 두 번째 반환값). 조용한 폴백은 오늘 608건의
//	거짓 이름표를 만든 바로 그 길이다.
var (
	codexBudgetMu   sync.Mutex
	codexBudgetDay  string
	codexBudgetUsed int
)

// codexDailyCalls — 하루에 허용할 codex 호출 수.
//
// ★기본 60 → 300 (2026-09-17, 오너 지시 "cli 를 사용하니 우선 한도 올려서 진행해봐").
//
//	60 은 **API 과금을 전제로** 정한 수였다. 지금은 API 키가 아니라 ChatGPT 계정
//	(codex login)으로 돈다 — 토큰당 돈이 나가지 않는다. 제약이 «비용》에서
//	«계정 한도를 서버와 사람이 나눠 쓰는 것》으로 바뀌었다.
//
//	실측으로 잡은 수다(최근 14일, LLM 판정이 필요한 정정):
//	  일평균 13건 · 최대일 35건(09-16) · 정정 1건당 재시도 포함 약 2.5회
//	  → 최대일 소요 ≈ 88회. 300 은 그 위로 3배 이상 여유다.
//
//	폭주 위험은 실행 속도가 막는다: 동시성은 1~4(토큰 만료 임박 시 1)이고 오늘
//	실측 소진 속도는 **분당 2.5회**였다. 300 을 다 쓰려면 2시간이 걸린다 —
//	모르는 사이에 사라지는 크기가 아니다.
//
//	그리고 이제 남은 양이 보인다. 종전엔 CodexBudgetSnapshot 이 **소진된 순간에만**
//	불려서, 바닥난 뒤에야 알았다. cand-evidence tick 이 매번 «codex예산 n/300》 을
//	찍는다 — 300 이 맞는 수인지는 그 줄을 며칠 보고 정하면 된다.
func codexDailyCalls() int {
	if v := strings.TrimSpace(os.Getenv("KDB_CODEX_DAILY_CALLS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return 300
}

// codexBudgetTake — 한 호출을 예산에서 뺀다. 남지 않으면 false.
func codexBudgetTake() bool {
	today := time.Now().Format("2006-01-02")
	codexBudgetMu.Lock()
	defer codexBudgetMu.Unlock()
	if codexBudgetDay != today {
		codexBudgetDay, codexBudgetUsed = today, 0
	}
	if codexBudgetUsed >= codexDailyCalls() {
		return false
	}
	codexBudgetUsed++
	return true
}

// CodexBudgetSnapshot — 오늘 쓴 호출과 상한. 로그·화면용.
func CodexBudgetSnapshot() (used, limit int) {
	codexBudgetMu.Lock()
	defer codexBudgetMu.Unlock()
	return codexBudgetUsed, codexDailyCalls()
}

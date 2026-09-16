// Package codexcli — LLM 프롬프트·스키마와 gemma 호출 한 자리.
//
// ★이름은 codexcli 로 남았지만 **codex 는 더 이상 없다** (폐기 2026-09-16).
//   부르는 곳이 스물 몇 군데라 패키지 이름만 따로 바꾸지 않는다 — 이름을 고치는
//   변경과 codex 를 걷어내는 변경을 한 커밋에 섞으면 무엇이 무엇을 깨뜨렸는지
//   못 가린다.
//
// 남은 것: Build*Prompt · *Schema · Runner.Run(gemma 전용) · role 별 라우팅.
// 없어진 것: codex CLI 프로세스 실행 · ChatGPT OAuth 토큰 갱신 게이트 ·
//            인증 디렉터리 파일잠금 · 옛 경로를 되살리던 복원 스위치.
//            (그 이름들을 여기 그대로 적지 않는다 — 시험이 소스에서 그 문자열을
//             찾아 "폐기가 덜 됐다"고 말하는데, 주석이 거기 걸리면 시험이 못 쓴다.)
//
// internal/kdb 를 import 하면 안 된다(import cycle: internal/kdb 와
// internal/kdb/aijudge 가 둘 다 이 패키지를 쓴다).
package codexcli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/rickyjoo73/kdb/internal/kdb/gemma"
)

// Runner — LLM 호출자. **codex 시절의 Bin/Model/Timeout/ForceModel 은 없앴다**
// (폐기 2026-09-16) — 그 값들은 codex 프로세스에만 쓰였고, 남겨 두면 다음 사람이
// "모델을 바꿀 수 있나 보다" 하고 CODEX_MODEL 을 만지게 된다. gemma 의 모델은
// KDB_GEMMA_MODEL 이 정한다.
type Runner struct {
	// Effort — reasoning effort(minimal|low|medium|high|xhigh). role 별 차등용.
	// 환경변수 이름은 CODEX_EFFORT_<ROLE> 로 남아 있다 — 배포 설정을 같이 바꾸면
	// 이 커밋이 깨뜨린 것과 설정이 깨뜨린 것을 못 가리므로 이름은 따로 정리한다.
	Effort string
	// Provider — 이 runner 의 LLM 백엔드. 이제 "gemma" 하나뿐이고, 값이 무엇이든
	// RoleProvider 가 gemma 로 돌린다. 호출측 코드를 한꺼번에 고치지 않으려고 남긴다.
	Provider string
}

// NewRunner — role 별 effort 기본값만 읽는다. codex 관련 환경변수는 더 읽지 않는다.
func NewRunner() *Runner {
	return &Runner{Effort: os.Getenv("CODEX_REASONING_EFFORT")}
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
//     codex-bridge 컨테이너 없음 · 인증 파일 없음 · 7일간 호출 로그 없음
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
	if strings.EqualFold(p, "codex") {
		// 설정이 아직 codex 를 가리켜도 따르지 않는다. 남은 env 를 걷어내는 동안의 안전판.
		return "gemma"
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

// Run — **codex 는 여기서 끝났다.** gemma 로만 간다.
//
// ★운영자 지시 (2026-09-15): "codex 를 걷어내. 사용하지 않고, 실제로 고유명사를
//   prepare 에 제공하는 것이 codex gpt-5.6-sol 이라 신뢰해도 돼."
//   고유명사를 뽑아 보내는 일은 **소비자 쪽 GPT 가 이미 한다.** KDB 가 같은 일을
//   자기 안에서 또 하면 판단 주체가 둘이 되고, 어긋날 때 가릴 근거가 없다.
//
// ★그때는 라우팅만 gemma 로 돌리고 환경변수 하나로 옛 경로를 되살릴 수 있는 문을
//   남겨 뒀다.
//   그리고 실행 코드·CLI·인증 마운트가 전부 그대로 남아 **쓰는 것처럼 보였다.**
//   실제로 그 착시에 한 번 걸렸다 (2026-09-16): 컨테이너에 codex 0.146.0 이 있고
//   CODEX_* 환경변수가 11개 붙어 있어, 손으로 불러 보고 401 을 받고는 "앱이 codex 를
//   부르는데 전부 실패한다"고 보고했다. 앱은 codex 를 부르지 않는다 — 남은 껍데기가
//   그렇게 읽히게 만들었을 뿐이다.
//
// ★그래서 문을 닫고 실행 코드를 들어낸다 (운영자 지시 2026-09-16 "codex 사용은
//   폐기해, 사용이 안 되도록"). 남길 것은 프롬프트·스키마다 — 그것들은 gemma 가 쓴다.
//
// gemma 가 없으면 **없다고 말한다.** 조용히 폴백할 곳이 이제 없고, 있어서도 안 된다 —
// 죽은 곳으로 넘기면 장애가 "분류 보류"로 삼켜져 품질만 조용히 떨어진다.
func (r *Runner) Run(ctx context.Context, prompt string, schema []byte) (json.RawMessage, error) {
	if r == nil {
		return nil, fmt.Errorf("codexcli: nil runner")
	}
	if !gemma.Configured() {
		return nil, fmt.Errorf("codexcli: gemma 미구성 — KDB 의 LLM 경로는 gemma 하나뿐이다(codex 폐기 2026-09-16)")
	}
	return gemma.Complete(ctx, prompt, schema)
}

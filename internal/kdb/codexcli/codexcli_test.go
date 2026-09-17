package codexcli

import (
	"context"
	"os"
	"strings"
	"testing"
)

// ★codex 를 **판정 역할 하나에 한해** 되살렸다 (운영자 지시 2026-09-16 저녁:
//
//	"나는 codex 를 추천해 5.6 sol low 로 설정하면 좀더 좋은 판단을 할거야").
//
//	낮에 폐기했던 이유는 그대로 유효하다 — 실행 껍데기만 남아 있으면 "쓰는 것처럼"
//	보여서 사람을 속인다(그날 내가 그 착시에 걸려 잘못 보고했다). 그래서 이 시험들이
//	지키는 것이 «없는 것»에서 «라우팅이 가리킬 때만, 정해진 양만큼》으로 바뀌었다.
//
//	되살린 자리가 어디인지가 중요하다. 정정 검증은 근거를 하나도 주지 않고 "이
//	로케일의 매체가 실제로 쓰는 표기가 무엇인가"를 묻는 **순수 지식 과제**이고,
//	하루 평균 21건 · 최대 49건 · 프롬프트 300~400 토큰이다. 뉴스근거 판정(하루 342건,
//	스니펫 5건)과 유입 분류는 gemma 로 둔다 — 그쪽은 읽는 과제라 모델을 바꿔도
//	소용이 없었고(근거를 두껍게 해서 고쳤다), 양도 많다.

// ★기본값은 여전히 gemma 다. 아무 설정 없이 codex 로 가면 안 된다.
func TestDefaultProviderIsGemmaNotCodex(t *testing.T) {
	t.Setenv("KDB_LLM_VERIFY", "")
	if got := RoleProvider("VERIFY", "gemma"); got != "gemma" {
		t.Errorf("기본 판정 역할이 %q 다 — gemma 여야 한다", got)
	}
	// 역할 설정이 가리키면 그때만 codex 다.
	t.Setenv("KDB_LLM_CORRECTION", "codex")
	if got := RoleProvider("CORRECTION", "gemma"); got != "codex" {
		t.Errorf("CORRECTION=codex 로 걸었는데 %q — 라우팅이 안 먹는다", got)
	}
}

// ★codex 로 가지 않는 경로는 **gemma 가 없으면 없다고 말한다.** 조용히 폴백하면
// 장애가 "판정 보류"로 삼켜져 품질만 소리 없이 떨어진다.
func TestNonCodexPathFailsLoudlyWithoutGemma(t *testing.T) {
	t.Setenv("KDB_GEMMA_BASE_URL", "")
	t.Setenv("GEMMA_BASE_URL", "")
	r := &Runner{Provider: "gemma"}
	_, by, err := r.RunP(context.Background(), "prompt", []byte(`{}`))
	if err == nil {
		t.Fatal("gemma 가 없는데 오류를 안 냈다 — 어딘가로 조용히 넘어갔다")
	}
	if !strings.Contains(err.Error(), "gemma") {
		t.Errorf("오류가 이유를 말하지 않는다: %v", err)
	}
	if by == "codex" {
		t.Error("gemma 경로인데 codex 가 답했다고 적었다")
	}
}

// ★**어느 쪽이 답했는지 감추지 않는다.**
//
//	라우팅이 codex 를 가리켜도 상한 소진·인증 실패·장애로 gemma 가 답할 수 있다.
//	그때 라우팅 설정 이름을 원장에 적으면 거짓말이 된다 — 그렇게 «codex 검증》이
//	608건 쌓였고, 폐기한 당일에도 20건이 그렇게 들어갔다.
func TestRunPReportsWhoAnswered(t *testing.T) {
	b, err := os.ReadFile("codexcli.go")
	if err != nil {
		t.Fatalf("codexcli.go 를 못 읽었다: %v", err)
	}
	src := string(b)
	if !strings.Contains(src, "func (r *Runner) RunP(") {
		t.Fatal("RunP 가 없다 — 호출자가 누가 답했는지 알 길이 없다")
	}
	if !strings.Contains(src, `return raw, "gemma", err`) {
		t.Error("gemma 가 답했을 때 그 사실을 안 돌려준다")
	}
	if !strings.Contains(src, `return json.RawMessage(txt), "codex", nil`) {
		t.Error("codex 가 답했을 때 그 사실을 안 돌려준다")
	}
}

// ★일일 상한이 있어야 한다 (운영자 지시: "너무 많이 사용되면 gpt 감당못하고,
//
//	간단히 짧게 사용하는내용이면 사용할수 잇지").
//
//	라우팅만으로는 부족하다 — 새 레인이 실수로 CORRECTION 역할을 쓰거나 재시도가
//	폭주하면 조용히 늘어난다. 상한은 그것을 숫자로 막는다.
func TestCodexDailyBudget(t *testing.T) {
	t.Setenv("KDB_CODEX_DAILY_CALLS", "2")
	codexBudgetMu.Lock()
	codexBudgetDay, codexBudgetUsed = "", 0
	codexBudgetMu.Unlock()

	if !codexBudgetTake() || !codexBudgetTake() {
		t.Fatal("예산 안인데 거부됐다")
	}
	if codexBudgetTake() {
		t.Fatal("상한을 넘겨 호출을 허용했다")
	}
	used, limit := CodexBudgetSnapshot()
	if used != 2 || limit != 2 {
		t.Fatalf("집계가 어긋난다: used=%d limit=%d", used, limit)
	}
	// 날이 바뀌면 되살아난다.
	codexBudgetMu.Lock()
	codexBudgetDay = "1999-01-01"
	codexBudgetMu.Unlock()
	if !codexBudgetTake() {
		t.Error("어제 예산이 오늘을 막는다")
	}
}

// ★상한을 넘겨도 **판정을 멈추지 않는다.** 소진이 «판정 불가》가 되면 정정신고가
// 그냥 사라진다 — gemma 로 내려가고, 내려갔다는 사실을 돌려준다.
func TestBudgetExhaustionFallsBackNotFails(t *testing.T) {
	b, err := os.ReadFile("codexcli.go")
	if err != nil {
		t.Fatalf("codexcli.go 를 못 읽었다: %v", err)
	}
	src := string(b)
	i := strings.Index(src, "codexBudgetTake()")
	if i < 0 {
		t.Fatal("상한 가드가 라우팅에 없다")
	}
	seg := src[i:minN(i+400, len(src))]
	if strings.Contains(seg, "return nil, \"\", fmt.Errorf") {
		t.Error("상한 소진이 곧 실패다 — 정정신고가 사라진다")
	}
	if !strings.Contains(seg, "useCodex = false") {
		t.Error("소진 시 gemma 로 내려가지 않는다")
	}
}

// ★모델 기본값은 운영자가 지정한 것이어야 한다 (gpt-5.6 · effort low).
func TestDefaultModelIsOwnerSpecified(t *testing.T) {
	b, err := os.ReadFile("codexcli.go")
	if err != nil {
		t.Fatalf("codexcli.go 를 못 읽었다: %v", err)
	}
	if !strings.Contains(string(b), `model = "gpt-5.6"`) {
		t.Error("기본 모델이 gpt-5.6 이 아니다 — 운영자가 지정한 값이다")
	}
}

func minN(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestCodexDailyCallsHonorsEnvAndDefault — 상한이 env 로 조절되고 기본값이 300 인지.
//
// 60 은 API 과금 전제로 정한 수였다. ChatGPT 계정(codex login)으로 돌면 토큰당 비용이
// 없고 제약이 «계정 한도 나눠쓰기》로 바뀐다. 실측(최근 14일): LLM 판정이 필요한 정정이
// 일평균 13건·최대일 35건, 1건당 재시도 포함 약 2.5회 → 최대일 ≈ 88회. 300 은 그 위로
// 3배 이상 여유다.
func TestCodexDailyCallsHonorsEnvAndDefault(t *testing.T) {
	t.Setenv("KDB_CODEX_DAILY_CALLS", "")
	if got := codexDailyCalls(); got != 300 {
		t.Errorf("기본 상한 = %d, 기대 300", got)
	}
	t.Setenv("KDB_CODEX_DAILY_CALLS", "120")
	if got := codexDailyCalls(); got != 120 {
		t.Errorf("env 상한 = %d, 기대 120", got)
	}
	// 0 은 «codex 를 쓰지 않는다》는 유효한 설정이다 — 기본값으로 되돌아가면 안 된다.
	t.Setenv("KDB_CODEX_DAILY_CALLS", "0")
	if got := codexDailyCalls(); got != 0 {
		t.Errorf("상한 0 = %d, 기대 0 (codex 끄기)", got)
	}
	// 쓰레기 값은 기본값으로.
	t.Setenv("KDB_CODEX_DAILY_CALLS", "많이")
	if got := codexDailyCalls(); got != 300 {
		t.Errorf("잘못된 값일 때 = %d, 기대 300", got)
	}
}

// TestCodexBudgetIsObservableBeforeExhaustion — 남은 예산을 **소진 전에** 볼 수 있어야
// 한다.
//
// 종전엔 CodexBudgetSnapshot 을 소진 시점(codexcli.go 의 "일일 상한 소진" 로그)에서만
// 불렀다. 그래서 오늘 상한이 30분 만에 바닥난 것을 *바닥난 뒤에야* 알았고, 60 이 맞는
// 수인지 판단할 근거가 없었다. 이 저장소가 반복해 밟는 «장치는 있는데 아무도 안 켠»
// 이다 — 이번엔 cmd/kdb 가 매 tick 찍는다. 그 호출이 사라지지 않게 잠근다.
func TestCodexBudgetIsObservableBeforeExhaustion(t *testing.T) {
	src, err := os.ReadFile("../../../cmd/kdb/main.go")
	if err != nil {
		t.Fatalf("main.go 읽기 실패: %v", err)
	}
	if !strings.Contains(string(src), "codexcli.CodexBudgetSnapshot()") {
		t.Error("cmd/kdb 가 codex 예산을 읽지 않는다 — 소진 전에는 남은 양을 알 수 없다")
	}
	if !strings.Contains(string(src), "codex예산") {
		t.Error("codex 예산을 로그로 내보내지 않는다 — 읽어도 보이지 않으면 없는 것과 같다")
	}
}

// TestAnsweredByCarriesTheModel — 원장 이름표가 **어느 모델이 판정했는지**를 담는지.
//
// 2026-09-17 저녁에 정정 검증 모델을 gpt-5.6-sol → gpt-5.6-luna 로 바꿨다. 그런데
// RunP 가 돌려주는 이름이 "codex" 한 단어라, 바꾸기 전후 판정이 원장에서 똑같이
// «codex 검증》으로 보였다. 그러면 나중에 «luna 로 바꾼 뒤 품질이 어땠나》를 되짚을
// 수가 없다. 이 저장소는 이미 이름표가 거짓이라 608건을 잘못 읽은 적이 있다.
func TestAnsweredByCarriesTheModel(t *testing.T) {
	src, err := os.ReadFile("codexcli.go")
	if err != nil {
		t.Fatalf("codexcli.go 읽기 실패: %v", err)
	}
	body := string(src)
	if !strings.Contains(body, `answeredBy := "codex(" + model + ")"`) {
		t.Error("판정자 이름에 모델이 안 들어간다 — sol 과 luna 를 구분할 수 없다")
	}
	// 모델 확정 이후에 맨 "codex" 를 그대로 돌려주는 자리가 남아 있으면 안 된다.
	i := strings.Index(body, `answeredBy := "codex(" + model + ")"`)
	if i > 0 && strings.Contains(body[i:], `, "codex", `) {
		t.Error("모델 확정 뒤에도 \"codex\" 를 그대로 돌려주는 반환이 남아 있다")
	}
}

// TestDefaultModelIsAcceptedByChatGPTAccount — 설정이 비었을 때의 기본 모델이
// ChatGPT 계정 경로에서 통하는 이름인지.
//
// 실측(2026-09-17): 접미사 없는 gpt-5.6 / gpt-5 / gpt-5-codex 는 400 으로 거부된다
// ("not supported when using Codex with a ChatGPT account"). 기본값이 그런 이름이면
// 설정이 비는 순간 **전부 조용히 실패하고 gemma 로 내려간다** — 그리고 원장에는
// gemma 라 적힌다. 어제 codex 가 하루 종일 안 켜졌던 이유가 정확히 이것이다.
func TestDefaultModelIsAcceptedByChatGPTAccount(t *testing.T) {
	src, err := os.ReadFile("codexcli.go")
	if err != nil {
		t.Fatalf("codexcli.go 읽기 실패: %v", err)
	}
	body := string(src)
	for _, bad := range []string{`model = "gpt-5.6"`, `model = "gpt-5"`, `model = "gpt-5-codex"`} {
		if strings.Contains(body, bad) {
			t.Errorf("기본 모델이 ChatGPT 계정에서 거부되는 이름이다: %s", bad)
		}
	}
	if !strings.Contains(body, `os.Getenv("CODEX_MODEL")`) {
		t.Error("모델 기본값이 CODEX_MODEL 을 보지 않는다 — 운영 설정과 갈린다")
	}
}

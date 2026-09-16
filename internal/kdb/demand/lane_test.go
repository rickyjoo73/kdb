package demand

import (
	"os"
	"strings"
	"testing"
	"time"
)

// 요청 훅은 **요청 핫패스에서 불린다.** 캡이 차 있어도 절대 블록하면 안 된다.
// 블록하면 소비자 응답이 cascade 시간만큼 늦어진다 — 빠르게 하려고 만든 것이
// 정반대로 작동하는 자리다.
func TestTriggerNeverBlocks(t *testing.T) {
	l := &Lane{sem: make(chan struct{}, 1)}
	l.sem <- struct{}{} // 캡을 가득 채운다
	done := make(chan struct{})
	go func() {
		l.Trigger("6f1f2a3e-0000-4000-8000-000000000001")
		close(done)
	}()
	select {
	case <-done:
	default:
		// 채널 수신이 아직이면 아주 짧게 한 번 더 본다(스케줄 지연 흡수).
		<-done
	}
	if got := l.Snapshot(); got.Dropped != 1 {
		t.Fatalf("캡이 찼는데 버린 것으로 안 셌다: %+v", got)
	}
	if got := l.Snapshot(); got.Ran != 0 {
		t.Fatalf("캡이 찼는데 실행했다: %+v", got)
	}
}

// 잘못된 id 는 조용히 무시한다. 요청 경로에서 오는 값이라 오류로 소비자 응답을
// 흔들면 안 된다 — 그런데 **걸림 집계에는 남는다**(불렸다는 사실은 사실이다).
func TestTriggerIgnoresBadID(t *testing.T) {
	l := &Lane{sem: make(chan struct{}, 1)}
	l.Trigger("")
	l.Trigger("not-a-uuid")
	if got := l.Snapshot(); got.Ran != 0 || got.Dropped != 0 {
		t.Fatalf("잘못된 id 로 일을 시작했다: %+v", got)
	}
}

// 끄는 문이 실제로 닫히는가. nil 을 돌려줘야 호출부가 nil 검사 하나로 끝난다.
func TestKillSwitch(t *testing.T) {
	t.Setenv("KDB_DEMAND_LANE", "0")
	if l := New(nil); l != nil {
		t.Fatal("KDB_DEMAND_LANE=0 인데 레인이 생겼다")
	}
	// pool 이 nil 이면 끄지 않아도 nil — 잘못 배선된 채로 돌지 않게.
	os.Unsetenv("KDB_DEMAND_LANE")
	if l := New(nil); l != nil {
		t.Fatal("pool 이 없는데 레인이 생겼다")
	}
}

// nil 레인에 걸어도 죽지 않는다. 호출부가 `if lane != nil` 을 빠뜨려도 운영이
// 멈추지 않아야 한다 — bgEnrich 와 같은 계약.
func TestNilLaneIsSafe(t *testing.T) {
	var l *Lane
	l.Trigger("6f1f2a3e-0000-4000-8000-000000000001")
	l.LogStats()
	if got := l.Snapshot(); got.Triggered != 0 {
		t.Fatalf("nil 레인이 집계를 냈다: %+v", got)
	}
}

// ★이 레인은 **승급을 결정하지 않는다.**
//
//	처음엔 research worker 의 규칙("enrich 의 위키데이터 레이어가 돌았으면 승급")을
//	그대로 썼다. 운영 데이터가 그것을 물렸다 — `이재명`(오늘 20회 요청)에는
//	Q6514101 이 붙어 있는데 그건 **1991년생 축구선수**다. 앵커가 이미 있는 행은
//	runWikidata 가 QID 를 직접 Fetch 하고 동명이인 가드가 면제되므로 레이어는 돌고,
//	그 규칙대로면 축구선수의 표기가 이재명으로 나간다.
//
//	그래서 승급 판단은 전부 CandidateEvidenceOne(뉴스근거+gemma)에 맡긴다.
//	여기에 status 를 바꾸는 코드가 생기면 그 순간 두 번째 기준이 만들어진다.
func TestLaneNeverPromotesByItself(t *testing.T) {
	b, err := os.ReadFile("lane.go")
	if err != nil {
		t.Fatalf("lane.go 를 못 읽었다: %v", err)
	}
	src := string(b)
	for _, banned := range []string{"status = 'active'", "status='active'"} {
		if strings.Contains(src, banned) {
			t.Errorf("레인이 직접 승급한다(%q) — 판단은 cand-evidence 의 몫이다", banned)
		}
	}
	if !strings.Contains(src, "verify.CandidateEvidenceOne(") {
		t.Error("뉴스근거 판정기를 안 부른다 — 그러면 훅이 행을 밀기만 하고 아무도 판정하지 않는다")
	}
}

// ★앵커가 이미 있으면 **다시 긁지 않는다.**
//
//	앵커가 붙었는데도 candidate 라는 것은 그 앵커가 의심스럽다는 뜻이다. 같은
//	QID 를 다시 Fetch 하면 (QID-pin 경로라 동명이인 가드도 면제된 채) 잘못된
//	표기만 더 깊이 박힌다. 찾는 일은 못 찾은 행에만 한다.
func TestEnrichOnlyWhenUnanchored(t *testing.T) {
	b, err := os.ReadFile("lane.go")
	if err != nil {
		t.Fatalf("lane.go 를 못 읽었다: %v", err)
	}
	src := string(b)
	guard := strings.Index(src, "if !anchored {")
	call := strings.Index(src, "l.Orch.Enrich(")
	if guard < 0 {
		t.Fatal("앵커 유무 분기가 없다 — 앵커 있는 행까지 다시 긁는다")
	}
	if call < 0 {
		t.Fatal("enrich 호출을 못 찾았다")
	}
	if call < guard {
		t.Fatal("enrich 가 앵커 분기 밖에 있다 — 앵커 있는 행도 긁는다")
	}
	if strings.Count(src, "l.Orch.Enrich(") != 1 {
		t.Error("enrich 호출이 두 군데 이상이다 — 분기 밖 경로가 생겼는지 봐야 한다")
	}
}

// ★되풀이 방지가 **일을 시작하기 전에** 걸려 있는가.
//
//	이 레인은 요청마다 불린다. 오늘 트래픽은 낱말 1,523건 중 고유 1,255건이라
//	같은 낱말이 하루에 여러 번 들어온다. 쿨다운이 없으면 소비자 폴링 주기가
//	그대로 위키데이터·네이버·gemma 호출 주기가 된다.
//
//	처음 쓸 때 실제로 빠뜨렸다 — bgEnrich 의 1시간 claim 이 Trigger 안에 있고
//	이 레인은 Orchestrator.Enrich 를 직접 불러서, 쿨다운이 하나도 없었다.
func TestCooldownClaimPrecedesWork(t *testing.T) {
	b, err := os.ReadFile("lane.go")
	if err != nil {
		t.Fatalf("lane.go 를 못 읽었다: %v", err)
	}
	src := string(b)
	claim := strings.Index(src, "SET last_enriched_at = now()")
	if claim < 0 {
		t.Fatal("claim 이 없다 — 같은 행을 요청마다 다시 민다")
	}
	work := strings.Index(src, "l.Orch.Enrich(")
	if work < 0 {
		t.Fatal("cascade 호출을 못 찾았다")
	}
	if claim > work {
		t.Fatal("claim 이 cascade 뒤에 있다 — 선점이 아니라 사후 기록이다")
	}
	// 조건부 UPDATE 하나로 검사와 선점을 같이 해야 한다. 읽고 나서 쓰면 그 사이에
	// 다른 요청이 끼어든다.
	for _, need := range []string{
		"last_enriched_at < now() - $2::interval",
		"status = 'candidate'",
		"operator_locked = false",
	} {
		if !strings.Contains(src, need) {
			t.Errorf("claim 조건에 %q 가 없다", need)
		}
	}
}

// ★쿨다운 칸은 bgEnrich 와 **같아야 한다.** 칸이 다르면 두 경로가 같은 행을
// 각자 붙잡고 같은 외부 호출을 두 번 한다 — 아끼려고 만든 것이 두 배로 쓴다.
func TestCooldownSharesColumnWithBackgroundEnrich(t *testing.T) {
	bg, err := os.ReadFile("../enrich/background.go")
	if err != nil {
		t.Fatalf("enrich/background.go 를 못 읽었다: %v", err)
	}
	if !strings.Contains(string(bg), "SET last_enriched_at = now()") {
		t.Fatal("bgEnrich 가 last_enriched_at 으로 claim 하지 않는다 — 이 레인의 칸도 같이 봐야 한다")
	}
	if staleAfter != time.Hour {
		t.Fatalf("staleAfter=%v — bgEnrich 의 StaleAfter(1h) 와 달라졌다", staleAfter)
	}
}

// ★이 레인은 **문턱을 낮추지 않는다.** 빠르게 하는 것과 무르게 하는 것은 다르다.
//
//	자체 판정 로직이 들어오는 순간 기준이 두 개가 되고, 둘이 갈리면 같은 낱말이
//	경로에 따라 다른 답을 받는다. 판정은 전부 이미 있는 함수에 맡겨야 한다.
func TestLaneHasNoJudgementOfItsOwn(t *testing.T) {
	b, err := os.ReadFile("lane.go")
	if err != nil {
		t.Fatalf("lane.go 를 못 읽었다: %v", err)
	}
	src := string(b)
	for _, banned := range []struct{ token, why string }{
		{"IsKWaveDescription", "한국 여부를 여기서 다시 판단하면 안 된다"},
		{"AnchorTypeAllowed", "유형 판정은 앵커 레인·감사의 몫이다"},
		{"SearchAndFetch", "이름검색을 직접 부르면 동명이인 가드를 우회한다"},
		{"gatekeeper.", "유입 판정을 다시 내리면 안 된다"},
		{"LayersRun", "어떤 레이어가 돌았는가로 승급을 정하면 이재명이 축구선수가 된다"},
	} {
		if strings.Contains(src, banned.token) {
			t.Errorf("%s 가 들어왔다(%s) — 요청 훅은 언제 볼지만 바꾼다", banned.token, banned.why)
		}
	}
}

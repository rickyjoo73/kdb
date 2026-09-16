package demand

import (
	"os"
	"strings"
	"testing"
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

// ★승급 기준은 research worker 와 **같은 값**이어야 한다. 다르면 같은 근거로
// 승급한 행의 신뢰도가 경로에 따라 갈리고, 그 차이는 아무도 설명할 수 없다.
func TestPromoteConfMatchesResearchWorker(t *testing.T) {
	b, err := os.ReadFile("../research/worker.go")
	if err != nil {
		t.Fatalf("research/worker.go 를 못 읽었다: %v", err)
	}
	if !strings.Contains(string(b), "promoteConf         = 0.72") {
		t.Fatal("research worker 의 promoteConf 가 0.72 가 아니다 — 이 레인의 값도 같이 고쳐야 한다")
	}
	if promoteConf != 0.72 {
		t.Fatalf("요청 훅 promoteConf=%v — research worker 와 달라졌다", promoteConf)
	}
}

// ★이 레인은 **문턱을 낮추지 않는다.** 빠르게 하는 것과 무르게 하는 것은 다르다.
// 자체 판정 로직을 들이면 그 순간 두 개의 기준이 생긴다 — 승급은 전부 이미 있는
// 함수(Enrich 의 위키데이터 검증 · CandidateEvidenceOne)에 맡겨야 한다.
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
	} {
		if strings.Contains(src, banned.token) {
			t.Errorf("%s 가 들어왔다(%s) — 요청 훅은 언제 볼지만 바꾼다", banned.token, banned.why)
		}
	}
	// 승급 UPDATE 는 candidate 에서만, 운영자 잠금은 건드리지 않는다.
	if !strings.Contains(src, "status = 'candidate' AND operator_locked = false") {
		t.Error("승급 UPDATE 가 candidate·운영자잠금 조건을 안 걸었다")
	}
}

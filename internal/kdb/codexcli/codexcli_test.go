package codexcli

import (
	"context"
	"errors"
	"testing"
	"time"
)

func forceRefreshGateForTest(t *testing.T) {
	t.Helper()
	t.Setenv("CODEX_HOME", "")
	expMu.Lock()
	oldExp, oldAt := cachedExp, cachedExpAt
	cachedExp, cachedExpAt = time.Time{}, time.Time{}
	expMu.Unlock()
	t.Cleanup(func() {
		expMu.Lock()
		cachedExp, cachedExpAt = oldExp, oldAt
		expMu.Unlock()
	})
}

// 게이트가 점유돼 있으면 Run 은 codex 를 exec 하지 않고 대기하며, 부모 ctx 취소를
// 존중해 즉시 반환해야 한다. 테스트 환경엔 CODEX_HOME 이 없어 exp 판독 불가 →
// 보수적 단일화 경로(codexRefreshGate)를 탄다.
func TestRun_SerializationGateRespectsContext(t *testing.T) {
	forceRefreshGateForTest(t)
	codexRefreshGate <- struct{}{} // 다른 codex 가 refresh 보호 슬롯을 점유한 상황 모사.
	defer func() { <-codexRefreshGate }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 이미 취소된 ctx.

	// 일부러 존재하지 않는 bin: 만약 게이트를 무시하고 exec 까지 갔다면 exec 에러가
	// 났을 것이다. ctx.Canceled 가 나오면 게이트에서 막혀 exec 하지 않았다는 뜻.
	// codex 는 걷어냈지만(2026-09-15) 직렬화 게이트 코드는 살아 있다. 복원 스위치로
	// 그 경로를 켜고 게이트만 시험한다 — 덮지 않으면 다음에 누가 건드릴 때 조용히 깨진다.
	t.Setenv("KDB_CODEX_ALLOW", "1")
	r := &Runner{Bin: "kdb-nonexistent-binary-xyz", Timeout: time.Second, Provider: "codex"}
	_, err := r.Run(ctx, "prompt", []byte(`{}`))
	if err == nil {
		t.Fatal("expected error when gate is held and ctx is cancelled")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled (blocked at gate, no exec), got %v", err)
	}
}

// 게이트가 비어 있으면 정상적으로 진입해 (가짜 bin 이라) exec 단계 에러가 나야
// 한다 — 즉 ctx 에러가 아니라 codex 실행 에러. 게이트가 throughput 을 영구
// 막지 않음을 확인.
func TestRun_GateReleasedAfterRun(t *testing.T) {
	forceRefreshGateForTest(t)
	r := &Runner{Bin: "kdb-nonexistent-binary-xyz", Timeout: time.Second, Provider: "codex"}
	_, err := r.Run(context.Background(), "prompt", []byte(`{}`))
	if err == nil {
		t.Fatal("expected exec error for nonexistent bin")
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("should have reached exec (ctx not involved), got %v", err)
	}
	// 게이트가 반납됐는지: 다시 보낼 수 있어야 한다 (defer 로 풀렸으면 즉시 성공).
	select {
	case codexRefreshGate <- struct{}{}:
		<-codexRefreshGate
	default:
		t.Fatal("gate not released after Run returned")
	}
}

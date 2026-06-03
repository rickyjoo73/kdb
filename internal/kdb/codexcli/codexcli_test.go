package codexcli

import (
	"context"
	"errors"
	"testing"
	"time"
)

// 게이트가 점유돼 있으면 Run 은 codex 를 exec 하지 않고 대기하며, 부모 ctx 취소를
// 존중해 즉시 반환해야 한다 (동시 토큰 refresh 방지의 핵심 동작).
func TestRun_SerializationGateRespectsContext(t *testing.T) {
	codexGate <- struct{}{}            // 다른 codex 가 도는 상황을 모사 (게이트 점유).
	defer func() { <-codexGate }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 이미 취소된 ctx.

	// 일부러 존재하지 않는 bin: 만약 게이트를 무시하고 exec 까지 갔다면 exec 에러가
	// 났을 것이다. ctx.Canceled 가 나오면 게이트에서 막혀 exec 하지 않았다는 뜻.
	r := &Runner{Bin: "kdb-nonexistent-binary-xyz", Timeout: time.Second}
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
	r := &Runner{Bin: "kdb-nonexistent-binary-xyz", Timeout: time.Second}
	_, err := r.Run(context.Background(), "prompt", []byte(`{}`))
	if err == nil {
		t.Fatal("expected exec error for nonexistent bin")
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("should have reached exec (ctx not involved), got %v", err)
	}
	// 게이트가 반납됐는지: 다시 보낼 수 있어야 한다 (defer 로 풀렸으면 즉시 성공).
	select {
	case codexGate <- struct{}{}:
		<-codexGate
	default:
		t.Fatal("gate not released after Run returned")
	}
}

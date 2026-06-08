package httpx

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestLimiter_paces — n 회 호출은 최소 (n-1)*min 걸린다(첫 호출 즉시, 이후 min 간격).
func TestLimiter_paces(t *testing.T) {
	const min = 20 * time.Millisecond
	const n = 5
	l := NewLimiter(min)
	start := time.Now()
	for i := 0; i < n; i++ {
		if err := l.Wait(context.Background()); err != nil {
			t.Fatalf("Wait: %v", err)
		}
	}
	if got := time.Since(start); got < (n-1)*min {
		t.Fatalf("paced too fast: %v < %v", got, (n-1)*min)
	}
}

// TestLimiter_concurrent_race — 다수 goroutine 동시 Wait 시 data race 없음
// (`go test -race`). 종전 lastCall 공유필드의 race 를 대체한 핵심 보장.
func TestLimiter_concurrent_race(t *testing.T) {
	l := NewLimiter(time.Millisecond)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = l.Wait(context.Background())
		}()
	}
	wg.Wait()
}

// TestLimiter_ctxCancel — 대기 중 ctx 취소 시 즉시 에러 반환.
func TestLimiter_ctxCancel(t *testing.T) {
	l := NewLimiter(time.Hour) // 두 번째 호출은 사실상 영구 대기.
	if err := l.Wait(context.Background()); err != nil {
		t.Fatalf("first Wait should pass immediately: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := l.Wait(ctx); err == nil {
		t.Fatal("expected ctx cancellation error")
	}
}

// TestLimiter_nilAndZero — nil 또는 min<=0 이면 no-op.
func TestLimiter_nilAndZero(t *testing.T) {
	var nilL *Limiter
	if err := nilL.Wait(context.Background()); err != nil {
		t.Fatalf("nil limiter should be no-op: %v", err)
	}
	if err := NewLimiter(0).Wait(context.Background()); err != nil {
		t.Fatalf("zero limiter should be no-op: %v", err)
	}
}

package httpx

import (
	"context"
	"sync"
	"time"
)

// Limiter — 공유 클라이언트로 외부 API 를 병렬 호출할 때 호출 사이 최소 간격을
// 보장하는 reservation 기반 레이트리미터. 동시성 안전.
//
// 배경: musicbrainz/wikidata Client 단일 인스턴스를 enrich 병렬 goroutine 들이
// 공유한다. 종전엔 `lastCall time.Time` 을 락 없이 읽고/써 (a) data race 이고
// (b) 여러 goroutine 이 동시에 "간격 지났다"고 판단해 1 req/s 가드가 무력화됐다.
//
// reservation 방식 — lock 은 다음 슬롯 시각 계산(짧음)에만 쥐고, 실제 대기는 lock
// 밖에서 한다. 따라서 sleep 동안 다른 goroutine 을 막지 않으면서도 호출 시각이
// min 간격으로 직렬 페이싱된다. ctx 취소는 즉시 존중.
type Limiter struct {
	mu   sync.Mutex
	min  time.Duration
	next time.Time // 다음 호출이 진행 가능한 가장 이른 시각.
}

// NewLimiter — min 간격 레이트리미터. min<=0 이면 Wait 가 즉시 반환(무제한).
func NewLimiter(min time.Duration) *Limiter {
	return &Limiter{min: min}
}

// Wait — 이 호출의 슬롯을 예약하고 그 시각까지 대기한다. nil Limiter 또는 min<=0
// 이면 즉시 반환. ctx 취소 시 대기를 중단하고 ctx.Err() 반환(이미 예약된 슬롯은
// 소비된 것으로 두 — 롤백은 다른 goroutine 의 예약과 race 라, 취소당 최대 min 의
// 빈 슬롯 낭비는 감수).
func (l *Limiter) Wait(ctx context.Context) error {
	if l == nil || l.min <= 0 {
		return nil
	}
	l.mu.Lock()
	now := time.Now()
	start := now
	if l.next.After(now) {
		start = l.next
	}
	l.next = start.Add(l.min)
	l.mu.Unlock()

	wait := start.Sub(now)
	if wait <= 0 {
		return nil
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

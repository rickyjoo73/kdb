package autopilot

import (
	"context"
	"sync"
)

// fanOut — items 를 workers 개 고루틴으로 fn 처리. stepEnrichEmpty 의 sem 패턴을
// 일반화한 것. workers<=1 이면 기존 직렬 동작 그대로(KDB_STEP_FANOUT 미설정/1 =
// 현행 유지, 롤백 축). LLM 상한은 gemma 전역 sem(24)/codex 전역 sem(3)이 강제하므로
// 호출측은 자유 fan-out — 초과분은 클라이언트에서 큐잉된다.
//
// fn 안에서 Report 카운터를 만지면 호출측이 mutex 로 보호할 것.
func fanOut[T any](ctx context.Context, workers int, items []T, fn func(T)) {
	if workers <= 1 || len(items) <= 1 {
		for _, it := range items {
			if ctx.Err() != nil {
				return
			}
			fn(it)
		}
		return
	}
	if workers > len(items) {
		workers = len(items)
	}
	jobs := make(chan T, len(items))
	for _, it := range items {
		jobs <- it
	}
	close(jobs)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for it := range jobs {
				if ctx.Err() != nil {
					return
				}
				fn(it)
			}
		}()
	}
	wg.Wait()
}

// stepFanout — 직렬 gemma 루프들의 병렬 폭 (기본 1 = 현행 직렬).
// 실측(2026-07-02): 사이클 75%가 직렬 gemma/검색 루프 — 8 권장.
func stepFanout() int { return envInt("KDB_STEP_FANOUT", 1) }

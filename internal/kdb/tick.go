// Package kdb — supervisor entry point.
//
// Phase 4 (2026-05-25) — raw buffer wire 연결:
//   - PollerTick (15분 quota) : RSS fetch + kwave_rss_items_raw INSERT 만.
//   - SweeperTick (fast tick) : pending raw items → Codex 추출 → observations.
//   - BridgeHealthCheck (fast tick) : local `codex --version` probe.
package kdb

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// 30분 quota (운영자 정공법).
var (
	tickMu       sync.Mutex
	tickInterval = 30 * time.Minute
)

// PollerTick — RSS poll (15분 supervisor slow tick 에서 호출, 30분 quota enforce).
// Codex 호출 안 함 — 별도 SweeperTick 이 처리.
//
// 발굴 무게중심 이동 (2026-06-01): RSS passive 수집은 비효율(고유 기여 ~16%)이라
// on-demand 검색 발굴(research worker)로 대체 중. KDB_DISABLE_RSS_POLLING=1 이면
// poll 전면 중단(기존 raw 는 sweeper 가 계속 처리).
//
// ★기본이 **안 가져오는 것**으로 뒤집혔다 (2026-09-15 운영자 지시).
//
//	"더이상 일반 한국어 기사를 가져오는 경우 등 사용하지 말자. gemma 가 고유명사를
//	 제대로 파악하지 못해서 kdb 가 오염된다."
//
//	일반 기사를 긁어 추출기가 고유명사를 골라내는 방식은 골라내는 쪽이 틀리면
//	원장이 오염된다. 고유명사 분리는 소비자 쪽(GPT)이 하고, KDB 는 **지목된 것만**
//	받는다. 켜려면 KDB_ENABLE_RSS_POLLING=1 을 **명시**해야 한다 —
//	env 하나가 빠졌다고 수집이 되살아나면 안 된다.
func PollerTick(ctx context.Context, pool *pgxpool.Pool) {
	if os.Getenv("KDB_DISABLE_RSS_POLLING") == "1" {
		return
	}
	if os.Getenv("KDB_ENABLE_RSS_POLLING") != "1" {
		return
	}
	tickMu.Lock()
	defer tickMu.Unlock()

	var lastStarted *time.Time
	err := pool.QueryRow(ctx,
		`SELECT MAX(started_at) FROM kwave_kdb_poll_cycles`).Scan(&lastStarted)
	if err == nil && lastStarted != nil && time.Since(*lastStarted) < tickInterval {
		return
	}

	// PollOnce 가 raw INSERT 만 — 콜백 X.
	NewPoller(pool).PollOnce(ctx)
}

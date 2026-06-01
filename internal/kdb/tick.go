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
// poll 전면 중단(기존 raw 는 sweeper 가 계속 처리). 기본은 종전대로 가동.
func PollerTick(ctx context.Context, pool *pgxpool.Pool) {
	if os.Getenv("KDB_DISABLE_RSS_POLLING") == "1" {
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

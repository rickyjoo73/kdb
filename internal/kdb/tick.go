// Package kdb — supervisor entry point.
//
// Phase 4 (2026-05-25) — raw buffer wire 연결:
//   - PollerTick (15분 quota) : RSS fetch + kwave_rss_items_raw INSERT 만.
//   - SweeperTick (fast tick) : pending raw items → Codex 추출 → observations.
//   - BridgeHealthCheck (fast tick) : codex-bridge:9002/health probe.
package kdb

import (
	"context"
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
func PollerTick(ctx context.Context, pool *pgxpool.Pool) {
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

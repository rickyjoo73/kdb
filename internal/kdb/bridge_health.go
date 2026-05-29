// Package kdb — Codex health monitor + circuit breaker.
//
// Agent (SRE) 권고 2026-05-25:
//
//	*"운영자가 cycle 12 의 1265 fail 을 cycle log 로 사후 알았음.
//	 self-supervision 의 usefulness 결함 (§8.4)"*
//
// 구현 (post-consolidation):
//  1. BridgeHealthCheck — supervisor fast tick (30s) 에서 `codex --version` 로컬 체크.
//     (codex-bridge HTTP /health 는 폐기 — 이제 codex CLI 직접 exec.)
//  2. 3회 연속 fail → kwave_kdb_codex_runs incident audit row (domain=KDB).
//  3. Extractor circuit breaker — 호출 10회 연속 fail → 5분 호출 차단.
package kdb

import (
	"context"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ─── Bridge health monitor (supervisor fast tick) ─────────────────

const (
	bridgeFailThreshold = 3 // 3회 연속 fail → incident
	bridgeProbeTimeout  = 3 * time.Second
)

var (
	bridgeMu           sync.Mutex
	bridgeFailCount    int
	bridgeIncidentOpen bool
)

// BridgeHealthCheck — supervisor fast tick (30s) 에서 호출.
// 3회 연속 fail → kwave_kdb_codex_runs 에 incident audit row.
// 복구 (1회 ok) → incident close audit.
//
// 시그니처는 그대로 (cmd caller 호환). 내부적으로 HTTP probe 대신
// 로컬 `codex --version` 호출.
func BridgeHealthCheck(ctx context.Context, pool *pgxpool.Pool) {
	probe := checkCodexCLI(ctx)

	bridgeMu.Lock()
	defer bridgeMu.Unlock()

	if probe.OK {
		if bridgeIncidentOpen {
			log.Printf("kdb.BridgeHealth: RECOVERED (was offline %d ticks)", bridgeFailCount)
			recordBridgeAudit(ctx, pool, "recovered", "")
			bridgeIncidentOpen = false
		}
		bridgeFailCount = 0
		return
	}

	bridgeFailCount++
	if bridgeFailCount >= bridgeFailThreshold && !bridgeIncidentOpen {
		log.Printf("kdb.BridgeHealth: OFFLINE %d consecutive fails — incident opened (err=%s)",
			bridgeFailCount, probe.Error)
		recordBridgeAudit(ctx, pool, "offline", probe.Error)
		bridgeIncidentOpen = true
	}
}

func recordBridgeAudit(ctx context.Context, pool *pgxpool.Pool, status, errText string) {
	_, err := pool.Exec(ctx, `
INSERT INTO kwave_kdb_codex_runs
  (source_domain, locale, rss_title, hint_count, status, spelling_count, duration_ms, error_text, ran_at)
VALUES ('__bridge_health__', '__system__', $1, 0, $2, 0, 0, $3, now())`,
		"BRIDGE HEALTH", "bridge-"+status, nullIfEmpty(errText))
	if err != nil {
		log.Printf("kdb.recordBridgeAudit: %v", err)
	}
}

// ─── codex CLI health probe ───────────────────────────────────────

// codexProbe — `codex --version` 결과.
type codexProbe struct {
	OK    bool
	Error string
}

// checkCodexCLI — `codex --version` 을 짧은 timeout 으로 실행.
// exit 0 → ok; 그 외/exec 실패 → !ok.
func checkCodexCLI(ctx context.Context) codexProbe {
	bin := os.Getenv("CODEX_BIN")
	if bin == "" {
		bin = "codex"
	}
	probeCtx, cancel := context.WithTimeout(ctx, bridgeProbeTimeout)
	defer cancel()

	out, err := exec.CommandContext(probeCtx, bin, "--version").CombinedOutput()
	if probeCtx.Err() == context.DeadlineExceeded {
		return codexProbe{OK: false, Error: "codex --version timeout"}
	}
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return codexProbe{OK: false, Error: msg}
	}
	return codexProbe{OK: true}
}

// ─── Circuit breaker (extractor 호출 전 체크) ─────────────────────

const (
	breakerFailThreshold = 10
	breakerOpenDuration  = 5 * time.Minute
)

var (
	breakerMu        sync.Mutex
	breakerFails     int
	breakerOpenUntil time.Time
)

// BreakerIsOpen — 호출 차단 여부 (true = 차단).
func BreakerIsOpen() bool {
	breakerMu.Lock()
	defer breakerMu.Unlock()
	if !breakerOpenUntil.IsZero() && time.Now().Before(breakerOpenUntil) {
		return true
	}
	if !breakerOpenUntil.IsZero() {
		// 자동 close 시점 도래 — half-open 으로 1 호출 허용.
		breakerOpenUntil = time.Time{}
		breakerFails = 0
		log.Printf("kdb.Breaker: half-open (try 1 call)")
	}
	return false
}

// BreakerRecordResult — 호출 결과 기록. ok=true 면 fail count reset.
// fail count threshold 도달 시 5분 차단.
func BreakerRecordResult(ok bool) {
	breakerMu.Lock()
	defer breakerMu.Unlock()
	if ok {
		breakerFails = 0
		return
	}
	breakerFails++
	if breakerFails >= breakerFailThreshold {
		breakerOpenUntil = time.Now().Add(breakerOpenDuration)
		log.Printf("kdb.Breaker: OPEN for %v (%d consecutive fails)",
			breakerOpenDuration, breakerFails)
	}
}

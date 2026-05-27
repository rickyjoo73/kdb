// Package kdb — Codex bridge health monitor + circuit breaker.
//
// Agent (SRE) 권고 2026-05-25:
//
//	*"운영자가 cycle 12 의 1265 fail 을 cycle log 로 사후 알았음.
//	 self-supervision 의 usefulness 결함 (§8.4)"*
//
// 구현:
//  1. BridgeHealthMonitor — supervisor fast tick (30s) 에서 GET /health.
//  2. 3회 연속 fail → hermes incident OPEN (domain=KDB stepKey=bridge-down).
//  3. Extractor circuit breaker — 호출 10회 연속 fail → 5분 호출 차단.
package kdb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
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
func BridgeHealthCheck(ctx context.Context, pool *pgxpool.Pool) {
	bridgeURL := bridgeURLOrDefault()
	probe := NewBridgeHealthClient(bridgeURL).Check(ctx)

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

func bridgeURLOrDefault() string {
	// extractor.go 의 default 와 일관.
	return "http://codex-bridge:9002/kdb_extract"
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

// BridgeHealthClient — codex bridge :9002/health 호출.
type BridgeHealthClient struct {
	URL        string
	HTTPClient *http.Client
}

// NewBridgeHealthClient — env KDB_CODEX_BRIDGE_URL 의 base + /health.
func NewBridgeHealthClient(bridgeURL string) *BridgeHealthClient {
	if bridgeURL == "" {
		bridgeURL = "http://codex-bridge:9002/kdb_extract"
	}
	// base url 추출 — /extract 끝부분 제거 후 /health 부착.
	u, _ := url.Parse(bridgeURL)
	if u != nil {
		u.Path = "/health"
		bridgeURL = u.String()
	}
	return &BridgeHealthClient{
		URL:        bridgeURL,
		HTTPClient: &http.Client{Timeout: 3 * time.Second},
	}
}

// BridgeHealth — operator bridge (scripts/codex_agent_bridge.py) 의 /health 응답.
//
//	{ "ok": true, "provider": "codex", "codex_bin": "...",
//	  "schema_dir": "...", "model_default": "gpt-5.5", "search_enabled": true }
type BridgeHealth struct {
	OK            bool   `json:"ok"`
	Provider      string `json:"provider"`
	Codex         string `json:"codex_bin"`
	SchemaDir     string `json:"schema_dir"`
	DefaultModel  string `json:"model_default"`
	SearchEnabled bool   `json:"search_enabled"`
	Error         string `json:"-"`
}

// Check — bridge 호출 + 응답.
func (c *BridgeHealthClient) Check(ctx context.Context) BridgeHealth {
	req, err := http.NewRequestWithContext(ctx, "GET", c.URL, nil)
	if err != nil {
		return BridgeHealth{OK: false, Error: err.Error()}
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return BridgeHealth{OK: false, Error: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return BridgeHealth{OK: false, Error: fmt.Sprintf("status %d", resp.StatusCode)}
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var h BridgeHealth
	if err := json.Unmarshal(body, &h); err != nil {
		return BridgeHealth{OK: false, Error: "decode: " + err.Error()}
	}
	h.OK = true
	return h
}

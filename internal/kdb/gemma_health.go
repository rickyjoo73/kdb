// Package kdb — Gemma 게이트웨이 health monitor + 자율 폴백 breaker (2026-06-20).
//
// 완전자율 모델: Gemma 가 주력 워크호스(CLASSIFY/FILL/FILLPERSON 대량 처리)이고,
// gpt-5.5(Codex)가 이를 감독한다. 그러나 그동안 Gemma 게이트웨이용 헬스 모니터가
// 없어, Gemma 가 죽으면 분류/채움이 조용히 합성 unknown 으로 떨어지기만 했다(감지·
// 대응 없음). 이 모니터는 bridge_health.go(Codex probe)와 대칭으로:
//   1. GemmaHealthCheck — supervisor fast tick(30s)에서 KDB_GEMMA_BASE_URL reachability probe.
//   2. 3회 연속 fail → incident audit(kwave_kdb_codex_runs) + breaker open.
//   3. breaker open 동안 codexcli.GemmaDown 훅이 true → RoleProvider 가 gemma 라우팅
//      role(CLASSIFY/FILL/FILLPERSON)을 Codex 로 자동 폴백(자가복구). 1회 ok 면 자동 환원.
package kdb

import (
	"context"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	gemmaFailThreshold = 3
	gemmaProbeTimeout  = 4 * time.Second
)

var (
	gemmaMu           sync.Mutex
	gemmaFailCount    int
	gemmaIncidentOpen bool
)

// GemmaHealthy — Gemma 게이트웨이가 정상(또는 미설정)이면 true. incident 가 열려있으면
// false. codexcli.GemmaDown 훅이 !GemmaHealthy() 로 와이어된다(main.go).
func GemmaHealthy() bool {
	gemmaMu.Lock()
	defer gemmaMu.Unlock()
	return !gemmaIncidentOpen
}

// GemmaHealthCheck — supervisor fast tick(30s)에서 호출. KDB_GEMMA_BASE_URL 미설정이면
// no-op(Gemma 미사용 — Codex 단독). 3회 연속 fail → incident open + audit, 1회 ok → 복구.
func GemmaHealthCheck(ctx context.Context, pool *pgxpool.Pool) {
	base := strings.TrimSpace(os.Getenv("KDB_GEMMA_BASE_URL"))
	if base == "" {
		return // Gemma 미설정 — 모니터 대상 아님(폴백 불필요, Run 이 이미 codex 사용)
	}
	ok, errText := probeGemma(ctx, base)

	gemmaMu.Lock()
	defer gemmaMu.Unlock()

	if ok {
		if gemmaIncidentOpen {
			log.Printf("kdb.GemmaHealth: RECOVERED (was offline %d ticks) — gemma 라우팅 환원", gemmaFailCount)
			recordGemmaAudit(ctx, pool, "recovered", "")
			gemmaIncidentOpen = false
		}
		gemmaFailCount = 0
		return
	}

	gemmaFailCount++
	if gemmaFailCount >= gemmaFailThreshold && !gemmaIncidentOpen {
		log.Printf("kdb.GemmaHealth: OFFLINE %d consecutive fails — incident 열림, CLASSIFY/FILL 을 Codex 로 자동 폴백 (err=%s)",
			gemmaFailCount, errText)
		recordGemmaAudit(ctx, pool, "offline", errText)
		gemmaIncidentOpen = true
	}
}

// probeGemma — base URL reachability probe. 네트워크 오류/타임아웃 → down. 어떤 HTTP
// 응답이든(2xx/4xx 포함) 게이트웨이 프로세스가 살아있다는 신호 → up (codex --version
// 이 바이너리 존재만 보는 것과 동일 등급의 reachability 체크).
func probeGemma(ctx context.Context, base string) (bool, string) {
	pctx, cancel := context.WithTimeout(ctx, gemmaProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(pctx, http.MethodGet, base, nil)
	if err != nil {
		return false, "build req: " + err.Error()
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err.Error()
	}
	defer resp.Body.Close()
	return true, "" // 응답 수신 = 게이트웨이 reachable
}

func recordGemmaAudit(ctx context.Context, pool *pgxpool.Pool, status, errText string) {
	if pool == nil {
		return
	}
	_, err := pool.Exec(ctx, `
INSERT INTO kwave_kdb_codex_runs
  (source_domain, locale, rss_title, hint_count, status, spelling_count, duration_ms, error_text, ran_at)
VALUES ('__gemma_health__', '__system__', $1, 0, $2, 0, 0, $3, now())`,
		"GEMMA HEALTH", "gemma-"+status, nullIfEmpty(errText))
	if err != nil {
		log.Printf("kdb.recordGemmaAudit: %v", err)
	}
}

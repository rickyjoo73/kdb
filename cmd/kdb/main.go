// kdb-app — consolidated single-process KDB platform.
//
// Runs the lookup API (KDB_API_PORT, default 9100), the admin UI
// (KDB_ADMIN_PORT, default 9101), and the worker/autopilot loop in one process.
// LLM calls exec the `codex` CLI directly (internal/kdb/codexcli) — no Node
// bridge. The old per-service binaries (cmd/kdb-api, cmd/kdb-admin,
// cmd/kdb-worker) remain buildable for fallback.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"

	"github.com/rickyjoo73/kdb/internal/db"
	"github.com/rickyjoo73/kdb/internal/kdb"
	"github.com/rickyjoo73/kdb/internal/kdb/agents"
	"github.com/rickyjoo73/kdb/internal/kdb/aijudge"
	"github.com/rickyjoo73/kdb/internal/kdb/autopilot"
	"github.com/rickyjoo73/kdb/internal/kdb/hermes"
	"github.com/rickyjoo73/kdb/internal/kdbadmin"
	"github.com/rickyjoo73/kdb/internal/kdbapi"
)

func main() {
	_ = godotenv.Load()
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.LUTC | log.Lshortfile)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := db.New(ctx)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer pool.Close()

	// ─── one-shot subcommand: drain-candidates ────────────────────
	// `kdb-app drain-candidates [workers]` — 적체된 candidate 전체를 gpt 로
	// 분류해 인물DB / 고유명사DB / reject 로 일괄 정리하고 종료 (서버 미기동).
	if len(os.Args) > 1 && os.Args[1] == "drain-candidates" {
		workers := 4
		if len(os.Args) > 2 {
			if n, e := strconv.Atoi(os.Args[2]); e == nil && n > 0 {
				workers = n
			}
		}
		log.Printf("kdb-app: drain-candidates start (workers=%d)", workers)
		autopilot.New(pool).DrainCandidatesConcurrent(ctx, workers)
		log.Printf("kdb-app: drain-candidates done")
		return
	}

	// ─── one-shot subcommand: drain-persons ───────────────────────
	// `kdb-app drain-persons [workers]` — 고유명사DB 에 섞인 인명(unknown
	// candidate)을 gpt 로 분류해 person 인 것만 인물DB 로 이동하고 종료.
	if len(os.Args) > 1 && os.Args[1] == "drain-persons" {
		workers := 4
		if len(os.Args) > 2 {
			if n, e := strconv.Atoi(os.Args[2]); e == nil && n > 0 {
				workers = n
			}
		}
		log.Printf("kdb-app: drain-persons start (workers=%d)", workers)
		autopilot.New(pool).DrainPersonsConcurrent(ctx, workers)
		log.Printf("kdb-app: drain-persons done")
		return
	}

	// ─── diagnostic: classify-test ────────────────────────────────
	// `kdb-app classify-test <ko>` — 단건 gpt 분류 결과를 출력하고 종료.
	if len(os.Args) > 2 && os.Args[1] == "classify-test" {
		j := aijudge.New()
		res, err := j.Classify(ctx, &aijudge.ClassifyInput{Ko: os.Args[2]})
		log.Printf("classify-test ko=%q err=%v result=%+v", os.Args[2], err, res)
		return
	}

	// ─── API server (same options as cmd/kdb-api) ─────────────────
	apiPort := os.Getenv("KDB_API_PORT")
	if apiPort == "" {
		apiPort = "9100"
	}
	apiSrv := &http.Server{
		Addr: ":" + apiPort,
		Handler: kdbapi.NewRouterWithOptions(pool, kdbapi.RouterOptions{
			RequestTimeout: envDurationSeconds("KDB_API_REQUEST_TIMEOUT_SECONDS", 10*time.Second),
			LogRequests:    true,
			APIKeys:        envCSV("KDB_API_KEYS"),
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}

	// ─── Admin server (same options as cmd/kdb-admin) ─────────────
	adminPort := os.Getenv("KDB_ADMIN_PORT")
	if adminPort == "" {
		adminPort = "9101"
	}
	var secret []byte
	if s := os.Getenv("KDB_ADMIN_SESSION_SECRET"); s != "" {
		secret = []byte(s)
	}
	adminSrv := &http.Server{
		Addr: ":" + adminPort,
		Handler: kdbadmin.NewRouter(pool, kdbadmin.Options{
			SessionSecret: secret,
			LogRequests:   true,
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}

	// ─── graceful shutdown of both servers ────────────────────────
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := apiSrv.Shutdown(shutdownCtx); err != nil {
			log.Printf("api shutdown: %v", err)
		}
		if err := adminSrv.Shutdown(shutdownCtx); err != nil {
			log.Printf("admin shutdown: %v", err)
		}
	}()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		log.Printf("kdb-app api listening on :%s", apiPort)
		if err := apiSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("api listen: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		log.Printf("kdb-app admin listening on :%s", adminPort)
		if err := adminSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("admin listen: %v", err)
		}
	}()

	// ─── worker loop (identical to cmd/kdb-worker) ────────────────
	go runWorker(ctx, pool)

	wg.Wait()
}

// runWorker — fast 30s (BridgeHealthCheck + SweeperTick), poll 15m (PollerTick),
// autopilot 30m (autopilot.New(pool).Run, first run 30s after start).
func runWorker(ctx context.Context, pool *pgxpool.Pool) {
	fastInterval := envDurationSeconds("KDB_WORKER_FAST_INTERVAL_SECONDS", 30*time.Second)
	pollInterval := envDurationSeconds("KDB_WORKER_POLL_INTERVAL_SECONDS", 15*time.Minute)
	autoInterval := envDurationSeconds("KDB_AUTOPILOT_INTERVAL_SECONDS", 30*time.Minute)

	log.Printf("kdb-app worker starting fast=%s poll=%s autopilot=%s", fastInterval, pollInterval, autoInterval)

	auto := autopilot.New(pool)

	// Hermes supervisor (opt-in, additive). KDB_HERMES_ENABLED=1 runs the
	// existing 8 sweep steps as audited agents under the supervisor (per-step
	// run rows in kwave_kdb_hermes_runs + item-conservation/leak detection)
	// instead of the plain auto.Run. Default (unset) keeps the running
	// autopilot behaviour exactly as before. Requires migration 0061.
	runAutopilot := buildAutopilotRunner(pool, auto)

	runFast(ctx, pool)
	runPoll(ctx, pool)
	// 첫 autopilot 은 30 초 후 (startup 직후 cascade 호출 폭주 회피).
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
			runAutopilot(ctx)
		}
	}()

	fastTicker := time.NewTicker(fastInterval)
	defer fastTicker.Stop()
	pollTicker := time.NewTicker(pollInterval)
	defer pollTicker.Stop()
	autoTicker := time.NewTicker(autoInterval)
	defer autoTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Printf("kdb-app worker stopping")
			return
		case <-fastTicker.C:
			runFast(ctx, pool)
		case <-pollTicker.C:
			runPoll(ctx, pool)
		case <-autoTicker.C:
			go runAutopilot(ctx)
		}
	}
}

// buildAutopilotRunner returns the per-cycle autopilot function. When
// KDB_HERMES_ENABLED=1 it wraps the 8 sweep steps as audited agents under the
// Hermes supervisor (cmd-level wiring; no behaviour change to the steps).
// Otherwise it returns the plain auto.Run, preserving current behaviour.
func buildAutopilotRunner(pool *pgxpool.Pool, auto *autopilot.Sweeper) func(context.Context) {
	plain := func(ctx context.Context) { auto.Run(ctx) }
	if os.Getenv("KDB_HERMES_ENABLED") != "1" {
		return plain
	}
	registry := agents.NewRegistry()
	if err := auto.RegisterSteps(registry); err != nil {
		log.Printf("kdb-app: hermes register steps: %v — falling back to plain autopilot", err)
		return plain
	}
	supervisor := hermes.New(pool)
	// Reuse the existing circuit breaker (internal/kdb) via hooks to avoid an
	// import cycle.
	supervisor.Hooks = hermes.Hooks{
		BreakerIsOpen:       kdb.BreakerIsOpen,
		BreakerRecordResult: kdb.BreakerRecordResult,
	}
	log.Printf("kdb-app: Hermes supervisor enabled (%d steps)", registry.Len())
	return func(ctx context.Context) {
		supervisor.SuperviseCycle(ctx, registry)
	}
}

func runFast(ctx context.Context, pool *pgxpool.Pool) {
	kdb.BridgeHealthCheck(ctx, pool)
	go kdb.SweeperTick(ctx, pool)
}

func runPoll(ctx context.Context, pool *pgxpool.Pool) {
	kdb.PollerTick(ctx, pool)
}

func envDurationSeconds(key string, fallback time.Duration) time.Duration {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return fallback
	}
	return time.Duration(n) * time.Second
}

func envCSV(key string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

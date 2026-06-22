package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"

	"github.com/rickyjoo73/kdb/internal/db"
	"github.com/rickyjoo73/kdb/internal/kdb"
	"github.com/rickyjoo73/kdb/internal/kdb/agents"
	"github.com/rickyjoo73/kdb/internal/kdb/autopilot"
	"github.com/rickyjoo73/kdb/internal/kdb/codexcli"
	"github.com/rickyjoo73/kdb/internal/kdb/hermes"
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

	fastInterval := envDurationSeconds("KDB_WORKER_FAST_INTERVAL_SECONDS", 30*time.Second)
	pollInterval := envDurationSeconds("KDB_WORKER_POLL_INTERVAL_SECONDS", 15*time.Minute)
	autoInterval := envDurationSeconds("KDB_AUTOPILOT_INTERVAL_SECONDS", 30*time.Minute)

	log.Printf("kdb-worker starting fast=%s poll=%s autopilot=%s", fastInterval, pollInterval, autoInterval)

	auto := autopilot.New(pool)

	// 자율 폴백 와이어: Gemma 다운 시 gemma 라우팅을 Codex 로 폴백(cmd/kdb 와 parity).
	codexcli.GemmaDown = func() bool { return !kdb.GemmaHealthy() }
	// 거울: Codex breaker open 시 codex 라우팅을 로컬 gemma 로 인계(자가복구, 양방향).
	codexcli.CodexDown = kdb.BreakerIsOpen

	// Hermes supervisor (opt-in, additive). KDB_HERMES_ENABLED=1 runs the
	// existing 8 sweep steps as audited agents under the supervisor (per-step
	// run rows in kwave_kdb_hermes_runs + item-conservation/leak detection)
	// instead of the plain auto.Run. Default (unset) keeps the running
	// autopilot behaviour exactly as before. Requires migration 0061.
	var (
		supervisor   *hermes.Supervisor
		registry     *agents.Registry
		hermesActive bool
	)
	if os.Getenv("KDB_HERMES_ENABLED") == "1" {
		registry = agents.NewRegistry()
		if err := auto.RegisterSteps(registry); err != nil {
			log.Printf("kdb-worker: hermes register steps: %v — falling back to plain autopilot", err)
		} else {
			supervisor = hermes.New(pool)
			// Reuse the existing circuit breaker (internal/kdb) via hooks to
			// avoid an import cycle.
			supervisor.Hooks = hermes.Hooks{
				BreakerIsOpen:       kdb.BreakerIsOpen,
				BreakerRecordResult: kdb.BreakerRecordResult,
			}
			hermesActive = true
			log.Printf("kdb-worker: Hermes supervisor enabled (%d steps)", registry.Len())
		}
	}
	runAutopilot := func(ctx context.Context) {
		if hermesActive {
			supervisor.SuperviseCycle(ctx, registry)
			auto.RunTail(ctx) // Hermes 미등록 tail 보충 — cmd/kdb 와 parity
			return
		}
		auto.Run(ctx)
	}

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
			log.Printf("kdb-worker stopping")
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

func runFast(ctx context.Context, pool *pgxpool.Pool) {
	kdb.BridgeHealthCheck(ctx, pool)
	kdb.GemmaHealthCheck(ctx, pool) // Gemma 게이트웨이 감독 — cmd/kdb 와 parity
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

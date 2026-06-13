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
	"sync/atomic"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"

	"github.com/rickyjoo73/kdb/internal/db"
	"github.com/rickyjoo73/kdb/internal/kdb"
	"github.com/rickyjoo73/kdb/internal/kdb/agents"
	"github.com/rickyjoo73/kdb/internal/kdb/agents/enricher"
	"github.com/rickyjoo73/kdb/internal/kdb/aijudge"
	"github.com/rickyjoo73/kdb/internal/kdb/apikeys"
	"github.com/rickyjoo73/kdb/internal/kdb/autopilot"
	"github.com/rickyjoo73/kdb/internal/kdb/codexcli"
	"github.com/rickyjoo73/kdb/internal/kdb/corrections"
	"github.com/rickyjoo73/kdb/internal/kdb/dataqa"
	"github.com/rickyjoo73/kdb/internal/kdb/enrich"
	"github.com/rickyjoo73/kdb/internal/kdb/hermes"
	"github.com/rickyjoo73/kdb/internal/kdb/research"
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
		autoEnrichAfterClassify(ctx, pool, workers)
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
		autoEnrichAfterClassify(ctx, pool, workers)
		return
	}

	// ─── one-shot subcommand: drain-bucket ────────────────────────
	// `kdb-app drain-bucket [workers]` — 남은 unknown candidate 를 gpt 로 분류해
	// 실체 type(고유명사/person)으로 버킷팅하거나 일반어를 reject 하고 종료.
	// drain-persons 와 달리 모든 실체 type 을 대상으로 하고 영어 제목도 분류.
	if len(os.Args) > 1 && os.Args[1] == "drain-bucket" {
		workers := 4
		if len(os.Args) > 2 {
			if n, e := strconv.Atoi(os.Args[2]); e == nil && n > 0 {
				workers = n
			}
		}
		log.Printf("kdb-app: drain-bucket start (workers=%d)", workers)
		autopilot.New(pool).DrainBucketConcurrent(ctx, workers)
		log.Printf("kdb-app: drain-bucket done")
		autoEnrichAfterClassify(ctx, pool, workers)
		return
	}

	// ─── one-shot subcommand: drain-enrich ────────────────────────
	// `kdb-app drain-enrich [workers]` — 빈 외국어 locale/인물필드를 가진 active
	// entity backlog 를 Enricher 에이전트 cascade(L2 MusicBrainz→L3 Wikidata→L4
	// codex)로 단번에 비운다. 30분 cycle 의 budget(20) 제약 없이 backlog 0/수렴까지
	// 라운드 반복. Wikidata(~76%)는 worker 병렬, codex(~15%)는 codexGate 직렬.
	if len(os.Args) > 1 && os.Args[1] == "drain-enrich" {
		workers := 4
		if len(os.Args) > 2 {
			if n, e := strconv.Atoi(os.Args[2]); e == nil && n > 0 {
				workers = n
			}
		}
		log.Printf("kdb-app: drain-enrich start (workers=%d)", workers)
		enricher.New(codexcli.NewRunner()).DrainConcurrent(ctx, pool, workers)
		log.Printf("kdb-app: drain-enrich done")
		return
	}

	// ─── one-shot subcommand: resolve-unknowns ────────────────────
	// `kdb-app resolve-unknowns [workers]` — entity_type='unknown' 을 0 으로.
	// 로컬+Google News 검색 문맥으로 gpt 재분류 → 실체면 제 타입 active(인물은
	// 인물DB), 비실체면 term+rejected. "모르면 검색" 루프.
	if len(os.Args) > 1 && os.Args[1] == "resolve-unknowns" {
		workers := 4
		if len(os.Args) > 2 {
			if n, e := strconv.Atoi(os.Args[2]); e == nil && n > 0 {
				workers = n
			}
		}
		log.Printf("kdb-app: resolve-unknowns start (workers=%d)", workers)
		autopilot.New(pool).ResolveUnknownsConcurrent(ctx, workers)
		log.Printf("kdb-app: resolve-unknowns done")
		autoEnrichAfterClassify(ctx, pool, workers)
		return
	}

	// ─── data QA: dataqa ──────────────────────────────────────────
	// `kdb-app dataqa [--apply]` — person/group 로마자 locale 오염을 gpt-5.5 로 검수.
	// 기본 dry-run(리포트만). --apply 시 오염 locale 을 감사로그 남기고 비운다(복구 가능).
	if len(os.Args) > 1 && os.Args[1] == "dataqa" {
		apply := false
		for _, a := range os.Args[2:] {
			if a == "--apply" {
				apply = true
			}
		}
		runDataQA(ctx, pool, apply)
		return
	}

	// ─── corrections review: corrections ──────────────────────────
	// `kdb-app corrections [list|approve <id>|reject <id> [사유]]`
	// 외부 소비자 정정 신고 큐(자동반영 미달분)를 운영자가 심사.
	if len(os.Args) > 1 && os.Args[1] == "corrections" {
		runCorrections(ctx, pool, os.Args[2:])
		return
	}

	// ─── one-shot subcommand: import-kenterhub ────────────────────
	// `kdb-app import-kenterhub <json>` — kenterhub.com /api/celebrities 덤프를
	// candidate 로 등록(Observe 경로 재사용 — PreGate·동명이인 안전 그대로).
	if len(os.Args) > 2 && os.Args[1] == "import-kenterhub" {
		log.Printf("kdb-app: import-kenterhub start (%s)", os.Args[2])
		runImportKenterhub(ctx, pool, os.Args[2])
		return
	}

	// ─── diagnostic: api-test ─────────────────────────────────────
	// `kdb-app api-test` — 외부 API 연결을 실측 점검(키는 DB/.env). OK/FAIL 출력.
	if len(os.Args) > 1 && os.Args[1] == "api-test" {
		for _, p := range apikeys.Probe(ctx, pool) {
			st := "OK  "
			if p.Skipped {
				st = "SKIP"
			} else if !p.OK {
				st = "FAIL"
			}
			log.Printf("api-test [%s] %-16s %s", st, p.Title, p.Detail)
		}
		return
	}

	// ─── diagnostic: enrich-test ──────────────────────────────────
	// `kdb-app enrich-test <ko>` — canonical_ko 로 entity 찾아 enrich 1회 실행 후
	// 결과(채운 locale/source)를 출력. TMDb/KOFIC/Wikidata 연동 점검용.
	if len(os.Args) > 2 && os.Args[1] == "enrich-test" {
		var id string
		if err := pool.QueryRow(ctx,
			`SELECT id::text FROM kwave_entities WHERE canonical_ko=$1 ORDER BY (status='active') DESC LIMIT 1`,
			os.Args[2]).Scan(&id); err != nil {
			log.Printf("enrich-test: entity 없음 ko=%q: %v", os.Args[2], err)
			return
		}
		uid, _ := uuid.Parse(id)
		rep, err := enrich.New(pool).Enrich(ctx, uid)
		log.Printf("enrich-test ko=%q type=%s err=%v layers=%v filled=%+v stillEmpty=%v",
			os.Args[2], rep.EntityType, err, rep.LayersRun, rep.Filled, rep.StillEmpty)
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
	researchInterval := envDurationSeconds("KDB_RESEARCH_INTERVAL_SECONDS", 60*time.Second)
	dataqaInterval := envDurationSeconds("KDB_DATAQA_INTERVAL_SECONDS", 20*time.Minute)
	dataqaOn := os.Getenv("KDB_DATAQA_ENABLED") == "1"

	log.Printf("kdb-app worker starting fast=%s poll=%s autopilot=%s research=%s dataqa=%v(%s)", fastInterval, pollInterval, autoInterval, researchInterval, dataqaOn, dataqaInterval)

	auto := autopilot.New(pool)
	researchWorker := research.New(pool)

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
	researchTicker := time.NewTicker(researchInterval)
	defer researchTicker.Stop()
	dataqaTicker := time.NewTicker(dataqaInterval)
	defer dataqaTicker.Stop()

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
		case <-researchTicker.C:
			go researchWorker.Tick(ctx)
		case <-dataqaTicker.C:
			if dataqaOn {
				go runDataQATick(ctx, pool)
			}
		}
	}
}

// dataqaRunning — 워커 내 dataqa tick single-flight.
var dataqaRunning atomic.Bool

// runDataQATick — 주기적 자가치유: pending 의심 entity 한 배치를 gpt-5.5 로 검수해
// 오염 locale 정리(감사·복구가능) + duplicate 플래그. codex 는 flock 으로 다른
// 스텝과 직렬화돼 안전. 한 번에 한 배치만(점진 커버리지).
func runDataQATick(ctx context.Context, pool *pgxpool.Pool) {
	if !dataqaRunning.CompareAndSwap(false, true) {
		return
	}
	defer dataqaRunning.Store(false)
	st, _, err := dataqa.RunBatch(ctx, pool, codexcli.NewRunner(), dataqa.Schema, 20, true)
	if err != nil {
		log.Printf("kdb-app dataqa-tick: %v", err)
		return
	}
	if st.Reviewed > 0 {
		log.Printf("kdb-app dataqa-tick: reviewed=%d contaminated=%d(cleared %d fields) dup=%d unc=%d",
			st.Reviewed, st.Contaminated, st.ClearedFields, st.Duplicate, st.Uncertain)
	}
}

// runCorrections — 외부 소비자 정정 신고 심사 큐 CLI.
//   list                 — 대기 큐 출력
//   approve <id>         — suggested 적용(source=operator, 원값 스냅샷으로 revert 가능)
//   reject  <id> [사유]  — 거부
func runCorrections(ctx context.Context, pool *pgxpool.Pool, args []string) {
	op := "list"
	if len(args) > 0 {
		op = args[0]
	}
	switch op {
	case "list":
		n, _ := corrections.CountPending(ctx, pool)
		items, err := corrections.ListPending(ctx, pool, 100)
		if err != nil {
			log.Fatalf("corrections list: %v", err)
		}
		log.Printf("kdb-app corrections: pending=%d", n)
		for _, p := range items {
			log.Printf("  #%d  ko=%q locale=%s  %q→%q  근거=%s  신고자=%s  %s",
				p.ID, p.Ko, p.Locale, p.Returned, p.Suggested, p.EvidenceURL, p.Reporter, p.Reason)
		}
		if n > 0 {
			log.Printf("승인: kdb-app corrections approve <id> / 거부: kdb-app corrections reject <id> [사유]")
		}
	case "approve":
		if len(args) < 2 {
			log.Fatalf("usage: kdb-app corrections approve <id>")
		}
		id, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			log.Fatalf("bad id %q", args[1])
		}
		if err := corrections.Approve(ctx, pool, id, "cli"); err != nil {
			log.Fatalf("approve #%d: %v", id, err)
		}
		log.Printf("kdb-app corrections: #%d 승인·적용 완료", id)
	case "reject":
		if len(args) < 2 {
			log.Fatalf("usage: kdb-app corrections reject <id> [사유]")
		}
		id, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			log.Fatalf("bad id %q", args[1])
		}
		why := strings.Join(args[2:], " ")
		if err := corrections.Reject(ctx, pool, id, "cli", why); err != nil {
			log.Fatalf("reject #%d: %v", id, err)
		}
		log.Printf("kdb-app corrections: #%d 거부", id)
	default:
		log.Fatalf("unknown: corrections %s (list|approve|reject)", op)
	}
}

// runDataQA — person/group 로마자 locale 오염을 gpt-5.5 로 배치 검수하고, --apply 시
// 오염 locale 을 감사로그 남긴 뒤 비운다(kwave_kdb_dataqa_log 로 복구 가능). codex 는
// 직렬화돼 있어 배치당 ~1분.
func runDataQA(ctx context.Context, pool *pgxpool.Pool, apply bool) {
	total, err := dataqa.CountSuspects(ctx, pool)
	if err != nil {
		log.Fatalf("dataqa count: %v", err)
	}
	log.Printf("kdb-app dataqa: pending suspect person/group=%d (apply=%v)", total, apply)
	runner := codexcli.NewRunner()
	const batch = 20
	var agg dataqa.Stats
	for ctx.Err() == nil {
		st, verds, err := dataqa.RunBatch(ctx, pool, runner, dataqa.Schema, batch, apply)
		if err != nil {
			log.Printf("  batch err: %v", err)
			break
		}
		if st.Reviewed == 0 {
			break // pending 소진
		}
		for _, v := range verds {
			if v.Verdict == "contaminated" {
				log.Printf("  [contaminated] %s wrong=%v — %s", v.ID, v.WrongFields, v.Reason)
			} else if v.Verdict == "duplicate" {
				log.Printf("  [dup] %s — %s", v.ID, v.Reason)
			}
		}
		agg.Reviewed += st.Reviewed
		agg.OK += st.OK
		agg.Contaminated += st.Contaminated
		agg.Duplicate += st.Duplicate
		agg.Uncertain += st.Uncertain
		agg.ClearedFields += st.ClearedFields
		agg.FlaggedDup += st.FlaggedDup
		log.Printf("  progress reviewed=%d (ok=%d cont=%d dup=%d unc=%d)", agg.Reviewed, agg.OK, agg.Contaminated, agg.Duplicate, agg.Uncertain)
	}
	log.Printf("kdb-app dataqa done: reviewed=%d ok=%d contaminated=%d duplicate=%d uncertain=%d cleared_fields=%d flagged_dup=%d (apply=%v)",
		agg.Reviewed, agg.OK, agg.Contaminated, agg.Duplicate, agg.Uncertain, agg.ClearedFields, agg.FlaggedDup, apply)
	if !apply && agg.Contaminated > 0 {
		log.Printf("kdb-app dataqa: dry-run — --apply 로 %d 건 오염 locale 정리(복구는 kwave_kdb_dataqa_log)", agg.Contaminated)
	}
}

// buildAutopilotRunner returns the per-cycle autopilot function. When
// KDB_HERMES_ENABLED=1 it wraps the 8 sweep steps as audited agents under the
// Hermes supervisor (cmd-level wiring; no behaviour change to the steps).
// Otherwise it returns the plain auto.Run, preserving current behaviour.
// autoEnrichAfterClassify — Phase 4 유입 제어. 대량 분류 drain(drain-candidates/
// bucket/persons/resolve-unknowns)이 새로 active 시킨 entity 가 빈 locale 인 채로
// enrich backlog 스파이크를 만들지 않게, 분류 직후 Enricher 수렴 패스를 이어 돈다.
// DrainConcurrent 는 source-exhausted 필드를 건너뛰므로 이미 채워졌거나 채울 수
// 없는 기존 건은 재작업하지 않고 사실상 신규분만 처리한다(수렴 상태에선 짧게 끝남).
// KDB_AUTO_ENRICH_AFTER_DRAIN=0 로 끌 수 있다(대량 적체 시 분류만 빠르게 돌릴 때).
func autoEnrichAfterClassify(ctx context.Context, pool *pgxpool.Pool, workers int) {
	if os.Getenv("KDB_AUTO_ENRICH_AFTER_DRAIN") == "0" {
		log.Printf("kdb-app: 분류 후 자동 enrich 비활성(KDB_AUTO_ENRICH_AFTER_DRAIN=0)")
		return
	}
	log.Printf("kdb-app: 분류 후 자동 enrich 시작 (유입 제어, workers=%d)", workers)
	enricher.New(codexcli.NewRunner()).DrainConcurrent(ctx, pool, workers)
	log.Printf("kdb-app: 분류 후 자동 enrich 완료")
}

func buildAutopilotRunner(pool *pgxpool.Pool, auto *autopilot.Sweeper) func(context.Context) {
	plain := func(ctx context.Context) { auto.Run(ctx) }
	runner := plain

	if os.Getenv("KDB_HERMES_ENABLED") == "1" {
		registry := agents.NewRegistry()
		if err := auto.RegisterSteps(registry); err != nil {
			log.Printf("kdb-app: hermes register steps: %v — falling back to plain autopilot", err)
		} else {
			supervisor := hermes.New(pool)
			// Reuse the existing circuit breaker (internal/kdb) via hooks to avoid
			// an import cycle.
			supervisor.Hooks = hermes.Hooks{
				BreakerIsOpen:       kdb.BreakerIsOpen,
				BreakerRecordResult: kdb.BreakerRecordResult,
			}
			log.Printf("kdb-app: Hermes supervisor enabled (%d steps)", registry.Len())
			runner = func(ctx context.Context) {
				supervisor.SuperviseCycle(ctx, registry)
			}
		}
	}

	// Single-flight 가드 (양쪽 모드 공통). cmd 가 30분 ticker 로 `go runAutopilot()`
	// 을 띄우므로, 한 cycle 이 30분을 넘기면 다음 ticker 가 같은 Sweeper/DB 위에서
	// 두 번째 cycle 을 동시에 돌려 Codex 비용 2배 + 같은 row 경합이 난다. plain 경로
	// (auto.Run)는 자체 guard 가 있으나 Hermes 경로(SuperviseCycle)는 없어, 여기서
	// 공통으로 막는다 (Hermes 의 guard 부재 = 리뷰 H5).
	var running atomic.Bool
	return func(ctx context.Context) {
		if !running.CompareAndSwap(false, true) {
			log.Printf("kdb-app: autopilot cycle still running — skip overlapping tick")
			return
		}
		defer running.Store(false)
		runner(ctx)
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

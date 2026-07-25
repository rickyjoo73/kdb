// Package research — on-demand 검색 기반 엔티티 발굴 워커 (2026-06-01).
//
// 발굴 무게중심 이동: RSS passive 수집(언제 걸릴지 모르는 pull-luck)을 줄이고,
// 소비자(mediafine 등)가 KDB 에 없는 이름을 질의하면(lookup miss) 그 이름을
// kwave_entity_research_queue 에 적재 → 이 워커가 그 자리에서 검색으로 발굴한다.
//
// 흐름(미검증 데이터 핫패스 배제 원칙 준수):
//  1. pending 큐 row claim (atomic, single-flight).
//  2. 같은 canonical_ko 가 이미 active 면 done (이미 알고 있음).
//  3. 없으면 bare candidate INSERT (conf 0.4, status candidate).
//  4. enrich cascade 실행 → Wikidata 이름검증 가드(SearchAndFetch) 통과 시 다국어 채움.
//  5. Wikidata 검증 통과(rep.LayersRun 에 "wikidata") → active 승격(conf 0.72,
//     provenance=wikidata). 미검증(검색 miss) → candidate 유지(0.4) — RSS/운영자 보강 대상.
//
// 즉 "Wikidata 검증분만 신뢰, 미검증은 candidate" 원칙을 발굴 단계에서 코드화한다.
package research

import (
	"context"
	"errors"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb"
	"github.com/rickyjoo73/kdb/internal/kdb/agents/gatekeeper"
	"github.com/rickyjoo73/kdb/internal/kdb/enrich"
	"github.com/rickyjoo73/kdb/internal/kdb/verify"
)

const (
	maxAttempts         = 3 // 이 횟수 넘으면 failed
	enrichTimeout       = 150 * time.Second
	promoteConf         = 0.72 // Wikidata 검증 발굴분 신뢰도
	transientRetryDelay = 15 * time.Minute
)

var errIntakePrecheckStopped = errors.New("research intake stopped before provider work")

// batchSize — tick 당 처리 상한. 실측(2026-07-02) e2e p90 295s 의 주범이 pick 대기
// (batch 5 × 60s tick)라 상향. 롤백: KDB_RESEARCH_BATCH=5.
func batchSize() int { return envIntDefault("KDB_RESEARCH_BATCH", 16) }

// workerCount — 배치 내 병렬 워커 수. claim 이 FOR UPDATE SKIP LOCKED 라 동시 선점
// 안전, enrich 의 LLM 은 gemma(24)/codex(3) 전역 sem 이 상한. 롤백: KDB_RESEARCH_WORKERS=1.
func workerCount() int { return envIntDefault("KDB_RESEARCH_WORKERS", 4) }

func envIntDefault(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// localMediaLocales — 현지 매체가 실제 표기를 쓰는 K-콘텐츠 타겟 시장.
// 발굴 시 이 중 빈 locale 에 대해 현지 매체를 즉시 검색해 통용 표기를 확보한다.
// (en 은 글로벌·위키로 충분해 제외 — 현지 매체 고유가치 낮음.)
var localMediaLocales = []struct{ code, col string }{
	{"ja", "canonical_ja"},
	{"vi", "canonical_vi"},
	{"zh-hant", "canonical_zh_hant"},
	{"es", "canonical_es"},
	{"id", "canonical_id"},
	{"pt-br", "canonical_pt_br"},
}

// Worker — research_queue 소비 발굴기.
type Worker struct {
	Pool    *pgxpool.Pool
	Orch    *enrich.Orchestrator
	Site    *kdb.SiteSearchService
	running atomic.Bool
}

// New — 기본 생성자.
func New(pool *pgxpool.Pool) *Worker {
	return &Worker{Pool: pool, Orch: enrich.New(pool), Site: kdb.NewSiteSearchService(pool)}
}

// Tick — single-flight. 비활성 플래그면 즉시 반환.
func (w *Worker) Tick(ctx context.Context) {
	if os.Getenv("KDB_DISABLE_RESEARCH_WORKER") == "1" {
		return
	}
	if !w.running.CompareAndSwap(false, true) {
		return // 직전 배치 진행 중 — skip
	}
	defer w.running.Store(false)
	w.RunOnce(ctx, batchSize())
}

// RunOnce — pending 큐를 최대 max 건 처리. workerCount()>1 이면 병렬 — 각 워커가
// claim(SKIP LOCKED, 원자 선점) 루프를 돌아 배치 상한까지 소비한다. 중간 크래시로
// 고아화된 in_progress 는 reapStale(10분)이 회수(기존 안전망, 변경 없음).
func (w *Worker) RunOnce(ctx context.Context, max int) {
	w.reapStale(ctx)
	w.retryDeferredLocaleSearches(ctx, 4)
	workers := workerCount()
	if workers > max {
		workers = max
	}
	if workers <= 1 {
		for i := 0; i < max; i++ {
			if ctx.Err() != nil {
				return
			}
			if !w.claimAndProcess(ctx) {
				return // pending 없음
			}
		}
		return
	}
	remaining := int64(max)
	var wg sync.WaitGroup
	for k := 0; k < workers; k++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				if ctx.Err() != nil {
					return
				}
				if atomic.AddInt64(&remaining, -1) < 0 {
					return // 배치 상한 소진
				}
				if !w.claimAndProcess(ctx) {
					return // pending 없음
				}
			}
		}()
	}
	wg.Wait()
}

// claimAndProcess — 한 건 선점→처리→종결. 반환 false = pending 없음(워커 종료 신호).
func (w *Worker) claimAndProcess(ctx context.Context) bool {
	id, koHint, reqType, attempts, ok := w.claim(ctx)
	if !ok {
		return false
	}
	if err := w.process(ctx, id, koHint, reqType); err != nil {
		if errors.Is(err, errIntakePrecheckStopped) {
			return true
		}
		w.fail(ctx, id, attempts, err)
		return true
	}
	w.finish(ctx, id)
	return true
}

// reapStale — process() 도중 프로세스 재시작/크래시로 in_progress 에 고아화된 row
// 를 pending 으로 복구한다(picked_at 10분 경과 기준). claim 이 attempts++ 하므로
// maxAttempts 로 무한루프는 방지됨. 재시작 빈발 환경에서 term 유실 누수 차단.
func (w *Worker) reapStale(ctx context.Context) {
	tag, err := w.Pool.Exec(ctx, `
UPDATE kwave_entity_research_queue
   SET status='pending'
 WHERE status='in_progress' AND picked_at < now() - interval '10 minutes'`)
	if err == nil && tag.RowsAffected() > 0 {
		log.Printf("kdb.research: reaped %d stale in_progress → pending", tag.RowsAffected())
	}
}

// claim — pending row 하나를 in_progress 로 원자적 선점.
func (w *Worker) claim(ctx context.Context) (id uuid.UUID, koHint, reqType string, attempts int, ok bool) {
	err := w.Pool.QueryRow(ctx, `
UPDATE kwave_entity_research_queue
   SET status = 'in_progress', picked_at = now(), attempts = attempts + 1,
       next_attempt_at = NULL, last_outcome = ''
 WHERE id = (
   SELECT id FROM kwave_entity_research_queue
    WHERE status = 'pending'
	  AND precheck_status IN ('pass','approved')
      AND (next_attempt_at IS NULL OR next_attempt_at <= now())
    ORDER BY created_at
    FOR UPDATE SKIP LOCKED
    LIMIT 1
 )
 RETURNING id, entity_ko, requested_entity_type::text, attempts`).
		Scan(&id, &koHint, &reqType, &attempts)
	if err != nil {
		return uuid.Nil, "", "", 0, false
	}
	return id, koHint, reqType, attempts, true
}

// process — 발굴 한 건. 에러면 호출자가 재시도 처리.
func (w *Worker) process(ctx context.Context, queueID uuid.UUID, koHint, reqType string) error {
	// 1) 이미 active 또는 rejected 면 발굴 불필요.
	// rejected = 게이트키퍼가 K-콘텐츠 아님으로 판정한 경우 → 재발굴 금지.
	var existCount int
	if err := w.Pool.QueryRow(ctx,
		`SELECT count(*) FROM kwave_entities WHERE canonical_ko = $1 AND status IN ('active','rejected')`,
		koHint).Scan(&existCount); err != nil {
		return err
	}
	if existCount > 0 {
		return nil // done — 이미 알고 있거나 기각됨
	}

	// Provider-cost authorization is re-evaluated after claim. This protects
	// against legacy rows, direct SQL, an older app instance, or a stale rule
	// version forging precheck_status='pass'. Review/reject is terminal and has
	// zero candidate/provider side effects.
	var contextHint, sourceURL, precheckStatus, intakeOrigin string
	var hasSourceID bool
	if err := w.Pool.QueryRow(ctx, `
SELECT COALESCE(context_hint,''), COALESCE(source_url,''), source_id IS NOT NULL,
       COALESCE(precheck_status,'legacy'), COALESCE(intake_origin,'unknown')
  FROM kwave_entity_research_queue WHERE id=$1`, queueID).
		Scan(&contextHint, &sourceURL, &hasSourceID, &precheckStatus, &intakeOrigin); err != nil {
		return err
	}
	decision := gatekeeper.DecideIntake(gatekeeper.IntakeInput{
		Term: koHint, EntityType: reqType, Context: contextHint, SourceURL: sourceURL,
		HasSourceID: hasSourceID, SourceTrusted: w.isTrustedIntakeSource(ctx, sourceURL),
		OperatorApproved: precheckStatus == "approved",
	})
	if decision.Verdict != gatekeeper.IntakePass {
		w.stopAtPrecheck(ctx, queueID, decision)
		return errIntakePrecheckStopped
	}
	// approved(운영자/자동검증) row 는 승인 사유·증거 플래그(auto_evidence 등)를 보존한다 —
	// 여기서 'operator_approved' 로 덮으면 자동 승격이 사람 승인으로 위장 기록된다(정직한 가시성).
	_, _ = w.Pool.Exec(ctx, `
UPDATE kwave_entity_research_queue
   SET precheck_status=CASE WHEN precheck_status='approved' THEN 'approved' ELSE 'pass' END,
       precheck_reason=CASE WHEN precheck_status='approved' THEN precheck_reason ELSE $2 END,
       precheck_flags=CASE WHEN precheck_status='approved' THEN precheck_flags ELSE $3 END,
       precheck_rule_version=$4
 WHERE id=$1`, queueID, decision.ReasonCode, decision.Flags, decision.RuleVersion)

	// 2) candidate 확보 (있으면 재사용, 없으면 신규). homonym 다수면 운영자 몫 → skip.
	var ids []uuid.UUID
	rows, err := w.Pool.Query(ctx,
		`SELECT id FROM kwave_entities WHERE canonical_ko = $1 AND status = 'candidate'`, koHint)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	var entityID uuid.UUID
	switch {
	case len(ids) == 1:
		entityID = ids[0]
	case len(ids) >= 2:
		return nil // 동명이인 후보 다수 — blind 발굴 금지, 운영자 리뷰
	default:
		et := reqType
		if et == "" {
			et = "unknown"
		}
		// 소비자 type힌트를 notes 에 남긴다 — classify 프롬프트가 notes 를 받으므로
		// 힌트가 사전확률로 전달된다(2026-07-25 인입감사: 힌트 미전달로 classify 가
		// 옳은 힌트를 뒤집는 오분류 실측 — JTBC→drama·김지운→song_album 등).
		notes := "KDB candidate — on-demand 검색 발굴 (lookup-miss)"
		if et != "unknown" && et != "term" {
			notes += " · 소비자 type힌트=" + et
		}
		insertErr := w.Pool.QueryRow(ctx, `
INSERT INTO kwave_entities (canonical_ko, entity_type, confidence, status, notes)
VALUES ($1, $2::kwave_entity_type, 0.400, 'candidate', $3)
RETURNING id`, koHint, et, notes).Scan(&entityID)
		if insertErr != nil {
			// 23505: unique violation — 같은 (canonical_ko, entity_type, disambig) 가 이미 있음.
			// kwave_entities_homonym_key 는 UNIQUE INDEX(명명된 CONSTRAINT 아님)라 ON CONFLICT
			// ON CONSTRAINT 를 쓸 수 없으므로, 에러 후 기존 row 를 SELECT 해 재사용한다.
			var pgErr *pgconn.PgError
			if errors.As(insertErr, &pgErr) && pgErr.Code == "23505" {
				if err2 := w.Pool.QueryRow(ctx,
					`SELECT id FROM kwave_entities WHERE canonical_ko = $1 AND entity_type = $2::kwave_entity_type AND COALESCE(disambig,'') = ''`,
					koHint, et).Scan(&entityID); err2 != nil {
					return err2
				}
			} else {
				return insertErr
			}
		}
	}

	// 3) enrich cascade — Wikidata 이름검증 가드 경유. 빈 locale 채움.
	ectx, cancel := context.WithTimeout(ctx, enrichTimeout)
	rep, err := w.Orch.Enrich(ectx, entityID)
	cancel()
	if err != nil {
		return err
	}

	// 4) Wikidata 검증 통과 시에만 active 승격(미검증은 candidate 유지).
	if rep != nil && containsLayer(rep.LayersRun, "wikidata") {
		if _, err := w.Pool.Exec(ctx, `
UPDATE kwave_entities
   SET status = 'active',
       confidence = GREATEST(confidence, $2::numeric),
       updated_at = now()
 WHERE id = $1 AND status = 'candidate'`, entityID, promoteConf); err != nil {
			return err
		}
		log.Printf("kdb.research: 발굴 active 승격 ko=%q id=%s (wikidata 검증)", koHint, entityID)
	} else {
		log.Printf("kdb.research: 발굴 candidate 유지 ko=%q id=%s (wikidata 미검증)", koHint, entityID)
		// ★프레시 패스트레인(2026-07-25 전략1): 소비자 origin 발굴이 wikidata 미검증으로
		// candidate 에 머물면, 20분 cand-evidence 스위프를 기다리지 않고 그 1건만 뉴스근거
		// 판정을 즉시 실행(비동기) — 요청→서빙 p50 15h 의 주범이 스위프 대기였다.
		// 게이트·오염 방어는 스위프와 동일 코드 재사용. 실패해도 스위프가 이어받는다.
		if isConsumerOrigin(intakeOrigin) {
			go func(id string) {
				fctx, fcancel := context.WithTimeout(context.Background(), 90*time.Second)
				defer fcancel()
				_, _ = verify.CandidateEvidenceOne(fctx, w.Pool, id)
			}(entityID.String())
		}
	}

	// 5) ★현지 통용 표기 확보 — 위키(공식표기)로 못 채운 빈 현지 locale 에 대해
	//    그 이름으로 현지 매체를 검색(site-search) → 본문에서 실제 표기 추출.
	//    결과는 raw 적재 → sweeper 추출 → 매체 합의(2곳+)로 확정/갱신(기존 파이프라인).
	//    위키 라벨 ≠ 현지 통용 표기일 수 있으므로, "현지표기는 현지 매체에서" 원칙.
	//
	//    ★온디맨드 "빠른 채움"(2026-07-04): 이 단계는 비동기 관측 파이프라인이라 결과를
	//    sweeper/consensus 가 나중에 확정한다(즉시 안 채워짐). 그런데 동기 for 루프
	//    (6 locale × 20s)가 발굴 finished_at 을 최대 120s 늦춰 채움 지연 avg 158s 의 주범이었다.
	//    소비자 서빙에 필요한 canonical_ko + 권위표기(Wikidata/TMDb)는 (3)(4)에서 완료되므로,
	//    현지표기 확보는 백그라운드로 떼어 큐를 즉시 종결한다(SearXNG 부하는 세마포어로 원래 수준 유지).
	w.scheduleLocalMediaSearch(ctx, queueID, entityID)
	// ★넷플릭스/디즈니 OTT 공식제목 보강(작품 한정, 오너 "넷플릭스도 가져오고"): 무QID·무TMDb
	//   작품의 codex/빈칸 현지 locale 을 공식 OTT 제목으로 채운다. 큐 종결과 분리(백그라운드),
	//   IP 차단 방지 위해 전역 1개만 직렬 실행(best-effort — 진행 중이면 스킵).
	w.triggerOTTBoost(koHint)
	return nil
}

// isConsumerOrigin — 소비자가 지금 기다리는 유입인가(패스트레인 대상). 내부 축
// (rss-observation 등)은 스위프 배치로 충분 — 네이버 쿼터를 소비자 체감에 우선 배분.
func isConsumerOrigin(origin string) bool {
	switch origin {
	case "prepare", "lookup-miss", "correction-miss", "correction":
		return true
	}
	return false
}

func (w *Worker) isTrustedIntakeSource(ctx context.Context, rawURL string) bool {
	if gatekeeper.ConfiguredTrustedIntakeSourceURL(rawURL) {
		return true
	}
	host, ok := gatekeeper.IntakeSourceHost(rawURL)
	if !ok || w == nil || w.Pool == nil {
		return false
	}
	var trusted bool
	_ = w.Pool.QueryRow(ctx, `
SELECT EXISTS(SELECT 1 FROM kwave_news_whitelist
               WHERE discovery_enabled=true
                 AND lower(regexp_replace(domain, '^www\.', ''))=$1)`, host).Scan(&trusted)
	return trusted
}

func (w *Worker) stopAtPrecheck(ctx context.Context, id uuid.UUID, decision gatekeeper.IntakeDecision) {
	resolution, outcome := "review_required", "precheck_review"
	if decision.Verdict == gatekeeper.IntakeReject {
		resolution, outcome = "rejected_precheck", "precheck_reject"
	}
	_, _ = w.Pool.Exec(ctx, `
UPDATE kwave_entity_research_queue
   SET status='done', finished_at=now(), picked_at=NULL, next_attempt_at=NULL,
       resolution_status=$2, locale_status='blocked_precheck', last_outcome=$3,
       precheck_status=$4, precheck_reason=$5, precheck_flags=$6,
       precheck_rule_version=$7,
       last_error='provider calls blocked by proper-noun precheck'
 WHERE id=$1`, id, resolution, outcome, string(decision.Verdict), decision.ReasonCode,
		decision.Flags, decision.RuleVersion)
}

// localMediaSem — 백그라운드 현지매체 site-search 동시 실행 상한(SearXNG 부하 보호).
// 발굴을 큐에서 즉시 종결시키되(빠른 채움), site-search 자체의 동시성은 종전 워커수 수준으로 억제.
var localMediaSem = make(chan struct{}, 4)

// ottSem — OTT 보강 전역 직렬화(동시 1개). Netflix/Disney 는 IP 차단 방지로 pacing 이 필수라
// 절대 벌크 금지 — 여러 발굴이 동시에 OTT 를 때리지 않게 한 번에 하나만.
var ottSem = make(chan struct{}, 1)

// triggerOTTBoost — 방금 발굴된 작품(ko)의 현지제목을 공식 OTT(Netflix→Disney)로 보강한다.
// DrainOTTCascade 가 내부에서 작품 type·무QID/무TMDb 여부를 게이트하므로 비작품·부적격 ko 는
// 즉시 0건 no-op. best-effort: 다른 OTT 진행 중이면 스킵(다음 발굴/운영자가 커버).
func (w *Worker) triggerOTTBoost(ko string) {
	if strings.TrimSpace(ko) == "" || os.Getenv("KDB_DISABLE_OTT_BOOST") == "1" {
		return
	}
	select {
	case ottSem <- struct{}{}:
	default:
		return // 다른 OTT 진행 중 — 전역 직렬화 유지(IP 차단 방지)
	}
	go func() {
		defer func() { <-ottSem }()
		octx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
		defer cancel()
		if _, filled := kdb.DrainOTTCascade(octx, w.Pool, 1, ko); filled > 0 {
			log.Printf("kdb.research: OTT 보강 ko=%q filled=%d locale", ko, filled)
		}
	}()
}

// triggerLocalMediaSearch — entity 의 빈 현지 locale 마다 현지 매체 검색을 시동.
// 비동기 관측 파이프라인이므로 즉시 채워지지 않고 다음 sweeper/consensus 에서 확정.
func (w *Worker) triggerLocalMediaSearch(ctx context.Context, id uuid.UUID) (attempted, enqueued, blocked int) {
	if w.Site == nil || os.Getenv("KDB_DISABLE_LOCAL_MEDIA_SEARCH") == "1" {
		return 0, 0, 0
	}
	var ja, vi, zhHant, es, idn, ptBr string
	if err := w.Pool.QueryRow(ctx, `
SELECT COALESCE(canonical_ja,''), COALESCE(canonical_vi,''), COALESCE(canonical_zh_hant,''),
       COALESCE(canonical_es,''), COALESCE(canonical_id,''), COALESCE(canonical_pt_br,'')
	  FROM kwave_entities WHERE id = $1`, id).Scan(&ja, &vi, &zhHant, &es, &idn, &ptBr); err != nil {
		return 0, 0, 0
	}
	vals := map[string]string{
		"canonical_ja": ja, "canonical_vi": vi, "canonical_zh_hant": zhHant,
		"canonical_es": es, "canonical_id": idn, "canonical_pt_br": ptBr,
	}
	for _, loc := range localMediaLocales {
		if vals[loc.col] != "" {
			continue // 이미 값 있음(위키 등) — 빈칸 우선 확보
		}
		sctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		resp, err := w.Site.SearchAndEnqueue(sctx, kdb.SiteSearchRequest{EntityID: id, Locale: loc.code})
		cancel()
		if err != nil {
			if errors.Is(err, kdb.ErrNoDiscoverySource) {
				blocked++
				continue
			}
			attempted++
			log.Printf("kdb.research: 현지매체 검색 실패 id=%s locale=%s: %v", id, loc.code, err)
			continue
		}
		attempted++
		if resp != nil {
			enqueued += resp.Enqueued
		}
	}
	return attempted, enqueued, blocked
}

func (w *Worker) scheduleLocalMediaSearch(ctx context.Context, queueID, entityID uuid.UUID) {
	if w.Site == nil || os.Getenv("KDB_DISABLE_LOCAL_MEDIA_SEARCH") == "1" {
		w.setLocaleStatus(ctx, queueID, "complete", "")
		return
	}
	locales := make([]string, 0, len(localMediaLocales))
	for _, loc := range localMediaLocales {
		locales = append(locales, loc.code)
	}
	var hasSource bool
	if err := w.Pool.QueryRow(ctx, `
SELECT EXISTS(SELECT 1 FROM kwave_news_whitelist
               WHERE discovery_enabled=true AND locale=ANY($1::text[]))`, locales).Scan(&hasSource); err != nil {
		w.setLocaleStatus(ctx, queueID, "retrying", "transient")
		return
	}
	if !hasSource {
		w.setLocaleStatus(ctx, queueID, "blocked_no_source", "blocked_no_source")
		return
	}

	// Acquire before spawning. A full lane becomes a durable DB-visible retry
	// state instead of an unbounded pile of goroutines waiting on a semaphore.
	select {
	case localMediaSem <- struct{}{}:
		w.setLocaleStatus(ctx, queueID, "searching", "")
		go func() {
			defer func() { <-localMediaSem }()
			bg, cancel := context.WithTimeout(context.Background(), enrichTimeout)
			defer cancel()
			attempted, enqueued, blocked := w.triggerLocalMediaSearch(bg, entityID)
			status, outcome := "complete", "noop"
			switch {
			case enqueued > 0:
				status, outcome = "evidence_queued", "applied"
			case attempted > 0:
				status, outcome = "no_match", "no_match"
			case blocked > 0:
				status, outcome = "blocked_no_source", "blocked_no_source"
			}
			w.setLocaleStatus(bg, queueID, status, outcome)
		}()
	default:
		w.setLocaleStatus(ctx, queueID, "retrying", "transient")
	}
}

func (w *Worker) retryDeferredLocaleSearches(ctx context.Context, limit int) {
	if limit <= 0 || w.Pool == nil {
		return
	}
	rows, err := w.Pool.Query(ctx, `
SELECT q.id, e.id
  FROM kwave_entity_research_queue q
  JOIN LATERAL (
    SELECT id FROM kwave_entities
     WHERE canonical_ko=q.entity_ko AND status IN ('active','candidate')
     ORDER BY (status='active') DESC, confidence DESC LIMIT 1
  ) e ON true
 WHERE q.status='done' AND q.locale_status='retrying'
 ORDER BY q.finished_at NULLS FIRST
 LIMIT $1`, limit)
	if err != nil {
		return
	}
	defer rows.Close()
	type deferred struct{ queueID, entityID uuid.UUID }
	var jobs []deferred
	for rows.Next() {
		var job deferred
		if rows.Scan(&job.queueID, &job.entityID) == nil {
			jobs = append(jobs, job)
		}
	}
	for _, job := range jobs {
		w.scheduleLocalMediaSearch(ctx, job.queueID, job.entityID)
	}
}

func (w *Worker) setLocaleStatus(ctx context.Context, id uuid.UUID, status, outcome string) {
	_, _ = w.Pool.Exec(ctx, `
UPDATE kwave_entity_research_queue
   SET locale_status=$2,
       last_outcome=CASE WHEN $3<>'' THEN $3 ELSE last_outcome END
 WHERE id=$1`, id, status, outcome)
}

func (w *Worker) finish(ctx context.Context, id uuid.UUID) {
	_, _ = w.Pool.Exec(ctx, `
UPDATE kwave_entity_research_queue
   SET status = 'done', finished_at = now(), last_error = NULL,
       resolution_status = CASE
         WHEN EXISTS(SELECT 1 FROM kwave_entities e
                      WHERE e.canonical_ko=kwave_entity_research_queue.entity_ko AND e.status='active') THEN 'active'
         WHEN EXISTS(SELECT 1 FROM kwave_entities e
                      WHERE e.canonical_ko=kwave_entity_research_queue.entity_ko AND e.status='candidate') THEN 'candidate'
         WHEN EXISTS(SELECT 1 FROM kwave_entities e
                      WHERE e.canonical_ko=kwave_entity_research_queue.entity_ko AND e.status='rejected') THEN 'rejected'
         ELSE 'not_created' END,
       last_outcome = CASE WHEN last_outcome<>'' THEN last_outcome ELSE 'finished' END
 WHERE id = $1`, id)
}

// fail — maxAttempts 초과면 failed, 아니면 pending 으로 되돌려 재시도.
//
// transient(codex timeout / enrich deadline / 네트워크)는 발굴 대상의 잘못이
// 아니므로 attempts 를 1 돌려줘(claim 이 무조건 +1 함) 일시적 외부 장애가
// 정상 이름을 영구 failed 로 만들지 않게 한다.
func (w *Worker) fail(ctx context.Context, id uuid.UUID, attempts int, cause error) {
	msg := cause.Error()
	if r := []rune(msg); len(r) > 500 { // rune 기준 (멀티바이트 절단 방지)
		msg = string(r[:500])
	}
	if isTransientErr(cause) {
		_, _ = w.Pool.Exec(ctx, `
UPDATE kwave_entity_research_queue
   SET status = 'pending', attempts = GREATEST(attempts - 1, 0), last_error = $2,
       last_outcome='transient', next_attempt_at=now()+make_interval(secs=>$3)
 WHERE id = $1`, id, "transient: "+msg, int(transientRetryDelay.Seconds()))
		log.Printf("kdb.research: 발굴 일시실패(재시도, attempts 미차감) id=%s: %v", id, cause)
		return
	}
	status := "pending"
	if attempts >= maxAttempts {
		status = "failed"
	}
	_, _ = w.Pool.Exec(ctx, `
UPDATE kwave_entity_research_queue
   SET status = $2, last_error = $3, last_outcome='permanent',
       next_attempt_at=CASE WHEN $2='pending' THEN now()+interval '1 hour' ELSE NULL END,
       finished_at = CASE WHEN $2 = 'failed' THEN now() ELSE finished_at END
 WHERE id = $1`, id, status, msg)
	log.Printf("kdb.research: 발굴 실패 id=%s attempts=%d status=%s: %v", id, attempts, status, cause)
}

// isTransientErr — 외부 일시 장애(소진 카운트 제외 대상). 우선 errors.Is 로 판정하고,
// 문자열 매칭은 오분류를 줄이기 위해 *구체적인* 마커만 본다. 과거 bare "eof"/"timeout"
// 부분문자열 매칭은 영구 실패 메시지(예: 'unexpected EOF' 파싱 에러)를 transient 로 오인해
// attempts 가 영영 소진되지 않는(무한 재시도) 버그가 있었다.
func isTransientErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	msg := strings.ToLower(err.Error())
	// 구체 마커만 — 일시적 외부 인프라 장애를 강하게 시사하는 구절.
	for _, s := range []string{"codex timeout", "i/o timeout", "connection refused",
		"connection reset", "no such host", "context deadline exceeded", "503", "502", "429"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

func containsLayer(layers []string, want string) bool {
	for _, l := range layers {
		if l == want {
			return true
		}
	}
	return false
}

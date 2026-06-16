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
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb"
	"github.com/rickyjoo73/kdb/internal/kdb/enrich"
)

const (
	defaultBatch  = 5 // tick 당 처리 건수 (검색+enrich 는 무거움)
	maxAttempts   = 3 // 이 횟수 넘으면 failed
	enrichTimeout = 150 * time.Second
	promoteConf   = 0.72 // Wikidata 검증 발굴분 신뢰도
)

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
	w.RunOnce(ctx, defaultBatch)
}

// RunOnce — pending 큐를 최대 max 건 처리.
func (w *Worker) RunOnce(ctx context.Context, max int) {
	w.reapStale(ctx)
	for i := 0; i < max; i++ {
		if ctx.Err() != nil {
			return
		}
		id, koHint, reqType, attempts, ok := w.claim(ctx)
		if !ok {
			return // pending 없음
		}
		if err := w.process(ctx, koHint, reqType); err != nil {
			w.fail(ctx, id, attempts, err)
			continue
		}
		w.finish(ctx, id)
	}
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
   SET status = 'in_progress', picked_at = now(), attempts = attempts + 1
 WHERE id = (
   SELECT id FROM kwave_entity_research_queue
    WHERE status = 'pending'
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
func (w *Worker) process(ctx context.Context, koHint, reqType string) error {
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
		// ON CONFLICT: 같은 (canonical_ko, entity_type, disambig) 조합이 이미 있으면
		// 기존 ID 를 사용 (중복 INSERT 실패 방지 — rejected 포함 모든 기존 행 재사용).
		if err := w.Pool.QueryRow(ctx, `
INSERT INTO kwave_entities (canonical_ko, entity_type, confidence, status, notes)
VALUES ($1, $2::kwave_entity_type, 0.400, 'candidate',
        'KDB candidate — on-demand 검색 발굴 (lookup-miss)')
ON CONFLICT ON CONSTRAINT kwave_entities_homonym_key DO UPDATE SET updated_at = now()
RETURNING id`, koHint, et).Scan(&entityID); err != nil {
			return err
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
	}

	// 5) ★현지 통용 표기 확보 — 위키(공식표기)로 못 채운 빈 현지 locale 에 대해
	//    그 이름으로 현지 매체를 즉시 검색(site-search) → 본문에서 실제 표기 추출.
	//    결과는 raw 적재 → sweeper 추출 → 매체 합의(2곳+)로 확정/갱신(기존 파이프라인).
	//    위키 라벨 ≠ 현지 통용 표기일 수 있으므로, "현지표기는 현지 매체에서" 원칙.
	w.triggerLocalMediaSearch(ctx, entityID)
	return nil
}

// triggerLocalMediaSearch — entity 의 빈 현지 locale 마다 현지 매체 검색을 시동.
// 비동기 관측 파이프라인이므로 즉시 채워지지 않고 다음 sweeper/consensus 에서 확정.
func (w *Worker) triggerLocalMediaSearch(ctx context.Context, id uuid.UUID) {
	if w.Site == nil || os.Getenv("KDB_DISABLE_LOCAL_MEDIA_SEARCH") == "1" {
		return
	}
	var ja, vi, zhHant, es, idn, ptBr string
	if err := w.Pool.QueryRow(ctx, `
SELECT COALESCE(canonical_ja,''), COALESCE(canonical_vi,''), COALESCE(canonical_zh_hant,''),
       COALESCE(canonical_es,''), COALESCE(canonical_id,''), COALESCE(canonical_pt_br,'')
  FROM kwave_entities WHERE id = $1`, id).Scan(&ja, &vi, &zhHant, &es, &idn, &ptBr); err != nil {
		return
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
		_, err := w.Site.SearchAndEnqueue(sctx, kdb.SiteSearchRequest{EntityID: id, Locale: loc.code})
		cancel()
		if err != nil {
			log.Printf("kdb.research: 현지매체 검색 실패 id=%s locale=%s: %v", id, loc.code, err)
		}
	}
}

func (w *Worker) finish(ctx context.Context, id uuid.UUID) {
	_, _ = w.Pool.Exec(ctx, `
UPDATE kwave_entity_research_queue
   SET status = 'done', finished_at = now(), last_error = NULL
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
   SET status = 'pending', attempts = GREATEST(attempts - 1, 0), last_error = $2
 WHERE id = $1`, id, "transient: "+msg)
		log.Printf("kdb.research: 발굴 일시실패(재시도, attempts 미차감) id=%s: %v", id, cause)
		return
	}
	status := "pending"
	if attempts >= maxAttempts {
		status = "failed"
	}
	_, _ = w.Pool.Exec(ctx, `
UPDATE kwave_entity_research_queue
   SET status = $2, last_error = $3, finished_at = CASE WHEN $2 = 'failed' THEN now() ELSE finished_at END
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

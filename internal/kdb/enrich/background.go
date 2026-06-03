package enrich

import (
	"context"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// BackgroundTrigger — API lookup 시 빈 locale 발견하면 호출.
// 1시간 이내 enrich 이력 있으면 skip (중복 방지). 그 외 background goroutine 에서
// 전체 cascade 실행 — 응답 시간에 영향 X.
//
// 동시 호출 안전: SELECT FOR UPDATE 로 last_enriched_at 즉시 갱신해 다른 lookup 이
// 같은 entity 트리거 못 하게.
type BackgroundTrigger struct {
	Pool         *pgxpool.Pool
	Orchestrator *Orchestrator
	StaleAfter   time.Duration // default 1h
	sem          chan struct{} // 동시 background enrich 캡 (cost/rate-limit 보호)
}

// maxConcurrentBGEnrich — 동시에 도는 background enrich cascade 상한. 각 cascade 는
// Wikidata+MusicBrainz+codex 호출(최대 3분)이라, 캡이 없으면 lookup 트래픽 스파이크
// (또는 스크래퍼)가 수백 개 cascade 를 띄워 외부 API rate-limit/Codex 비용을
// 폭증시킨다 (리뷰 H5).
const maxConcurrentBGEnrich = 4

func NewBackgroundTrigger(pool *pgxpool.Pool) *BackgroundTrigger {
	return &BackgroundTrigger{
		Pool:         pool,
		Orchestrator: New(pool),
		StaleAfter:   1 * time.Hour,
		sem:          make(chan struct{}, maxConcurrentBGEnrich),
	}
}

// Trigger — entity id 받으면 stale 검증 후 background goroutine 시작.
// 호출자는 즉시 리턴 — API 응답에 영향 X.
//
// 안전 장치:
//   - SELECT FOR UPDATE 로 last_enriched_at 즉시 갱신 → 동시 lookup 이 같은
//     entity 동시 enrich 트리거 못 함.
//   - background goroutine 은 context.Background() — request ctx cancel 안 받음.
//   - cascade timeout 3분.
func (t *BackgroundTrigger) Trigger(id uuid.UUID) {
	// 동시성 캡: claim 보다 먼저 슬롯을 확보한다. 슬롯이 없으면 claim 도 하지 않고
	// skip → last_enriched_at 이 그대로라 다음 lookup 에서 재시도된다 (claim 을
	// 먼저 했다면 갱신돼 1시간 동안 enrich 가 누락됐을 것).
	select {
	case t.sem <- struct{}{}:
		// 슬롯 확보.
	default:
		log.Printf("kdb.bg-enrich: %s skip — concurrency cap (%d) reached", id, cap(t.sem))
		return
	}

	// 동기 부분만 inline — claim. 실패면 슬롯 반납 후 즉시 return.
	claimCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var claimed bool
	err := t.Pool.QueryRow(claimCtx, `
UPDATE kwave_entities
   SET last_enriched_at = now()
 WHERE id = $1
   AND (last_enriched_at IS NULL OR last_enriched_at < now() - $2::interval)
 RETURNING true`,
		id, t.StaleAfter.String()).Scan(&claimed)
	if err != nil || !claimed {
		<-t.sem  // 슬롯 반납.
		return // 다른 lookup 이 이미 claim 했거나 최근에 enrich 됨.
	}
	go func() {
		defer func() { <-t.sem }() // 슬롯 반납 (cascade 완료/타임아웃 시).
		// best-effort 백그라운드 enrich 의 panic(외부 API 의 예기치 못한 응답 등)이
		// consolidated 바이너리(API+admin+worker)를 통째로 죽이지 않게 격리.
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("kdb.bg-enrich: %s panic recovered: %v", id, rec)
			}
		}()
		bgCtx, bgCancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer bgCancel()
		rep, err := t.Orchestrator.Enrich(bgCtx, id)
		if err != nil {
			log.Printf("kdb.bg-enrich: %s err=%v", id, err)
			return
		}
		if rep != nil {
			log.Printf("kdb.bg-enrich: %s [%v] filled=%d still=%d", id, rep.LayersRun, len(rep.Filled), len(rep.StillEmpty))
		}
	}()
}

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
	Pool        *pgxpool.Pool
	Orchestrator *Orchestrator
	StaleAfter  time.Duration // default 1h
}

func NewBackgroundTrigger(pool *pgxpool.Pool) *BackgroundTrigger {
	return &BackgroundTrigger{
		Pool:        pool,
		Orchestrator: New(pool),
		StaleAfter:   1 * time.Hour,
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
	// 동기 부분만 inline — claim. 실패면 즉시 return.
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
		return // 다른 lookup 이 이미 claim 했거나 최근에 enrich 됨.
	}
	go func() {
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

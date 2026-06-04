package enricher

import (
	"context"
	"log"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb/agents"
)

// debugDrain — KDB_DRAIN_DEBUG=1 이면 drain 이 entity 별 action/elapsed 를 로깅.
var debugDrain = os.Getenv("KDB_DRAIN_DEBUG") == "1"

// DrainConcurrent — 일회성 적체 해소(burst). backlog(빈 fillable gap 보유 active
// entity)를 더 이상 없을 때까지 라운드 반복으로 채운다. 30분 cycle 의 budget(20)
// 제약 없이, one-shot 서브커맨드로 backlog 를 단번에 비우는 용도.
//
// 라운드마다 Select(batch)로 대상을 뽑아 workers 개로 병렬 enrichOne 한다:
//   - L2/L3(MusicBrainz·Wikidata)는 HTTP 라 자유롭게 병렬 → 채움의 대다수(~76%).
//   - L4 codex 는 codexcli 의 codexGate + 파일락으로 전역 직렬화돼, 병렬 worker
//     라도 동시 토큰 refresh 없이 안전(2026-06-03 refresh_token_reused 재발 방지).
//
// 수렴: enrichOne 이 채우면 backlog 에서 빠지고, 못 채우면 per-field attempts++ →
// maxAttempts 도달 시 exhausted 마킹되어 Select 가 제외 → 라운드가 줄며 0 으로 수렴.
// 채운 게 전혀 없는 라운드가 dryLimit 연속이면(=남은 건 전부 source-exhausted/무소스)
// 정지. maxRounds 는 무한루프 안전판.
func (a *Agent) DrainConcurrent(ctx context.Context, pool *pgxpool.Pool, workers int) agents.RunReport {
	if workers < 1 {
		workers = 4
	}
	const (
		batch     = 200 // 라운드당 Select 상한
		maxRounds = 400 // 안전판
	)
	rep := agents.RunReport{Role: a.Role(), StartedAt: time.Now(), SelfCheck: agents.SelfCheck{Pass: true}}
	var totalFilled, totalSkipped, totalErrored int64

	for round := 1; round <= maxRounds; round++ {
		if ctx.Err() != nil {
			log.Printf("kdb.drain-enrich: ctx cancelled at round=%d", round)
			break
		}
		ids, err := a.Select(ctx, pool, batch)
		if err != nil {
			log.Printf("kdb.drain-enrich: select round=%d: %v", round, err)
			break
		}
		if len(ids) == 0 {
			log.Printf("kdb.drain-enrich: backlog 0 — converged (round=%d)", round)
			break
		}

		var filled, skipped, errored int64
		jobs := make(chan uuid.UUID, len(ids))
		for _, id := range ids {
			jobs <- id
		}
		close(jobs)

		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for id := range jobs {
					if ctx.Err() != nil {
						return
					}
					t0 := time.Now()
					res := a.enrichOne(ctx, pool, id)
					if debugDrain {
						log.Printf("kdb.drain-enrich: id=%s action=%s %s (%s)",
							id, res.Action, res.Reason, time.Since(t0).Round(time.Millisecond))
					}
					switch res.Action {
					case agents.ActionFilled:
						atomic.AddInt64(&filled, 1)
					case agents.ActionErrored:
						atomic.AddInt64(&errored, 1)
					default: // noop | skipped
						atomic.AddInt64(&skipped, 1)
					}
				}
			}()
		}
		wg.Wait()

		totalFilled += filled
		totalSkipped += skipped
		totalErrored += errored
		log.Printf("kdb.drain-enrich: round=%d ids=%d filled=%d skipped=%d errored=%d (cum filled=%d)",
			round, len(ids), filled, skipped, errored, totalFilled)

		// 종료는 Select==0(위에서 break)에만 의존한다. 수정된 Select 는 매 라운드
		// 선택된 entity 가 반드시 진전(채워져 gap 소멸 또는 recordAttempt 로 attempts++
		// → maxAttempts 도달 시 exhausted 제외)하므로 backlog 가 단조 감소해 0 으로
		// 수렴한다. maxRounds 는 이론적 무한루프 안전판일 뿐.
	}

	rep.Duration = time.Since(rep.StartedAt)
	rep.Selected = int(totalFilled + totalSkipped + totalErrored)
	rep.Acted = int(totalFilled)
	rep.Skipped = int(totalSkipped)
	rep.Errored = int(totalErrored)
	log.Printf("kdb.drain-enrich: done filled=%d skipped=%d errored=%d (%s)",
		totalFilled, totalSkipped, totalErrored, rep.Duration)
	return rep
}

// Package kdb — ExtractSweeper (Phase 4 raw buffer wire).
//
// Agent SRE #1 권고: PollOnce 는 INSERT 만, Codex 호출은 별도 goroutine.
// bridge down 시 raw items 살아남고, 복구 후 sweep 으로 자동 처리.
//
// 흐름:
//  1. PollOnce → kwave_rss_items_raw INSERT (cheap_status='hit' AND codex_status='pending')
//  2. ExtractSweeper (3분 tick) → pending row 30개 claim → Codex 호출
//  3. ok → codex_status='ok', observations + candidates 저장
//     fail → retry_count++ codex_status='retrying'; 3회 후 'failed' 영구
package kdb

import (
	"context"
	"encoding/json"
	"log"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SweepStats — SweepOnce 1회 결과. #6 in-place 감독용으로 cmd/kdb 가 hermes run row
// 로 기록한다(kdb→hermes import 는 hermes 테스트 경로에서 cycle 이라 호출부에서 기록).
type SweepStats struct {
	Processed int
	Succeeded int
	Failed    int
	StartedAt time.Time
}

// sweepConcurrency — sweeper 가 한 tick 에 동시 처리할 codex 추출 수.
// codexcli 의 전역 codexSem(KDB_CODEX_CONCURRENCY)이 실제 codex 동시성 상한이며,
// 여기선 goroutine 수를 같은 값으로 맞춰 불필요한 대기 고루틴 폭주를 막는다.
func sweepConcurrency() int {
	if v := os.Getenv("KDB_CODEX_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			return n
		}
	}
	return 4
}

// Sweeper — pending raw items 처리.
type Sweeper struct {
	Pool      *pgxpool.Pool
	Extractor LLMExtractor
	Obs       *ObservationStore
	Cand      *CandidateStore
	Audit     *CodexAuditor

	BatchSize  int // 매 tick 처리 개수 (default 30)
	MaxRetries int // codex 호출 재시도 (default 3)
}

// NewSweeper — 기본값.
func NewSweeper(pool *pgxpool.Pool) *Sweeper {
	return &Sweeper{
		Pool:       pool,
		Extractor:  NewCodexExtractor(),
		Obs:        NewObservationStore(pool),
		Cand:       NewCandidateStore(pool),
		Audit:      NewCodexAuditor(pool),
		BatchSize:  30,
		MaxRetries: 3,
	}
}

// SweepOnce — pending row 처리 1회 (3분 tick). SweepStats 반환(#6 in-place 감독).
func (s *Sweeper) SweepOnce(ctx context.Context) SweepStats {
	if BreakerIsOpen() {
		// bridge 차단 중 — 다음 tick 까지 raw 만 누적.
		return SweepStats{}
	}

	rows, err := s.Pool.Query(ctx, `
SELECT id, source_domain, locale, link, title, description,
       COALESCE(cheap_hints::text, '[]'), retry_count
FROM kwave_rss_items_raw
WHERE cheap_status = 'hit'
  AND (codex_status IS NULL OR codex_status IN ('pending','retrying'))
  AND retry_count < $1
ORDER BY fetched_at
LIMIT $2`, s.MaxRetries, s.BatchSize)
	if err != nil {
		log.Printf("kdb.Sweeper: select: %v", err)
		return SweepStats{}
	}
	defer rows.Close()

	type job struct {
		id           int64
		sourceDomain string
		locale       string
		link         string
		title        string
		description  string
		hintIDs      []string
		retryCount   int
	}
	var jobs []job
	for rows.Next() {
		var j job
		var hintsJSON string
		if err := rows.Scan(&j.id, &j.sourceDomain, &j.locale, &j.link,
			&j.title, &j.description, &hintsJSON, &j.retryCount); err != nil {
			continue
		}
		_ = json.Unmarshal([]byte(hintsJSON), &j.hintIDs)
		jobs = append(jobs, j)
	}
	if len(jobs) == 0 {
		return SweepStats{}
	}

	start := time.Now()
	conc := sweepConcurrency()
	log.Printf("kdb.Sweeper: processing %d pending items (concurrent=%d)", len(jobs), conc)
	var (
		mu                           sync.Mutex
		processed, succeeded, failed int
		sem                          = make(chan struct{}, conc)
		wg                           sync.WaitGroup
	)

	for _, j := range jobs {
		// shutdown/deadline 시 새 작업을 더 띄우지 않는다 (autopilot drain 루프와
		// 동일 패턴). 미처리 raw 는 7일 유지되어 다음 tick 이 이어받는다.
		if ctx.Err() != nil {
			break
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(j job) {
			defer wg.Done()
			defer func() { <-sem }()
			if ctx.Err() != nil {
				return
			}
			// hints 재구성 — hint IDs 로부터 entity 정보 조회.
			hints := s.loadHints(ctx, j.hintIDs)

			t0 := time.Now()
			spellings, err := s.Extractor.Extract(ctx, ExtractInput{
				Locale:      j.locale,
				Title:       j.title,
				Description: j.description,
				Hints:       hints,
			})
			dur := int(time.Since(t0).Milliseconds())

			if err != nil {
				mu.Lock()
				processed++
				failed++
				mu.Unlock()
				// retry_count++, 임계 도달 시 영구 failed
				s.markRetry(ctx, j.id, err.Error())
				s.Audit.AuditCall(ctx, 0, j.sourceDomain, j.locale,
					j.title, j.description, len(hints),
					"http_error", nil, dur, err.Error())
				return
			}
			mu.Lock()
			processed++
			succeeded++
			mu.Unlock()

			// observations 저장.
			//
			// ★출처는 **가져온 페이지의 실제 호스트**로 적는다 (2026-09-15 실측).
			//   종전엔 화이트리스트 매체 이름(j.sourceDomain)을 적었다. 검색이 그
			//   도메인 밖 페이지를 주면(최근 30일 3,012건 전부가 그랬다) 유튜브 한
			//   페이지가 브라질 매체 5곳으로 둔갑한다. 매체합의는 "서로 다른 매체가
			//   같은 말을 했다"를 세는 장치인데, 세는 재료가 거짓이면 합의도 거짓이다.
			//   실측: 683묶음 · 관측 2,426건 · 대상 168건, 그중 151건이 원장 칸까지 갔다.
			//
			// ★힌트에 없는 이름은 **버린다**. 우리가 물어본 대상 하나를 채우려고 가져온
			//   페이지에서 추출기가 덤으로 주운 고유명사다. 그걸 후보로 넣는 것이 KDB
			//   오염의 입구였다 — 요청하지도 않은 일반 기사의 낱말이 원장에 들어왔다.
			src := observationSource(j.link, j.sourceDomain)
			for _, sp := range spellings {
				if sp.Confidence < 0.7 {
					continue
				}
				var entityID uuid.UUID
				for _, h := range hints {
					if h.CanonicalKo == sp.KoHint {
						entityID = h.EntityID
						break
					}
				}
				if entityID == uuid.Nil {
					continue
				}
				if err := s.Obs.Save(ctx, entityID, sp, src, j.link); err != nil {
					log.Printf("kdb.Sweeper: obs save err=%v", err)
				}
			}

			// raw status = ok (성공 — 운영자 정공법: ok 미저장이지만 raw 는 7일 유지)
			s.markOK(ctx, j.id)
			s.Audit.AuditCall(ctx, 0, j.sourceDomain, j.locale,
				j.title, j.description, len(hints),
				"ok", spellings, dur, "")
		}(j)
	}
	wg.Wait()

	log.Printf("kdb.Sweeper: done processed=%d succeeded=%d failed=%d",
		processed, succeeded, failed)

	// Consensus + candidates promote (cycle 끝 정공법대로 sweep)
	if _, err := s.Obs.SweepEvaluation(ctx, 35*time.Minute); err != nil {
		log.Printf("kdb.Sweeper: consensus err=%v", err)
	}
	if _, err := s.Cand.SweepPromote(ctx); err != nil {
		log.Printf("kdb.Sweeper: candidates err=%v", err)
	}
	// #6 in-place 감독: 추출 결과를 호출부(cmd/kdb)가 hermes run row 로 기록(여기 도달 = processed>0).
	return SweepStats{Processed: processed, Succeeded: succeeded, Failed: failed, StartedAt: start}
}

// observationSource — 관측의 출처 이름. **가져온 페이지의 실제 호스트**를 쓴다.
// 피드 자신의 항목이면 피드 도메인과 같고, 검색이 밖의 것을 준 경우에만 달라진다 —
// 달라진다는 사실 자체가 기록돼야 나중에 근거를 되짚을 수 있다.
func observationSource(link, fallback string) string {
	if u, err := url.Parse(strings.TrimSpace(link)); err == nil && u.Hostname() != "" {
		return strings.ToLower(strings.TrimPrefix(u.Hostname(), "www."))
	}
	return fallback
}

func (s *Sweeper) loadHints(ctx context.Context, ids []string) []EntityHint {
	if len(ids) == 0 {
		return nil
	}
	uuids := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if u, err := uuid.Parse(id); err == nil {
			uuids = append(uuids, u)
		}
	}
	if len(uuids) == 0 {
		return nil
	}
	rows, err := s.Pool.Query(ctx,
		`SELECT id, canonical_ko FROM kwave_entities WHERE id = ANY($1)`, uuids)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []EntityHint
	for rows.Next() {
		var h EntityHint
		if err := rows.Scan(&h.EntityID, &h.CanonicalKo); err == nil {
			h.Matched = h.CanonicalKo
			out = append(out, h)
		}
	}
	return out
}

func (s *Sweeper) markOK(ctx context.Context, id int64) {
	if _, err := s.Pool.Exec(ctx, `
UPDATE kwave_rss_items_raw
   SET codex_status='ok', last_attempt_at=now()
 WHERE id=$1`, id); err != nil {
		log.Printf("kdb.Sweeper: markOK %d err=%v", id, err)
	}
}

func (s *Sweeper) markRetry(ctx context.Context, id int64, _ string) {
	if _, err := s.Pool.Exec(ctx, `
UPDATE kwave_rss_items_raw
   SET retry_count = retry_count + 1,
       codex_status = CASE
         WHEN retry_count + 1 >= $2 THEN 'failed'
         ELSE 'retrying'
       END,
       last_attempt_at = now()
 WHERE id = $1`, id, s.MaxRetries); err != nil {
		log.Printf("kdb.Sweeper: markRetry %d err=%v", id, err)
	}
}

// SweeperTick — supervisor fast tick 호출. mutex 로 동시 호출 차단.
var (
	sweeperMu      sync.Mutex
	sweeperRunning bool
)

func SweeperTick(ctx context.Context, pool *pgxpool.Pool) SweepStats {
	sweeperMu.Lock()
	if sweeperRunning {
		sweeperMu.Unlock()
		return SweepStats{}
	}
	sweeperRunning = true
	sweeperMu.Unlock()
	defer func() {
		sweeperMu.Lock()
		sweeperRunning = false
		sweeperMu.Unlock()
	}()
	return NewSweeper(pool).SweepOnce(ctx)
}

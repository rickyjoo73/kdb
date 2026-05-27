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
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

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

// SweepOnce — pending row 처리 1회 (3분 tick).
func (s *Sweeper) SweepOnce(ctx context.Context) {
	if BreakerIsOpen() {
		// bridge 차단 중 — 다음 tick 까지 raw 만 누적.
		return
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
		return
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
		return
	}

	log.Printf("kdb.Sweeper: processing %d pending items", len(jobs))
	var processed, succeeded, failed int

	for _, j := range jobs {
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
		processed++

		if err != nil {
			failed++
			// retry_count++, 임계 도달 시 영구 failed
			s.markRetry(ctx, j.id, err.Error())
			s.Audit.AuditCall(ctx, 0, j.sourceDomain, j.locale,
				j.title, j.description, len(hints),
				"http_error", nil, dur, err.Error())
			continue
		}
		succeeded++

		// observations + candidates 저장
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
				_ = s.Cand.Observe(ctx, sp.KoHint, sp.Locale, sp.Spelling, j.sourceDomain)
				continue
			}
			if err := s.Obs.Save(ctx, entityID, sp, j.sourceDomain, j.link); err != nil {
				log.Printf("kdb.Sweeper: obs save err=%v", err)
			}
		}

		// raw status = ok (성공 — 운영자 정공법: ok 미저장이지만 raw 는 7일 유지)
		s.markOK(ctx, j.id)
		s.Audit.AuditCall(ctx, 0, j.sourceDomain, j.locale,
			j.title, j.description, len(hints),
			"ok", spellings, dur, "")
	}

	log.Printf("kdb.Sweeper: done processed=%d succeeded=%d failed=%d",
		processed, succeeded, failed)

	// Consensus + candidates promote (cycle 끝 정공법대로 sweep)
	if _, err := s.Obs.SweepEvaluation(ctx, 35*time.Minute); err != nil {
		log.Printf("kdb.Sweeper: consensus err=%v", err)
	}
	if _, err := s.Cand.SweepPromote(ctx); err != nil {
		log.Printf("kdb.Sweeper: candidates err=%v", err)
	}
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

func SweeperTick(ctx context.Context, pool *pgxpool.Pool) {
	sweeperMu.Lock()
	if sweeperRunning {
		sweeperMu.Unlock()
		return
	}
	sweeperRunning = true
	sweeperMu.Unlock()
	defer func() {
		sweeperMu.Lock()
		sweeperRunning = false
		sweeperMu.Unlock()
	}()
	NewSweeper(pool).SweepOnce(ctx)
}

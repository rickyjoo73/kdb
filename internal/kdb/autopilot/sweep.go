// Package autopilot — KDB 자율 운영 layer.
//
// 운영자 개입 없이 주기적으로:
//  1. 미분류 active entity → gpt classify (≥ 0.9 자동 적용 + persons sync).
//  2. candidate ≥ 2 매체 → 자동 promote + enrich (entity_type 도 gpt 가 결정).
//  3. status='active' 인데 빈 locale 다수 → enrich cascade batch.
//  4. confidence < 0.7 → gpt 자동 검수 (TODO Phase 다음).
//
// 각 step batch size 환경변수로 제한 — LLM cost 제어. high-conf 만 자동,
// 그 외 운영자 큐 그대로.
package autopilot

import (
	"context"
	"errors"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	kdbroot "github.com/rickyjoo73/kdb/internal/kdb"
	"github.com/rickyjoo73/kdb/internal/kdb/aijudge"
	"github.com/rickyjoo73/kdb/internal/kdb/enrich"
	"github.com/rickyjoo73/kdb/internal/kdb/hangul"
	"github.com/rickyjoo73/kdb/internal/kdb/homonym"
)

// Config — env 로 조정 가능한 batch 크기 / threshold.
//
// MinConfAuto 는 매체 수 별로 차등 적용:
//   - 단일 매체  : MinConfSingle (default 0.85) — 매체 합의 없으므로 엄격.
//   - ≥ 2 매체   : MinConfTwo    (default 0.75) — 매체 합의 자체가 부가 신호.
//   - ≥ 3 매체   : MinConfThree  (default 0.70) — 강한 합의.
type Config struct {
	BatchClassify int
	BatchEnrich   int
	BatchPromote  int
	MinConfSingle float64
	MinConfTwo    float64
	MinConfThree  float64
}

func DefaultConfig() Config {
	return Config{
		BatchClassify: envInt("KDB_AUTOPILOT_BATCH_CLASSIFY", 20),
		BatchEnrich:   envInt("KDB_AUTOPILOT_BATCH_ENRICH", 10),
		BatchPromote:  envInt("KDB_AUTOPILOT_BATCH_PROMOTE", 20),
		MinConfSingle: envFloat("KDB_AUTOPILOT_MIN_CONF_SINGLE", 0.85),
		MinConfTwo:    envFloat("KDB_AUTOPILOT_MIN_CONF_TWO", 0.75),
		MinConfThree:  envFloat("KDB_AUTOPILOT_MIN_CONF_THREE", 0.70),
	}
}

// minConfFor — 매체 수 별 임계.
func (c Config) minConfFor(mediaCount int) float64 {
	if mediaCount >= 3 {
		return c.MinConfThree
	}
	if mediaCount >= 2 {
		return c.MinConfTwo
	}
	return c.MinConfSingle
}

// Sweeper — 자율 운영 1 cycle.
type Sweeper struct {
	Pool   *pgxpool.Pool
	Judge  *aijudge.Client
	Orch   *enrich.Orchestrator
	Config Config

	// running — single-flight 가드. cmd 가 30분 ticker 로 go runAutopilot() 을
	// 띄우는데, cycle 이 759 적체 처리로 30분을 넘기면 다음 ticker 가 중복 cycle
	// 을 겹쳐 실행한다 (codex 비용 2배 + 같은 row 경합). New(pool) 는 1회 생성
	// 후 매 tick 재사용되므로 이 필드가 cycle 간 single-flight 를 보장한다.
	running atomic.Bool
}

func New(pool *pgxpool.Pool) *Sweeper {
	return &Sweeper{
		Pool:   pool,
		Judge:  aijudge.New(),
		Orch:   enrich.New(pool),
		Config: DefaultConfig(),
	}
}

// Report — 1 cycle 요약.
type Report struct {
	Classified       int
	ClassifyDeferred int
	NonEntityReject  int // gpt 가 일반어 (term + conf 낮음) 으로 판단 → 자동 reject
	Promoted         int
	Enriched         int
	JamoMerged       int // 자소 깨진 row 정상 짝으로 merge
	JamoRejected     int // 정상 짝 없는 자소 깨진 row reject
	PersonsAdded     int // persons 에 추가된 row
	EntityTypeFixed  int // entities entity_type=person 으로 갱신된 row
	QualityFixed     int // confidence < 0.7 → Wikidata 자동 채택 + conf 갱신
	AliasResolved    int // alias 다중 매핑 → 자동 재할당
	StartedAt        time.Time
	Duration         time.Duration
}

// Run — 1 cycle 실행. 각 step error 는 log 만 (다음 step 영향 X).
//
// 실행 순서: 자소 깨짐 정리 → 미분류 분류 → candidate promote → 빈 locale enrich.
// 정리가 먼저 — 깨진 row 가 분류 / enrich 단계로 흘러가는 것 방지.
func (s *Sweeper) Run(ctx context.Context) Report {
	// single-flight: 직전 cycle 이 아직 돌고 있으면 이번 tick 은 skip (중복 방지).
	if !s.running.CompareAndSwap(false, true) {
		log.Printf("kdb.autopilot: 직전 cycle 진행 중 — 이번 tick skip")
		return Report{StartedAt: time.Now()}
	}
	defer s.running.Store(false)

	rep := Report{StartedAt: time.Now()}
	s.stepRepairBrokenJamo(ctx, &rep)
	s.stepSyncPersons(ctx, &rep)
	s.stepReviewCandidates(ctx, &rep) // candidate 1매체 — gpt 검수 / 일반어 자동 reject
	s.stepClassifyUnknown(ctx, &rep)
	s.stepResolveUnknowns(ctx, &rep) // 남은 unknown — 모르면 검색 후 확정 (unknown 박멸)
	s.stepPromoteConsensus(ctx, &rep)
	s.stepEnrichEmpty(ctx, &rep)
	s.stepQualityReview(ctx, &rep)
	s.stepResolveAliasConflicts(ctx, &rep)
	rep.Duration = time.Since(rep.StartedAt)
	s.persistLog(ctx, &rep)
	log.Printf("kdb.autopilot: done jamo=%d/%d persons=+%d type→person=%d term-reject=%d classified=%d/%d promoted=%d enriched=%d quality=%d alias=%d (%s)",
		rep.JamoMerged, rep.JamoRejected,
		rep.PersonsAdded, rep.EntityTypeFixed,
		rep.NonEntityReject,
		rep.Classified, rep.ClassifyDeferred,
		rep.Promoted, rep.Enriched,
		rep.QualityFixed, rep.AliasResolved,
		rep.Duration)
	return rep
}

// DrainCandidatesConcurrent — 적체된 status='candidate' 전체를 1 pass 로 gpt
// 분류해 person(→ 인물DB) / 비-person 고유명사(group/drama/movie/song_album/
// show/agency/brand_place 등) / term(→ reject) 로 깔끔히 구분 정리한다.
//
// 운영자 요청 (2026-05-29): "고유명사DB 와 인물DB 를 에이전트를 시켜서라도 모두
// 깔끔히 구분지어놔". autopilot 의 점진 batch(20/cycle)는 적체 해소에 ~10h 걸려,
// 이 일괄 drain 으로 즉시 전부 처리한다. workers 개 codex 동시 호출.
//
// enrich 는 하지 않는다 (분류만) — 9개 언어 채움은 이후 autopilot stepEnrichEmpty
// 가 담당. gpt 가 확신 못 하는 애매한 후보는 candidate 로 남겨 운영자 inbox 로.
func (s *Sweeper) DrainCandidatesConcurrent(ctx context.Context, workers int) Report {
	rep := Report{StartedAt: time.Now()}
	if workers < 1 {
		workers = 4
	}
	rows, err := s.Pool.Query(ctx, `
SELECT id, canonical_ko, source_domains
FROM kwave_entities
WHERE status='candidate' AND operator_locked = false
ORDER BY updated_at ASC`)
	if err != nil {
		log.Printf("kdb.drain: select: %v", err)
		return rep
	}
	type cand struct {
		ID uuid.UUID
		Ko string
		SD []string
	}
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.ID, &c.Ko, &c.SD); err == nil {
			cands = append(cands, c)
		}
	}
	rows.Close()
	log.Printf("kdb.drain: %d candidates, %d workers", len(cands), workers)

	jobs := make(chan cand, len(cands))
	for _, c := range cands {
		jobs <- c
	}
	close(jobs)

	var mu sync.Mutex
	var done int32
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range jobs {
				if ctx.Err() != nil {
					return
				}
				s.drainOne(ctx, c.ID, c.Ko, c.SD, &rep, &mu)
				if n := atomic.AddInt32(&done, 1); n%25 == 0 {
					log.Printf("kdb.drain: progress %d/%d", n, len(cands))
				}
			}
		}()
	}
	wg.Wait()
	rep.Duration = time.Since(rep.StartedAt)
	log.Printf("kdb.drain: done total=%d promoted=%d persons+=%d reject=%d deferred=%d (%s)",
		len(cands), rep.Promoted, rep.PersonsAdded, rep.NonEntityReject, rep.ClassifyDeferred, rep.Duration)
	return rep
}

// drainOne — 한 후보 gpt 분류 + 적용 (stepReviewCandidates 의 결정 로직과 동일).
// rep 카운터는 mu 로 보호.
func (s *Sweeper) drainOne(ctx context.Context, id uuid.UUID, ko string, sd []string, rep *Report, mu *sync.Mutex) {
	if !hangul.IsCleanKorean(ko) {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	res, err := s.Judge.Classify(cctx, &aijudge.ClassifyInput{Ko: ko, SourceDomains: sd})
	cancel()
	if err != nil || res == nil {
		return
	}

	// 일반어 → reject (인물DB / 고유명사DB 어느 쪽도 아님).
	if res.EntityType == "term" && res.Confidence <= 0.40 {
		_, _ = s.Pool.Exec(ctx, `
UPDATE kwave_entities
   SET status='rejected', confidence = 0.000,
       notes = COALESCE(NULLIF(notes,'') || ' · ','') || 'drain: gpt 일반어 — ' || $2,
       updated_at = now()
 WHERE id = $1 AND status='candidate'`, id, res.Reason)
		mu.Lock()
		rep.NonEntityReject++
		mu.Unlock()
		return
	}

	// 확신하는 실제 entity → active 승격 + type 확정. person 이면 인물DB sync.
	realType := res.EntityType != "" && res.EntityType != "unknown" && res.EntityType != "term"
	if realType && !res.NeedsSearch && res.Confidence >= s.Config.minConfFor(len(sd)) {
		conf := 0.70
		if len(sd) >= 2 {
			conf = 0.75
		}
		if _, err := s.Pool.Exec(ctx, `
UPDATE kwave_entities
   SET status='active', entity_type = $2::kwave_entity_type,
       confidence = GREATEST(confidence, $3::numeric), updated_at = now()
 WHERE id = $1 AND status='candidate'`, id, res.EntityType, conf); err != nil {
			return
		}
		if res.EntityType == "person" {
			_, _ = s.Pool.Exec(ctx, `
INSERT INTO kwave_persons (name_ko, primary_role, confidence, last_verified_at, created_at)
VALUES ($1, COALESCE(NULLIF($2,'')::person_role, 'other'::person_role), 0.500, now(), now())
ON CONFLICT (name_ko) DO NOTHING`, ko, derefStr(res.PrimaryRole))
			_, _ = s.Pool.Exec(ctx, `
INSERT INTO kwave_entity_person_details (entity_id, primary_role)
VALUES ($1, COALESCE(NULLIF($2,'')::person_role, 'other'::person_role))
ON CONFLICT (entity_id) DO NOTHING`, id, derefStr(res.PrimaryRole))
			s.persistPersonSignals(ctx, id, res)
			s.markHomonymsIfConflict(ctx, id, ko, res)
			mu.Lock()
			rep.PersonsAdded++
			mu.Unlock()
		}
		mu.Lock()
		rep.Promoted++
		mu.Unlock()
		return
	}

	// 애매 → candidate 유지 (운영자 inbox). touch 로 큐 회전.
	_, _ = s.Pool.Exec(ctx, `
UPDATE kwave_entities SET updated_at = now()
 WHERE id = $1 AND status='candidate'`, id)
	mu.Lock()
	rep.ClassifyDeferred++
	mu.Unlock()
}

// DrainPersonsConcurrent — 고유명사DB 에 섞여 있는 인명을 인물DB 로 이동.
//
// 운영자 요청 (2026-05-31): "고유명사에 이름이 아직 많다. 이름만 빼서 인물DB로
// 이동 못하나?". 적체 candidate 의 다수가 단발 멘션 인명(entity_type='unknown')
// 인데, 일반 drain 은 '누구인지' 확신(!NeedsSearch + 높은 conf)을 요구해 이들을
// 영구 defer 한다. 여기서는 판단 기준을 '인물인가'로 낮춰, gpt 가 person 으로
// 분류하면(중간 신뢰도라도) entity_type='person' 으로 승격하고 인물DB(kwave_persons)
// 로 sync 한다. person 이 아닌 후보는 건드리지 않는다(일반 drain/autopilot 담당).
func (s *Sweeper) DrainPersonsConcurrent(ctx context.Context, workers int) Report {
	rep := Report{StartedAt: time.Now()}
	if workers < 1 {
		workers = 4
	}
	rows, err := s.Pool.Query(ctx, `
SELECT id, canonical_ko, source_domains
FROM kwave_entities
WHERE status='candidate' AND entity_type='unknown' AND operator_locked = false
ORDER BY updated_at ASC`)
	if err != nil {
		log.Printf("kdb.drain-persons: select: %v", err)
		return rep
	}
	type cand struct {
		ID uuid.UUID
		Ko string
		SD []string
	}
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.ID, &c.Ko, &c.SD); err == nil {
			cands = append(cands, c)
		}
	}
	rows.Close()
	log.Printf("kdb.drain-persons: %d unknown candidates, %d workers", len(cands), workers)

	jobs := make(chan cand, len(cands))
	for _, c := range cands {
		jobs <- c
	}
	close(jobs)

	var mu sync.Mutex
	var done int32
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range jobs {
				if ctx.Err() != nil {
					return
				}
				s.drainPersonOne(ctx, c.ID, c.Ko, c.SD, &rep, &mu)
				if n := atomic.AddInt32(&done, 1); n%25 == 0 {
					log.Printf("kdb.drain-persons: progress %d/%d", n, len(cands))
				}
			}
		}()
	}
	wg.Wait()
	rep.Duration = time.Since(rep.StartedAt)
	log.Printf("kdb.drain-persons: done total=%d moved_to_persons=%d not_person=%d (%s)",
		len(cands), rep.PersonsAdded, rep.ClassifyDeferred, rep.Duration)
	return rep
}

// drainPersonMinConf — gpt 가 person 으로 분류했을 때 인물DB 이동을 허용하는 최소
// 신뢰도. '누가인지'가 아니라 '인물인가' 판단이므로 일반 승격(0.70+)보다 낮다.
const drainPersonMinConf = 0.50

// drainPersonOne — 한 후보를 gpt 분류해 person 이면 인물DB 로 이동. 그 외엔 무액션.
func (s *Sweeper) drainPersonOne(ctx context.Context, id uuid.UUID, ko string, sd []string, rep *Report, mu *sync.Mutex) {
	if !hangul.IsCleanKorean(ko) {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	res, err := s.Judge.Classify(cctx, &aijudge.ClassifyInput{Ko: ko, SourceDomains: sd})
	cancel()
	if err != nil || res == nil {
		return
	}
	// 인물이 아니면 건드리지 않는다 (일반 drain/autopilot 이 처리).
	if res.EntityType != "person" || res.Confidence < drainPersonMinConf {
		mu.Lock()
		rep.ClassifyDeferred++
		mu.Unlock()
		return
	}
	// person 승격 (status active, entity_type person) + 인물DB sync.
	if _, err := s.Pool.Exec(ctx, `
UPDATE kwave_entities
   SET status='active', entity_type='person'::kwave_entity_type,
       confidence = GREATEST(confidence, 0.500::numeric),
       notes = COALESCE(NULLIF(notes,'') || ' · ','') || 'drain-persons: gpt 인물 분류 — ' || $2,
       updated_at = now()
 WHERE id = $1 AND status='candidate'`, id, res.Reason); err != nil {
		return
	}
	_, _ = s.Pool.Exec(ctx, `
INSERT INTO kwave_persons (name_ko, primary_role, confidence, last_verified_at, created_at)
VALUES ($1, COALESCE(NULLIF($2,'')::person_role, 'other'::person_role), 0.500, now(), now())
ON CONFLICT (name_ko) DO NOTHING`, ko, derefStr(res.PrimaryRole))
	_, _ = s.Pool.Exec(ctx, `
INSERT INTO kwave_entity_person_details (entity_id, primary_role)
VALUES ($1, COALESCE(NULLIF($2,'')::person_role, 'other'::person_role))
ON CONFLICT (entity_id) DO NOTHING`, id, derefStr(res.PrimaryRole))
	s.persistPersonSignals(ctx, id, res)
	s.markHomonymsIfConflict(ctx, id, ko, res)
	mu.Lock()
	rep.PersonsAdded++
	rep.Promoted++
	mu.Unlock()
}

// DrainBucketConcurrent — 남은 unknown candidate 를 제 타입(고유명사) / 인물DB /
// reject 로 일괄 분리. drain-persons 다음 단계: 인물이 아니라 보류된 곡·드라마·쇼·
// 영화 제목 등을 실제 type 으로 버킷팅한다.
//
// 운영자 요청 (2026-05-31): "남은 55건도 분리하자". 일반 drain 의 두 한계를 푼다:
//  1. IsCleanKorean 스킵 — 영어 제목(Seven, ON, Dirty Work…)이 영구 unknown 으로
//     남던 것을 분류 대상에 포함.
//  2. !NeedsSearch + 높은 conf 게이트 — 단발 멘션이라 영구 defer 되던 것을, 기준을
//     '실체 type 인가'로 낮춰(conf≥0.50) gpt type 을 부여한다.
// gpt 가 일반어(term)/비실체로 보면 reject, 여전히 애매하면(unknown/저conf) candidate
// 유지(운영자 inbox).
func (s *Sweeper) DrainBucketConcurrent(ctx context.Context, workers int) Report {
	rep := Report{StartedAt: time.Now()}
	if workers < 1 {
		workers = 4
	}
	rows, err := s.Pool.Query(ctx, `
SELECT id, canonical_ko, source_domains
FROM kwave_entities
WHERE status='candidate' AND entity_type='unknown' AND operator_locked = false
ORDER BY updated_at ASC`)
	if err != nil {
		log.Printf("kdb.drain-bucket: select: %v", err)
		return rep
	}
	type cand struct {
		ID uuid.UUID
		Ko string
		SD []string
	}
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.ID, &c.Ko, &c.SD); err == nil {
			cands = append(cands, c)
		}
	}
	rows.Close()
	log.Printf("kdb.drain-bucket: %d unknown candidates, %d workers", len(cands), workers)

	jobs := make(chan cand, len(cands))
	for _, c := range cands {
		jobs <- c
	}
	close(jobs)

	var mu sync.Mutex
	var done int32
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range jobs {
				if ctx.Err() != nil {
					return
				}
				s.drainBucketOne(ctx, c.ID, c.Ko, c.SD, &rep, &mu)
				if n := atomic.AddInt32(&done, 1); n%25 == 0 {
					log.Printf("kdb.drain-bucket: progress %d/%d", n, len(cands))
				}
			}
		}()
	}
	wg.Wait()
	rep.Duration = time.Since(rep.StartedAt)
	log.Printf("kdb.drain-bucket: done total=%d typed=%d persons+=%d reject=%d deferred=%d (%s)",
		len(cands), rep.Promoted, rep.PersonsAdded, rep.NonEntityReject, rep.ClassifyDeferred, rep.Duration)
	return rep
}

// drainBucketOne — gpt 분류해 실체 type 이면 그 type 으로 active 승격(person 은
// 인물DB sync), 일반어면 reject, 애매하면 candidate 유지. 일반 drainOne 과 달리
// IsCleanKorean 스킵·NeedsSearch 게이트가 없다(버킷팅이 목적).
func (s *Sweeper) drainBucketOne(ctx context.Context, id uuid.UUID, ko string, sd []string, rep *Report, mu *sync.Mutex) {
	cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	res, err := s.Judge.Classify(cctx, &aijudge.ClassifyInput{Ko: ko, SourceDomains: sd})
	cancel()
	if err != nil || res == nil {
		return
	}

	// 일반어 → reject.
	if res.EntityType == "term" {
		_, _ = s.Pool.Exec(ctx, `
UPDATE kwave_entities
   SET status='rejected', confidence = 0.000,
       notes = COALESCE(NULLIF(notes,'') || ' · ','') || 'drain-bucket: gpt 일반어 — ' || $2,
       updated_at = now()
 WHERE id = $1 AND status='candidate'`, id, res.Reason)
		mu.Lock()
		rep.NonEntityReject++
		mu.Unlock()
		return
	}

	// 실체 type (person 포함) & conf≥0.50 → 해당 type 으로 active 승격.
	realType := res.EntityType != "" && res.EntityType != "unknown"
	if realType && res.Confidence >= drainPersonMinConf {
		if _, err := s.Pool.Exec(ctx, `
UPDATE kwave_entities
   SET status='active', entity_type = $2::kwave_entity_type,
       confidence = GREATEST(confidence, 0.500::numeric),
       notes = COALESCE(NULLIF(notes,'') || ' · ','') || 'drain-bucket: gpt ' || $2 || ' — ' || $3,
       updated_at = now()
 WHERE id = $1 AND status='candidate'`, id, res.EntityType, res.Reason); err != nil {
			return
		}
		if res.EntityType == "person" {
			_, _ = s.Pool.Exec(ctx, `
INSERT INTO kwave_persons (name_ko, primary_role, confidence, last_verified_at, created_at)
VALUES ($1, COALESCE(NULLIF($2,'')::person_role, 'other'::person_role), 0.500, now(), now())
ON CONFLICT (name_ko) DO NOTHING`, ko, derefStr(res.PrimaryRole))
			_, _ = s.Pool.Exec(ctx, `
INSERT INTO kwave_entity_person_details (entity_id, primary_role)
VALUES ($1, COALESCE(NULLIF($2,'')::person_role, 'other'::person_role))
ON CONFLICT (entity_id) DO NOTHING`, id, derefStr(res.PrimaryRole))
			s.persistPersonSignals(ctx, id, res)
			s.markHomonymsIfConflict(ctx, id, ko, res)
			mu.Lock()
			rep.PersonsAdded++
			mu.Unlock()
		}
		mu.Lock()
		rep.Promoted++
		mu.Unlock()
		return
	}

	// 여전히 애매 → candidate 유지 (운영자 inbox). touch 로 큐 회전.
	_, _ = s.Pool.Exec(ctx, `
UPDATE kwave_entities SET updated_at = now()
 WHERE id = $1 AND status='candidate'`, id)
	mu.Lock()
	rep.ClassifyDeferred++
	mu.Unlock()
}

// batchResolveUnknowns — 정시 step 이 cycle 당 처리하는 unknown 최대 개수.
// 항목당 gpt 1~2회 + 검색 1회라 비용 관리를 위해 작게 둔다(신규 unknown 은 드물게
// 유입되므로 충분히 따라잡는다).
const batchResolveUnknowns = 12

// stepResolveUnknowns — autopilot cycle 의 상시 step. stepClassifyUnknown 이
// needs_search 로 보류한 것 + 남은 unknown 을 "모르면 검색" 루프로 cycle 당
// batchResolveUnknowns 개씩 확정한다(보수 모드: 문맥 없으면 버리지 않고 재시도).
// 각 항목이 terminal(active/term-reject/defer-rotate)이라 공회전 없음.
func (s *Sweeper) stepResolveUnknowns(ctx context.Context, rep *Report) {
	rows, err := s.Pool.Query(ctx, `
SELECT id, canonical_ko, source_domains FROM kwave_entities
 WHERE entity_type='unknown' AND operator_locked = false
 ORDER BY status, updated_at ASC LIMIT $1`, batchResolveUnknowns)
	if err != nil {
		log.Printf("kdb.autopilot: resolve-unknowns select: %v", err)
		return
	}
	type item struct {
		ID uuid.UUID
		Ko string
		SD []string
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.ID, &it.Ko, &it.SD); err == nil {
			items = append(items, it)
		}
	}
	rows.Close()
	if len(items) == 0 {
		return
	}
	var mu sync.Mutex
	var searched, deleted int32
	for _, it := range items {
		if ctx.Err() != nil {
			return
		}
		s.resolveUnknownOne(ctx, it.ID, it.Ko, it.SD, rep, &mu, &searched, &deleted, false)
	}
	if searched > 0 || deleted > 0 {
		log.Printf("kdb.autopilot: resolve-unknowns batch=%d searched=%d deleted=%d", len(items), searched, deleted)
	}
}

// ResolveUnknownsConcurrent — entity_type='unknown' 을 0 으로 만든다("저품질 unknown
// 박멸"). 운영자 방침: unknown 을 가지고 있다는 건 분류가 안 됐다는 것 → 남겨둘 수 없다.
//
// 처리(운영자 합의 "모르면 검색"):
//  1. 로컬 RSS(title/description)에서 이름 문맥 수집 후 gpt 분류.
//  2. 확신 못 하면(needs_search/unknown/저conf) → Google News 일반검색으로 실제
//     기사 제목을 문맥(SearchHits)으로 넣어 재분류.
//  3. 그래도 실체 type(conf≥0.50)이면 그 type 으로 active 승격(person 은 인물DB
//     sync, 잘못 rejected 된 진짜 고유명사 복구). 아니면 term + rejected 로 확정
//     ("버릴것은 버린다"). 어느 쪽이든 entity_type 은 더 이상 unknown 이 아니다.
//
// candidate·rejected 모두 대상. operator_locked 는 제외(운영자 손댄 것 보호).
func (s *Sweeper) ResolveUnknownsConcurrent(ctx context.Context, workers int) Report {
	rep := Report{StartedAt: time.Now()}
	if workers < 1 {
		workers = 4
	}
	rows, err := s.Pool.Query(ctx, `
SELECT id, canonical_ko, source_domains
FROM kwave_entities
WHERE entity_type='unknown' AND operator_locked = false
ORDER BY status, updated_at ASC`)
	if err != nil {
		log.Printf("kdb.resolve-unknowns: select: %v", err)
		return rep
	}
	type cand struct {
		ID uuid.UUID
		Ko string
		SD []string
	}
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.ID, &c.Ko, &c.SD); err == nil {
			cands = append(cands, c)
		}
	}
	rows.Close()
	log.Printf("kdb.resolve-unknowns: %d unknown entities, %d workers", len(cands), workers)

	jobs := make(chan cand, len(cands))
	for _, c := range cands {
		jobs <- c
	}
	close(jobs)

	var mu sync.Mutex
	var done, searched, deleted int32
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range jobs {
				if ctx.Err() != nil {
					return
				}
				s.resolveUnknownOne(ctx, c.ID, c.Ko, c.SD, &rep, &mu, &searched, &deleted, true)
				if n := atomic.AddInt32(&done, 1); n%20 == 0 {
					log.Printf("kdb.resolve-unknowns: progress %d/%d", n, len(cands))
				}
			}
		}()
	}
	wg.Wait()
	rep.Duration = time.Since(rep.StartedAt)
	log.Printf("kdb.resolve-unknowns: done total=%d typed=%d persons+=%d searched=%d reject=%d deleted=%d (%s)",
		len(cands), rep.Promoted, rep.PersonsAdded, searched, rep.NonEntityReject, deleted, rep.Duration)
	return rep
}

// resolveUnknownOne — 한 unknown 을 (필요시 검색하여) 분류 후 active 승격 / term
// reject 로 확정.
//
// aggressive=true (일회성 일괄 정리): 끝내 실체 아니면 무조건 term+reject → unknown 0 보장.
// aggressive=false (정시 step): gpt 가 문맥(로컬/검색)을 보고도 실체로 못 만든 경우만
// reject. 문맥이 전혀 안 잡히면(검색 실패 등) reject 대신 큐 회전(touch)으로 다음에
// 재시도 — 일시적 검색 장애로 진짜 entity 를 잘못 버리는 실수 방지.
func (s *Sweeper) resolveUnknownOne(ctx context.Context, id uuid.UUID, ko string, sd []string, rep *Report, mu *sync.Mutex, searched, deleted *int32, aggressive bool) {
	// pass 1: 로컬 RSS 문맥 + 분류.
	local := s.localNewsContext(ctx, ko, 6)
	if res := s.classifyWith(ctx, ko, sd, local); res != nil && s.tryApplyRealType(ctx, id, ko, res, rep, mu, deleted) {
		return
	}

	// pass 2: "모르면 검색" — Google News 일반검색 문맥으로 재분류.
	web := kdbroot.SearchNewsContext(ctx, ko, 6)
	if len(web) > 0 {
		atomic.AddInt32(searched, 1)
		hits := append(append([]string{}, local...), web...)
		if res2 := s.classifyWith(ctx, ko, sd, hits); res2 != nil &&
			s.tryApplyRealType(ctx, id, ko, res2, rep, mu, deleted) {
			return
		}
	}

	hadContext := len(local) > 0 || len(web) > 0
	if aggressive || hadContext {
		// 실체 아님 확정(문맥 보고도 못 만듦) → term + rejected ("버린다").
		s.rejectAsTerm(ctx, id, deleted)
		mu.Lock()
		rep.NonEntityReject++
		mu.Unlock()
		return
	}
	// 문맥 전혀 없음(검색 실패 가능) → 버리지 말고 큐 회전 후 다음 cycle 재시도.
	_, _ = s.Pool.Exec(ctx, `UPDATE kwave_entities SET updated_at = now() WHERE id = $1`, id)
	mu.Lock()
	rep.ClassifyDeferred++
	mu.Unlock()
}

func (s *Sweeper) classifyWith(ctx context.Context, ko string, sd, hits []string) *aijudge.ClassifyResult {
	cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	res, err := s.Judge.Classify(cctx, &aijudge.ClassifyInput{Ko: ko, SourceDomains: sd, SearchHits: hits})
	if err != nil {
		return nil
	}
	return res
}

// localNewsContext — kwave_rss_items_raw 에서 이름이 언급된 기사 제목을 모은다.
func (s *Sweeper) localNewsContext(ctx context.Context, ko string, max int) []string {
	rows, err := s.Pool.Query(ctx, `
SELECT title FROM kwave_rss_items_raw
 WHERE (title ILIKE '%'||$1||'%' OR description ILIKE '%'||$1||'%') AND COALESCE(title,'') <> ''
 ORDER BY pub_date DESC NULLS LAST LIMIT $2`, ko, max)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if rows.Scan(&t) == nil && strings.TrimSpace(t) != "" {
			out = append(out, t)
		}
	}
	return out
}

// tryApplyRealType — res 가 실체 type(conf≥0.50)이면 그 type 으로 active 승격
// (person 은 인물DB sync). 적용했으면 true. unique 충돌(이미 같은 entity 존재)은
// 중복이므로 삭제하고 true(처리됨). 실체 아님/저conf 면 false.
func (s *Sweeper) tryApplyRealType(ctx context.Context, id uuid.UUID, ko string, res *aijudge.ClassifyResult, rep *Report, mu *sync.Mutex, deleted *int32) bool {
	realType := res.EntityType != "" && res.EntityType != "unknown" && res.EntityType != "term"
	if !realType || res.Confidence < drainPersonMinConf {
		return false
	}
	tag, err := s.Pool.Exec(ctx, `
UPDATE kwave_entities
   SET status='active', entity_type = $2::kwave_entity_type,
       confidence = GREATEST(confidence, 0.500::numeric),
       notes = COALESCE(NULLIF(notes,'') || ' · ','') || 'resolve-unknowns: gpt ' || $2 || ' — ' || $3,
       updated_at = now()
 WHERE id = $1 AND entity_type='unknown'`, id, res.EntityType, res.Reason)
	if err != nil {
		if isUniqueViolation(err) { // 이미 같은 (ko,type) entity 존재 → 중복 제거.
			s.hardDelete(ctx, id, deleted)
			return true
		}
		return false
	}
	if tag.RowsAffected() == 0 {
		return false
	}
	if res.EntityType == "person" {
		_, _ = s.Pool.Exec(ctx, `
INSERT INTO kwave_persons (name_ko, primary_role, confidence, last_verified_at, created_at)
VALUES ($1, COALESCE(NULLIF($2,'')::person_role, 'other'::person_role), 0.500, now(), now())
ON CONFLICT (name_ko) DO NOTHING`, ko, derefStr(res.PrimaryRole))
		_, _ = s.Pool.Exec(ctx, `
INSERT INTO kwave_entity_person_details (entity_id, primary_role)
VALUES ($1, COALESCE(NULLIF($2,'')::person_role, 'other'::person_role))
ON CONFLICT (entity_id) DO NOTHING`, id, derefStr(res.PrimaryRole))
		s.persistPersonSignals(ctx, id, res)
		s.markHomonymsIfConflict(ctx, id, ko, res)
		mu.Lock()
		rep.PersonsAdded++
		mu.Unlock()
	}
	mu.Lock()
	rep.Promoted++
	mu.Unlock()
	return true
}

// rejectAsTerm — 실체 아님 확정: term + rejected 로 박아 unknown 을 없앤다.
// 기존 (ko,'term') 과 충돌하면 중복이므로 삭제.
func (s *Sweeper) rejectAsTerm(ctx context.Context, id uuid.UUID, deleted *int32) {
	_, err := s.Pool.Exec(ctx, `
UPDATE kwave_entities
   SET status='rejected', entity_type='term'::kwave_entity_type, confidence = 0.000,
       notes = COALESCE(NULLIF(notes,'') || ' · ','') || 'resolve-unknowns: 비실체/일반어 — term reject',
       updated_at = now()
 WHERE id = $1 AND entity_type='unknown'`, id)
	if err != nil && isUniqueViolation(err) {
		s.hardDelete(ctx, id, deleted)
	}
}

func (s *Sweeper) hardDelete(ctx context.Context, id uuid.UUID, deleted *int32) {
	if _, err := s.Pool.Exec(ctx, `DELETE FROM kwave_entities WHERE id=$1 AND operator_locked=false`, id); err == nil {
		atomic.AddInt32(deleted, 1)
	}
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// persistLog — cycle 결과 1 row INSERT (kwave_kdb_autopilot_log). 실패해도
// cycle 영향 X (log 만). migration 0064 미적용 환경에선 INSERT 가 에러나고
// silent skip — autopilot 동작엔 영향 없다.
func (s *Sweeper) persistLog(ctx context.Context, rep *Report) {
	if _, err := s.Pool.Exec(ctx, `
INSERT INTO kwave_kdb_autopilot_log
  (ran_at, duration_ms, jamo_merged, jamo_rejected, persons_added,
   entity_type_fixed, non_entity_reject, classified, classify_deferred,
   promoted, enriched, quality_fixed, alias_resolved)
VALUES (now(), $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		int(rep.Duration.Milliseconds()), rep.JamoMerged, rep.JamoRejected,
		rep.PersonsAdded, rep.EntityTypeFixed, rep.NonEntityReject,
		rep.Classified, rep.ClassifyDeferred, rep.Promoted, rep.Enriched,
		rep.QualityFixed, rep.AliasResolved); err != nil {
		log.Printf("kdb.autopilot: persistLog: %v", err)
	}
}

// --- step 1.5: entities ↔ persons 정합성 자동 동기화 -----------------------

// stepSyncPersons —
//
//	(a) entity_type='person' 인데 kwave_persons 에 없음 → INSERT default.
//	(b) kwave_persons 에 있는데 entities 의 같은 ko 가 entity_type<>'person'
//	    (대부분 'unknown') → entity_type='person' 으로 갱신 + entity_person_details INSERT.
func (s *Sweeper) stepSyncPersons(ctx context.Context, rep *Report) {
	// (a) entity_type='person' 인데 persons 에 없는 row.
	if tag, err := s.Pool.Exec(ctx, `
INSERT INTO kwave_persons (name_ko, primary_role, confidence, last_verified_at, created_at)
SELECT e.canonical_ko, 'other'::person_role, 0.500, now(), now()
FROM kwave_entities e
LEFT JOIN kwave_persons p ON p.name_ko = e.canonical_ko
WHERE e.entity_type='person' AND e.status='active' AND p.name_ko IS NULL
ON CONFLICT (name_ko) DO NOTHING`); err == nil {
		rep.PersonsAdded += int(tag.RowsAffected())
	}
	// entity_person_details 도 같이.
	_, _ = s.Pool.Exec(ctx, `
INSERT INTO kwave_entity_person_details (entity_id, primary_role)
SELECT e.id, COALESCE(p.primary_role, 'other'::person_role)
FROM kwave_entities e
LEFT JOIN kwave_entity_person_details d ON d.entity_id = e.id
LEFT JOIN kwave_persons p ON p.name_ko = e.canonical_ko
WHERE e.entity_type='person' AND e.status='active' AND d.entity_id IS NULL
ON CONFLICT (entity_id) DO NOTHING`)

	// (b) persons 에 있는데 entities 의 entity_type<>'person' → 'person' 으로 갱신.
	// kwave_persons 가 truth — 운영자가 가만히 둔 인물 DB 의 사실을 entities 가 따라감.
	if tag, err := s.Pool.Exec(ctx, `
UPDATE kwave_entities e
   SET entity_type = 'person'::kwave_entity_type, updated_at = now()
  FROM kwave_persons p
 WHERE p.name_ko = e.canonical_ko
   AND e.entity_type <> 'person'
   AND e.status='active'
   AND e.operator_locked = false`); err == nil {
		rep.EntityTypeFixed += int(tag.RowsAffected())
	}

	// (c) 다국어 mirror — person entity 의 canonical_* (enrich cascade 결과) 를
	// kwave_persons.name_* 로 복사. 9개 언어 전부 (ko 는 join key). 빈 칸만 채움
	// (operator-curated 값 보호). entity 가 enrich 로 채워지면 인물DB 도 따라 채워진다.
	_, _ = s.Pool.Exec(ctx, `
UPDATE kwave_persons p
   SET name_en      = COALESCE(NULLIF(p.name_en,''),      NULLIF(e.canonical_en,'')),
       name_ja      = COALESCE(NULLIF(p.name_ja,''),      NULLIF(e.canonical_ja,'')),
       name_vi      = COALESCE(NULLIF(p.name_vi,''),      NULLIF(e.canonical_vi,'')),
       name_es      = COALESCE(NULLIF(p.name_es,''),      NULLIF(e.canonical_es,'')),
       name_id      = COALESCE(NULLIF(p.name_id,''),      NULLIF(e.canonical_id,'')),
       name_pt_br   = COALESCE(NULLIF(p.name_pt_br,''),   NULLIF(e.canonical_pt_br,'')),
       name_zh      = COALESCE(NULLIF(p.name_zh,''),      NULLIF(e.canonical_zh,'')),
       name_zh_hant = COALESCE(NULLIF(p.name_zh_hant,''), NULLIF(e.canonical_zh_hant,''))
  FROM kwave_entities e
 WHERE e.canonical_ko = p.name_ko
   AND e.entity_type = 'person'
   AND e.status = 'active'
   AND p.operator_locked = false
   AND (
        (NULLIF(p.name_en,'')      IS NULL AND NULLIF(e.canonical_en,'')      IS NOT NULL) OR
        (NULLIF(p.name_ja,'')      IS NULL AND NULLIF(e.canonical_ja,'')      IS NOT NULL) OR
        (NULLIF(p.name_vi,'')      IS NULL AND NULLIF(e.canonical_vi,'')      IS NOT NULL) OR
        (NULLIF(p.name_es,'')      IS NULL AND NULLIF(e.canonical_es,'')      IS NOT NULL) OR
        (NULLIF(p.name_id,'')      IS NULL AND NULLIF(e.canonical_id,'')      IS NOT NULL) OR
        (NULLIF(p.name_pt_br,'')   IS NULL AND NULLIF(e.canonical_pt_br,'')   IS NOT NULL) OR
        (NULLIF(p.name_zh,'')      IS NULL AND NULLIF(e.canonical_zh,'')      IS NOT NULL) OR
        (NULLIF(p.name_zh_hant,'') IS NULL AND NULLIF(e.canonical_zh_hant,'') IS NOT NULL)
       )`)
}

// --- step 0: 자소 깨진 ko → 정상 짝 자동 merge / 또는 reject -------------

// stepRepairBrokenJamo — `canonical_ko ~ '[ㄱ-ㅎㅏ-ㅣ]'` row 정리.
//   - 자소 stripped ko 가 정상 active entity 와 일치 → 깨진 row 의 source_domains/aliases 를
//     정상 row 로 이전, 깨진 row aliases_ko 에 broken 형 등록, status='rejected'.
//   - 매칭 없음 → status='rejected' (정보 손실 방지 위해 노출만 막음).
//   - operator_locked 인 row 는 건드리지 않음.
func (s *Sweeper) stepRepairBrokenJamo(ctx context.Context, rep *Report) {
	rows, err := s.Pool.Query(ctx, `
SELECT id, canonical_ko, source_domains, aliases_ko, status
FROM kwave_entities
WHERE canonical_ko ~ '[ㄱ-ㅎㅏ-ㅣ]'
  AND operator_locked = false
  AND status <> 'rejected'
LIMIT 100`)
	if err != nil {
		log.Printf("kdb.autopilot: jamo scan: %v", err)
		return
	}
	defer rows.Close()
	type b struct {
		ID            uuid.UUID
		Ko            string
		SourceDomains []string
		AliasesKo     []string
		Status        string
	}
	broken := []b{}
	for rows.Next() {
		var x b
		if err := rows.Scan(&x.ID, &x.Ko, &x.SourceDomains, &x.AliasesKo, &x.Status); err == nil {
			broken = append(broken, x)
		}
	}
	for _, br := range broken {
		// hangul check (safety) — 정말 깨졌나.
		if hangul.IsCleanKorean(br.Ko) {
			continue
		}
		cleaned := hangul.StripLoneJamo(br.Ko)
		if cleaned == "" || cleaned == br.Ko {
			continue
		}
		// 정상 짝 찾기.
		var pairID uuid.UUID
		err := s.Pool.QueryRow(ctx, `
SELECT id FROM kwave_entities
WHERE canonical_ko = $1 AND status='active'
LIMIT 1`, cleaned).Scan(&pairID)
		if err != nil {
			// 매칭 없음 → 그냥 reject (정상 짝 발견 시점에 운영자가 살릴 수 있게 notes 에 cleaned 기록).
			_, _ = s.Pool.Exec(ctx, `
UPDATE kwave_entities
   SET status='rejected', confidence = 0.000,
       notes = COALESCE(NULLIF(notes,'') || ' · ','') || 'autopilot: 자소 깨진 ko, cleaned=' || $2,
       updated_at = now()
 WHERE id = $1 AND operator_locked = false`, br.ID, cleaned)
			rep.JamoRejected++
			continue
		}
		// merge: 정상 row 에 source_domains + aliases_ko (broken 형 보존, 검색 시 매칭) 누적.
		_, _ = s.Pool.Exec(ctx, `
UPDATE kwave_entities
   SET source_domains = (SELECT ARRAY(SELECT DISTINCT d FROM unnest(source_domains || $2::text[]) d WHERE d <> '')),
       aliases_ko     = (SELECT ARRAY(SELECT DISTINCT a FROM unnest(aliases_ko || $3::text[]) a WHERE a <> '')),
       updated_at     = now()
 WHERE id = $1`, pairID, br.SourceDomains, append(br.AliasesKo, br.Ko))
		_, _ = s.Pool.Exec(ctx, `
UPDATE kwave_entities
   SET status='rejected', confidence = 0.000,
       notes = COALESCE(NULLIF(notes,'') || ' · ','') || 'autopilot: 자소 typo, merged into ' || $2::text,
       updated_at = now()
 WHERE id = $1 AND operator_locked = false`, br.ID, pairID.String())
		rep.JamoMerged++
	}
}

// --- step 0.5: candidate 1매체 — gpt 검수 (일반어 자동 reject) -----------

// stepReviewCandidates — status='candidate' 모든 row 를 batch 처리.
//   - gpt classify → entity_type='term' + conf ≤ 0.40 이면 자동 reject (운영자 큐 X).
//   - 그 외 candidate 그대로 유지 (≥ 2 매체 시 stepPromoteConsensus 에서 처리).
//
// "건강하게", "세계일주" 같은 일상어 / 동음이의어 일반어가 inbox 에 누적되는 것 방지.
func (s *Sweeper) stepReviewCandidates(ctx context.Context, rep *Report) {
	rows, err := s.Pool.Query(ctx, `
SELECT id, canonical_ko, source_domains
FROM kwave_entities WHERE status='candidate' AND operator_locked = false
ORDER BY updated_at ASC
LIMIT $1`, s.Config.BatchClassify)
	if err != nil {
		log.Printf("kdb.autopilot: review select: %v", err)
		return
	}
	defer rows.Close()
	type r struct {
		ID uuid.UUID
		Ko string
		SD []string
	}
	cands := []r{}
	for rows.Next() {
		var x r
		if err := rows.Scan(&x.ID, &x.Ko, &x.SD); err == nil {
			cands = append(cands, x)
		}
	}
	for _, c := range cands {
		if !hangul.IsCleanKorean(c.Ko) {
			continue
		}
		callCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		res, err := s.Judge.Classify(callCtx, &aijudge.ClassifyInput{
			Ko:            c.Ko,
			SourceDomains: c.SD,
		})
		cancel()
		if err != nil || res == nil {
			continue
		}

		// 일반어 / 일상어 — 자동 reject (운영자 큐 X).
		if res.EntityType == "term" && res.Confidence <= 0.40 {
			_, _ = s.Pool.Exec(ctx, `
UPDATE kwave_entities
   SET status='rejected', confidence = 0.000,
       notes = COALESCE(NULLIF(notes,'') || ' · ','') || 'autopilot: gpt 일반어 — ' || $2,
       updated_at = now()
 WHERE id = $1 AND status='candidate'`, c.ID, res.Reason)
			rep.NonEntityReject++
			continue
		}

		// gpt 가 확신하는 실제 entity → 단일매체라도 자동 promote.
		// 매체 수 별 임계 (단일 0.85 / ≥2 0.75 / ≥3 0.70) 충족 시.
		// 759 candidate 적체는 대부분 1매체에만 등장해 stepPromoteConsensus(≥2)
		// 에 걸리지 않던 row — gpt 신뢰도로 직접 분류한다.
		realType := res.EntityType != "" && res.EntityType != "unknown" && res.EntityType != "term"
		if realType && !res.NeedsSearch && res.Confidence >= s.Config.minConfFor(len(c.SD)) {
			conf := 0.70
			if len(c.SD) >= 2 {
				conf = 0.75
			}
			if _, err := s.Pool.Exec(ctx, `
UPDATE kwave_entities
   SET status='active', entity_type = $2::kwave_entity_type,
       confidence = GREATEST(confidence, $3::numeric), updated_at = now()
 WHERE id = $1 AND status='candidate'`, c.ID, res.EntityType, conf); err != nil {
				continue
			}
			if res.EntityType == "person" {
				_, _ = s.Pool.Exec(ctx, `
INSERT INTO kwave_persons (name_ko, primary_role, confidence, last_verified_at, created_at)
VALUES ($1, COALESCE(NULLIF($2,'')::person_role, 'other'::person_role), 0.500, now(), now())
ON CONFLICT (name_ko) DO NOTHING`, c.Ko, derefStr(res.PrimaryRole))
				_, _ = s.Pool.Exec(ctx, `
INSERT INTO kwave_entity_person_details (entity_id, primary_role)
VALUES ($1, COALESCE(NULLIF($2,'')::person_role, 'other'::person_role))
ON CONFLICT (entity_id) DO NOTHING`, c.ID, derefStr(res.PrimaryRole))
				s.persistPersonSignals(ctx, c.ID, res)
				s.markHomonymsIfConflict(ctx, c.ID, c.Ko, res)
			}
			// enrich 는 inline 하지 않는다 — 같은 cycle 의 stepEnrichEmpty(batch 10,
			// 통제된 경로)가 newly-active 빈 locale 을 9개 언어로 채운다. 여기서
			// 후보마다 풀 cascade(최대 150s)를 돌리면 759 적체 × batch 로 cycle 이
			// 과도하게 길어진다.
			rep.Promoted++
			continue
		}

		// 애매 (conf 미달 / needs_search / unknown) — updated_at touch 로 큐 뒤로
		// 보내 다음 cycle 에 다른 후보가 처리되게 한다 (ASC rotation, 적체 방지).
		_, _ = s.Pool.Exec(ctx, `
UPDATE kwave_entities SET updated_at = now()
 WHERE id = $1 AND status='candidate'`, c.ID)
	}
}

// --- step 1: 미분류 자동 분류 ---------------------------------------------

type unkRow struct {
	ID            uuid.UUID
	Ko            string
	Confidence    float64
	SourceDomains []string
	En, Ja, Vi    string
}

func (s *Sweeper) stepClassifyUnknown(ctx context.Context, rep *Report) {
	rows, err := s.Pool.Query(ctx, `
SELECT id, canonical_ko, confidence, source_domains,
       COALESCE(canonical_en,''), COALESCE(canonical_ja,''), COALESCE(canonical_vi,'')
FROM kwave_entities
WHERE status='active' AND entity_type='unknown' AND operator_locked = false
ORDER BY confidence DESC, updated_at DESC
LIMIT $1`, s.Config.BatchClassify)
	if err != nil {
		log.Printf("kdb.autopilot: classify select: %v", err)
		return
	}
	defer rows.Close()
	candidates := []unkRow{}
	for rows.Next() {
		var x unkRow
		if err := rows.Scan(&x.ID, &x.Ko, &x.Confidence, &x.SourceDomains, &x.En, &x.Ja, &x.Vi); err == nil {
			candidates = append(candidates, x)
		}
	}
	if len(candidates) == 0 {
		return
	}

	for _, c := range candidates {
		// 자소 깨진 ko 는 자동 분류 대상 X (step 0 에서 정리됨, 안전망).
		if !hangul.IsCleanKorean(c.Ko) {
			continue
		}
		// gpt classify 호출.
		in := &aijudge.ClassifyInput{
			Ko:            c.Ko,
			SourceDomains: c.SourceDomains,
			Spellings:     map[string]string{},
		}
		if c.En != "" {
			in.Spellings["en"] = c.En
		}
		if c.Ja != "" {
			in.Spellings["ja"] = c.Ja
		}
		if c.Vi != "" {
			in.Spellings["vi"] = c.Vi
		}
		callCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		r, err := s.Judge.Classify(callCtx, in)
		cancel()
		if err != nil || r == nil {
			log.Printf("kdb.autopilot: classify(%s) err=%v", c.Ko, err)
			continue
		}
		// 일반어 / 일상어 — gpt 가 "term" + 낮은 conf 로 판단했으면 자동 reject.
		// "건강하게", "세계일주" 같은 entity 아닌 후보 자동 정리.
		if r.EntityType == "term" && r.Confidence <= 0.40 {
			_, _ = s.Pool.Exec(ctx, `
UPDATE kwave_entities
   SET status='rejected', confidence = 0.000,
       notes = COALESCE(NULLIF(notes,'') || ' · ','') || 'autopilot: gpt 일반어 판정 — ' || $2,
       updated_at = now()
 WHERE id = $1 AND operator_locked = false`, c.ID, r.Reason)
			rep.NonEntityReject++
			continue
		}
		if r.Confidence < s.Config.minConfFor(len(c.SourceDomains)) || r.EntityType == "" || r.EntityType == "unknown" || r.NeedsSearch {
			rep.ClassifyDeferred++
			continue
		}
		// 자동 적용 — entity_type UPDATE + (person 시) persons sync.
		if _, err := s.Pool.Exec(ctx, `
UPDATE kwave_entities SET entity_type = $2::kwave_entity_type, updated_at = now()
WHERE id = $1 AND operator_locked = false`, c.ID, r.EntityType); err != nil {
			log.Printf("kdb.autopilot: classify update(%s): %v", c.Ko, err)
			continue
		}
		if r.EntityType == "person" {
			_, _ = s.Pool.Exec(ctx, `
INSERT INTO kwave_persons (name_ko, primary_role, confidence, last_verified_at, created_at)
VALUES ($1, COALESCE(NULLIF($2,'')::person_role, 'other'::person_role), 0.500, now(), now())
ON CONFLICT (name_ko) DO NOTHING`, c.Ko, derefStr(r.PrimaryRole))
			_, _ = s.Pool.Exec(ctx, `
INSERT INTO kwave_entity_person_details (entity_id, primary_role)
VALUES ($1, COALESCE(NULLIF($2,'')::person_role, 'other'::person_role))
ON CONFLICT (entity_id) DO NOTHING`, c.ID, derefStr(r.PrimaryRole))
			// 동명이인 보조 신호 persist + 충돌 시 리뷰 라우팅.
			s.persistPersonSignals(ctx, c.ID, r)
			s.markHomonymsIfConflict(ctx, c.ID, c.Ko, r)
		}
		rep.Classified++
	}
}

// --- step 2: candidate ≥ 2 매체 → 자동 promote + enrich --------------------

func (s *Sweeper) stepPromoteConsensus(ctx context.Context, rep *Report) {
	rows, err := s.Pool.Query(ctx, `
SELECT id, canonical_ko, COALESCE(array_length(source_domains,1),0)
FROM kwave_entities
WHERE status='candidate' AND COALESCE(array_length(source_domains,1),0) >= 2
ORDER BY COALESCE(array_length(source_domains,1),0) DESC, updated_at DESC
LIMIT $1`, s.Config.BatchPromote)
	if err != nil {
		log.Printf("kdb.autopilot: promote select: %v", err)
		return
	}
	defer rows.Close()
	type c struct {
		ID    uuid.UUID
		Ko    string
		Media int
	}
	cands := []c{}
	for rows.Next() {
		var x c
		if err := rows.Scan(&x.ID, &x.Ko, &x.Media); err == nil {
			cands = append(cands, x)
		}
	}
	for _, cnd := range cands {
		// gpt 가 type 결정.
		callCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		in := &aijudge.ClassifyInput{
			Ko: cnd.Ko,
		}
		// source_domains 빠르게.
		_ = s.Pool.QueryRow(ctx, `SELECT source_domains FROM kwave_entities WHERE id=$1`, cnd.ID).Scan(&in.SourceDomains)
		r, err := s.Judge.Classify(callCtx, in)
		cancel()
		if err != nil || r == nil {
			continue
		}
		// 일반어 — 매체 매칭만으로 promote 하면 안 됨. 자동 reject.
		if r.EntityType == "term" && r.Confidence <= 0.40 {
			_, _ = s.Pool.Exec(ctx, `
UPDATE kwave_entities
   SET status='rejected', confidence = 0.000,
       notes = COALESCE(NULLIF(notes,'') || ' · ','') || 'autopilot: gpt 일반어 판정 — ' || $2,
       updated_at = now()
 WHERE id = $1 AND status='candidate'`, cnd.ID, r.Reason)
			rep.NonEntityReject++
			continue
		}
		if r.Confidence < s.Config.minConfFor(cnd.Media) || r.EntityType == "unknown" || r.NeedsSearch {
			continue // 운영자 큐 (inbox) 유지
		}
		conf := 0.75
		if cnd.Media >= 3 {
			conf = 0.80
		}
		if _, err := s.Pool.Exec(ctx, `
UPDATE kwave_entities
   SET status='active', entity_type = $2::kwave_entity_type,
       confidence = GREATEST(confidence, $3::numeric), updated_at = now()
 WHERE id = $1 AND status='candidate'`, cnd.ID, r.EntityType, conf); err != nil {
			continue
		}
		if r.EntityType == "person" {
			_, _ = s.Pool.Exec(ctx, `
INSERT INTO kwave_persons (name_ko, primary_role, confidence, last_verified_at, created_at)
VALUES ($1, COALESCE(NULLIF($2,'')::person_role, 'other'::person_role), 0.500, now(), now())
ON CONFLICT (name_ko) DO NOTHING`, cnd.Ko, derefStr(r.PrimaryRole))
			_, _ = s.Pool.Exec(ctx, `
INSERT INTO kwave_entity_person_details (entity_id, primary_role)
VALUES ($1, COALESCE(NULLIF($2,'')::person_role, 'other'::person_role))
ON CONFLICT (entity_id) DO NOTHING`, cnd.ID, derefStr(r.PrimaryRole))
			// 동명이인 보조 신호 persist + 충돌 시 리뷰 라우팅.
			s.persistPersonSignals(ctx, cnd.ID, r)
			s.markHomonymsIfConflict(ctx, cnd.ID, cnd.Ko, r)
		}
		// enrich cascade.
		enrichCtx, ec := context.WithTimeout(ctx, 150*time.Second)
		_, _ = s.Orch.Enrich(enrichCtx, cnd.ID)
		ec()
		rep.Promoted++
	}
}

// --- step 3: 빈 locale enrich batch --------------------------------------

func (s *Sweeper) stepEnrichEmpty(ctx context.Context, rep *Report) {
	rows, err := s.Pool.Query(ctx, `
SELECT id FROM kwave_entities
WHERE status='active' AND confidence >= 0.5 AND operator_locked = false
  AND entity_type <> 'unknown'
  AND (canonical_en IS NULL OR canonical_en=''
    OR canonical_ja IS NULL OR canonical_ja=''
    OR canonical_vi IS NULL OR canonical_vi=''
    OR canonical_es IS NULL OR canonical_es=''
    OR canonical_id IS NULL OR canonical_id=''
    OR canonical_pt_br IS NULL OR canonical_pt_br=''
    OR canonical_zh_hant IS NULL OR canonical_zh_hant=''
    OR canonical_zh IS NULL OR canonical_zh='')
ORDER BY confidence DESC, updated_at ASC
LIMIT $1`, s.Config.BatchEnrich)
	if err != nil {
		log.Printf("kdb.autopilot: enrich select: %v", err)
		return
	}
	defer rows.Close()
	ids := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		callCtx, cancel := context.WithTimeout(ctx, 150*time.Second)
		_, _ = s.Orch.Enrich(callCtx, id)
		cancel()
		rep.Enriched++
	}
}

// --- helpers --------------------------------------------------------------

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return fallback
}

func envFloat(key string, fallback float64) float64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil && n >= 0 && n <= 1 {
			return n
		}
	}
	return fallback
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(*p)
}

// hasGlobalPresence — entity 에 foreign-locale 표기가 하나라도 있나.
// KDB 가치는 global localization 이므로 동명이인 enrich 는 global 인물에 집중.
func (s *Sweeper) hasGlobalPresence(ctx context.Context, entityID uuid.UUID) bool {
	var present bool
	_ = s.Pool.QueryRow(ctx, `
SELECT (COALESCE(canonical_en,'') <> '' OR COALESCE(canonical_ja,'') <> ''
     OR COALESCE(canonical_vi,'') <> '' OR COALESCE(canonical_id,'') <> ''
     OR COALESCE(canonical_es,'') <> '' OR COALESCE(canonical_pt_br,'') <> ''
     OR COALESCE(canonical_zh_hant,'') <> '' OR COALESCE(canonical_zh,'') <> '')
  FROM kwave_entities WHERE id = $1`, entityID).Scan(&present)
	return present
}

// persistPersonSignals — gpt classify 가 확신한 인물 보조 사실을
// kwave_entity_person_details 에 채운다 (빈 값/0 은 건드리지 않음, 기존 non-empty 값 보호).
//
// 두 갈래 (이 함수가 design 의 persistPersonClaims 에 해당하는 classify→persist 경로):
//
//	(1) agency/birth_year/notable_works — global presence 있는 entity 만
//	    (동명이인 enrich 저우선, 기존 동작 유지).
//	(2) gender/groups/secondary_roles (PR3, design §B.1) — global presence
//	    여부와 무관하게 기록. 이 세 컬럼은 그간 writer 가 없어 groups 가 68%
//	    비어 있었음 (design A.2). Korean-only 인물도 채워 backlog 를 줄인다.
//
// 모든 write 는 기존 non-empty 값을 빈 값으로 덮어쓰지 않음.
func (s *Sweeper) persistPersonSignals(ctx context.Context, entityID uuid.UUID, r *aijudge.ClassifyResult) {
	if r == nil {
		return
	}

	// (1) agency/birth/works — global presence gated (기존 동작 유지).
	agency := strings.TrimSpace(r.Agency)
	works := compactStr(r.NotableWorks)
	if (agency != "" || len(works) > 0 || r.BirthYear != 0) && s.hasGlobalPresence(ctx, entityID) {
		// COALESCE(NULLIF(...)) 패턴: 새 값이 비어있으면 기존 값 유지.
		_, _ = s.Pool.Exec(ctx, `
UPDATE kwave_entity_person_details
   SET agency        = COALESCE(NULLIF($2,''), agency),
       birth_year    = COALESCE(NULLIF($3,0), birth_year),
       notable_works = CASE
                         WHEN array_length(notable_works,1) IS NULL AND $4::text[] <> '{}'::text[]
                           THEN $4::text[]
                         ELSE notable_works
                       END
 WHERE entity_id = $1`, entityID, agency, r.BirthYear, works)
	}

	// (2) gender/groups/secondary_roles — PR3, presence 무관. 빈 값은 무시.
	gender := normalizeGender(r.Gender)
	groups := compactStr(r.Groups)
	secRoles := validRoles(r.SecondaryRoles)
	if gender == "" && len(groups) == 0 && len(secRoles) == 0 {
		return
	}
	// 기존 non-empty 값 보호:
	//   gender         : 기존이 비어있을 때만 채움.
	//   groups         : 기존 배열이 비어있을 때만 채움.
	//   secondary_roles: 기존 배열이 비어있을 때만 채움 (person_role[] cast).
	_, _ = s.Pool.Exec(ctx, `
UPDATE kwave_entity_person_details
   SET gender = COALESCE(NULLIF(gender,''), NULLIF($2,'')),
       groups = CASE
                  WHEN array_length(groups,1) IS NULL AND $3::text[] <> '{}'::text[]
                    THEN $3::text[]
                  ELSE groups
                END,
       secondary_roles = CASE
                  WHEN array_length(secondary_roles,1) IS NULL AND $4::text[] <> '{}'::text[]
                    THEN $4::person_role[]
                  ELSE secondary_roles
                END
 WHERE entity_id = $1`, entityID, gender, groups, secRoles)
}

// normalizeGender — classify 결과 gender 를 'M'/'F' 로 정규화. 그 외 빈 문자열.
func normalizeGender(g string) string {
	switch strings.ToUpper(strings.TrimSpace(g)) {
	case "M", "MALE":
		return "M"
	case "F", "FEMALE":
		return "F"
	default:
		return ""
	}
}

// validRoles — person_role enum 에 속하는 값만 통과 (DB cast 실패 방지), 중복 제거.
func validRoles(in []string) []string {
	allowed := map[string]bool{
		"idol": true, "singer": true, "rapper": true, "actor": true,
		"broadcaster": true, "comedian": true, "director": true, "producer": true,
		"model": true, "creator": true, "athlete": true, "politician": true,
		"businessperson": true, "journalist": true, "fictional": true, "other": true,
	}
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, v := range in {
		v = strings.ToLower(strings.TrimSpace(v))
		if allowed[v] && !seen[v] {
			out = append(out, v)
			seen[v] = true
		}
	}
	return out
}

// markHomonymsIfConflict — 같은 canonical_ko 의 기존 person entity 와 이번 신호가
// agency/works/role/birth 로 충돌하면, 둘 다 needs_disambig=true 로 표시해
// 운영자 리뷰로 보낸다. 자동 merge/split 은 하지 않음 (보수적).
func (s *Sweeper) markHomonymsIfConflict(ctx context.Context, entityID uuid.UUID, ko string, r *aijudge.ClassifyResult) {
	if r == nil || r.EntityType != "person" {
		return
	}
	incoming := homonym.PersonSignals{
		PrimaryRole:  derefStr(r.PrimaryRole),
		Agency:       strings.TrimSpace(r.Agency),
		NotableWorks: compactStr(r.NotableWorks),
		BirthYear:    r.BirthYear,
	}
	rows, err := s.Pool.Query(ctx, `
SELECT e.id, COALESCE(d.primary_role::text,''), COALESCE(d.agency,''),
       COALESCE(d.birth_year,0), COALESCE(d.notable_works,'{}'::text[])
  FROM kwave_entities e
  LEFT JOIN kwave_entity_person_details d ON d.entity_id = e.id
 WHERE e.canonical_ko = $1 AND e.id <> $2 AND e.status = 'active'
   AND e.entity_type = 'person'`, ko, entityID)
	if err != nil {
		return
	}
	defer rows.Close()
	conflict := false
	for rows.Next() {
		var other uuid.UUID
		var role, agency string
		var birth int
		var works []string
		if err := rows.Scan(&other, &role, &agency, &birth, &works); err != nil {
			continue
		}
		existing := homonym.PersonSignals{PrimaryRole: role, Agency: agency, NotableWorks: works, BirthYear: birth}
		if homonym.Conflict(incoming, existing) {
			conflict = true
			_, _ = s.Pool.Exec(ctx,
				`UPDATE kwave_entities SET needs_disambig = true, updated_at = now() WHERE id = $1`, other)
		}
	}
	if conflict {
		_, _ = s.Pool.Exec(ctx,
			`UPDATE kwave_entities SET needs_disambig = true, updated_at = now() WHERE id = $1`, entityID)
	}
}

func compactStr(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// --- step 4: confidence < 0.7 자동 검수 (Wikidata 채택) -------------------
//
// 운영자 정공법: 신뢰도 낮은 active entity 는 Wikidata K-Wave 매칭이 있으면
// 자동 채택해서 빈 locale 채우고 confidence 올림. 매칭 실패하면 그대로 유지
// (notes 에 audit, 운영자가 결정).
//
// orchestrator.Enrich 가 Wikidata 호출 + external_refs 기록까지 함. 이 step 은
// 그 결과로 confidence 갱신만 추가.
func (s *Sweeper) stepQualityReview(ctx context.Context, rep *Report) {
	rows, err := s.Pool.Query(ctx, `
SELECT id FROM kwave_entities
WHERE status='active' AND operator_locked = false
  AND confidence < 0.70
  AND entity_type <> 'unknown'
ORDER BY confidence ASC, updated_at DESC
LIMIT $1`, s.Config.BatchEnrich)
	if err != nil {
		log.Printf("kdb.autopilot: quality select: %v", err)
		return
	}
	defer rows.Close()
	ids := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		enrichCtx, cancel := context.WithTimeout(ctx, 150*time.Second)
		rep2, err := s.Orch.Enrich(enrichCtx, id)
		cancel()
		if err != nil || rep2 == nil {
			continue
		}
		// Wikidata 가 external_refs 에 기록됐으면 confidence 0.75 로 끌어올림.
		var hasWD bool
		_ = s.Pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM kwave_entity_external_refs WHERE entity_id = $1 AND provider='wikidata')`, id).Scan(&hasWD)
		if hasWD {
			tag, err := s.Pool.Exec(ctx,
				`UPDATE kwave_entities SET confidence = GREATEST(confidence, 0.750), updated_at = now()
				 WHERE id = $1 AND operator_locked = false`, id)
			if err == nil && tag.RowsAffected() > 0 {
				rep.QualityFixed++
			}
		}
	}
}

// --- step 5: alias 다중 매핑 자동 재할당 ----------------------------------
//
// 같은 alias 가 여러 active entity 의 aliases_ko 에 등록 → cascade resolver 가
// 어느 entity 인지 결정 못함. 규칙:
//
//	(a) alias 가 어떤 entity 의 canonical_ko 와 정확 매칭 → 그 entity 에만 유지.
//	(b) 매칭 없으면 confidence 최고 entity 에만 유지 (단, 차이가 0.1+ 일 때만).
//	(c) 그 외 운영자 큐 (지금 ko 만 처리 — 다른 locale 도 같은 패턴이지만 batch 제한).
func (s *Sweeper) stepResolveAliasConflicts(ctx context.Context, rep *Report) {
	rows, err := s.Pool.Query(ctx, `
WITH expanded AS (
  SELECT id, canonical_ko, confidence, unnest(aliases_ko) AS alias
  FROM kwave_entities WHERE status='active' AND operator_locked = false
), conflicts AS (
  SELECT alias, COUNT(DISTINCT id) AS n, array_agg(DISTINCT id) AS ids
  FROM expanded WHERE alias <> ''
  GROUP BY alias HAVING COUNT(DISTINCT id) > 1
  ORDER BY n DESC LIMIT 30
)
SELECT alias, ids FROM conflicts`)
	if err != nil {
		log.Printf("kdb.autopilot: alias select: %v", err)
		return
	}
	defer rows.Close()
	type c struct {
		Alias string
		IDs   []uuid.UUID
	}
	conflicts := []c{}
	for rows.Next() {
		var x c
		if err := rows.Scan(&x.Alias, &x.IDs); err == nil && len(x.IDs) >= 2 {
			conflicts = append(conflicts, x)
		}
	}
	for _, cf := range conflicts {
		// (a) alias 가 어떤 entity 의 canonical_ko 와 정확 매칭?
		var primary uuid.UUID
		err := s.Pool.QueryRow(ctx, `
SELECT id FROM kwave_entities
WHERE canonical_ko = $1 AND status='active' AND id = ANY($2)
LIMIT 1`, cf.Alias, cf.IDs).Scan(&primary)
		if err != nil {
			// (b) confidence 최고 entity 찾기 + 격차 ≥ 0.1.
			var top uuid.UUID
			var topConf, secondConf float64
			if err := s.Pool.QueryRow(ctx, `
SELECT id, confidence FROM kwave_entities
WHERE id = ANY($1) ORDER BY confidence DESC LIMIT 1`, cf.IDs).Scan(&top, &topConf); err != nil {
				continue
			}
			_ = s.Pool.QueryRow(ctx, `
SELECT confidence FROM kwave_entities
WHERE id = ANY($1) AND id <> $2 ORDER BY confidence DESC LIMIT 1`, cf.IDs, top).Scan(&secondConf)
			if topConf-secondConf < 0.1 {
				continue // 운영자 큐
			}
			primary = top
		}
		// primary 외 다른 entity 의 aliases_ko 에서 해당 alias 제거.
		tag, err := s.Pool.Exec(ctx, `
UPDATE kwave_entities
   SET aliases_ko = array_remove(aliases_ko, $1), updated_at = now()
 WHERE id <> $2 AND id = ANY($3) AND operator_locked = false`,
			cf.Alias, primary, cf.IDs)
		if err == nil && tag.RowsAffected() > 0 {
			rep.AliasResolved++
		}
	}
}

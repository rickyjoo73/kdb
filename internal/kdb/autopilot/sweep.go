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
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

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
	rep := Report{StartedAt: time.Now()}
	s.stepRepairBrokenJamo(ctx, &rep)
	s.stepSyncPersons(ctx, &rep)
	s.stepReviewCandidates(ctx, &rep) // candidate 1매체 — gpt 검수 / 일반어 자동 reject
	s.stepClassifyUnknown(ctx, &rep)
	s.stepPromoteConsensus(ctx, &rep)
	s.stepEnrichEmpty(ctx, &rep)
	s.stepQualityReview(ctx, &rep)
	s.stepResolveAliasConflicts(ctx, &rep)
	rep.Duration = time.Since(rep.StartedAt)
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
ORDER BY updated_at DESC
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
		if res.EntityType == "term" && res.Confidence <= 0.40 {
			_, _ = s.Pool.Exec(ctx, `
UPDATE kwave_entities
   SET status='rejected', confidence = 0.000,
       notes = COALESCE(NULLIF(notes,'') || ' · ','') || 'autopilot: gpt 일반어 — ' || $2,
       updated_at = now()
 WHERE id = $1 AND status='candidate'`, c.ID, res.Reason)
			rep.NonEntityReject++
		}
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
    OR canonical_vi IS NULL OR canonical_vi=''
    OR canonical_es IS NULL OR canonical_es=''
    OR canonical_id IS NULL OR canonical_id=''
    OR canonical_pt_br IS NULL OR canonical_pt_br=''
    OR canonical_zh_hant IS NULL OR canonical_zh_hant='')
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

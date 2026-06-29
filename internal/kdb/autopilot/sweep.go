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
	"encoding/json"
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
	"github.com/rickyjoo73/kdb/internal/kdb/codexcli"
	"github.com/rickyjoo73/kdb/internal/kdb/enrich"
	"github.com/rickyjoo73/kdb/internal/kdb/hangul"
	"github.com/rickyjoo73/kdb/internal/kdb/homonym"
	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
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
	WikidataVeto  bool // 일반어 거부 직전 Wikidata K-엔티티 재검증으로 오거부 방지(1단계)
	WebSearchVeto bool // Wikidata 부재 시 typed 후보를 웹검색 문맥으로 재검증(2단계)
}

func DefaultConfig() Config {
	return Config{
		BatchClassify: envInt("KDB_AUTOPILOT_BATCH_CLASSIFY", 20),
		BatchEnrich:   envInt("KDB_AUTOPILOT_BATCH_ENRICH", 10),
		BatchPromote:  envInt("KDB_AUTOPILOT_BATCH_PROMOTE", 20),
		MinConfSingle: envFloat("KDB_AUTOPILOT_MIN_CONF_SINGLE", 0.85),
		MinConfTwo:    envFloat("KDB_AUTOPILOT_MIN_CONF_TWO", 0.75),
		MinConfThree:  envFloat("KDB_AUTOPILOT_MIN_CONF_THREE", 0.70),
		WikidataVeto:  envBool("KDB_WIKIDATA_VETO", true),
		WebSearchVeto: envBool("KDB_WEBSEARCH_VETO", true),
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
	WD     *wikidata.Client // 일반어 오판 거부 직전 K-엔티티 재검증(veto)

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
		WD:     wikidata.New(),
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
	WikidataRescued  int // 일반어 오판 거부 직전 Wikidata 재검증으로 구제(active 승격)
	Quarantined      int // typed 큐 힌트 후보를 reject 대신 candidate 로 보류(운영자 검토 대기)
	ScopeFlagged     int // K-범위 재판정에서 out-of-scope(비-K) 의심으로 표면화된 active person
	ContamFlagged    int // 오염-의심 재판정(비-person)에서 정크/범위밖으로 표면화된 active 엔티티
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
	// 위 9개는 Hermes 모드에서 role-agent 로 등록돼 SuperviseCycle 이 실행한다. 아래
	// runTail 은 등록되지 않은 step/finalizer 라 Hermes 모드에선 main.go 가 cycle 종료
	// 후 별도 호출한다(parity). plain 모드(auto.Run)에선 여기서 호출.
	s.runTail(ctx, &rep)
	rep.Duration = time.Since(rep.StartedAt)
	s.persistLog(ctx, &rep)
	log.Printf("kdb.autopilot: done jamo=%d/%d persons=+%d type→person=%d term-reject=%d classified=%d/%d promoted=%d enriched=%d quality=%d alias=%d quarantine=%d (%s)",
		rep.JamoMerged, rep.JamoRejected,
		rep.PersonsAdded, rep.EntityTypeFixed,
		rep.NonEntityReject,
		rep.Classified, rep.ClassifyDeferred,
		rep.Promoted, rep.Enriched,
		rep.QualityFixed, rep.AliasResolved,
		rep.Quarantined,
		rep.Duration)
	if rep.ScopeFlagged > 0 {
		log.Printf("kdb.autopilot: scope-review 비-K 의심 %d건 표면화([scope:review])", rep.ScopeFlagged)
	}
	if rep.ContamFlagged > 0 {
		log.Printf("kdb.autopilot: contam-review 오염/정크 의심 %d건 표면화([contam:review])", rep.ContamFlagged)
	}
	return rep
}

// runTail — auto.Run 의 꼬리(미등록 step + finalizer). Hermes 모드에선 SuperviseCycle
// 이 등록된 9개 role-agent 만 돌리고 이 함수를 건너뛰므로(과거: on-demand drain·person
// 상세보완·WF-2 가시화·clearResolvedDisambig 가 프로덕션에서 미실행), main.go 가 cycle
// 종료 후 이걸 호출해 plain/Hermes 동작을 일치시킨다.
func (s *Sweeper) runTail(ctx context.Context, rep *Report) {
	s.stepDrainOnDemandCandidates(ctx, rep) // on-demand 7일+ → 완화 임계 최종 판단
	s.stepFillPersonDetails(ctx, rep)       // person agency/birth 미입력 보완
	s.stepDeduplicateCanonicalEn(ctx, rep)  // WF-2: 동일 canonical_en+type 충돌 가시화
	s.stepSweepContamination(ctx, rep)      // 결정론적 오염 자동 정리(canonical_ko 비한글 손상)
	s.stepScopeReview(ctx, rep)             // 자율 K-범위 재판정(비-K 인물 자동 발굴·표면화)
	s.stepContamReview(ctx, rep)            // 자율 오염-의심 재판정(비-person, 공식외국어 적은 순 우선)
	s.clearResolvedDisambig(ctx)            // 해소된 충돌 needs_disambig 자동 클리어(stuck 방지)
}

// stepScopeReview — 자율 scope-QA(2026-06-20): 기존 active person 을 매 cycle batch 로
// K-범위 재판정한다. "다른 오염을 누가/언제 자동으로 찾나"의 답 — 운영자가 일일이
// 짚지 않아도 시스템이 스스로 비-K 인물(젠슨황/안젤리나졸리 류)을 발굴한다.
//
// Gemma(주력, 저렴) classify 를 K-scope 프롬프트로 재실행:
//   - out-of-scope(entity_type=term 또는 reason 에 비-K/범위밖) → notes [scope:review] 마커 +
//     로그로 표면화(대시보드 [11]). *자동 reject 하지 않는다* — scope 판정은 오판 위험이
//     있어(미상 K 아이돌을 Gemma 가 모를 수 있음) 운영자 확인 후 reject. 명백한 건은 운영자 1액션.
//   - K-person 확정 → notes [scope:ok] 마커로 재검사 제외(점진적 전수 1회 후 신규만).
//
// operator_locked·이미 [scope:*] 마킹된 행 제외. 비용: Gemma batch(BatchClassify)/cycle.
func (s *Sweeper) stepScopeReview(ctx context.Context, rep *Report) {
	type prow struct {
		ID         uuid.UUID
		Ko         string
		SD         []string
		En, Ja, Vi string
	}
	rows, err := s.Pool.Query(ctx, `
SELECT id, canonical_ko, COALESCE(source_domains,'{}'::text[]),
       COALESCE(canonical_en,''), COALESCE(canonical_ja,''), COALESCE(canonical_vi,'')
  FROM kwave_entities
 WHERE status='active' AND entity_type='person' AND operator_locked=false
   AND COALESCE(notes,'') NOT LIKE '%[scope:%'
 ORDER BY confidence ASC, updated_at ASC
 LIMIT $1`, s.Config.BatchClassify)
	if err != nil {
		log.Printf("kdb.autopilot: scope-review select 실패: %v", err)
		return
	}
	var batch []prow
	for rows.Next() {
		var p prow
		if rows.Scan(&p.ID, &p.Ko, &p.SD, &p.En, &p.Ja, &p.Vi) == nil {
			batch = append(batch, p)
		}
	}
	rows.Close()

	for _, p := range batch {
		sp := map[string]string{}
		if p.En != "" {
			sp["en"] = p.En
		}
		if p.Ja != "" {
			sp["ja"] = p.Ja
		}
		if p.Vi != "" {
			sp["vi"] = p.Vi
		}
		cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		res, err := s.Judge.Classify(cctx, &aijudge.ClassifyInput{Ko: p.Ko, Spellings: sp, SourceDomains: p.SD})
		cancel()
		// Classify 는 transport/decode 실패 시 err=nil + {EntityType:"unknown",conf:0,Reason:err}
		// 합성을 돌려준다. err 만 보면 못 거르므로 unknown/빈 type 도 '판정실패'로 보고
		// *아무 마커도 찍지 않고* 다음 cycle 재시도한다 — 안 그러면 Gemma 장애 cycle 에
		// 정상 인물이 판정 없이 [scope:ok] 로 영구 각인돼 scope-review 에서 영구 누락된다.
		if err != nil || res == nil || res.EntityType == "" || res.EntityType == "unknown" {
			continue
		}
		reason := strings.ToLower(res.Reason)
		outOfScope := res.EntityType == "term" ||
			strings.Contains(res.Reason, "비-K") || strings.Contains(res.Reason, "비K") ||
			strings.Contains(res.Reason, "범위밖") || strings.Contains(res.Reason, "범위 밖") ||
			strings.Contains(reason, "out-of-scope") || strings.Contains(reason, "out of scope")
		if outOfScope {
			tag, _ := s.Pool.Exec(ctx, `
UPDATE kwave_entities
   SET notes = COALESCE(NULLIF(notes,'') || ' · ','') || '[scope:review] K-범위 의심(비-K): ' || $2,
       updated_at=now()
 WHERE id=$1 AND status='active' AND operator_locked=false AND COALESCE(notes,'') NOT LIKE '%[scope:%'`,
				p.ID, strings.TrimSpace(res.Reason))
			if tag.RowsAffected() == 1 {
				rep.ScopeFlagged++
				log.Printf("kdb.scope-review: 비-K 의심 표면화 — %q (%s)", p.Ko, strings.TrimSpace(res.Reason))
			}
			continue
		}
		// K-person 확정(real type, unknown 아님) — 재검사 제외 마킹.
		_, _ = s.Pool.Exec(ctx, `
UPDATE kwave_entities SET notes = COALESCE(NULLIF(notes,'') || ' · ','') || '[scope:ok]', updated_at=now()
 WHERE id=$1 AND status='active' AND operator_locked=false AND COALESCE(notes,'') NOT LIKE '%[scope:%'`, p.ID)
	}
}

// authoritativeForeignSources — '공식(권위) 소스'로 인정하는 source 라벨(오너 휴리스틱의
// "공식적인 곳에서 가져온"). verified-tier 와 동일: operator/wikidata/external-db/media-consensus.
// romanization/opencc/codex/langlinks 는 derived/추정이라 제외(공식 외국어 표기 아님).
var authoritativeForeignSources = []string{
	"operator-locked", "operator", "wikidata-label", "tmdb", "itunes", "discogs",
	"kofic", "kmdb", "musicbrainz", "naver-people", "correction-verified", "netflix", "disney", "media-consensus",
}

// stepContamReview — 자율 오염-의심 재판정(2026-06-29, 오너 휴리스틱). stepScopeReview(person
// 전용)의 *비-person*(고유명사: drama/group/agency/song_album/…) 버전.
//
// ★우선순위 = 오너 신호: "공식 외국어 표기 수가 적을수록 오염 후보 1위"(한국어만 있고 공식
// 외국어를 못 가져온 것). ORDER BY 권위-locale-수 ASC 로 가장 의심스러운 것부터 Gemma 재판정.
// ★단 공식외국어 0 이라도 niche-실제(영어제목 곡·라디오·캐릭터)·enrich 미완(해운대=QID 미연결)
// 이 섞여 *자동 reject 안 한다*(실측: 최협소 의심군 203 도 80%+ 실제) — 커버리지는 '순서'에만
// 쓰고, 판정은 Gemma 가. 정크/범위밖 판정만 [contam:review] 플래그(운영자 1액션 reject),
// 실제 type 확정은 [contam:ok] 로 재검사 제외. unknown/판정실패는 마커 안 찍고 다음 cycle 재시도.
// ★song_album 제외(2026-06-29 실측): Gemma 가 영어제목 실제 K-곡(백현 Winter Ahead·EXID Ah Yeah)을
// "일반 영어구절"로 과-플래그 → 정밀도 낮음. song_album 은 이미 iTunes/Discogs/MB 로 floor 확정이라
// contam-review 대상에서 빼 신호를 깨끗하게 유지(더 가디언 류 진짜 스코프누수에 집중).
func (s *Sweeper) stepContamReview(ctx context.Context, rep *Report) {
	type prow struct {
		ID         uuid.UUID
		Ko, Type   string
		SD         []string
		En, Ja, Vi string
	}
	rows, err := s.Pool.Query(ctx, `
SELECT id, canonical_ko, entity_type::text, COALESCE(source_domains,'{}'::text[]),
       COALESCE(canonical_en,''), COALESCE(canonical_ja,''), COALESCE(canonical_vi,'')
  FROM kwave_entities
 WHERE status='active' AND entity_type NOT IN ('person','song_album') AND operator_locked=false
   AND COALESCE(notes,'') NOT LIKE '%[contam:%'
 ORDER BY (
   (canonical_en_source = ANY($2))::int + (canonical_ja_source = ANY($2))::int
 + (canonical_vi_source = ANY($2))::int + (canonical_id_source = ANY($2))::int
 + (canonical_es_source = ANY($2))::int + (canonical_pt_br_source = ANY($2))::int
 + (canonical_zh_source = ANY($2))::int + (canonical_zh_hant_source = ANY($2))::int
 ) ASC, confidence ASC, updated_at ASC
 LIMIT $1`, s.Config.BatchClassify, authoritativeForeignSources)
	if err != nil {
		log.Printf("kdb.autopilot: contam-review select 실패: %v", err)
		return
	}
	var batch []prow
	for rows.Next() {
		var p prow
		if rows.Scan(&p.ID, &p.Ko, &p.Type, &p.SD, &p.En, &p.Ja, &p.Vi) == nil {
			batch = append(batch, p)
		}
	}
	rows.Close()

	for _, p := range batch {
		sp := map[string]string{}
		if p.En != "" {
			sp["en"] = p.En
		}
		if p.Ja != "" {
			sp["ja"] = p.Ja
		}
		if p.Vi != "" {
			sp["vi"] = p.Vi
		}
		cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		res, err := s.Judge.Classify(cctx, &aijudge.ClassifyInput{Ko: p.Ko, Spellings: sp, SourceDomains: p.SD})
		cancel()
		// 판정실패(transport/decode → unknown/빈 type)는 마커 안 찍고 다음 cycle 재시도
		// (Gemma 장애 cycle 에 실제 엔티티가 [contam:ok] 영구각인되는 것 방지 — scope-review 와 동일).
		if err != nil || res == nil || res.EntityType == "" || res.EntityType == "unknown" {
			continue
		}
		reason := strings.ToLower(res.Reason)
		suspect := res.EntityType == "term" ||
			strings.Contains(res.Reason, "비-K") || strings.Contains(res.Reason, "비K") ||
			strings.Contains(res.Reason, "범위밖") || strings.Contains(res.Reason, "범위 밖") ||
			strings.Contains(reason, "out-of-scope") || strings.Contains(reason, "out of scope") ||
			strings.Contains(reason, "junk") || strings.Contains(reason, "정크") || strings.Contains(reason, "일반어")
		if suspect {
			tag, _ := s.Pool.Exec(ctx, `
UPDATE kwave_entities
   SET notes = COALESCE(NULLIF(notes,'') || ' · ','') || '[contam:review] 오염/정크 의심: ' || $2,
       updated_at=now()
 WHERE id=$1 AND status='active' AND operator_locked=false AND COALESCE(notes,'') NOT LIKE '%[contam:%'`,
				p.ID, strings.TrimSpace(res.Reason))
			if tag.RowsAffected() == 1 {
				rep.ContamFlagged++
				log.Printf("kdb.contam-review: 오염/정크 의심 — %q [%s] (%s)", p.Ko, p.Type, strings.TrimSpace(res.Reason))
			}
			continue
		}
		// 실제 type 확정 — 재검사 제외 마킹.
		_, _ = s.Pool.Exec(ctx, `
UPDATE kwave_entities SET notes = COALESCE(NULLIF(notes,'') || ' · ','') || '[contam:ok]', updated_at=now()
 WHERE id=$1 AND status='active' AND operator_locked=false AND COALESCE(notes,'') NOT LIKE '%[contam:%'`, p.ID)
	}
}

// RunContamReview — on-demand 단발 오염-의심 재판정(CLI contam-review / 검증용). n>0 이면
// 그 배치로 실행. 새 Sweeper(one-shot 프로세스)라 Config 변경 안전. 반환=[contam:review] 플래그수.
func (s *Sweeper) RunContamReview(ctx context.Context, n int) int {
	if n > 0 {
		s.Config.BatchClassify = n
	}
	rep := &Report{}
	s.stepContamReview(ctx, rep)
	return rep.ContamFlagged
}

// stepSweepContamination — DB 에 *이미 들어온* 결정론적 오염을 매 cycle 자동으로 찾아
// 정리한다(2026-06-20). 유입 가드(PreGate·ko 문자셋)는 신규만 막으므로, 기존/잔존
// 오염은 이 스윕이 자율 처리한다 — 운영자가 일일이 짚지 않아도 됨.
//
// 대상(결정론적 = LLM 불필요, 오탐 거의 없음):
//
//	canonical_ko 가 한글을 *전혀* 포함하지 않으면서 가나(일본어) 또는 한자를 포함 →
//	K-콘텐츠 엔티티의 한국어 정본이 일본어/한자로 손상된 것(예: 常田大希, ホジュン~).
//	active 를 rejected 로 내려 소비자 노출을 막는다(잘못된 표기 송출보다 미노출이 안전).
//	재발굴 시 가드된 파이프라인이 정상 한글로 재생성. operator_locked 는 건드리지 않음.
//
// 모호한 클래스(고은언니 류 person 호칭·canonical_en 교차인물)는 오탐 위험이 있어
// 여기서 자동 reject 하지 않고 운영자 검수 대시보드([8][10]) + DataQA(gpt-5.5, 20분)로
// 넘긴다. canonical_ko 비한글만 결정론적이라 자동 처리한다.
func (s *Sweeper) stepSweepContamination(ctx context.Context, rep *Report) {
	tag, err := s.Pool.Exec(ctx, `
UPDATE kwave_entities
   SET status='rejected', confidence=0.000,
       notes = COALESCE(NULLIF(notes,'') || ' · ','') ||
               'autopilot: canonical_ko 비한글(일본어/한자) 손상 자동 정리 — 재발굴 시 정상 한글로 재생성',
       updated_at=now()
 WHERE status='active' AND operator_locked=false
   AND canonical_ko !~ '[가-힣]'                              -- 한글 전무
   AND (canonical_ko ~ '[぀-ヿ]' OR canonical_ko ~ '[一-鿿]')  -- 가나 또는 CJK한자 포함
   AND id IN (
     SELECT id FROM kwave_entities
      WHERE status='active' AND operator_locked=false
        AND canonical_ko !~ '[가-힣]'
        AND (canonical_ko ~ '[぀-ヿ]' OR canonical_ko ~ '[一-鿿]')
      LIMIT 50)`)
	if err != nil {
		log.Printf("kdb.autopilot: contamination sweep 실패: %v", err)
		return
	}
	if n := int(tag.RowsAffected()); n > 0 {
		rep.NonEntityReject += n
		log.Printf("kdb.autopilot: 오염 자동정리 — canonical_ko 비한글 손상 %d건 rejected", n)
	}
}

// RunTail 은 Hermes 경로(cmd/kdb)가 SuperviseCycle 후 호출하는 공개 래퍼.
// #6 in-place 감독을 위해 finalizer 6종이 수행한 작업을 담은 Report 를 반환한다
// (호출부가 run row 로 기록). 기존 호출부가 반환을 무시해도 무방.
func (s *Sweeper) RunTail(ctx context.Context) Report {
	rep := Report{StartedAt: time.Now()}
	s.runTail(ctx, &rep)
	rep.Duration = time.Since(rep.StartedAt)
	return rep
}

// TailActions — finalizer 가 실제 수행한 작업 총합(run row 의 items_out 용). rep 는
// RunTail 에서 새로 만든 것이라 여기 담긴 카운트는 전부 tail step 이 채운 것.
func (r Report) TailActions() int {
	return r.Classified + r.Promoted + r.Enriched + r.PersonsAdded + r.EntityTypeFixed +
		r.QualityFixed + r.AliasResolved + r.WikidataRescued + r.Quarantined + r.ScopeFlagged +
		r.ContamFlagged + r.NonEntityReject + r.JamoMerged + r.JamoRejected
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
  AND COALESCE(notes,'') NOT LIKE '%[kdb:q:typed]%'
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
		if s.tryRescue(ctx, id, ko, sd, rep, mu) {
			return
		}
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
  AND COALESCE(notes,'') NOT LIKE '%[kdb:q:typed]%'
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
//
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
  AND COALESCE(notes,'') NOT LIKE '%[kdb:q:typed]%'
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
		if s.tryRescue(ctx, id, ko, sd, rep, mu) {
			return
		}
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
SELECT id, canonical_ko, source_domains,
       (updated_at < now()-interval '14 days') AS aged
FROM kwave_entities
 WHERE entity_type='unknown' AND operator_locked = false
   AND COALESCE(notes,'') NOT LIKE '%[kdb:q:typed]%'
 ORDER BY status, updated_at ASC LIMIT $1`, batchResolveUnknowns)
	if err != nil {
		log.Printf("kdb.autopilot: resolve-unknowns select: %v", err)
		return
	}
	type item struct {
		ID   uuid.UUID
		Ko   string
		SD   []string
		Aged bool
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.ID, &it.Ko, &it.SD, &it.Aged); err == nil {
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
		// 14일+ 된 unknown — 더 이상 기다리지 않고 강제 확정(aggressive=true).
		// web 검색 후에도 K-content 실체가 아니면 term+rejected 로 정리.
		s.resolveUnknownOne(ctx, it.ID, it.Ko, it.SD, rep, &mu, &searched, &deleted, it.Aged)
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
  AND COALESCE(notes,'') NOT LIKE '%[kdb:q:typed]%'
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
		// ★오거부 봉인(CR-1, 2026-06-28): hard-reject 직전 tryRescue(Wikidata SearchAndFetch
		// 이름검증 + typed 큐 quarantine) 1회 — 실존 K-엔티티('막걸리 한잔'·'무조건' 류)가
		// 일반어로 영구 박제되는 것 차단. 구제/보류되면 reject 스킵.
		if s.tryRescue(ctx, id, ko, sd, rep, mu) {
			return
		}
		// 실체 아님 확정(문맥+외부증거로도 못 만듦) → term + rejected ("버린다").
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

// tryRescue — gpt 가 "일반어(term)" 로 판단해 거부하기 직전 호출하는 veto. 게이트키퍼가
// 맥락 없는 단일 토큰을 일반명사로 오판해 실재 K-엔티티(예: 신기루 개그우먼, 금비 가수)를
// 거부하는 문제를 막는다. 2단계로 외부 증거를 모아 재분류하고, 실체 type(term/unknown
// 아님)이 나오면 active 로 승격(구제)한다. 성공 시 true → 호출부는 reject 를 건너뛴다.
//
//	1단계 Wikidata: SearchAndFetch(K-Wave description + 이름 정규화 일치)로 확인 후
//	   Wikidata 증거를 붙여 재분류. "앤더슨"(→.Paak)·"용만"(→김용만) 류 맨 토큰은
//	   이름 불일치로 통과 못 한다(추측 매핑 오염 방지).
//	2단계 웹검색(Wikidata 부재 보완): research 큐에 typed 힌트(person/group/…)가 있는
//	   후보에 한해(비용 게이트) Google News 문맥을 모아 재분류. Wikidata 에 없는 소규모
//	   인물(금비·신기루)을 구제한다. typed 힌트가 없으면 잡토큰으로 보고 건너뛴다.
//
// 외부 조회 실패/재분류가 여전히 term·unknown 이면 false(거부 진행). active 승격은
// candidate 풀을 떠나므로 다음 cycle 재거부 루프가 없다. mu 가 nil 이면 단일 스레드
// 호출(stepReviewCandidates 등), non-nil 이면 worker 병렬(drainOne 등) rep 보호.
func (s *Sweeper) tryRescue(ctx context.Context, id uuid.UUID, ko string, sd []string, rep *Report, mu *sync.Mutex) bool {
	// 문장조각/잡토큰엔 호출 낭비 방지 — 2~25 음절 범위만.
	if n := len([]rune(strings.TrimSpace(ko))); n < 2 || n > 25 {
		return false
	}

	// 1단계: Wikidata 증거로 재분류.
	if s.Config.WikidataVeto && s.WD != nil {
		wctx, wcancel := context.WithTimeout(ctx, 12*time.Second)
		ent, cand, werr := s.WD.SearchAndFetch(wctx, ko)
		wcancel()
		if werr == nil && ent != nil {
			desc, label := "", ko
			if cand != nil {
				desc = cand.Description
				if cand.Label != "" {
					label = cand.Label
				}
			}
			cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
			re, err := s.Judge.Classify(cctx, &aijudge.ClassifyInput{
				Ko:            ko,
				SourceDomains: sd,
				Wikidata:      &aijudge.ClassifyWikidata{QID: ent.QID, Label: label, Description: desc},
			})
			cancel()
			if err == nil && re != nil && re.EntityType != "" && re.EntityType != "term" && re.EntityType != "unknown" {
				return s.promoteRescued(ctx, id, ko, re, rep, mu, "wikidata:"+ent.QID)
			}
		}
	}

	// typed 큐 힌트 = 소비자가 구체 타입(person/group/song_album/movie/…)으로 명시
	// 요청한 적 있음. 2단계 웹검색의 비용 게이트이자, 아래 quarantine 보루의 조건.
	typed := s.hasTypedQueueHint(ctx, ko)

	// 2단계: 웹검색(Google News) 문맥으로 재분류 — Wikidata 부재 보완. typed 큐 힌트가
	// 있는 후보만 검색해, 잡토큰에 매번 검색+LLM 을 돌리는 비용을 막는다.
	if s.Config.WebSearchVeto && typed {
		hits := kdbroot.SearchNewsContext(ctx, ko, 6)
		if len(hits) > 0 {
			cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
			re, err := s.Judge.Classify(cctx, &aijudge.ClassifyInput{
				Ko:            ko,
				SourceDomains: sd,
				SearchHits:    hits,
			})
			cancel()
			if err == nil && re != nil && !re.NeedsSearch &&
				re.EntityType != "" && re.EntityType != "term" && re.EntityType != "unknown" {
				return s.promoteRescued(ctx, id, ko, re, rep, mu, "websearch")
			}
		}
	}

	// 최종 보루(2026-06-20): 외부증거로 구제 못 했지만 typed 큐 힌트가 있으면
	// 하드 reject(영구 silent loss) 대신 candidate 로 보류(quarantine)한다. candidate
	// 는 소비자 API(lookup/match 기본 active)에 노출되지 않으므로 안전하고, 운영자
	// inbox 에는 남아 검토 가능하다(honest visibility). 웹검색 veto 가 Google News
	// 차단으로 무력한 현 상황에서 aespa Savage 같은 실재 작품이 통째 사라지는 것을 막음.
	// quarantine 된 후보는 재처리 selection 에서 제외돼 거부 루프를 돌지 않는다.
	if typed {
		return s.quarantineTyped(ctx, id, ko, rep, mu)
	}
	return false
}

// quarantineTyped — typed 큐 힌트 후보를 candidate 로 보류 표시. notes 에 멱등 마커
// [kdb:q:typed] 를 달아 모든 candidate-처리 step SELECT 에서 제외되게 한다.
// 실제로 보류된 경우(candidate 행 1건 갱신)만 true — 호출부(tryRescue)가 하드
// reject 를 건너뛴다. candidate 가 아니면 false(정상 경로 진행, 카운터 미증가).
func (s *Sweeper) quarantineTyped(ctx context.Context, id uuid.UUID, ko string, rep *Report, mu *sync.Mutex) bool {
	tag, _ := s.Pool.Exec(ctx, `
UPDATE kwave_entities
   SET notes = CASE WHEN COALESCE(notes,'') LIKE '%[kdb:q:typed]%' THEN notes
               ELSE COALESCE(NULLIF(notes,'') || ' · ','') ||
                    '[kdb:q:typed] 소비자 typed 요청·외부증거 미확보 — 운영자 검토 대기' END,
       updated_at = now()
 WHERE id = $1 AND status='candidate'`, id)
	if tag.RowsAffected() != 1 {
		// 후보가 candidate 가 아니면(예: active+unknown 경로) 보류가 적용되지 않는다.
		// false 를 돌려 호출부가 정상 경로(하드 reject — active 면 그 역시 no-op)를
		// 타게 하고, 카운터도 올리지 않는다(정직한 Report).
		return false
	}
	if mu != nil {
		mu.Lock()
	}
	rep.Quarantined++
	if mu != nil {
		mu.Unlock()
	}
	log.Printf("kdb.gatekeeper: quarantine(typed) — %q (외부증거 미확보, 운영자 검토 대기)", ko)
	return true
}

// hasTypedQueueHint — ko 가 research 큐에 구체 타입(person/group/show/…) 힌트로
// 적재된 적이 있는지. 2단계 웹검색 veto 의 비용 게이트(명명된 엔티티만 검색).
func (s *Sweeper) hasTypedQueueHint(ctx context.Context, ko string) bool {
	var t string
	err := s.Pool.QueryRow(ctx, `
SELECT requested_entity_type::text
FROM kwave_entity_research_queue
WHERE entity_ko = $1
  AND requested_entity_type IS NOT NULL
  AND requested_entity_type::text NOT IN ('unknown','term')
LIMIT 1`, ko).Scan(&t)
	return err == nil && t != ""
}

// promoteRescued — veto 로 구제된 entity 를 active 승격(person 이면 인물DB sync) + 카운터.
// operator_locked=false 조건으로 candidate(드레인) · active-미분류 양쪽 모두 커버.
func (s *Sweeper) promoteRescued(ctx context.Context, id uuid.UUID, ko string, re *aijudge.ClassifyResult, rep *Report, mu *sync.Mutex, via string) bool {
	if _, err := s.Pool.Exec(ctx, `
UPDATE kwave_entities
   SET status='active', entity_type = $2::kwave_entity_type,
       confidence = GREATEST(confidence, 0.600::numeric),
       last_verified_at = now(), updated_at = now(),
       notes = left(COALESCE(NULLIF(notes,'') || ' · ','') || 'veto 구제: 일반어 오판 — ' || $3, 1000)
 WHERE id = $1 AND operator_locked = false`, id, re.EntityType, via); err != nil {
		return false
	}
	if re.EntityType == "person" {
		_, _ = s.Pool.Exec(ctx, `
INSERT INTO kwave_persons (name_ko, primary_role, confidence, last_verified_at, created_at)
VALUES ($1, COALESCE(NULLIF($2,'')::person_role, 'other'::person_role), 0.500, now(), now())
ON CONFLICT (name_ko) DO NOTHING`, ko, derefStr(re.PrimaryRole))
		_, _ = s.Pool.Exec(ctx, `
INSERT INTO kwave_entity_person_details (entity_id, primary_role)
VALUES ($1, COALESCE(NULLIF($2,'')::person_role, 'other'::person_role))
ON CONFLICT (entity_id) DO NOTHING`, id, derefStr(re.PrimaryRole))
		s.persistPersonSignals(ctx, id, re)
		s.markHomonymsIfConflict(ctx, id, ko, re)
	}
	if mu != nil {
		mu.Lock()
	}
	rep.Promoted++
	rep.WikidataRescued++
	if re.EntityType == "person" {
		rep.PersonsAdded++
	}
	if mu != nil {
		mu.Unlock()
	}
	log.Printf("kdb.veto: rescued ko=%q type=%s via=%s (gpt 일반어 오판)", ko, re.EntityType, via)
	return true
}

// stepReviewCandidates — status='candidate' 모든 row 를 batch 처리.
//   - gpt classify → entity_type='term' + conf ≤ 0.40 이면 자동 reject (운영자 큐 X).
//   - 그 외 candidate 그대로 유지 (≥ 2 매체 시 stepPromoteConsensus 에서 처리).
//
// "건강하게", "세계일주" 같은 일상어 / 동음이의어 일반어가 inbox 에 누적되는 것 방지.
func (s *Sweeper) stepReviewCandidates(ctx context.Context, rep *Report) {
	rows, err := s.Pool.Query(ctx, `
SELECT id, canonical_ko, source_domains
FROM kwave_entities WHERE status='candidate' AND operator_locked = false
  AND COALESCE(notes,'') NOT LIKE '%[kdb:q:typed]%'
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
			if s.tryRescue(ctx, c.ID, c.Ko, c.SD, rep, nil) {
				continue
			}
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

		// needs_search → Google News 문맥으로 2nd pass 재시도.
		if res.NeedsSearch {
			web := kdbroot.SearchNewsContext(ctx, c.Ko, 6)
			if len(web) > 0 {
				in2 := &aijudge.ClassifyInput{Ko: c.Ko, SourceDomains: c.SD, SearchHits: web}
				callCtx2, cancel2 := context.WithTimeout(ctx, 90*time.Second)
				res2, err2 := s.Judge.Classify(callCtx2, in2)
				cancel2()
				if err2 == nil && res2 != nil {
					res = res2
				}
			}
		}
		realType2 := res.EntityType != "" && res.EntityType != "unknown" && res.EntityType != "term"
		if realType2 && !res.NeedsSearch && res.Confidence >= s.Config.minConfFor(len(c.SD)) {
			conf := 0.70
			if len(c.SD) >= 2 {
				conf = 0.75
			}
			if _, err := s.Pool.Exec(ctx, `
UPDATE kwave_entities
   SET status='active', entity_type = $2::kwave_entity_type,
       confidence = GREATEST(confidence, $3::numeric), updated_at = now()
 WHERE id = $1 AND status='candidate'`, c.ID, res.EntityType, conf); err == nil {
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
				rep.Promoted++
				continue
			}
		}
		// 여전히 미확정 — rotate
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
			if s.tryRescue(ctx, c.ID, c.Ko, c.SourceDomains, rep, nil) {
				continue
			}
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

// ResolveOnDemand — on-demand(lookup-miss) candidate 적체 해소.
// 클라이언트가 일부러 질의했으나 매체 0건이라 ≥2 합의 게이트(stepPromoteConsensus)에
// 영원히 막히던 candidate 를, 검색증강 enrich 로 외부 검증한다:
//   - enrich 후 외부참조(Wikidata 등) 또는 외국어 표기 1개 이상 확보 → 실존 확인 →
//     즉시 active 승급(매체 합의 불필요).
//   - 무검증(외부 근거 전무) → last_enriched_at 마킹 + 사유 노트(재시도 루프 방지).
//     candidate 로 남아 운영자 카드에 계속 노출(숨기지 않음).
//
// 반환: (promoted, processed).
func (s *Sweeper) ResolveOnDemand(ctx context.Context, limit int) (promoted, processed int) {
	if s.Orch == nil {
		return 0, 0
	}
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.Pool.Query(ctx, `
SELECT id, canonical_ko, entity_type::text FROM kwave_entities
WHERE status='candidate' AND operator_locked = false AND entity_type <> 'unknown'
  AND notes LIKE '%on-demand%'
  AND COALESCE(notes,'') NOT LIKE '%[kdb:q:typed]%'
  AND (last_enriched_at IS NULL OR last_enriched_at < now() - interval '7 days')
ORDER BY last_enriched_at ASC NULLS FIRST, created_at ASC LIMIT $1`, limit)
	if err != nil {
		log.Printf("kdb.ondemand: select: %v", err)
		return 0, 0
	}
	type cand struct {
		ID   uuid.UUID
		Ko   string
		Type string
	}
	var cs []cand
	for rows.Next() {
		var c cand
		if rows.Scan(&c.ID, &c.Ko, &c.Type) == nil {
			cs = append(cs, c)
		}
	}
	rows.Close()

	for _, c := range cs {
		processed++
		ec, cancel := context.WithTimeout(ctx, 120*time.Second)
		_, _ = s.Orch.Enrich(ec, c.ID)
		cancel()

		var verified bool
		_ = s.Pool.QueryRow(ctx, `
SELECT EXISTS(SELECT 1 FROM kwave_entity_external_refs r WHERE r.entity_id=$1)
    OR COALESCE(canonical_en,'')<>''  OR COALESCE(canonical_ja,'')<>''
    OR COALESCE(canonical_zh,'')<>''  OR COALESCE(canonical_es,'')<>''
    OR COALESCE(canonical_vi,'')<>''  OR COALESCE(canonical_id,'')<>''
    OR COALESCE(canonical_pt_br,'')<>'' OR COALESCE(canonical_zh_hant,'')<>''
  FROM kwave_entities WHERE id=$1`, c.ID).Scan(&verified)

		if verified {
			tag, err := s.Pool.Exec(ctx, `
UPDATE kwave_entities
   SET status='active', confidence = GREATEST(confidence, 0.60),
       last_enriched_at = COALESCE(last_enriched_at, now()),
       notes = COALESCE(NULLIF(notes,'') || ' · ','') || 'autopilot: on-demand 검증 승급(외부근거 확보)',
       updated_at = now()
 WHERE id=$1 AND status='candidate'`, c.ID)
			if err == nil && tag.RowsAffected() == 1 {
				if c.Type == "person" {
					_, _ = s.Pool.Exec(ctx, `
INSERT INTO kwave_persons (name_ko, primary_role, confidence, last_verified_at, created_at)
VALUES ($1, 'other'::person_role, 0.500, now(), now()) ON CONFLICT (name_ko) DO NOTHING`, c.Ko)
					_, _ = s.Pool.Exec(ctx, `
INSERT INTO kwave_entity_person_details (entity_id, primary_role)
VALUES ($1, 'other'::person_role) ON CONFLICT (entity_id) DO NOTHING`, c.ID)
				}
				promoted++
			}
			continue
		}
		// 무검증 — 재시도 횟수 확인: 3회 이상이면 자동 기각(21일+ 외부근거 전무).
		var retryCount int
		_ = s.Pool.QueryRow(ctx, `
SELECT COALESCE(array_length(regexp_split_to_array(COALESCE(notes,''), '무검증'), 1) - 1, 0)
FROM kwave_entities WHERE id=$1`, c.ID).Scan(&retryCount)

		if retryCount >= 3 {
			_, _ = s.Pool.Exec(ctx, `
UPDATE kwave_entities
   SET status='rejected', confidence=0.000,
       notes = COALESCE(NULLIF(notes,'') || ' · ','') || 'autopilot: on-demand 3회 무검증 → 자동 기각',
       updated_at = now()
 WHERE id=$1 AND status='candidate'`, c.ID)
			continue
		}
		// 재시도 여지 있음 — 마킹만 하고 다음 7일 후 재처리.
		_, _ = s.Pool.Exec(ctx, `
UPDATE kwave_entities
   SET last_enriched_at = now(),
       notes = COALESCE(NULLIF(notes,'') || ' · ','') || 'autopilot: on-demand enrich 무검증(외부근거 없음) — 운영자 검토',
       updated_at = now()
 WHERE id=$1 AND status='candidate'`, c.ID)
	}
	if processed > 0 {
		log.Printf("kdb.ondemand: processed=%d promoted=%d", processed, promoted)
	}
	return promoted, processed
}

// --- step 2: candidate ≥ 2 매체 → 자동 promote + enrich --------------------

func (s *Sweeper) stepPromoteConsensus(ctx context.Context, rep *Report) {
	rows, err := s.Pool.Query(ctx, `
SELECT id, canonical_ko, COALESCE(array_length(source_domains,1),0)
FROM kwave_entities
WHERE status='candidate' AND COALESCE(array_length(source_domains,1),0) >= 2
  AND COALESCE(notes,'') NOT LIKE '%[kdb:q:typed]%'
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
			if s.tryRescue(ctx, cnd.ID, cnd.Ko, in.SourceDomains, rep, nil) {
				continue
			}
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
-- operator_locked 포함(enrich 는 empty-only — 운영자 값 보존, 빈 칸만 채움).
SELECT id FROM kwave_entities
WHERE status='active' AND confidence >= 0.5
  AND entity_type <> 'unknown'
  AND (canonical_en IS NULL OR canonical_en=''
    OR canonical_ja IS NULL OR canonical_ja=''
    OR canonical_vi IS NULL OR canonical_vi=''
    OR canonical_es IS NULL OR canonical_es=''
    OR canonical_id IS NULL OR canonical_id=''
    OR canonical_pt_br IS NULL OR canonical_pt_br=''
    OR canonical_zh_hant IS NULL OR canonical_zh_hant=''
    OR canonical_zh IS NULL OR canonical_zh='')
ORDER BY (CASE WHEN updated_at > now()-interval '2 hours' THEN 0 ELSE 1 END) ASC,
         confidence DESC, updated_at ASC
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
	// 병렬 enrich — Orchestrator.Enrich 는 entity 별 독립(공유 가변상태 없음)이라
	// 동시 호출 안전. codex(L4)는 전역 codexSem 이 동시성 상한을 잡고, L2/L3(HTTP)는
	// 그와 별개로 병렬 진행한다. 동시수는 KDB_CODEX_CONCURRENCY 와 맞춘다.
	conc := envInt("KDB_CODEX_CONCURRENCY", 4)
	var (
		mu  sync.Mutex
		sem = make(chan struct{}, conc)
		wg  sync.WaitGroup
	)
	for _, id := range ids {
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
		go func(id uuid.UUID) {
			defer wg.Done()
			defer func() { <-sem }()
			if ctx.Err() != nil {
				return
			}
			callCtx, cancel := context.WithTimeout(ctx, 150*time.Second)
			_, _ = s.Orch.Enrich(callCtx, id)
			cancel()
			mu.Lock()
			rep.Enriched++
			mu.Unlock()
		}(id)
	}
	wg.Wait()
}

// --- helpers --------------------------------------------------------------

func envBool(key string, fallback bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return fallback
}

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

// clearResolvedDisambig — 해소된 needs_disambig 플래그를 매 cycle 클리어한다.
// Disambiguator 가 distinct 라벨을 주거나(disambig 설정) merge 로 동명이인이 사라져
// (같은 ko active < 2) 충돌이 사실상 해소됐는데도 needs_disambig=true 가 남아
// 영구 stuck 되던 누수 차단 — "충돌이 안 줄어든다"의 근본 원인.
func (s *Sweeper) clearResolvedDisambig(ctx context.Context) {
	tag, err := s.Pool.Exec(ctx, `
UPDATE kwave_entities e SET needs_disambig=false, updated_at=now()
 WHERE e.needs_disambig=true AND e.status='active'
   AND (COALESCE(e.disambig,'')<>''
     OR (SELECT count(*) FROM kwave_entities x WHERE x.status='active' AND x.canonical_ko=e.canonical_ko) < 2)`)
	if err == nil && tag.RowsAffected() > 0 {
		log.Printf("kdb.autopilot: cleared %d resolved needs_disambig", tag.RowsAffected())
	}
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
			// 이미 disambig 라벨이 부여된(Disambiguator 가 distinct 처리한) 쌍은
			// 재플래그하지 않는다 — needs_disambig 를 되살려 무한 정체시키는 것 방지.
			_, _ = s.Pool.Exec(ctx,
				`UPDATE kwave_entities SET needs_disambig = true, updated_at = now()
				   WHERE id = $1 AND COALESCE(disambig,'')='' `, other)
		}
	}
	if conflict {
		_, _ = s.Pool.Exec(ctx,
			`UPDATE kwave_entities SET needs_disambig = true, updated_at = now()
			   WHERE id = $1 AND COALESCE(disambig,'')='' `, entityID)
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

// DrainQuality — 품질검토 적체 해소. bumpable = 저신뢰(conf<0.70)지만 Wikidata 로 검증된
// (canonical_*_source 가 wikidata 또는 external_ref 보유) active entity. Wikidata 가 해당
// 항목을 갖고 있다는 것 자체가 검증 신호(enrich 시 H1 이름매칭 가드 통과분)이므로
// confidence 를 검증 tier(0.75)로 승급해 백로그를 푼다.
//
// ★기존 버그: stepQualityReview 가 bump 조건을 'wikidata external_ref'로만 봐서, 대부분
// (wikidata-label 소스만 있고 ref 없음)이 영원히 안 올라가 품질카드가 안 빠졌다.
// 여기선 bumpable 정의와 동일 조건으로 승급한다. 단 dataqa 가 현재 오염(미revert)으로
// 표시한 entity 는 제외(오염 승급 방지). 반환: (bumped, processed).
func (s *Sweeper) DrainQuality(ctx context.Context, limit int) (bumped, processed int) {
	if limit <= 0 {
		limit = 200
	}
	tag, err := s.Pool.Exec(ctx, `
UPDATE kwave_entities SET confidence = 0.750, updated_at = now()
 WHERE id IN (
   SELECT id FROM kwave_entities e
    WHERE e.status='active' AND e.operator_locked = false
      AND e.confidence < 0.70 AND e.entity_type <> 'unknown'
      AND (e.canonical_en_source ILIKE '%wikidata%' OR e.canonical_ja_source ILIKE '%wikidata%'
        OR e.canonical_vi_source ILIKE '%wikidata%' OR e.canonical_es_source ILIKE '%wikidata%'
        OR e.canonical_id_source ILIKE '%wikidata%' OR e.canonical_pt_br_source ILIKE '%wikidata%'
        OR e.canonical_zh_source ILIKE '%wikidata%' OR e.canonical_zh_hant_source ILIKE '%wikidata%'
        OR EXISTS(SELECT 1 FROM kwave_entity_external_refs r WHERE r.entity_id=e.id AND r.provider='wikidata'))
      AND NOT EXISTS(SELECT 1 FROM kwave_kdb_dataqa_log d
                     WHERE d.entity_id=e.id AND d.verdict='contaminated' AND d.reverted_at IS NULL)
    ORDER BY e.confidence ASC LIMIT $1)`, limit)
	if err != nil {
		log.Printf("kdb.drainquality: %v", err)
		return 0, 0
	}
	bumped = int(tag.RowsAffected())
	processed = bumped
	log.Printf("kdb.drainquality: bumped=%d (wikidata-verified 저신뢰 → 0.75)", bumped)

	// 2차 tier(2026-06-20): wikidata 는 아니지만 *권위 외부DB 교차검증*(tmdb/musicbrainz/
	// kofic/kmdb external_ref)이 있는 저신뢰 active 만 0.70 으로 소폭 승급(wikidata 0.75
	// 한 단계 아래 — 출처 신뢰도 정직 반영). RSS 매체 수(source_domains)는 약한 신호라
	// 제외 — 포함하면 llm-only 표기 행이 소비자 min_confidence:0.7 게이트를 통과해 검증된
	// 것처럼 노출된다. wikidata 보유 행도 제외(tier1 0.75 소관 — LIMIT overflow 시
	// under-tiering 방지). 권위 증거 없는 진짜 검증불가 저신뢰는 손대지 않고 대시보드 [9]로
	// 가시화한다. dataqa 오염(미revert) 표시분 제외.
	tag2, err2 := s.Pool.Exec(ctx, `
UPDATE kwave_entities SET confidence = 0.700, updated_at = now()
 WHERE id IN (
   SELECT id FROM kwave_entities e
    WHERE e.status='active' AND e.operator_locked = false
      AND e.confidence < 0.70 AND e.entity_type <> 'unknown'
      AND EXISTS(SELECT 1 FROM kwave_entity_external_refs r
                  WHERE r.entity_id=e.id AND r.provider IN ('tmdb','musicbrainz','kofic','kmdb'))
      AND NOT (e.canonical_en_source ILIKE '%wikidata%' OR e.canonical_ja_source ILIKE '%wikidata%'
        OR e.canonical_vi_source ILIKE '%wikidata%' OR e.canonical_es_source ILIKE '%wikidata%'
        OR e.canonical_id_source ILIKE '%wikidata%' OR e.canonical_pt_br_source ILIKE '%wikidata%'
        OR e.canonical_zh_source ILIKE '%wikidata%' OR e.canonical_zh_hant_source ILIKE '%wikidata%'
        OR EXISTS(SELECT 1 FROM kwave_entity_external_refs r2 WHERE r2.entity_id=e.id AND r2.provider='wikidata'))
      AND NOT EXISTS(SELECT 1 FROM kwave_kdb_dataqa_log d
                     WHERE d.entity_id=e.id AND d.verdict='contaminated' AND d.reverted_at IS NULL)
    ORDER BY e.confidence ASC LIMIT $1)`, limit)
	if err2 != nil {
		log.Printf("kdb.drainquality(tier2): %v", err2)
	} else if n := int(tag2.RowsAffected()); n > 0 {
		bumped += n
		processed += n
		log.Printf("kdb.drainquality: tier2 bumped=%d (권위 외부DB ref 저신뢰 → 0.70)", n)
	}
	return bumped, processed
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
		// Wikidata 검증(external_ref 또는 wikidata-소스 canonical) + 미오염 → confidence 0.75.
		// (기존엔 external_ref 만 봐서 wikidata-label 소스만 보유한 대다수가 영영 안 올라감 = 품질적체 버그.)
		var verified bool
		_ = s.Pool.QueryRow(ctx, `
SELECT (EXISTS(SELECT 1 FROM kwave_entity_external_refs WHERE entity_id=$1 AND provider='wikidata')
    OR canonical_en_source ILIKE '%wikidata%' OR canonical_ja_source ILIKE '%wikidata%'
    OR canonical_vi_source ILIKE '%wikidata%' OR canonical_es_source ILIKE '%wikidata%'
    OR canonical_id_source ILIKE '%wikidata%' OR canonical_pt_br_source ILIKE '%wikidata%'
    OR canonical_zh_source ILIKE '%wikidata%' OR canonical_zh_hant_source ILIKE '%wikidata%')
   AND NOT EXISTS(SELECT 1 FROM kwave_kdb_dataqa_log d
                  WHERE d.entity_id=$1 AND d.verdict='contaminated' AND d.reverted_at IS NULL)
  FROM kwave_entities WHERE id=$1`, id).Scan(&verified)
		if verified {
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

// --- step: WF-2 canonical_en 중복 자동 해소 ----------------------------------

// stepDeduplicateCanonicalEn — 같은 canonical_en + 같은 entity_type active 충돌을
// *가시화*한다(2026-06-20 재설계: 비파괴·DB 무변경, 요약 로그 + 대시보드).
//
// 왜 자동 병합/은폐/플래그를 안 하나:
//   - 같은 영문표기 = 같은 엔티티가 아니다(샘 김 셰프/가수 동명이인).
//   - canonical_ko 일치도 증거가 못 된다 — 서로 다른 실존 인물이 동일 canonical_ko 를
//     갖고 disambig 로만 구분되도록 설계됨(migration 0060).
//   - 외부 ref(QID/TMDb) 공유조차 mislink(동명이인 QID 오링크)면 다른 인물을 합친다.
//   - needs_disambig 컬럼은 canonical_ko 동명이인 전용 lifecycle 이다 — clearResolvedDisambig
//     가 같은 cycle 에 canonical_ko 그룹<2 면 즉시 false 로 되돌리므로 canonical_en
//     충돌(ko 상이)에 쓰면 무효화·지표 진동을 일으킨다.
//
// 따라서 WF-2 는 *아무것도 변경하지 않고* 충돌 그룹 수만 로그로 남긴다. 소비자에겐
// match 응답의 locale_ambiguous=true(api.go)로 이미 모호성을 통지하고, 운영자는
// 대시보드 [8](kdb-dashboard.sh)에서 충돌 상세를 검토해 정식 해소한다. 정식 distinct/
// 병합은 evidence-gated disambiguator·운영자 몫(과거 disambig='auto-dedup' 폐기).
func (s *Sweeper) stepDeduplicateCanonicalEn(ctx context.Context, rep *Report) {
	var groups int
	err := s.Pool.QueryRow(ctx, `
SELECT count(*) FROM (
  SELECT 1 FROM kwave_entities
   WHERE status='active' AND canonical_en IS NOT NULL AND canonical_en <> ''
     AND disambig IS NULL AND operator_locked = false
   GROUP BY lower(canonical_en), entity_type
  HAVING count(*) > 1
) t`).Scan(&groups)
	if err != nil {
		log.Printf("kdb.autopilot: WF-2 collision scan 실패: %v", err) // 조용한 실패 방지
		return
	}
	if groups > 0 {
		log.Printf("kdb.autopilot: WF-2 — 동일 canonical_en+type 충돌 그룹 %d개 (소비자엔 match locale_ambiguous 통지; 운영자 대시보드 [8] 검토)", groups)
	}
}

// --- step: person agency/birth 미입력 보완 -----------------------------------

// stepFillPersonDetails — person entity 중 agency 가 비어 있는 것을
// local RSS 문맥 + Gemma 로 보완. FillPerson 이 wikidata 없이 실행됐거나
// wikidata 에 소속사 정보가 없는 경우를 주기 재시도로 채운다.
func (s *Sweeper) stepFillPersonDetails(ctx context.Context, rep *Report) {
	rows, err := s.Pool.Query(ctx, `
SELECT e.id, e.canonical_ko, d.primary_role
  FROM kwave_entities e
  JOIN kwave_entity_person_details d ON d.entity_id = e.id
 WHERE e.status='active' AND e.operator_locked=false
   AND (d.agency IS NULL OR d.agency='')
 ORDER BY e.confidence DESC, e.updated_at ASC
 LIMIT 20`)
	if err != nil {
		return
	}
	type row struct {
		ID          uuid.UUID
		Ko          string
		PrimaryRole string
	}
	var items []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.ID, &r.Ko, &r.PrimaryRole); err == nil {
			items = append(items, r)
		}
	}
	rows.Close()

	agencySchema := []byte(`{"type":"object","required":["agency"],"properties":{"agency":{"type":"string"}}}`)
	for _, it := range items {
		// local RSS 문맥: 소속사 언급 기사 찾기
		hits := s.localNewsContext(ctx, it.Ko, 8)
		if len(hits) == 0 {
			continue // 문맥 없으면 skip — Gemma 추측 금지
		}
		// Gemma 에게 agency 추출 요청 (간단한 단일 필드 추출)
		prompt := "아래 뉴스 기사 제목들에서 \"" + it.Ko + "\" 의 소속 연예 기획사명을 추출해. " +
			"확실하지 않으면 빈 문자열 출력. 출력 형식: JSON {\"agency\": \"기획사명 또는 \"\"\"}\n\n기사:\n" +
			strings.Join(hits, "\n")
		callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		raw, err := s.Judge.Runner.WithProvider(codexcli.RoleProvider("FILL", "gemma")).Run(callCtx, prompt, agencySchema)
		cancel()
		if err != nil || raw == nil {
			continue
		}
		var out struct {
			Agency string `json:"agency"`
		}
		if err := json.Unmarshal(raw, &out); err != nil || strings.TrimSpace(out.Agency) == "" {
			continue
		}
		_, _ = s.Pool.Exec(ctx, `
UPDATE kwave_entity_person_details SET agency=$2 WHERE entity_id=$1 AND (agency IS NULL OR agency='')`,
			it.ID, strings.TrimSpace(out.Agency))
	}
}

// --- step: on-demand 후보 자동 드레인 ----------------------------------------

// stepDrainOnDemandCandidates — lookup/prepare miss 로 생성된 on-demand 후보 중
// 7일 이상 경과한 항목을 완화된 신뢰도 임계(0.65)로 최종 판단.
//
// 배경: 클라이언트가 조회한 이름 자체가 외부 신호. 단일 매체 임계(0.85)는 너무 엄격해
// 소규모 K-엔터 인물이 영구 candidate 로 고착. 7일이 지나도 RSS 에 잡히지 않으면
// LLM 최종 판단으로 승급 or 기각 — 운영자 개입 불필요.
func (s *Sweeper) stepDrainOnDemandCandidates(ctx context.Context, rep *Report) {
	rows, err := s.Pool.Query(ctx, `
SELECT id, canonical_ko, COALESCE(source_domains, '{}'), entity_type::text
  FROM kwave_entities
 WHERE status='candidate' AND operator_locked = false
   AND (needs_disambig = false OR needs_disambig IS NULL)
   AND notes LIKE '%on-demand%'
   AND COALESCE(notes,'') NOT LIKE '%[kdb:q:typed]%'
   AND created_at < now() - interval '7 days'
 ORDER BY created_at ASC
 LIMIT 30`)
	if err != nil {
		log.Printf("kdb.autopilot: stepDrainOnDemand select: %v", err)
		return
	}
	defer rows.Close()
	type cand struct {
		ID   uuid.UUID
		Ko   string
		SD   []string
		Type string
	}
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.ID, &c.Ko, &c.SD, &c.Type); err == nil {
			cands = append(cands, c)
		}
	}

	for _, c := range cands {
		callCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		res, err := s.Judge.Classify(callCtx, &aijudge.ClassifyInput{Ko: c.Ko, SourceDomains: c.SD})
		cancel()
		if err != nil || res == nil {
			continue
		}

		realType := res.EntityType != "" && res.EntityType != "unknown" && res.EntityType != "term"

		// 0.65 이상 → 승급 (클라 조회 자체가 외부 신호이므로 단일매체 0.85 완화)
		if realType && res.Confidence >= 0.65 {
			et := res.EntityType
			if c.Type != "" && c.Type != "unknown" {
				et = c.Type // 이미 올바른 type 이면 유지
			}
			if _, err := s.Pool.Exec(ctx, `
UPDATE kwave_entities
   SET status='active', entity_type=$2::kwave_entity_type,
       confidence=GREATEST(confidence, 0.55::numeric),
       notes=COALESCE(NULLIF(notes,'') || ' · ','') || 'autopilot: on-demand 7일+ LLM 승급',
       updated_at=now()
 WHERE id=$1 AND status='candidate'`, c.ID, et); err == nil {
				rep.Promoted++
				if et == "person" {
					_, _ = s.Pool.Exec(ctx, `
INSERT INTO kwave_persons (name_ko, primary_role, confidence, last_verified_at, created_at)
VALUES ($1, COALESCE(NULLIF($2,'')::person_role,'other'::person_role), 0.500, now(), now())
ON CONFLICT (name_ko) DO NOTHING`, c.Ko, derefStr(res.PrimaryRole))
					_, _ = s.Pool.Exec(ctx, `
INSERT INTO kwave_entity_person_details (entity_id, primary_role)
VALUES ($1, COALESCE(NULLIF($2,'')::person_role,'other'::person_role))
ON CONFLICT (entity_id) DO NOTHING`, c.ID, derefStr(res.PrimaryRole))
					s.persistPersonSignals(ctx, c.ID, res)
				}
			}
			continue
		}

		// term 또는 0.50 미만 → 7일 기다렸으나 근거 없음 → 기각.
		// 단, 하드 reject 전 tryRescue 게이트: 소비자가 typed 로 요청한 on-demand 후보는
		// Wikidata/웹 재검증 후에도 미확보면 quarantine(보류)로 돌려 silent loss 를 막는다
		// (gatekeeper term-reject 경로와 동일 정책). typed 힌트 없으면 정상 기각.
		if !realType || res.Confidence < 0.50 {
			if s.tryRescue(ctx, c.ID, c.Ko, c.SD, rep, nil) {
				continue
			}
			_, _ = s.Pool.Exec(ctx, `
UPDATE kwave_entities
   SET status='rejected', confidence=0.000,
       notes=COALESCE(NULLIF(notes,'') || ' · ','') || 'autopilot: on-demand 7일+ 무근거 기각 — ' || $2,
       updated_at=now()
 WHERE id=$1 AND status='candidate'`, c.ID, strings.TrimSpace(res.Reason))
			rep.NonEntityReject++
			continue
		}

		// 0.50~0.64 — 아직 판단 유보, 14일 후 다시 시도
		_, _ = s.Pool.Exec(ctx, `UPDATE kwave_entities SET updated_at=now() WHERE id=$1`, c.ID)
	}
}

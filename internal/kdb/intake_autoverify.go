// intake_autoverify.go — 고유명사 게이트 review 보류분 자동 검증 드레인 (2026-07-13).
//
// ★오너 계약: 매체(소비자)는 키워드만 보낸다. "없으면 준비, 있으면 보내주기".
// 07-11 게이트는 비용 낭비(근거 없는 후보 대량 provider 호출)를 막았지만, 근거 공급을
// 소비자 payload(type·context·source_url)에 요구해 신규 키워드 대부분이 review 에
// 보류됐다(승인 처리 주체 없음 = 사실상 미답변). 이 드레인은 그 근거를 소비자 대신
// KDB 가 직접 수집한다:
//
//	review row(요청빈도순) → Naver encyc/news 검색으로 (type, 문맥, 출처URL) 확보
//	→ 기존 gatekeeper.DecideIntake 규칙을 "그대로" 재평가 (규칙 완화 없음)
//	→ pass 면 precheck_status='approved'(auto_evidence)로 승격 + status='pending'
//	→ research worker 가 즉시 발굴·채움 진행 (worker 재평가는 approved 를 인정)
//
// 두 레인으로 돈다(오너 07-13: "제대로 된 키워드는 유입 즉시 심사"):
//   - fresh 레인: 소비자 유입/재요청 순간 row id 로 즉시 검증(주기·백로그 순서 무대기).
//     백오프 중이어도 소비자가 다시 찾으면 재검증한다(exhausted 만 운영자 몫으로 제외).
//   - backlog 레인: 2분 tick 마다 적체를 요청빈도순으로 소진. 일일 예산에서 fresh
//     예약분(KDB_INTAKE_AUTOVERIFY_FRESH_RESERVE)을 남겨두고 멈춰 신규를 굶기지 않는다.
//
// 게이트 철학 유지: 판정 주체는 여전히 DecideIntake 하나다. 이 드레인이 하는 일은
// "증거 수집"뿐이며, 증거로도 못 넘는 것(일반어 비작품 type, 형태 충돌, 동명이인
// 충돌)은 그대로 review 에 남아 운영자 몫이다. Naver 쿼터(1,000/일)는 일일 예산으로
// 보호한다(KDB_INTAKE_AUTOVERIFY_DAILY_CALLS).
package kdb

import (
	"context"
	"errors"
	"html"
	"log"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb/agents/gatekeeper"
	"github.com/rickyjoo73/kdb/internal/kdb/naver"
	"github.com/rickyjoo73/kdb/internal/kdb/websearch"
)

// IntakeSearcher — naver.Client 의 최소 표면(테스트 fake 주입용).
type IntakeSearcher interface {
	Search(ctx context.Context, kind, query string, display int) (*naver.SearchResult, error)
}

// autoVerifyReasons — 이 드레인이 증거 수집으로 풀 수 있는 review 사유.
// 정체성/타입 충돌(동명이인)은 증거가 아니라 판단이 필요하므로 제외(운영자 몫).
var autoVerifyReasons = []string{
	"missing_or_unsupported_type",
	"missing_exact_context",
	"missing_type_context_cue",
	"missing_source_evidence",
	"ambiguous_common_for_type",
}

// autoVerifyTypePriority — type 힌트가 없을 때 증거를 대조할 순서.
// 소비자 요청의 대다수(인물·그룹·작품)를 앞에 둔다.
var autoVerifyTypePriority = []string{
	"person", "group", "drama", "movie", "show", "song_album",
	"agency", "channel_outlet", "event_tour", "brand_place", "character",
}

type intakeEvidence struct {
	Type      string
	Context   string
	SourceURL string
	Reason    string // auto_evidence_encyc | auto_evidence_news
	Decision  gatekeeper.IntakeDecision
}

type autoVerifyItem struct {
	id           uuid.UUID
	ko           string
	reqType      string
	normKey      string
	misses       int
	siblingTypes []string
}

// IntakeAutoVerifier — review 보류 큐 자동 검증 드레인. Tick 은 single-flight.
type IntakeAutoVerifier struct {
	Pool *pgxpool.Pool
	// NewSearcher — tick 마다 자격증명(admin settings 우선)을 다시 읽는다.
	NewSearcher func(ctx context.Context) (IntakeSearcher, error)
	// Kick — 승격 발생 시 research worker 를 즉시 깨운다(옵션).
	Kick func()
	// Translator — 음차 제목류의 번역 원형으로 news 재검색(3차 증거, 오너 승인 07-15).
	// nil = 비활성(KDB_GTRANSLATE_KEY 미설정) — 기존 동작 불변.
	Translator *GTranslator

	running atomic.Bool
	// 일일 Naver 호출 예산(재시작 시 리셋 — 예산은 보호 상한이지 정밀 회계가 아님).
	// backlog tick 과 fresh 레인이 병행 소비하므로 mutex 로 보호.
	mu         sync.Mutex
	budgetDay  string
	budgetUsed int
}

func NewIntakeAutoVerifier(pool *pgxpool.Pool) *IntakeAutoVerifier {
	return &IntakeAutoVerifier{
		Pool: pool,
		NewSearcher: func(ctx context.Context) (IntakeSearcher, error) {
			return naver.NewFromSettings(ctx, pool)
		},
		Translator: NewGTranslator(pool),
	}
}

func autoVerifyEnvInt(key string, def int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// budgetRemaining — 오늘 남은 호출 수(reserve 만큼은 이 호출자가 못 쓰게 제외).
// backlog 레인은 fresh 예약분을 남기고 멈추고, fresh 레인은 reserve=0 으로 전액 사용.
func (v *IntakeAutoVerifier) budgetRemaining(reserve int) int {
	v.mu.Lock()
	defer v.mu.Unlock()
	today := time.Now().Format("2006-01-02")
	if v.budgetDay != today {
		v.budgetDay, v.budgetUsed = today, 0
	}
	limit := autoVerifyEnvInt("KDB_INTAKE_AUTOVERIFY_DAILY_CALLS", 600) - reserve
	if limit < 0 {
		limit = 0
	}
	return limit - v.budgetUsed
}

func (v *IntakeAutoVerifier) budgetAdd(n int) {
	v.mu.Lock()
	v.budgetUsed += n
	v.mu.Unlock()
}

func (v *IntakeAutoVerifier) budgetSnapshot() (used, limit int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.budgetUsed, autoVerifyEnvInt("KDB_INTAKE_AUTOVERIFY_DAILY_CALLS", 600)
}

func autoVerifyFreshReserve() int {
	return autoVerifyEnvInt("KDB_INTAKE_AUTOVERIFY_FRESH_RESERVE", 120)
}

// Tick — backlog 레인 주기 진입점. KDB_INTAKE_AUTOVERIFY=0 이면 전체 비활성.
func (v *IntakeAutoVerifier) Tick(ctx context.Context) (checked, promoted int) {
	if os.Getenv("KDB_INTAKE_AUTOVERIFY") == "0" {
		return 0, 0
	}
	if !v.running.CompareAndSwap(false, true) {
		return 0, 0
	}
	defer v.running.Store(false)
	return v.Run(ctx, autoVerifyEnvInt("KDB_INTAKE_AUTOVERIFY_BATCH", 12))
}

// Run — backlog 레인: 적체 review 를 (번역miss 우선 → 요청빈도순)으로 최대 limit 건
// 검증. Naver 예산 소진 후에도 멈추지 않고 웹검색 폴백으로 계속 처리한다(07-17).
func (v *IntakeAutoVerifier) Run(ctx context.Context, limit int) (checked, promoted int) {
	if v.Pool == nil || limit <= 0 {
		return 0, 0
	}
	reserve := autoVerifyFreshReserve()

	// 좀비 보류 종결(오너 지적 07-17 실측: ready 446/446 전부가 "기각 엔티티 존재"
	// 필터에 걸려 영구 스킵 — 종결도 검증도 안 되는 limbo 500건). 매 Run 선두에서
	// 결론이 이미 있는 행을 닫는다: ①active 존재 → 기존 엔티티로 서빙 종결
	// ②rejected 만 존재 → 기각 확정 종결(운영자 승인 버튼으로 복원 가능).
	_, _ = v.Pool.Exec(ctx, `
UPDATE kwave_entity_research_queue q
   SET status='done', finished_at=COALESCE(finished_at,now()),
       resolution_status='active', locale_status='complete',
       last_outcome='existing_entity', precheck_status='approved', precheck_reason='existing_entity'
 WHERE precheck_status='review' AND status NOT IN ('pending','in_progress')
   AND EXISTS (SELECT 1 FROM kwave_entities e WHERE e.status='active'
                 AND (e.canonical_ko=q.entity_ko OR q.entity_ko=ANY(e.aliases_ko)))`)
	_, _ = v.Pool.Exec(ctx, `
UPDATE kwave_entity_research_queue q
   SET precheck_status='reject', precheck_reason='existing_rejected_entity',
       resolution_status='rejected_precheck', last_outcome='precheck_reject',
       status='done', finished_at=COALESCE(finished_at,now())
 WHERE precheck_status='review' AND status NOT IN ('pending','in_progress')
   AND EXISTS (SELECT 1 FROM kwave_entities e WHERE e.canonical_ko=q.entity_ko AND e.status='rejected')`)
	// ③TTL 자동 종결(무인화, 오너 지시 07-17): triage 가 "후보 가능"으로 남겼어도
	// 21일간 근거가 끝내 안 나오면 기각 확정(복원 가능) — 운영자 개입 없이 수렴한다.
	_, _ = v.Pool.Exec(ctx, `
UPDATE kwave_entity_research_queue q
   SET precheck_status='reject', precheck_reason='no_evidence_expired',
       resolution_status='rejected_precheck', last_outcome='precheck_reject',
       status='done', finished_at=COALESCE(finished_at,now())
 WHERE precheck_status='review' AND status NOT IN ('pending','in_progress')
   AND precheck_flags && ARRAY['triage_kept']
   AND created_at < now()-interval '21 days'`)

	// DISTINCT ON (정규화키): 같은 키워드가 배치에 두 번 뽑혀 검색·판정을 중복하지
	// 않게(오너 지시 07-17 "게이트가 두 번 일 하지 않도록"). 남은 형제 행은 승격 시
	// unique 제약이 duplicate_live_request 로 종결한다.
	rows, err := v.Pool.Query(ctx, `
SELECT t.id, t.entity_ko, t.rt, t.nk, t.misses, t.siblings FROM (
  SELECT DISTINCT ON (q.intake_normalized_key)
       q.id, q.entity_ko, q.requested_entity_type::text AS rt, q.intake_normalized_key AS nk,
       COALESCE(array_length(array_positions(q.precheck_flags,'autoverify_miss'),1),0) AS misses,
       COALESCE((SELECT array_agg(DISTINCT q2.requested_entity_type::text)
                   FROM kwave_entity_research_queue q2
                  WHERE q2.intake_normalized_key=q.intake_normalized_key AND q2.id<>q.id
                    AND q2.requested_entity_type::text NOT IN ('unknown','term')
                    AND COALESCE(q2.precheck_status,'legacy')<>'reject'), '{}') AS siblings,
       (q.intake_origin IN ('lookup-miss','correction-miss')) AS urgent,
       q.request_count, COALESCE(q.last_requested_at, q.created_at) AS last_req
  FROM kwave_entity_research_queue q
 WHERE precheck_status='review'
   AND status NOT IN ('pending','in_progress')
   AND precheck_reason = ANY($2)
   AND NOT (precheck_flags && ARRAY['autoverify_exhausted'])
   AND (next_attempt_at IS NULL OR next_attempt_at <= now())
   AND NOT EXISTS (SELECT 1 FROM kwave_entities e
                    WHERE e.canonical_ko=q.entity_ko AND e.status='rejected')
 ORDER BY q.intake_normalized_key, (q.requested_entity_type::text NOT IN ('unknown','term')) DESC, q.created_at DESC
) t ORDER BY t.urgent DESC, -- 지금 번역에 필요한 것 먼저 (오너 지시 07-16)
          t.request_count DESC, t.last_req DESC
 LIMIT $1`, limit, autoVerifyReasons)
	if err != nil {
		log.Printf("kdb.intake-autoverify: select: %v", err)
		return 0, 0
	}
	var items []autoVerifyItem
	for rows.Next() {
		var it autoVerifyItem
		if rows.Scan(&it.id, &it.ko, &it.reqType, &it.normKey, &it.misses, &it.siblingTypes) == nil {
			items = append(items, it)
		}
	}
	rows.Close()
	if len(items) == 0 {
		return 0, 0
	}

	searcher, err := v.NewSearcher(ctx)
	if err != nil {
		log.Printf("kdb.intake-autoverify: naver client: %v", err)
		return 0, 0
	}
	trusted := v.trustedDomains(ctx)

	// 병렬 검증(오너 지시 07-17 "미해결 보류 빠르게") — 검색·gemma 는 I/O 바운드라
	// 직렬이 병목이었다. gemma 함대(3대×2슬롯)·자체 SearXNG 용량에 맞춘 기본 6.
	conc := autoVerifyEnvInt("KDB_INTAKE_AUTOVERIFY_CONCURRENCY", 6)
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	var mu sync.Mutex
	stopAll := false
	for _, it := range items {
		if ctx.Err() != nil {
			break
		}
		mu.Lock()
		if stopAll {
			mu.Unlock()
			break
		}
		mu.Unlock()
		wg.Add(1)
		sem <- struct{}{}
		go func(it autoVerifyItem) {
			defer wg.Done()
			defer func() { <-sem }()
			// Naver 는 항목당 최대 3콜 — 예산이 그 이하로 남으면 웹검색 폴백만 쓴다.
			useNaver := v.budgetRemaining(reserve) > 3
			ok, stop := v.verifyItem(ctx, searcher, it, useNaver, trusted)
			mu.Lock()
			checked++
			if ok {
				promoted++
			}
			if stop {
				stopAll = true
			}
			mu.Unlock()
		}(it)
	}
	wg.Wait()
	if promoted > 0 && v.Kick != nil {
		v.Kick()
	}
	if checked > 0 {
		used, limitAll := v.budgetSnapshot()
		log.Printf("kdb.intake-autoverify: checked=%d promoted=%d budget=%d/%d",
			checked, promoted, used, limitAll)
	}
	return checked, promoted
}

// VerifyFresh — fresh 레인: 방금 유입/재요청된 review row 를 즉시 검증(오너: "제대로 된
// 키워드는 바로 심사"). 백오프(next_attempt_at)는 무시하되 exhausted(운영자 몫)는 제외.
func (v *IntakeAutoVerifier) VerifyFresh(ctx context.Context, rowID string) {
	if v.Pool == nil || os.Getenv("KDB_INTAKE_AUTOVERIFY") == "0" {
		return
	}
	id, err := uuid.Parse(strings.TrimSpace(rowID))
	if err != nil {
		return
	}
	var it autoVerifyItem
	it.id = id
	err = v.Pool.QueryRow(ctx, `
SELECT entity_ko, requested_entity_type::text, intake_normalized_key,
       COALESCE(array_length(array_positions(precheck_flags,'autoverify_miss'),1),0),
       COALESCE((SELECT array_agg(DISTINCT q2.requested_entity_type::text)
                   FROM kwave_entity_research_queue q2
                  WHERE q2.intake_normalized_key=q.intake_normalized_key AND q2.id<>q.id
                    AND q2.requested_entity_type::text NOT IN ('unknown','term')
                    AND COALESCE(q2.precheck_status,'legacy')<>'reject'), '{}')
  FROM kwave_entity_research_queue q
 WHERE id=$1
   AND precheck_status='review'
   AND status NOT IN ('pending','in_progress')
   AND precheck_reason = ANY($2)
   AND NOT (precheck_flags && ARRAY['autoverify_exhausted'])
   AND NOT EXISTS (SELECT 1 FROM kwave_entities e
                    WHERE e.canonical_ko=q.entity_ko AND e.status='rejected')`,
		id, autoVerifyReasons).
		Scan(&it.ko, &it.reqType, &it.normKey, &it.misses, &it.siblingTypes)
	if err != nil {
		return // 대상 아님(이미 처리됐거나 운영자 몫)
	}
	searcher, err := v.NewSearcher(ctx)
	if err != nil {
		return
	}
	// fresh 레인은 예산 전액 사용 가능(reserve=0). 소진 시 웹검색 폴백으로 즉시심사 유지.
	if ok, _ := v.verifyItem(ctx, searcher, it, v.budgetRemaining(0) > 3, v.trustedDomains(ctx)); ok {
		if v.Kick != nil {
			v.Kick()
		}
		log.Printf("kdb.intake-autoverify: fresh 즉시심사 승격 ko=%q", it.ko)
	}
}

// verifyItem — 한 건 검증: 기존재 단락 → 증거 수집 → 충돌 확인 → 승격/미스 기록.
// stop=true 는 외부 장애(검색 오류)로 이번 레인을 중단하라는 신호.
func (v *IntakeAutoVerifier) verifyItem(ctx context.Context, searcher IntakeSearcher, it autoVerifyItem, useNaver bool, trusted map[string]bool) (promoted, stop bool) {
	// 한 글자 비인물 키워드는 근거가 있어도 자동 승격 금지(운영자 몫) — 게이트 규칙과
	// 동기. person 예명(뷔·진)은 기존대로 자동검증 대상.
	if utf8.RuneCountInString(it.ko) == 1 && strings.ToLower(strings.TrimSpace(it.reqType)) != "person" {
		v.parkForOperator(ctx, it.id, "single_char")
		return false, false
	}
	// 발굴 대기 중 active 가 생겼을 수 있음 — 정확 1건이면 provider 없이 종결.
	activeMatches, compatibleMatches := v.identityState(ctx, it.normKey, it.reqType)
	switch {
	case activeMatches == 1 && compatibleMatches == 1:
		v.closeAsExisting(ctx, it.id)
		return false, false
	case activeMatches > 0:
		v.parkForOperator(ctx, it.id, "autoverify_identity_conflict")
		return false, false
	}

	// 문장형 의심 키워드는 검색 전에 오염 판별 에이전트가 먼저 거른다(쿼터 절약,
	// 오너 승인 07-17: "남파 트레이더 김철수씨의 근황" 류). 보수적 — garbage 확정만 기각.
	if LooksPhraseLike(it.ko) {
		if garbage, reason := TriageKeyword(ctx, it.ko, it.reqType); garbage {
			if rejectByTriage(ctx, v.Pool, it.id, it.ko, reason) {
				return false, false
			}
		}
	}

	// 증거 수집: Naver(예산 내) → 무신호/장애 시 자체 웹검색(SearXNG, 화이트리스트 매체만).
	var ev *intakeEvidence
	var verr error
	if useNaver {
		var calls int
		ev, calls, verr = v.gatherEvidence(ctx, searcher, it.ko, it.reqType, it.siblingTypes)
		v.budgetAdd(calls)
		if verr != nil {
			log.Printf("kdb.intake-autoverify: naver search ko=%q: %v — 웹검색 폴백 시도", it.ko, verr)
		}
	}
	if ev == nil {
		ev = v.webEvidence(ctx, it.ko, it.reqType, it.siblingTypes, trusted)
	}
	if ev == nil {
		if verr != nil {
			// Naver 장애 + 웹 폴백도 무신호 — row 는 건드리지 않고 이번 레인 중단.
			return false, true
		}
		// 근거 무신호 + 오염 판별 에이전트가 garbage 확정 → 재시도 대신 기각 종결.
		if garbage, reason := TriageKeyword(ctx, it.ko, it.reqType); garbage {
			if rejectByTriage(ctx, v.Pool, it.id, it.ko, reason) {
				return false, false
			}
		}
		v.recordMiss(ctx, it.id, it.misses)
		return false, false
	}
	// 증거 type 기준 충돌 재확인(다른 type 의 live/기존 정체가 있으면 운영자 몫).
	// 자기 자신의 row 는 제외 — 증거가 소비자 힌트를 교정하는 경우(type 재지정)까지
	// 자기충돌로 파킹하면 안 된다.
	if ev.Type != it.reqType {
		if v.hasTypeConflict(ctx, it.id, it.normKey, ev.Type) {
			v.parkForOperator(ctx, it.id, "autoverify_type_conflict")
			return false, false
		}
	}
	return v.promote(ctx, it.id, it.reqType, ev), false
}

// trustedDomains — 뉴스 화이트리스트(enabled) 도메인 집합. 웹검색 폴백 증거의
// 출처 신뢰 판정용 — Naver news/encyc 와 달리 일반 웹결과는 화이트리스트 매체만
// 증거로 인정한다(오염 방지). Run/VerifyFresh 당 1회 로드.
func (v *IntakeAutoVerifier) trustedDomains(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	rows, err := v.Pool.Query(ctx, `SELECT DISTINCT domain FROM kwave_news_whitelist WHERE enabled`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var d string
		if rows.Scan(&d) == nil && strings.TrimSpace(d) != "" {
			out[strings.ToLower(strings.TrimSpace(d))] = true
		}
	}
	return out
}

func hostWhitelisted(trusted map[string]bool, rawURL string) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" {
		return false
	}
	host := strings.ToLower(strings.TrimPrefix(u.Hostname(), "www."))
	for d := range trusted {
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

// webEvidence — Naver 무신호/예산소진/장애 시 자체 웹검색(SearXNG) 폴백(2026-07-17,
// 오너: "게이트 병목을 빨리, 오염 없이"). 처리량이 Naver 일일 쿼터(1,000)에 묶이지
// 않게 한다. 오염 방지 2중 장치: ①화이트리스트 매체 결과만 증거로 인정
// ②판정 주체는 여전히 DecideIntake(규칙 완화 없음 — evidenceFromSearchResult 와 동일).
func (v *IntakeAutoVerifier) webEvidence(ctx context.Context, term, requestedType string, siblingTypes []string, trusted map[string]bool) *intakeEvidence {
	if len(trusted) == 0 {
		return nil
	}
	results, _, err := websearch.Default().Search(ctx, term, "ko", 8)
	if err != nil || len(results) == 0 {
		return nil
	}
	types := autoVerifyTypesToTry(requestedType, siblingTypes)
	for _, r := range results {
		if !hostWhitelisted(trusted, r.URL) {
			continue
		}
		contextText := autoVerifyTruncate(strings.TrimSpace(autoVerifyClean(r.Title)+" — "+autoVerifyClean(r.Snippet)), 200)
		if contextText == "" || contextText == "—" {
			continue
		}
		for _, t := range types {
			decision := gatekeeper.DecideIntake(gatekeeper.IntakeInput{
				Term: term, EntityType: t, Context: contextText,
				SourceURL: strings.TrimSpace(r.URL), SourceTrusted: true,
			})
			if decision.Verdict == gatekeeper.IntakePass {
				return &intakeEvidence{
					Type: t, Context: contextText, SourceURL: strings.TrimSpace(r.URL),
					Reason: "auto_evidence_web", Decision: decision,
				}
			}
		}
	}
	return nil
}

// gatherEvidence — encyc(지식백과) 우선, 실패 시 news 정확검색. 반환 calls 는 소비한
// Naver 호출 수. 검색 자체가 실패하면 (nil, calls, err).
func (v *IntakeAutoVerifier) gatherEvidence(ctx context.Context, s IntakeSearcher, term, requestedType string, siblingTypes []string) (*intakeEvidence, int, error) {
	types := autoVerifyTypesToTry(requestedType, siblingTypes)
	calls := 0
	res, err := s.Search(ctx, "encyc", term, 5)
	calls++
	if err != nil {
		return nil, calls, err
	}
	if ev, ok := evidenceFromSearchResult(term, types, res, "auto_evidence_encyc"); ok {
		return &ev, calls, nil
	}
	res, err = s.Search(ctx, "news", `"`+term+`"`, 5)
	calls++
	if err != nil {
		return nil, calls, err
	}
	if ev, ok := evidenceFromSearchResult(term, types, res, "auto_evidence_news"); ok {
		return &ev, calls, nil
	}
	// 3차: 번역 원형 재검색(음차 제목류만, 오너 승인 07-15) — 국내 기사는 음차 대신
	// 영문 원제('Stay This Way')로 쓰는 경우가 많아 원형으로 news 정확검색을 한 번 더.
	// 게이트의 정확언급 대상도 번역형(같은 엔티티의 영문 표기) — reason 라벨로 출처 구분.
	if v.Translator != nil {
		if tr, _, terr := v.Translator.TitleToEN(ctx, term, requestedType); terr == nil && tr != "" {
			res, err = s.Search(ctx, "news", `"`+tr+`"`, 5)
			calls++
			if err != nil {
				return nil, calls, err
			}
			if ev, ok := evidenceFromSearchResult(tr, types, res, "auto_evidence_news_translated"); ok {
				return &ev, calls, nil
			}
		}
	}
	return nil, calls, nil
}

var autoVerifyTagRe = regexp.MustCompile(`<[^>]+>`)

func autoVerifyClean(s string) string {
	return strings.Join(strings.Fields(html.UnescapeString(autoVerifyTagRe.ReplaceAllString(s, " "))), " ")
}

// autoVerifyTruncate — store 의 context_hint 절단(200 rune)과 동일 규칙.
// 절단본으로 게이트를 평가해 "저장되는 문맥 = 판정된 문맥"을 보장한다.
func autoVerifyTruncate(s string, n int) string {
	if rs := []rune(s); len(rs) > n {
		return string(rs[:n])
	}
	return s
}

// evidenceFromSearchResult — 검색결과에서 게이트를 통과시키는 (type, 문맥, 출처)
// 조합을 찾는다. 판정은 DecideIntake 가 전담한다(정확 언급·32rune 근접 type cue·
// 일반어/형태 가드 전부 유지). 순수 함수 — 단위테스트 대상.
func evidenceFromSearchResult(term string, types []string, res *naver.SearchResult, reason string) (intakeEvidence, bool) {
	if res == nil || len(res.Items) == 0 {
		return intakeEvidence{}, false
	}
	for _, it := range res.Items {
		title, desc := autoVerifyClean(it.Title), autoVerifyClean(it.Description)
		if title == "" && desc == "" {
			continue
		}
		contextText := autoVerifyTruncate(strings.TrimSpace(title+" — "+desc), 200)
		for _, t := range types {
			decision := gatekeeper.DecideIntake(gatekeeper.IntakeInput{
				Term: term, EntityType: t, Context: contextText,
				SourceURL: strings.TrimSpace(it.Link), SourceTrusted: true,
			})
			if decision.Verdict == gatekeeper.IntakePass {
				return intakeEvidence{
					Type: t, Context: contextText, SourceURL: strings.TrimSpace(it.Link),
					Reason: reason, Decision: decision,
				}, true
			}
		}
	}
	return intakeEvidence{}, false
}

// autoVerifyTypesToTry — 대조 순서: 이 row 의 type 힌트 → 같은 키워드 형제 row 들의
// 구체 type → 기본 우선순위. 힌트가 틀렸을 수 있으므로(오너: "잘못된 힌트는 우리가
// 판단") 앞순위 실패 시 나머지 type 도 이어서 대조한다.
func autoVerifyTypesToTry(requestedType string, siblingTypes []string) []string {
	out := make([]string, 0, len(autoVerifyTypePriority)+1)
	seen := map[string]bool{}
	add := func(t string) {
		t = strings.ToLower(strings.TrimSpace(t))
		if gatekeeper.IsConcreteIntakeType(t) && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	add(requestedType)
	for _, t := range siblingTypes {
		add(t)
	}
	for _, t := range autoVerifyTypePriority {
		add(t)
	}
	return out
}

// identityState — store 의 인테이크 정체성 검사와 동일 SQL(정규화 키 기준 active 매치 수).
func (v *IntakeAutoVerifier) identityState(ctx context.Context, normKey, entityType string) (activeMatches, compatibleMatches int) {
	_ = v.Pool.QueryRow(ctx, `
SELECT count(*), count(*) FILTER (
         WHERE $2 IN ('unknown','term') OR entity_type::text=$2
       )
 FROM kwave_entities
 WHERE status='active'
   AND (
     lower(regexp_replace(btrim(canonical_ko), '[[:space:][:punct:]]+', '', 'g'))=$1
     OR EXISTS (
       SELECT 1 FROM unnest(COALESCE(aliases_ko,'{}')) a
        WHERE lower(regexp_replace(btrim(a), '[[:space:][:punct:]]+', '', 'g'))=$1
     )
   )`, normKey, entityType).Scan(&activeMatches, &compatibleMatches)
	return activeMatches, compatibleMatches
}

// hasTypeConflict — 같은 정규화 키가 다른 구체 type 으로 이미 진행/존재하는지
// (store 인테이크와 동일 규칙 — 충돌이면 자동 승격 금지, 운영자 몫).
// 검증 중인 row 자신($3)은 제외 — 증거가 그 row 의 type 힌트를 교정하는 경우다.
func (v *IntakeAutoVerifier) hasTypeConflict(ctx context.Context, selfID uuid.UUID, normKey, entityType string) bool {
	var conflict bool
	_ = v.Pool.QueryRow(ctx, `
SELECT EXISTS (
  SELECT 1 FROM kwave_entity_research_queue q
   WHERE q.intake_normalized_key=$1
     AND q.id <> $3
     AND q.requested_entity_type::text NOT IN ('unknown','term',$2)
     AND COALESCE(q.precheck_status,'legacy') <> 'reject'
  UNION ALL
  SELECT 1 FROM kwave_entities e
   WHERE (lower(regexp_replace(btrim(e.canonical_ko), '[[:space:][:punct:]]+', '', 'g'))=$1
          OR EXISTS (SELECT 1 FROM unnest(COALESCE(e.aliases_ko,'{}')) a
                      WHERE lower(regexp_replace(btrim(a), '[[:space:][:punct:]]+', '', 'g'))=$1))
     AND e.entity_type::text NOT IN ('unknown','term',$2)
     AND e.status IN ('active','candidate')
)`, normKey, entityType, selfID).Scan(&conflict)
	return conflict
}

func (v *IntakeAutoVerifier) closeAsExisting(ctx context.Context, id uuid.UUID) {
	_, _ = v.Pool.Exec(ctx, `
UPDATE kwave_entity_research_queue
   SET status='done', finished_at=now(), picked_at=NULL, next_attempt_at=NULL,
       resolution_status='active', locale_status='complete', last_outcome='existing_entity',
       precheck_status='pass', precheck_reason='existing_entity',
       precheck_flags=array_append(precheck_flags,'auto_evidence'), last_error=NULL
 WHERE id=$1 AND precheck_status='review' AND status <> 'in_progress'`, id)
}

// parkForOperator — 자동 검증으로 풀 수 없는 충돌. review 유지 + 재시도 제외 표시.
func (v *IntakeAutoVerifier) parkForOperator(ctx context.Context, id uuid.UUID, flag string) {
	_, _ = v.Pool.Exec(ctx, `
UPDATE kwave_entity_research_queue
   SET precheck_flags=array_append(array_append(precheck_flags,$2),'autoverify_exhausted'),
       next_attempt_at=NULL
 WHERE id=$1 AND precheck_status='review'`, id, flag)
}

// recordMiss — 증거 미발견. 1d → 3d 백오프 후 3회째는 소진(운영자 잔류).
func (v *IntakeAutoVerifier) recordMiss(ctx context.Context, id uuid.UUID, misses int) {
	if misses >= 2 {
		_, _ = v.Pool.Exec(ctx, `
UPDATE kwave_entity_research_queue
   SET precheck_flags=array_append(array_append(precheck_flags,'autoverify_miss'),'autoverify_exhausted'),
       next_attempt_at=NULL, last_outcome='autoverify_no_evidence'
 WHERE id=$1 AND precheck_status='review'`, id)
		return
	}
	delay := "1 day"
	if misses == 1 {
		delay = "3 days"
	}
	_, _ = v.Pool.Exec(ctx, `
UPDATE kwave_entity_research_queue
   SET precheck_flags=array_append(precheck_flags,'autoverify_miss'),
       next_attempt_at=now()+$2::interval, last_outcome='autoverify_no_evidence'
 WHERE id=$1 AND precheck_status='review'`, id, delay)
}

// promote — 증거로 게이트를 통과한 row 를 approved/pending 으로 전환.
// 'approved' 를 쓰는 이유: worker 의 fail-closed 재평가는 출처 신뢰를 whitelist 로만
// 재계산하는데, 이 증거는 KDB 가 Naver API 에서 1차로 수집한 것이라 whitelist 밖이다.
// approved(+auto_evidence 사유·문맥·출처 저장) = "서버 자체 검증 완료" 감사 표식.
func (v *IntakeAutoVerifier) promote(ctx context.Context, id uuid.UUID, requestedType string, ev *intakeEvidence) bool {
	flags := append([]string{}, ev.Decision.Flags...)
	flags = append(flags, "auto_evidence")
	if rt := strings.ToLower(strings.TrimSpace(requestedType)); gatekeeper.IsConcreteIntakeType(rt) && rt != ev.Type {
		flags = append(flags, "autoverify_type_reassigned")
	}
	tag, err := v.Pool.Exec(ctx, `
UPDATE kwave_entity_research_queue
   SET status='pending', finished_at=NULL, picked_at=NULL, next_attempt_at=NULL,
       resolution_status='unknown', locale_status='unknown', last_outcome='', last_error=NULL,
       precheck_status='approved', precheck_reason=$2,
       precheck_flags=$3, precheck_rule_version=$4,
       requested_entity_type=$5::kwave_entity_type,
       context_hint=$6, source_url=$7
 WHERE id=$1 AND precheck_status='review' AND status <> 'in_progress'`,
		id, ev.Reason, flags, ev.Decision.RuleVersion, ev.Type, ev.Context, ev.SourceURL)
	if err != nil {
		// 같은 (정규화키, type) live row 가 이미 발굴 중 — 이 row 는 중복 종결.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			_, _ = v.Pool.Exec(ctx, `
UPDATE kwave_entity_research_queue
   SET status='done', finished_at=now(), precheck_reason='duplicate_live_request',
       last_outcome='duplicate_live_request'
 WHERE id=$1 AND precheck_status='review'`, id)
			return false
		}
		log.Printf("kdb.intake-autoverify: promote id=%s: %v", id, err)
		return false
	}
	return err == nil && tag.RowsAffected() == 1
}

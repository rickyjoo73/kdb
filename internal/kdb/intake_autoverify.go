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
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb/agents/gatekeeper"
	"github.com/rickyjoo73/kdb/internal/kdb/naver"
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

// Run — backlog 레인: 적체 review 를 요청빈도순으로 최대 limit 건 검증.
func (v *IntakeAutoVerifier) Run(ctx context.Context, limit int) (checked, promoted int) {
	if v.Pool == nil || limit <= 0 {
		return 0, 0
	}
	reserve := autoVerifyFreshReserve()
	if v.budgetRemaining(reserve) <= 0 {
		return 0, 0
	}

	rows, err := v.Pool.Query(ctx, `
SELECT id, entity_ko, requested_entity_type::text, intake_normalized_key,
       COALESCE(array_length(array_positions(precheck_flags,'autoverify_miss'),1),0),
       COALESCE((SELECT array_agg(DISTINCT q2.requested_entity_type::text)
                   FROM kwave_entity_research_queue q2
                  WHERE q2.intake_normalized_key=q.intake_normalized_key AND q2.id<>q.id
                    AND q2.requested_entity_type::text NOT IN ('unknown','term')
                    AND COALESCE(q2.precheck_status,'legacy')<>'reject'), '{}')
  FROM kwave_entity_research_queue q
 WHERE precheck_status='review'
   AND status NOT IN ('pending','in_progress')
   AND precheck_reason = ANY($2)
   AND NOT (precheck_flags && ARRAY['autoverify_exhausted'])
   AND (next_attempt_at IS NULL OR next_attempt_at <= now())
   AND NOT EXISTS (SELECT 1 FROM kwave_entities e
                    WHERE e.canonical_ko=q.entity_ko AND e.status='rejected')
 ORDER BY request_count DESC, COALESCE(last_requested_at, created_at) DESC
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

	for _, it := range items {
		if ctx.Err() != nil || v.budgetRemaining(reserve) <= 0 {
			break
		}
		checked++
		ok, stop := v.verifyItem(ctx, searcher, it)
		if ok {
			promoted++
		}
		if stop {
			break
		}
	}
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
	if v.budgetRemaining(0) <= 0 {
		return // 오늘 예산 전액 소진 — backlog tick 이 내일 수거
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
	if ok, _ := v.verifyItem(ctx, searcher, it); ok {
		if v.Kick != nil {
			v.Kick()
		}
		log.Printf("kdb.intake-autoverify: fresh 즉시심사 승격 ko=%q", it.ko)
	}
}

// verifyItem — 한 건 검증: 기존재 단락 → 증거 수집 → 충돌 확인 → 승격/미스 기록.
// stop=true 는 외부 장애(검색 오류)로 이번 레인을 중단하라는 신호.
func (v *IntakeAutoVerifier) verifyItem(ctx context.Context, searcher IntakeSearcher, it autoVerifyItem) (promoted, stop bool) {
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

	ev, calls, verr := v.gatherEvidence(ctx, searcher, it.ko, it.reqType, it.siblingTypes)
	v.budgetAdd(calls)
	if verr != nil {
		// 외부 일시 장애(쿼터/429/5xx 포함) — row 는 건드리지 않고 다음 기회에 재시도.
		log.Printf("kdb.intake-autoverify: search ko=%q: %v", it.ko, verr)
		return false, true
	}
	if ev == nil {
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

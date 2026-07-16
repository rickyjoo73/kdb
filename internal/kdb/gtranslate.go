package kdb

// gtranslate — miss 한글 키워드(음차 제목류)를 Google Cloud Translation v2 로 영문
// 원형으로 복원한다(예: "스테이 디스 웨이" → "Stay This Way"). ★읽기 경로 전용:
// 결과는 재매칭 질의로만 쓰고 canonical_*/aliases_* 에 기록하지 않는다("빈칸>틀린값").
//
// 전송은 B방식(유형 템플릿 문장) — 단어만 보내면 일반명사로 오역(거짓말→"lie")되고,
// 짧은 템플릿 문장에 넣으면 공식 제목으로 인식(거짓말→"Lies")됨을 실측으로 확정
// (docs/runbooks/KDB_TRANSLATION_API_REVIEW_20260715.md §6, 오너 결정 07-15).
//
// 과금 가드: 단어당 1회 캐시(kwave_kdb_translate_cache, 실패도 기록) + 당월 전송
// 문자수 400,000자(무료 50만자의 80%) 초과 시 호출 중단. KDB_GTRANSLATE_KEY 미설정
// 이면 전체 비활성(nil translator) — 기존 동작 불변.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	gtranslateEndpoint = "https://translation.googleapis.com/language/translate/v2"
	// gtranslateMonthCapChars — 당월 전송 문자수 상한(무료한도 50만자의 80%).
	gtranslateMonthCapChars = 400_000
)

// gtranslateTemplates — 유형별 B방식 템플릿. 따옴표 안 제목이 번역문에서도 따옴표로
// 남는 구조라 추출이 결정적이다.
var gtranslateTemplates = map[string]string{
	"song_album": "신곡 '%s'를 발표했다.",
	"drama":      "드라마 '%s'에 출연했다.",
	"movie":      "영화 '%s'가 개봉했다.",
	"show":       "예능 '%s'에 출연했다.",
	"event_tour": "'%s' 공연이 열린다.",
	"":           "'%s'가 화제다.",
}

// gtranslateFillTemplates — 채움(쓰기 폴백, 오너 방침 2026-07-16) 전용 추가 템플릿.
// 읽기(재매칭) 경로의 eligibility 는 gtranslateTemplates 만 보므로 재매칭 동작 불변.
// 인명은 짧은 인용문 문장에 넣으면 구글이 표준 로마자로 복원한다("김민준"→"Kim Min-jun").
var gtranslateFillTemplates = map[string]string{
	"person": "'%s'가 인터뷰에서 말했다.",
}

// GTranslateFillTermType — 채움(쓰기 폴백) 경로의 엔티티타입→템플릿 키 매핑.
// ok=false 는 채움 제외 타입(unknown/term — 정체불명은 번역해봐야 오염).
func GTranslateFillTermType(entityType string) (string, bool) {
	switch entityType {
	case "song_album", "drama", "movie", "show", "event_tour":
		return entityType, true
	case "person", "group", "character":
		return "person", true
	case "unknown", "term", "":
		return "", false
	}
	return "", true // agency/brand_place/channel_outlet 등 — 일반 템플릿
}

var (
	gtranslateHangulRe = regexp.MustCompile(`[가-힣]`)
	// 번역문에서 따옴표 구간 추출 — 최외곽 greedy. 소유격 아포스트로피("Kim
	// Young-chul's Power FM")가 내부에 있어도 잘리지 않게 첫~마지막 따옴표를 잡는다
	// (우리 템플릿의 인용부는 정확히 1개 — 실측: 내부문자클래스 방식은 오추출).
	gtranslateQuoteRe = regexp.MustCompile(`['‘’"“”](.{2,80})['‘’"“”]`)
	// 정규화 비교용 — 공백·구두점 차이 무시(더쇼 vs 더 쇼, 놀면 뭐하니 vs 놀면 뭐하니?).
	gtranslateNormRe = regexp.MustCompile(`[^0-9A-Za-z가-힣]+`)
)

// GTranslateNormKey — 공백/구두점 제거 + 소문자화한 비교 키.
func GTranslateNormKey(s string) string {
	return strings.ToLower(gtranslateNormRe.ReplaceAllString(s, ""))
}

// GTranslateSafeHit — 번역 재매칭 히트의 오매칭 가드(서빙 정밀도 우선, "틀린값보다
// 빈칸"). 매칭 엔티티가 요청어와 다른 '한글 고유제목'을 가지면 동명 영문제목의 다른
// 작품일 가능성이 높아 기각한다(실측: 광장→The Square 가 '더 스퀘어'에 오매칭).
//   - canonical_ko(정규화)가 요청어와 같으면 통과(띄어쓰기·구두점 변형 — 보너스 매칭)
//   - canonical_ko 에 한글이 없으면(영문제목 엔티티) 통과 — 진짜 음차 복원 케이스
//   - aliases_ko 에 요청어(정규화)가 있으면 통과
func GTranslateSafeHit(termKo, canonicalKo string, aliasesKo []string) bool {
	tk := GTranslateNormKey(termKo)
	if GTranslateNormKey(canonicalKo) == tk {
		return true
	}
	if !gtranslateHangulRe.MatchString(canonicalKo) {
		return true
	}
	for _, a := range aliasesKo {
		if GTranslateNormKey(a) == tk {
			return true
		}
	}
	return false
}

// GTranslateEligible — 번역 재매칭 대상인가: 한글 포함 && 제목류 type(또는 무유형).
// 인명(person/character/group)은 번역할 게 없어 제외(한국명 로마자는 별도 경로).
func GTranslateEligible(term, termType string) bool {
	if !gtranslateHangulRe.MatchString(term) {
		return false
	}
	_, ok := gtranslateTemplates[termType]
	return ok
}

// GTranslator — Cloud Translation v2 클라이언트 + 캐시/한도 가드. nil 리시버 안전.
type GTranslator struct {
	Pool *pgxpool.Pool
	Key  string
	HTTP *http.Client
}

// NewGTranslator — KDB_GTRANSLATE_KEY 미설정이면 nil(비활성).
func NewGTranslator(pool *pgxpool.Pool) *GTranslator {
	key := strings.TrimSpace(os.Getenv("KDB_GTRANSLATE_KEY"))
	if key == "" || pool == nil {
		return nil
	}
	return &GTranslator{Pool: pool, Key: key, HTTP: &http.Client{Timeout: 6 * time.Second}}
}

// TitleToEN — 한글 제목류 term 의 영문 원형 후보를 반환. 캐시 우선, 미스면 v2 호출.
// fresh=true 는 이번에 실제 API 를 호출했음(호출측 예산 제어용). 복원 실패/비대상/
// 한도초과는 "" (에러는 일시 장애만 — 호출측은 무시하고 기존 miss 흐름 유지).
func (g *GTranslator) TitleToEN(ctx context.Context, term, termType string) (string, bool, error) {
	if g == nil || !GTranslateEligible(term, termType) {
		return "", false, nil
	}
	return g.translateWithTemplate(ctx, term, termType, gtranslateTemplates[termType])
}

// FillEN — 채움(쓰기 폴백) 경로: canonical_en 빈칸용 영문 표기 후보를 반환한다.
// 캐시/월한도/추출 가드는 TitleToEN 과 공유. 호출측(enrich L5)이 applyEmptyOnly +
// SourceGTranslate(prio 8)로 기록해 상위 소스가 항상 업그레이드할 수 있게 한다.
func (g *GTranslator) FillEN(ctx context.Context, ko, entityType string) (string, bool, error) {
	if g == nil || !gtranslateHangulRe.MatchString(ko) {
		return "", false, nil
	}
	tt, ok := GTranslateFillTermType(entityType)
	if !ok {
		return "", false, nil
	}
	tpl, ok := gtranslateFillTemplates[tt]
	if !ok {
		tpl = gtranslateTemplates[tt]
	}
	return g.translateWithTemplate(ctx, ko, tt, tpl)
}

// translateWithTemplate — 캐시→월한도→v2 호출→추출→캐시기록 공통 코어.
func (g *GTranslator) translateWithTemplate(ctx context.Context, term, termType, template string) (string, bool, error) {
	term = strings.TrimSpace(term)
	// 1) 캐시 (실패 기록 포함 — 재과금 방지)
	var cached string
	err := g.Pool.QueryRow(ctx,
		`SELECT translated_en FROM kwave_kdb_translate_cache WHERE term_ko=$1 AND term_type=$2`,
		term, termType).Scan(&cached)
	if err == nil {
		return cached, false, nil
	}
	// 2) 월 한도 가드
	var monthChars int
	_ = g.Pool.QueryRow(ctx,
		`SELECT COALESCE(sum(chars),0) FROM kwave_kdb_translate_cache
		  WHERE created_at >= date_trunc('month', now())`).Scan(&monthChars)
	if monthChars >= gtranslateMonthCapChars {
		log.Printf("kdb.gtranslate: month cap reached (%d chars) — skipping ko=%q", monthChars, term)
		return "", false, nil
	}
	// 3) v2 호출 (B방식 템플릿 문장)
	sentence := fmt.Sprintf(template, term)
	out, err := g.call(ctx, sentence)
	if err != nil {
		return "", true, err // 일시 장애 — 캐시에 남기지 않고 다음 기회에 재시도
	}
	translated := gtranslateExtract(out, term)
	// 4) 캐시 기록(실패=빈값도 기록)
	_, _ = g.Pool.Exec(ctx,
		`INSERT INTO kwave_kdb_translate_cache (term_ko, term_type, translated_en, chars)
		 VALUES ($1,$2,$3,$4) ON CONFLICT (term_ko, term_type) DO NOTHING`,
		term, termType, translated, len([]rune(sentence)))
	return translated, true, nil
}

// call — v2 REST 호출. 키는 POST body 로만 전송(URL/로그 노출 방지).
func (g *GTranslator) call(ctx context.Context, q string) (string, error) {
	form := url.Values{
		"q": {q}, "source": {"ko"}, "target": {"en"}, "format": {"text"}, "key": {g.Key},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, gtranslateEndpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := g.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gtranslate: http %d", resp.StatusCode)
	}
	var body struct {
		Data struct {
			Translations []struct {
				TranslatedText string `json:"translatedText"`
			} `json:"translations"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	if len(body.Data.Translations) == 0 {
		return "", fmt.Errorf("gtranslate: empty response")
	}
	return body.Data.Translations[0].TranslatedText, nil
}

// gtranslateExtract — 번역문의 첫 따옴표 구간이 복원 후보. 한글 잔존(미번역)·원문
// 동일·비정상 길이는 실패("")로 처리해 오매칭을 차단한다.
func gtranslateExtract(sentence, term string) string {
	m := gtranslateQuoteRe.FindStringSubmatch(sentence)
	if m == nil {
		return ""
	}
	got := strings.TrimSpace(m[1])
	if got == "" || gtranslateHangulRe.MatchString(got) || strings.EqualFold(got, term) {
		return ""
	}
	return got
}

// TranslateBacklog — miss 백로그(request_terms preparing/new 한글 제목류)를 번역
// 원형으로 재매칭해 리포트한다(one-shot). DB 쓰기는 번역 캐시와 rejected 오거부
// 플래그(research_queue precheck_flags)만 — canonical/aliases/상태 불변(읽기 경로 원칙).
func TranslateBacklog(ctx context.Context, pool *pgxpool.Pool, limit int) {
	g := NewGTranslator(pool)
	if g == nil {
		log.Printf("kdb.translate-backlog: KDB_GTRANSLATE_KEY 미설정 — 중단")
		return
	}
	rows, err := pool.Query(ctx, `
SELECT DISTINCT ON (term_ko) term_ko, term_type
  FROM kwave_kdb_request_terms
 WHERE item_status IN ('preparing','new')
   AND term_ko ~ '[가-힣]'
   AND term_type IN ('song_album','drama','movie','show','event_tour','')
 ORDER BY term_ko, created_at DESC
 LIMIT $1`, limit)
	if err != nil {
		log.Printf("kdb.translate-backlog: query: %v", err)
		return
	}
	type item struct{ ko, ttype string }
	var todo []item
	for rows.Next() {
		var it item
		if rows.Scan(&it.ko, &it.ttype) == nil {
			todo = append(todo, it)
		}
	}
	rows.Close()

	var nActive, nCandidate, nRejected, nNone, nSkip, nAmbig int
	var rejectedList, activeList []string
	for i, it := range todo {
		tr, fresh, err := g.TitleToEN(ctx, it.ko, it.ttype)
		if err != nil {
			log.Printf("kdb.translate-backlog: ko=%q: %v", it.ko, err)
			continue
		}
		if tr == "" {
			nSkip++
			continue
		}
		var status, ctype, cko string
		var caliases []string
		err = pool.QueryRow(ctx, `
SELECT status, entity_type::text, canonical_ko, aliases_ko FROM kwave_entities
 WHERE lower(canonical_en) = lower($1) OR lower(canonical_ko) = lower($1)
    OR EXISTS (SELECT 1 FROM unnest(aliases_en || aliases_ko) a WHERE lower(a) = lower($1))
 ORDER BY (status='active') DESC, (status='candidate') DESC
 LIMIT 1`, tr).Scan(&status, &ctype, &cko, &caliases)
		switch {
		case err != nil:
			nNone++
		case status == "active" && !GTranslateSafeHit(it.ko, cko, caliases):
			nAmbig++ // 동명 영문제목 의심 — 서빙 가드와 동일 기준으로 기각
		case status == "active":
			nActive++
			activeList = append(activeList, fmt.Sprintf("%s → %s (%s/%s)", it.ko, tr, ctype, cko))
		case status == "candidate":
			nCandidate++
		case status == "rejected":
			nRejected++
			rejectedList = append(rejectedList, fmt.Sprintf("%s → %s (%s)", it.ko, tr, cko))
			_, _ = pool.Exec(ctx, `
UPDATE kwave_entity_research_queue
   SET precheck_flags = array_append(precheck_flags, 'translate_hit_rejected')
 WHERE entity_ko = $1 AND NOT ('translate_hit_rejected' = ANY(precheck_flags))`, it.ko)
		default:
			nNone++
		}
		if fresh {
			time.Sleep(120 * time.Millisecond) // 외부 API 예의(레이트 완충)
		}
		if (i+1)%50 == 0 {
			log.Printf("kdb.translate-backlog: %d/%d 진행", i+1, len(todo))
		}
	}
	log.Printf("kdb.translate-backlog: 완료 total=%d active=%d candidate=%d rejected=%d none=%d 동명기각=%d 변환실패=%d 당월사용=%d자",
		len(todo), nActive, nCandidate, nRejected, nNone, nAmbig, nSkip, g.GTranslateMonthUsage(ctx))
	for _, s := range activeList {
		log.Printf("kdb.translate-backlog: [active-hit] %s", s)
	}
	for _, s := range rejectedList {
		log.Printf("kdb.translate-backlog: [오거부 후보] %s", s)
	}
}

// GTranslateMonthUsage — 당월 전송 문자수(admin/리포트용).
func (g *GTranslator) GTranslateMonthUsage(ctx context.Context) int {
	if g == nil {
		return 0
	}
	var n int
	_ = g.Pool.QueryRow(ctx,
		`SELECT COALESCE(sum(chars),0) FROM kwave_kdb_translate_cache
		  WHERE created_at >= date_trunc('month', now())`).Scan(&n)
	return n
}

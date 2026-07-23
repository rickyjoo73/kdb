package kdb

// OTT(넷플릭스 등) 현지제목 그라운딩 — Wikidata·TMDb에 없는 작품(drama/show/movie)의
// locale 공식제목을, 넷플릭스 지역 페이지에서 확보한다.
//
// ★IP 차단 방지(오너 방침 "절대 벌크 금지"): websearch 전역 throttle 위에서 동작 +
// 본 드레이너가 매 조회 사이 ottInterval(기본 10초) pacing → 버스트 불가. 소량·점진만.
//
// ★정확도(중요): 한국어 키워드 site-search 는 현지 페이지에 한국어가 없어 다른 작품을
// 오매칭한다(실측: vn->Nguyet lan, br->Katrina). 따라서 반드시 ① site:netflix.com {ko} 로
// title ID 확보(한국 페이지가 한국명 매칭) → ② site:netflix.com/{loc}/title/{ID} 로 그
// 작품의 정확한 현지제목. URL 에 그 ID 가 있는 결과만 신뢰한다.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb/agents"
	"github.com/rickyjoo73/kdb/internal/kdb/websearch"
)

// ottHTTP — OTT site 검색 전용 직접 SearXNG 클라이언트. websearch.Chain 은 locale 별로
// engines=wikipedia,bing 으로 제한해 netflix site: 결과를 떨군다(실측 202/0건). OTT 는
// engines 제한 없이(전체 default 엔진) 직접 질의. IP-safety 는 DrainNetflixWorks 의 10초
// pacing 이 보장(throttle 불요 — 호출 간격이 이미 길다).
var ottHTTP = &http.Client{Timeout: 15 * time.Second}

func ottSearxngBase() string {
	if v := strings.TrimSpace(os.Getenv("KDB_SEARXNG_URL")); v != "" {
		return v
	}
	return "http://kdb-searxng:8080"
}

// searxngDefault — engines 제한 없는 SearXNG 직접 검색(netflix site: 용).
func searxngDefault(ctx context.Context, query string) []websearch.Result {
	q := url.Values{}
	q.Set("q", query)
	q.Set("format", "json")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(ottSearxngBase(), "/")+"/search?"+q.Encode(), nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Accept", "application/json")
	resp, err := ottHTTP.Do(req)
	if err != nil {
		log.Printf("kdb.ott: searxng %v", err)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("kdb.ott: searxng status %d", resp.StatusCode)
		return nil
	}
	var sr struct {
		Results []struct {
			URL     string `json:"url"`
			Title   string `json:"title"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&sr) != nil {
		return nil
	}
	out := make([]websearch.Result, 0, len(sr.Results))
	for _, r := range sr.Results {
		out = append(out, websearch.Result{Title: r.Title, URL: r.URL, Snippet: r.Content})
	}
	return out
}

// netflixLocalePath — KDB locale → 넷플릭스 URL 지역코드.
// ★ja/zh_hant/vi/es 만: 이 locale 현지제목은 non-ASCII(한자·발음부호)라 "순수 ASCII=영어leak"
// 가드가 신뢰성 있게 영어를 거른다. pt_br/id 는 언어 자체가 ASCII 라 영어와 구분 불가 → 제외.
// zh(간체)는 중국 넷플릭스 부재로 제외.
var netflixLocalePath = map[string]string{
	"ja": "jp", "vi": "vn", "es": "es", "zh_hant": "tw",
}

// isPureASCII — 모든 문자가 ASCII 면 true. ja/zh_hant/vi/es 현지제목은 non-ASCII 여야
// 하므로(영어 제목은 순수 ASCII), 이걸로 넷플릭스가 그 지역에 영어로 서빙한 제목(leak)을 거른다.
func isPureASCII(s string) bool {
	for _, r := range s {
		if r > 127 {
			return false
		}
	}
	return true
}

// ottLocaleCol — locale → canonical 컬럼명.
var ottLocaleCol = map[string]string{
	"ja": "canonical_ja", "vi": "canonical_vi", "es": "canonical_es",
	"pt_br": "canonical_pt_br", "id": "canonical_id", "zh_hant": "canonical_zh_hant",
}

var netflixTitleIDRe = regexp.MustCompile(`/title/(\d+)`)

// 넷플릭스 스니펫 제목의 watch 동사(접두/접미) — 현지제목만 남기려 제거.
var netflixWatchPrefix = []string{"Assistir a ", "Assistir ", "Xem ", "Ve ", "Nonton ", "Watch "}
var netflixWatchSuffix = []string{"を観る", "を視聴する", "を視聴", "觀看", "观看"}

// zeroWidth — 스니펫 제목에 섞인 BOM/zero-width 문자(검색엔진이 삽입).
var zeroWidth = []string{string(rune(0xFEFF)), string(rune(0x200B)), string(rune(0x200C))}

// ottInterval — OTT 조회 간 최소 간격(IP 차단 방지). 오너 방침: 10초(2.5초는 너무 빠름).
func ottInterval() time.Duration {
	if v := os.Getenv("KDB_OTT_MIN_INTERVAL_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Millisecond
		}
	}
	return 10 * time.Second
}

// extractNetflixTitle — "<현지제목>を観る | Netflix..." → "<현지제목>".
func extractNetflixTitle(snippetTitle string) string {
	t := snippetTitle
	for _, z := range zeroWidth {
		t = strings.ReplaceAll(t, z, "")
	}
	// 구분자(" | " 또는 " - "/"–"/"—")에서 컷 — 이후는 전부 Netflix 브랜딩(제목마다 |/-
	// 가 섞임: "...을 観る | Netflix", "...を観る - Netflix"). 가장 앞 구분자에서 자른다.
	// 공백을 요구해 제목 내부 하이픈("·"·"〜"는 다른 문자)과 구분.
	cut := len(t)
	for _, sep := range []string{" | ", " - ", " – ", " — "} {
		if i := strings.Index(t, sep); i >= 0 && i < cut {
			cut = i
		}
	}
	t = strings.TrimSpace(t[:cut])
	for _, p := range netflixWatchPrefix {
		if strings.HasPrefix(t, p) {
			t = strings.TrimSpace(t[len(p):])
			break
		}
	}
	for _, s := range netflixWatchSuffix {
		if strings.HasSuffix(t, s) {
			t = strings.TrimSpace(t[:len(t)-len(s)])
			break
		}
	}
	return strings.TrimSpace(t)
}

// ottSeasonRe — 제목 끝의 시즌 표기(시즌2 / 2 / 시즌). 시즌 변형 동일작 매칭용.
var ottSeasonRe = regexp.MustCompile(`(시즌)?\d+$`)

// ottBrandSuffix — 검색 스니펫 제목의 distributor 접미사 마커(작품명 뒤). 가장 앞 마커에서 컷.
var ottBrandSuffix = []string{" | ", ", 지금", " 지금 ", " - Netflix"}

// ottNorm — 매칭용 정규화: 영숫자·한글·가나·한자만(공백·구두점·발음부호 제거), 소문자.
func ottNorm(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z',
			r >= 0xAC00 && r <= 0xD7A3, // 한글
			r >= 0x3040 && r <= 0x30FF, // 가나
			r >= 0x4E00 && r <= 0x9FFF: // 한자
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ottWorkTitle — 스니펫 제목에서 작품명만(distributor 접미사 제거). 제목 내부 콤마 보존
// 위해 ", 지금"(넷플릭스 한국어 접미사)·" | "(브랜딩) 등 마커에서 컷.
func ottWorkTitle(raw string) string {
	cut := len(raw)
	for _, mk := range ottBrandSuffix {
		if i := strings.Index(raw, mk); i >= 0 && i < cut {
			cut = i
		}
	}
	return strings.TrimSpace(raw[:cut])
}

// ottTitleMatchesKo — 스니펫 제목이 우리 canonical_ko 와 동일작인가(오매칭 차단의 핵심).
// 정규화 후 정확일치 또는 시즌-strip 일치(굿파트너2≈굿파트너)만 인정. 부분/퍼지 매치는
// 거부(여왕의집≠눈물의여왕, 첫번째남자≠첫사랑은처음이라서, 전원일기≠어쩌다전원일기).
func ottTitleMatchesKo(title, ko string) bool {
	tn, kn := ottNorm(ottWorkTitle(title)), ottNorm(ko)
	if tn == "" || kn == "" {
		return false
	}
	if tn == kn {
		return true
	}
	ks := ottSeasonRe.ReplaceAllString(kn, "")
	return ks != "" && ks == ottSeasonRe.ReplaceAllString(tn, "")
}

// resolveOTTID — site: 검색에서 스니펫 제목이 ko 와 동일작인 첫 결과의 ID(idRe 캡처1).
// ★ko-제목 앵커: 키워드 퍼지매치로 다른 작품이 반환돼도(여왕의집→눈물의여왕) 채택 안 함.
func resolveOTTID(ctx context.Context, query string, idRe *regexp.Regexp, ko string) (string, bool) {
	res := searxngDefault(ctx, query)
	for _, r := range res {
		m := idRe.FindStringSubmatch(r.URL)
		if m == nil {
			continue
		}
		if ottTitleMatchesKo(r.Title, ko) {
			return m[1], true
		}
	}
	return "", false
}

// resolveNetflixID — site:netflix.com {ko} → ko-제목 앵커 통과한 첫 /title/{id}.
func resolveNetflixID(ctx context.Context, ko string) (string, bool) {
	return resolveOTTID(ctx, "site:netflix.com "+ko, netflixTitleIDRe, ko)
}

// ottProvider — OTT distributor 추상화(넷플릭스/디즈니+ 등). 공통 드레인 로직을 공유하고
// provider 별 URL·지역코드·ID 해석만 갈아끼운다. 새 distributor 추가 = 이 구조체 1개 + Drain 래퍼.
type ottProvider struct {
	name          string                                              // source 라벨: "netflix"/"disney"
	brand         string                                              // 스니펫 브랜딩 토큰(거부검사): "Netflix"/"Disney"
	cooldownField string                                              // enrich_attempts.field: "ottfill"/"disneyfill"
	localePath    map[string]string                                   // KDB locale → URL 지역세그먼트
	interval      func() time.Duration                                // 조회 간 pacing
	resolveID     func(ctx context.Context, ko string) (string, bool) // ko → 작품 ID(site: 검색)
	localeQuery   func(loc, id string) (query, want string)           // 지역제목 site: 질의 + 결과 URL 확인 substring
}

// netflixProvider — 넷플릭스. site:netflix.com/{지역}/title/{numeric-id}.
var netflixProvider = ottProvider{
	name:          "netflix",
	brand:         "Netflix",
	cooldownField: "ottfill",
	localePath:    netflixLocalePath,
	interval:      ottInterval,
	resolveID:     resolveNetflixID,
	localeQuery: func(loc, id string) (string, string) {
		path := netflixLocalePath[loc]
		return fmt.Sprintf("site:netflix.com/%s/title/%s", path, id),
			fmt.Sprintf("/%s/title/%s", path, id)
	},
}

// ottLocaleTitle — ID 앵커로 그 작품의 정확한 현지제목을 gemma 로 추출한다(provider 공통).
// ① provider.localeQuery 로 그 작품·그 지역 페이지 결과만 모아(올바른 작품 보장)
// ② gemma(localFillVote)로 공식 현지제목 추출 — 구분자(|/-)·watch동사·브랜딩 변형을
// 결정적 파싱보다 견고하게 처리(오너 방침: gemma 활용). 영어복사·문자셋불일치·브랜딩은 제외.
func ottLocaleTitle(ctx context.Context, ex *agents.Base, p ottProvider, id, kdbLoc, ko, etype, enHint string) (string, bool) {
	if _, ok := p.localePath[kdbLoc]; !ok {
		return "", false
	}
	query, want := p.localeQuery(kdbLoc, id)
	res := searxngDefault(ctx, query)
	var hits []string
	for _, r := range res {
		if strings.Contains(r.URL, want) {
			hits = append(hits, strings.TrimSpace(r.Title+" "+r.Snippet))
		}
	}
	if len(hits) == 0 {
		return "", false // 그 locale 에 그 작품 없음(지역 미가용) → 빈칸 유지
	}
	out, err := localFillVote(ctx, ex, localFillEntity{ko: ko, etype: etype, en: enHint}, kdbLoc, hits)
	if err != nil {
		return "", false
	}
	title := strings.TrimSpace(out.Native)
	if title == "" || strings.Contains(title, p.brand) {
		return "", false
	}
	// ★영어leak 차단: ja/zh_hant/vi/es 현지제목은 non-ASCII(한자·발음부호) 여야 한다.
	// 순수 ASCII = distributor 가 그 지역에 영어로 서빙한 제목(My First First Love 등) → 거부.
	if isPureASCII(title) {
		return "", false
	}
	if enHint != "" && strings.EqualFold(title, strings.TrimSpace(enHint)) {
		return "", false // 영어복사 = leak, 빈칸 유지가 나음
	}
	if !IsValidSpellingForLocale(kdbLoc, title) {
		return "", false
	}
	return title, true
}

// drainOTT — provider 의 지역 공식제목으로 작품(codex/빈칸 locale)을 채운다(공통 드레인).
// source=p.name. can_replace 가드로 상위소스 보존. 7d 쿨다운(field=p.cooldownField).
// ★매 조회 사이 p.interval() pacing — 절대 벌크 아님.
func drainOTT(ctx context.Context, pool *pgxpool.Pool, p ottProvider, n int, koFilter string) (processed, filled int) {
	if pool == nil || n <= 0 {
		return 0, 0
	}
	// koFilter 비면 배치(codex locale·쿨다운 밖), 있으면 그 작품 1건(검증용·쿨다운 무시).
	q := `
SELECT id, canonical_ko, entity_type::text, COALESCE(canonical_en,'')
  FROM kwave_entities e
 WHERE status='active' AND operator_locked=false
   AND entity_type IN ('drama','show','movie')
   -- ★codex-fallback locale 을 가진 작품 전체(TMDb/Wikidata 보유 무관). distributor 작품은
   -- TMDb도 있으므로 무소스로 한정하면 정작 채울 작품을 배제한다. per-locale 가드
   -- (curSrc 빈칸/codex 만 교체)가 wikidata/tmdb 값을 보존하므로 포함이 안전.
   AND 'codex-fallback' IN (canonical_ja_source,canonical_zh_hant_source,canonical_vi_source,
                            canonical_id_source,canonical_es_source,canonical_pt_br_source)
   AND NOT EXISTS(SELECT 1 FROM kwave_kdb_enrich_attempts a WHERE a.entity_id=e.id
                  AND a.field=$2 AND a.last_attempt_at > now() - interval '7 days')
   -- 시즌 엔티티 제외: distributor 는 시리즈명(시즌 표기 없음)을 줘 codex의 "...시즌2"보다 덜 구체적.
   AND canonical_ko !~ '시즌|시리즈|시즌제'
 ORDER BY updated_at DESC
 LIMIT $1`
	args := []any{n, p.cooldownField}
	if strings.TrimSpace(koFilter) != "" {
		q = `SELECT id, canonical_ko, entity_type::text, COALESCE(canonical_en,'') FROM kwave_entities
		      WHERE canonical_ko=$1 AND status='active' AND entity_type IN ('drama','show','movie')`
		args = []any{strings.TrimSpace(koFilter)}
	}
	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		log.Printf("kdb.ott[%s]: select: %v", p.name, err)
		return 0, 0
	}
	type work struct {
		id            uuid.UUID
		ko, etype, en string
	}
	var works []work
	for rows.Next() {
		var w work
		if rows.Scan(&w.id, &w.ko, &w.etype, &w.en) == nil {
			works = append(works, w)
		}
	}
	rows.Close()

	ex := newLocalFillExtractor() // gemma 추출기(현지제목 추출용)

	for _, w := range works {
		processed++
		time.Sleep(p.interval()) // ID 조회 전 pacing
		id, ok := p.resolveID(ctx, w.ko)
		_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_enrich_attempts (entity_id, field, attempts, last_attempt_at, last_source)
VALUES ($1,$2,1,now(),$3)
ON CONFLICT (entity_id, field) DO UPDATE SET attempts=kwave_kdb_enrich_attempts.attempts+1, last_attempt_at=now()`,
			w.id, p.cooldownField, p.name)
		if !ok {
			continue
		}
		// ★2026-07-23 Phase1 잔여분: ko-제목 앵커 통과한 타이틀 ID 를 external_ref 로
		// 기록 — officialPromotionProviders 의 netflix/disney 가 처음으로 실제 발동
		// 가능해진다(07-22 감사: writer 부재로 죽은 신뢰토큰이던 것).
		recordOTTAnchor(ctx, pool, w.id, p, id)
		for kdbLoc, col := range ottLocaleCol {
			var curSrc string
			if pool.QueryRow(ctx,
				fmt.Sprintf("SELECT COALESCE(%s_source,'') FROM kwave_entities WHERE id=$1", col),
				w.id).Scan(&curSrc) != nil {
				continue
			}
			if curSrc != "" && curSrc != "codex-fallback" {
				continue // 빈칸 또는 codex 만 교체 대상
			}
			if _, supp := p.localePath[kdbLoc]; !supp {
				continue // 이 distributor 가 미지원하는 locale — 네트워크/sleep 없이 즉시 스킵
			}
			time.Sleep(p.interval()) // locale 조회 전 pacing(지원 locale 만)
			title, ok := ottLocaleTitle(ctx, ex, p, id, kdbLoc, w.ko, w.etype, w.en)
			if !ok {
				continue
			}
			var applied bool
			err := pool.QueryRow(ctx, fmt.Sprintf(`
UPDATE kwave_entities SET %[1]s=$2, %[1]s_source=$3, updated_at=now()
 WHERE id=$1 AND (%[1]s IS NULL OR %[1]s=''
        OR can_replace_canonical(operator_locked, COALESCE(%[1]s_source,''),$3))
 RETURNING true`, col), w.id, title, p.name).Scan(&applied)
			if err == nil && applied {
				filled++
				log.Printf("kdb.ott[%s]: %q[%s] <- %q (id=%s)", p.name, w.ko, kdbLoc, title, id)
			}
		}
	}
	if processed > 0 {
		log.Printf("kdb.ott[%s]: drain processed=%d filled=%d", p.name, processed, filled)
	}
	return processed, filled
}

// DrainNetflixWorks — 넷플릭스 단독 드레인(drainOTT 래퍼, 수동/디버그용).
func DrainNetflixWorks(ctx context.Context, pool *pgxpool.Pool, n int, koFilter string) (processed, filled int) {
	return drainOTT(ctx, pool, netflixProvider, n, koFilter)
}

// recordOTTAnchor — ko-제목 앵커 통과한 distributor 타이틀 ID 를 승급앵커 ref 로 기록.
func recordOTTAnchor(ctx context.Context, pool *pgxpool.Pool, entityID uuid.UUID, p ottProvider, id string) {
	url := "https://www.netflix.com/title/" + id
	if p.name == "disney" {
		url = "https://www.disneyplus.com/browse/entity-" + id
	}
	_, _ = pool.Exec(ctx, `
INSERT INTO kwave_entity_external_refs (entity_id, provider, external_id, url, confidence, raw_payload, fetched_at)
VALUES ($1,$2,$3,$4,0.75,'{"anchor":"ko-title"}',now())
ON CONFLICT DO NOTHING`, entityID, p.name, id, url)
}

// DrainOTTCandidatePromotions — drama/show/movie candidate 를 넷플릭스 타이틀 ID
// (ko-제목 앵커)로 확인해 승급한다 (2026-07-23 Phase1 잔여분 — TMDb 실패분 보완축:
// 넷플릭스 오리지널/독점작은 TMDb 등재가 늦거나 없을 수 있다). SearXNG 의존이라
// 소량(기본 4)·30d 쿨다운. 반환 (promoted, checked).
func DrainOTTCandidatePromotions(ctx context.Context, pool *pgxpool.Pool, n int) (promoted, checked int) {
	if pool == nil || n <= 0 {
		return 0, 0
	}
	rows, err := pool.Query(ctx, `
SELECT id, canonical_ko FROM kwave_entities e
 WHERE status='candidate' AND operator_locked=false
   AND entity_type IN ('drama','show','movie')
   AND char_length(canonical_ko) BETWEEN 2 AND 60
   AND canonical_ko !~ '시즌|시리즈'
   AND NOT EXISTS(SELECT 1 FROM kwave_entity_external_refs r
                  WHERE r.entity_id=e.id AND r.provider IN ('netflix','disney'))
   AND NOT EXISTS(SELECT 1 FROM kwave_kdb_enrich_attempts a WHERE a.entity_id=e.id
                  AND a.field='ott-cand' AND a.last_attempt_at > now() - interval '30 days')
 ORDER BY updated_at DESC
 LIMIT $1`, n)
	if err != nil {
		return 0, 0
	}
	type cand struct {
		id uuid.UUID
		ko string
	}
	var cs []cand
	for rows.Next() {
		var c cand
		if rows.Scan(&c.id, &c.ko) == nil {
			cs = append(cs, c)
		}
	}
	rows.Close()

	for _, c := range cs {
		if ctx.Err() != nil {
			break
		}
		checked++
		time.Sleep(ottInterval())
		id, ok := resolveNetflixID(ctx, c.ko)
		_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_enrich_attempts (entity_id, field, attempts, last_attempt_at, last_source)
VALUES ($1,'ott-cand',1,now(),$2)
ON CONFLICT (entity_id, field) DO UPDATE SET attempts=kwave_kdb_enrich_attempts.attempts+1,
    last_attempt_at=now(), last_source=EXCLUDED.last_source`,
			c.id, map[bool]string{true: "applied", false: "no_match"}[ok])
		if !ok {
			continue
		}
		recordOTTAnchor(ctx, pool, c.id, netflixProvider, id)
		tag, uerr := pool.Exec(ctx, `
UPDATE kwave_entities
   SET status='active', confidence=GREATEST(confidence,0.75),
       notes = COALESCE(NULLIF(notes,'') || ' · ','') || 'netflix 타이틀 ID(ko-제목 앵커) 확인 승급',
       updated_at=now()
 WHERE id=$1 AND status='candidate' AND operator_locked=false`, c.id)
		if uerr == nil && tag.RowsAffected() == 1 {
			promoted++
			log.Printf("kdb.ott[netflix]: candidate 승급 %q (title/%s)", c.ko, id)
		}
	}
	return promoted, checked
}

// ottCascadeChain — OTT 폴백 체인(오너 설계). 작품 locale 을 이 순서로 시도하고 첫 적중에서
// 멈춘다. ★전체 시스템 캐스케이드 = Wikidata → TMDb/KMDb(=enrich 상위레이어; locale 이
// codex-fallback 이면 이 셋이 이미 실패한 것. KMDb 는 KR/EN 만이라 외국어 locale 엔 무용) →
// **Disney → Netflix**(이 함수) → codex-fallback(최후). 즉 OTT 구간 = Disney 먼저, 없으면 Netflix.
var ottCascadeChain = []ottProvider{disneyProvider, netflixProvider}

// ottCascadeLocales — OTT 가 채울 수 있는 locale(비-ASCII 가드 가능). 체인 중 한 provider 라도
// 지원하면 대상. (ja/vi=netflix, es/zh_hant=양쪽, ...)
func ottProviderSupports(p ottProvider, loc string) bool { _, ok := p.localePath[loc]; return ok }

// DrainOTTCascade — 작품의 빈/codex locale 을 Disney→Netflix 폴백 체인으로 채운다(첫 적중 정지).
// 한 작품을 1회 방문해 필요한 locale 만 체인으로 시도 → provider 별 중복 드레인/쿨다운 제거.
// 쿨다운 field='ottcascade'(기존 ottfill/disneyfill 과 별개 → stale 쿨다운 우회). 7d.
// ★매 조회 사이 provider.interval() pacing — 절대 벌크 아님.
func DrainOTTCascade(ctx context.Context, pool *pgxpool.Pool, n int, koFilter string) (processed, filled int) {
	if pool == nil || n <= 0 {
		return 0, 0
	}
	q := `
SELECT id, canonical_ko, entity_type::text, COALESCE(canonical_en,'')
  FROM kwave_entities e
 WHERE status='active' AND operator_locked=false AND entity_type IN ('drama','show','movie')
   AND ( 'codex-fallback' IN (canonical_ja_source,canonical_vi_source,canonical_es_source,canonical_zh_hant_source)
         OR canonical_ja='' OR canonical_vi='' OR canonical_es='' OR canonical_zh_hant='' )
   AND NOT EXISTS(SELECT 1 FROM kwave_kdb_enrich_attempts a WHERE a.entity_id=e.id
                  AND a.field='ottcascade' AND a.last_attempt_at > now() - interval '7 days')
   AND canonical_ko !~ '시즌|시리즈|시즌제'
 ORDER BY updated_at DESC
 LIMIT $1`
	args := []any{n}
	if strings.TrimSpace(koFilter) != "" {
		q = `SELECT id, canonical_ko, entity_type::text, COALESCE(canonical_en,'') FROM kwave_entities
		      WHERE canonical_ko=$1 AND status='active' AND entity_type IN ('drama','show','movie')`
		args = []any{strings.TrimSpace(koFilter)}
	}
	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		log.Printf("kdb.ott[cascade]: select: %v", err)
		return 0, 0
	}
	type work struct {
		id            uuid.UUID
		ko, etype, en string
	}
	var works []work
	for rows.Next() {
		var w work
		if rows.Scan(&w.id, &w.ko, &w.etype, &w.en) == nil {
			works = append(works, w)
		}
	}
	rows.Close()

	ex := newLocalFillExtractor()

	for _, w := range works {
		processed++
		// 채움 필요한 locale(빈칸 또는 codex)만 — ottLocaleCol 중 체인이 지원하는 것.
		need := map[string]string{} // loc -> col
		for loc, col := range ottLocaleCol {
			supported := false
			for _, p := range ottCascadeChain {
				if ottProviderSupports(p, loc) {
					supported = true
					break
				}
			}
			if !supported {
				continue
			}
			var curVal, curSrc string
			if pool.QueryRow(ctx, fmt.Sprintf(
				"SELECT COALESCE(%[1]s,''), COALESCE(%[1]s_source,'') FROM kwave_entities WHERE id=$1", col),
				w.id).Scan(&curVal, &curSrc) != nil {
				continue
			}
			if curVal == "" || curSrc == "codex-fallback" {
				need[loc] = col
			}
		}
		// 쿨다운 기록(방문 1회) — 적중 여부 무관.
		_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_enrich_attempts (entity_id, field, attempts, last_attempt_at, last_source)
VALUES ($1,'ottcascade',1,now(),'ott')
ON CONFLICT (entity_id, field) DO UPDATE SET attempts=kwave_kdb_enrich_attempts.attempts+1, last_attempt_at=now()`, w.id)
		if len(need) == 0 {
			continue
		}
		// 폴백 체인: Disney → Netflix. 남은 locale 이 있을 때만 다음 provider 로.
		for _, p := range ottCascadeChain {
			if len(need) == 0 {
				break
			}
			// 이 provider 가 아직 필요한 locale 을 하나라도 지원?
			useful := false
			for loc := range need {
				if ottProviderSupports(p, loc) {
					useful = true
					break
				}
			}
			if !useful {
				continue
			}
			time.Sleep(p.interval()) // ID 조회 pacing
			id, ok := p.resolveID(ctx, w.ko)
			if !ok {
				continue // 이 provider 에 그 작품 없음 → 다음 provider
			}
			recordOTTAnchor(ctx, pool, w.id, p, id) // 승급앵커 ref(2026-07-23)
			for loc, col := range need {
				if !ottProviderSupports(p, loc) {
					continue
				}
				time.Sleep(p.interval()) // locale 조회 pacing
				title, ok := ottLocaleTitle(ctx, ex, p, id, loc, w.ko, w.etype, w.en)
				if !ok {
					continue
				}
				var applied bool
				err := pool.QueryRow(ctx, fmt.Sprintf(`
UPDATE kwave_entities SET %[1]s=$2, %[1]s_source=$3, updated_at=now()
 WHERE id=$1 AND (%[1]s IS NULL OR %[1]s=''
        OR can_replace_canonical(operator_locked, COALESCE(%[1]s_source,''),$3))
 RETURNING true`, col), w.id, title, p.name).Scan(&applied)
				if err == nil && applied {
					filled++
					delete(need, loc) // 이 locale 캐스케이드 정지(첫 적중)
					log.Printf("kdb.ott[cascade]: %q[%s] <- %s %q (id=%s)", w.ko, loc, p.name, title, id)
				}
			}
		}
	}
	if processed > 0 {
		log.Printf("kdb.ott[cascade]: processed=%d filled=%d", processed, filled)
	}
	return processed, filled
}

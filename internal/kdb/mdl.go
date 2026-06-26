package kdb

// MyDramaList(MDL) 현지제목 그라운딩 — 작품(drama/show/movie)의 ja(일본어) 공식/통용
// 제목을 MDL "Also Known As" 목록에서 확보한다. 넷플릭스 OTT(ott.go)의 자매 소스.
//
// ★왜 ja 만(v1): MDL AKA 는 다국어 평면 목록(유럽어·로마자·CJK 혼재)이다. 일본어는
// 가나(ひらがな/カタカナ) 포함 여부로 결정적·고신뢰 식별이 가능(LLM 불요). 중국어는
// 간체/번체(zh/zh_hant) 구분이 평면목록에서 불확실해 오배치 위험 → v1 제외(빈칸>틀린값).
//
// ★정확도(중요): MDL 검색은 영문제목으로 다른 작품을 반환할 수 있다. 반드시 페이지의
// "Native Title"(한국어 원제)을 우리 canonical_ko 와 정규화 대조해 **앵커**한다. 불일치 시
// 스킵(오매칭 오염 차단 — 넷플릭스 ID-앵커와 동일 철학).
//
// ★IP 차단 방지(오너 방침 "벌크 금지"): 매 HTTP 사이 mdlInterval(기본 4초) pacing. 소량·점진.
// source='mydramalist'(prio 7) — can_replace 로 codex-fallback(8)만 교체, 권위소스 보존.
// 커뮤니티 편집 출처라 verified tier 제외(api.go: verified_only 소비자엔 비노출).

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// mdlHTTP — MDL 직접 fetch 클라이언트. SearXNG 는 MDL 을 색인하지 않아 직접 검색·fetch.
var mdlHTTP = &http.Client{Timeout: 15 * time.Second}

const mdlUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36"

// mdlInterval — MDL 조회 간 최소 간격(IP 차단 방지). 기본 4초.
func mdlInterval() time.Duration {
	if v := os.Getenv("KDB_MDL_MIN_INTERVAL_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Millisecond
		}
	}
	return 4 * time.Second
}

var (
	mdlResultRe = regexp.MustCompile(`href="(/\d+-[a-z0-9-]+)"`)
	mdlNativeRe = regexp.MustCompile(`Native Title:</b>\s*<[^>]+>([^<]+)<`)
	mdlAkaRe    = regexp.MustCompile(`mdl-aka-titles"[^>]*>(.*?)</span>`)
	kanaRe      = regexp.MustCompile(`[\x{3040}-\x{309F}\x{30A0}-\x{30FF}]`) // 히라가나+가타카나
	tagStripRe  = regexp.MustCompile(`<[^>]+>`)
)

// mdlFetch — URL fetch, 본문 문자열. 비200/에러 시 ("", false).
func mdlFetch(ctx context.Context, rawURL string) (string, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("User-Agent", mdlUA)
	req.Header.Set("Accept", "text/html")
	resp, err := mdlHTTP.Do(req)
	if err != nil {
		log.Printf("kdb.mdl: fetch %v", err)
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", false
	}
	return string(b), true
}

// mdlSearchURL — MDL 검색(질의 q)에서 첫 작품 페이지 경로. 없으면 ("", false).
func mdlSearchURL(ctx context.Context, q string) (string, bool) {
	html, ok := mdlFetch(ctx, "https://mydramalist.com/search?q="+urlQueryEscape(q))
	if !ok {
		return "", false
	}
	if m := mdlResultRe.FindStringSubmatch(html); m != nil {
		return "https://mydramalist.com" + m[1], true
	}
	return "", false
}

// urlQueryEscape — 최소 query 이스케이프(net/url 의존 회피·공백/특수문자만).
func urlQueryEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == ' ':
			b.WriteByte('+')
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			for _, by := range []byte(string(r)) {
				fmt.Fprintf(&b, "%%%02X", by)
			}
		}
	}
	return b.String()
}

// mdlParse — 작품 페이지에서 (nativeTitle, akaList) 추출.
func mdlParse(html string) (native string, aka []string) {
	if m := mdlNativeRe.FindStringSubmatch(html); m != nil {
		native = strings.TrimSpace(m[1])
	}
	if m := mdlAkaRe.FindStringSubmatch(html); m != nil {
		raw := tagStripRe.ReplaceAllString(m[1], " ")
		for _, part := range strings.Split(raw, ",") {
			if t := strings.TrimSpace(part); t != "" {
				aka = append(aka, t)
			}
		}
	}
	return native, aka
}

// mdlNormTitle — 제목 앵커 비교용 정규화: 공백·구두점 제거, 소문자.
func mdlNormTitle(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r >= 0xAC00 && r <= 0xD7A3: // 한글 음절
			b.WriteRune(r)
		case r >= 0x4E00 && r <= 0x9FFF: // CJK
			b.WriteRune(r)
		default:
			// 공백·구두점·발음부호 등 제거
		}
	}
	return b.String()
}

// mdlAnchorOK — MDL native title 이 우리 canonical_ko 와 충분히 일치하는가(오매칭 차단).
// 정규화 후 동일 또는 한쪽이 다른쪽을 포함(부제·외전 표기차 허용)하면 OK.
func mdlAnchorOK(ko, native string) bool {
	a, b := mdlNormTitle(ko), mdlNormTitle(native)
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	// 짧은 쪽이 4자 이상이고 긴 쪽에 포함되면 동일작 변형으로 간주(예: "킹덤아신전"⊂"킹덤외전아신"? — 아님)
	short, long := a, b
	if len(short) > len(long) {
		short, long = long, short
	}
	return len([]rune(short)) >= 4 && strings.Contains(long, short)
}

// mdlExtractJA — AKA 목록에서 일본어 제목(가나 포함)을 결정적으로 추출.
// 가나가 들어간 첫 항목 = 일본어. 가드(문자셋·영어복사) 통과해야 반환.
func mdlExtractJA(aka []string, enHint string) (string, bool) {
	for _, t := range aka {
		if !kanaRe.MatchString(t) {
			continue // 가나 없음 = 일본어 아님(유럽어·로마자·중국어)
		}
		t = strings.TrimSpace(t)
		if t == "" || isPureASCII(t) {
			continue
		}
		if enHint != "" && strings.EqualFold(t, strings.TrimSpace(enHint)) {
			continue
		}
		if !IsValidSpellingForLocale("ja", t) {
			continue
		}
		return t, true
	}
	return "", false
}

// DrainMDLWorks — 작품의 codex/빈칸 ja 를 MDL "Also Known As" 일본어 제목으로 채운다.
// koFilter 비면 배치(codex ja·쿨다운 밖), 있으면 그 작품 1건(검증용·쿨다운 무시).
// ★매 HTTP 사이 mdlInterval pacing. source='mydramalist'(prio 7, codex 만 교체).
func DrainMDLWorks(ctx context.Context, pool *pgxpool.Pool, n int, koFilter string) (processed, filled int) {
	if pool == nil || n <= 0 {
		return 0, 0
	}
	q := `
SELECT id, canonical_ko, entity_type::text, COALESCE(canonical_en,''), COALESCE(canonical_ja,''), COALESCE(canonical_ja_source,'')
  FROM kwave_entities e
 WHERE status='active' AND operator_locked=false
   AND entity_type IN ('drama','show','movie')
   -- ja 가 비었거나 codex-fallback 인 작품(권위소스 ja 는 건드리지 않음)
   AND (canonical_ja='' OR canonical_ja IS NULL OR canonical_ja_source='codex-fallback')
   AND NOT EXISTS(SELECT 1 FROM kwave_kdb_enrich_attempts a WHERE a.entity_id=e.id
                  AND a.field='mdlfill' AND a.last_attempt_at > now() - interval '7 days')
   AND canonical_ko !~ '시즌|시리즈|시즌제'
 ORDER BY updated_at DESC
 LIMIT $1`
	args := []any{n}
	if strings.TrimSpace(koFilter) != "" {
		q = `SELECT id, canonical_ko, entity_type::text, COALESCE(canonical_en,''), COALESCE(canonical_ja,''), COALESCE(canonical_ja_source,'')
		      FROM kwave_entities WHERE canonical_ko=$1 AND status='active' AND entity_type IN ('drama','show','movie')`
		args = []any{strings.TrimSpace(koFilter)}
	}
	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		log.Printf("kdb.mdl: select: %v", err)
		return 0, 0
	}
	type work struct {
		id                          uuid.UUID
		ko, etype, en, curJa, curSrc string
	}
	var works []work
	for rows.Next() {
		var w work
		if rows.Scan(&w.id, &w.ko, &w.etype, &w.en, &w.curJa, &w.curSrc) == nil {
			works = append(works, w)
		}
	}
	rows.Close()

	for _, w := range works {
		processed++
		// 검색 질의: 영문제목 우선(MDL slug 는 영문), 없으면 한국어.
		query := w.en
		if strings.TrimSpace(query) == "" {
			query = w.ko
		}
		time.Sleep(mdlInterval())
		pageURL, ok := mdlSearchURL(ctx, query)
		// 시도 기록(쿨다운)
		_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_enrich_attempts (entity_id, field, attempts, last_attempt_at, last_source)
VALUES ($1,'mdlfill',1,now(),'mydramalist')
ON CONFLICT (entity_id, field) DO UPDATE SET attempts=kwave_kdb_enrich_attempts.attempts+1, last_attempt_at=now()`, w.id)
		if !ok {
			continue
		}
		time.Sleep(mdlInterval())
		html, ok := mdlFetch(ctx, pageURL)
		if !ok {
			continue
		}
		native, aka := mdlParse(html)
		if !mdlAnchorOK(w.ko, native) {
			log.Printf("kdb.mdl: anchor mismatch %q vs MDL %q (%s) — skip", w.ko, native, pageURL)
			continue
		}
		title, ok := mdlExtractJA(aka, w.en)
		if !ok {
			continue
		}
		var applied bool
		err := pool.QueryRow(ctx, `
UPDATE kwave_entities SET canonical_ja=$2, canonical_ja_source='mydramalist', updated_at=now()
 WHERE id=$1 AND (canonical_ja IS NULL OR canonical_ja=''
        OR can_replace_canonical(operator_locked, COALESCE(canonical_ja_source,''),'mydramalist'))
 RETURNING true`, w.id, title).Scan(&applied)
		if err == nil && applied {
			filled++
			log.Printf("kdb.mdl: %q[ja] <- mydramalist %q (%s)", w.ko, title, pageURL)
		}
	}
	if processed > 0 {
		log.Printf("kdb.mdl: DrainMDLWorks processed=%d filled=%d", processed, filled)
	}
	return processed, filled
}

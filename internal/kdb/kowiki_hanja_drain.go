package kdb

// kowiki_hanja_drain — 한국어 위키백과 첫 문장의 **한자 병기**를 번체 칸에 넣는다.
//
// ★왜 (2026-09-23). 중국어 빈칸을 메울 공급원을 전부 재 봤는데 기관류에서 그나마 이것이
//   제일 나았다 — 표적 185곳 중 22곳(12%)에 한자가 있었다. 한국 기관의 중국어 표기는
//   실제로 그 한자다(한국도로공사 韓國道路公社 · 신용보증기금 信用保證基金).
//   잠정값(llm-provisional)보다 위 등급이라 **이미 채운 잠정값을 고치는** 레인이기도 하다.
//
// ★같은 실측에서 배운 함정 셋. 전부 «다른 대상의 한자»를 가져오는 길이다.
//
//	해병대 → 海兵隊        일반 개념 문서다(중국어는 海军陆战队). 짧은 이름일수록 위험하다
//	신협   → 信用協同組合  리다이렉트가 일반 개념 문서로 데려간다
//	하동군청 → 河東郡      리다이렉트가 군(郡) 문서로 데려간다
//
//   그래서 두 길만 연다:
//     ① 앵커가 있으면 **그 앵커의 kowiki 문서**를 쓴다 — 동일인·동일 기관이 보장된다.
//     ② 앵커가 없으면 **제목이 정확히 같은** 문서만 쓰고, 한자가 네 글자 이상일 때만 쓴다
//        (기관명은 길다. 짧은 것은 일반 개념일 확률이 높다).
//
// ★사람은 앵커가 있을 때만. 동명이인이 가장 흔한 자리다.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
)

// kowikiAPI — 한국어 위키백과 API. 무키·공개. UA 에 연락처를 넣는 것이 규약이다.
const kowikiAPI = "https://ko.wikipedia.org/w/api.php"

const kowikiUA = "KDB-entity-db/1.0 (https://atikar.com; contact admin) kowiki-hanja"

var (
	// kowikiHanjaOnly — 한자(+ 인명 가운뎃점)만. 한글·라틴이 섞이면 한자 이름이 아니다.
	kowikiHanjaOnly = regexp.MustCompile(`^[\p{Han}·]{2,20}$`)
	kowikiSplitRE   = regexp.MustCompile(`[,，;；]`)
)

// kowikiLead — 문서 제목 → 첫 문단(평문). 없으면 "".
func kowikiLead(ctx context.Context, cl *http.Client, title string) (string, string, error) {
	q := url.Values{}
	q.Set("action", "query")
	q.Set("prop", "extracts|pageprops")
	q.Set("exintro", "1")
	q.Set("explaintext", "1")
	q.Set("redirects", "1")
	q.Set("titles", title)
	q.Set("format", "json")
	q.Set("ppprop", "disambiguation")
	q.Set("maxlag", "5")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, kowikiAPI+"?"+q.Encode(), nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", kowikiUA)
	resp, err := cl.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("kowiki: status %d", resp.StatusCode)
	}
	var body struct {
		Query struct {
			Pages map[string]struct {
				Title     string            `json:"title"`
				Extract   string            `json:"extract"`
				Missing   *string           `json:"missing"`
				PageProps map[string]string `json:"pageprops"`
			} `json:"pages"`
		} `json:"query"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body); err != nil {
		return "", "", err
	}
	for _, p := range body.Query.Pages {
		if p.Missing != nil {
			return "", "", nil
		}
		if _, dis := p.PageProps["disambiguation"]; dis {
			return "", "", nil // 동음이의 목록 — 어느 대상인지 말할 수 없다
		}
		return p.Title, p.Extract, nil
	}
	return "", "", nil
}

// kowikiHanjaFrom — 첫 문장이 「이름(漢字, …)」 모양일 때 그 한자. 순수 함수.
//
// minLen 은 «앵커 없이 제목만 맞은» 경우의 안전장치다(기관명은 길다 — 짧은 한자는
// 일반 개념 문서일 확률이 높다: 해병대 海兵隊).
func kowikiHanjaFrom(title, ko, lead string, minLen int) string {
	lead = strings.TrimSpace(lead)
	if lead == "" {
		return ""
	}
	for _, name := range []string{title, ko} {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		re := regexp.MustCompile(`^` + regexp.QuoteMeta(name) + `\s*[\(（]([^)）]*)[\)）]`)
		m := re.FindStringSubmatch(lead)
		if m == nil {
			continue
		}
		first := strings.TrimSpace(kowikiSplitRE.Split(m[1], 2)[0])
		if !kowikiHanjaOnly.MatchString(first) {
			continue
		}
		if len([]rune(strings.ReplaceAll(first, "·", ""))) < minLen {
			continue
		}
		return first
	}
	return ""
}

// kowikiTitleIsOurs — 문서 제목이 우리 정본·별칭과 같은가. 괄호 꼬리는 뗀다
// (「송골매 (밴드)」). 앵커가 틀린 대상을 가리킬 때 그 대상의 한자를 가져오는 것을 막는
// 마지막 관문이다 — 순수 함수라 시험이 규칙을 고정한다.
func kowikiTitleIsOurs(title, ko, aliasesPipe string) bool {
	t := itunesNormTitle(kowikiStripParen(title))
	if t == "" {
		return false
	}
	if t == itunesNormTitle(ko) {
		return true
	}
	for _, a := range strings.Split(aliasesPipe, "|") {
		if a = strings.TrimSpace(a); a != "" && itunesNormTitle(a) == t {
			return true
		}
	}
	return false
}

var kowikiParenTailRE = regexp.MustCompile(`\s*[\(（][^)）]*[\)）]\s*$`)

func kowikiStripParen(s string) string {
	return strings.TrimSpace(kowikiParenTailRE.ReplaceAllString(strings.TrimSpace(s), ""))
}

// kowikiTitleFromURL — 사이트링크 URL 에서 문서 제목.
func kowikiTitleFromURL(u string) string {
	i := strings.LastIndex(u, "/wiki/")
	if i < 0 {
		return ""
	}
	t, err := url.PathUnescape(u[i+len("/wiki/"):])
	if err != nil {
		return ""
	}
	return strings.ReplaceAll(t, "_", " ")
}

// kowikiHanjaTypes — 앵커 없이도 제목 일치만으로 여는 유형(기관류). 이름이 길고 고유하다.
var kowikiHanjaTypes = map[string]bool{
	"company": true, "organization": true, "government_body": true,
	"agency": true, "school": true, "brand_place": true,
}

// DrainKowikiHanja — 번체 빈칸·기계값을 한국어 위키백과의 한자 병기로 채운다.
func DrainKowikiHanja(ctx context.Context, pool *pgxpool.Pool, wd *wikidata.Client, limit int, dry bool) (applied, checked int) {
	if pool == nil || limit <= 0 {
		return 0, 0
	}
	rows, err := pool.Query(ctx, `
WITH rq AS (
  SELECT term_ko, count(*) n FROM kwave_kdb_request_terms
   WHERE created_at > now() - interval '14 days' AND origin IN ('prepare','lookup') GROUP BY 1)
SELECT e.id::text, e.canonical_ko, e.entity_type::text,
       COALESCE((SELECT r.external_id FROM kwave_entity_external_refs r
                  WHERE r.entity_id=e.id AND r.provider='wikidata' LIMIT 1),''),
       COALESCE(array_to_string(e.aliases_ko,'|'),'')
  FROM kwave_entities e
  LEFT JOIN rq ON rq.term_ko = e.canonical_ko
 WHERE e.status='active' AND e.operator_locked=false
   AND (COALESCE(e.canonical_zh_hant,'')='' OR COALESCE(e.canonical_zh_hant_source,'') = ANY($2))
   AND (e.entity_type::text IN ('company','organization','government_body','agency','school','brand_place')
        OR (e.entity_type::text IN ('person','group')
            AND EXISTS(SELECT 1 FROM kwave_entity_external_refs r2
                        WHERE r2.entity_id=e.id AND r2.provider='wikidata')))
   AND NOT EXISTS(SELECT 1 FROM kwave_kdb_enrich_attempts a
                  WHERE a.entity_id=e.id AND a.field='kowiki-hanja'
                    AND a.last_attempt_at > now() - interval '60 days')
 ORDER BY COALESCE(rq.n,0) DESC, e.updated_at DESC
 LIMIT $1`, limit, MachineFilledSourcesWeakerThan(SourceKoWikiHanja))
	if err != nil {
		return 0, 0
	}
	type item struct{ id, ko, et, qid, aliases string }
	var items []item
	for rows.Next() {
		var it item
		if rows.Scan(&it.id, &it.ko, &it.et, &it.qid, &it.aliases) == nil {
			items = append(items, it)
		}
	}
	rows.Close()

	cl := &http.Client{Timeout: 15 * time.Second}
	reasons := map[string]int{}
	for _, it := range items {
		if ctx.Err() != nil {
			break
		}
		title, minLen := it.ko, 4
		if it.qid != "" && wd != nil {
			if ent, err := wd.Fetch(ctx, it.qid); err == nil && ent != nil {
				if t := kowikiTitleFromURL(ent.Sitelinks["kowiki"]); t != "" {
					title, minLen = t, 2 // 앵커가 동일 대상을 보증한다
				} else if !kowikiHanjaTypes[it.et] {
					reasons["앵커에 kowiki 문서 없음"]++
					recordKowikiHanjaAttempt(ctx, pool, it.id, "no_kowiki")
					continue
				}
			}
			time.Sleep(200 * time.Millisecond)
		} else if !kowikiHanjaTypes[it.et] {
			reasons["앵커 없음(사람·그룹)"]++
			recordKowikiHanjaAttempt(ctx, pool, it.id, "no_anchor")
			continue
		}
		checked++
		got, lead, err := kowikiLead(ctx, cl, title)
		time.Sleep(400 * time.Millisecond) // 위키백과 예의(429 를 실제로 맞았다)
		if err != nil {
			continue // transient — 쿨다운 없이 다음 회차
		}
		if got == "" {
			reasons["문서 없음·동음이의"]++
			recordKowikiHanjaAttempt(ctx, pool, it.id, "no_page")
			continue
		}
		// ★문서 제목이 **우리 이름**이어야 한다 (2026-09-23 운영 첫 회차에 잡았다).
		//   권성준(나폴리 맛피아)의 앵커가 다른 사람(남성훈)을 가리켰고, 레인은 그
		//   문서의 한자 南星薰 을 그대로 가져왔다. 앵커가 틀릴 수 있으니 앵커를 믿되
		//   **이름까지 같은지** 본다. 괄호 꼬리(「송골매 (밴드)」)만 떼고 비교한다.
		//   기관은 리다이렉트도 막는다 — 신협→신용협동조합, 하동군청→하동군.
		if !kowikiTitleIsOurs(got, it.ko, it.aliases) {
			reasons["다른 문서"]++
			recordKowikiHanjaAttempt(ctx, pool, it.id, "other_page")
			continue
		}
		hanja := kowikiHanjaFrom(got, it.ko, lead, minLen)
		if hanja == "" {
			reasons["한자 병기 없음"]++
			recordKowikiHanjaAttempt(ctx, pool, it.id, "no_hanja")
			continue
		}
		if !IsValidSpellingForLocale("zh_hant", hanja) {
			reasons["문자셋 거부"]++
			recordKowikiHanjaAttempt(ctx, pool, it.id, "charset")
			continue
		}
		if dry {
			applied++
			continue
		}
		tag, upErr := pool.Exec(ctx, `
UPDATE kwave_entities
   SET canonical_zh_hant=$2, canonical_zh_hant_source='kowiki-hanja', updated_at=now()
 WHERE id=$1 AND status='active' AND operator_locked=false
   AND (COALESCE(canonical_zh_hant,'')='' OR COALESCE(canonical_zh_hant_source,'') = ANY($3))`,
			it.id, hanja, MachineFilledSourcesWeakerThan(SourceKoWikiHanja))
		if upErr != nil || tag.RowsAffected() != 1 {
			continue
		}
		applied++
		recordKowikiHanjaAttempt(ctx, pool, it.id, "applied")
	}
	reasons["못 채움"] = checked - applied
	RecordCounts(ctx, pool, "kowiki-hanja", dry, checked, applied, reasons)
	return applied, checked
}

func recordKowikiHanjaAttempt(ctx context.Context, pool *pgxpool.Pool, id, outcome string) {
	_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_enrich_attempts (entity_id, field, attempts, last_attempt_at, last_source)
VALUES ($1,'kowiki-hanja',1,now(),$2)
ON CONFLICT (entity_id, field) DO UPDATE
SET attempts=kwave_kdb_enrich_attempts.attempts+1, last_attempt_at=now(), last_source=EXCLUDED.last_source`, id, outcome)
}

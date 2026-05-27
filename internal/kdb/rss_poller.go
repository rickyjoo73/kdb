// Package kdb — RSS poller (30분 cron).
//
// 운영자 directive 2026-05-25:
//
//	"우리는 이것만 30분 단위로 각각 다른 사이트별로 수집하여
//	 여기서 이들이 어떻게 사용하는지 아이디어를 얻고 우리 디비에 저장하고 버리는것이다"
//
// 흐름:
//  1. kwave_news_whitelist 에서 enabled=true + rss_url IS NOT NULL 매체 조회
//  2. 매체별 직렬 GET rss_url (UA = 'mediafine-kdb/1.0', timeout 10s)
//  3. RSS 2.0 / Atom 1.0 parse → FeedItem[]
//  4. EntityIndex (in-memory) 로 cheap-gate substring 매칭
//  5. 1개 이상 hit → ExtractRequest enqueue (다음 단계 Gemma 추출)
//  6. 0개 hit + 매체 category='kpop'/'k-content' → CandidateObservation
//  7. poll cycle audit → kwave_kdb_poll_cycles
//
// 외부 모듈 추가 X (encoding/xml + net/http 표준 lib).
package kdb

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Poller — KDB RSS poll orchestrator.
type Poller struct {
	Pool       *pgxpool.Pool
	HTTPClient *http.Client
	UserAgent  string
}

// NewPoller — 기본 클라이언트 (10s timeout, 1 connection per host).
func NewPoller(pool *pgxpool.Pool) *Poller {
	return &Poller{
		Pool: pool,
		HTTPClient: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				MaxIdleConnsPerHost: 1,
			},
		},
		UserAgent: "mediafine-kdb/1.0 ( rickyjoo@aiinad.com )",
	}
}

// Feed — kwave_news_whitelist 한 매체.
type Feed struct {
	Domain   string
	Locale   string
	Category string
	RSSURL   string
}

// LoadFeeds — RSS URL 등록된 enabled 매체 SELECT.
func (p *Poller) LoadFeeds(ctx context.Context) ([]Feed, error) {
	rows, err := p.Pool.Query(ctx, `
SELECT domain, locale, COALESCE(category,''), rss_url
FROM kwave_news_whitelist
WHERE enabled = true
  AND rss_url IS NOT NULL AND rss_url <> ''
ORDER BY locale, domain`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Feed
	for rows.Next() {
		var f Feed
		if err := rows.Scan(&f.Domain, &f.Locale, &f.Category, &f.RSSURL); err != nil {
			continue
		}
		out = append(out, f)
	}
	return out, nil
}

// PollOnce — 1 cycle 실행. 모든 enabled 매체 직렬 poll.
//
// 정공법:
//   - 매체 간 100ms 간격 (공격적 polling X).
//   - 각 매체 실패는 다른 매체에 영향 X.
//   - 모든 audit 은 kwave_kdb_poll_cycles + last_polled UPDATE.
func (p *Poller) PollOnce(ctx context.Context) {
	cycleID, err := p.beginCycle(ctx)
	if err != nil {
		log.Printf("kdb.Poller: begin cycle: %v", err)
		return
	}
	defer p.endCycle(ctx, cycleID)

	feeds, err := p.LoadFeeds(ctx)
	if err != nil {
		log.Printf("kdb.Poller: load feeds: %v", err)
		return
	}
	if len(feeds) == 0 {
		log.Printf("kdb.Poller: no feeds with rss_url — skip cycle")
		return
	}

	idx, err := LoadEntityIndex(ctx, p.Pool)
	if err != nil {
		log.Printf("kdb.Poller: load index: %v", err)
		return
	}
	log.Printf("kdb.Poller: cycle=%d feeds=%d index=%d entities (%d spellings)",
		cycleID, len(feeds), idx.EntityCount(), idx.Size())

	// Phase 4 정공법 (Agent SRE #1): PollOnce 는 fetch + raw INSERT 만.
	// Codex 호출은 별도 ExtractSweeper 가 처리 → bridge down 이어도 raw 살아남음.
	var totItems, totCheap, totCand, totErr int
	for _, f := range feeds {
		items, ferr := p.fetchAndParse(ctx, f.RSSURL)
		if ferr != nil {
			totErr++
			p.updateFeedStatsErr(ctx, f, "unreachable", ferr.Error())
			log.Printf("kdb.Poller: %s/%s fetch fail: %v", f.Locale, f.Domain, ferr)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		feedCheap := 0
		for _, it := range items {
			text := it.Title + " " + it.Description
			hints := idx.MatchText(text)
			cheapStatus := "miss"
			var hintsJSON interface{} = nil
			if len(hints) > 0 {
				cheapStatus = "hit"
				totCheap++
				feedCheap++
				ids := make([]string, 0, len(hints))
				for _, h := range hints {
					ids = append(ids, h.EntityID.String())
				}
				if b, err := json.Marshal(ids); err == nil {
					hintsJSON = string(b)
				}
			} else if isKContentCategory(f.Category) {
				// cheap-gate miss + K-content 매체 → 신규 entity 후보 후보. Sweeper 가 처리.
				cheapStatus = "miss"
				totCand++
			}
			// INSERT raw (idempotent — same (domain, link) UNIQUE)
			if _, err := p.Pool.Exec(ctx, `
INSERT INTO kwave_rss_items_raw
  (source_domain, locale, link, title, description, fetched_at,
   cheap_status, cheap_hints, codex_status)
VALUES ($1, $2, $3, $4, $5, now(), $6, $7::jsonb,
        CASE WHEN $6='hit' THEN 'pending' ELSE NULL END)
ON CONFLICT (source_domain, link) DO NOTHING`,
				f.Domain, f.Locale, it.Link, it.Title, it.Description,
				cheapStatus, hintsJSON); err != nil {
				log.Printf("kdb.Poller: raw INSERT %s/%s err=%v", f.Locale, f.Domain, err)
			}
		}
		p.updateFeedStatsOK(ctx, f, len(items), feedCheap)
		totItems += len(items)
		time.Sleep(100 * time.Millisecond)
	}

	// extract count 는 sweeper 가 별도 처리 — cycle stats 에서는 0 또는 sweeper 처리량.
	p.recordCycleStats(ctx, cycleID, len(feeds), totItems, totCheap, 0, totCand, totErr)
	log.Printf("kdb.Poller: cycle=%d done feeds=%d items=%d cheap_pass=%d candidates=%d errors=%d (sweeper handles Codex)",
		cycleID, len(feeds), totItems, totCheap, totCand, totErr)
}

// fetchAndParse — single feed HTTP GET + RSS/Atom parse.
func (p *Poller) fetchAndParse(ctx context.Context, feedURL string) ([]FeedItem, error) {
	if _, err := url.Parse(feedURL); err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", feedURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", p.UserAgent)
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml, text/xml")
	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return ParseFeed(resp.Body)
}

// Agent (SRE) 권고 2026-05-25: silent DB swallow (_,_ = Pool.Exec) 모두 제거.
// CLAUDE.md §5 정공법 위반 — 에러는 반드시 log + counter.

func (p *Poller) updateFeedStatus(ctx context.Context, f Feed, status, _ string) {
	if _, err := p.Pool.Exec(ctx, `
UPDATE kwave_news_whitelist
   SET last_polled = now(), poll_status = $3
 WHERE domain = $1 AND locale = $2`, f.Domain, f.Locale, status); err != nil {
		log.Printf("kdb.Poller: updateFeedStatus %s/%s: %v", f.Locale, f.Domain, err)
	}
}

// updateFeedStatsOK — 정상 fetch 후 items + cheap-gate 통과 수 update.
// consecutive_failures = 0 reset (성공).
func (p *Poller) updateFeedStatsOK(ctx context.Context, f Feed, items, cheap int) {
	if _, err := p.Pool.Exec(ctx, `
UPDATE kwave_news_whitelist
   SET last_polled = now(),
       poll_status = 'ok',
       items_last_poll = $3,
       cheap_pass_last_poll = $4,
       consecutive_failures = 0,
       observations_total = (SELECT COUNT(*) FROM kwave_media_observations WHERE source_domain = $1)
 WHERE domain = $1 AND locale = $2`, f.Domain, f.Locale, items, cheap); err != nil {
		log.Printf("kdb.Poller: updateFeedStatsOK %s/%s: %v", f.Locale, f.Domain, err)
	}
}

// updateFeedStatsErr — fetch 실패 시 consecutive_failures++.
// 7회 도달 시 enabled=false 자동 (Agent SRE auto-disable 정공법).
func (p *Poller) updateFeedStatsErr(ctx context.Context, f Feed, status, _ string) {
	if _, err := p.Pool.Exec(ctx, `
UPDATE kwave_news_whitelist
   SET last_polled = now(),
       poll_status = $3,
       items_last_poll = 0,
       cheap_pass_last_poll = 0,
       consecutive_failures = consecutive_failures + 1,
       enabled = CASE WHEN consecutive_failures + 1 >= 7 THEN false ELSE enabled END
 WHERE domain = $1 AND locale = $2`, f.Domain, f.Locale, status); err != nil {
		log.Printf("kdb.Poller: updateFeedStatsErr %s/%s: %v", f.Locale, f.Domain, err)
	}
}

func (p *Poller) beginCycle(ctx context.Context) (int64, error) {
	var id int64
	err := p.Pool.QueryRow(ctx, `
INSERT INTO kwave_kdb_poll_cycles (started_at) VALUES (now()) RETURNING id`).Scan(&id)
	return id, err
}

func (p *Poller) endCycle(ctx context.Context, id int64) {
	// recordCycleStats 가 ended_at 까지 set 함 — endCycle 은 의도된 no-op.
	// 별도 호출 안 함 (이전 deferred 사고 방지).
	_ = id
}

func (p *Poller) recordCycleStats(ctx context.Context, id int64, feeds, items, cheap, extract, cand, errs int) {
	if _, err := p.Pool.Exec(ctx, `
UPDATE kwave_kdb_poll_cycles
   SET feeds_polled=$2, items_total=$3, cheap_pass=$4, gemma_calls=$5,
       observations=$5, candidates=$6, errors=$7, ended_at=now()
 WHERE id=$1`, id, feeds, items, cheap, extract, cand, errs); err != nil {
		log.Printf("kdb.Poller: recordCycleStats id=%d: %v", id, err)
	}
}

// isKContentCategory — kwave_news_whitelist.category 에 K-content 의도 표시.
// 향후 운영자가 명시 설정.
func isKContentCategory(c string) bool {
	c = strings.ToLower(strings.TrimSpace(c))
	switch c {
	case "kpop", "k-pop", "kdrama", "k-drama", "kcontent", "k-content", "hallyu", "korean":
		return true
	}
	return false
}

// RunTicker — supervisor 또는 별도 goroutine 에서 호출. 30분 간격 cron.
// 즉시 첫 cycle 1회 실행 후 ticker.
func (p *Poller) RunTicker(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Minute
	}
	// 즉시 1회
	p.PollOnce(ctx)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.PollOnce(ctx)
		}
	}
}

// ─── RSS / Atom parser (이전 rss_parser.go 흡수) ────────────────────

// FeedItem — RSS / Atom feed 의 한 항목.
type FeedItem struct {
	Title       string
	Description string // RSS description, Atom summary, RSS content:encoded 중 첫 비-빈
	Link        string
}

// rssRoot — RSS 2.0 채널 구조.
type rssRoot struct {
	XMLName xml.Name `xml:"rss"`
	Channel struct {
		Items []struct {
			Title       string `xml:"title"`
			Description string `xml:"description"`
			Encoded     string `xml:"http://purl.org/rss/1.0/modules/content/ encoded"`
			Link        string `xml:"link"`
		} `xml:"item"`
	} `xml:"channel"`
}

// atomRoot — Atom 1.0 feed 구조.
type atomRoot struct {
	XMLName xml.Name `xml:"http://www.w3.org/2005/Atom feed"`
	Entries []struct {
		Title   string `xml:"title"`
		Summary string `xml:"summary"`
		Content string `xml:"content"`
		Links   []struct {
			Rel  string `xml:"rel,attr"`
			Href string `xml:"href,attr"`
		} `xml:"link"`
	} `xml:"entry"`
}

// ParseFeed — RSS 2.0 또는 Atom 1.0 자동 감지.
func ParseFeed(body io.Reader) ([]FeedItem, error) {
	raw, err := io.ReadAll(io.LimitReader(body, 5<<20)) // 5MB cap
	if err != nil {
		return nil, fmt.Errorf("read feed: %w", err)
	}

	// RSS 시도
	var rss rssRoot
	if err := xml.Unmarshal(raw, &rss); err == nil && len(rss.Channel.Items) > 0 {
		out := make([]FeedItem, 0, len(rss.Channel.Items))
		for _, it := range rss.Channel.Items {
			desc := it.Description
			if desc == "" {
				desc = it.Encoded
			}
			out = append(out, FeedItem{
				Title:       cleanText(it.Title),
				Description: cleanText(desc),
				Link:        strings.TrimSpace(it.Link),
			})
		}
		return out, nil
	}

	// Atom 시도
	var atom atomRoot
	if err := xml.Unmarshal(raw, &atom); err == nil && len(atom.Entries) > 0 {
		out := make([]FeedItem, 0, len(atom.Entries))
		for _, e := range atom.Entries {
			desc := e.Summary
			if desc == "" {
				desc = e.Content
			}
			link := ""
			for _, l := range e.Links {
				if l.Rel == "" || l.Rel == "alternate" {
					link = l.Href
					break
				}
			}
			out = append(out, FeedItem{
				Title:       cleanText(e.Title),
				Description: cleanText(desc),
				Link:        link,
			})
		}
		return out, nil
	}

	return nil, fmt.Errorf("no RSS/Atom items detected (body len=%d)", len(raw))
}

// htmlTagInItemRE — RSS description 안 HTML 태그 제거.
var htmlTagInItemRE = regexp.MustCompile(`<[^>]+>`)

// whitespaceRE — 다중 공백/줄바꿈을 단일 공백으로.
var whitespaceRE = regexp.MustCompile(`\s+`)

// cleanText — RSS title/description HTML 태그 + entity decode + whitespace 정리.
func cleanText(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = htmlTagInItemRE.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	s = whitespaceRE.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

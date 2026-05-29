package kdbadmin

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

type whitelistRow struct {
	Domain              string
	Locale              string
	Trust               float64
	Enabled             bool
	Category            string
	AddedBy             string
	AddedAt             time.Time
	LastUsedAt          *time.Time
	RSSURL              string
	LastPolled          *time.Time
	PollStatus          string
	ItemsLastPoll       int
	CheapPassLastPoll   int
	ObservationsTotal   int
	ConsecutiveFailures int
}

type whitelistAgg struct {
	Locale      string
	Active      int
	TotalItems  int
	TotalCheap  int
	TotalObserv int
	PassRate    string
}

func (s *Server) entityWhitelist(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	showAll := strings.TrimSpace(r.URL.Query().Get("all")) == "1"
	where := "WHERE enabled = true"
	if showAll {
		where = ""
	}

	rows, err := s.pool.Query(ctx, `
SELECT domain, locale, trust, enabled, COALESCE(category,''), added_by, added_at,
       last_used_at, COALESCE(rss_url,''), last_polled, COALESCE(poll_status,''),
       items_last_poll, cheap_pass_last_poll, observations_total, consecutive_failures
FROM kwave_news_whitelist `+where+`
ORDER BY enabled DESC, observations_total DESC, domain, locale`)
	if err != nil {
		s.renderError(w, r, "whitelist", err)
		return
	}
	defer rows.Close()

	items := []whitelistRow{}
	for rows.Next() {
		var x whitelistRow
		if err := rows.Scan(&x.Domain, &x.Locale, &x.Trust, &x.Enabled, &x.Category, &x.AddedBy, &x.AddedAt,
			&x.LastUsedAt, &x.RSSURL, &x.LastPolled, &x.PollStatus,
			&x.ItemsLastPoll, &x.CheapPassLastPoll, &x.ObservationsTotal, &x.ConsecutiveFailures); err == nil {
			items = append(items, x)
		}
	}

	// Per-locale aggregates.
	aggs := []whitelistAgg{}
	if aRows, aErr := s.pool.Query(ctx, `
SELECT locale,
       SUM(CASE WHEN enabled THEN 1 ELSE 0 END) AS active,
       SUM(items_last_poll) AS total_items,
       SUM(cheap_pass_last_poll) AS total_cheap,
       SUM(observations_total) AS total_obs
FROM kwave_news_whitelist GROUP BY locale ORDER BY locale`); aErr == nil {
		defer aRows.Close()
		for aRows.Next() {
			var x whitelistAgg
			if err := aRows.Scan(&x.Locale, &x.Active, &x.TotalItems, &x.TotalCheap, &x.TotalObserv); err == nil {
				if x.TotalItems > 0 {
					rate := float64(x.TotalCheap) / float64(x.TotalItems) * 100
					x.PassRate = formatPct(rate)
				} else {
					x.PassRate = "-"
				}
				aggs = append(aggs, x)
			}
		}
	}

	// 상단 카운트.
	var inactive, failing int
	_ = s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM kwave_news_whitelist WHERE enabled = false`).Scan(&inactive)
	_ = s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM kwave_news_whitelist WHERE consecutive_failures >= 5`).Scan(&failing)
	var lastCycle *time.Time
	_ = s.pool.QueryRow(ctx, `SELECT MAX(last_polled) FROM kwave_news_whitelist`).Scan(&lastCycle)

	s.render(w, r, "entity_whitelist.html", map[string]any{
		"title":         "매체 관리 (WF-5)",
		"items":         items,
		"aggs":          aggs,
		"showAll":       showAll,
		"inactiveCount": inactive,
		"failingCount":  failing,
		"lastPolled":    lastCycle,
		"flash":         r.URL.Query().Get("flash"),
		"page":          "/admin/entities/whitelist",
	})
}

// --- WF-5 액션 -----------------------------------------------------------

// whitelistAdd — 신규 도메인 INSERT.
func (s *Server) whitelistAdd(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.redirectBack(w, r, "bad form")
		return
	}
	domain := strings.TrimSpace(strings.ToLower(r.FormValue("domain")))
	locale := strings.TrimSpace(strings.ToLower(r.FormValue("locale")))
	trustStr := r.FormValue("trust")
	category := strings.TrimSpace(r.FormValue("category"))
	rssURL := strings.TrimSpace(r.FormValue("rss_url"))

	if domain == "" || locale == "" {
		s.redirectBack(w, r, "domain / locale 필수")
		return
	}
	trust := 0.80
	if v, err := strconv.ParseFloat(trustStr, 64); err == nil && v >= 0 && v <= 1 {
		trust = v
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	if _, err := s.pool.Exec(ctx, `
INSERT INTO kwave_news_whitelist (domain, locale, trust, enabled, category, rss_url, added_by, added_at)
VALUES ($1, $2, $3::numeric, TRUE, NULLIF($4,''), NULLIF($5,''), 'operator', now())
ON CONFLICT (domain, locale) DO NOTHING`, domain, locale, trust, category, rssURL); err != nil {
		s.redirectBack(w, r, "추가 실패: "+err.Error())
		return
	}
	s.redirectBack(w, r, "추가: "+domain+"/"+locale)
}

// whitelistSaveRSS — RSS URL UPDATE + last_polled=NULL (다음 cycle 강제 poll).
func (s *Server) whitelistSaveRSS(w http.ResponseWriter, r *http.Request) {
	domain := chi.URLParam(r, "domain")
	locale := chi.URLParam(r, "locale")
	if err := r.ParseForm(); err != nil {
		s.redirectBack(w, r, "bad form")
		return
	}
	rssURL := strings.TrimSpace(r.FormValue("rss_url"))

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	tag, err := s.pool.Exec(ctx, `
UPDATE kwave_news_whitelist SET rss_url = NULLIF($3,''), last_polled = NULL, consecutive_failures = 0
WHERE domain = $1 AND locale = $2`, domain, locale, rssURL)
	if err != nil {
		s.redirectBack(w, r, "RSS 저장 실패: "+err.Error())
		return
	}
	if tag.RowsAffected() == 0 {
		s.redirectBack(w, r, "RSS 저장: 대상 매체 없음")
		return
	}
	if rssURL == "" {
		s.redirectBack(w, r, "RSS URL 제거: "+domain+"/"+locale)
		return
	}
	s.redirectBack(w, r, "RSS 저장: "+domain+"/"+locale)
}

// whitelistToggle — enabled toggle. enable 시 consecutive_failures=0.
func (s *Server) whitelistToggle(w http.ResponseWriter, r *http.Request) {
	domain := chi.URLParam(r, "domain")
	locale := chi.URLParam(r, "locale")

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var newState bool
	if err := s.pool.QueryRow(ctx, `
UPDATE kwave_news_whitelist
   SET enabled = NOT enabled,
       consecutive_failures = CASE WHEN NOT enabled THEN 0 ELSE consecutive_failures END
 WHERE domain = $1 AND locale = $2
 RETURNING enabled`, domain, locale).Scan(&newState); err != nil {
		s.redirectBack(w, r, "toggle 실패: 매체 없음")
		return
	}
	state := "OFF ⏸"
	if newState {
		state = "ON ▶"
	}
	s.redirectBack(w, r, state+": "+domain+"/"+locale)
}

// whitelistDelete — DELETE (영구). 운영자가 confirm dialog 통과한 후.
func (s *Server) whitelistDelete(w http.ResponseWriter, r *http.Request) {
	domain := chi.URLParam(r, "domain")
	locale := chi.URLParam(r, "locale")

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM kwave_news_whitelist WHERE domain = $1 AND locale = $2`, domain, locale)
	if err != nil {
		s.redirectBack(w, r, "삭제 실패: "+err.Error())
		return
	}
	if tag.RowsAffected() == 0 {
		s.redirectBack(w, r, "삭제: 대상 없음")
		return
	}
	s.redirectBack(w, r, "삭제: "+domain+"/"+locale)
}

func formatPct(v float64) string {
	if v <= 0 {
		return "0%"
	}
	s := fmt.Sprintf("%.1f", v)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	return s + "%"
}

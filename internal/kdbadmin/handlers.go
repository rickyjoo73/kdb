package kdbadmin

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// --- auth handlers ------------------------------------------------------

func (s *Server) loginGet(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
		if _, ok := decodeSession(s.opts.SessionSecret, c.Value); ok {
			http.Redirect(w, r, "/admin", http.StatusFound)
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if n, err := adminCount(ctx, s.pool); err == nil && n == 0 {
		http.Redirect(w, r, "/admin/setup", http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "login.html", map[string]any{
		"next":  r.URL.Query().Get("next"),
		"email": "",
	}); err != nil {
		log.Printf("kdbadmin: render login.html: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func (s *Server) loginPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	email := strings.TrimSpace(r.FormValue("email"))
	pass := r.FormValue("password")
	next := r.FormValue("next")

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	u, err := authenticate(ctx, s.pool, email, pass)
	if err != nil {
		msg := "이메일 또는 비밀번호가 올바르지 않습니다."
		if errors.Is(err, errLocked) {
			msg = "로그인 실패 횟수 초과로 잠금 상태입니다. 15분 후 다시 시도해 주세요."
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_ = s.tmpl.ExecuteTemplate(w, "login.html", map[string]any{
			"error": msg,
			"email": email,
			"next":  next,
		})
		return
	}

	token := encodeSession(s.opts.SessionSecret, u.Email, sessionMaxAge)
	setSessionCookie(w, r, token)

	http.Redirect(w, r, safeAdminRedirect(next), http.StatusFound)
}

// safeAdminRedirect — next 가 동일-출처의 /admin 경로일 때만 그대로, 아니면 /admin.
// raw HasPrefix 는 `/admin\@evil.com`(브라우저가 \→/ 정규화) 같은 open-redirect 를
// 통과시킨다 → parse 기반으로 scheme/host/백슬래시를 거부(safeInternalReferer 와 동일 정책).
func safeAdminRedirect(next string) string {
	next = strings.TrimSpace(next)
	if next == "" || strings.ContainsAny(next, "\\") {
		return "/admin"
	}
	u, err := url.Parse(next)
	if err != nil || u.IsAbs() || u.Host != "" || !strings.HasPrefix(u.EscapedPath(), "/admin") {
		return "/admin"
	}
	out := u.EscapedPath()
	if u.RawQuery != "" {
		out += "?" + u.RawQuery
	}
	return out
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	clearSessionCookie(w)
	http.Redirect(w, r, "/admin/login", http.StatusFound)
}

func (s *Server) setupGet(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	n, err := adminCount(ctx, s.pool)
	if err != nil {
		log.Printf("kdbadmin: setup db error: %v", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	if n > 0 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.tmpl.ExecuteTemplate(w, "setup.html", map[string]any{
		"email": "",
		"name":  "",
	})
}

func (s *Server) setupPost(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	n, err := adminCount(ctx, s.pool)
	if err != nil {
		log.Printf("kdbadmin: setup db error: %v", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	if n > 0 {
		http.NotFound(w, r)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	email := strings.TrimSpace(r.FormValue("email"))
	name := strings.TrimSpace(r.FormValue("name"))
	pass := r.FormValue("password")
	confirm := r.FormValue("confirm")

	renderErr := func(msg string) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_ = s.tmpl.ExecuteTemplate(w, "setup.html", map[string]any{
			"error": msg,
			"email": email,
			"name":  name,
		})
	}

	if email == "" || pass == "" {
		renderErr("이메일과 비밀번호를 입력해 주세요.")
		return
	}
	if pass != confirm {
		renderErr("비밀번호 확인이 일치하지 않습니다.")
		return
	}

	created, err := createAdmin(ctx, s.pool, email, pass, name)
	if err != nil {
		renderErr("계정 생성 실패: " + err.Error())
		return
	}
	if !created {
		renderErr("이미 존재하는 이메일입니다.")
		return
	}

	token := encodeSession(s.opts.SessionSecret, strings.ToLower(email), sessionMaxAge)
	setSessionCookie(w, r, token)
	http.Redirect(w, r, "/admin", http.StatusFound)
}

// --- inbox counts (dashboard + nav 뱃지 공통) ----------------------------

type inboxCounts struct {
	NewCandidates int64
	Unclassified  int64
	Conflicts     int64
	LocaleGaps    int64
	LowQuality    int64
}

// fetchInboxCounts — 사이드바 뱃지 / dashboard 카드 공통. 매 요청마다 1회 (~10 ms).
// 에러는 무시 (카운트 0 표시).
func (s *Server) fetchInboxCounts(ctx context.Context) inboxCounts {
	c := inboxCounts{}
	_ = s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM kwave_entities WHERE status='candidate'`).Scan(&c.NewCandidates)
	_ = s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM kwave_entities WHERE status='active' AND entity_type='unknown'`).Scan(&c.Unclassified)
	// 충돌 = canonical_ko 중복 + alias 다중 매핑 + resolution_attempts 30일 실패. 빠르게 추정.
	_ = s.pool.QueryRow(ctx, `
SELECT
  (SELECT COUNT(*) FROM (SELECT canonical_ko FROM kwave_entities GROUP BY canonical_ko HAVING COUNT(*) > 1) d)
+ (SELECT COUNT(*) FROM kwave_entity_resolution_attempts
   WHERE status IN ('disambiguation-fail','conflict','error')
     AND attempted_at > now() - interval '30 days')`).Scan(&c.Conflicts)
	// 누락 locale = "실제 채울 수 있는" 것만 (측정 착시 제거): conf≥0.5 active 중
	// priority locale 빈칸이되, operator_locked/unknown 제외 + source-exhausted(7d) 필드
	// 제외 (이미 시도해 못 채운 hard-case 는 빈칸이 정답). Hermes backlog 와 동일 기준.
	_ = s.pool.QueryRow(ctx, `
SELECT COUNT(*) FROM kwave_entities e
  LEFT JOIN LATERAL (
    SELECT COALESCE(array_agg(a.field) FILTER (
             WHERE a.exhausted AND a.last_attempt_at > now() - interval '7 days'), '{}') AS f
      FROM kwave_kdb_enrich_attempts a WHERE a.entity_id = e.id) ex ON true
WHERE e.status='active' AND e.confidence >= 0.5
  AND e.operator_locked = false AND e.entity_type <> 'unknown'
  AND ((COALESCE(e.canonical_en,'')=''    AND NOT 'canonical_en'    = ANY(ex.f))
    OR (COALESCE(e.canonical_vi,'')=''    AND NOT 'canonical_vi'    = ANY(ex.f))
    OR (COALESCE(e.canonical_id,'')=''    AND NOT 'canonical_id'    = ANY(ex.f))
    OR (COALESCE(e.canonical_es,'')=''    AND NOT 'canonical_es'    = ANY(ex.f))
    OR (COALESCE(e.canonical_pt_br,'')='' AND NOT 'canonical_pt_br' = ANY(ex.f)))`).Scan(&c.LocaleGaps)
	// 품질 검토 = conf<0.7 中 "bumpable"(Wikidata 레퍼런스 보유 → 검증·승급 여지) 만.
	// Wikidata ref 전무한 무명은 정당한 저신뢰 floor 라 제외 (측정 착시 제거).
	_ = s.pool.QueryRow(ctx, `
SELECT COUNT(*) FROM kwave_entities
WHERE confidence < 0.7 AND status='active'
  AND operator_locked = false AND entity_type <> 'unknown'
  AND (canonical_en_source     ILIKE '%wikidata%' OR canonical_ja_source ILIKE '%wikidata%'
    OR canonical_vi_source     ILIKE '%wikidata%' OR canonical_es_source ILIKE '%wikidata%'
    OR canonical_id_source     ILIKE '%wikidata%' OR canonical_pt_br_source ILIKE '%wikidata%'
    OR canonical_zh_source     ILIKE '%wikidata%' OR canonical_zh_hant_source ILIKE '%wikidata%')`).Scan(&c.LowQuality)
	return c
}

// --- dashboard ----------------------------------------------------------

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	stats := dashboardStats{}
	type row struct {
		sql  string
		dest *int64
	}
	queries := []row{
		{`SELECT COUNT(*) FROM kwave_entities`, &stats.Entities},
		{`SELECT COUNT(*) FROM kwave_persons`, &stats.Persons},
		{`SELECT COUNT(*) FROM kwave_media_observations`, &stats.Observations},
		{`SELECT COUNT(*) FROM kwave_kdb_codex_runs WHERE ran_at > now() - interval '24 hours'`, &stats.CodexRuns24h},
		{`SELECT COUNT(*) FROM kwave_kdb_codex_runs WHERE status <> 'ok' AND ran_at > now() - interval '24 hours'`, &stats.CodexErrors24h},
		{`SELECT COUNT(*) FROM kwave_entity_research_queue WHERE status = 'pending'`, &stats.PendingResearch},
		{`SELECT COUNT(*) FROM kwave_entity_candidates WHERE status = 'pending'`, &stats.PendingCandidates},
		{`SELECT COUNT(*) FROM kwave_news_whitelist WHERE enabled = true`, &stats.WhitelistActive},
	}
	for _, q := range queries {
		if err := s.pool.QueryRow(ctx, q.sql).Scan(q.dest); err != nil && stats.Error == "" {
			stats.Error = err.Error()
		}
	}

	// Latest poll cycle.
	var lastCycle pollCycle
	_ = s.pool.QueryRow(ctx, `
SELECT id, started_at, ended_at, feeds_polled, items_total, cheap_pass, gemma_calls, observations, candidates, errors
FROM kwave_kdb_poll_cycles
ORDER BY started_at DESC LIMIT 1`).Scan(
		&lastCycle.ID, &lastCycle.StartedAt, &lastCycle.EndedAt,
		&lastCycle.FeedsPolled, &lastCycle.ItemsTotal, &lastCycle.CheapPass,
		&lastCycle.GemmaCalls, &lastCycle.Observations, &lastCycle.Candidates, &lastCycle.Errors)

	inbox := s.fetchInboxCounts(ctx)

	// At-a-glance per-language fill bars for both DBs.
	entityProgress := s.localeProgressData(ctx)
	personProgress := s.personsLocaleProgress(ctx)

	// 최근 autopilot cycle (kwave_kdb_autopilot_log, migration 0064).
	autopilotLog := s.recentAutopilotLog(ctx)

	s.render(w, r, "dashboard.html", map[string]any{
		"title":          "운영 개요",
		"stats":          stats,
		"lastCycle":      lastCycle,
		"inbox":          inbox,
		"entityProgress": entityProgress,
		"personProgress": personProgress,
		"autopilotLog":   autopilotLog,
	})
}

// autopilotLogRow — kwave_kdb_autopilot_log 한 행 (dashboard 표시용).
type autopilotLogRow struct {
	RanAt      time.Time
	DurationMs int
	Reject     int
	Classified int
	Promoted   int
	Enriched   int
	Quality    int
	Alias      int
	Persons    int
}

// recentAutopilotLog — 최근 12 cycle. 테이블 없으면(0064 미적용) 빈 슬라이스.
func (s *Server) recentAutopilotLog(ctx context.Context) []autopilotLogRow {
	rows, err := s.pool.Query(ctx, `
SELECT ran_at, duration_ms, non_entity_reject, classified, promoted,
       enriched, quality_fixed, alias_resolved, persons_added
FROM kwave_kdb_autopilot_log
ORDER BY ran_at DESC LIMIT 12`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []autopilotLogRow{}
	for rows.Next() {
		var x autopilotLogRow
		if err := rows.Scan(&x.RanAt, &x.DurationMs, &x.Reject, &x.Classified,
			&x.Promoted, &x.Enriched, &x.Quality, &x.Alias, &x.Persons); err == nil {
			out = append(out, x)
		}
	}
	return out
}

type dashboardStats struct {
	Entities          int64
	Persons           int64
	Observations      int64
	CodexRuns24h      int64
	CodexErrors24h    int64
	PendingResearch   int64
	PendingCandidates int64
	WhitelistActive   int64
	Error             string
}

type pollCycle struct {
	ID           int64
	StartedAt    time.Time
	EndedAt      *time.Time
	FeedsPolled  int
	ItemsTotal   int
	CheapPass    int
	GemmaCalls   int
	Observations int
	Candidates   int
	Errors       int
}

// --- pagination helpers -------------------------------------------------

type page struct {
	Limit      int
	Offset     int
	PrevOffset int
	NextOffset int
	HasPrev    bool
	HasNext    bool
	Q          string
	Filter     string
	BaseURL    string
	Total      int64
	StartIndex int64
	EndIndex   int64
	PageNo     int
	TotalPages int
	Extras     map[string]string // additional query params to preserve
}

func newPage(r *http.Request, base string) page {
	limit := atoiOr(r.URL.Query().Get("limit"), 50)
	if limit < 1 || limit > 500 {
		limit = 50
	}
	offset := atoiOr(r.URL.Query().Get("offset"), 0)
	if offset < 0 {
		offset = 0
	}
	prev := offset - limit
	if prev < 0 {
		prev = 0
	}
	return page{
		Limit:      limit,
		Offset:     offset,
		PrevOffset: prev,
		NextOffset: offset + limit,
		HasPrev:    offset > 0,
		Q:          strings.TrimSpace(r.URL.Query().Get("q")),
		Filter:     strings.TrimSpace(r.URL.Query().Get("filter")),
		BaseURL:    base,
		Extras:     map[string]string{},
	}
}

// finalize fills in total/page metadata once the caller knows the row count.
func (p *page) finalize(total int64) {
	p.Total = total
	if total > 0 {
		p.StartIndex = int64(p.Offset) + 1
		p.EndIndex = int64(p.Offset + p.Limit)
		if p.EndIndex > total {
			p.EndIndex = total
		}
	}
	if p.Limit > 0 {
		p.PageNo = p.Offset/p.Limit + 1
		p.TotalPages = int((total + int64(p.Limit) - 1) / int64(p.Limit))
		if p.TotalPages < 1 {
			p.TotalPages = 1
		}
	}
	p.HasNext = int64(p.NextOffset) < total
}

func atoiOr(s string, fallback int) int {
	if s == "" {
		return fallback
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return fallback
	}
	return n
}

// --- error rendering ----------------------------------------------------

func (s *Server) renderError(w http.ResponseWriter, r *http.Request, where string, err error) {
	// 상세(드라이버/스키마 내부)는 서버 로그로만, 클라이언트엔 일반 메시지.
	log.Printf("kdbadmin: error at %s: %v", where, err)
	w.WriteHeader(http.StatusInternalServerError)
	s.render(w, r, "error.html", map[string]any{
		"title": "Error",
		"where": where,
		"err":   "처리 중 오류가 발생했습니다. 잠시 후 다시 시도해 주세요.",
	})
}

package kdbadmin

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// --- auth handlers ------------------------------------------------------

func (s *Server) loginGet(w http.ResponseWriter, r *http.Request) {
	// If already authenticated, send straight to /admin.
	if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
		if _, ok := decodeSession(s.opts.SessionSecret, c.Value); ok {
			http.Redirect(w, r, "/admin", http.StatusFound)
			return
		}
	}
	// First-run: nudge to setup if no admins exist yet.
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
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

	if next == "" || !strings.HasPrefix(next, "/admin") {
		next = "/admin"
	}
	http.Redirect(w, r, next, http.StatusFound)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	clearSessionCookie(w)
	http.Redirect(w, r, "/admin/login", http.StatusFound)
}

// setupGet renders the first-run admin creation page when no admins exist.
func (s *Server) setupGet(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	n, err := adminCount(ctx, s.pool)
	if err != nil {
		http.Error(w, "db error: "+err.Error(), http.StatusInternalServerError)
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

// setupPost creates the first admin and starts a session.
func (s *Server) setupPost(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// Re-check that no admins exist yet (race protection).
	n, err := adminCount(ctx, s.pool)
	if err != nil {
		http.Error(w, "db error: "+err.Error(), http.StatusInternalServerError)
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

// dashboard renders the entry page with high-level counts.
func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	stats := dashboardStats{}
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM kwave_entities`).Scan(&stats.Entities); err != nil {
		stats.Error = err.Error()
	}
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM kwave_persons`).Scan(&stats.Persons); err != nil && stats.Error == "" {
		stats.Error = err.Error()
	}
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM kwave_media_observations`).Scan(&stats.Observations); err != nil && stats.Error == "" {
		stats.Error = err.Error()
	}
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM kwave_kdb_codex_runs WHERE ran_at > now() - interval '24 hours'`).Scan(&stats.CodexRuns24h); err != nil && stats.Error == "" {
		stats.Error = err.Error()
	}
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM kwave_kdb_codex_runs WHERE status <> 'ok' AND ran_at > now() - interval '24 hours'`).Scan(&stats.CodexErrors24h); err != nil && stats.Error == "" {
		stats.Error = err.Error()
	}
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM kwave_entity_research_queue WHERE status = 'pending'`).Scan(&stats.PendingResearch); err != nil && stats.Error == "" {
		stats.Error = err.Error()
	}

	s.render(w, r, "dashboard.html", map[string]any{
		"title": "대시보드",
		"stats": stats,
	})
}

type dashboardStats struct {
	Entities        int64
	Persons         int64
	Observations    int64
	CodexRuns24h    int64
	CodexErrors24h  int64
	PendingResearch int64
	Error           string
}

// --- pagination helpers --------------------------------------------------

type page struct {
	Limit      int
	Offset     int
	PrevOffset int
	NextOffset int
	HasPrev    bool
	Q          string
	Filter     string
	BaseURL    string
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
	}
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

// --- entities -----------------------------------------------------------

func (s *Server) entitiesList(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	p := newPage(r, "/admin/entities")

	args := []any{p.Limit, p.Offset}
	where := ""
	if p.Q != "" {
		where = `WHERE canonical_ko ILIKE '%'||$3||'%' OR canonical_en ILIKE '%'||$3||'%' OR $3 = ANY(aliases_ko) OR $3 = ANY(aliases_en)`
		args = append(args, p.Q)
	}

	rows, err := s.pool.Query(ctx, `
SELECT id, entity_type::text, canonical_ko,
       COALESCE(canonical_en,''), COALESCE(canonical_ja,''),
       confidence, operator_locked, updated_at
FROM kwave_entities `+where+`
ORDER BY updated_at DESC
LIMIT $1 OFFSET $2`, args...)
	if err != nil {
		s.renderError(w, r, "entities list", err)
		return
	}
	defer rows.Close()

	type row struct {
		ID             uuid.UUID
		Type           string
		Ko, En, Ja     string
		Confidence     float64
		OperatorLocked bool
		UpdatedAt      time.Time
	}
	items := make([]row, 0, p.Limit)
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.ID, &x.Type, &x.Ko, &x.En, &x.Ja, &x.Confidence, &x.OperatorLocked, &x.UpdatedAt); err != nil {
			s.renderError(w, r, "entities scan", err)
			return
		}
		items = append(items, x)
	}

	s.render(w, r, "entities_list.html", map[string]any{
		"title": "Entities",
		"items": items,
		"page":  "/admin/entities",
		"p":     p,
	})
}

func (s *Server) entityDetail(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var e entityDetail
	err = s.pool.QueryRow(ctx, `
SELECT id, entity_type::text, canonical_ko,
       COALESCE(canonical_en,''), COALESCE(canonical_ja,''),
       COALESCE(canonical_vi,''), COALESCE(canonical_zh,''),
       COALESCE(canonical_es,''), COALESCE(canonical_id,''),
       COALESCE(canonical_pt_br,''), COALESCE(canonical_zh_hant,''),
       aliases_ko, aliases_en, aliases_ja, aliases_vi, aliases_zh,
       confidence, operator_locked,
       COALESCE(category_hint,''), COALESCE(notes,''),
       source_urls, last_verified_at, created_at, updated_at
FROM kwave_entities WHERE id = $1`, id).Scan(
		&e.ID, &e.Type, &e.Ko, &e.En, &e.Ja, &e.Vi, &e.Zh, &e.Es, &e.IDLang, &e.PtBr, &e.ZhHant,
		&e.AliasesKo, &e.AliasesEn, &e.AliasesJa, &e.AliasesVi, &e.AliasesZh,
		&e.Confidence, &e.OperatorLocked, &e.CategoryHint, &e.Notes,
		&e.SourceURLs, &e.LastVerifiedAt, &e.CreatedAt, &e.UpdatedAt)
	if err != nil {
		s.renderError(w, r, "entity detail", err)
		return
	}

	obsRows, err := s.pool.Query(ctx, `
SELECT id, locale, spelling, source_domain, COALESCE(source_url,''), observed_at, COALESCE(confidence,0)
FROM kwave_media_observations WHERE entity_id = $1
ORDER BY observed_at DESC LIMIT 100`, id)
	if err == nil {
		defer obsRows.Close()
		for obsRows.Next() {
			var o observation
			if err := obsRows.Scan(&o.ID, &o.Locale, &o.Spelling, &o.SourceDomain, &o.SourceURL, &o.ObservedAt, &o.Confidence); err == nil {
				e.Observations = append(e.Observations, o)
			}
		}
	}

	s.render(w, r, "entity_detail.html", map[string]any{
		"title": "Entity · " + e.Ko,
		"e":     e,
		"page":  "/admin/entities",
	})
}

type entityDetail struct {
	ID                                                                                uuid.UUID
	Type, Ko, En, Ja, Vi, Zh, Es, IDLang, PtBr, ZhHant                                string
	AliasesKo, AliasesEn, AliasesJa, AliasesVi, AliasesZh                             []string
	Confidence                                                                        float64
	OperatorLocked                                                                    bool
	CategoryHint, Notes                                                               string
	SourceURLs                                                                        []string
	LastVerifiedAt, CreatedAt, UpdatedAt                                              time.Time
	Observations                                                                      []observation
}

type observation struct {
	ID                  int64
	Locale, Spelling    string
	SourceDomain, SourceURL string
	ObservedAt          time.Time
	Confidence          float64
}

func (s *Server) entitySources(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	rows, err := s.pool.Query(ctx, `
SELECT source_domain, locale, COUNT(*) AS observations,
       COUNT(DISTINCT entity_id) AS distinct_entities,
       MAX(observed_at) AS last_seen
FROM kwave_media_observations
WHERE observed_at > now() - interval '30 days'
GROUP BY source_domain, locale
ORDER BY observations DESC
LIMIT 200`)
	if err != nil {
		s.renderError(w, r, "entity sources", err)
		return
	}
	defer rows.Close()

	type row struct {
		Domain, Locale  string
		Observations    int64
		DistinctEntities int64
		LastSeen        time.Time
	}
	items := make([]row, 0, 200)
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.Domain, &x.Locale, &x.Observations, &x.DistinctEntities, &x.LastSeen); err != nil {
			s.renderError(w, r, "sources scan", err)
			return
		}
		items = append(items, x)
	}
	s.render(w, r, "entity_sources.html", map[string]any{
		"title": "Entity sources",
		"items": items,
		"page":  "/admin/entities/sources",
	})
}

func (s *Server) entityReview(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	rows, err := s.pool.Query(ctx, `
SELECT id, entity_type::text, canonical_ko, COALESCE(canonical_en,''),
       confidence, operator_locked, updated_at
FROM kwave_entities
WHERE confidence < 0.7
ORDER BY confidence ASC, updated_at DESC
LIMIT 200`)
	if err != nil {
		s.renderError(w, r, "entity review", err)
		return
	}
	defer rows.Close()

	type row struct {
		ID                       uuid.UUID
		Type, Ko, En             string
		Confidence               float64
		OperatorLocked           bool
		UpdatedAt                time.Time
	}
	items := []row{}
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.ID, &x.Type, &x.Ko, &x.En, &x.Confidence, &x.OperatorLocked, &x.UpdatedAt); err == nil {
			items = append(items, x)
		}
	}
	s.render(w, r, "entity_review.html", map[string]any{
		"title": "Entity review (confidence < 0.7)",
		"items": items,
		"page":  "/admin/entities/review",
	})
}

func (s *Server) entityConflicts(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// alias 충돌: 같은 (locale, alias)이 여러 entity에 매핑된 경우.
	// kwave_entities aliases_* 컬럼은 text[] — unnest로 펼쳐서 그룹핑.
	rows, err := s.pool.Query(ctx, `
WITH all_aliases AS (
  SELECT id, canonical_ko, 'ko' AS locale, unnest(aliases_ko) AS alias FROM kwave_entities
  UNION ALL
  SELECT id, canonical_ko, 'en', unnest(aliases_en) FROM kwave_entities
  UNION ALL
  SELECT id, canonical_ko, 'ja', unnest(aliases_ja) FROM kwave_entities
)
SELECT locale, alias, COUNT(DISTINCT id) AS n, array_agg(DISTINCT canonical_ko) AS canonicals
FROM all_aliases
WHERE alias <> ''
GROUP BY locale, alias
HAVING COUNT(DISTINCT id) > 1
ORDER BY n DESC, alias
LIMIT 200`)
	if err != nil {
		s.renderError(w, r, "entity conflicts", err)
		return
	}
	defer rows.Close()

	type row struct {
		Locale, Alias string
		N             int
		Canonicals    []string
	}
	items := []row{}
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.Locale, &x.Alias, &x.N, &x.Canonicals); err == nil {
			items = append(items, x)
		}
	}
	s.render(w, r, "entity_conflicts.html", map[string]any{
		"title": "Entity conflicts",
		"items": items,
		"page":  "/admin/entities/conflicts",
	})
}

func (s *Server) entityWhitelist(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	rows, err := s.pool.Query(ctx, `
SELECT domain, locale, trust, enabled, COALESCE(category,''), added_by, added_at,
       last_used_at, COALESCE(rss_url,''), last_polled, COALESCE(poll_status,''),
       items_last_poll, cheap_pass_last_poll, observations_total, consecutive_failures
FROM kwave_news_whitelist
ORDER BY enabled DESC, observations_total DESC, domain, locale`)
	if err != nil {
		s.renderError(w, r, "whitelist", err)
		return
	}
	defer rows.Close()

	type row struct {
		Domain, Locale          string
		Trust                   float64
		Enabled                 bool
		Category, AddedBy       string
		AddedAt                 time.Time
		LastUsedAt              *time.Time
		RssURL                  string
		LastPolled              *time.Time
		PollStatus              string
		ItemsLastPoll           int
		CheapPassLastPoll       int
		ObservationsTotal       int
		ConsecutiveFailures     int
	}
	items := []row{}
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.Domain, &x.Locale, &x.Trust, &x.Enabled, &x.Category, &x.AddedBy, &x.AddedAt,
			&x.LastUsedAt, &x.RssURL, &x.LastPolled, &x.PollStatus,
			&x.ItemsLastPoll, &x.CheapPassLastPoll, &x.ObservationsTotal, &x.ConsecutiveFailures); err == nil {
			items = append(items, x)
		}
	}
	s.render(w, r, "entity_whitelist.html", map[string]any{
		"title": "RSS 화이트리스트",
		"items": items,
		"page":  "/admin/entities/whitelist",
	})
}

// --- KDB sections -------------------------------------------------------

func (s *Server) kdbCandidates(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	rows, err := s.pool.Query(ctx, `
SELECT id, entity_ko, requested_entity_type::text, COALESCE(context_hint,''), status, attempts,
       COALESCE(last_error,''), created_at, picked_at, finished_at
FROM kwave_entity_research_queue
ORDER BY (status = 'pending') DESC, created_at DESC
LIMIT 200`)
	if err != nil {
		s.renderError(w, r, "candidates", err)
		return
	}
	defer rows.Close()

	type row struct {
		ID                       uuid.UUID
		EntityKo                 string
		EntityType, ContextHint  string
		Status                   string
		Attempts                 int
		LastError                string
		CreatedAt                time.Time
		PickedAt, FinishedAt     *time.Time
	}
	items := []row{}
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.ID, &x.EntityKo, &x.EntityType, &x.ContextHint, &x.Status, &x.Attempts,
			&x.LastError, &x.CreatedAt, &x.PickedAt, &x.FinishedAt); err == nil {
			items = append(items, x)
		}
	}
	s.render(w, r, "kdb_candidates.html", map[string]any{
		"title": "KDB candidates (research queue)",
		"items": items,
		"page":  "/admin/kdb/candidates",
	})
}

func (s *Server) kdbCodexRuns(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	p := newPage(r, "/admin/kdb/codex-runs")

	rows, err := s.pool.Query(ctx, `
SELECT id, COALESCE(cycle_id,0), source_domain, locale, rss_title,
       hint_count, status, spelling_count, duration_ms, COALESCE(error_text,''), ran_at
FROM kwave_kdb_codex_runs
ORDER BY ran_at DESC
LIMIT $1 OFFSET $2`, p.Limit, p.Offset)
	if err != nil {
		s.renderError(w, r, "codex runs", err)
		return
	}
	defer rows.Close()

	type row struct {
		ID, CycleID                                int64
		SourceDomain, Locale, RssTitle, Status, ErrorText string
		HintCount, SpellingCount, DurationMs       int
		RanAt                                      time.Time
	}
	items := []row{}
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.ID, &x.CycleID, &x.SourceDomain, &x.Locale, &x.RssTitle,
			&x.HintCount, &x.Status, &x.SpellingCount, &x.DurationMs, &x.ErrorText, &x.RanAt); err == nil {
			items = append(items, x)
		}
	}
	s.render(w, r, "kdb_codex_runs.html", map[string]any{
		"title": "Codex audit (에러/no_spelling 만 저장)",
		"items": items,
		"page":  "/admin/kdb/codex-runs",
		"p":     p,
	})
}

func (s *Server) kdbObservations(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	p := newPage(r, "/admin/kdb/observations")

	rows, err := s.pool.Query(ctx, `
SELECT o.id, o.entity_id, COALESCE(e.canonical_ko,''), o.locale, o.spelling,
       o.source_domain, COALESCE(o.source_url,''), o.observed_at, COALESCE(o.confidence,0)
FROM kwave_media_observations o
LEFT JOIN kwave_entities e ON e.id = o.entity_id
ORDER BY o.observed_at DESC
LIMIT $1 OFFSET $2`, p.Limit, p.Offset)
	if err != nil {
		s.renderError(w, r, "observations", err)
		return
	}
	defer rows.Close()

	type row struct {
		ID                                          int64
		EntityID                                    *uuid.UUID
		Canonical, Locale, Spelling, SourceDomain, SourceURL string
		ObservedAt                                  time.Time
		Confidence                                  float64
	}
	items := []row{}
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.ID, &x.EntityID, &x.Canonical, &x.Locale, &x.Spelling,
			&x.SourceDomain, &x.SourceURL, &x.ObservedAt, &x.Confidence); err == nil {
			items = append(items, x)
		}
	}
	s.render(w, r, "kdb_observations.html", map[string]any{
		"title": "Media observations",
		"items": items,
		"page":  "/admin/kdb/observations",
		"p":     p,
	})
}

// --- persons ------------------------------------------------------------

func (s *Server) personsList(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	p := newPage(r, "/admin/persons")

	args := []any{p.Limit, p.Offset}
	where := ""
	if p.Q != "" {
		where = `WHERE name_ko ILIKE '%'||$3||'%' OR name_en ILIKE '%'||$3||'%' OR $3 = ANY(aliases)`
		args = append(args, p.Q)
	}

	rows, err := s.pool.Query(ctx, `
SELECT id, name_ko, COALESCE(name_en,''), primary_role::text,
       COALESCE(agency,''), birth_year, confidence, operator_locked, last_verified_at
FROM kwave_persons `+where+`
ORDER BY last_verified_at DESC
LIMIT $1 OFFSET $2`, args...)
	if err != nil {
		s.renderError(w, r, "persons", err)
		return
	}
	defer rows.Close()

	type row struct {
		ID                                       uuid.UUID
		NameKo, NameEn, Role, Agency             string
		BirthYear                                *int
		Confidence                               float64
		OperatorLocked                           bool
		LastVerifiedAt                           time.Time
	}
	items := []row{}
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.ID, &x.NameKo, &x.NameEn, &x.Role, &x.Agency, &x.BirthYear,
			&x.Confidence, &x.OperatorLocked, &x.LastVerifiedAt); err == nil {
			items = append(items, x)
		}
	}
	s.render(w, r, "persons_list.html", map[string]any{
		"title": "Persons",
		"items": items,
		"page":  "/admin/persons",
		"p":     p,
	})
}

// --- helpers -----------------------------------------------------------

func (s *Server) renderError(w http.ResponseWriter, r *http.Request, where string, err error) {
	w.WriteHeader(http.StatusInternalServerError)
	s.render(w, r, "error.html", map[string]any{
		"title": "Error",
		"where": where,
		"err":   err.Error(),
	})
}

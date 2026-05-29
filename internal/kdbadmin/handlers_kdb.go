package kdbadmin

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// --- candidates (kwave_entity_candidates) -------------------------------

type candidateRow struct {
	KoHint        string
	FirstSeen     time.Time
	LastSeen      time.Time
	ObservedCount int
	SourceDomains []string
	Status        string
	// joined from observations
	Spellings []candidateSpelling
}

type candidateSpelling struct {
	Locale  string
	Sample  string
	Count   int64
}

type candidateStat struct {
	Status string
	Count  int64
}

func (s *Server) kdbCandidates(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	statusFilter := strings.TrimSpace(r.URL.Query().Get("status"))
	if statusFilter == "" {
		statusFilter = "pending"
	}

	// Stats per status.
	stats := []candidateStat{}
	if rows, err := s.pool.Query(ctx, `
SELECT status, COUNT(*) FROM kwave_entity_candidates GROUP BY status ORDER BY 2 DESC`); err == nil {
		defer rows.Close()
		for rows.Next() {
			var x candidateStat
			if err := rows.Scan(&x.Status, &x.Count); err == nil {
				stats = append(stats, x)
			}
		}
	}

	where := "WHERE status = $1"
	args := []any{statusFilter}
	if statusFilter == "all" {
		where = ""
		args = nil
	}

	rows, err := s.pool.Query(ctx, `
SELECT ko_hint, first_seen_at, last_seen_at, observed_count, source_domains, status
FROM kwave_entity_candidates `+where+`
ORDER BY last_seen_at DESC LIMIT 300`, args...)
	if err != nil {
		s.renderError(w, r, "candidates", err)
		return
	}
	defer rows.Close()

	cands := []candidateRow{}
	hints := []string{}
	for rows.Next() {
		var x candidateRow
		if err := rows.Scan(&x.KoHint, &x.FirstSeen, &x.LastSeen, &x.ObservedCount, &x.SourceDomains, &x.Status); err == nil {
			cands = append(cands, x)
			hints = append(hints, x.KoHint)
		}
	}

	// Pull spellings per candidate from observations (join via canonical_ko/aliases is unreliable
	// — fall back to grouping observations whose entity is missing and source_domain matches).
	// For now, summarize spellings by locale aggregated from media_observations linked via
	// kwave_entities.canonical_ko = candidate.ko_hint when such an entity exists; otherwise no rows.
	if len(hints) > 0 {
		spellingsByKo := map[string][]candidateSpelling{}
		if sRows, sErr := s.pool.Query(ctx, `
WITH cand AS (SELECT unnest($1::text[]) AS ko_hint)
SELECT c.ko_hint, o.locale, (array_agg(o.spelling ORDER BY o.observed_at DESC))[1] AS sample, COUNT(*) AS n
FROM cand c
LEFT JOIN kwave_entities e ON e.canonical_ko = c.ko_hint
LEFT JOIN kwave_media_observations o ON o.entity_id = e.id
WHERE o.locale IS NOT NULL
GROUP BY c.ko_hint, o.locale
ORDER BY c.ko_hint, n DESC`, hints); sErr == nil {
			defer sRows.Close()
			for sRows.Next() {
				var ko, loc, sample string
				var n int64
				if err := sRows.Scan(&ko, &loc, &sample, &n); err == nil {
					spellingsByKo[ko] = append(spellingsByKo[ko], candidateSpelling{Locale: loc, Sample: sample, Count: n})
				}
			}
		}
		for i := range cands {
			cands[i].Spellings = spellingsByKo[cands[i].KoHint]
		}
	}

	s.render(w, r, "kdb_candidates.html", map[string]any{
		"title":        "Entity 후보 (candidates)",
		"candidates":   cands,
		"stats":        stats,
		"statusFilter": statusFilter,
		"page":         "/admin/kdb/candidates",
	})
}

// --- codex runs ---------------------------------------------------------

type codexRunRow struct {
	ID              int64
	CycleID         int64
	SourceDomain    string
	Locale          string
	RssTitle        string
	HintCount       int
	Status          string
	SpellingCount   int
	DurationMs      int
	ErrorText       string
	RawSpellingsRaw []byte
	RanAt           time.Time
}

func (r *codexRunRow) RawSpellings() string {
	if len(r.RawSpellingsRaw) == 0 {
		return ""
	}
	s := string(r.RawSpellingsRaw)
	if len(s) > 400 {
		return s[:400] + "…"
	}
	return s
}

type codexStat struct {
	Status string
	Count  int64
}

func (s *Server) kdbCodexRuns(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	p := newPage(r, "/admin/kdb/codex-runs")
	statusFilter := strings.TrimSpace(r.URL.Query().Get("status"))
	if statusFilter != "" {
		p.Extras["status"] = statusFilter
	}

	where := ""
	args := []any{p.Limit, p.Offset}
	if statusFilter != "" {
		where = "WHERE status = $3"
		args = append(args, statusFilter)
	}

	// Total.
	countSQL := "SELECT COUNT(*) FROM kwave_kdb_codex_runs " + where
	countSQL = renumberFromOffset(countSQL, 2)
	var total int64
	if err := s.pool.QueryRow(ctx, countSQL, args[2:]...).Scan(&total); err != nil {
		s.renderError(w, r, "codex runs count", err)
		return
	}
	p.finalize(total)

	rows, err := s.pool.Query(ctx, `
SELECT id, COALESCE(cycle_id,0), source_domain, locale, rss_title,
       hint_count, status, spelling_count, duration_ms,
       COALESCE(error_text,''), raw_spellings::text, ran_at
FROM kwave_kdb_codex_runs `+where+`
ORDER BY ran_at DESC LIMIT $1 OFFSET $2`, args...)
	if err != nil {
		s.renderError(w, r, "codex runs", err)
		return
	}
	defer rows.Close()

	items := []codexRunRow{}
	for rows.Next() {
		var x codexRunRow
		var raw *string
		if err := rows.Scan(&x.ID, &x.CycleID, &x.SourceDomain, &x.Locale, &x.RssTitle,
			&x.HintCount, &x.Status, &x.SpellingCount, &x.DurationMs,
			&x.ErrorText, &raw, &x.RanAt); err == nil {
			if raw != nil {
				x.RawSpellingsRaw = []byte(*raw)
			}
			items = append(items, x)
		}
	}

	// Last-hour stats.
	stats := []codexStat{}
	if sRows, sErr := s.pool.Query(ctx, `
SELECT status, COUNT(*) FROM kwave_kdb_codex_runs
WHERE ran_at > now() - interval '1 hour'
GROUP BY status ORDER BY 2 DESC`); sErr == nil {
		defer sRows.Close()
		for sRows.Next() {
			var x codexStat
			if err := sRows.Scan(&x.Status, &x.Count); err == nil {
				stats = append(stats, x)
			}
		}
	}

	s.render(w, r, "kdb_codex_runs.html", map[string]any{
		"title":        "Codex Audit",
		"runs":         items,
		"stats":        stats,
		"statusFilter": statusFilter,
		"p":            p,
		"page":         "/admin/kdb/codex-runs",
	})
}

// --- observations -------------------------------------------------------

type observationListRow struct {
	ID                       int64
	EntityID                 *uuid.UUID
	Canonical, Locale, Spelling string
	SourceDomain, SourceURL  string
	ObservedAt               time.Time
	Confidence               float64
}

type consensusRow struct {
	KoName, Locale, Spelling string
	Domains                  int
}

func (s *Server) kdbObservations(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	p := newPage(r, "/admin/kdb/observations")
	localeFilter := strings.TrimSpace(r.URL.Query().Get("locale"))
	domainFilter := strings.TrimSpace(r.URL.Query().Get("domain"))
	if localeFilter != "" {
		p.Extras["locale"] = localeFilter
	}
	if domainFilter != "" {
		p.Extras["domain"] = domainFilter
	}

	args := []any{p.Limit, p.Offset}
	conds := []string{}
	nextArg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	if localeFilter != "" {
		conds = append(conds, "o.locale = "+nextArg(localeFilter))
	}
	if domainFilter != "" {
		conds = append(conds, "o.source_domain = "+nextArg(domainFilter))
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}

	// Total.
	countSQL := "SELECT COUNT(*) FROM kwave_media_observations o " + where
	countSQL = renumberFromOffset(countSQL, 2)
	var total int64
	if err := s.pool.QueryRow(ctx, countSQL, args[2:]...).Scan(&total); err != nil {
		s.renderError(w, r, "observations count", err)
		return
	}
	p.finalize(total)

	rows, err := s.pool.Query(ctx, `
SELECT o.id, o.entity_id, COALESCE(e.canonical_ko,''), o.locale, o.spelling,
       o.source_domain, COALESCE(o.source_url,''), o.observed_at, COALESCE(o.confidence,0)
FROM kwave_media_observations o
LEFT JOIN kwave_entities e ON e.id = o.entity_id
`+where+`
ORDER BY o.observed_at DESC LIMIT $1 OFFSET $2`, args...)
	if err != nil {
		s.renderError(w, r, "observations", err)
		return
	}
	defer rows.Close()

	items := []observationListRow{}
	for rows.Next() {
		var x observationListRow
		if err := rows.Scan(&x.ID, &x.EntityID, &x.Canonical, &x.Locale, &x.Spelling,
			&x.SourceDomain, &x.SourceURL, &x.ObservedAt, &x.Confidence); err == nil {
			items = append(items, x)
		}
	}

	// Consensus: same (entity, locale, spelling) seen by ≥2 domains.
	consensus := []consensusRow{}
	if cRows, cErr := s.pool.Query(ctx, `
SELECT e.canonical_ko, o.locale, o.spelling, COUNT(DISTINCT o.source_domain) AS d
FROM kwave_media_observations o
JOIN kwave_entities e ON e.id = o.entity_id
WHERE o.observed_at > now() - interval '30 days'
GROUP BY e.canonical_ko, o.locale, o.spelling
HAVING COUNT(DISTINCT o.source_domain) >= 2
ORDER BY d DESC, e.canonical_ko LIMIT 100`); cErr == nil {
		defer cRows.Close()
		for cRows.Next() {
			var x consensusRow
			if err := cRows.Scan(&x.KoName, &x.Locale, &x.Spelling, &x.Domains); err == nil {
				consensus = append(consensus, x)
			}
		}
	}

	s.render(w, r, "kdb_observations.html", map[string]any{
		"title":        "매체 표기 관측",
		"items":        items,
		"consensus":    consensus,
		"localeFilter": localeFilter,
		"domainFilter": domainFilter,
		"p":            p,
		"page":         "/admin/kdb/observations",
	})
}

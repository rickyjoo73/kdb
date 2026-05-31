package kdbadmin

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

type personRow struct {
	ID                     uuid.UUID
	NameKo, NameEn, NameJa string
	Role                   string
	SecondaryRoles         []string
	Agency                 string
	Groups                 []string
	BirthYear              *int
	Gender                 string
	NotableWorks           []string
	CategoryHint           string
	Confidence             float64
	OperatorLocked         bool
	LastVerifiedAt         time.Time
}

type roleCount struct {
	Role  string
	Count int64
}

type personQueueRow struct {
	NameKo    string
	Status    string
	Attempts  int
	Hint      string
	CreatedAt time.Time
}

func (s *Server) personsList(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	p := newPage(r, "/admin/persons")
	roleFilter := strings.TrimSpace(r.URL.Query().Get("role"))
	if roleFilter != "" {
		p.Extras["role"] = roleFilter
	}

	args := []any{p.Limit, p.Offset}
	conds := []string{}
	nextArg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	if p.Q != "" {
		ph := nextArg(p.Q)
		conds = append(conds, "(name_ko ILIKE '%'||"+ph+"||'%' OR name_en ILIKE '%'||"+ph+"||'%' OR name_ja ILIKE '%'||"+ph+"||'%' OR "+ph+" = ANY(aliases))")
	}
	if roleFilter != "" {
		ph := nextArg(roleFilter)
		conds = append(conds, "primary_role = "+ph+"::person_role")
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}

	// Total.
	countSQL := "SELECT COUNT(*) FROM kwave_persons " + where
	countSQL = renumberFromOffset(countSQL, 2)
	var total int64
	if err := s.pool.QueryRow(ctx, countSQL, args[2:]...).Scan(&total); err != nil {
		s.renderError(w, r, "persons count", err)
		return
	}
	p.finalize(total)

	rows, err := s.pool.Query(ctx, `
SELECT id, name_ko, COALESCE(name_en,''), COALESCE(name_ja,''),
       primary_role::text, secondary_roles::text[],
       COALESCE(agency,''), groups, birth_year, COALESCE(gender,''), notable_works,
       COALESCE(category_hint,''), confidence, operator_locked, last_verified_at
FROM kwave_persons `+where+`
ORDER BY last_verified_at DESC
LIMIT $1 OFFSET $2`, args...)
	if err != nil {
		s.renderError(w, r, "persons list", err)
		return
	}
	defer rows.Close()

	items := []personRow{}
	for rows.Next() {
		var x personRow
		if err := rows.Scan(&x.ID, &x.NameKo, &x.NameEn, &x.NameJa,
			&x.Role, &x.SecondaryRoles,
			&x.Agency, &x.Groups, &x.BirthYear, &x.Gender, &x.NotableWorks,
			&x.CategoryHint, &x.Confidence, &x.OperatorLocked, &x.LastVerifiedAt); err == nil {
			items = append(items, x)
		}
	}

	// Role distribution.
	roles := []roleCount{}
	if rRows, rErr := s.pool.Query(ctx, `
SELECT primary_role::text, COUNT(*) FROM kwave_persons GROUP BY 1 ORDER BY 2 DESC`); rErr == nil {
		defer rRows.Close()
		for rRows.Next() {
			var x roleCount
			if err := rRows.Scan(&x.Role, &x.Count); err == nil {
				roles = append(roles, x)
			}
		}
	}

	// Person research queue (last 50).
	queue := []personQueueRow{}
	if qRows, qErr := s.pool.Query(ctx, `
SELECT name_ko, status, attempts, COALESCE(context_hint,''), created_at
FROM kwave_person_research_queue
ORDER BY (status='pending') DESC, created_at DESC LIMIT 50`); qErr == nil {
		defer qRows.Close()
		for qRows.Next() {
			var x personQueueRow
			if err := qRows.Scan(&x.NameKo, &x.Status, &x.Attempts, &x.Hint, &x.CreatedAt); err == nil {
				queue = append(queue, x)
			}
		}
	}

	// Per-locale fill bars for kwave_persons (ko/en/ja/vi). Mirrors the
	// entities locale-gap 진도바: same ≥80/≥50 thresholds via localeProgress.
	personProgress := s.personsLocaleProgress(ctx)

	s.render(w, r, "persons_list.html", map[string]any{
		"title":      "인물 DB",
		"items":      items,
		"p":          p,
		"roles":      roles,
		"roleFilter": roleFilter,
		"allRoles":   personRoles,
		"queue":      queue,
		"progress":   personProgress,
	})
}

// personsLocaleProgress computes per-locale fill progress for kwave_persons.
// Returns one localeProgress per language column (ko/en/ja/vi) using the same
// Filled/Total/Pct/bar-color convention as localeProgressData (entities).
func (s *Server) personsLocaleProgress(ctx context.Context) []localeProgress {
	var total int64
	_ = s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM kwave_persons`).Scan(&total)
	if total == 0 {
		return nil
	}
	cols := []struct {
		Locale, Col string
	}{
		{"ko", "name_ko"}, {"en", "name_en"}, {"ja", "name_ja"}, {"vi", "name_vi"},
		{"es", "name_es"}, {"id", "name_id"}, {"pt-br", "name_pt_br"},
		{"zh", "name_zh"}, {"zh-hant", "name_zh_hant"},
	}
	out := []localeProgress{}
	for _, c := range cols {
		var filled int64
		_ = s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM kwave_persons WHERE `+c.Col+` IS NOT NULL AND `+c.Col+` <> ''`).Scan(&filled)
		pct := int(filled * 100 / total)
		bar := "bg-orange-500"
		if pct >= 80 {
			bar = "bg-emerald-500"
		} else if pct >= 50 {
			bar = "bg-blue-500"
		}
		out = append(out, localeProgress{Locale: c.Locale, Filled: filled, Total: total, Pct: pct, IsBar: bar})
	}
	return out
}

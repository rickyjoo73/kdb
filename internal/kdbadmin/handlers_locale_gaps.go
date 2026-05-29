package kdbadmin

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/rickyjoo73/kdb/internal/kdb/enrich"
)

// --- WF-3: 누락 locale 페이지 (/admin/entities/locale-gaps) -------------

type localeGapRow struct {
	ID         uuid.UUID
	Ko         string
	EntityType string
	Confidence float64
	Filled     []localeMark
	Missing    []string
}

type localeProgress struct {
	Locale  string
	Filled  int64
	Total   int64
	Pct     int
	IsBar   string // "bg-emerald-500" / "bg-blue-500" / "bg-orange-500"
}

func (s *Server) entitiesLocaleGaps(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	p := newPage(r, "/admin/entities/locale-gaps")
	priorityFilter := strings.TrimSpace(r.URL.Query().Get("locale")) // "vi" / "pt-br" etc.
	typeFilter := strings.TrimSpace(r.URL.Query().Get("type"))
	if priorityFilter != "" {
		p.Extras["locale"] = priorityFilter
	}
	if typeFilter != "" {
		p.Extras["type"] = typeFilter
	}

	// locale 진도바.
	progress := s.localeProgressData(ctx)

	// WHERE 빌드.
	conds := []string{"status='active'", "confidence >= 0.5"}
	args := []any{p.Limit, p.Offset}
	nextArg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	if typeFilter != "" {
		conds = append(conds, "entity_type = "+nextArg(typeFilter)+"::kwave_entity_type")
	}
	// 누락 locale 조건.
	priorityLocales := []string{"en", "ja", "vi", "id", "es", "pt_br", "zh_hant"}
	if priorityFilter != "" {
		col := localeFilterColumn(priorityFilter)
		if col != "" {
			conds = append(conds, "("+col+" IS NULL OR "+col+" = '')")
		}
	} else {
		// 우선순위 locale 중 ≥ 1 빈 것.
		missCond := []string{}
		for _, l := range priorityLocales {
			col := localeFilterColumn(l)
			if col != "" {
				missCond = append(missCond, col+" IS NULL OR "+col+" = ''")
			}
		}
		conds = append(conds, "("+strings.Join(missCond, " OR ")+")")
	}
	where := "WHERE " + strings.Join(conds, " AND ")

	// total count.
	countSQL := "SELECT COUNT(*) FROM kwave_entities " + where
	countSQL = renumberFromOffset(countSQL, 2)
	var total int64
	if err := s.pool.QueryRow(ctx, countSQL, args[2:]...).Scan(&total); err != nil {
		s.renderError(w, r, "locale-gaps count", err)
		return
	}
	p.finalize(total)

	rows, err := s.pool.Query(ctx, `
SELECT id, canonical_ko, entity_type::text, confidence,
       COALESCE(canonical_en,''),    COALESCE(canonical_en_source,''),
       COALESCE(canonical_ja,''),    COALESCE(canonical_ja_source,''),
       COALESCE(canonical_vi,''),    COALESCE(canonical_vi_source,''),
       COALESCE(canonical_id,''),    COALESCE(canonical_id_source,''),
       COALESCE(canonical_es,''),    COALESCE(canonical_es_source,''),
       COALESCE(canonical_pt_br,''), COALESCE(canonical_pt_br_source,''),
       COALESCE(canonical_zh_hant,''),COALESCE(canonical_zh_hant_source,'')
FROM kwave_entities `+where+`
ORDER BY confidence DESC, updated_at DESC
LIMIT $1 OFFSET $2`, args...)
	if err != nil {
		s.renderError(w, r, "locale-gaps list", err)
		return
	}
	defer rows.Close()

	items := []localeGapRow{}
	for rows.Next() {
		var x localeGapRow
		var en, enS, ja, jaS, vi, viS, id, idS, es, esS, pt, ptS, zh, zhS string
		if err := rows.Scan(&x.ID, &x.Ko, &x.EntityType, &x.Confidence,
			&en, &enS, &ja, &jaS, &vi, &viS, &id, &idS, &es, &esS, &pt, &ptS, &zh, &zhS); err != nil {
			continue
		}
		for _, lv := range []struct {
			Loc, V, S string
		}{
			{"en", en, enS}, {"ja", ja, jaS}, {"vi", vi, viS}, {"id", id, idS},
			{"es", es, esS}, {"pt-br", pt, ptS}, {"zh-hant", zh, zhS},
		} {
			if lv.V == "" {
				x.Missing = append(x.Missing, lv.Loc)
			} else {
				m := mark(lv.V, lv.S)
				m.Source = lv.Loc + ":" + m.Source // tooltip 용
				x.Filled = append(x.Filled, m)
			}
		}
		items = append(items, x)
	}

	s.render(w, r, "entities_locale_gaps.html", map[string]any{
		"title":          "누락 locale (WF-3)",
		"items":          items,
		"p":              p,
		"progress":       progress,
		"priorityFilter": priorityFilter,
		"typeFilter":     typeFilter,
		"entityTypes":    entityTypes,
		"priorityLocales": []string{"en", "ja", "vi", "id", "es", "pt-br", "zh-hant"},
		"flash":          r.URL.Query().Get("flash"),
		"page":           "/admin/entities/locale-gaps",
	})
}

func (s *Server) localeProgressData(ctx context.Context) []localeProgress {
	var total int64
	_ = s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM kwave_entities WHERE status='active'`).Scan(&total)
	if total == 0 {
		return nil
	}
	cols := []struct {
		Locale, Col string
	}{
		{"en", "canonical_en"}, {"ja", "canonical_ja"}, {"vi", "canonical_vi"},
		{"id", "canonical_id"}, {"es", "canonical_es"}, {"pt-br", "canonical_pt_br"},
		{"zh-hant", "canonical_zh_hant"}, {"zh", "canonical_zh"},
	}
	out := []localeProgress{}
	for _, c := range cols {
		var filled int64
		_ = s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM kwave_entities WHERE status='active' AND `+c.Col+` IS NOT NULL AND `+c.Col+` <> ''`).Scan(&filled)
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

func localeFilterColumn(loc string) string {
	switch strings.ToLower(loc) {
	case "en":
		return "canonical_en"
	case "ja":
		return "canonical_ja"
	case "vi":
		return "canonical_vi"
	case "id":
		return "canonical_id"
	case "es":
		return "canonical_es"
	case "pt-br", "pt_br":
		return "canonical_pt_br"
	case "zh-hant", "zh_hant":
		return "canonical_zh_hant"
	case "zh":
		return "canonical_zh"
	}
	return ""
}

// --- WF-3 액션: 단일 entity enrich ----------------------------------------

func (s *Server) entityEnrich(w http.ResponseWriter, r *http.Request) {
	id, ok := parseEntityID(w, r)
	if !ok {
		return
	}
	// codex fallback 포함이라 시간 길어질 수 있음 — 120s 까지.
	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()

	orch := enrich.New(s.pool)
	rep, err := orch.Enrich(ctx, id)
	if err != nil {
		s.redirectBack(w, r, "enrich 실패: "+err.Error())
		return
	}
	if rep == nil {
		s.redirectBack(w, r, "enrich: 결과 없음")
		return
	}
	msg := "enrich " + rep.Ko + " [" + strings.Join(rep.LayersRun, "→") + "] "
	msg += "채움 " + strconv.Itoa(len(rep.Filled)) + " · 빈채 " + strconv.Itoa(len(rep.StillEmpty))
	s.redirectBack(w, r, msg)
}

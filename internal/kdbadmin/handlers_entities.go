package kdbadmin

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/rickyjoo73/kdb/internal/kdb"
)

// localeMark — 한 locale 값 + source 조합. UI 뱃지 표시용.
type localeMark struct {
	Value  string
	Source string
	Mark   string
	Class  string
}

func mark(value, source string) localeMark {
	src := kdb.Source(source)
	return localeMark{
		Value:  value,
		Source: source,
		Mark:   kdb.Mark(src),
		Class:  kdb.MarkClass(src),
	}
}

// --- entities list ------------------------------------------------------

type entityRow struct {
	ID             uuid.UUID
	Type           string
	Ko             string
	En, Ja, Vi, ID_, Es, PtBr, ZhHant, Zh localeMark
	AliasesKo      []string
	CategoryHint   string
	Notes          string
	Confidence     float64
	OperatorLocked bool
	Status         string
	LastVerifiedAt time.Time
	UpdatedAt      time.Time
}

type typeCount struct {
	Type  string
	Count int64
}

type queueRow struct {
	ID        uuid.UUID
	EntityKo  string
	Type      string
	Status    string
	Attempts  int
	Hint      string
	CreatedAt time.Time
}

func (s *Server) entitiesList(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	p := newPage(r, "/admin/entities")
	typeFilter := strings.TrimSpace(r.URL.Query().Get("type"))
	if typeFilter != "" {
		p.Extras["type"] = typeFilter
	}

	// WHERE conditions.
	args := []any{p.Limit, p.Offset}
	conds := []string{}
	nextArg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	if p.Q != "" {
		// search across all canonical_* and aliases
		ph := nextArg(p.Q)
		conds = append(conds, "(canonical_ko ILIKE '%'||"+ph+"||'%' OR canonical_en ILIKE '%'||"+ph+"||'%' OR canonical_ja ILIKE '%'||"+ph+"||'%' OR canonical_vi ILIKE '%'||"+ph+"||'%' OR canonical_zh_hant ILIKE '%'||"+ph+"||'%' OR canonical_id ILIKE '%'||"+ph+"||'%' OR canonical_es ILIKE '%'||"+ph+"||'%' OR canonical_pt_br ILIKE '%'||"+ph+"||'%' OR "+ph+" = ANY(aliases_ko) OR "+ph+" = ANY(aliases_en))")
	}
	if typeFilter != "" {
		ph := nextArg(typeFilter)
		conds = append(conds, "entity_type = "+ph+"::kwave_entity_type")
	} else {
		// 고유명사DB 는 인물 제외 — 인물은 인물DB(kwave_persons) 에서 본다.
		// 그룹/드라마/영화/앨범/프로그램/브랜드 등만 노출.
		// type=person 을 명시 선택했을 때만 예외적으로 보인다.
		conds = append(conds, "entity_type <> 'person'::kwave_entity_type")
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}

	// Total count (uses args except $1/$2 — count uses positions $3+).
	countArgs := args[2:]
	var total int64
	countSQL := "SELECT COUNT(*) FROM kwave_entities " + where
	// Replace $3 → $1, $4 → $2 etc. in countSQL.
	countSQL = renumberFromOffset(countSQL, 2)
	if err := s.pool.QueryRow(ctx, countSQL, countArgs...).Scan(&total); err != nil {
		s.renderError(w, r, "entities count", err)
		return
	}
	p.finalize(total)

	// List rows.
	rows, err := s.pool.Query(ctx, `
SELECT id, entity_type::text, canonical_ko,
       COALESCE(canonical_en,''),    COALESCE(canonical_en_source,''),
       COALESCE(canonical_ja,''),    COALESCE(canonical_ja_source,''),
       COALESCE(canonical_vi,''),    COALESCE(canonical_vi_source,''),
       COALESCE(canonical_id,''),    COALESCE(canonical_id_source,''),
       COALESCE(canonical_es,''),    COALESCE(canonical_es_source,''),
       COALESCE(canonical_pt_br,''), COALESCE(canonical_pt_br_source,''),
       COALESCE(canonical_zh_hant,''), COALESCE(canonical_zh_hant_source,''),
       COALESCE(canonical_zh,''),    COALESCE(canonical_zh_source,''),
       aliases_ko, COALESCE(category_hint,''), COALESCE(notes,''),
       confidence, operator_locked, status, last_verified_at, updated_at
FROM kwave_entities `+where+`
ORDER BY updated_at DESC
LIMIT $1 OFFSET $2`, args...)
	if err != nil {
		s.renderError(w, r, "entities list", err)
		return
	}
	defer rows.Close()

	items := make([]entityRow, 0, p.Limit)
	for rows.Next() {
		var x entityRow
		var enV, enS, jaV, jaS, viV, viS, idV, idS, esV, esS, ptV, ptS, zhHantV, zhHantS, zhV, zhS string
		if err := rows.Scan(
			&x.ID, &x.Type, &x.Ko,
			&enV, &enS, &jaV, &jaS, &viV, &viS, &idV, &idS,
			&esV, &esS, &ptV, &ptS, &zhHantV, &zhHantS, &zhV, &zhS,
			&x.AliasesKo, &x.CategoryHint, &x.Notes,
			&x.Confidence, &x.OperatorLocked, &x.Status, &x.LastVerifiedAt, &x.UpdatedAt,
		); err != nil {
			s.renderError(w, r, "entities scan", err)
			return
		}
		x.En = mark(enV, enS)
		x.Ja = mark(jaV, jaS)
		x.Vi = mark(viV, viS)
		x.ID_ = mark(idV, idS)
		x.Es = mark(esV, esS)
		x.PtBr = mark(ptV, ptS)
		x.ZhHant = mark(zhHantV, zhHantS)
		x.Zh = mark(zhV, zhS)
		items = append(items, x)
	}

	// Type distribution.
	counts := []typeCount{}
	if cRows, cErr := s.pool.Query(ctx, `
SELECT entity_type::text AS t, COUNT(*) FROM kwave_entities GROUP BY t ORDER BY 2 DESC`); cErr == nil {
		defer cRows.Close()
		for cRows.Next() {
			var c typeCount
			if err := cRows.Scan(&c.Type, &c.Count); err == nil {
				counts = append(counts, c)
			}
		}
	}

	// Research queue (last 50).
	queue := []queueRow{}
	if qRows, qErr := s.pool.Query(ctx, `
SELECT id, entity_ko, requested_entity_type::text, status, attempts, COALESCE(context_hint,''), created_at
FROM kwave_entity_research_queue
ORDER BY (status='pending') DESC, created_at DESC LIMIT 50`); qErr == nil {
		defer qRows.Close()
		for qRows.Next() {
			var q queueRow
			if err := qRows.Scan(&q.ID, &q.EntityKo, &q.Type, &q.Status, &q.Attempts, &q.Hint, &q.CreatedAt); err == nil {
				queue = append(queue, q)
			}
		}
	}

	s.render(w, r, "entities_list.html", map[string]any{
		"title":      "고유명사 DB",
		"items":      items,
		"p":          p,
		"types":      entityTypes,
		"typeFilter": typeFilter,
		"counts":     counts,
		"queue":      queue,
	})
}

// renumberFromOffset replaces $(n+offset) with $n for n=1,2,...
// Used so a WHERE clause built for the LIMIT/OFFSET query can be reused for COUNT(*).
func renumberFromOffset(sql string, offset int) string {
	// naive but adequate: walk from high to low so $10 isn't broken by $1 substitution.
	for i := 30; i >= 1; i-- {
		old := "$" + strconv.Itoa(i+offset)
		new_ := "$" + strconv.Itoa(i)
		sql = strings.ReplaceAll(sql, old, new_)
	}
	return sql
}

// --- entity detail ------------------------------------------------------

type entityDetail struct {
	ID              uuid.UUID
	Type            string
	Ko              string
	En, Ja, Vi, IDLang, Es, PtBr, ZhHant, Zh localeMark
	AliasesKo, AliasesEn, AliasesJa, AliasesVi   []string
	AliasesID, AliasesEs, AliasesPtBr, AliasesZhHant, AliasesZh []string
	Confidence      float64
	OperatorLocked  bool
	CategoryHint    string
	Notes           string
	SourceURLs      []string
	Status          string
	Disambig        string
	NeedsDisambig   bool
	LastVerifiedAt  time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
	ResearchSession string

	// related sections
	Person       *personDetail
	Siblings     []siblingRow
	Relations    []relationRow
	ExternalRefs []externalRefRow
	Resolution   []resolutionRow
	History      []historyRow
	Observations []observationRow
}

// siblingRow = 같은 canonical_ko 를 가진 다른 entity (동명이인 형제).
type siblingRow struct {
	ID            uuid.UUID
	EntityType    string
	Disambig      string
	PrimaryRole   string
	Agency        string
	Confidence    float64
	Status        string
	NeedsDisambig bool
}

type personDetail struct {
	PrimaryRole    string
	SecondaryRoles []string
	Groups         []string
	Agency         string
	Gender         string
	BirthYear      *int
	NotableWorks   []string
}

type relationRow struct {
	Direction    string
	RelationType string
	EntityID     uuid.UUID
	EntityKo     string
	EntityType   string
	Confidence   float64
	SourceURLs   []string
}

type externalRefRow struct {
	Provider   string
	ExternalID string
	URL        string
	Confidence float64
	FetchedAt  time.Time
}

type resolutionRow struct {
	Provider       string
	Status         string
	CandidateCount *int
	MatchScore     *float64
	DurationMs     *int
	ErrorText      string
	AttemptedAt    time.Time
}

type historyRow struct {
	Status     string
	Type       string
	Attempts   int
	Hint       string
	LastError  string
	CreatedAt  time.Time
	PickedAt   *time.Time
	FinishedAt *time.Time
}

type observationRow struct {
	ID                       int64
	Locale, Spelling         string
	SourceDomain, SourceURL  string
	ObservedAt               time.Time
	Confidence               float64
}

func (s *Server) entityDetail(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	var e entityDetail
	var enV, enS, jaV, jaS, viV, viS, idV, idS, esV, esS, ptV, ptS, zhHantV, zhHantS, zhV, zhS string
	err = s.pool.QueryRow(ctx, `
SELECT id, entity_type::text, canonical_ko,
       COALESCE(canonical_en,''),    COALESCE(canonical_en_source,''),
       COALESCE(canonical_ja,''),    COALESCE(canonical_ja_source,''),
       COALESCE(canonical_vi,''),    COALESCE(canonical_vi_source,''),
       COALESCE(canonical_id,''),    COALESCE(canonical_id_source,''),
       COALESCE(canonical_es,''),    COALESCE(canonical_es_source,''),
       COALESCE(canonical_pt_br,''), COALESCE(canonical_pt_br_source,''),
       COALESCE(canonical_zh_hant,''), COALESCE(canonical_zh_hant_source,''),
       COALESCE(canonical_zh,''),    COALESCE(canonical_zh_source,''),
       aliases_ko, aliases_en, aliases_ja, aliases_vi,
       aliases_id, aliases_es, aliases_pt_br, aliases_zh_hant, aliases_zh,
       confidence, operator_locked,
       COALESCE(category_hint,''), COALESCE(notes,''),
       source_urls, status, COALESCE(disambig,''), needs_disambig,
       last_verified_at, created_at, updated_at,
       COALESCE(research_session,'')
FROM kwave_entities WHERE id = $1`, id).Scan(
		&e.ID, &e.Type, &e.Ko,
		&enV, &enS, &jaV, &jaS, &viV, &viS, &idV, &idS,
		&esV, &esS, &ptV, &ptS, &zhHantV, &zhHantS, &zhV, &zhS,
		&e.AliasesKo, &e.AliasesEn, &e.AliasesJa, &e.AliasesVi,
		&e.AliasesID, &e.AliasesEs, &e.AliasesPtBr, &e.AliasesZhHant, &e.AliasesZh,
		&e.Confidence, &e.OperatorLocked,
		&e.CategoryHint, &e.Notes,
		&e.SourceURLs, &e.Status, &e.Disambig, &e.NeedsDisambig,
		&e.LastVerifiedAt, &e.CreatedAt, &e.UpdatedAt,
		&e.ResearchSession)
	if err != nil {
		s.renderError(w, r, "entity detail", err)
		return
	}
	e.En = mark(enV, enS)
	e.Ja = mark(jaV, jaS)
	e.Vi = mark(viV, viS)
	e.IDLang = mark(idV, idS)
	e.Es = mark(esV, esS)
	e.PtBr = mark(ptV, ptS)
	e.ZhHant = mark(zhHantV, zhHantS)
	e.Zh = mark(zhV, zhS)

	// Person details (LEFT JOIN by canonical_ko = name_ko for person-type entities).
	if e.Type == "person" {
		var pd personDetail
		if err := s.pool.QueryRow(ctx, `
SELECT primary_role::text, secondary_roles::text[], groups, COALESCE(agency,''),
       COALESCE(gender,''), birth_year, notable_works
FROM kwave_persons WHERE name_ko = $1 LIMIT 1`, e.Ko).Scan(
			&pd.PrimaryRole, &pd.SecondaryRoles, &pd.Groups, &pd.Agency,
			&pd.Gender, &pd.BirthYear, &pd.NotableWorks); err == nil {
			e.Person = &pd
		}
		// Fallback: kwave_entity_person_details by entity_id.
		if e.Person == nil {
			if err := s.pool.QueryRow(ctx, `
SELECT primary_role::text, secondary_roles::text[], groups, COALESCE(agency,''),
       COALESCE(gender,''), birth_year, notable_works
FROM kwave_entity_person_details WHERE entity_id = $1`, e.ID).Scan(
				&pd.PrimaryRole, &pd.SecondaryRoles, &pd.Groups, &pd.Agency,
				&pd.Gender, &pd.BirthYear, &pd.NotableWorks); err == nil {
				e.Person = &pd
			}
		}
	}

	// 동명이인 형제: 같은 canonical_ko, 다른 id (rejected 제외).
	if sRows, sErr := s.pool.Query(ctx, `
SELECT e.id, e.entity_type::text, COALESCE(e.disambig,''),
       COALESCE(d.primary_role::text,''), COALESCE(d.agency,''),
       e.confidence, e.status, e.needs_disambig
FROM kwave_entities e
LEFT JOIN kwave_entity_person_details d ON d.entity_id = e.id
WHERE e.canonical_ko = $1 AND e.id <> $2 AND e.status <> 'rejected'
ORDER BY e.confidence DESC LIMIT 50`, e.Ko, id); sErr == nil {
		defer sRows.Close()
		for sRows.Next() {
			var x siblingRow
			if err := sRows.Scan(&x.ID, &x.EntityType, &x.Disambig, &x.PrimaryRole,
				&x.Agency, &x.Confidence, &x.Status, &x.NeedsDisambig); err == nil {
				e.Siblings = append(e.Siblings, x)
			}
		}
	}

	// Relations (both directions).
	if rRows, rErr := s.pool.Query(ctx, `
SELECT 'from→to' AS dir, r.relation_type, e2.id, e2.canonical_ko, e2.entity_type::text,
       r.confidence, r.source_urls
FROM kwave_entity_relations r
JOIN kwave_entities e2 ON e2.id = r.to_entity_id
WHERE r.from_entity_id = $1
UNION ALL
SELECT 'to←from', r.relation_type, e1.id, e1.canonical_ko, e1.entity_type::text,
       r.confidence, r.source_urls
FROM kwave_entity_relations r
JOIN kwave_entities e1 ON e1.id = r.from_entity_id
WHERE r.to_entity_id = $1
ORDER BY 1`, id); rErr == nil {
		defer rRows.Close()
		for rRows.Next() {
			var x relationRow
			if err := rRows.Scan(&x.Direction, &x.RelationType, &x.EntityID, &x.EntityKo,
				&x.EntityType, &x.Confidence, &x.SourceURLs); err == nil {
				e.Relations = append(e.Relations, x)
			}
		}
	}

	// External refs.
	if eRows, eErr := s.pool.Query(ctx, `
SELECT provider, external_id, COALESCE(url,''), confidence, fetched_at
FROM kwave_entity_external_refs WHERE entity_id = $1
ORDER BY fetched_at DESC LIMIT 50`, id); eErr == nil {
		defer eRows.Close()
		for eRows.Next() {
			var x externalRefRow
			if err := eRows.Scan(&x.Provider, &x.ExternalID, &x.URL, &x.Confidence, &x.FetchedAt); err == nil {
				e.ExternalRefs = append(e.ExternalRefs, x)
			}
		}
	}

	// Resolution attempts.
	if aRows, aErr := s.pool.Query(ctx, `
SELECT provider, status, candidate_count, match_score, duration_ms, COALESCE(error_text,''), attempted_at
FROM kwave_entity_resolution_attempts WHERE entity_id = $1
ORDER BY attempted_at DESC LIMIT 50`, id); aErr == nil {
		defer aRows.Close()
		for aRows.Next() {
			var x resolutionRow
			if err := aRows.Scan(&x.Provider, &x.Status, &x.CandidateCount, &x.MatchScore,
				&x.DurationMs, &x.ErrorText, &x.AttemptedAt); err == nil {
				e.Resolution = append(e.Resolution, x)
			}
		}
	}

	// Research history (by entity_ko = canonical_ko).
	if hRows, hErr := s.pool.Query(ctx, `
SELECT status, requested_entity_type::text, attempts, COALESCE(context_hint,''),
       COALESCE(last_error,''), created_at, picked_at, finished_at
FROM kwave_entity_research_queue WHERE entity_ko = $1
ORDER BY created_at DESC LIMIT 20`, e.Ko); hErr == nil {
		defer hRows.Close()
		for hRows.Next() {
			var x historyRow
			if err := hRows.Scan(&x.Status, &x.Type, &x.Attempts, &x.Hint, &x.LastError,
				&x.CreatedAt, &x.PickedAt, &x.FinishedAt); err == nil {
				e.History = append(e.History, x)
			}
		}
	}

	// Observations.
	if oRows, oErr := s.pool.Query(ctx, `
SELECT id, locale, spelling, source_domain, COALESCE(source_url,''), observed_at, COALESCE(confidence,0)
FROM kwave_media_observations WHERE entity_id = $1
ORDER BY observed_at DESC LIMIT 100`, id); oErr == nil {
		defer oRows.Close()
		for oRows.Next() {
			var x observationRow
			if err := oRows.Scan(&x.ID, &x.Locale, &x.Spelling, &x.SourceDomain, &x.SourceURL,
				&x.ObservedAt, &x.Confidence); err == nil {
				e.Observations = append(e.Observations, x)
			}
		}
	}

	s.render(w, r, "entity_detail.html", map[string]any{
		"title": e.Ko,
		"e":     e,
		"flash": r.URL.Query().Get("flash"),
		"page":  "/admin/entities",
	})
}

// --- entity sources -----------------------------------------------------

type localeCoverage struct {
	Locale  string
	Filled  int64
	Total   int64
	Rate    float64
	RateStr string
	RatePct int
}

type wlSummary struct {
	Locale   string
	Enabled  int
	Disabled int
}

type resolutionStat struct {
	Provider                                       string
	Total, Matched, NoMatch, Disamb, Conflict, Errored int64
	AvgMs                                          int
	LastCall                                       *time.Time
}

type extRefDist struct {
	Provider string
	Count    int64
}

type missingEntity struct {
	ID         uuid.UUID
	Ko         string
	EntityType string
	Confidence float64
	Known      string
	Missing    []string
}

func (s *Server) entitySources(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// Total entities for denominator.
	var totalEntities int64
	_ = s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM kwave_entities WHERE confidence >= 0.5`).Scan(&totalEntities)

	// Locale coverage.
	localeCols := []struct {
		Locale, Col string
	}{
		{"ko", "canonical_ko"},
		{"en", "canonical_en"},
		{"ja", "canonical_ja"},
		{"vi", "canonical_vi"},
		{"zh-Hant", "canonical_zh_hant"},
		{"id", "canonical_id"},
		{"es", "canonical_es"},
		{"pt-BR", "canonical_pt_br"},
		{"zh legacy", "canonical_zh"},
	}
	coverages := make([]localeCoverage, 0, len(localeCols))
	for _, lc := range localeCols {
		var filled int64
		if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM kwave_entities WHERE confidence >= 0.5 AND `+lc.Col+` IS NOT NULL AND `+lc.Col+` <> ''`).Scan(&filled); err != nil {
			continue
		}
		c := localeCoverage{Locale: lc.Locale, Filled: filled, Total: totalEntities}
		if totalEntities > 0 {
			c.Rate = float64(filled) / float64(totalEntities)
		}
		c.RateStr = strconv.FormatFloat(c.Rate*100, 'f', 1, 64) + "%"
		c.RatePct = int(c.Rate * 100)
		coverages = append(coverages, c)
	}

	// Whitelist locale summary.
	wlSummaries := []wlSummary{}
	if wRows, wErr := s.pool.Query(ctx, `
SELECT locale,
       SUM(CASE WHEN enabled THEN 1 ELSE 0 END) AS enabled,
       SUM(CASE WHEN enabled THEN 0 ELSE 1 END) AS disabled
FROM kwave_news_whitelist GROUP BY locale ORDER BY locale`); wErr == nil {
		defer wRows.Close()
		for wRows.Next() {
			var x wlSummary
			if err := wRows.Scan(&x.Locale, &x.Enabled, &x.Disabled); err == nil {
				wlSummaries = append(wlSummaries, x)
			}
		}
	}

	// 24h cascade stats.
	stats := []resolutionStat{}
	if sRows, sErr := s.pool.Query(ctx, `
SELECT provider,
       COUNT(*) AS total,
       SUM(CASE WHEN status='matched' THEN 1 ELSE 0 END) AS matched,
       SUM(CASE WHEN status='no-match' THEN 1 ELSE 0 END) AS no_match,
       SUM(CASE WHEN status='disambiguation-fail' THEN 1 ELSE 0 END) AS disamb,
       SUM(CASE WHEN status='conflict' THEN 1 ELSE 0 END) AS conflict,
       SUM(CASE WHEN status='error' THEN 1 ELSE 0 END) AS errored,
       AVG(duration_ms)::int AS avg_ms,
       MAX(attempted_at) AS last_call
FROM kwave_entity_resolution_attempts
WHERE attempted_at > now() - interval '24 hours'
GROUP BY provider ORDER BY total DESC`); sErr == nil {
		defer sRows.Close()
		for sRows.Next() {
			var x resolutionStat
			var avgMs *int
			if err := sRows.Scan(&x.Provider, &x.Total, &x.Matched, &x.NoMatch, &x.Disamb,
				&x.Conflict, &x.Errored, &avgMs, &x.LastCall); err == nil {
				if avgMs != nil {
					x.AvgMs = *avgMs
				}
				stats = append(stats, x)
			}
		}
	}

	// External ref distribution.
	refDist := []extRefDist{}
	if rRows, rErr := s.pool.Query(ctx, `
SELECT provider, COUNT(*) FROM kwave_entity_external_refs GROUP BY provider ORDER BY 2 DESC`); rErr == nil {
		defer rRows.Close()
		for rRows.Next() {
			var x extRefDist
			if err := rRows.Scan(&x.Provider, &x.Count); err == nil {
				refDist = append(refDist, x)
			}
		}
	}

	// Missing locale candidates: entities with confidence >= 0.5 but missing >=1 priority locale.
	missing := []missingEntity{}
	if mRows, mErr := s.pool.Query(ctx, `
SELECT id, canonical_ko, entity_type::text, confidence,
       canonical_en, canonical_ja, canonical_vi, canonical_id,
       canonical_es, canonical_pt_br, canonical_zh_hant
FROM kwave_entities
WHERE confidence >= 0.5
  AND (canonical_en IS NULL OR canonical_en = ''
    OR canonical_ja IS NULL OR canonical_ja = ''
    OR canonical_vi IS NULL OR canonical_vi = ''
    OR canonical_id IS NULL OR canonical_id = ''
    OR canonical_es IS NULL OR canonical_es = ''
    OR canonical_pt_br IS NULL OR canonical_pt_br = ''
    OR canonical_zh_hant IS NULL OR canonical_zh_hant = '')
ORDER BY confidence DESC, updated_at DESC LIMIT 50`); mErr == nil {
		defer mRows.Close()
		for mRows.Next() {
			var x missingEntity
			var en, ja, vi, id, es, pt, zh *string
			if err := mRows.Scan(&x.ID, &x.Ko, &x.EntityType, &x.Confidence,
				&en, &ja, &vi, &id, &es, &pt, &zh); err != nil {
				continue
			}
			known := []string{}
			locs := []struct {
				Code string
				V    *string
			}{{"en", en}, {"ja", ja}, {"vi", vi}, {"id", id}, {"es", es}, {"pt-br", pt}, {"zh-hant", zh}}
			for _, l := range locs {
				if l.V != nil && *l.V != "" {
					known = append(known, l.Code+":"+*l.V)
				} else {
					x.Missing = append(x.Missing, l.Code)
				}
			}
			x.Known = strings.Join(known, "  ")
			missing = append(missing, x)
		}
	}

	s.render(w, r, "entity_sources.html", map[string]any{
		"title":         "다국어 DB 소스",
		"coverages":     coverages,
		"totalEntities": totalEntities,
		"wlSummaries":   wlSummaries,
		"stats":         stats,
		"refDist":       refDist,
		"missing":       missing,
		"page":          "/admin/entities/sources",
	})
}

// --- entity conflicts ---------------------------------------------------

type dupKoRow struct {
	CanonicalKo string
	Count       int
}

type aliasConflict struct {
	Locale, Alias string
	N             int
	Canonicals    []string
}

type disambRow struct {
	ID         uuid.UUID
	Ko         string
	EntityType string
	Confidence float64
	ErrorText  string
}

func (s *Server) entityConflicts(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// canonical_ko duplicates (rare — UNIQUE constraint exists, but watch via group).
	dupKo := []dupKoRow{}
	if rows, err := s.pool.Query(ctx, `
SELECT canonical_ko, COUNT(*) FROM kwave_entities
GROUP BY canonical_ko HAVING COUNT(*) > 1 ORDER BY 2 DESC LIMIT 100`); err == nil {
		defer rows.Close()
		for rows.Next() {
			var x dupKoRow
			if err := rows.Scan(&x.CanonicalKo, &x.Count); err == nil {
				dupKo = append(dupKo, x)
			}
		}
	}

	// Alias conflicts (same alias maps to multiple entities, per locale).
	aliasConflicts := []aliasConflict{}
	if rows, err := s.pool.Query(ctx, `
WITH all_aliases AS (
  SELECT id, canonical_ko, 'ko' AS locale, unnest(aliases_ko) AS alias FROM kwave_entities
  UNION ALL SELECT id, canonical_ko, 'en', unnest(aliases_en) FROM kwave_entities
  UNION ALL SELECT id, canonical_ko, 'ja', unnest(aliases_ja) FROM kwave_entities
  UNION ALL SELECT id, canonical_ko, 'vi', unnest(aliases_vi) FROM kwave_entities
  UNION ALL SELECT id, canonical_ko, 'zh-hant', unnest(aliases_zh_hant) FROM kwave_entities
)
SELECT locale, alias, COUNT(DISTINCT id) AS n, array_agg(DISTINCT canonical_ko) AS canonicals
FROM all_aliases WHERE alias <> ''
GROUP BY locale, alias HAVING COUNT(DISTINCT id) > 1
ORDER BY n DESC, alias LIMIT 200`); err == nil {
		defer rows.Close()
		for rows.Next() {
			var x aliasConflict
			if err := rows.Scan(&x.Locale, &x.Alias, &x.N, &x.Canonicals); err == nil {
				aliasConflicts = append(aliasConflicts, x)
			}
		}
	}

	// Disambiguation-fail: entities with recent attempt status='disambiguation-fail' or 'error'.
	disamb := []disambRow{}
	if rows, err := s.pool.Query(ctx, `
SELECT DISTINCT ON (e.id) e.id, e.canonical_ko, e.entity_type::text, e.confidence, COALESCE(a.error_text,'')
FROM kwave_entities e
JOIN kwave_entity_resolution_attempts a ON a.entity_id = e.id
WHERE a.status IN ('disambiguation-fail','error','conflict')
  AND a.attempted_at > now() - interval '30 days'
ORDER BY e.id, a.attempted_at DESC LIMIT 100`); err == nil {
		defer rows.Close()
		for rows.Next() {
			var x disambRow
			if err := rows.Scan(&x.ID, &x.Ko, &x.EntityType, &x.Confidence, &x.ErrorText); err == nil {
				disamb = append(disamb, x)
			}
		}
	}

	s.render(w, r, "entity_conflicts.html", map[string]any{
		"title":          "충돌 / 동명이인",
		"dupKo":          dupKo,
		"aliasConflicts": aliasConflicts,
		"disamb":         disamb,
		"page":           "/admin/entities/conflicts",
	})
}

// --- entity review (WF-4: confidence < threshold) ----------------------

type reviewRow struct {
	ID             uuid.UUID
	Ko             string
	Type           string
	Status         string
	En, Ja, Vi     localeMark
	Confidence     float64
	OperatorLocked bool
	UpdatedAt      time.Time
}

func (s *Server) entityReview(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	p := newPage(r, "/admin/entities/review")
	threshold := r.URL.Query().Get("threshold")
	cutoff := 0.7
	if threshold != "" {
		if v, err := strconv.ParseFloat(threshold, 64); err == nil && v > 0 && v <= 1 {
			cutoff = v
		}
		p.Extras["threshold"] = threshold
	}

	// 상단 카운트 (3개).
	var total, veryLow, locked int64
	_ = s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM kwave_entities WHERE confidence < $1 AND status='active'`, cutoff).Scan(&total)
	_ = s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM kwave_entities WHERE confidence < 0.4 AND status='active'`).Scan(&veryLow)
	_ = s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM kwave_entities WHERE operator_locked = true`).Scan(&locked)
	p.finalize(total)

	rows, err := s.pool.Query(ctx, `
SELECT id, entity_type::text, canonical_ko, status,
       COALESCE(canonical_en,''),    COALESCE(canonical_en_source,''),
       COALESCE(canonical_ja,''),    COALESCE(canonical_ja_source,''),
       COALESCE(canonical_vi,''),    COALESCE(canonical_vi_source,''),
       confidence, operator_locked, updated_at
FROM kwave_entities WHERE confidence < $1 AND status='active'
ORDER BY confidence ASC, updated_at DESC
LIMIT $2 OFFSET $3`, cutoff, p.Limit, p.Offset)
	if err != nil {
		s.renderError(w, r, "entity review", err)
		return
	}
	defer rows.Close()

	items := []reviewRow{}
	for rows.Next() {
		var x reviewRow
		var enV, enS, jaV, jaS, viV, viS string
		if err := rows.Scan(&x.ID, &x.Type, &x.Ko, &x.Status,
			&enV, &enS, &jaV, &jaS, &viV, &viS,
			&x.Confidence, &x.OperatorLocked, &x.UpdatedAt); err == nil {
			x.En = mark(enV, enS)
			x.Ja = mark(jaV, jaS)
			x.Vi = mark(viV, viS)
			items = append(items, x)
		}
	}

	s.render(w, r, "entity_review.html", map[string]any{
		"title":     "품질 검토 (confidence < " + strconv.FormatFloat(cutoff, 'f', 2, 64) + ")",
		"items":     items,
		"p":         p,
		"threshold": cutoff,
		"veryLow":   veryLow,
		"locked":    locked,
		"flash":     r.URL.Query().Get("flash"),
		"page":      "/admin/entities/review",
	})
}

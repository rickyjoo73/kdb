package kdbadmin

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/rickyjoo73/kdb/internal/kdb/agents/gatekeeper"
)

type localeSpelling struct {
	Label string // 표시 라벨 (KO/EN/JA/ZH/ZH-Hant/VI/ES/ID/PT-BR)
	Code  string // locale 코드 (ko/en/...)
	Value string // 표기 (빈값 가능 — detail 에선 누락을 보이기 위해 '—' 표시)
}

type personRow struct {
	ID             uuid.UUID
	NameKo         string
	NameEn         string
	NameJa         string
	NameVi         string
	NameEs         string
	NameId         string
	NamePtBr       string
	NameZh         string
	NameZhHant     string
	Locales        []localeSpelling // 표시용: 비어있지 않은 다국어 표기(KO 제외)
	Role           string
	SecondaryRoles []string
	Aliases        []string
	Agency         string
	Groups         []string
	BirthYear      *int
	Gender         string
	NotableWorks   []string
	CategoryHint   string
	Confidence     float64
	OperatorLocked bool
	SourceURLs     []string
	LastVerifiedAt time.Time
}

// personLocales — personRow 의 9개 언어 표기를 정해진 순서로 반환. onlyFilled=true 면
// 비어있지 않은 것만(리스트용), false 면 KO 포함 전체(detail 용 — 누락 가시화).
func personLocales(x personRow, onlyFilled bool) []localeSpelling {
	all := []localeSpelling{
		{"KO", "ko", x.NameKo}, {"EN", "en", x.NameEn}, {"JA", "ja", x.NameJa},
		{"ZH", "zh", x.NameZh}, {"ZH-Hant", "zh_hant", x.NameZhHant},
		{"VI", "vi", x.NameVi}, {"ES", "es", x.NameEs}, {"ID", "id", x.NameId},
		{"PT-BR", "pt_br", x.NamePtBr},
	}
	out := make([]localeSpelling, 0, len(all))
	for _, p := range all {
		if onlyFilled {
			if p.Code == "ko" || strings.TrimSpace(p.Value) == "" {
				continue // 리스트: KO 는 별도 컬럼, 빈값 제외
			}
		}
		out = append(out, p)
	}
	return out
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
		// 고유명사DB 와 같은 이유 — 날 것 부분일치는 띄어쓰기 차이를 못 넘는다.
		nph := nextArg(gatekeeper.NormalizedKey(p.Q))
		conds = append(conds, "(name_ko ILIKE '%'||"+ph+"||'%' OR name_en ILIKE '%'||"+ph+"||'%' OR name_ja ILIKE '%'||"+ph+"||'%' OR "+ph+" = ANY(aliases) OR "+
			normSearchSQL(nph, []string{"name_ko", "name_en"}, "aliases")+")")
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
       COALESCE(name_vi,''), COALESCE(name_es,''), COALESCE(name_id,''),
       COALESCE(name_pt_br,''), COALESCE(name_zh,''), COALESCE(name_zh_hant,''),
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
			&x.NameVi, &x.NameEs, &x.NameId, &x.NamePtBr, &x.NameZh, &x.NameZhHant,
			&x.Role, &x.SecondaryRoles,
			&x.Agency, &x.Groups, &x.BirthYear, &x.Gender, &x.NotableWorks,
			&x.CategoryHint, &x.Confidence, &x.OperatorLocked, &x.LastVerifiedAt); err == nil {
			x.Locales = personLocales(x, true) // 리스트: 채워진 다국어만
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

// personDetail — kwave_persons 1건의 세부정보(9개 언어 표기 전체 + 역할/소속/그룹/
// 대표작/별칭/출처)를 보여준다. 리스트의 이름 링크에서 진입. 누락 locale 도 '—' 로
// 표시해 운영자가 어느 언어가 비었는지 한눈에 확인할 수 있게 한다.
func (s *Server) personDetail(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	var x personRow
	err = s.pool.QueryRow(ctx, `
SELECT id, name_ko, COALESCE(name_en,''), COALESCE(name_ja,''),
       COALESCE(name_vi,''), COALESCE(name_es,''), COALESCE(name_id,''),
       COALESCE(name_pt_br,''), COALESCE(name_zh,''), COALESCE(name_zh_hant,''),
       primary_role::text, secondary_roles::text[], aliases,
       COALESCE(agency,''), groups, birth_year, COALESCE(gender,''), notable_works,
       COALESCE(category_hint,''), confidence, operator_locked, source_urls, last_verified_at
FROM kwave_persons WHERE id = $1`, id).Scan(
		&x.ID, &x.NameKo, &x.NameEn, &x.NameJa,
		&x.NameVi, &x.NameEs, &x.NameId, &x.NamePtBr, &x.NameZh, &x.NameZhHant,
		&x.Role, &x.SecondaryRoles, &x.Aliases,
		&x.Agency, &x.Groups, &x.BirthYear, &x.Gender, &x.NotableWorks,
		&x.CategoryHint, &x.Confidence, &x.OperatorLocked, &x.SourceURLs, &x.LastVerifiedAt)
	if err != nil {
		s.renderError(w, r, "person detail", err)
		return
	}
	x.Locales = personLocales(x, false) // detail: KO 포함 9개 전체(누락 가시화)

	// 같은 canonical_ko 의 엔티티(다국어 표기 SSOT)로 교차 링크 — 있으면.
	var entityID string
	_ = s.pool.QueryRow(ctx, `SELECT id::text FROM kwave_entities WHERE canonical_ko=$1 AND status='active' ORDER BY confidence DESC LIMIT 1`, x.NameKo).Scan(&entityID)

	p := newPage(r, "/admin/persons")
	s.render(w, r, "person_detail.html", map[string]any{
		"title":    x.NameKo,
		"x":        x,
		"entityID": entityID,
		"p":        p,
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

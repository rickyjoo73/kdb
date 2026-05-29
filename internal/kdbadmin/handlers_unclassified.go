package kdbadmin

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

// --- 미분류 entity 페이지 (status='active' AND entity_type='unknown') ----

type unclassifiedRow struct {
	ID            uuid.UUID
	Ko            string
	Confidence    float64
	SourceDomains []string
	En, Ja, Vi    localeMark
	Suggested     string
	UpdatedAt     time.Time
}

func (s *Server) entitiesUnclassified(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	p := newPage(r, "/admin/entities/unclassified")

	var total int64
	_ = s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM kwave_entities WHERE status='active' AND entity_type='unknown'`).Scan(&total)
	p.finalize(total)

	rows, err := s.pool.Query(ctx, `
SELECT id, canonical_ko, confidence, source_domains,
       COALESCE(canonical_en,''),    COALESCE(canonical_en_source,''),
       COALESCE(canonical_ja,''),    COALESCE(canonical_ja_source,''),
       COALESCE(canonical_vi,''),    COALESCE(canonical_vi_source,''),
       updated_at
FROM kwave_entities
WHERE status='active' AND entity_type='unknown'
ORDER BY confidence DESC, updated_at DESC
LIMIT $1 OFFSET $2`, p.Limit, p.Offset)
	if err != nil {
		s.renderError(w, r, "unclassified list", err)
		return
	}
	defer rows.Close()

	items := []unclassifiedRow{}
	for rows.Next() {
		var x unclassifiedRow
		var enV, enS, jaV, jaS, viV, viS string
		if err := rows.Scan(&x.ID, &x.Ko, &x.Confidence, &x.SourceDomains,
			&enV, &enS, &jaV, &jaS, &viV, &viS, &x.UpdatedAt); err != nil {
			continue
		}
		x.En = mark(enV, enS)
		x.Ja = mark(jaV, jaS)
		x.Vi = mark(viV, viS)
		x.Suggested = suggestEntityType(x.Ko)
		items = append(items, x)
	}

	s.render(w, r, "entities_unclassified.html", map[string]any{
		"title":       "미분류 entity (type=unknown)",
		"items":       items,
		"p":           p,
		"entityTypes": entityTypes,
		"flash":       r.URL.Query().Get("flash"),
		"page":        "/admin/entities/unclassified",
	})
}

// entityClassify — entity_type 결정 + (person 시) persons / entity_person_details sync.
// inboxPromote 와 동일한 sync 로직 (별도 헬퍼로 추출).
func (s *Server) entityClassify(w http.ResponseWriter, r *http.Request) {
	id, ok := parseEntityID(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		s.redirectBack(w, r, "bad form")
		return
	}
	chosen := strings.TrimSpace(r.FormValue("entity_type"))
	if chosen == "" || chosen == "unknown" {
		s.redirectBack(w, r, "분류 차단: entity_type 을 선택하라 (unknown 금지)")
		return
	}
	if !isValidEntityType(chosen) {
		s.redirectBack(w, r, "알 수 없는 entity_type: "+chosen)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var ko string
	if err := s.pool.QueryRow(ctx, `
UPDATE kwave_entities SET entity_type = $2::kwave_entity_type, updated_at = now()
WHERE id = $1 AND operator_locked = false
RETURNING canonical_ko`, id, chosen).Scan(&ko); err != nil {
		s.redirectBack(w, r, "분류 실패: entity 없음 또는 잠김")
		return
	}

	note := ""
	if chosen == "person" {
		_, _ = s.pool.Exec(ctx, `
INSERT INTO kwave_persons (name_ko, primary_role, confidence, last_verified_at, created_at)
VALUES ($1, 'other'::person_role, 0.500, now(), now())
ON CONFLICT (name_ko) DO NOTHING`, ko)
		_, _ = s.pool.Exec(ctx, `
INSERT INTO kwave_entity_person_details (entity_id, primary_role)
VALUES ($1, 'other'::person_role)
ON CONFLICT (entity_id) DO NOTHING`, id)
		note = " · persons sync ✓"
	}
	s.redirectBack(w, r, "분류 → "+chosen+": "+ko+note)
}

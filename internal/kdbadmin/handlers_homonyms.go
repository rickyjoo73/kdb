package kdbadmin

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// --- 동명이인 (homonym) 그룹 리뷰 ---------------------------------------
//
// 같은 canonical_ko 를 공유하는 서로 다른 실존 인물(예: 윤성호 감독 vs 가수)을
// agency/works/role/birth 와 함께 나란히 보여주고, 운영자가 disambig 라벨을
// 지정하거나 reject(잘못된 분리)할 수 있게 한다. 자동 merge/split 은 하지 않음.

// homonymMember — 한 그룹 내 entity 1건 (구분 신호 포함).
type homonymMember struct {
	ID            uuid.UUID
	EntityType    string
	Disambig      string
	PrimaryRole   string
	Agency        string
	BirthYear     int
	NotableWorks  []string
	Confidence    float64
	Status        string
	NeedsDisambig bool
	Global        bool // foreign-locale 표기 보유 여부
	UpdatedAt     time.Time
}

// homonymGroup — 같은 canonical_ko 를 공유하는 entity 묶음.
type homonymGroup struct {
	Ko      string
	Count   int
	Members []homonymMember
}

func (s *Server) entityHomonyms(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// canonical_ko 가 2건 이상인 그룹 (= 진짜 동명이인 공존). UNIQUE 제거 후 발생.
	rows, err := s.pool.Query(ctx, `
SELECT e.canonical_ko, e.id, e.entity_type::text, COALESCE(e.disambig,''),
       COALESCE(d.primary_role::text,''), COALESCE(d.agency,''),
       COALESCE(d.birth_year,0), COALESCE(d.notable_works,'{}'::text[]),
       e.confidence, e.status, e.needs_disambig, e.updated_at,
       (COALESCE(e.canonical_en,'')<>'' OR COALESCE(e.canonical_ja,'')<>''
        OR COALESCE(e.canonical_vi,'')<>'' OR COALESCE(e.canonical_id,'')<>''
        OR COALESCE(e.canonical_es,'')<>'' OR COALESCE(e.canonical_pt_br,'')<>''
        OR COALESCE(e.canonical_zh_hant,'')<>'') AS global
  FROM kwave_entities e
  LEFT JOIN kwave_entity_person_details d ON d.entity_id = e.id
 WHERE e.canonical_ko IN (
   SELECT canonical_ko FROM kwave_entities
   WHERE status <> 'rejected'
   GROUP BY canonical_ko HAVING COUNT(*) > 1
 )
 AND e.status <> 'rejected'
 ORDER BY e.canonical_ko, e.confidence DESC`)
	if err != nil {
		s.renderError(w, r, "homonyms list", err)
		return
	}
	defer rows.Close()

	groupMap := map[string]*homonymGroup{}
	order := []string{}
	for rows.Next() {
		var ko string
		var m homonymMember
		if err := rows.Scan(&ko, &m.ID, &m.EntityType, &m.Disambig, &m.PrimaryRole,
			&m.Agency, &m.BirthYear, &m.NotableWorks, &m.Confidence, &m.Status,
			&m.NeedsDisambig, &m.UpdatedAt, &m.Global); err != nil {
			continue
		}
		g, ok := groupMap[ko]
		if !ok {
			g = &homonymGroup{Ko: ko}
			groupMap[ko] = g
			order = append(order, ko)
		}
		g.Members = append(g.Members, m)
		g.Count++
	}
	groups := make([]homonymGroup, 0, len(order))
	for _, ko := range order {
		groups = append(groups, *groupMap[ko])
	}

	// needs_disambig 표시된 단독(아직 그룹화 안 된) entity 도 별도로 노출.
	flagged := []homonymMember{}
	var koFlag []string
	if fRows, fErr := s.pool.Query(ctx, `
SELECT e.id, e.canonical_ko, e.entity_type::text, COALESCE(e.disambig,''),
       COALESCE(d.primary_role::text,''), COALESCE(d.agency,''),
       COALESCE(d.birth_year,0), COALESCE(d.notable_works,'{}'::text[]),
       e.confidence, e.status, e.updated_at
  FROM kwave_entities e
  LEFT JOIN kwave_entity_person_details d ON d.entity_id = e.id
 WHERE e.needs_disambig = true AND e.status <> 'rejected'
 ORDER BY e.updated_at DESC LIMIT 100`); fErr == nil {
		defer fRows.Close()
		for fRows.Next() {
			var m homonymMember
			var ko string
			if err := fRows.Scan(&m.ID, &ko, &m.EntityType, &m.Disambig, &m.PrimaryRole,
				&m.Agency, &m.BirthYear, &m.NotableWorks, &m.Confidence, &m.Status,
				&m.UpdatedAt); err == nil {
				m.NeedsDisambig = true
				flagged = append(flagged, m)
				koFlag = append(koFlag, ko)
			}
		}
	}

	s.render(w, r, "entity_homonyms.html", map[string]any{
		"title":   "동명이인 그룹",
		"groups":  groups,
		"flagged": flagged,
		"koFlag":  koFlag,
		"flash":   r.URL.Query().Get("flash"),
		"page":    "/admin/entities/homonyms",
	})
}

// entitySetDisambig — 운영자가 entity 에 disambig 라벨 지정 + needs_disambig 해제.
func (s *Server) entitySetDisambig(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	disambig := strings.TrimSpace(r.FormValue("disambig"))
	clearFlag := r.FormValue("clear_flag") != ""
	if clearFlag {
		_, err = s.pool.Exec(ctx, `
UPDATE kwave_entities SET disambig = NULLIF($2,''), needs_disambig = false, updated_at = now()
 WHERE id = $1`, id, disambig)
	} else {
		_, err = s.pool.Exec(ctx, `
UPDATE kwave_entities SET disambig = NULLIF($2,''), updated_at = now()
 WHERE id = $1`, id, disambig)
	}
	flash := "disambig+saved"
	if err != nil {
		flash = "error"
	}
	http.Redirect(w, r, "/admin/entities/homonyms?flash="+flash, http.StatusSeeOther)
}

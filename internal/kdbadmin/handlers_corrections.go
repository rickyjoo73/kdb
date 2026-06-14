package kdbadmin

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rickyjoo73/kdb/internal/kdb/corrections"
)

// --- 외부 클라이언트 교정요청(corrections) 심사 UI -----------------------
// /v1/corrections 로 들어온 정정 신고. Wikidata 교차검증되면 제출 시 자동적용,
// 미달은 pending 으로 남아 여기서 운영자가 승인/거부. (그동안 CLI 만 있었음)

type correctionResolved struct {
	ID         int64
	Ko         string
	Locale     string
	Suggested  string
	Status     string
	Resolution string
	ResolvedAt time.Time
}

func (s *Server) sessionUser(r *http.Request) string {
	if c, err := r.Cookie(sessionCookieName); err == nil {
		if u, ok := decodeSession(s.opts.SessionSecret, c.Value); ok && u != "" {
			return u
		}
	}
	return "operator"
}

func (s *Server) correctionsList(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	items, err := corrections.ListPending(ctx, s.pool, 200)
	if err != nil {
		s.renderError(w, r, "corrections list", err)
		return
	}

	resolved := []correctionResolved{}
	if rows, e := s.pool.Query(ctx, `
SELECT c.id, COALESCE(e.canonical_ko,''), c.locale, c.suggested_value, c.status,
       COALESCE(c.resolution,''), COALESCE(c.resolved_at, c.created_at)
  FROM kwave_kdb_corrections c LEFT JOIN kwave_entities e ON e.id=c.entity_id
 WHERE c.status NOT IN ('pending','proposed')
 ORDER BY c.resolved_at DESC NULLS LAST, c.id DESC LIMIT 30`); e == nil {
		defer rows.Close()
		for rows.Next() {
			var x correctionResolved
			if rows.Scan(&x.ID, &x.Ko, &x.Locale, &x.Suggested, &x.Status, &x.Resolution, &x.ResolvedAt) == nil {
				resolved = append(resolved, x)
			}
		}
	}

	s.render(w, r, "corrections.html", map[string]any{
		"title":    "교정요청 심사",
		"items":    items,
		"resolved": resolved,
		"flash":    r.URL.Query().Get("flash"),
		"page":     "/admin/corrections",
	})
}

func (s *Server) correctionApprove(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		s.redirectBack(w, r, "잘못된 id")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	if err := corrections.Approve(ctx, s.pool, id, s.sessionUser(r)); err != nil {
		s.redirectBack(w, r, "승인 실패: "+err.Error())
		return
	}
	s.redirectBack(w, r, "교정 승인·적용 #"+strconv.FormatInt(id, 10))
}

func (s *Server) correctionReject(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		s.redirectBack(w, r, "잘못된 id")
		return
	}
	_ = r.ParseForm()
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	if err := corrections.Reject(ctx, s.pool, id, s.sessionUser(r), r.FormValue("why")); err != nil {
		s.redirectBack(w, r, "거부 실패: "+err.Error())
		return
	}
	s.redirectBack(w, r, "교정 거부 #"+strconv.FormatInt(id, 10))
}

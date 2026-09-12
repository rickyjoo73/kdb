package kdbadmin

import (
	"errors"
	"net/http"
	"os"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rickyjoo73/kdb/internal/kentity"
)

func (s *Server) commonMappings(w http.ResponseWriter, r *http.Request) {
	if os.Getenv("KDB_COMMON_ENTITY_ENABLED") != "1" {
		http.Error(w, "기능 활성화 전입니다.", 503)
		return
	}
	state := r.URL.Query().Get("status")
	if state == "" {
		state = "review"
	}
	items, err := (&kentity.Store{Pool: s.pool}).Mappings(r.Context(), state, 100)
	if errors.Is(err, kentity.ErrInvalid) {
		http.Error(w, "지원하지 않는 상태입니다.", 400)
		return
	}
	s.render(w, r, "kentity_mappings.html", map[string]any{"title": "기존 인물 ID 연결 검수", "items": items, "status": state, "loadError": err != nil})
}
func (s *Server) commonMapping(w http.ResponseWriter, r *http.Request) {
	if os.Getenv("KDB_COMMON_ENTITY_ENABLED") != "1" {
		http.Error(w, "기능 활성화 전입니다.", 503)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "sourceID"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	m, err := (&kentity.Store{Pool: s.pool}).Mapping(r.Context(), id)
	if errors.Is(err, kentity.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	staff, _ := r.Context().Value(readinessStaffKey{}).(readinessStaff)
	canDecide := err == nil && !m.Source.Locked && (m.Status == "review" || m.Status == "conflict") && (staff.Role == "operator" || staff.Role == "admin")
	s.render(w, r, "kentity_mappings.html", map[string]any{"title": "동명이인 연결 비교", "detail": true, "mapping": m, "canDecide": canDecide, "loadError": err != nil})
}
func (s *Server) commonMappingDecide(w http.ResponseWriter, r *http.Request) {
	staff, ok := r.Context().Value(readinessStaffKey{}).(readinessStaff)
	if !ok || (staff.Role != "operator" && staff.Role != "admin") {
		http.Error(w, "운영자 권한이 필요합니다.", 403)
		return
	}
	if os.Getenv("KDB_COMMON_ENTITY_ENABLED") != "1" {
		http.Error(w, "기능 활성화 전입니다.", 503)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "sourceID"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	in := kentity.MappingDecision{SourceID: id, Fingerprint: r.FormValue("fingerprint"), EntityFingerprint: r.FormValue("entity_fingerprint"), Decision: r.FormValue("decision"), Reason: r.FormValue("reason"), EvidenceURL: r.FormValue("evidence_url"), IdentityFacts: r.FormValue("identity_facts"), Attested: r.FormValue("attested") == "yes"}
	in.Revision, _ = strconv.ParseInt(r.FormValue("revision"), 10, 64)
	in.EntityRevision, _ = strconv.ParseInt(r.FormValue("entity_revision"), 10, 64)
	in.EntityID, _ = uuid.Parse(r.FormValue("entity_id"))
	err = (&kentity.Store{Pool: s.pool}).DecideMapping(r.Context(), staff.Email, in)
	if errors.Is(err, kentity.ErrInvalid) {
		http.Error(w, "사유·근거 URL·동일인 확인 사실을 입력하고 확인 항목을 체크하세요.", 400)
		return
	}
	var dbErr *pgconn.PgError
	if errors.Is(err, kentity.ErrProtected) || (errors.As(err, &dbErr) && (dbErr.Code == "40001" || dbErr.Code == "40P01" || dbErr.Code == "55P03")) {
		http.Error(w, "대상 정보 또는 잠금이 변경되었습니다. 상세 화면을 새로 열어 다시 비교하세요.", 409)
		return
	}
	if errors.Is(err, kentity.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "연결 결정을 저장하지 못했습니다. 변경은 취소되었습니다.", 503)
		return
	}
	http.Redirect(w, r, "/admin/kentity/mappings/"+id.String(), 303)
}

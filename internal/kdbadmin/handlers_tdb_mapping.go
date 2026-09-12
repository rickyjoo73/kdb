package kdbadmin

import (
	"errors"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rickyjoo73/kdb/internal/kentity"
	"net/http"
	"os"
	"strconv"
)

func tdbMappingEnabled() bool {
	return os.Getenv("KDB_COMMON_ENTITY_ENABLED") == "1" && os.Getenv("KDB_TDB_SHADOW_ENABLED") == "1" && os.Getenv("KDB_TDB_MAPPING_ENABLED") == "1"
}
func (s *Server) tdbMappingDetail(w http.ResponseWriter, r *http.Request) {
	if !tdbMappingEnabled() {
		http.Error(w, "TDB 공통 연결 기능 활성화 전입니다.", 503)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "shadowID"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	m, err := (&kentity.Store{Pool: s.pool}).TDBMapping(r.Context(), id)
	if errors.Is(err, kentity.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	staff, _ := r.Context().Value(readinessStaffKey{}).(readinessStaff)
	d := map[string]any{"title": "TDB 공통 UUID 연결", "mapping": m, "loadError": err != nil, "canManage": staff.Role == "operator" || staff.Role == "admin"}
	s.render(w, r, "tdb_mapping.html", d)
}
func (s *Server) tdbAction(w http.ResponseWriter, r *http.Request) (uuid.UUID, readinessStaff, bool) {
	staff, ok := r.Context().Value(readinessStaffKey{}).(readinessStaff)
	if !ok || (staff.Role != "operator" && staff.Role != "admin") {
		http.Error(w, "운영자 권한이 필요합니다.", 403)
		return uuid.Nil, staff, false
	}
	if !tdbMappingEnabled() {
		http.Error(w, "TDB 공통 연결 기능 활성화 전입니다.", 503)
		return uuid.Nil, staff, false
	}
	id, err := uuid.Parse(chi.URLParam(r, "shadowID"))
	if err != nil {
		http.NotFound(w, r)
		return uuid.Nil, staff, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	if err = r.ParseForm(); err != nil {
		http.Error(w, "입력 형식 오류입니다.", 400)
		return uuid.Nil, staff, false
	}
	return id, staff, true
}
func tdbActionResult(w http.ResponseWriter, r *http.Request, id uuid.UUID, err error) {
	switch {
	case errors.Is(err, kentity.ErrInvalid):
		http.Error(w, "대상·분야·근거·확인 항목을 확인하세요.", 400)
	case errors.Is(err, kentity.ErrProtected), errors.Is(err, kentity.ErrConflict):
		http.Error(w, "원본 관측·잠금·버전·기존 ID 연결이 변경되었거나 재확인 한도에 도달했습니다. 상세 화면을 다시 확인하세요.", 409)
	case errors.Is(err, kentity.ErrNotFound):
		http.NotFound(w, r)
	case err != nil:
		http.Error(w, "TDB 연결 작업을 저장하지 못했습니다.", 503)
	default:
		http.Redirect(w, r, "/admin/kentity/tdb/"+id.String(), 303)
	}
}
func (s *Server) tdbCandidateRegister(w http.ResponseWriter, r *http.Request) {
	id, staff, ok := s.tdbAction(w, r)
	if !ok {
		return
	}
	generation, _ := strconv.ParseInt(r.FormValue("generation"), 10, 64)
	_, err := (&kentity.Store{Pool: s.pool, AutoResearch: os.Getenv("KDB_ENTITY_RESOLVER_ENABLED") == "1"}).RegisterTDBCandidate(r.Context(), staff.Email, kentity.TDBRegistration{ShadowID: id, Generation: generation, Fingerprint: r.FormValue("fingerprint"), Reason: r.FormValue("reason"), Type: r.FormValue("type"), Domains: r.Form["domains"]})
	tdbActionResult(w, r, id, err)
}
func (s *Server) tdbMappingDecide(w http.ResponseWriter, r *http.Request) {
	id, staff, ok := s.tdbAction(w, r)
	if !ok {
		return
	}
	generation, _ := strconv.ParseInt(r.FormValue("generation"), 10, 64)
	rev, _ := strconv.ParseInt(r.FormValue("revision"), 10, 64)
	entityRev, _ := strconv.ParseInt(r.FormValue("entity_revision"), 10, 64)
	target, _ := uuid.Parse(r.FormValue("entity_id"))
	err := (&kentity.Store{Pool: s.pool}).DecideTDBMapping(r.Context(), staff.Email, kentity.TDBMappingDecision{ShadowID: id, EntityID: target, Generation: generation, Revision: rev, EntityRevision: entityRev, Fingerprint: r.FormValue("fingerprint"), Decision: r.FormValue("decision"), Reason: r.FormValue("reason"), IdentityFacts: r.FormValue("identity_facts"), EvidenceURL: r.FormValue("evidence_url"), Attested: r.FormValue("attested") == "yes"})
	tdbActionResult(w, r, id, err)
}
func (s *Server) tdbSourceRecheck(w http.ResponseWriter, r *http.Request) {
	id, staff, ok := s.tdbAction(w, r)
	if !ok {
		return
	}
	generation, _ := strconv.ParseInt(r.FormValue("generation"), 10, 64)
	err := (&kentity.Store{Pool: s.pool}).RecheckTDB(r.Context(), staff.Email, id, generation, r.FormValue("reason"))
	tdbActionResult(w, r, id, err)
}

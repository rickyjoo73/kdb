package kdbadmin

import (
	"errors"
	"net/http"
	"os"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rickyjoo73/kdb/internal/kentity"
)

func (s *Server) commonEntityList(w http.ResponseWriter, r *http.Request) {
	if os.Getenv("KDB_COMMON_ENTITY_ENABLED") != "1" {
		http.Error(w, "공통 Entity 기능 활성화 전입니다.", 503)
		return
	}
	data := map[string]any{"title": "공통 Entity 관리", "requestKey": uuid.NewString(), "q": r.URL.Query().Get("q"), "domain": r.URL.Query().Get("domain"), "type": r.URL.Query().Get("type")}
	staff, _ := r.Context().Value(readinessStaffKey{}).(readinessStaff)
	data["canCreate"] = staff.Role == "admin" || staff.Role == "operator"
	items, err := (&kentity.Store{Pool: s.pool}).Search(r.Context(), r.URL.Query().Get("q"), r.URL.Query().Get("type"), r.URL.Query().Get("domain"), 100)
	if err != nil {
		data["loadError"] = true
	} else {
		data["items"] = items
	}
	var review int
	if err = s.pool.QueryRow(r.Context(), `SELECT count(*) FROM kentity_crosswalks WHERE status IN ('review','conflict')`).Scan(&review); err != nil {
		data["loadError"] = true
	} else {
		data["mappingReview"] = review
	}
	s.render(w, r, "kentity.html", data)
}
func (s *Server) commonEntityDetail(w http.ResponseWriter, r *http.Request) {
	if os.Getenv("KDB_COMMON_ENTITY_ENABLED") != "1" {
		http.Error(w, "공통 Entity 기능 활성화 전입니다.", 503)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	e, err := (&kentity.Store{Pool: s.pool}).Get(r.Context(), id)
	if errors.Is(err, kentity.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	data := map[string]any{"title": "공통 Entity 상세", "entity": e, "detail": true, "loadError": err != nil}
	if err == nil && e.WriteOwner == "native" && os.Getenv("KDB_ENTITY_OWNERSHIP_ENABLED") == "1" {
		staff, _ := r.Context().Value(readinessStaffKey{}).(readinessStaff)
		data["canManageCommonLock"] = staff.Role == "operator" || staff.Role == "admin"
	}
	if err == nil && e.WriteOwner == "kdb" && e.Status == "rejected" && os.Getenv("KDB_ENTITY_OWNERSHIP_ENABLED") == "1" {
		data["ownershipEnabled"] = true
		legacy, legacyErr := (&kentity.Store{Pool: s.pool}).LegacyOwnership(r.Context(), e.ID)
		data["legacyScope"], data["ownershipError"] = legacy, legacyErr != nil
		staff, _ := r.Context().Value(readinessStaffKey{}).(readinessStaff)
		data["canAdoptLegacy"] = legacyErr == nil && !legacy.Locked && !e.Locked && (staff.Role == "operator" || staff.Role == "admin")
	}
	if err == nil && e.WriteOwner == "native" && os.Getenv("KDB_ENTITY_RESOLVER_ENABLED") == "1" {
		data["resolverEnabled"] = true
		resolution, researchErr := (&kentity.Store{Pool: s.pool}).Resolution(r.Context(), e.ID)
		data["resolution"], data["researchError"] = resolution, researchErr != nil
		staff, _ := r.Context().Value(readinessStaffKey{}).(readinessStaff)
		data["researchStale"] = researchErr == nil && resolution != nil && resolution.Revision != e.Revision
		data["canResearch"] = researchErr == nil && (resolution == nil || resolution.Revision != e.Revision) && e.Status == "candidate" && !e.Locked && (staff.Role == "operator" || staff.Role == "admin")
		data["canCancelResearch"] = researchErr == nil && resolution != nil && (resolution.State == "pending" || resolution.State == "running" || resolution.State == "failed") && (staff.Role == "operator" || staff.Role == "admin")
		data["canApproveResearch"] = researchErr == nil && resolution != nil && resolution.Revision == e.Revision && resolution.State == "review" && e.Status == "candidate" && !e.Locked && (staff.Role == "operator" || staff.Role == "admin")
	}
	s.render(w, r, "kentity.html", data)
}

func (s *Server) commonEntityResearchCancel(w http.ResponseWriter, r *http.Request) {
	staff, ok := r.Context().Value(readinessStaffKey{}).(readinessStaff)
	if !ok || (staff.Role != "operator" && staff.Role != "admin") {
		http.Error(w, "운영자 권한이 필요합니다.", 403)
		return
	}
	if os.Getenv("KDB_COMMON_ENTITY_ENABLED") != "1" || os.Getenv("KDB_ENTITY_RESOLVER_ENABLED") != "1" {
		http.Error(w, "자동 조사 활성화 전입니다.", 503)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	jobID, _ := uuid.Parse(r.FormValue("job_id"))
	generation, _ := strconv.ParseInt(r.FormValue("generation"), 10, 64)
	err = (&kentity.Store{Pool: s.pool}).CancelResearch(r.Context(), staff.Email, id, jobID, generation, r.FormValue("reason"))
	if errors.Is(err, kentity.ErrInvalid) {
		http.Error(w, "취소 사유와 대상 작업을 확인하세요.", 400)
		return
	}
	if errors.Is(err, kentity.ErrProtected) {
		http.Error(w, "작업 상태가 변경되었습니다. 상세를 다시 확인하세요.", 409)
		return
	}
	if err != nil {
		http.Error(w, "취소를 저장하지 못했습니다.", 503)
		return
	}
	http.Redirect(w, r, "/admin/kentity/"+id.String(), 303)
}
func (s *Server) commonEntityCreate(w http.ResponseWriter, r *http.Request) {
	staff, ok := r.Context().Value(readinessStaffKey{}).(readinessStaff)
	if !ok || (staff.Role != "admin" && staff.Role != "operator") {
		http.Error(w, "운영자 권한이 필요합니다.", 403)
		return
	}
	if os.Getenv("KDB_COMMON_ENTITY_ENABLED") != "1" {
		http.Error(w, "기능 활성화 전입니다.", 503)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "입력 형식 오류", 400)
		return
	}
	in := kentity.CandidateInput{KO: r.FormValue("ko"), Type: r.FormValue("type"), Subtype: r.FormValue("subtype"), Domains: r.Form["domains"], Reason: r.FormValue("reason")}
	e, err := (&kentity.Store{Pool: s.pool, AutoResearch: os.Getenv("KDB_ENTITY_RESOLVER_ENABLED") == "1"}).CreateCandidate(r.Context(), staff.Email, r.FormValue("request_key"), in)
	if errors.Is(err, kentity.ErrInvalid) {
		http.Error(w, "이름·유형·분야·등록 사유를 확인하세요.", 400)
		return
	}
	if errors.Is(err, kentity.ErrConflict) {
		http.Error(w, "이미 처리한 요청입니다. 목록을 새로 확인하세요.", 409)
		return
	}
	if err != nil {
		http.Error(w, "후보를 저장하지 못했습니다.", 503)
		return
	}
	http.Redirect(w, r, "/admin/kentity/"+e.ID.String(), 303)
}

func (s *Server) commonEntityResearch(w http.ResponseWriter, r *http.Request) {
	staff, ok := r.Context().Value(readinessStaffKey{}).(readinessStaff)
	if !ok || (staff.Role != "operator" && staff.Role != "admin") {
		http.Error(w, "운영자 권한이 필요합니다.", 403)
		return
	}
	if os.Getenv("KDB_COMMON_ENTITY_ENABLED") != "1" || os.Getenv("KDB_ENTITY_RESOLVER_ENABLED") != "1" {
		http.Error(w, "자동 조사 활성화 전입니다.", 503)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	revision, _ := strconv.ParseInt(r.FormValue("revision"), 10, 64)
	err = (&kentity.Store{Pool: s.pool}).RequestResearch(r.Context(), staff.Email, id, revision, r.FormValue("reason"))
	if errors.Is(err, kentity.ErrInvalid) {
		http.Error(w, "조사 사유를 확인하세요.", 400)
		return
	}
	if errors.Is(err, kentity.ErrProtected) {
		http.Error(w, "대상 상태·버전·잠금을 다시 확인하세요.", 409)
		return
	}
	if errors.Is(err, kentity.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "조사 요청을 저장하지 못했습니다.", 503)
		return
	}
	http.Redirect(w, r, "/admin/kentity/"+id.String(), 303)
}

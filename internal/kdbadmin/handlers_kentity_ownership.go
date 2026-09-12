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

func (s *Server) commonEntityAdopt(w http.ResponseWriter, r *http.Request) {
	staff, ok := r.Context().Value(readinessStaffKey{}).(readinessStaff)
	if !ok || (staff.Role != "operator" && staff.Role != "admin") {
		http.Error(w, "운영자 권한이 필요합니다.", 403)
		return
	}
	if os.Getenv("KDB_COMMON_ENTITY_ENABLED") != "1" || os.Getenv("KDB_ENTITY_OWNERSHIP_ENABLED") != "1" {
		http.Error(w, "범위 전환 활성화 전입니다.", 503)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	rev, _ := strconv.ParseInt(r.FormValue("revision"), 10, 64)
	in := kentity.OwnershipInput{ID: id, Revision: rev, Fingerprint: r.FormValue("fingerprint"), Type: r.FormValue("type"), Domains: r.Form["domains"], Reason: r.FormValue("reason"), SourceURL: r.FormValue("source_url"), Attested: r.FormValue("attested") == "yes"}
	err = (&kentity.Store{Pool: s.pool, AutoResearch: os.Getenv("KDB_ENTITY_RESOLVER_ENABLED") == "1"}).AdoptLegacy(r.Context(), staff.Email, in)
	if errors.Is(err, kentity.ErrInvalid) {
		http.Error(w, "유형·분야·근거 URL·전환 사유와 확인 항목을 확인하세요.", 400)
		return
	}
	var pgErr *pgconn.PgError
	if errors.Is(err, kentity.ErrProtected) || (errors.As(err, &pgErr) && (pgErr.Code == "40001" || pgErr.Code == "40P01" || pgErr.Code == "55P03" || pgErr.Code == "23505")) {
		http.Error(w, "원본·버전·잠금·쓰기 책임이 변경되었습니다. 상세를 다시 확인하세요.", 409)
		return
	}
	if errors.Is(err, kentity.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "범위 전환을 저장하지 못했습니다. 변경은 전체 취소되었습니다.", 503)
		return
	}
	http.Redirect(w, r, "/admin/kentity/"+id.String(), 303)
}

func (s *Server) commonEntityLock(w http.ResponseWriter, r *http.Request) {
	staff, ok := r.Context().Value(readinessStaffKey{}).(readinessStaff)
	if !ok || (staff.Role != "operator" && staff.Role != "admin") {
		http.Error(w, "운영자 권한이 필요합니다.", 403)
		return
	}
	if os.Getenv("KDB_COMMON_ENTITY_ENABLED") != "1" || os.Getenv("KDB_ENTITY_OWNERSHIP_ENABLED") != "1" {
		http.Error(w, "공통 잠금 활성화 전입니다.", 503)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	rev, _ := strconv.ParseInt(r.FormValue("revision"), 10, 64)
	state := r.FormValue("locked")
	if state != "true" && state != "false" {
		http.Error(w, "잠금 요청 형식 오류", 400)
		return
	}
	err = (&kentity.Store{Pool: s.pool}).SetLock(r.Context(), staff.Email, id, rev, state == "true", r.FormValue("reason"))
	if errors.Is(err, kentity.ErrInvalid) {
		http.Error(w, "잠금 변경 사유를 10자 이상 입력하세요.", 400)
		return
	}
	var pgErr *pgconn.PgError
	if errors.Is(err, kentity.ErrProtected) || (errors.As(err, &pgErr) && (pgErr.Code == "40001" || pgErr.Code == "40P01" || pgErr.Code == "55P03")) {
		http.Error(w, "버전·쓰기 책임 또는 기존 KDB 잠금이 변경을 막았습니다. 상세를 다시 확인하세요.", 409)
		return
	}
	if errors.Is(err, kentity.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "잠금 변경을 저장하지 못했습니다.", 503)
		return
	}
	http.Redirect(w, r, "/admin/kentity/"+id.String(), 303)
}

package kdbadmin

import (
	"errors"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rickyjoo73/kdb/internal/kentity"
)

// commonEntityAddRole — 인물에게 직업을 더한다.
//
// 이 경로가 없으면 새로 등록한 정치인·경제인·스포츠인의 직업을 기록할 수단이 없다.
// 사전은 열려 있는데 넣을 곳이 없는 상태였다.
//
// **한 사람이 여러 직업을 갖는 것이 정상이다.** 이 폼은 직업을 **더하는** 것이지
// 바꾸는 것이 아니며, 새 UUID 를 만들지 않는다(식별 계약 §1.1).
func (s *Server) commonEntityAddRole(w http.ResponseWriter, r *http.Request) {
	staff, ok := r.Context().Value(readinessStaffKey{}).(readinessStaff)
	if !ok || (staff.Role != "admin" && staff.Role != "operator") {
		http.Error(w, "운영자 권한이 필요합니다.", 403)
		return
	}
	if os.Getenv("KDB_COMMON_ENTITY_ENABLED") != "1" {
		http.Error(w, "기능 활성화 전입니다.", 503)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	if err = r.ParseForm(); err != nil {
		http.Error(w, "입력 형식 오류", 400)
		return
	}
	_, err = (&kentity.Store{Pool: s.pool}).AddPersonRole(
		r.Context(), staff.Email, id, r.FormValue("role_code"), r.FormValue("reason"))
	switch {
	case errors.Is(err, kentity.ErrInvalid):
		http.Error(w, "인물만 직업을 가질 수 있고, 직군은 사전에 있어야 하며, 사유는 10자 이상이어야 합니다.", 400)
		return
	case errors.Is(err, kentity.ErrNotFound):
		http.NotFound(w, r)
		return
	case errors.Is(err, kentity.ErrProtected):
		http.Error(w, "잠긴 대상입니다. 잠금을 먼저 해제하세요.", 409)
		return
	case err != nil:
		http.Error(w, "직업을 저장하지 못했습니다.", 503)
		return
	}
	http.Redirect(w, r, "/admin/kentity/"+id.String(), http.StatusSeeOther)
}

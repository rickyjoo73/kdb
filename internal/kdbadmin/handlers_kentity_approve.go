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

func (s *Server) commonEntityResearchApprove(w http.ResponseWriter, r *http.Request) {
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
	in := kentity.ResearchApproval{EntityID: id, JobID: jobID, Generation: generation, QID: r.FormValue("qid"), Locales: r.Form["locales"], Reason: r.FormValue("reason"), IdentityFacts: r.FormValue("identity_facts"), Attested: r.FormValue("attested") == "yes"}
	err = (&kentity.Store{Pool: s.pool}).ApproveResearch(r.Context(), staff.Email, in)
	if errors.Is(err, kentity.ErrInvalid) {
		http.Error(w, "대상·선택 언어·동일인 확인 사실과 승인 사유를 확인하세요.", 400)
		return
	}
	var dbErr *pgconn.PgError
	if errors.Is(err, kentity.ErrProtected) || (errors.As(err, &dbErr) && (dbErr.Code == "40001" || dbErr.Code == "40P01" || dbErr.Code == "55P03" || dbErr.Code == "23505")) {
		http.Error(w, "대상 상태·잠금·외부 ID가 변경되었거나 충돌합니다. 상세와 기존 UUID를 다시 비교하세요.", 409)
		return
	}
	if errors.Is(err, kentity.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "승인을 저장하지 못했습니다. 변경은 전체 취소되었습니다.", 503)
		return
	}
	http.Redirect(w, r, "/admin/kentity/"+id.String(), 303)
}

package kdbadmin

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rickyjoo73/kdb/internal/kdb/readiness"
)

type readinessStaffKey struct{}
type readinessStaff struct{ Email, Role string }

// A valid cookie is not enough: revocation and the stored role are checked on
// every request to the new operational ledger, including direct POSTs.
func (s *Server) readinessStaffAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookieName)
		if err != nil {
			http.Error(w, "로그인이 필요합니다.", 401)
			return
		}
		email, ok := decodeSession(s.opts.SessionSecret, c.Value)
		if !ok {
			http.Error(w, "로그인이 필요합니다.", 401)
			return
		}
		if s.pool == nil {
			http.Error(w, "인증 상태를 확인할 수 없습니다.", 503)
			return
		}
		var role string
		var enabled bool
		err = s.pool.QueryRow(r.Context(), `SELECT role,enabled FROM kwave_kdb_admin_users WHERE email=$1`, email).Scan(&role, &enabled)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "인증 상태를 확인할 수 없습니다.", 503)
			return
		}
		if errors.Is(err, pgx.ErrNoRows) || !enabled {
			http.Error(w, "사용할 수 없는 관리 계정입니다.", 403)
			return
		}
		switch role {
		case "admin", "operator", "reviewer", "viewer":
		default:
			http.Error(w, "허용되지 않은 역할입니다.", 403)
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" && role != "admin" && role != "operator" {
			http.Error(w, "운영자 권한이 필요합니다.", 403)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), readinessStaffKey{}, readinessStaff{email, role})))
	})
}

type preparationRow struct {
	ID                         uuid.UUID
	Status, ArticleID, Version string
	CreatedAt                  time.Time
	Ready, Total               int
}
type preparationEvent struct {
	State, Reason, Locale string
	At                    time.Time
}
type preparationJob struct {
	ID                    uuid.UUID
	Locale, State, Reason string
	Attempts              int
	NextRetry             *time.Time
	UpdatedAt             time.Time
}

func readinessLabel(state string) string {
	switch state {
	case "preparing", "pending":
		return "준비 중"
	case "ready":
		return "요청 표기 준비"
	case "review":
		return "확인 필요"
	case "ambiguous":
		return "동명이인 검토"
	case "no_evidence":
		return "근거 부족"
	case "policy_blocked":
		return "정책·잠금 보류"
	case "failed":
		return "처리 오류"
	case "unverified":
		return "표기 미검증"
	case "cancelled":
		return "요청 취소"
	case "resolved":
		return "대상 연결됨"
	case "running":
		return "보충 중"
	case "complete":
		return "보충 저장 완료"
	case "stale":
		return "입력 변경·취소"
	case "created":
		return "요청 접수"
	case "retry_requested":
		return "운영자 재시도"
	}
	return state
}

func readinessReason(reason string) string {
	switch {
	case strings.Contains(reason, "value was withdrawn"):
		return "이전에 저장한 표기가 철회되었습니다. 근거를 다시 검토해야 합니다."
	case strings.Contains(reason, "renewed request interest"):
		return "새 요청이 있어 남아 있는 재시도 한도 안에서 다시 확인합니다."
	case strings.HasPrefix(reason, "source_error"):
		return "외부 원천 조회 오류입니다. 정해진 간격으로 재시도합니다."
	case strings.Contains(reason, "no resolved stable identity"):
		return "확정된 외부 식별자가 없어 자동 보충을 보류합니다."
	case strings.Contains(reason, "identity is not resolved"):
		return "정체성 근거 또는 동명이인 구분이 필요합니다."
	case strings.Contains(reason, "identity requires review"):
		return "대상 정체성을 먼저 검토해야 합니다."
	case strings.Contains(reason, "native locale value"):
		return "해당 언어의 표기와 인정된 출처 기록이 있습니다."
	case strings.Contains(reason, "no evidenced value"):
		return "요청 언어의 근거 표기를 보충하고 있습니다."
	case strings.Contains(reason, "lacks qualified"):
		return "저장된 표기를 아직 검증할 수 없습니다. 자동 덮어쓰기는 하지 않습니다."
	case strings.Contains(reason, "operator lock"):
		return "운영자 잠금 때문에 자동 보충을 중지했습니다."
	case strings.Contains(reason, "not active"):
		return "활성 Entity가 아니므로 표기를 제공하지 않습니다."
	case strings.Contains(reason, "no admissible"):
		return "현재 원천에 사용할 수 있는 해당 언어 표기가 없습니다."
	case strings.Contains(reason, "source returned"), strings.Contains(reason, "source identity"), strings.Contains(reason, "anchor is"):
		return "원천의 정체성 근거가 일치하지 않아 반영하지 않았습니다."
	case strings.Contains(reason, "changed during fetch"), strings.Contains(reason, "no longer eligible"):
		return "조회 중 입력이나 보호 상태가 변경되어 결과를 폐기했습니다."
	case strings.Contains(reason, "cancelled or replaced"):
		return "요청이 취소되거나 바뀌어 결과를 반영하지 않았습니다."
	case strings.Contains(reason, "installed from"):
		return "동일한 외부 식별자의 근거 표기를 저장했습니다."
	case strings.Contains(reason, "retry budget"):
		return "재시도 한도를 소진했습니다. 원인을 확인해 주세요."
	case strings.Contains(reason, "request accepted"):
		return "요청을 접수했습니다. 과거 준비 시각은 추정하지 않습니다."
	case strings.Contains(reason, "bounded retry"), strings.HasPrefix(reason, "operator retry:"):
		return "운영자가 사유를 남기고 제한된 재시도를 요청했습니다."
	}
	return reason
}

func (s *Server) preparationsList(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{"title": "요청 언어별 준비 상태", "enabled": os.Getenv("KDB_READINESS_ENABLED") == "1", "status": r.URL.Query().Get("status")}
	if s.pool == nil {
		data["loadError"] = true
		s.render(w, r, "preparations.html", data)
		return
	}
	rows, err := s.pool.Query(r.Context(), `WITH recent AS (
 SELECT id,status,article_id,article_version,created_at FROM kentity_preparations WHERE ($1='' OR status=$1)
 ORDER BY created_at DESC,id LIMIT 100)
 SELECT r.id,r.status,r.article_id,r.article_version,r.created_at,
 (SELECT count(*) FROM kentity_locale_readiness l WHERE l.preparation_id=r.id AND state='ready'),
 (SELECT count(*) FROM kentity_locale_readiness l WHERE l.preparation_id=r.id)
 FROM recent r ORDER BY r.created_at DESC,r.id`, r.URL.Query().Get("status"))
	if err != nil {
		log.Printf("admin preparations: %v", err)
		data["loadError"] = true
		s.render(w, r, "preparations.html", data)
		return
	}
	var items []preparationRow
	for rows.Next() {
		var item preparationRow
		if err = rows.Scan(&item.ID, &item.Status, &item.ArticleID, &item.Version, &item.CreatedAt, &item.Ready, &item.Total); err != nil {
			break
		}
		items = append(items, item)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		data["loadError"] = true
	} else {
		data["items"] = items
	}
	s.render(w, r, "preparations.html", data)
}

func (s *Server) preparationDetail(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var owner string
	if err = s.pool.QueryRow(r.Context(), `SELECT owner_key FROM kentity_preparations WHERE id=$1`, id).Scan(&owner); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.NotFound(w, r)
		} else {
			http.Error(w, "요청 정보를 조회하지 못했습니다.", 503)
		}
		return
	}
	p, err := (&readiness.Store{Pool: s.pool}).Get(r.Context(), owner, id)
	data := map[string]any{"title": "요청 언어별 준비 상세", "detail": true, "enabled": os.Getenv("KDB_READINESS_ENABLED") == "1"}
	if err != nil {
		data["loadError"] = true
		s.render(w, r, "preparations.html", data)
		return
	}
	data["request"] = p
	staff, _ := r.Context().Value(readinessStaffKey{}).(readinessStaff)
	data["canRetry"] = staff.Role == "admin" || staff.Role == "operator"
	rows, err := s.pool.Query(r.Context(), `SELECT state,reason,locale,observed_at FROM kentity_readiness_events WHERE preparation_id=$1 ORDER BY id DESC LIMIT 100`, id)
	if err != nil {
		data["loadError"] = true
	} else {
		var events []preparationEvent
		for rows.Next() {
			var e preparationEvent
			if err = rows.Scan(&e.State, &e.Reason, &e.Locale, &e.At); err != nil {
				break
			}
			events = append(events, e)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			data["loadError"] = true
		} else {
			data["events"] = events
		}
	}
	rows, err = s.pool.Query(r.Context(), `SELECT j.id,j.locale,j.state,j.last_reason,j.attempts,j.next_retry_at,j.updated_at FROM kentity_locale_fill_jobs j
 WHERE EXISTS(SELECT 1 FROM kentity_preparation_items i JOIN kentity_locale_readiness l USING(preparation_id,ordinal)
 WHERE i.preparation_id=$1 AND i.resolved_entity_id=j.entity_id AND l.locale=j.locale) ORDER BY j.updated_at DESC LIMIT 100`, id)
	if err != nil {
		data["loadError"] = true
	} else {
		var jobs []preparationJob
		for rows.Next() {
			var j preparationJob
			if err = rows.Scan(&j.ID, &j.Locale, &j.State, &j.Reason, &j.Attempts, &j.NextRetry, &j.UpdatedAt); err != nil {
				break
			}
			jobs = append(jobs, j)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			data["loadError"] = true
		} else {
			data["jobs"] = jobs
		}
	}
	s.render(w, r, "preparations.html", data)
}

func (s *Server) preparationRetry(w http.ResponseWriter, r *http.Request) {
	staff, ok := r.Context().Value(readinessStaffKey{}).(readinessStaff)
	if !ok || (staff.Role != "admin" && staff.Role != "operator") {
		http.Error(w, "운영자 권한이 필요합니다.", 403)
		return
	}
	if os.Getenv("KDB_READINESS_ENABLED") != "1" {
		http.Error(w, "자동 보충이 활성화되지 않았습니다.", 409)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	revision, e1 := strconv.ParseInt(r.FormValue("revision"), 10, 64)
	ordinal, e2 := strconv.Atoi(r.FormValue("ordinal"))
	if e1 != nil || e2 != nil || ordinal < 0 || ordinal > 199 {
		http.Error(w, "잘못된 대상입니다.", 400)
		return
	}
	var owner string
	if err = s.pool.QueryRow(r.Context(), `SELECT owner_key FROM kentity_preparations WHERE id=$1`, id).Scan(&owner); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.NotFound(w, r)
		} else {
			http.Error(w, "요청 정보를 조회하지 못했습니다.", 503)
		}
		return
	}
	err = (&readiness.Store{Pool: s.pool}).Retry(r.Context(), owner, staff.Email, id, revision, ordinal, r.FormValue("locale"), r.FormValue("reason"))
	if errors.Is(err, readiness.ErrPolicy) || errors.Is(err, readiness.ErrRevision) {
		http.Error(w, "입력·잠금·재시도 제한이 변경되었습니다. 상세 화면을 다시 확인하세요.", 409)
		return
	}
	if err != nil {
		log.Printf("admin preparation retry: %v", err)
		http.Error(w, "재시도를 저장하지 못했습니다.", 503)
		return
	}
	http.Redirect(w, r, "/admin/preparations/"+id.String(), http.StatusSeeOther)
}

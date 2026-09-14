package kdbadmin

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rickyjoo73/kdb/internal/kentity"
)

func (s *Server) commonEntityList(w http.ResponseWriter, r *http.Request) {
	if os.Getenv("KDB_COMMON_ENTITY_ENABLED") != "1" {
		http.Error(w, "공통 Entity 기능 활성화 전입니다.", 503)
		return
	}
	q := r.URL.Query()
	f := kentity.CatalogFilter{Q: q.Get("q"), Type: q.Get("type"), Domain: q.Get("domain"), Status: q.Get("status"), Origin: q.Get("origin"), Period: q.Get("period"), Sort: q.Get("sort"), Classify: q.Get("classify")}
	if raw := q.Get("offset"); raw != "" {
		var err error
		f.Offset, err = strconv.Atoi(raw)
		if err != nil {
			http.Error(w, "페이지 번호를 확인하세요.", 400)
			return
		}
	}
	if !f.Valid() {
		http.Error(w, "검색 조건을 확인하세요.", 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	data := map[string]any{"title": "고유명사 탐색 · 등록", "requestKey": uuid.NewString(), "q": f.Q, "domain": f.Domain, "type": f.Type, "classify": f.Classify, "filter": f, "openCreate": q.Get("create") == "1"}
	staff, _ := r.Context().Value(readinessStaffKey{}).(readinessStaff)
	data["canCreate"] = staff.Role == "admin" || staff.Role == "operator"
	page, err := (&kentity.Store{Pool: s.pool}).Catalog(ctx, f, 50)
	if err != nil {
		data["loadError"] = true
	} else {
		data["items"], data["catalog"] = page.Items, page
		data["rangeStart"] = 0
		if len(page.Items) > 0 {
			data["rangeStart"] = f.Offset + 1
		}
		data["rangeEnd"] = f.Offset + len(page.Items)
		if f.Offset > 0 {
			prev := f.Offset - 50
			if prev < 0 {
				prev = 0
			}
			data["prevURL"] = catalogPageURL(f, prev)
		}
		if int64(f.Offset+len(page.Items)) < page.Total && f.Offset+50 <= 100000 {
			data["nextURL"] = catalogPageURL(f, f.Offset+50)
		}
	}
	var review int
	if err = s.pool.QueryRow(ctx, `SELECT count(*) FROM kentity_crosswalks WHERE source_system='legacy_person' AND status IN ('review','conflict')`).Scan(&review); err != nil {
		data["loadError"] = true
	} else {
		data["mappingReview"] = review
	}
	s.render(w, r, "kentity.html", data)
}

func catalogPageURL(f kentity.CatalogFilter, offset int) string {
	q := url.Values{"q": {f.Q}, "type": {f.Type}, "domain": {f.Domain}, "status": {f.Status}, "origin": {f.Origin}, "period": {f.Period}, "sort": {f.Sort}, "classify": {f.Classify}, "offset": {strconv.Itoa(offset)}}
	return "/admin/kentity?" + q.Encode()
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
	// 인물이면 직업을 보여주고 더할 수 있게 한다. 여러 개인 것이 정상이다.
	if err == nil && e.Type == "person" {
		st := &kentity.Store{Pool: s.pool}
		if roles, rerr := st.PersonRoles(r.Context(), e.ID); rerr == nil {
			data["personRoles"] = roles
		}
		if codes, cerr := st.EnabledRoleCodes(r.Context()); cerr == nil {
			data["roleCodes"] = codes
		}
		staff, _ := r.Context().Value(readinessStaffKey{}).(readinessStaff)
		data["canAddRole"] = !e.Locked && (staff.Role == "operator" || staff.Role == "admin")
	}
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

// commonSupplyGates — 공급 개시 대기. 흡수분 중 **지금 열 수 있는 것**과
// **막힌 이유**를 한 화면에서 본다.
//
// 왜 필요한가. 흡수분 537,841건이 관리자 화면에서 통째로 안 보였다. 공통 원장 자체가
// 메뉴에 없었고(2026-09-14 발견), 무엇이 공급 가능한 상태인지·왜 막혔는지를 물어볼
// 곳도 없었다. 실제 작업은 SQL 스크립트로 돌아가는데 화면엔 흔적이 없으니
// "달라진 게 없어 보인다"가 정확한 관찰이었다.
//
// 가드는 internal/kentity/supply.go 한 곳에 있고 docs/p4/activate_absorbed_supply.sql
// 과 같아야 한다. 화면이 "열 수 있다"는데 스크립트가 막으면 화면이 거짓말이다.
func (s *Server) commonSupplyGates(w http.ResponseWriter, r *http.Request) {
	if os.Getenv("KDB_COMMON_ENTITY_ENABLED") != "1" {
		http.Error(w, "공통 Entity 기능 활성화 전입니다.", 503)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	data := map[string]any{"title": "공급 개시 대기"}
	page, err := (&kentity.Store{Pool: s.pool}).Supply(ctx, 50)
	if err != nil {
		log.Printf("kdbadmin: supply gates: %v", err)
		data["loadError"] = "집계를 불러오지 못했습니다. 잠시 후 다시 시도하세요."
	} else {
		data["supply"] = page
	}
	s.render(w, r, "kentity_supply.html", data)
}

// commonIdentityBacklog — 동일인 판정 대기열. **두 원장에 같은 대상이 있는 자리**를 모은다.
//
// 기존 원장 화면은 기존 원장만 보고 공통 원장 화면은 공통 원장만 본다.
// 겹치는 곳이 문제인데 **겹쳐 보는 화면이 없었다.** 여기가 P4.07 의 입력이고,
// P4.03~P4.06(인물 원천)이 여기 막혀 있다.
func (s *Server) commonIdentityBacklog(w http.ResponseWriter, r *http.Request) {
	if os.Getenv("KDB_COMMON_ENTITY_ENABLED") != "1" {
		http.Error(w, "공통 Entity 기능 활성화 전입니다.", 503)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	data := map[string]any{"title": "동일인 판정 대기열"}
	page, err := (&kentity.Store{Pool: s.pool}).IdentityBacklog(ctx, r.URL.Query().Get("kind"), 50)
	if err != nil {
		log.Printf("kdbadmin: identity backlog: %v", err)
		data["loadError"] = "집계를 불러오지 못했습니다. 잠시 후 다시 시도하세요."
	} else {
		data["backlog"] = page
	}
	s.render(w, r, "kentity_identity.html", data)
}

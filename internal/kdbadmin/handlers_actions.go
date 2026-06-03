package kdbadmin

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
)

// --- entity 일반 액션 (WF-4 / WF-6 공통) -------------------------------

// entityLockToggle — operator_locked 토글. true 시 source=operator-locked 모든 자동 source 차단.
func (s *Server) entityLockToggle(w http.ResponseWriter, r *http.Request) {
	id, ok := parseEntityID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var ko string
	var locked bool
	if err := s.pool.QueryRow(ctx, `
UPDATE kwave_entities SET operator_locked = NOT operator_locked, updated_at = now()
WHERE id = $1 RETURNING canonical_ko, operator_locked`, id).Scan(&ko, &locked); err != nil {
		s.redirectBack(w, r, "lock 실패: "+err.Error())
		return
	}
	state := "잠금 해제 🔓"
	if locked {
		state = "잠금 🔒"
	}
	s.redirectBack(w, r, state+": "+ko)
}

// entityReject — confidence=0, status='rejected'. 검색/리스트에서 사라짐.
func (s *Server) entityReject(w http.ResponseWriter, r *http.Request) {
	id, ok := parseEntityID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var ko string
	if err := s.pool.QueryRow(ctx, `
UPDATE kwave_entities SET status='rejected', confidence=0.000, updated_at=now()
WHERE id = $1 AND operator_locked = false
RETURNING canonical_ko`, id).Scan(&ko); err != nil {
		s.redirectBack(w, r, "reject 실패 (잠긴 entity 는 reject 불가)")
		return
	}
	s.redirectBack(w, r, "reject: "+ko)
}

// entityWikidataAdopt — Wikidata Fetch → 빈 locale 채움 (운영자 확인 표시: source='operator').
// confidence=GREATEST(현재, 0.75). 이미 있는 값은 안 덮음 (priority 룰 따름).
func (s *Server) entityWikidataAdopt(w http.ResponseWriter, r *http.Request) {
	id, ok := parseEntityID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	var ko string
	if err := s.pool.QueryRow(ctx, `SELECT canonical_ko FROM kwave_entities WHERE id=$1`, id).Scan(&ko); err != nil {
		s.redirectBack(w, r, "adopt 실패: entity 없음")
		return
	}

	wc := wikidata.New()
	ent, _, werr := wc.SearchAndFetch(ctx, ko)
	if werr != nil || ent == nil {
		s.redirectBack(w, r, "Wikidata 매칭 실패: "+ko+" (K-Wave 후보 없음)")
		return
	}

	// 빈 locale 만 채움 + source='operator' (운영자 확인 — 다른 source 가 못 덮음).
	_, _ = s.pool.Exec(ctx, `
UPDATE kwave_entities SET
  canonical_en       = COALESCE(NULLIF(canonical_en,''),       NULLIF($2,'')),
  canonical_en_source = CASE WHEN canonical_en IS NULL OR canonical_en = '' THEN 'operator' ELSE canonical_en_source END,
  canonical_ja       = COALESCE(NULLIF(canonical_ja,''),       NULLIF($3,'')),
  canonical_ja_source = CASE WHEN canonical_ja IS NULL OR canonical_ja = '' THEN 'operator' ELSE canonical_ja_source END,
  canonical_vi       = COALESCE(NULLIF(canonical_vi,''),       NULLIF($4,'')),
  canonical_vi_source = CASE WHEN canonical_vi IS NULL OR canonical_vi = '' THEN 'operator' ELSE canonical_vi_source END,
  canonical_id       = COALESCE(NULLIF(canonical_id,''),       NULLIF($5,'')),
  canonical_id_source = CASE WHEN canonical_id IS NULL OR canonical_id = '' THEN 'operator' ELSE canonical_id_source END,
  canonical_es       = COALESCE(NULLIF(canonical_es,''),       NULLIF($6,'')),
  canonical_es_source = CASE WHEN canonical_es IS NULL OR canonical_es = '' THEN 'operator' ELSE canonical_es_source END,
  canonical_pt_br    = COALESCE(NULLIF(canonical_pt_br,''),    NULLIF($7,'')),
  canonical_pt_br_source = CASE WHEN canonical_pt_br IS NULL OR canonical_pt_br = '' THEN 'operator' ELSE canonical_pt_br_source END,
  canonical_zh_hant  = COALESCE(NULLIF(canonical_zh_hant,''),  NULLIF($8,'')),
  canonical_zh_hant_source = CASE WHEN canonical_zh_hant IS NULL OR canonical_zh_hant = '' THEN 'operator' ELSE canonical_zh_hant_source END,
  canonical_zh       = COALESCE(NULLIF(canonical_zh,''),       NULLIF($9,'')),
  canonical_zh_source = CASE WHEN canonical_zh IS NULL OR canonical_zh = '' THEN 'operator' ELSE canonical_zh_source END,
  confidence = GREATEST(confidence, 0.750),
  updated_at = now()
WHERE id = $1`,
		id,
		ent.Labels["en"], ent.Labels["ja"], ent.Labels["vi"], ent.Labels["id"],
		ent.Labels["es"], ent.Labels["pt_br"], ent.Labels["zh_hant"], ent.Labels["zh"])

	// external_refs 에 Q-ID 매핑 기록.
	_, _ = s.pool.Exec(ctx, `
INSERT INTO kwave_entity_external_refs (entity_id, provider, external_id, url, confidence, fetched_at)
VALUES ($1, 'wikidata', $2, $3, 0.85, now())
ON CONFLICT DO NOTHING`,
		id, ent.QID, "https://www.wikidata.org/wiki/"+ent.QID)

	s.redirectBack(w, r, "Wikidata 채택: "+ko+" ("+ent.QID+")")
}

// --- helpers --------------------------------------------------------------

func parseEntityID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return uuid.UUID{}, false
	}
	return id, true
}

// redirectBack — Referer 가 동일 호스트의 /admin 경로일 때만 거기로, 아니면 /admin.
// flash 메시지는 query 로 전달. (raw Referer 를 그대로 쓰면 외부/프로토콜-상대 URL
// 로의 open-redirect 가 가능 → 내부 경로로 제한.)
func (s *Server) redirectBack(w http.ResponseWriter, r *http.Request, flash string) {
	dest := safeInternalReferer(r)
	if flash != "" {
		sep := "?"
		if u, err := url.Parse(dest); err == nil && u.RawQuery != "" {
			sep = "&"
		}
		dest += sep + "flash=" + url.QueryEscape(flash)
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// safeInternalReferer — Referer 가 (동일 호스트 또는 상대경로) 의 /admin 경로면 그
// path[+query] 를, 아니면 /admin 을 반환한다. 외부 호스트·프로토콜-상대 URL 거부.
func safeInternalReferer(r *http.Request) string {
	ref := r.Header.Get("Referer")
	if ref == "" {
		return "/admin"
	}
	u, err := url.Parse(ref)
	if err != nil {
		return "/admin"
	}
	if u.Host != "" && u.Host != r.Host { // 외부 호스트 → 거부
		return "/admin"
	}
	p := u.EscapedPath()
	if !strings.HasPrefix(p, "/admin") {
		return "/admin"
	}
	if u.RawQuery != "" {
		return p + "?" + u.RawQuery
	}
	return p
}

// pass-through used by other files (avoid unused import).
var _ = context.Background

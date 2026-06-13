package kdbadmin

import (
	"net/http"

	"github.com/rickyjoo73/kdb/internal/kdb/apikeys"
)

// apiSettingRow — API 설정 페이지 한 행(표시용).
type apiSettingRow struct {
	EnvVar      string
	Title       string
	Purpose     string
	IssueURL    string
	EntityTypes []string
	Keyless     bool
	Configured  bool   // 키가 설정돼 있나 (DB 또는 env)
	Source      string // "db" | "env" | ""
	Masked      string // 마스킹된 값
}

func (s *Server) settingRows(r *http.Request) []apiSettingRow {
	ctx := r.Context()
	var rows []apiSettingRow
	for _, sp := range apikeys.Specs() {
		row := apiSettingRow{
			EnvVar: sp.EnvVar, Title: sp.Title, Purpose: sp.Purpose,
			IssueURL: sp.IssueURL, EntityTypes: sp.EntityTypes, Keyless: sp.Keyless,
		}
		if !sp.Keyless {
			v, src := apikeys.Resolve(ctx, s.pool, sp.EnvVar)
			row.Configured = v != ""
			row.Source = src
			row.Masked = apikeys.Mask(v)
		}
		rows = append(rows, row)
	}
	return rows
}

// apiSettings — GET /admin/settings. 사용되는 모든 API 를 등록·표시(상태/소스/마스킹).
// 하단에 외부 매체(소비자) 인바운드 키 발급/회수 섹션도 함께 렌더.
func (s *Server) apiSettings(w http.ResponseWriter, r *http.Request) {
	consumers, cerr := s.listConsumers(r.Context())
	data := map[string]any{
		"title":     "API 설정",
		"page":      "/admin/settings",
		"rows":      s.settingRows(r),
		"saved":     r.URL.Query().Get("saved") == "1",
		"consumers": consumers,
		"crevoked":  r.URL.Query().Get("crevoked") == "1",
		// newKey 는 발급 직후 consumersIssue 가 직접 렌더할 때만 set (URL 미경유).
		"cerr":      r.URL.Query().Get("cerr"),
	}
	if cerr != nil {
		data["cerr"] = "소비자 목록 조회 실패: " + cerr.Error()
	}
	s.render(w, r, "api_settings.html", data)
}

// apiSettingsTest — POST /admin/settings/test. 외부 API 연결을 실측 점검 후 표시.
func (s *Server) apiSettingsTest(w http.ResponseWriter, r *http.Request) {
	// consumers 도 함께 넘긴다 — 안 넘기면 api_settings.html 의 소비자 키 섹션이
	// 빈값으로 렌더돼 "발급된 키 없음" 으로 잘못 보인다(테스트 후 키 목록 사라짐).
	consumers, _ := s.listConsumers(r.Context())
	s.render(w, r, "api_settings.html", map[string]any{
		"title":     "API 설정",
		"page":      "/admin/settings",
		"rows":      s.settingRows(r),
		"probes":    apikeys.Probe(r.Context(), s.pool),
		"consumers": consumers,
	})
}

// apiSettingsSave — POST /admin/settings. 입력된 키를 DB 에 저장(빈 입력=변경없음,
// "삭제" 체크=DB값 제거→.env fallback 복귀).
func (s *Server) apiSettingsSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, "settings parse", err)
		return
	}
	by := "admin"
	if c, err := r.Cookie(sessionCookieName); err == nil {
		if u, ok := decodeSession(s.opts.SessionSecret, c.Value); ok {
			by = u
		}
	}
	for _, sp := range apikeys.Specs() {
		if sp.Keyless || sp.EnvVar == "" {
			continue
		}
		if r.PostFormValue("clear_"+sp.EnvVar) == "1" {
			_ = apikeys.Set(r.Context(), s.pool, sp.EnvVar, "", by) // 삭제
			continue
		}
		v := r.PostFormValue(sp.EnvVar)
		if v != "" { // 빈 입력은 변경 없음(기존 값 보존)
			_ = apikeys.Set(r.Context(), s.pool, sp.EnvVar, v, by)
		}
	}
	http.Redirect(w, r, "/admin/settings?saved=1", http.StatusSeeOther)
}

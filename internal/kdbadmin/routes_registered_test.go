package kdbadmin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestOperatorActionRoutesRegistered 는 이전에 핸들러만 존재하고 라우터에
// 미등록(404)이던 운영자 액션 POST 라우트들이 실제로 등록됐는지 검증한다.
// 미인증 요청은 sessionAuth 가 로그인으로 redirect(3xx) 하므로, 라우트가
// 존재하면 404 가 아니다. 미등록이면 chi NotFound 가 404 를 준다.
// (nil pool 로도 안전 — sessionAuth 가 핸들러보다 먼저 실행돼 DB 미접근.)
func TestOperatorActionRoutesRegistered(t *testing.T) {
	h := NewRouter(nil, Options{SessionSecret: []byte("test-secret-routes")})

	paths := []string{
		"/admin/entities/123/lock",
		"/admin/entities/123/wikidata-adopt",
		"/admin/entities/whitelist/add",
		"/admin/entities/whitelist/vnexpress.net/vi/rss",
		"/admin/entities/whitelist/vnexpress.net/vi/toggle",
		"/admin/entities/whitelist/vnexpress.net/vi/delete",
	}
	for _, p := range paths {
		req := httptest.NewRequest(http.MethodPost, p, strings.NewReader(""))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code == http.StatusNotFound {
			t.Errorf("POST %s → 404 (route not registered)", p)
		}
	}
}

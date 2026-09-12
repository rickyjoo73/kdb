package kdbadmin

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rickyjoo73/kdb/internal/kdb/readiness"
	"github.com/rickyjoo73/kdb/internal/testdb"
)

func preparationPreview(state string) map[string]any {
	now := time.Date(2026, 9, 12, 3, 0, 0, 0, time.UTC)
	id := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	data := map[string]any{"title": "요청 언어별 준비 상태", "nav": activeNavItems("/admin/preparations"), "enabled": true, "csrf": "synthetic-fixture-only"}
	switch state {
	case "error":
		data["loadError"] = true
	case "list":
		data["items"] = []preparationRow{{ID: id, Status: "review", ArticleID: "테스트 기사", Version: "v2", CreatedAt: now, Ready: 1, Total: 3}}
	case "detail", "viewer":
		data["detail"] = true
		data["canRetry"] = state == "detail"
		data["request"] = &readiness.Preparation{ID: id, Status: "review", Revision: 2, RequestedLocales: []string{"en", "ja", "zh"}, PolicyVersion: readiness.PolicyVersion, CreatedAt: now, ArticleID: "테스트 기사", ArticleVersion: "v2", Items: []readiness.Item{{Term: "시험인물", Type: "person", EntityID: &id, IdentityState: "resolved", Context: "합성 예시이며 실제 인물이 아닙니다.", Locales: []readiness.Locale{
			{Locale: "en", State: "ready", Value: "Test Person", Source: "operator", FirstReadyAt: &now, ObservedAt: now},
			{Locale: "ja", State: "failed", FallbackValue: "Test Person", Reason: "source_error: fixture", ObservedAt: now},
			{Locale: "zh", State: "no_evidence", FallbackValue: "Test Person", Reason: "anchored source has no admissible requested-locale label", ObservedAt: now},
		}}}}
	}
	return data
}

func TestPreparationTemplateStatesAndRoleActions(t *testing.T) {
	s := renderSmokeServer(t)
	for _, state := range []string{"list", "empty", "error", "detail", "viewer"} {
		t.Run(state, func(t *testing.T) {
			var b bytes.Buffer
			if err := s.tmpl.ExecuteTemplate(&b, "preparations.html", preparationPreview(state)); err != nil {
				t.Fatal(err)
			}
			body := b.String()
			if state == "error" && (!strings.Contains(body, "원장 조회 실패") || strings.Contains(body, "추적한 요청이 없습니다")) {
				t.Fatal("error shown as empty")
			}
			if state == "detail" && (!strings.Contains(body, "제한 재시도 요청") || strings.Contains(body, "source_error: fixture") || !strings.Contains(body, "준비 완료에 포함하지 않음")) {
				t.Fatal("detail policy presentation missing")
			}
			if state == "viewer" && strings.Contains(body, "제한 재시도 요청") {
				t.Fatal("viewer offered mutation")
			}
		})
	}
}

func TestPreparationAdminRoleRevocationCSRFAndDatabaseFailure(t *testing.T) {
	t.Setenv("KDB_READINESS_ENABLED", "0")
	pool := testdb.New(t)
	ctx := context.Background()
	b, err := os.ReadFile("../../migrations/0115_kentity_readiness.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(b)); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `CREATE TABLE kwave_kdb_admin_users(email text PRIMARY KEY,role text,enabled boolean);
 INSERT INTO kwave_kdb_admin_users VALUES('fixture@test','viewer',true)`); err != nil {
		t.Fatal(err)
	}
	secret := []byte("synthetic-admin-secret-for-tests-only")
	session := encodeSession(secret, "fixture@test", sessionMaxAge)
	h := NewRouter(pool, Options{SessionSecret: secret})
	call := func(method, path string, csrf bool, cancelled bool) *httptest.ResponseRecorder {
		t.Helper()
		form := url.Values{}
		if csrf {
			form.Set("_csrf", csrfToken(secret, session))
		}
		r := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
		if cancelled {
			c, cancel := context.WithCancel(ctx)
			cancel()
			r = r.WithContext(c)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	path := "/admin/preparations/11111111-1111-4111-8111-111111111111/retry"
	if w := call("GET", "/admin/preparations", false, false); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("POST", path, true, false); w.Code != 403 {
		t.Fatal("viewer direct POST", w.Code)
	}
	if _, err = pool.Exec(ctx, `UPDATE kwave_kdb_admin_users SET role='operator'`); err != nil {
		t.Fatal(err)
	}
	if w := call("POST", path, false, false); w.Code != 403 {
		t.Fatal("CSRF bypass", w.Code)
	}
	if w := call("POST", path, true, false); w.Code != 409 {
		t.Fatal("disabled worker bypass", w.Code, w.Body.String())
	}
	if _, err = pool.Exec(ctx, `UPDATE kwave_kdb_admin_users SET enabled=false`); err != nil {
		t.Fatal(err)
	}
	if w := call("GET", "/admin/preparations", false, false); w.Code != 403 {
		t.Fatal("revoked cookie accepted", w.Code)
	}
	if w := call("GET", "/admin/preparations", false, true); w.Code != 503 || strings.Contains(w.Body.String(), "context canceled") {
		t.Fatal("DB failure concealed or leaked", w.Code, w.Body.String())
	}
}

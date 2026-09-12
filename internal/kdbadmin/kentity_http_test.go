package kdbadmin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/rickyjoo73/kdb/internal/testdb"
)

func TestCommonEntityHTTPRolesCSRFIdempotencyAndRevocation(t *testing.T) {
	t.Setenv("KDB_COMMON_ENTITY_ENABLED", "1")
	pool := testdb.New(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `ALTER TABLE kwave_persons ADD name_ko text; CREATE TABLE kwave_kdb_admin_users(email text PRIMARY KEY,role text,enabled boolean); INSERT INTO kwave_kdb_admin_users VALUES('fixture@test','viewer',true); INSERT INTO kwave_persons(name_ko) VALUES('검수 예시')`); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("../../migrations/0116_kentity_core.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(b)); err != nil {
		t.Fatal(err)
	}
	secret := []byte("common-admin-synthetic-secret")
	session := encodeSession(secret, "fixture@test", sessionMaxAge)
	h := NewRouter(pool, Options{SessionSecret: secret})
	call := func(method, path string, form url.Values) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	form := url.Values{"_csrf": {csrfToken(secret, session)}, "ko": {"동명이인 합성"}, "type": {"person"}, "domains": {"politics", "sports"}, "reason": {"합성 후보 등록 검증"}, "request_key": {"same-request"}}
	for _, path := range []string{"/admin/kentity", "/admin/kentity/mappings"} {
		if w := call("GET", path, nil); w.Code != 200 || strings.Contains(w.Body.String(), "조회 실패") {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	if w := call("POST", "/admin/kentity/candidates", form); w.Code != 403 {
		t.Fatal("viewer write", w.Code)
	}
	if _, err = pool.Exec(ctx, `UPDATE kwave_kdb_admin_users SET role='operator'`); err != nil {
		t.Fatal(err)
	}
	form.Del("_csrf")
	if w := call("POST", "/admin/kentity/candidates", form); w.Code != 403 {
		t.Fatal("csrf bypass", w.Code)
	}
	form.Set("_csrf", csrfToken(secret, session))
	w := call("POST", "/admin/kentity/candidates", form)
	if w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}
	path := w.Header().Get("Location")
	if w = call("POST", "/admin/kentity/candidates", form); w.Code != 303 || w.Header().Get("Location") != path {
		t.Fatal("duplicate candidate", w.Code)
	}
	if w = call("GET", path, nil); w.Code != 200 || strings.Contains(w.Body.String(), "조회 실패") {
		t.Fatal(w.Code, w.Body.String())
	}
	form.Set("reason", "다른 요청 내용")
	if w = call("POST", "/admin/kentity/candidates", form); w.Code != 409 {
		t.Fatal("reused key with changed input", w.Code)
	}
	form.Set("reason", strings.Repeat("가", 20000))
	if w = call("POST", "/admin/kentity/candidates", form); w.Code != 400 {
		t.Fatal("oversized form parsed before bound", w.Code)
	}
	var sourceID uuid.UUID
	if err = pool.QueryRow(ctx, `SELECT id FROM kwave_persons LIMIT 1`).Scan(&sourceID); err != nil {
		t.Fatal(err)
	}
	if w = call("GET", "/admin/kentity/mappings/"+sourceID.String(), nil); w.Code != 200 || strings.Contains(w.Body.String(), "조회 실패") {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, err = pool.Exec(ctx, `UPDATE kwave_kdb_admin_users SET enabled=false`); err != nil {
		t.Fatal(err)
	}
	if w = call("GET", path, nil); w.Code != 403 {
		t.Fatal("revoked account", w.Code)
	}
}

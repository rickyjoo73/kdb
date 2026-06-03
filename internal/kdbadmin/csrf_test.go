package kdbadmin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestValidCSRF(t *testing.T) {
	secret := []byte("test-secret-32-bytes-xxxxxxxxxxxx")
	sess := "session-token-abc"
	tok := csrfToken(secret, sess)
	if !validCSRF(secret, sess, tok) {
		t.Fatal("valid token should pass")
	}
	if validCSRF(secret, sess, "wrong") {
		t.Fatal("wrong token must fail")
	}
	if validCSRF(secret, "other-session", tok) {
		t.Fatal("token bound to a different session must fail")
	}
	if validCSRF(secret, sess, "") || validCSRF(secret, "", tok) {
		t.Fatal("empty inputs must fail")
	}
}

func TestCSRFProtectMiddleware(t *testing.T) {
	s := &Server{opts: Options{SessionSecret: []byte("test-secret-32-bytes-xxxxxxxxxxxx")}}
	ok := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }
	h := s.csrfProtect(http.HandlerFunc(ok))

	// GET 은 토큰 없이 통과.
	g := httptest.NewRequest("GET", "/admin/settings", nil)
	gw := httptest.NewRecorder()
	h.ServeHTTP(gw, g)
	if gw.Code != 200 {
		t.Fatalf("GET should pass, got %d", gw.Code)
	}

	// 세션 쿠키 + 일치 토큰으로 POST → 통과.
	sessTok := encodeSession(s.opts.SessionSecret, "admin@x", sessionMaxAge)
	tok := csrfToken(s.opts.SessionSecret, sessTok)
	form := url.Values{"_csrf": {tok}}
	p := httptest.NewRequest("POST", "/admin/settings", strings.NewReader(form.Encode()))
	p.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	p.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessTok})
	pw := httptest.NewRecorder()
	h.ServeHTTP(pw, p)
	if pw.Code != 200 {
		t.Fatalf("POST with valid CSRF should pass, got %d", pw.Code)
	}

	// 토큰 없는 POST → 403.
	p2 := httptest.NewRequest("POST", "/admin/settings", nil)
	p2.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessTok})
	pw2 := httptest.NewRecorder()
	h.ServeHTTP(pw2, p2)
	if pw2.Code != http.StatusForbidden {
		t.Fatalf("POST without CSRF must be 403, got %d", pw2.Code)
	}
}

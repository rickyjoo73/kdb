package kdbadmin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// hermesTestServer builds a Server with the full embedded template set parsed
// via the production loadTemplates() path and a nil pool (so the Hermes queries
// short-circuit to nil and the page renders its empty state without a DB).
func hermesTestServer(t *testing.T) *Server {
	t.Helper()
	s := &Server{opts: Options{SessionSecret: []byte("test-secret-test-secret-0123456789")}}
	s.loadTemplates()
	return s
}

// TestHermesNavWired asserts the report page is reachable from the sidebar.
func TestHermesNavWired(t *testing.T) {
	for _, n := range navItems() {
		if n.Path == "/admin/hermes" {
			return
		}
	}
	t.Fatal("nav is missing the /admin/hermes Hermes item")
}

// TestHermesPageRendersEmpty verifies the report page renders cleanly with zero
// rows when the DB pool is nil (kwave_kdb_hermes_runs is empty until Hermes
// runs live), and that the empty-state copy + nav item are present.
func TestHermesPageRendersEmpty(t *testing.T) {
	s := hermesTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/hermes", nil)
	s.handleHermes(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("handleHermes: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"no runs yet",
		"no open incidents",
		"no leaks detected",
		`href="/admin/hermes"`, // nav item rendered by the header partial
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
}

// TestHermesRouteRequiresAuth confirms the route is behind sessionAuth: an
// unauthenticated GET /admin/hermes redirects to the login page.
func TestHermesRouteRequiresAuth(t *testing.T) {
	h := NewRouter(nil, Options{SessionSecret: []byte("test-secret-test-secret-0123456789")})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/hermes", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("unauthenticated /admin/hermes: got %d, want 302 redirect", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/admin/login") {
		t.Fatalf("redirect Location = %q, want /admin/login...", loc)
	}
}

// TestFmtMetric covers nullable metric formatting.
func TestFmtMetric(t *testing.T) {
	if got := fmtMetric(nil); got != "–" {
		t.Errorf("fmtMetric(nil)=%q, want en-dash", got)
	}
	v := 0.5
	if got := fmtMetric(&v); got != "0.500" {
		t.Errorf("fmtMetric(0.5)=%q, want 0.500", got)
	}
}

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
		"트랙 A — 요청 처리",         // workflow track A heading
		"트랙 B — 자율 품질",         // workflow track B heading
		"자율성 heartbeat",          // heartbeat bar
		"LLM 미투입",                // honest gap badge on lexical/no-LLM steps
		"감독 밖",                   // un-supervised step badge
		"도메인별 LLM 에이전트",      // collapsed LLM-role detail section
		"분류 (Classify)",          // a domain card title (inside details)
		"레지스트리 등록 안 됨",       // phantom Classifier surfaced (honest visibility)
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

// TestWorkflowStepBadges asserts the workflow map classifies steps correctly:
// lexical/no-LLM steps carry no LLM, judgment steps with a backend resolve a
// provider, and the not-yet-built LLM gaps are flagged (not hidden). Pure
// VM-building (no DB), so it runs without a pool.
func TestWorkflowStepBadges(t *testing.T) {
	var s Server
	steps := map[string]workflowStepVM{}
	for _, tr := range s.buildWorkflowTracks(workflowStats{}) {
		for _, st := range tr.Steps {
			steps[st.Num] = st
		}
	}
	// A4 (lookup) is purely lexical — no LLM.
	if a4, ok := steps["A4"]; !ok || a4.LLMUsed {
		t.Errorf("A4 lookup should be LLM 미투입, got %+v", a4)
	}
	// B12 (Enricher L4) is an active LLM step — must resolve a provider.
	if b12, ok := steps["B12"]; !ok || !b12.LLMUsed || b12.Provider == "" {
		t.Errorf("B12 Enricher should be an LLM step with a provider, got %+v", b12)
	}
	// A8 (match-miss extract) is still a judgment gap with no LLM — must be
	// flagged, not silently LLM-less.
	if a8 := steps["A8"]; a8.LLMUsed || a8.Warn == "" {
		t.Errorf("A8 should be a flagged LLM gap (LLMUsed=false, Warn set), got %+v", a8)
	}
	// B13 (FillVerifier) is now an implemented LLM step (gemma), flag-gated —
	// must resolve a provider and still carry the flag note.
	if b13, ok := steps["B13"]; !ok || !b13.LLMUsed || b13.Provider == "" || b13.Warn == "" {
		t.Errorf("B13 FillVerifier should be an LLM step with provider + flag note, got %+v", b13)
	}
	// Every step must name an owning agent.
	for num, st := range steps {
		if st.Agent == "" {
			t.Errorf("step %s has no owning agent", num)
		}
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

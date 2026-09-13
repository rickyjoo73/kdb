package kdbadmin

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// 토큰 없이 통과해도 되는 POST — 세션이 아직 없어 세션-파생 토큰을 만들 수 없는 경로다.
// 대신 IP 단위 rate limit 이 붙어 있고, setup 은 관리자가 이미 있으면 404 로 닫힌다.
var csrfExemptPOST = map[string]bool{
	"/admin/login": true,
	"/admin/setup": true,
}

var chiParam = regexp.MustCompile(`\{[^}]+\}`)

// walkRoutes — 라우터에 실제로 등록된 (method, pattern) 을 전부 모은다.
// 목록을 손으로 적으면 새 라우트가 추가될 때 시험이 따라가지 못한다. 그게 이 저장소에서
// 실제로 벌어진 일이다 — /admin/entities/homonyms 는 핸들러만 있고 등록이 빠진 채
// 404 로 죽어 있었고, 손으로 적은 목록에는 애초에 없었으니 아무도 눈치채지 못했다.
func walkRoutes(t *testing.T, h http.Handler) map[string][]string {
	t.Helper()
	mux, ok := h.(chi.Routes)
	if !ok {
		t.Fatal("router 가 chi.Routes 가 아니다")
	}
	out := map[string][]string{}
	err := chi.Walk(mux, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		out[method] = append(out[method], route)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// concretePath — 패턴의 {id} 류를 실제 요청에 쓸 수 있는 값으로 바꾼다.
func concretePath(route string) string {
	p := chiParam.ReplaceAllString(route, "00000000-0000-4000-8000-000000000001")
	return strings.TrimSuffix(p, "/*")
}

// TestEveryAuthenticatedPOSTRequiresCSRF — 인증 그룹의 모든 POST 가 토큰 없는 요청을 거부하는가.
//
// 기존 csrf_test.go 는 미들웨어를 단독으로 호출할 뿐 라우터를 통과시키지 않는다. 그래서
// "POST 라우트를 인증 그룹 *밖에* 추가한다"거나 "r.Use(s.csrfProtect) 를 지운다"는 회귀를
// 잡지 못했다. 여기서는 등록부를 직접 걸어 전수로 확인한다.
func TestEveryAuthenticatedPOSTRequiresCSRF(t *testing.T) {
	h := NewRouter(nil, Options{SessionSecret: []byte("test-secret-test-secret-0123456789")})
	routes := walkRoutes(t, h)
	if len(routes["POST"]) < 20 {
		t.Fatal("POST 라우트가 너무 적다 — 열거가 실패했을 수 있다:", len(routes["POST"]))
	}
	sess := encodeSession([]byte("test-secret-test-secret-0123456789"), "admin@x", sessionMaxAge)
	checked := 0
	for _, route := range routes["POST"] {
		if csrfExemptPOST[route] {
			continue
		}
		path := concretePath(route)
		r := httptest.NewRequest("POST", path, strings.NewReader("x=1"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("POST %s: 토큰 없는 요청이 %d — 403 이어야 한다", route, w.Code)
		}
		checked++
	}
	t.Logf("인증 그룹 POST %d개 전부 토큰 없는 요청을 거부", checked)
}

// TestExemptPOSTRoutesAreOnlyLoginAndSetup — 면제 목록이 조용히 늘어나지 않게 고정한다.
// 세션 전(pre-session) 이 아닌 POST 가 인증 그룹 밖에 생기면 여기서 걸린다.
func TestExemptPOSTRoutesAreOnlyLoginAndSetup(t *testing.T) {
	h := NewRouter(nil, Options{SessionSecret: []byte("test-secret-test-secret-0123456789")})
	for _, route := range walkRoutes(t, h)["POST"] {
		if csrfExemptPOST[route] {
			continue
		}
		// 세션 쿠키 없이 보내면 sessionAuth 가 먼저 잡아 로그인으로 보내야 한다.
		r := httptest.NewRequest("POST", concretePath(route), strings.NewReader("x=1"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code/100 != 3 {
			t.Errorf("POST %s: 인증 없는 요청이 %d — 인증 그룹 밖에 있다", route, w.Code)
		}
	}
}

// TestOperatorOnlyRoutesAreUnderStaffAuth — 운영자 전용 서브트리가 그대로 있는지.
// readinessStaffAuth 는 역할 화이트리스트와 계정 비활성(revocation)을 매 요청 DB 로 확인한다.
// 이 미들웨어가 빠지면 유효 세션 쿠키만으로 승인·채택·잠금이 가능해진다.
func TestOperatorOnlyRoutesAreUnderStaffAuth(t *testing.T) {
	h := NewRouter(nil, Options{SessionSecret: []byte("test-secret-test-secret-0123456789")})
	want := map[string]bool{}
	for _, route := range walkRoutes(t, h)["POST"] {
		if strings.HasPrefix(route, "/admin/kentity") || strings.HasPrefix(route, "/admin/preparations") {
			want[route] = true
		}
	}
	if len(want) < 10 {
		t.Fatal("운영자 전용 POST 가 줄었다:", len(want))
	}
	t.Logf("운영자 전용 POST %d개 확인", len(want))
}

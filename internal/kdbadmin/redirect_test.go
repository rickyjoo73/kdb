package kdbadmin

import "testing"

func TestSafeAdminRedirect(t *testing.T) {
	cases := map[string]string{
		"/admin":                 "/admin",
		"/admin/entities?q=x":    "/admin/entities?q=x",
		"":                       "/admin",
		"/admin\\@evil.com":      "/admin", // 백슬래시 → 거부
		"//evil.com/admin":       "/admin", // scheme-relative(host=evil) → 거부
		"https://evil.com/admin": "/admin", // 절대 URL → 거부
		"/evil":                  "/admin", // /admin 외 경로 → 거부
		"/admin@evil.com":        "/admin/admin@evil.com", // 주: path 가 /admin 으로 시작하면 허용(동일출처 경로)
	}
	for in, want := range cases {
		// /admin@evil.com 케이스는 host 없는 동일출처 path 라 허용되는 게 맞다(브라우저가 같은 출처로 해석).
		if in == "/admin@evil.com" {
			if got := safeAdminRedirect(in); got != "/admin@evil.com" {
				t.Errorf("safeAdminRedirect(%q)=%q want /admin@evil.com (same-origin path)", in, got)
			}
			continue
		}
		if got := safeAdminRedirect(in); got != want {
			t.Errorf("safeAdminRedirect(%q)=%q want %q", in, got, want)
		}
	}
}

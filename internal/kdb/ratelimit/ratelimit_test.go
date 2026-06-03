package ratelimit

import (
	"net/http"
	"testing"
	"time"
)

func TestClientIP_RightmostXFF(t *testing.T) {
	// nginx append: 클라 위조분은 왼쪽, 실 IP 는 맨 오른쪽.
	r := &http.Request{Header: http.Header{}, RemoteAddr: "10.0.0.1:5000"}
	r.Header.Set("X-Forwarded-For", "1.2.3.4, 9.9.9.9, 203.0.113.7")
	if got := ClientIP(r); got != "203.0.113.7" {
		t.Fatalf("rightmost XFF = %q; want 203.0.113.7 (spoofed leftmost ignored)", got)
	}
	r2 := &http.Request{Header: http.Header{}, RemoteAddr: "10.0.0.1:5000"}
	r2.Header.Set("X-Forwarded-For", "198.51.100.2")
	if got := ClientIP(r2); got != "198.51.100.2" {
		t.Fatalf("single XFF = %q", got)
	}
	r3 := &http.Request{Header: http.Header{}, RemoteAddr: "192.0.2.5:443"}
	if got := ClientIP(r3); got != "192.0.2.5" {
		t.Fatalf("no XFF = %q; want 192.0.2.5", got)
	}
}

func TestLimiter_Blocks(t *testing.T) {
	l := New(2, time.Hour)
	now := time.Now()
	if !l.allow("a", now) || !l.allow("a", now) {
		t.Fatal("first 2 should pass")
	}
	if l.allow("a", now) {
		t.Fatal("3rd should be blocked")
	}
	if !l.allow("b", now) {
		t.Fatal("different ip must be independent")
	}
	if !l.allow("a", now.Add(2*time.Hour)) {
		t.Fatal("after window should reset")
	}
}

// Package ratelimit — 의존성 없는 인메모리 IP 고정창(fixed-window) rate limiter.
//
// 외부 패키지 없이(공급망/빌드 네트워크 회피) 브루트포스·요청 폭주를 완화한다.
// admin 로그인, 공개 /v1 그룹 등 HTTP 미들웨어로 쓴다. 단일 프로세스 한정
// (kdb-app 컨솔리데이티드 바이너리) — 분산 환경이 아니므로 인메모리로 충분.
package ratelimit

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// Limiter — IP 별 고정창 카운터.
type Limiter struct {
	mu     sync.Mutex
	hits   map[string]*window
	limit  int
	window time.Duration
	last   time.Time // 마지막 청소 시각 (idle IP purge)
}

type window struct {
	start time.Time
	count int
}

// New — limit 회/window 를 넘는 IP 를 차단하는 limiter.
func New(limit int, w time.Duration) *Limiter {
	return &Limiter{hits: make(map[string]*window), limit: limit, window: w}
}

// allow — 해당 IP 가 현재 창에서 허용되면 true (그리고 카운트 증가).
func (l *Limiter) allow(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	// 주기적 청소: 만료된 창의 IP 항목 제거(메모리 누수 방지).
	if now.Sub(l.last) > l.window {
		for k, v := range l.hits {
			if now.Sub(v.start) > l.window {
				delete(l.hits, k)
			}
		}
		l.last = now
	}
	w := l.hits[ip]
	if w == nil || now.Sub(w.start) > l.window {
		l.hits[ip] = &window{start: now, count: 1}
		return true
	}
	if w.count >= l.limit {
		return false
	}
	w.count++
	return true
}

// Middleware — 초과 시 429 를 반환하는 chi/net-http 미들웨어.
func (l *Limiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.allow(ClientIP(r), time.Now()) {
			w.Header().Set("Retry-After", "60")
			http.Error(w, `{"error":"rate limited"}`, http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ClientIP — 역프록시(nginx) 뒤를 고려해 X-Forwarded-For 첫 홉 우선, 없으면
// RemoteAddr 의 host. 신뢰 경계 안(127.0.0.1 바인딩 + 내부 docker 망)이라 XFF 신뢰.
func ClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := indexByte(xff, ','); i >= 0 {
			return trim(xff[:i])
		}
		return trim(xff)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func trim(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}

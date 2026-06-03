// Package ratelimit — 의존성 없는 인메모리 IP 고정창(fixed-window) rate limiter.
//
// 외부 패키지 없이(공급망/빌드 네트워크 회피) 브루트포스·요청 폭주를 완화한다.
// admin 로그인, 공개 /v1 그룹 등 HTTP 미들웨어로 쓴다. 단일 프로세스 한정
// (kdb-app 컨솔리데이티드 바이너리) — 분산 환경이 아니므로 인메모리로 충분.
package ratelimit

import (
	"net"
	"net/http"
	"strings"
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

// ClientIP — 실 클라이언트 IP. nginx 가 `$proxy_add_x_forwarded_for` 로 X-Forwarded-For
// 끝에 실제 접속 IP 를 *덧붙이므로*(append), 클라이언트가 헤더를 위조해도 위조분은
// 왼쪽에 쌓이고 신뢰 가능한 실 IP 는 항상 **맨 오른쪽**이다. 따라서 leftmost(클라
// 제어, 위조로 rate-limit 우회/피해자 차단 가능)가 아니라 rightmost 를 쓴다. XFF 가
// 없으면 RemoteAddr(직결) 사용.
func ClientIP(r *http.Request) string {
	if xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xff != "" {
		// nginx 가 append 한 마지막(가장 오른쪽) 항목 = 신뢰 가능한 실 접속 IP.
		if i := strings.LastIndexByte(xff, ','); i >= 0 {
			if ip := strings.TrimSpace(xff[i+1:]); ip != "" {
				return ip
			}
		} else {
			return xff // 단일 항목(프록시가 처음 설정) = 실 IP
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

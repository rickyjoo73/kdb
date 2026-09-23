// Package ratelimit — 의존성 없는 인메모리 IP 고정창(fixed-window) rate limiter.
//
// 외부 패키지 없이(공급망/빌드 네트워크 회피) 브루트포스·요청 폭주를 완화한다.
// admin 로그인, 공개 /v1 그룹 등 HTTP 미들웨어로 쓴다. 단일 프로세스 한정
// (kdb-app 컨솔리데이티드 바이너리) — 분산 환경이 아니므로 인메모리로 충분.
package ratelimit

import (
	"net"
	"net/http"
	"strconv"
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
	ok, _, _ := l.take(ip, now)
	return ok
}

// take — allow 와 같되 **남은 횟수와 창이 다시 열리는 시각**을 함께 준다.
//
// ★왜 필요한가 (2026-09-23 PressLocale). 문서가 «분당 120회»라고만 말하고 응답에는
//
//	남은 횟수가 없었다. 소비자는 자기가 얼마나 썼는지 알 길이 없어 **429 를 받고 나서야**
//	속도를 줄인다. 남은 횟수를 알려 주면 그전에 스스로 늦출 수 있다.
func (l *Limiter) take(ip string, now time.Time) (allowed bool, remaining int, reset time.Time) {
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
	win := l.hits[ip]
	if win == nil || now.Sub(win.start) > l.window {
		l.hits[ip] = &window{start: now, count: 1}
		return true, l.limit - 1, now.Add(l.window)
	}
	if win.count >= l.limit {
		return false, 0, win.start.Add(l.window)
	}
	win.count++
	return true, l.limit - win.count, win.start.Add(l.window)
}

// Middleware — 초과 시 429 를 반환하는 chi/net-http 미들웨어. 매 응답에 남은 횟수를
// 실어, 소비자가 429 를 받기 **전에** 스스로 속도를 늦출 수 있게 한다.
func (l *Limiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allowed, remaining, reset := l.take(ClientIP(r), time.Now())
		// 이름은 널리 쓰이는 관례를 따른다(X-RateLimit-*). Reset 은 유닉스 초 —
		// 남은 «초»가 아니라 시각이라야 시계가 어긋나도 해석이 갈리지 않는다.
		w.Header().Set("X-RateLimit-Limit", strconv.Itoa(l.limit))
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
		if !allowed {
			retry := int(time.Until(reset).Seconds())
			if retry < 1 {
				retry = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(retry))
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

package kdbapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// apiRequestLogger — 인증 통과한 /v1/* 클라이언트 요청을 DB(kwave_kdb_api_requests)에
// 비동기 기록한다. 대시보드 "클라이언트 요청" 가시화의 데이터 원천. 인증 미들웨어가
// 컨텍스트에 심은 소비자 id·tier 를 읽으므로 반드시 auth 뒤에 배선한다(=더 안쪽).
// health/docs 는 잡음이라 제외. 응답을 블록하지 않도록 INSERT 는 detached goroutine.
func apiRequestLogger(pool *pgxpool.Pool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			if pool == nil || !strings.HasPrefix(r.URL.Path, "/v1/") ||
				r.URL.Path == "/v1/health" || r.URL.Path == "/v1/docs" {
				return
			}
			cid, _ := r.Context().Value(ctxKeyConsumer).(string)
			tier, _ := r.Context().Value(ctxKeyTier).(keyTier)
			q := r.URL.RawQuery
			if len(q) > 500 {
				q = q[:500]
			}
			status := rec.status
			method := r.Method
			path := r.URL.Path
			dur := int(time.Since(start).Milliseconds())
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				var cidArg any
				if cid != "" {
					cidArg = cid
				}
				_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_api_requests (consumer_id, tier, method, path, query, status, duration_ms)
VALUES ($1,$2,$3,$4,$5,$6,$7)`, cidArg, string(tier), method, path, q, status, dur)
			}()
		})
	}
}

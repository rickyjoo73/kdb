package kdbadmin

import (
	"context"
	"net/http"
	"time"
)

// --- 클라이언트(소비자) API 요청 로그 뷰 (kwave_kdb_api_requests, migration 0077) ---
// 외부 클라이언트가 /v1 을 어떻게 호출하는지(누가·무엇을·성공/미스) 가시화.

type apiReqRow struct {
	Consumer   string
	Tier       string
	Method     string
	Path       string
	Query      string
	Status     int
	DurationMs int
	CreatedAt  time.Time
}

type apiReqAgg struct {
	Label string
	N     int64
	Miss  int64
}

func (s *Server) apiRequests(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	var today, week, miss7d int64
	_ = s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM kwave_kdb_api_requests WHERE created_at::date = now()::date`).Scan(&today)
	_ = s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM kwave_kdb_api_requests WHERE created_at > now()-interval '7 days'`).Scan(&week)
	_ = s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM kwave_kdb_api_requests WHERE created_at > now()-interval '7 days' AND status >= 400`).Scan(&miss7d)

	consumerLabel := `COALESCE(c.label, CASE WHEN ar.tier='write' THEN '(operator/env)' ELSE '(unknown)' END)`

	byConsumer := []apiReqAgg{}
	if rows, err := s.pool.Query(ctx, `
SELECT `+consumerLabel+` label, COUNT(*) n, COUNT(*) FILTER (WHERE ar.status>=400) miss
FROM kwave_kdb_api_requests ar
LEFT JOIN kwave_kdb_api_consumers c ON c.id = ar.consumer_id
WHERE ar.created_at > now()-interval '7 days'
GROUP BY 1 ORDER BY n DESC LIMIT 50`); err == nil {
		defer rows.Close()
		for rows.Next() {
			var x apiReqAgg
			if rows.Scan(&x.Label, &x.N, &x.Miss) == nil {
				byConsumer = append(byConsumer, x)
			}
		}
	}

	byPath := []apiReqAgg{}
	if rows, err := s.pool.Query(ctx, `
SELECT path, COUNT(*) n, COUNT(*) FILTER (WHERE status>=400) miss
FROM kwave_kdb_api_requests WHERE created_at > now()-interval '7 days'
GROUP BY path ORDER BY n DESC LIMIT 50`); err == nil {
		defer rows.Close()
		for rows.Next() {
			var x apiReqAgg
			if rows.Scan(&x.Label, &x.N, &x.Miss) == nil {
				byPath = append(byPath, x)
			}
		}
	}

	recent := []apiReqRow{}
	if rows, err := s.pool.Query(ctx, `
SELECT `+consumerLabel+`, ar.tier, ar.method, ar.path, ar.query, ar.status, ar.duration_ms, ar.created_at
FROM kwave_kdb_api_requests ar
LEFT JOIN kwave_kdb_api_consumers c ON c.id = ar.consumer_id
ORDER BY ar.id DESC LIMIT 100`); err == nil {
		defer rows.Close()
		for rows.Next() {
			var x apiReqRow
			if rows.Scan(&x.Consumer, &x.Tier, &x.Method, &x.Path, &x.Query, &x.Status, &x.DurationMs, &x.CreatedAt) == nil {
				recent = append(recent, x)
			}
		}
	}

	s.render(w, r, "api_requests.html", map[string]any{
		"title":      "클라이언트 요청 로그",
		"today":      today,
		"week":       week,
		"miss7d":     miss7d,
		"byConsumer": byConsumer,
		"byPath":     byPath,
		"recent":     recent,
		"page":       "/admin/kdb/requests",
	})
}

package kdbadmin

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// --- 온디맨드(kstory) 섹션 --------------------------------------------------
// 프로액티브 RSS 크롤을 폐기하고 소비자 요청 키워드를 즉시 해소·서빙하는 모델로
// 전환하면서, 그 흐름(요청 → 발굴 → 응답)을 admin 에서 볼 수 있게 하는 두 화면.
//   1) 발굴 큐   : research_queue + entities 조인 → 요청 키워드별 처리 결과
//   2) 소비자    : api_requests + api_consumers 집계 → 누가·뭘·성공/미스

// ---------- 1. 발굴 큐 (/admin/ondemand/queue) ----------

type rqRow struct {
	Ko           string
	Type         string
	QStatus      string // pending / in_progress / done / failed
	Attempts     int
	LastError    string
	CreatedAt    time.Time
	FinishedAt   *time.Time
	EntityStatus string // active / candidate / rejected / ""(미생성)
	Outcome      string // 사람이 읽는 처리 결과
	OutcomeClass string // 뱃지 색 키
}

// rqOutcome — 큐 상태 + 엔티티 상태를 "요청 키워드가 어떻게 처리됐나" 라벨로 환산.
func rqOutcome(qStatus, entityStatus string) (label, class string) {
	switch qStatus {
	case "pending":
		return "대기중", "wait"
	case "in_progress":
		return "발굴중", "prog"
	case "failed":
		return "실패", "fail"
	}
	// done — 실제 엔티티 결말로 판정
	switch entityStatus {
	case "active":
		return "해소됨 · active", "ok"
	case "candidate":
		return "후보 대기 · Inbox", "cand"
	case "rejected":
		return "기각 · 오염/범위밖", "rej"
	default:
		return "완료 · 미생성", "none"
	}
}

func (s *Server) ondemandQueue(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()

	p := newPage(r, "/admin/ondemand/queue")
	statusFilter := strings.TrimSpace(r.URL.Query().Get("status"))
	if statusFilter != "" {
		p.Extras["status"] = statusFilter
	}

	// 큐 상태 요약 (pending/in_progress/done/failed)
	var qPending, qProgress, qDone, qFailed int64
	_ = s.pool.QueryRow(ctx, `
SELECT count(*) FILTER (WHERE status='pending'),
       count(*) FILTER (WHERE status='in_progress'),
       count(*) FILTER (WHERE status='done'),
       count(*) FILTER (WHERE status='failed')
FROM kwave_entity_research_queue`).Scan(&qPending, &qProgress, &qDone, &qFailed)

	// 처리 결과(엔티티 결말) 요약 — 요청이 실제로 어떻게 끝났나
	var outActive, outCand, outRej, outNone int64
	_ = s.pool.QueryRow(ctx, `
SELECT count(*) FILTER (WHERE e.status='active'),
       count(*) FILTER (WHERE e.status='candidate'),
       count(*) FILTER (WHERE e.status='rejected'),
       count(*) FILTER (WHERE e.st IS NULL)
FROM kwave_entity_research_queue rq
LEFT JOIN LATERAL (
  SELECT status AS st, status FROM kwave_entities
  WHERE canonical_ko = rq.entity_ko
  ORDER BY CASE status WHEN 'active' THEN 0 WHEN 'candidate' THEN 1 ELSE 2 END
  LIMIT 1
) e ON true`).Scan(&outActive, &outCand, &outRej, &outNone)

	// 큐 상태 필터 (pending/in_progress/done/failed)
	listArgs := []any{p.Limit, p.Offset}
	listWhere := ""
	if statusFilter != "" {
		listWhere = "WHERE rq.status = $3"
		listArgs = append(listArgs, statusFilter)
	}

	var total int64
	if statusFilter != "" {
		_ = s.pool.QueryRow(ctx, "SELECT count(*) FROM kwave_entity_research_queue WHERE status = $1", statusFilter).Scan(&total)
	} else {
		_ = s.pool.QueryRow(ctx, "SELECT count(*) FROM kwave_entity_research_queue").Scan(&total)
	}
	p.finalize(total)
	rows, err := s.pool.Query(ctx, `
SELECT rq.entity_ko, rq.requested_entity_type::text, rq.status, rq.attempts,
       COALESCE(rq.last_error,''), rq.created_at, rq.finished_at,
       COALESCE(e.status,'')
FROM kwave_entity_research_queue rq
LEFT JOIN LATERAL (
  SELECT status FROM kwave_entities
  WHERE canonical_ko = rq.entity_ko
  ORDER BY CASE status WHEN 'active' THEN 0 WHEN 'candidate' THEN 1 ELSE 2 END
  LIMIT 1
) e ON true
`+listWhere+`
ORDER BY rq.created_at DESC
LIMIT $1 OFFSET $2`, listArgs...)
	if err != nil {
		s.renderError(w, r, "ondemand-queue", err)
		return
	}
	defer rows.Close()

	items := []rqRow{}
	for rows.Next() {
		var x rqRow
		if err := rows.Scan(&x.Ko, &x.Type, &x.QStatus, &x.Attempts,
			&x.LastError, &x.CreatedAt, &x.FinishedAt, &x.EntityStatus); err != nil {
			continue
		}
		x.Outcome, x.OutcomeClass = rqOutcome(x.QStatus, x.EntityStatus)
		items = append(items, x)
	}

	s.render(w, r, "ondemand_queue.html", map[string]any{
		"title":        "온디맨드 발굴 큐",
		"items":        items,
		"p":            p,
		"statusFilter": statusFilter,
		"qPending":     qPending,
		"qProgress":    qProgress,
		"qDone":        qDone,
		"qFailed":      qFailed,
		"outActive":    outActive,
		"outCand":      outCand,
		"outRej":       outRej,
		"outNone":      outNone,
		"page":         "/admin/ondemand/queue",
	})
}

// ---------- 2. 소비자 대시보드 (/admin/ondemand/consumers) ----------

type ondemandConsumerRow struct {
	Label    string
	Active   bool
	Req7d    int64
	Miss7d   int64
	MissPct  int
	LastUsed *time.Time
	TopPath  string
}

func (s *Server) ondemandConsumers(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()

	var totalConsumers, activeConsumers, req7d, miss7d int64
	_ = s.pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE active) FROM kwave_kdb_api_consumers`).Scan(&totalConsumers, &activeConsumers)
	_ = s.pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE status>=400) FROM kwave_kdb_api_requests WHERE created_at > now()-interval '7 days'`).Scan(&req7d, &miss7d)
	missPct := 0
	if req7d > 0 {
		missPct = int(miss7d * 100 / req7d)
	}

	items := []ondemandConsumerRow{}
	rows, err := s.pool.Query(ctx, `
SELECT c.label, c.active, c.last_used_at,
  COUNT(ar.id) FILTER (WHERE ar.created_at > now()-interval '7 days'),
  COUNT(ar.id) FILTER (WHERE ar.created_at > now()-interval '7 days' AND ar.status >= 400),
  COALESCE((SELECT ar2.path FROM kwave_kdb_api_requests ar2
            WHERE ar2.consumer_id = c.id AND ar2.created_at > now()-interval '7 days'
            GROUP BY ar2.path ORDER BY count(*) DESC LIMIT 1), '')
FROM kwave_kdb_api_consumers c
LEFT JOIN kwave_kdb_api_requests ar ON ar.consumer_id = c.id
GROUP BY c.id, c.label, c.active, c.last_used_at
ORDER BY 4 DESC, c.created_at DESC`)
	if err != nil {
		s.renderError(w, r, "ondemand-consumers", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var x ondemandConsumerRow
		if err := rows.Scan(&x.Label, &x.Active, &x.LastUsed, &x.Req7d, &x.Miss7d, &x.TopPath); err != nil {
			continue
		}
		if x.Req7d > 0 {
			x.MissPct = int(x.Miss7d * 100 / x.Req7d)
		}
		items = append(items, x)
	}

	s.render(w, r, "ondemand_consumers.html", map[string]any{
		"title":           "소비자 · kstory 대시보드",
		"items":           items,
		"totalConsumers":  totalConsumers,
		"activeConsumers": activeConsumers,
		"req7d":           req7d,
		"miss7d":          miss7d,
		"missPct":         missPct,
		"page":            "/admin/ondemand/consumers",
	})
}

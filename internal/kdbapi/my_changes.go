package kdbapi

// my_changes — **"당신이 물었던 것 중 달라진 것."**
//
// ★왜 이것이 통보를 대신하는가 (2026-09-16).
//
//   우리는 소비자별로 **어떤 낱말에 무엇이라 답했는지** 이미 전부 갖고 있다
//   (kwave_kdb_request_terms: consumer_id · term_ko · item_status · created_at).
//   그러니 "무엇을 다시 물어야 하는지"는 소비자가 계산할 일이 아니라 **우리가 아는 일**이다.
//
//   통보 방식과 비교하면:
//     메일/슬랙  사람이 읽어야 하고, 다음 변경 때 또 사람이 필요하다.
//     웹훅       소비자 쪽에 수신 엔드포인트와 우리 쪽에 연락처·재시도·인증이 필요하다.
//     이 방식     소비자가 하루 한 번 부르면 자기 미결이 정확히 돌아온다. 연락처가 필요 없다.
//
//   소비자는 기계다 — 7일간 /v1/entities 1,227 · match 554 · lookup/bulk 304 · prepare 222.
//   이미 부르고 있는 문에 하나를 더하는 것이 가장 닿는 길이다.
//
// ★지어내지 않는다(D-37). 여기서 하는 말은 전부 원장에서 읽은 것이다:
//     지금 active 다            → ready
//     되살아나 candidate 다     → preparing
//     판정 판본이 낡았다        → reask (다시 물으면 지금 규칙으로 다시 판단된다)
//   달라진 게 없으면 **행을 안 낸다.** 빈 목록이 "볼 것 없음"의 정직한 답이다.

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/rickyjoo73/kdb/internal/kdb/agents/gatekeeper"
)

// MyChange — 소비자가 받았던 답 하나와 지금의 답.
type MyChange struct {
	Term string `json:"term"`
	// Was — 그때 우리가 준 답.
	Was string `json:"was"`
	// Now — 지금 물으면 받을 답. reask 면 "다시 물어 주세요"라는 뜻이다.
	Now string `json:"now"`
	// Why — 무엇이 바뀌어서 달라졌는가.
	Why string `json:"why"`
	// KID — 지금 대상이 특정되면 그 id. 이후로는 이름 대신 이것으로 물으면 된다.
	KID        string    `json:"kid,omitempty"`
	Type       string    `json:"type,omitempty"`
	AnsweredAt time.Time `json:"answered_at"`
}

type myChangesResponse struct {
	Consumer     string     `json:"consumer,omitempty"`
	Since        time.Time  `json:"since"`
	NextSince    time.Time  `json:"next_since"`
	RulesVersion string     `json:"rules_version"`
	Changes      []MyChange `json:"changes"`
	// Truncated — 한도에 걸려 잘렸다. next_since 로 이어 부르면 나머지가 온다.
	Truncated bool   `json:"truncated,omitempty"`
	HowToUse  string `json:"how_to_use"`
}

const (
	myChangesDefaultWindow = 30 * 24 * time.Hour
	myChangesMaxWindow     = 90 * 24 * time.Hour
	myChangesLimit         = 500
)

func (h *handler) myChanges(w http.ResponseWriter, r *http.Request) {
	if h.store == nil || h.store.Pool == nil {
		writeError(w, http.StatusServiceUnavailable, "store unavailable")
		return
	}
	consumer := reporterID(r)
	if consumer == "" {
		writeError(w, http.StatusForbidden, "소비자 키로만 부를 수 있습니다")
		return
	}
	now := time.Now()
	since := now.Add(-myChangesDefaultWindow)
	if raw := strings.TrimSpace(r.URL.Query().Get("since")); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "since 는 RFC3339 여야 합니다 (예: 2026-09-01T00:00:00Z)")
			return
		}
		since = t
		if oldest := now.Add(-myChangesMaxWindow); since.Before(oldest) {
			since = oldest // 창을 넓게 잡아도 90일까지만 본다 — 질의 비용이 소비자 손에 달리면 안 된다
		}
	}
	changes, truncated, err := h.store.ConsumerChanges(r.Context(), consumer, since, myChangesLimit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "조회 실패")
		return
	}
	writeJSON(w, http.StatusOK, myChangesResponse{
		Since: since, NextSince: now, RulesVersion: CurrentRuleVersion(),
		Changes: changes, Truncated: truncated,
		HowToUse: "now=ready 는 바로 다시 조회하시면 값이 나옵니다. now=preparing 은 준비 중입니다. " +
			"now=reask 는 그때의 판정이 만료된 것이라 다시 보내 주시면 지금 규칙으로 다시 판단합니다. " +
			"다음 호출에 since=next_since 를 넣으시면 그 뒤의 변화만 옵니다.",
	})
}

// ConsumerChanges — 이 소비자에게 **종결로 답했던** 낱말 중 지금 답이 달라진 것.
func (s *Store) ConsumerChanges(ctx context.Context, consumer string, since time.Time, limit int) ([]MyChange, bool, error) {
	rows, err := s.Pool.Query(ctx, `
WITH mine AS (
  SELECT DISTINCT ON (term_ko) term_ko, item_status AS was, term_type, created_at
    FROM kwave_kdb_request_terms
   WHERE consumer_id = $1 AND created_at >= $2
     -- 이미 값을 드린 것(ready)은 볼 필요가 없다. 못 드린 것만 본다.
     AND item_status IN ('out_of_scope','review','unfillable','preparing','new')
   ORDER BY term_ko, created_at DESC
)
SELECT m.term_ko, m.was, m.created_at,
       COALESCE(e.status::text,''), COALESCE(e.entity_type::text,''), COALESCE(e.kid,''),
       COALESCE(q.precheck_rule_version,'')
  FROM mine m
  LEFT JOIN LATERAL (
       SELECT status, entity_type, kid FROM kwave_entities e2
        WHERE e2.canonical_ko = m.term_ko AND e2.status IN ('active','candidate')
        ORDER BY (e2.status='active') DESC, e2.updated_at DESC LIMIT 1
  ) e ON true
  LEFT JOIN LATERAL (
       SELECT precheck_rule_version FROM kwave_entity_research_queue q2
        WHERE q2.entity_ko = m.term_ko ORDER BY q2.created_at DESC LIMIT 1
  ) q ON true
 ORDER BY m.created_at DESC
 LIMIT $3`, consumer, since, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	out := make([]MyChange, 0, 16)
	seen := 0
	for rows.Next() {
		seen++
		var term, was, status, typ, kid, ruleVer string
		var at time.Time
		if err := rows.Scan(&term, &was, &at, &status, &typ, &kid, &ruleVer); err != nil {
			return nil, false, err
		}
		if seen > limit {
			return out, true, nil
		}
		c := MyChange{Term: term, Was: was, KID: kid, Type: typ, AnsweredAt: at}
		switch {
		case status == "active":
			// 원장에 살아 있다 — 바로 다시 조회하면 값이 나온다.
			if was == "ready" {
				continue
			}
			c.Now, c.Why = "ready", "now_served"
		case status == "candidate":
			// 되살아났거나 새로 들어왔다. 아직 서빙은 아니다 — 지어내지 않는다.
			if was == "preparing" || was == "new" {
				continue // 그때도 준비중이었고 지금도 준비중이다. 달라진 게 없다.
			}
			c.Now, c.Why = "preparing", "reopened"
		case ruleVer != "" && ruleVer != gatekeeper.IntakeRuleVersion &&
			(was == "out_of_scope" || was == "review"):
			// 판정을 내린 규칙이 바뀌었다. 다시 물으면 지금 규칙으로 다시 본다.
			c.Now, c.Why = "reask", "rule_version_expired"
		default:
			continue // 달라진 게 없다 — 행을 안 낸다
		}
		out = append(out, c)
	}
	return out, false, rows.Err()
}

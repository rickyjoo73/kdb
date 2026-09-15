package kdbapi

// prepare_outcome — 이미 끝난 발굴을 "준비중"이라고 답하지 않는다.
//
// ★실측 (2026-09-15). `preparing` 으로 답한 낱말을 6시간 뒤에 되짚으니 절반 이상이
//   끝내 안 채워졌다 — 하루가 지나도 34~45% 에서 멈춘다. 그런데 발굴 큐를 보면
//   그것들은 **이미 끝나 있었다**:
//
//     done · no_match          86   표기를 못 찾았고 끝났다
//     done · blocked_precheck  79   입력 규칙에서 막혔고 끝났다
//     done · complete          13   실제로 됐다
//
//   188건 중 165건이 끝난 일인데 "기다리라"고 답했다. `preparing` 은 곧 "다시
//   물어보라"는 뜻이므로, 소비자는 영영 오지 않을 답을 계속 물어본다. 문서는
//   `out_of_scope` 에 "재조회 불필요"를 못 박아 두었는데 이 자리엔 그 말이 없었다.
//
// ★원인은 단순하다. 같은 낱말을 다시 물어도 **그 낱말에 무슨 일이 있었는지 안 봤다.**
//   게이트 판정만 새로 계산해 매번 같은 답을 냈다. 큐에 답이 있는데 읽지 않았다.

import (
	"context"
	"strings"

	"github.com/rickyjoo73/kdb/internal/kdb/agents/gatekeeper"
)

// ResearchOutcome — 이 낱말의 발굴이 이미 어떻게 끝났는가. Found=false 면 처음 보는 낱말이다.
type ResearchOutcome struct {
	Found      bool
	Done       bool   // 큐가 끝났다(status='done')
	Resolution string // active · candidate · review_required · rejected_precheck · unknown
	Locale     string // complete · no_match · blocked_precheck · evidence_queued · unknown
}

// LastResearchOutcome — 같은 정규화 키로 이미 접수된 발굴의 마지막 처지를 읽는다.
func (s *Store) LastResearchOutcome(ctx context.Context, term string) ResearchOutcome {
	var o ResearchOutcome
	key := gatekeeper.NormalizedKey(term)
	if key == "" || s.Pool == nil {
		return o
	}
	var status, resolution, locale string
	err := s.Pool.QueryRow(ctx, `
SELECT COALESCE(status,''), COALESCE(resolution_status,''), COALESCE(locale_status,'')
  FROM kwave_entity_research_queue
 WHERE intake_normalized_key = $1
 ORDER BY created_at DESC
 LIMIT 1`, key).Scan(&status, &resolution, &locale)
	if err != nil {
		return o
	}
	o.Found = true
	o.Done = status == "done"
	o.Resolution, o.Locale = resolution, locale
	return o
}

// statusForFinishedResearch — 끝난 발굴을 소비자 말로 옮긴다.
//
// 두 번째 반환값이 false 면 아직 끝나지 않은 것이므로 호출부가 종전 답(preparing/new)을 쓴다.
// 여기서 지어내는 것은 없다 — 큐가 적어 둔 것을 그대로 옮긴다(D-37).
func statusForFinishedResearch(o ResearchOutcome) (string, string, bool) {
	if !o.Found || !o.Done {
		return "", "", false
	}
	switch {
	// 게이트가 기각했다. 같은 낱말을 다시 보내도 같은 답이다.
	case o.Resolution == "rejected_precheck":
		return "out_of_scope",
			"고유명사 입력 규칙에서 기각됨 — 재조회해도 준비되지 않습니다.", true
	// 사람 판단이 필요한 자리. 기다린다고 저절로 되지 않는다.
	case o.Resolution == "review_required" || o.Locale == "blocked_precheck":
		return "review",
			"검토가 필요한 항목 — 자동으로 채워지지 않습니다. 근거 URL 을 함께 정정신고로 보내주시면 재심합니다.", true
	// 대상은 맞는데 표기 근거를 못 찾았다. 이게 가장 많다(실측 86건).
	case o.Locale == "no_match":
		return "unfillable",
			"대상은 확인했으나 현지 표기 근거를 찾지 못했습니다 — 재조회해도 채워지지 않습니다. 근거 URL 을 정정신고로 보내주시면 재심합니다.", true
	}
	return "", "", false
}

// prepareAllMissingExhausted — 빈 locale 이 **전부** 소진됐는가.
//
// 하나라도 아직 가능성이 있으면 `preparing` 이 맞다 — 그 하나가 채워질 수 있기 때문이다.
// 전부 소진됐으면 "기다리라"는 답은 거짓이다. 이미 `Unavailable` 로 알리고는 있었지만,
// 소비자가 보는 것은 status 다. 필드에만 적고 status 로는 기다리라고 하면 안 읽힌다.
func prepareAllMissingExhausted(missing, unavailable []string) bool {
	if len(missing) == 0 || len(unavailable) == 0 {
		return false
	}
	out := make(map[string]bool, len(unavailable))
	for _, loc := range unavailable {
		out[strings.TrimSpace(loc)] = true
	}
	for _, loc := range missing {
		if !out[strings.TrimSpace(loc)] {
			return false
		}
	}
	return true
}

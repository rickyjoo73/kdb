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

	"github.com/rickyjoo73/kdb/internal/kdb"

	"github.com/rickyjoo73/kdb/internal/kdb/agents/gatekeeper"
)

// ResearchOutcome — 이 낱말의 발굴이 이미 어떻게 끝났는가. Found=false 면 처음 보는 낱말이다.
type ResearchOutcome struct {
	Found      bool
	Done       bool   // 큐가 끝났다(status='done')
	Resolution string // active · candidate · review_required · rejected_precheck · unknown
	Locale     string // complete · no_match · blocked_precheck · evidence_queued · unknown
	// Reason — 왜 그렇게 끝났는가(precheck_reason). **종결의 성격이 여기서 갈린다.**
	Reason string
}

// reasonsThatDoNotClose — 이 사유로 끝난 것은 `out_of_scope` 가 아니다.
//
// ★2026-09-16. 범위를 넓히고 문을 넷이나 열었는데도 조국·류현진·정의선이 계속
//   범위 밖으로 나갔다. 큐를 보니 사유가 `no_evidence_expired` — TTL 21일 만료였다.
//
//   그런데 그 종결의 **설계 의도가 정반대**다. Tombstoned 주석이 이미 그렇게 적어
//   두었다: "TTL 종결의 설계 의도 자체가 '종결하되 재요청 시 재발굴'이라, 여기서
//   막으면 종결이 곧 영구 차단이 된다."
//
//   prepare 만 그 규칙을 안 따르고 있었다. `rejected_precheck` 를 통째로
//   out_of_scope("재조회해도 준비되지 않습니다")로 옮겼기 때문이다. 명제가 다르다:
//     rejected_precheck + 입력규칙 위반  → 다시 물어도 같다 (out_of_scope 맞다)
//     rejected_precheck + 기한 내 못 찾음 → 다시 물으면 다시 찾는다 (아니다)
var reasonsThatDoNotClose = map[string]bool{
	"no_evidence_expired":  true, // 기한 내 근거를 못 찾았을 뿐 — 재요청 시 재발굴이 설계다
	"duplicate_live_request": true, // 다른 요청이 처리 중이었을 뿐이다
	"transient":            true,
}

// LastResearchOutcome — 같은 정규화 키로 이미 접수된 발굴의 마지막 처지를 읽는다.
func (s *Store) LastResearchOutcome(ctx context.Context, term string) ResearchOutcome {
	var o ResearchOutcome
	key := gatekeeper.NormalizedKey(term)
	if key == "" || s.Pool == nil {
		return o
	}
	// ★옛 규칙으로 내린 종결은 **만료된다** (2026-09-15).
	//
	//   `out_of_scope` 는 "재조회해도 준비되지 않습니다"라는 약속이다. 우리 규칙이
	//   바뀌면 지킬 수 없는 약속이고, 그대로 되풀이하면 바꾼 것이 소비자에게 안 닿는다.
	//
	//   실제로 그렇게 됐다. 범위를 넓히고(0143) 문서까지 고쳤는데, 소비자가 이재명
	//   (대통령)·차범근·서울대학교·더불어민주당을 물으면 전부 out_of_scope 였다 —
	//   옛 범위로 내린 종결이 그대로 재생되고 있었다. 소비자가 "문서와 실제가 다르다"고
	//   알려 줘서 알았다.
	//
	//   판본이 다르면 처음 보는 낱말처럼 다룬다(Found=false). 그러면 평소 발굴 경로가
	//   **지금 규칙으로** 다시 판단한다. 종결을 지어내지 않고, 되풀이하지도 않는다.
	var status, resolution, locale, ruleVersion, reason, queuedKo, queuedType string
	err := s.Pool.QueryRow(ctx, `
SELECT COALESCE(status,''), COALESCE(resolution_status,''), COALESCE(locale_status,''),
       COALESCE(precheck_rule_version,''), COALESCE(precheck_reason,''),
       COALESCE(entity_ko,''), COALESCE(requested_entity_type::text,'')
  FROM kwave_entity_research_queue
 WHERE intake_normalized_key = $1
 ORDER BY created_at DESC
 LIMIT 1`, key).Scan(&status, &resolution, &locale, &ruleVersion, &reason, &queuedKo, &queuedType)
	if err != nil {
		return o
	}
	// ★원장에서 **파생된** 종결은 원장이 바뀌면 같이 죽는다 (2026-09-16).
	//
	//   `existing_rejected_entity` 는 낱말에 대한 판단이 아니라 **그때 원장 상태에
	//   대한 진술**이다 — "같은 이름의 기각 행이 있다". 그 행이 되살아나면 진술이
	//   거짓이 되는데, 큐에 적힌 종결은 그대로 남아 요청을 계속 막는다.
	//
	//   실측(2026-09-16): 오세훈은 scope-reopen 이 candidate 로 되살린 뒤에도
	//   out_of_scope 였다. 판본은 최신이라 만료도 안 걸렸다. 되살린 것이
	//   소비자에게 닿지 않았다 — 되살리는 일 자체가 무의미해진다.
	//
	//   판본 만료와 다른 문제다. 판본은 **우리 규칙**이 바뀐 것이고, 이건
	//   **근거가 된 사실**이 바뀐 것이다. 사실이 바뀌면 다시 본다.
	if reason == "existing_rejected_entity" && queuedKo != "" && !s.rejectedTwinStillExists(ctx, queuedKo, queuedType) {
		return o
	}
	// ★종결하지 않는 사유면 처음 보는 낱말처럼 다룬다. 다시 발굴한다.
	if reasonsThatDoNotClose[reason] {
		return o
	}
	// 종결(기각·검토)은 판본을 탄다. 표기를 못 찾은 것(no_match)은 규칙이 아니라
	// 관측의 문제라 판본과 무관하게 유효하다 — 규칙이 바뀌어도 그 표기가 생기진 않는다.
	if resolution == "rejected_precheck" || resolution == "review_required" || locale == "blocked_precheck" {
		if ruleVersion != gatekeeper.IntakeRuleVersion {
			return o // 옛 판정 — 처음 보는 낱말처럼 다시 본다
		}
	}
	o.Found = true
	o.Done = status == "done"
	o.Resolution, o.Locale, o.Reason = resolution, locale, reason
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

// rejectedTwinStillExists — `existing_rejected_entity` 의 전제가 아직 참인가.
//
// 같은 유형 규칙은 CloseResolvedBacklog 가 그 종결을 내릴 때 쓴 것과 **같아야 한다**
// (I05 · 동명이인 분리). 둘이 다르면 한쪽이 닫은 것을 다른 쪽이 못 열거나 그 반대가 된다.
func (s *Store) rejectedTwinStillExists(ctx context.Context, ko, requestedType string) bool {
	if s.Pool == nil {
		return true // 못 보면 종전 판단을 그대로 둔다 — 모르는 것을 근거로 열지 않는다
	}
	var exists bool
	// ★tombstone 판정은 kdb.NotATombstoneSQL 한 자리에서 온다 (2026-09-16).
	//   여기만 빼먹으면 TTL·revert-term 기각이 다시 전제가 되어, 같은 세탁이
	//   이 경로로 되살아난다.
	err := s.Pool.QueryRow(ctx, `
SELECT EXISTS (
  SELECT 1 FROM kwave_entities e
   WHERE e.canonical_ko = $1 AND e.status = 'rejected'
     AND `+kdb.NotATombstoneSQL("e")+`
     AND (COALESCE(NULLIF($2,''),'unknown') = 'unknown'
          OR e.entity_type::text = $2))`, ko, requestedType).Scan(&exists)
	if err != nil {
		return true
	}
	return exists
}

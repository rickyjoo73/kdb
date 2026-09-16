package kdbapi

import "testing"

// ★"준비중"은 곧 "다시 물어보라"다 (2026-09-15).
//
//	실측으로 그렇게 답한 낱말의 절반 이상이 하루가 지나도 안 채워졌는데,
//	발굴 큐를 보면 이미 끝나 있었다 — done·no_match 86 · done·blocked_precheck 79.
//	188건 중 165건이 끝난 일인데 기다리라고 답했다.
func TestFinishedResearchIsNotReportedAsPreparing(t *testing.T) {
	cases := []struct {
		name string
		o    ResearchOutcome
		want string
		done bool
	}{
		{"처음 보는 낱말은 그대로 준비중", ResearchOutcome{}, "", false},
		{"큐가 아직 안 끝났으면 준비중", ResearchOutcome{Found: true, Resolution: "unknown", Locale: "unknown"}, "", false},
		{"게이트가 기각했다", ResearchOutcome{Found: true, Done: true, Resolution: "rejected_precheck", Locale: "blocked_precheck"}, "out_of_scope", true},
		{"사람 판단이 필요하다", ResearchOutcome{Found: true, Done: true, Resolution: "review_required", Locale: "blocked_precheck"}, "review", true},
		{"표기 근거를 못 찾았다", ResearchOutcome{Found: true, Done: true, Resolution: "candidate", Locale: "no_match"}, "unfillable", true},
		{"끝났고 채워졌으면 여기서 답하지 않는다", ResearchOutcome{Found: true, Done: true, Resolution: "active", Locale: "complete"}, "", false},
	}
	for _, c := range cases {
		got, why, done := statusForFinishedResearch(c.o)
		if done != c.done || got != c.want {
			t.Fatalf("%s: status=%q done=%v, want %q/%v", c.name, got, done, c.want, c.done)
		}
		if done && why == "" {
			t.Fatalf("%s: 종결 상태인데 이유가 비었다 — 상태만 주면 소비자가 다시 묻는다", c.name)
		}
	}
}

// 빈 칸이 하나라도 채워질 수 있으면 `preparing` 이 맞다. 전부 소진됐을 때만 종결이다.
func TestOnlyFullyExhaustedLocalesEndTheWait(t *testing.T) {
	cases := []struct {
		name                 string
		missing, unavailable []string
		want                 bool
	}{
		{"전부 소진", []string{"ja", "vi"}, []string{"ja", "vi"}, true},
		{"하나는 아직 가능", []string{"ja", "vi"}, []string{"ja"}, false},
		{"소진 정보 없음", []string{"ja"}, nil, false},
		{"빈 칸 없음", nil, []string{"ja"}, false},
		{"공백은 같은 locale 로 본다", []string{" ja "}, []string{"ja"}, true},
	}
	for _, c := range cases {
		if got := prepareAllMissingExhausted(c.missing, c.unavailable); got != c.want {
			t.Fatalf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// TTL 만료는 **종결이 아니다.** 그 종결의 설계 의도가 "재요청 시 재발굴" 이다.
//
// ★2026-09-16. 범위를 넓히고 문을 넷이나 연 뒤에도 조국·류현진·정의선이 계속
// out_of_scope 로 나갔다. 큐 사유가 `no_evidence_expired` 였다.
//
// Tombstoned 는 이미 같은 규칙을 적어 두고 있었다 — "TTL 종결의 설계 의도 자체가
// '종결하되 재요청 시 재발굴'이라, 여기서 막으면 종결이 곧 영구 차단이 된다."
// prepare 만 그 규칙을 안 따랐다: rejected_precheck 를 사유와 무관하게 통째로
// out_of_scope("재조회해도 준비되지 않습니다")로 옮겼기 때문이다.
//
// 명제가 다르다:
//
//	rejected_precheck + 입력규칙 위반   → 다시 물어도 같다   (out_of_scope 맞다)
//	rejected_precheck + 기한 내 못 찾음 → 다시 물으면 다시 찾는다 (아니다)
func TestTTLExpiryIsNotAClosedVerdict(t *testing.T) {
	for reason, shouldClose := range map[string]bool{
		"no_evidence_expired":    false, // 기한 내 못 찾았을 뿐
		"duplicate_live_request": false, // 다른 요청이 처리 중이었을 뿐
		"transient":              false,
		"term_not_proper_noun":   true, // 입력 규칙 위반 — 다시 물어도 같다
		"latin_passthrough":      true,
		"existing_rejected_entity": true,
	} {
		closes := !reasonsThatDoNotClose[reason]
		if closes != shouldClose {
			t.Errorf("%s: 종결 %v, 기대 %v", reason, closes, shouldClose)
		}
	}
}

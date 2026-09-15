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

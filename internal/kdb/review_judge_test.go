package kdb

import (
	"encoding/json"
	"strings"
	"testing"
)

// 스키마는 유효한 JSON 이고 여섯 판정을 다 담는다.
func TestReviewJudgeSchemaIsValid(t *testing.T) {
	var v map[string]any
	if err := json.Unmarshal(reviewJudgeSchema(), &v); err != nil {
		t.Fatalf("스키마가 JSON 이 아니다: %v", err)
	}
	for _, a := range []string{"register", "promote", "reopen", "reject", "foreign", "skip"} {
		if !strings.Contains(string(reviewJudgeSchema()), `"`+a+`"`) {
			t.Errorf("판정 %s 가 스키마에 없다", a)
		}
	}
}

// 프롬프트는 문맥과 기존 행을 보여 주고, 해외는 범위 밖이라고 말해야 한다.
func TestReviewJudgePromptCarriesContext(t *testing.T) {
	p := buildReviewJudgePrompt(reviewTerm{Ko: "주현웅", Requests: 19, Types: "person",
		Existing: "rejected|person|en=Joo Hyun-woong", Context: "내외경제TV=주현웅 기자"})
	for _, w := range []string{"주현웅", "19회", "rejected", "내외경제TV", "해외 대상은 범위 밖"} {
		if !strings.Contains(p, w) {
			t.Errorf("프롬프트에 %q 가 없다", w)
		}
	}
}

// 레인은 기본으로 켜져 있어야 한다 — 꺼져 있으면 review 가 다시 «무시» 가 된다.
func TestReviewJudgeOnByDefault(t *testing.T) {
	t.Setenv("KDB_REVIEW_JUDGE_ENABLED", "")
	if !ReviewJudgeEnabled() {
		t.Error("기본값이 꺼짐이다")
	}
	t.Setenv("KDB_REVIEW_JUDGE_ENABLED", "0")
	if ReviewJudgeEnabled() {
		t.Error("0 으로 꺼지지 않는다")
	}
}

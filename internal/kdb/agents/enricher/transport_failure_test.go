package enricher

import (
	"context"
	"errors"
	"testing"
)

// codex transport 실패(타임아웃/브레이커)는 "시도"로 기록되면 안 된다 — 그러면
// attempts 가 소진돼 채울 수 있는 필드가 조기 exhausted 된다. 실패 시 failed 만
// set 되고 tried 는 비어, 호출측(enrichOne)이 recordAttempt 를 건너뛴다.
func TestCascadeLocales_TransportFailureMarksFailedNotTried(t *testing.T) {
	a := newTestAgent(``, ``, errors.New("codex: timeout"))
	r := &record{ko: "박보검", entityType: "person", localeVals: map[string]string{}}
	missing := []string{"canonical_zh", "canonical_ja"}

	filled := map[string]string{}
	tried := map[string]string{}
	failed := map[string]bool{}
	skipped := map[string]bool{}
	a.cascadeLocales(context.Background(), nil, r, missing, filled, tried, failed, skipped)

	for _, f := range missing {
		if !failed[f] {
			t.Errorf("%s: transport 실패가 failed 에 기록돼야 함", f)
		}
		if _, ok := tried[f]; ok {
			t.Errorf("%s: 실패는 tried(시도)로 기록되면 안 됨", f)
		}
		if _, ok := filled[f]; ok {
			t.Errorf("%s: 실패인데 filled 로 기록됨", f)
		}
	}
}

// 호출 성공 시엔 tried 가 기록돼(설령 값을 못 받아 skip 이어도) attempts 가
// 정상 전진한다 — failed 는 비어야 한다.
func TestCascadeLocales_SuccessMarksTriedNotFailed(t *testing.T) {
	// 유효 JSON, spelling 없음(모델이 skip). nil pool 이라 write 는 안 되지만
	// tried 는 성공 분기에서 set 된다.
	a := newTestAgent(`{"spellings":[],"skipped":["zh","ja"]}`, ``, nil)
	r := &record{ko: "박보검", entityType: "person", localeVals: map[string]string{}}
	missing := []string{"canonical_zh", "canonical_ja"}

	filled := map[string]string{}
	tried := map[string]string{}
	failed := map[string]bool{}
	skipped := map[string]bool{}
	a.cascadeLocales(context.Background(), nil, r, missing, filled, tried, failed, skipped)

	for _, f := range missing {
		if tried[f] != "gpt-5.5" {
			t.Errorf("%s: 성공 호출은 tried=gpt-5.5 여야 함, got %q", f, tried[f])
		}
		if failed[f] {
			t.Errorf("%s: 성공인데 failed 로 기록됨", f)
		}
	}
}

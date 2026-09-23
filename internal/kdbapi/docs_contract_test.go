package kdbapi

import (
	"strings"
	"testing"
)

// 공개 문서가 **실제로 내보내는 것**을 말하는지 고정한다.
//
// ★2026-09-15. presslocale 이 "KDB 문서에는 이런 항목이 없다"고 했다. 맞는 말이었다 —
// include_absent · locale_absent · fill_hint · disambig 는 이미 응답에 실려 나가는데
// 문서에 한 줄도 없었다. 구현이 앞서가고 문서가 안 따라오면, 있는 기능을 아무도 못 쓴다.
// 필드를 더할 때 이 시험이 문서를 같이 고치게 만든다.
func TestDocsMentionShippedResponseFields(t *testing.T) {
	for _, want := range []string{
		"include_absent", "fill_hint", "transliterate", "translate_title",
		"no_value", "llm_only", "disambig",
		"suggestions", "suggestion_meta", "producer",
	} {
		if !strings.Contains(docsHTML, want) {
			t.Errorf("공개 문서에 %q 가 없다 — 내보내는데 아무도 못 쓴다", want)
		}
	}
}

// 제안 표기의 계약은 문서에 **반드시** 있어야 한다.
// 이게 없으면 보내는 쪽이 "제안이 검증값이 된다"고 오해한다.
//
// ★2026-09-24 오너 지시로 «빈 칸은 제안으로 채운다»가 됐다. 그래도 지켜야 하는 것은 같다 —
//
//	제안은 검증의 근거가 아니고, verified_only 에 섞이지 않으며, 소비자가 자기 제안을
//	골라낼 이름표(consumer-suggestion)가 붙는다.
func TestDocsStateSuggestionContract(t *testing.T) {
	for _, want := range []string{"검증의 근거가", "consumer-suggestion", "입증되면 교체", "verified_only"} {
		if !strings.Contains(docsHTML, want) {
			t.Errorf("문서에 제안 계약 %q 가 없다", want)
		}
	}
}

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

// 제안 표기의 계약 두 줄은 문서에 **반드시** 있어야 한다.
// 이게 없으면 보내는 쪽이 "제안이 검증값이 된다"고 오해한다.
func TestDocsStateSuggestionContract(t *testing.T) {
	for _, want := range []string{"승격 경로가 없습니다", "입증되면 교체", "verified_only"} {
		if !strings.Contains(docsHTML, want) {
			t.Errorf("문서에 제안 계약 %q 가 없다", want)
		}
	}
}

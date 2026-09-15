package kdb

// aijudge_hooks — 분류가 **근거로** 판정할 수 있게 표를 넘겨준다.
//
// ★2026-09-15, 운영자 지시 "가능한 gemma를 사용하지 않고 처리될 수 있도록".
//   분류는 이제 ① 소비자 type ② 위키데이터 P31 ③ 문맥 단서 순으로 근거를 보고,
//   근거가 말이 없을 때만(그리고 기본은 꺼짐) LLM 을 부른다.
//
// ★훅으로 넘기는 이유. `internal/kdb` 가 `aijudge` 를 임포트하므로 반대로 임포트하면
//   순환이다. codexcli 가 같은 이유로 같은 방식을 쓴다(그 파일 머리말 참조).

import (
	"strings"

	"github.com/rickyjoo73/kdb/internal/kdb/agents/gatekeeper"
	"github.com/rickyjoo73/kdb/internal/kdb/aijudge"
)

func init() {
	aijudge.P31Type = AnchorExpectedType
	aijudge.ContextCue = contextCueType
}

// contextCueType — 문맥에 유형 단서가 있으면 그 유형. 게이트키퍼의 표를 그대로 본다
// — 인입에서 쓰는 단서와 분류에서 쓰는 단서가 다르면 한쪽이 통과시킨 것을 다른 쪽이 막는다.
func contextCueType(text string) (string, bool) {
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" {
		return "", false
	}
	best, bestHits := "", 0
	for typ, cues := range gatekeeper.TypeCues() {
		hits := 0
		for _, c := range cues {
			if strings.Contains(text, strings.ToLower(c)) {
				hits++
			}
		}
		// 동수면 유형 이름 순으로 가른다 — 같은 입력에 같은 답이 나와야 한다.
		if hits > bestHits || (hits == bestHits && hits > 0 && typ < best) {
			best, bestHits = typ, hits
		}
	}
	if bestHits == 0 {
		return "", false
	}
	return best, true
}

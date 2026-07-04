package kdbapi

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/rickyjoo73/kdb/internal/kdb/agents"
	"github.com/rickyjoo73/kdb/internal/kdb/codexcli"
)

// match 기사맥락 판별(오너 방향): /v1/entities/match 는 기사 본문(source_text)에서 canonical_ko
// 를 부분문자열로 찾을 뿐, 그게 기사에서 실제로 그 K-엔티티를 가리키는지(일반어·오매칭·동명이인
// 아님)는 판단하지 않는다. disambiguate=true 소비자에 한해, 기사 본문 + 매칭 후보들을 gemma 가
// 한 번에 읽어 "실제로 그 정체로 언급된 후보"만 남긴다(핫패스 보호: 옵션·1회 호출).
//
// 예: 기사에 "환한 미소를 지으며"만 있고 미소(가수)를 안 가리키면 그 매칭을 제거(빈칸>오답).

type matchCand struct {
	ko, etype, disambig string
}

type matchJudgeInput struct {
	sourceText string
	cands      []matchCand
}

type matchJudgeResult struct {
	Valid []int `json:"valid"`
}

var matchJudgeSchema = []byte(`{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "valid": {"type": "array", "items": {"type": "integer"}}
  },
  "required": ["valid"]
}`)

func newMatchJudge() *agents.Base {
	r := codexcli.NewRunner().
		WithProvider(codexcli.RoleProvider("MATCHDISAMBIG", "gemma")).
		WithEffort(codexcli.RoleEffort("MATCHDISAMBIG", "low"))
	return agents.NewBase(r, agents.LLMRole{
		Role:   agents.Role("MatchDisambig"),
		Schema: matchJudgeSchema,
		BuildPrompt: func(in any) (string, error) {
			mi, ok := in.(matchJudgeInput)
			if !ok {
				return "", fmt.Errorf("matchdisambig: bad input")
			}
			return buildMatchJudgePrompt(mi), nil
		},
	})
}

func buildMatchJudgePrompt(mi matchJudgeInput) string {
	var b strings.Builder
	b.WriteString("당신은 한국 대중문화(K-콘텐츠) 기사에서 고유명사 매칭을 검증하는 판별기입니다.\n")
	b.WriteString("아래 기사에서, 후보 목록 중 '기사가 실제로 그 K-엔티티를 가리키는' 것만 valid 인덱스로 고르세요.\n")
	b.WriteString("제외: 일반어(예: 웃음 '미소')·무관·동명이의(기사 맥락이 다른 정체)·오매칭.\n\n")
	src := mi.sourceText
	if len([]rune(src)) > 1500 {
		src = string([]rune(src)[:1500])
	}
	b.WriteString("기사 본문:\n" + src + "\n\n후보:\n")
	for i, c := range mi.cands {
		line := strconv.Itoa(i) + ": " + c.ko + " (" + c.etype
		if c.disambig != "" {
			line += ", " + c.disambig
		}
		b.WriteString(line + ")\n")
	}
	b.WriteString("\n규칙(엄격):\n")
	b.WriteString("- 후보 이름이 기사에서 '그 인물/작품/그룹을 직접 지칭'할 때만 valid.\n")
	b.WriteString("- 제외: ①일반어(웃음 '미소', 참 '진심') ②부분매칭·다른 단어의 일부(예: '주연을 맡았다'의 '주연'은 인물 '이주연'이 아님, '진지한'의 '진'은 인물 '진'이 아님) ③동명이의(기사가 다른 정체) ④무관.\n")
	b.WriteString("- 애매하면 제외(빈칸>오답).\n")
	b.WriteString("JSON 한 개만: {\"valid\":[직접 지칭된 후보의 인덱스…]}\n")
	return b.String()
}

// disambiguateMatches — 기사 본문으로 매칭 후보를 gemma 가 검증해 유효한 것만 남긴다.
// 판별 실패(gemma 오류/타임아웃)면 원본 유지(핫패스 안전 — 판별 실패로 결과를 없애지 않음).
func disambiguateMatches(ctx context.Context, judge *agents.Base, sourceText string, ents []MatchedEntity) []MatchedEntity {
	if judge == nil || len(ents) == 0 {
		return ents
	}
	cands := make([]matchCand, len(ents))
	for i, e := range ents {
		cands[i] = matchCand{ko: e.KO, etype: e.EntityType, disambig: e.Disambig}
	}
	var res matchJudgeResult
	if err := judge.CallJSON(ctx, matchJudgeInput{sourceText: sourceText, cands: cands}, &res); err != nil {
		return ents // 판별 실패 → 원본 유지(안전)
	}
	valid := make(map[int]bool, len(res.Valid))
	for _, i := range res.Valid {
		valid[i] = true
	}
	out := make([]MatchedEntity, 0, len(ents))
	for i, e := range ents {
		if valid[i] {
			out = append(out, e)
		}
	}
	return out
}

package kdb

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
)

// 스키마는 유효한 JSON 이고, 판정 셋과 유형 목록을 그대로 담아야 한다.
func TestAnchorJudgeSchemaIsValid(t *testing.T) {
	var v map[string]any
	if err := json.Unmarshal(anchorJudgeSchema(), &v); err != nil {
		t.Fatalf("스키마가 JSON 이 아니다: %v", err)
	}
	s := string(anchorJudgeSchema())
	for _, w := range []string{"anchor_wrong", "type_wrong", "unclear", `"person"`, `"company"`} {
		if !strings.Contains(s, w) {
			t.Errorf("스키마에 %s 가 없다", w)
		}
	}
}

// 프롬프트는 양쪽 근거(우리 항목 · 위키데이터 항목)를 모두 보여 줘야 판정할 수 있다.
func TestAnchorJudgePromptCarriesBothSides(t *testing.T) {
	r := anchorJudgeRow{PersonAnchorMismatch: PersonAnchorMismatch{KO: "단발머리", EntityType: "song_album", QID: "Q17155555", Verdict: AnchorTypeMismatch}, EN: "Bob Girls"}
	ent := &wikidata.Entity{
		Labels:       map[string]string{"ko": "단발머리", "en": "Bob Girls"},
		SiteTitles:   map[string]string{"kowiki": "단발머리 (음악 그룹)"},
		Descriptions: map[string]string{"en": "South Korean girl group"},
	}
	p := buildAnchorJudgePrompt(r, ent)
	for _, w := range []string{"단발머리", "song_album", "Q17155555", "단발머리 (음악 그룹)", "South Korean girl group", "unclear"} {
		if !strings.Contains(p, w) {
			t.Errorf("프롬프트에 %q 가 없다", w)
		}
	}
}

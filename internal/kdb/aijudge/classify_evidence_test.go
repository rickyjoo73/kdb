package aijudge

import "testing"

// 분류는 **근거 순서대로** 정해진다. LLM 은 이 표 어디에도 없다.
//
// ★운영자 지시 (2026-09-15): "gemma가 장애가 많아서 분류에 적용하는 것은 문제가
// 많을 것 같은데, 가능한 gemma를 사용하지 않고 처리될 수 있도록 해줘."
// "우리가 가이드를 제대로 주면 기사 원문에서 분류해서 모두 올려줄 거야."
//
// 실측이 이 설계를 받친다 — 소비자 prepare 요청 1,508건 중 type 누락 **0%**.
func TestEvidenceOrderPutsTheConsumerFirst(t *testing.T) {
	p31 := func(q string) (string, bool) {
		if q == "Q5" {
			return "person", true
		}
		return "", false
	}
	cue := func(s string) (string, bool) { return "group", true }

	// ① 소비자 type 이 있으면 그것이다 — 기사 원문을 본 쪽의 판단이다.
	v := ClassifyFromEvidence(ClassifyInput{Ko: "이재명"}, "person", []string{"Q5"}, p31, cue)
	if v.EntityType != "person" || v.Source != "consumer-type" {
		t.Errorf("소비자 type 이 먼저여야 한다: %+v", v)
	}
	// 소비자가 위키데이터와 다르게 말해도 소비자를 따른다(문맥을 가진 쪽이다).
	v = ClassifyFromEvidence(ClassifyInput{Ko: "두산 베어스"}, "sports_team", []string{"Q5"}, p31, cue)
	if v.EntityType != "sports_team" {
		t.Errorf("소비자 type 을 P31 이 덮었다: %+v", v)
	}
	// ② 소비자 type 이 없으면 위키데이터 P31.
	v = ClassifyFromEvidence(ClassifyInput{Ko: "홍길동"}, "", []string{"Q5"}, p31, cue)
	if v.EntityType != "person" || v.Source != "wikidata-p31" {
		t.Errorf("P31 이 안 쓰였다: %+v", v)
	}
	// ③ 둘 다 없으면 문맥 단서.
	v = ClassifyFromEvidence(ClassifyInput{Ko: "무언가"}, "", nil, p31, cue)
	if v.Source != "context-cue" {
		t.Errorf("문맥 단서가 안 쓰였다: %+v", v)
	}
	// ④ 아무 근거도 없으면 **지어내지 않는다**(D-37).
	v = ClassifyFromEvidence(ClassifyInput{Ko: "무언가"}, "", nil, nil, nil)
	if v.Source != "" || v.EntityType != "unknown" || v.Confidence != 0 {
		t.Errorf("근거 없이 판정했다: %+v", v)
	}
}

// notes 의 소비자 힌트도 읽는다 — 인입 경로가 거기에 남긴다.
func TestConsumerHintInNotesIsRead(t *testing.T) {
	v := ClassifyFromEvidence(
		ClassifyInput{Ko: "기획재정부", Notes: "KDB candidate — 소비자 type힌트=government_body [cand-evidence:…"},
		"", nil, nil, nil)
	if v.EntityType != "government_body" || v.Source != "consumer-type" {
		t.Errorf("notes 힌트를 못 읽었다: %+v", v)
	}
}

// 새 유형 6종을 받아야 한다. 안 받으면 소비자가 보내도 unknown 이 된다.
func TestNewCivicTypesPassNormalization(t *testing.T) {
	for _, typ := range []string{
		"political_party", "government_body", "company",
		"organization", "sports_team", "school",
	} {
		if normalizeType(typ) != typ {
			t.Errorf("%s 가 유형으로 안 받아진다", typ)
		}
	}
	// 모르는 값은 통과시키지 않는다 — 원장 enum 에 없는 값이 들어가면 쓰기가 죽는다.
	for _, typ := range []string{"athlete", "politician", "나는없는유형", ""} {
		if normalizeType(typ) != "" {
			t.Errorf("%q 를 유형으로 받았다", typ)
		}
	}
}

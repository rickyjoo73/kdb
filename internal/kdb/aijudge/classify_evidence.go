package aijudge

// classify_evidence — **LLM 없이 유형을 정한다.**
//
// ★운영자 지시 (2026-09-15):
//   "gemma가 장애가 많아서 분류에 적용하는 것은 문제가 많을 것 같은데, 가능한 gemma를
//    사용하지 않고 처리될 수 있도록 해줘."
//   "우리가 가이드를 제대로 주면 기사 원문에서 분류해서 모두 올려줄 거야. 그것을
//    기반으로 하면 되지."
//
// ★역할이 갈린다.
//     소비자(GPT)  기사 원문을 읽고 고유명사를 뽑고 **유형을 정한다**
//     KDB          받은 고유명사에 **표기·근거·kid** 를 붙인다
//
//   판단 주체가 둘이면 어긋날 때 가릴 근거가 없다. 그리고 기사 원문은 소비자에게
//   있지 우리에게 없다 — 문맥을 가진 쪽이 분류하는 것이 맞다.
//
// ★실측이 이 설계를 받친다 (2026-09-15).
//     소비자 prepare 요청 1,508건 중 type 누락 **0%**
//   유형은 이미 온다. 우리가 다시 알아맞힐 이유가 없었다.
//
// ★순서. 위에서 답이 나오면 아래를 안 본다.
//     ① 소비자가 보낸 type          — 기사 문맥을 본 쪽의 판단
//     ② 위키데이터 P31              — 권위 출처가 말하는 종류
//     ③ 문맥 단서(typeCues)          — 결정적 규칙
//     ④ 없으면 unknown              — 지어내지 않는다(D-37)
//
//   LLM 은 여기 없다. 근거가 없으면 unknown 으로 두고 사람·다음 근거를 기다린다.

import (
	"regexp"
	"strings"
)

// EvidenceVerdict — 근거만으로 낸 판정.
type EvidenceVerdict struct {
	EntityType string
	Confidence float64
	Reason     string
	Source     string // consumer-type | wikidata-p31 | context-cue | (빈값=판정 못함)
}

// consumerTypeHint — notes 에 적힌 소비자 type 힌트를 읽는다.
// 인입 경로가 `소비자 type힌트=<type>` 로 남긴다.
var consumerTypeHint = regexp.MustCompile(`소비자 type힌트=([a-z_]+)`)

// ClassifyFromEvidence — 근거만으로 유형을 정한다. Source 가 비면 정하지 못한 것이다.
//
// p31 은 위키데이터 P31 QID 목록, p31Type 은 그 QID 를 우리 유형으로 옮기는 표
// (kdb.AnchorExpectedType). 패키지 의존을 만들지 않으려 함수로 받는다.
func ClassifyFromEvidence(
	in ClassifyInput,
	requestedType string,
	p31 []string,
	p31Type func(string) (string, bool),
	cueType func(text string) (string, bool),
) EvidenceVerdict {
	// ① 소비자가 보낸 type. 기사 원문을 본 쪽의 판단이라 가장 앞이다.
	if t := normalizeType(requestedType); t != "" {
		return EvidenceVerdict{
			EntityType: t, Confidence: 0.80,
			Reason: "소비자가 기사 문맥에서 정한 유형", Source: "consumer-type",
		}
	}
	if m := consumerTypeHint.FindStringSubmatch(in.Notes); len(m) == 2 {
		if t := normalizeType(m[1]); t != "" {
			return EvidenceVerdict{
				EntityType: t, Confidence: 0.75,
				Reason: "notes 의 소비자 type힌트", Source: "consumer-type",
			}
		}
	}

	// ② 위키데이터 P31. 권위 출처가 "이것이 무엇인가"를 말한다.
	if p31Type != nil {
		for _, q := range p31 {
			if t, ok := p31Type(q); ok && t != "" {
				return EvidenceVerdict{
					EntityType: t, Confidence: 0.85,
					Reason: "위키데이터 P31=" + q, Source: "wikidata-p31",
				}
			}
		}
	}

	// ③ 문맥 단서. 결정적 규칙이라 LLM 이 필요 없다.
	if cueType != nil {
		hay := strings.Join([]string{in.Ko, in.Notes, strings.Join(in.SearchHits, " ")}, " ")
		if t, ok := cueType(hay); ok && t != "" {
			return EvidenceVerdict{
				EntityType: t, Confidence: 0.55,
				Reason: "문맥 단서", Source: "context-cue",
			}
		}
	}

	// ④ 지어내지 않는다.
	return EvidenceVerdict{EntityType: "unknown", Confidence: 0, Reason: "근거 없음 — 판정 보류"}
}

// normalizeType — 받아들이는 유형만 통과시킨다. 모르는 값은 빈 문자열이다.
func normalizeType(t string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	switch t {
	case "person", "group", "show", "drama", "movie", "song_album", "agency",
		"channel_outlet", "brand_place", "event_tour", "character", "term",
		"political_party", "government_body", "company", "organization",
		"sports_team", "school":
		return t
	}
	return ""
}

// ── 훅 ────────────────────────────────────────────────────────────────────
//
// `internal/kdb` 가 이 패키지를 임포트하므로 **여기서 kdb 를 임포트하면 순환**이다
// (codexcli.go 머리말이 같은 이유로 같은 방식을 쓴다). 표를 가진 쪽이 주입한다.
var (
	// P31Type — 위키데이터 P31 QID → 우리 유형. kdb 가 init 에서 채운다.
	P31Type func(qid string) (string, bool)
	// ContextCue — 문맥 문자열 → 우리 유형. 게이트키퍼의 typeCues 를 쓴다.
	ContextCue func(text string) (string, bool)
)

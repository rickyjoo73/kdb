package kdb

// trust.go — entity 단위 "신뢰등급" (유형축과 직교하는 신뢰축).
//
// 필드별 source(Priority/Mark)를 entity 1개의 신뢰등급으로 모은다. 운영자/소비자가
// "이 항목을 얼마나 믿을 수 있나"를 한눈에 보게 하기 위함. source_priority.go 의
// Source 분류를 그대로 재사용해 일관성을 유지한다.
//
//	🔒 locked   = 운영자 확정/잠금 (불변)
//	🟢 verified = 권위 출처 보유 (TMDb·KOFIC·MusicBrainz·Naver·Wikidata·Wikipedia·정정검증)
//	🟡 observed = 현지 매체 표기 (≥2 합의 / 단일 관측) — 실사용 근거이나 권위 미교차
//	🔵 llm      = LLM 합성(codex/gemma)·출처 미상 — 실존 미검증, 참고용

type TrustCode string

const (
	TrustLocked   TrustCode = "locked"
	TrustVerified TrustCode = "verified"
	TrustObserved TrustCode = "observed"
	TrustLLM      TrustCode = "llm"
)

// Trust — UI 표시용 신뢰등급 메타.
type Trust struct {
	Code  TrustCode
	Label string
	Mark  string
	Class string // tailwind 배지 클래스
}

var (
	trustLocked   = Trust{TrustLocked, "운영자확정", "🔒", "bg-slate-800 text-white"}
	trustVerified = Trust{TrustVerified, "검증", "🟢", "bg-green-100 text-green-800"}
	trustObserved = Trust{TrustObserved, "매체확인", "🟡", "bg-amber-100 text-amber-800"}
	trustLLM      = Trust{TrustLLM, "LLM합성", "🔵", "bg-blue-100 text-blue-700"}
)

// sourceTrustRank — Source 를 신뢰 tier 로 (0 locked > 1 verified > 2 observed > 3 llm > 4 none).
func sourceTrustRank(s Source) int {
	if isRSSObservation(s) {
		return 2
	}
	switch s {
	case SourceOperatorLocked, SourceOperator:
		return 0
	case SourceTMDb, SourceKOFIC, SourceKMDb, SourceMusicBrainz, SourceNaverPeople,
		SourceCorrectionVerified, SourceWikidataLabel,
		SourceWikipediaLanglinks, SourceWikipediaSitelink, SourceWikipediaZhVariant:
		return 1
	case SourceMediaConsensus:
		return 2
	case SourceCodexFallback:
		return 3
	}
	return 4 // unknown / 빈 값
}

// TrustOf — operator_locked + canonical_*_source 들로 entity 신뢰등급 산출.
// 가장 강한(낮은 rank) 출처 기준. SQL 측(handlers 의 trust CASE)과 동일 규칙이어야 한다.
func TrustOf(operatorLocked bool, sources ...string) Trust {
	if operatorLocked {
		return trustLocked
	}
	best := 4
	for _, s := range sources {
		if s == "" {
			continue
		}
		if r := sourceTrustRank(Source(s)); r < best {
			best = r
		}
	}
	switch best {
	case 0:
		return trustLocked
	case 1:
		return trustVerified
	case 2:
		return trustObserved
	default: // 3, 4 → 미검증
		return trustLLM
	}
}

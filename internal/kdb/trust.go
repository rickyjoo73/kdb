package kdb

// trust.go — entity 단위 "신뢰등급" (유형축과 직교하는 신뢰축).
//
// 핵심 철학(운영자 방침): "확정"은 운영자 수동 잠금이 아니라 **현지 매체에서 반복
// 사용된 빈도**로 자동 결정한다. 같은 표기를 서로 다른 매체가 3곳 이상 쓰면 그것이
// 확실한 현지 통용 표기 = 확정. (틀린 정보면 매체 표기가 갱신되며 자동 교정된다.)
//
//	✅ 확정     = 현지 매체 ≥3곳 관측(=반복 확인) 또는 운영자 잠금(불변)
//	🟢 검증     = 권위 출처 보유(Wikidata·TMDb·MusicBrainz·KOFIC·정정검증) — 존재 교차확인
//	🟡 매체확인 = 현지 매체 1~2곳 관측 — 부분 근거
//	🔵 미검증   = LLM 합성(codex/gemma)·출처미상 — 실사용 미확인, 참고용
//
// 매체 ≥3(확정)이 권위출처(검증)보다 높은 이유: 이 DB 목적은 "현지에서 실제로 어떻게
// 쓰나"이고, 매체 반복 사용이 그 최종 근거다(권위 라벨은 현지 통용과 다를 수 있다).

type TrustCode string

const (
	TrustConfirmed TrustCode = "confirmed"
	TrustVerified  TrustCode = "verified"
	TrustObserved  TrustCode = "observed"
	TrustLLM       TrustCode = "llm"
)

// MediaConfirmThreshold — 확정으로 인정하는 현지 매체(distinct domain) 최소 수.
const MediaConfirmThreshold = 3

// Trust — UI 표시용 신뢰등급 메타.
type Trust struct {
	Code  TrustCode
	Label string
	Mark  string
	Class string // tailwind 배지 클래스
}

var (
	trustConfirmed = Trust{TrustConfirmed, "확정", "✅", "bg-emerald-600 text-white"}
	trustVerified  = Trust{TrustVerified, "검증", "🟢", "bg-green-100 text-green-800"}
	trustObserved  = Trust{TrustObserved, "매체확인", "🟡", "bg-amber-100 text-amber-800"}
	trustLLM       = Trust{TrustLLM, "미검증", "🔵", "bg-blue-100 text-blue-700"}
)

// hasAuthoritySource — 권위 출처(Priority 4~6: 권위API/Wikidata/Wikipedia/정정) 보유 여부.
func hasAuthoritySource(sources []string) bool {
	for _, s := range sources {
		switch Source(s) {
		case SourceTMDb, SourceKOFIC, SourceKMDb, SourceMusicBrainz, SourceNaverPeople,
			SourceCorrectionVerified, SourceWikidataLabel,
			SourceWikipediaLanglinks, SourceWikipediaSitelink, SourceWikipediaZhVariant:
			return true
		}
	}
	return false
}

// TrustOf — operator_locked + 현지 매체 관측 수(distinct domain) + canonical 출처들로
// entity 신뢰등급 산출. SQL 측(handlers 의 trust CASE)과 동일 규칙이어야 한다.
//
//	mediaDomains: 이 entity 를 관측한 서로 다른 매체 도메인 수.
func TrustOf(operatorLocked bool, mediaDomains int, sources ...string) Trust {
	if operatorLocked || mediaDomains >= MediaConfirmThreshold {
		return trustConfirmed // ✅ 반복 확인 = 확정
	}
	if hasAuthoritySource(sources) {
		return trustVerified // 🟢 권위 교차확인
	}
	if mediaDomains >= 1 {
		return trustObserved // 🟡 1~2 매체
	}
	// canonical 출처에 매체(media-consensus/rss)가 있으나 관측 카운트 0인 경계도 매체확인.
	for _, s := range sources {
		if r := sourceTrustRank(Source(s)); r == 2 {
			return trustObserved
		}
	}
	return trustLLM // 🔵 출처미상/LLM
}

// sourceTrustRank — Source 를 rank 로 (0 lock/operator, 1 authority, 2 media, 3 llm, 4 none).
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
	return 4
}

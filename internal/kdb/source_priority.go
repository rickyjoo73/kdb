// Package kdb — K-content Entity DB platform.
//
// 운영자 정공법 (docs/ENTITY_DB_PLATFORM.md):
//   - 미래 도메인 = kdb.aiinplanet.com.
//   - 현재 구현 = mediafine repo 내부 internal/kdb/ 모듈.
//   - 분리 시점 = mediafine 성공 후 도커 컨테이너 분리 (코드 거의 무변경).
//   - 현지 매체 합의 = 최우선 (operator-locked 다음).
//   - API 출처 = backbone, media-consensus 가 도착하면 덮임.
//
// source_priority.go — canonical_X 값의 출처 (source) enum + 덮어쓰기 룰.
package kdb

// Source — canonical_X_source 컬럼에 박히는 enum 값.
// 낮은 priority 숫자 = 높은 우선순위.
type Source string

const (
	// SourceOperatorLocked — operator_locked=true 또는 운영자 수동 입력.
	// 다른 어떤 source 도 덮을 수 없는 불변.
	SourceOperatorLocked Source = "operator-locked"

	// SourceOperator — 운영자가 admin UI 에서 직접 pick / 입력. operator_locked
	// 와 의미상 동일 (별도 lock 없이 명시 입력). priority 1 동급.
	SourceOperator Source = "operator"

	// SourceMediaConsensus — 현지 매체 ≥2 합의로 자동 promote.
	// 운영자 정공법: "현지기준이 우선순위가 맞도록" — API 출처를 덮음.
	SourceMediaConsensus Source = "media-consensus"

	// SourceWikidataLabel — Wikidata wbgetentities labels 직접 제공.
	SourceWikidataLabel Source = "wikidata-label"

	// SourceWikipediaLanglinks — Wikipedia 다국어 article 제목 (Stage C).
	SourceWikipediaLanglinks Source = "wikipedia-langlinks"

	// SourceWikipediaSitelink — Wikipedia sitelink fallback (Stage A).
	SourceWikipediaSitelink Source = "wikipedia-sitelink"

	// SourceWikipediaZhVariant — zh.wikipedia.org ?variant=zh-tw (Stage B).
	// zh_hant 컬럼 전용.
	SourceWikipediaZhVariant Source = "wikipedia-zh-variant"

	// SourceUnknown — 마이그레이션 default 또는 source 미지정.
	// priority 최저 (어떤 명시 source 든 덮음).
	SourceUnknown Source = "unknown"
)

// Priority — Source enum 의 정렬 가중치 (낮을수록 우선).
func Priority(s Source) int {
	switch s {
	case SourceOperatorLocked, SourceOperator:
		return 1
	case SourceMediaConsensus:
		return 2
	case SourceWikidataLabel:
		return 3
	case SourceWikipediaLanglinks:
		return 4
	case SourceWikipediaSitelink, SourceWikipediaZhVariant:
		return 5
	}
	return 99 // unknown / 빈 값
}

// ShouldReplace — 기존 source 의 값을 새 source 의 값으로 덮어쓸지 결정.
//
// 운영자 정공법 (docs/ENTITY_DB_PLATFORM.md §3.3):
//   - operator-locked / operator 는 어떤 source 도 못 덮음.
//   - 새 priority < 현 priority (더 우선)  → 덮음.
//   - 같은 priority + same value           → no-op (idempotent).
//   - 같은 priority + different value      → drift (덮지 않음, 호출자가 audit/review 처리).
//   - 새 priority > 현 priority            → 무시.
//
// 반환:
//
//	replace=true  → caller 는 UPDATE 수행.
//	replace=false → caller 는 skip (또는 drift 시 audit).
//	drift=true    → 같은 priority 인데 값이 다름. caller 는 review 큐에 알림 권장.
func ShouldReplace(current Source, currentVal string, incoming Source, incomingVal string) (replace bool, drift bool) {
	// operator-locked 보호
	if current == SourceOperatorLocked || current == SourceOperator {
		return false, false
	}
	cur := Priority(current)
	nw := Priority(incoming)
	if nw < cur {
		return true, false
	}
	if nw == cur {
		if currentVal == incomingVal {
			return false, false
		}
		// 같은 출처에서 값 변동 = drift. 자동 덮음 X.
		return false, true
	}
	return false, false
}

// SourcesByPriorityAsc — UI / audit 표시용 정렬 helper.
func SourcesByPriorityAsc() []Source {
	return []Source{
		SourceOperatorLocked,
		SourceOperator,
		SourceMediaConsensus,
		SourceWikidataLabel,
		SourceWikipediaLanglinks,
		SourceWikipediaSitelink,
		SourceWikipediaZhVariant,
		SourceUnknown,
	}
}

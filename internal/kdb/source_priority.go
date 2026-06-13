// Package kdb — K-content Entity DB platform.
//
// 운영자 정공법:
//   - 단독 백엔드 entity DB 서비스. 클라이언트(외부 사이트)가 API 로 데이터 받음.
//   - 현지 매체 표기 = 최우선 (operator-locked 다음).
//   - 권위 API(TMDb/KOFIC/MusicBrainz 등) = 매체 합의 없을 때 보강.
//   - Wikidata = bootstrap 보충. 매체 표기 도착하면 덮임.
//
// source_priority.go — canonical_X 값의 출처 (source) enum + 마크 + 덮어쓰기 룰.
package kdb

// Source — canonical_X_source 컬럼에 박히는 enum 값.
// 낮은 priority 숫자 = 높은 우선순위.
type Source string

const (
	// SourceOperatorLocked — operator_locked=true 또는 운영자 수동 입력.
	// 다른 어떤 source 도 덮을 수 없는 불변.
	SourceOperatorLocked Source = "operator-locked"

	// SourceOperator — 운영자가 admin UI 에서 직접 pick / 입력.
	SourceOperator Source = "operator"

	// SourceMediaConsensus — 현지 매체 ≥ 2 합의로 자동 promote (L).
	SourceMediaConsensus Source = "media-consensus"

	// SourceRSSObservation — 단일 매체 표기 (l). 1건 등장.
	// rss-observation:<domain> 형태로 prefix 매칭.
	SourceRSSObservation Source = "rss-observation"

	// 권위 API (O) — entity_type 매칭 시 보강.
	SourceTMDb        Source = "tmdb"
	SourceKOFIC       Source = "kofic"
	SourceKMDb        Source = "kmdb"
	SourceMusicBrainz Source = "musicbrainz"
	SourceNaverPeople Source = "naver-people"

	// SourceCorrectionVerified — 클라이언트 정정 신고가 codex 검증 + 클라 확인을
	// 통과해 반영된 값 (C). 교차검증 등급(권위 API 와 동급, prio 4).
	SourceCorrectionVerified Source = "correction-verified"

	// SourceWikidataLabel — Wikidata wbgetentities labels (W).
	SourceWikidataLabel Source = "wikidata-label"

	// SourceWikipediaLanglinks — Wikipedia langlinks (w, W 보조).
	SourceWikipediaLanglinks Source = "wikipedia-langlinks"

	// SourceWikipediaSitelink — Wikipedia sitelink fallback (w).
	SourceWikipediaSitelink Source = "wikipedia-sitelink"

	// SourceWikipediaZhVariant — zh.wikipedia ?variant=zh-tw. zh-hant 전용 (w).
	SourceWikipediaZhVariant Source = "wikipedia-zh-variant"

	// SourceCodexFallback — codex-bridge LLM 합성 (마지막 보루).
	SourceCodexFallback Source = "codex-fallback"

	// SourceUnknown — 마이그레이션 default 또는 source 미지정.
	SourceUnknown Source = "unknown"
)

// rssObservationPrefix — 단일 매체 source 는 "rss-observation:<domain>" 형식.
// startsWith 비교를 위한 헬퍼.
const rssObservationPrefix = "rss-observation"

// isRSSObservation — source 가 rss-observation:domain 형식인지.
func isRSSObservation(s Source) bool {
	if len(s) < len(rssObservationPrefix) {
		return false
	}
	return string(s[:len(rssObservationPrefix)]) == rssObservationPrefix
}

// Priority — Source enum 의 정렬 가중치. 낮을수록 우선.
//
// 운영자 정공법: 현지 매체 표기 > 권위 API > Wikidata > Wikipedia 보조.
func Priority(s Source) int {
	if isRSSObservation(s) {
		return 3
	}
	switch s {
	case SourceOperatorLocked, SourceOperator:
		return 1
	case SourceMediaConsensus:
		return 2
	// rss-observation:* → 3 (위 isRSSObservation 분기)
	case SourceTMDb, SourceKOFIC, SourceKMDb, SourceMusicBrainz, SourceNaverPeople,
		SourceCorrectionVerified:
		return 4
	case SourceWikidataLabel:
		return 5
	case SourceWikipediaLanglinks, SourceWikipediaSitelink, SourceWikipediaZhVariant:
		return 6
	case SourceCodexFallback:
		return 7
	}
	return 99 // unknown / 빈 값
}

// Mark — 운영자 UI 에 표시할 한 글자 마크.
//
//	🔒 = operator lock / operator
//	L  = media-consensus (현지 매체 합의)
//	l  = rss-observation (단일 매체)
//	O  = 권위 API (TMDb/KOFIC/KMDb/MusicBrainz/Naver)
//	W  = Wikidata
//	w  = Wikipedia 보조
//	?  = codex-fallback / unknown
func Mark(s Source) string {
	if isRSSObservation(s) {
		return "l"
	}
	switch s {
	case SourceOperatorLocked, SourceOperator:
		return "🔒"
	case SourceMediaConsensus:
		return "L"
	case SourceCorrectionVerified:
		return "C"
	case SourceTMDb, SourceKOFIC, SourceKMDb, SourceMusicBrainz, SourceNaverPeople:
		return "O"
	case SourceWikidataLabel:
		return "W"
	case SourceWikipediaLanglinks, SourceWikipediaSitelink, SourceWikipediaZhVariant:
		return "w"
	case SourceCodexFallback:
		return "?"
	}
	return ""
}

// MarkClass — Mark 에 대응하는 tailwind 색 클래스 (admin UI 뱃지용).
func MarkClass(s Source) string {
	if isRSSObservation(s) {
		return "bg-emerald-50 text-emerald-700 border border-emerald-200"
	}
	switch s {
	case SourceOperatorLocked, SourceOperator:
		return "bg-slate-900 text-white"
	case SourceMediaConsensus:
		return "bg-emerald-100 text-emerald-800 font-semibold"
	case SourceCorrectionVerified:
		return "bg-teal-100 text-teal-800"
	case SourceTMDb, SourceKOFIC, SourceKMDb, SourceMusicBrainz, SourceNaverPeople:
		return "bg-amber-100 text-amber-800"
	case SourceWikidataLabel:
		return "bg-white text-slate-600 border border-slate-300"
	case SourceWikipediaLanglinks, SourceWikipediaSitelink, SourceWikipediaZhVariant:
		return "bg-slate-100 text-slate-500"
	case SourceCodexFallback:
		return "bg-purple-50 text-purple-700"
	}
	return "bg-slate-50 text-slate-400"
}

// ShouldReplace — 기존 source 의 값을 새 source 의 값으로 덮어쓸지 결정.
//
// 운영자 정공법:
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
		SourceRSSObservation,
		SourceTMDb,
		SourceKOFIC,
		SourceKMDb,
		SourceMusicBrainz,
		SourceNaverPeople,
		SourceCorrectionVerified,
		SourceWikidataLabel,
		SourceWikipediaLanglinks,
		SourceWikipediaSitelink,
		SourceWikipediaZhVariant,
		SourceCodexFallback,
		SourceUnknown,
	}
}

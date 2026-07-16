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

	// SourceLocalUsage — 현지사용: 각 언어로 현지에서 실제 쓰이는 표기를 언어별 검색으로
	// 발견·확정(잠금)한 값. KDB 핵심 권위 — 검색그라운드 현지표기. 운영자 수동확정
	// (operator-locked)과 동일한 최상위 티어(priority 1). 단 ShouldReplace 가 수동
	// operator-locked 값은 보호하므로, 자동 발견이 사람의 잠금을 덮지는 않는다.
	SourceLocalUsage Source = "local-usage"

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

	// SourceNetflix/SourceDisney — 공식 OTT distributor 의 지역별 공식 현지제목 (O, prio 4,
	// 권위 API 동급). 작품(drama/show/movie)만. ID-앵커링으로 확보(internal/kdb/ott.go).
	SourceNetflix Source = "netflix"
	SourceDisney  Source = "disney"

	// SourceCorrectionVerified — 클라이언트 정정 신고가 codex 검증 + 클라 확인을
	// 통과해 반영된 값 (C). 교차검증 등급(권위 API 와 동급, prio 4).
	SourceCorrectionVerified Source = "correction-verified"

	// SourceITunes — Apple iTunes 국가별 스토어의 공식 트랙/앨범 제목 (권위 API, prio 4).
	// song_album 의 현지표기를 ID-앵커(trackId)+제목 confirm 으로 확보(internal/kdb/itunes/).
	SourceITunes Source = "itunes"

	// SourceDiscogs — Discogs 음악 DB의 공식 release 제목 (권위 API, prio 4). iTunes 폴백 +
	// release/artist 앵커. confirm-only(internal/kdb/discogs/).
	SourceDiscogs Source = "discogs"

	// 음악/엔터 공식 페이지·DSP/카탈로그 앵커 후보. 자동 수집 가능 여부는 source_policy.go 가 결정한다.
	SourceCubeOfficial Source = "cube-official"
	SourceWarnerJapan  Source = "warner-japan"
	SourceMelon        Source = "melon"
	SourceGenie        Source = "genie"
	SourceBugs         Source = "bugs"
	SourceVibe         Source = "vibe"
	SourceQQMusic      Source = "qq-music"
	SourceNetEaseMusic Source = "netease-music"
	SourceTencentMusic Source = "tencent-music"
	SourceSpotify      Source = "spotify"
	SourceKOMCA        Source = "komca"

	// 새 공식/공식페이지 앵커 후보. 자동 수집 가능 여부는 source_policy.go 가 결정한다.
	SourceOfficialPage        Source = "official-page"
	SourceBroadcasterOfficial Source = "broadcaster-official"
	SourceOTTOfficial         Source = "ott-official"
	SourceTVING               Source = "tving"
	SourceWavve               Source = "wavve"
	SourceWatcha              Source = "watcha"
	SourceCoupangPlay         Source = "coupang-play"
	SourceViki                Source = "viki"
	SourceLollapalooza        Source = "lollapalooza"
	SourceYES24LiveHall       Source = "yes24-livehall"

	// 보조/탐색/커뮤니티 소스. source 로는 기록하지만 candidate 자동승급 앵커는 아니다.
	SourceTVMaze          Source = "tvmaze"
	SourceNaverEncyc      Source = "naver-encyc"
	SourceNaverSearch     Source = "naver-search"
	SourceKakaoSearch     Source = "kakao-search"
	SourceYouTubeOfficial Source = "youtube-official"
	SourceNamuWiki        Source = "namuwiki"
	SourceBaiduBaike      Source = "baidu-baike"
	SourceGeminiSearch    Source = "gemini-search"

	// SourceWikidataLabel — Wikidata wbgetentities labels (W).
	SourceWikidataLabel Source = "wikidata-label"

	// SourceWikipediaLanglinks — Wikipedia langlinks (w, W 보조).
	SourceWikipediaLanglinks Source = "wikipedia-langlinks"

	// SourceWikipediaSitelink — Wikipedia sitelink fallback (w).
	SourceWikipediaSitelink Source = "wikipedia-sitelink"

	// SourceWikipediaZhVariant — zh.wikipedia ?variant=zh-tw. zh-hant 전용 (w).
	SourceWikipediaZhVariant Source = "wikipedia-zh-variant"

	// SourceLocalSearch — LocalFill/QA 워커의 약증거(만장일치 미달) 검색보강값. 빈칸만
	// 채우는 잠정 표기. 검색그라운드라 codex-fallback(LLM 합성)보다는 우선하되, 권위
	// 소스(media/api/wikidata/wiki)는 이를 업그레이드한다. 강증거는 local-usage 로 승급.
	SourceLocalSearch Source = "local-search"

	// SourceRomanization — 한국 인물의 Latin locale(vi/es/id/pt_br) 표기를 canonical_en
	// (검증된 로마자)에서 결정적 재속성한 값(외부호출 0). 한국 인명은 라틴문자권에서 사실상
	// 영문 로마자와 동일 표기. codex 합성보다 신뢰(실 로마자)지만 현지매체 실사용은 아니므로
	// prio 7(media-consensus/권위/wiki 가 업그레이드), verified tier 제외. internal/kdb/romanize.go.
	SourceRomanization Source = "romanization"

	// SourceOpenCC — 보유 zh(간체)↔zh_hant(번체)를 OpenCC 로 결정적 변환한 값(외부호출 0).
	// 한자 스크립트 변환은 결정적이라 신뢰(원본 zh/zh_hant 의 신뢰 승계)지만 현지매체 실사용은
	// 아니므로 prio 7. internal/kdb/opencc_convert.go.
	SourceOpenCC Source = "opencc"

	// SourceMyDramaList — MyDramaList 의 "Also Known As" 에서 추출한 공식/통용 현지제목
	// (커뮤니티 DB tier, prio 7 = codex-fallback 만 교체, 권위소스 불가침). 작품
	// (drama/show/movie)만. 한국어 원제 앵커 + 문자셋 결정적 탐지로 확보(internal/kdb/mdl.go).
	// 커뮤니티 편집 출처라 verified tier 에서는 제외(verified_only 소비자엔 노출 안 함).
	SourceMyDramaList Source = "mydramalist"

	// SourceGTranslate — Google Cloud Translation 기계번역 폴백 (마지막 보루, 오너 방침
	// 2026-07-16: 공식소스·검색그라운드가 모두 못 채운 빈칸은 기계번역이라도 채워 서빙).
	// codex-fallback 과 동급 최하위 — 모든 상위 소스가 업그레이드하고, verified tier 제외.
	SourceGTranslate Source = "gtranslate"

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
	case SourceOperatorLocked, SourceOperator, SourceLocalUsage:
		return 1
	case SourceMediaConsensus:
		return 2
	// rss-observation:* → 3 (위 isRSSObservation 분기)
	case SourceTMDb, SourceKOFIC, SourceKMDb, SourceMusicBrainz, SourceNaverPeople,
		SourceCorrectionVerified, SourceNetflix, SourceDisney, SourceITunes, SourceDiscogs,
		SourceCubeOfficial, SourceWarnerJapan, SourceMelon, SourceGenie, SourceBugs, SourceVibe,
		SourceQQMusic, SourceNetEaseMusic, SourceTencentMusic, SourceSpotify, SourceKOMCA,
		SourceOfficialPage, SourceBroadcasterOfficial, SourceOTTOfficial, SourceTVING, SourceWavve,
		SourceWatcha, SourceCoupangPlay, SourceViki, SourceLollapalooza, SourceYES24LiveHall:
		return 4
	case SourceWikidataLabel:
		return 5
	case SourceWikipediaLanglinks, SourceWikipediaSitelink, SourceWikipediaZhVariant:
		return 6
	case SourceLocalSearch, SourceMyDramaList, SourceRomanization, SourceOpenCC,
		SourceTVMaze, SourceNaverEncyc, SourceNaverSearch, SourceKakaoSearch,
		SourceYouTubeOfficial, SourceNamuWiki, SourceBaiduBaike, SourceGeminiSearch:
		return 7 // 검색그라운드/커뮤니티 잠정 — codex 합성보다 우선, 권위소스는 업그레이드
	case SourceGTranslate, SourceCodexFallback:
		return 8
	}
	return 99 // unknown / 빈 값
}

// Mark — 운영자 UI 에 표시할 한 글자 마크.
//
//	🔒 = operator lock / operator
//	★  = local-usage (검색그라운드 현지표기·확정)
//	L  = media-consensus (현지 매체 합의)
//	l  = rss-observation (단일 매체)
//	O  = 권위 API (TMDb/KOFIC/KMDb/MusicBrainz/Naver)
//	W  = Wikidata
//	w  = Wikipedia 보조
//	s  = local-search (검색 잠정·약증거)
//	?  = codex-fallback / unknown
func Mark(s Source) string {
	if isRSSObservation(s) {
		return "l"
	}
	switch s {
	case SourceOperatorLocked, SourceOperator:
		return "🔒"
	case SourceLocalUsage:
		return "★"
	case SourceMediaConsensus:
		return "L"
	case SourceCorrectionVerified:
		return "C"
	case SourceTMDb, SourceKOFIC, SourceKMDb, SourceMusicBrainz, SourceNaverPeople,
		SourceNetflix, SourceDisney, SourceITunes, SourceDiscogs, SourceSpotify, SourceKOMCA,
		SourceCubeOfficial, SourceWarnerJapan, SourceMelon, SourceGenie, SourceBugs, SourceVibe,
		SourceQQMusic, SourceNetEaseMusic, SourceTencentMusic, SourceOfficialPage,
		SourceBroadcasterOfficial, SourceOTTOfficial, SourceTVING, SourceWavve, SourceWatcha,
		SourceCoupangPlay, SourceViki, SourceLollapalooza, SourceYES24LiveHall:
		return "O"
	case SourceWikidataLabel:
		return "W"
	case SourceWikipediaLanglinks, SourceWikipediaSitelink, SourceWikipediaZhVariant:
		return "w"
	case SourceLocalSearch, SourceNaverSearch, SourceKakaoSearch, SourceGeminiSearch:
		return "s"
	case SourceMyDramaList, SourceTVMaze, SourceNamuWiki, SourceNaverEncyc, SourceYouTubeOfficial,
		SourceBaiduBaike:
		return "m"
	case SourceRomanization:
		return "r"
	case SourceOpenCC:
		return "o"
	case SourceGTranslate:
		return "g"
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
	case SourceLocalUsage:
		return "bg-indigo-600 text-white"
	case SourceMediaConsensus:
		return "bg-emerald-100 text-emerald-800 font-semibold"
	case SourceCorrectionVerified:
		return "bg-teal-100 text-teal-800"
	case SourceTMDb, SourceKOFIC, SourceKMDb, SourceMusicBrainz, SourceNaverPeople,
		SourceNetflix, SourceDisney, SourceITunes, SourceDiscogs, SourceSpotify, SourceKOMCA,
		SourceCubeOfficial, SourceWarnerJapan, SourceMelon, SourceGenie, SourceBugs, SourceVibe,
		SourceQQMusic, SourceNetEaseMusic, SourceTencentMusic, SourceOfficialPage,
		SourceBroadcasterOfficial, SourceOTTOfficial, SourceTVING, SourceWavve, SourceWatcha,
		SourceCoupangPlay, SourceViki, SourceLollapalooza, SourceYES24LiveHall:
		return "bg-amber-100 text-amber-800"
	case SourceWikidataLabel:
		return "bg-white text-slate-600 border border-slate-300"
	case SourceWikipediaLanglinks, SourceWikipediaSitelink, SourceWikipediaZhVariant:
		return "bg-slate-100 text-slate-500"
	case SourceLocalSearch, SourceNaverSearch, SourceKakaoSearch, SourceGeminiSearch:
		return "bg-sky-50 text-sky-700"
	case SourceMyDramaList, SourceTVMaze, SourceNamuWiki, SourceNaverEncyc, SourceYouTubeOfficial,
		SourceBaiduBaike:
		return "bg-rose-50 text-rose-700"
	case SourceRomanization:
		return "bg-cyan-50 text-cyan-700"
	case SourceOpenCC:
		return "bg-teal-50 text-teal-700"
	case SourceGTranslate:
		return "bg-orange-50 text-orange-700"
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
		SourceLocalUsage,
		SourceMediaConsensus,
		SourceRSSObservation,
		SourceTMDb,
		SourceKOFIC,
		SourceKMDb,
		SourceMusicBrainz,
		SourceNaverPeople,
		SourceCorrectionVerified,
		SourceNetflix,
		SourceDisney,
		SourceITunes,
		SourceDiscogs,
		SourceCubeOfficial,
		SourceWarnerJapan,
		SourceMelon,
		SourceGenie,
		SourceBugs,
		SourceVibe,
		SourceQQMusic,
		SourceNetEaseMusic,
		SourceTencentMusic,
		SourceSpotify,
		SourceKOMCA,
		SourceOfficialPage,
		SourceBroadcasterOfficial,
		SourceOTTOfficial,
		SourceTVING,
		SourceWavve,
		SourceWatcha,
		SourceCoupangPlay,
		SourceViki,
		SourceLollapalooza,
		SourceYES24LiveHall,
		SourceWikidataLabel,
		SourceWikipediaLanglinks,
		SourceWikipediaSitelink,
		SourceWikipediaZhVariant,
		SourceLocalSearch,
		SourceNaverSearch,
		SourceKakaoSearch,
		SourceMyDramaList,
		SourceTVMaze,
		SourceNaverEncyc,
		SourceYouTubeOfficial,
		SourceNamuWiki,
		SourceBaiduBaike,
		SourceGeminiSearch,
		SourceRomanization,
		SourceOpenCC,
		SourceGTranslate,
		SourceCodexFallback,
		SourceUnknown,
	}
}

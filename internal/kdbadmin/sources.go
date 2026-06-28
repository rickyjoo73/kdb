package kdbadmin

// sources.go — KDB 가 다국어 표기를 확보하는 모든 데이터 소스 인벤토리(관리자 settings 하단 표시).
// 오너 지시(2026-06-28): 우리가 확보할 수 있는 모든 관련 소스를 한곳에 가시화.
// source_priority.go 의 source enum + 확보 로드맵(docs/runbooks/KDB_ACQUIRE_ROADMAP_20260628.md) 동기.

// sourceInfo — 소스 한 행(표시용).
type sourceInfo struct {
	Name     string // 표시명
	Key      string // canonical_<loc>_source 값 / 식별자
	Provides string // 무엇을(어떤 타입·locale) 제공하나
	Tier     int    // source_priority(낮을수록 우선)
	Status   string // live | new | exp | plan
	Bulk     string // 벌크 안전성
	Note     string
}

// sourceStatusBadge — Status → (라벨, tailwind class).
func sourceStatusBadge(st string) (string, string) {
	switch st {
	case "live":
		return "연동", "bg-emerald-100 text-emerald-800"
	case "new":
		return "신규", "bg-cyan-100 text-cyan-800"
	case "exp":
		return "실험", "bg-amber-100 text-amber-800"
	case "plan":
		return "계획", "bg-slate-100 text-slate-500"
	}
	return st, "bg-slate-100 text-slate-500"
}

// sourceInventory — 전체 데이터 소스 목록(우선순위 tier 순). 새 소스 추가 시 여기에 등록.
func sourceInventory() []sourceInfo {
	return []sourceInfo{
		// 운영자/현지 (tier 1~3)
		{"운영자 잠금/입력", "operator / operator-locked", "전 타입·전 locale 수동 확정값(불가침)", 1, "live", "내부", "admin entity_detail 에서 pick/lock"},
		{"현지사용(검색확정)", "local-usage", "검색 그라운딩으로 확정한 현지표기(전 locale)", 1, "live", "throttle", "SearXNG+gemma 다회투표 만장일치"},
		{"현지매체 합의", "media-consensus", "현지 매체 ≥2 합의 표기", 2, "live", "내부", "RSS 관측 누적"},
		{"단일 매체", "rss-observation:<도메인>", "단일 현지매체 등장 표기", 3, "live", "throttle", "kwave_news_whitelist 피드"},
		// 권위 API (tier 4)
		{"TMDb", "tmdb", "작품(drama/movie/show) 다국어 제목·번역·alt_titles", 4, "live", "API키", "translations/alternative_titles"},
		{"KOFIC", "kofic", "한국 영화 메타", 4, "live", "API키", "영화진흥위원회"},
		{"KMDb", "kmdb", "한국영상자료원 영화 — KR/EN 제목만(외국 locale 무용)", 4, "plan", "API키", "외국어 미보유라 저우선"},
		{"MusicBrainz", "musicbrainz", "음악 아티스트 다국어 alias(group/person)", 4, "live", "1req/s", "recording/release-group alias 확장 계획"},
		{"Naver People", "naver-people", "인물 메타", 4, "live", "API", ""},
		{"Netflix(OTT)", "netflix", "작품 지역 공식 현지제목(ja/vi/es/zh_hant)", 4, "live", "10초 pacing", "ID앵커+gemma, internal/kdb/ott.go"},
		{"Disney+(OTT)", "disney", "작품 지역 공식 현지제목(ja/es/zh_hant)", 4, "live", "10초 pacing", "ID앵커+gemma, internal/kdb/disney.go"},
		{"클라 정정(검증)", "correction-verified", "소비자 정정 + Wikidata/신뢰출처 검증 반영", 4, "live", "내부", "POST /v1/corrections"},
		// Wikidata/위키 (tier 5~6)
		{"Wikidata 라벨", "wikidata-label", "전 엔티티 다국어 라벨(QID-pin 직접조회)", 5, "live", "덤프/배치 safe", "langlinks 13,702 · refill-anchored"},
		{"Wikipedia langlinks", "wikipedia-langlinks", "각 언어판 문서제목으로 빈칸 보강", 6, "live", "UA+maxlag safe", "무QID 꼬리는 직접 langlinks 계획"},
		{"Wikipedia zh변종", "wikipedia-zh-variant", "zh.wikipedia ?variant=zh-tw → zh_hant", 6, "live", "safe", ""},
		// 결정적 재속성/검색 (tier 7)
		{"로마자 재속성", "romanization", "person/group Latin locale(vi/es/id/pt_br) ← canonical_en 결정적", 7, "new", "내부(외부0)", "internal/kdb/romanize.go · CLI romanize-persons"},
		{"OpenCC zh↔zh_hant", "opencc", "검증 zh↔zh_hant 결정적 변환(s2t/t2s)", 7, "new", "오프라인", "internal/kdb/opencc_convert.go · CLI opencc-convert"},
		{"MyDramaList", "mydramalist", "작품 AKA 다국어 제목(ja)", 7, "exp", "공식키 throttle", "수동 CLI mdl-fill · niche 수율낮음"},
		{"검색 그라운딩(약증거)", "local-search", "빈칸 잠정 검색보강(만장일치 미달)", 7, "live", "throttle", "LocalFill"},
		// 로드맵 계획(미연동)
		{"iTunes Search", "itunes(계획)", "song_album ja/zh 공식 현지표기(국가별 스토어프론트)", 4, "plan", "20/min throttle", "song_album 최난구간 — ID앵커+non-ASCII가드"},
		{"Wikidata hanja(P1814)", "wikidata-hanja(계획)", "인물 한자/가나 본명 → zh_hant/ja", 5, "plan", "덤프 safe", "한자명 보유 배우/감독"},
		{"VIAF", "viaf(계획)", "인물 다국어 변형명(한자/가나)", 5, "plan", "덤프 safe", "P214 안전연결"},
		{"Discogs", "discogs(계획)", "음악 발매정보 다국어", 4, "plan", "API throttle", ""},
		// 최후
		{"LLM 합성(codex)", "codex-fallback", "권위소스 부재 시 최후 합성(미검증)", 8, "live", "내부", "verified_only 게이팅으로 엄격소비자 비노출"},
	}
}

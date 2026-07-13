package kdb

import "strings"

// SourcePipeline describes the operational lane a source belongs to. The admin
// UI uses this to show the owner where the next bottleneck is by entity class.
type SourcePipeline struct {
	Key        string
	Name       string
	Purpose    string
	Bottleneck string
}

// SourcePolicy is the central registry for source semantics. It intentionally
// separates "can be used as evidence/review context" from "may promote a
// candidate to active".
type SourcePolicy struct {
	Key           string
	Name          string
	Pipeline      string
	Provides      string
	Tier          int
	Status        string
	Access        string
	AutoPromote   bool
	ReviewOnly    bool
	DiscoveryOnly bool
	Note          string
}

const (
	SourcePipelineCuration  = "curation"
	SourcePipelinePerson    = "person"
	SourcePipelineWorks     = "works"
	SourcePipelineEvents    = "events"
	SourcePipelineMusic     = "music"
	SourcePipelineDiscovery = "discovery"
	SourcePipelineDerived   = "derived"
)

var sourcePipelineDefs = []SourcePipeline{
	{
		Key:        SourcePipelineCuration,
		Name:       "운영·현지 관측",
		Purpose:    "사람 확정, 현지 매체 합의, 단일 관측을 분리한다.",
		Bottleneck: "자동 승급에는 공식 앵커가 별도로 필요하다.",
	},
	{
		Key:        SourcePipelinePerson,
		Name:       "인물 공식 소스",
		Purpose:    "후보 person/group/연예인 실존성 앵커와 인물 메타를 확보한다.",
		Bottleneck: "Naver People/KMDb/KOFIC 인물 커버리지와 동명이인 검증.",
	},
	{
		Key:        SourcePipelineWorks,
		Name:       "작품·예능·드라마",
		Purpose:    "movie/drama/show 공식 제목·방영/출연/OTT 페이지를 확보한다.",
		Bottleneck: "방송사·OTT 공식 페이지는 통합 API가 없어 URL 앵커 파이프라인이 필요하다.",
	},
	{
		Key:        SourcePipelineEvents,
		Name:       "공연·행사·장소",
		Purpose:    "페스티벌 라인업, 공연장, 시상식, 투어·공연명 공식 앵커를 확보한다.",
		Bottleneck: "공식 라인업/대관 페이지와 티켓·뉴스 후보를 분리해야 한다.",
	},
	{
		Key:        SourcePipelineMusic,
		Name:       "음악",
		Purpose:    "artist/group/song_album의 공식 카탈로그 ID와 제목 확인을 확보한다.",
		Bottleneck: "한국어권 음원 사이트·KOMCA는 자동화 제한이 있어 허가/수동 검증 계층이 필요하다.",
	},
	{
		Key:        SourcePipelineDiscovery,
		Name:       "탐색·커뮤니티·리뷰",
		Purpose:    "공식 페이지 발견, alias 후보, 리뷰 근거를 모은다.",
		Bottleneck: "Namuwiki/검색 결과는 소스이지만 자동 active 승급 앵커가 아니다.",
	},
	{
		Key:        SourcePipelineDerived,
		Name:       "결정적 변환·LLM",
		Purpose:    "로마자/OpenCC/LLM 보강을 공식 소스와 분리해 관리한다.",
		Bottleneck: "LLM 산출값은 추출·정리용이며 그 자체로 승급 근거가 될 수 없다.",
	},
}

// SourcePipelineDefinitions returns the stable pipeline lanes in display order.
func SourcePipelineDefinitions() []SourcePipeline {
	out := make([]SourcePipeline, len(sourcePipelineDefs))
	copy(out, sourcePipelineDefs)
	return out
}

// SourcePolicies returns all source definitions in admin display order.
func SourcePolicies() []SourcePolicy {
	out := make([]SourcePolicy, len(sourcePolicies))
	copy(out, sourcePolicies)
	// A planned/experimental connector is inventory, not an executable trust
	// contract. Do not advertise it as an automatic promotion anchor until the
	// end-to-end pipeline is actually wired and reviewed.
	for i := range out {
		if out[i].Status != "live" {
			out[i].AutoPromote = false
		}
	}
	return out
}

var sourcePolicies = []SourcePolicy{
	{Key: string(SourceOperatorLocked) + " / " + string(SourceOperator), Name: "운영자 잠금/입력", Pipeline: SourcePipelineCuration, Provides: "전 타입·전 locale 수동 확정값", Tier: 1, Status: "live", Access: "내부", AutoPromote: true, Note: "operator-locked는 불가침"},
	{Key: string(SourceLocalUsage), Name: "현지사용(검색확정)", Pipeline: SourcePipelineCuration, Provides: "현지 검색 그라운딩으로 확정한 표기", Tier: 1, Status: "live", Access: "throttle", Note: "강한 값 소스지만 공식 앵커는 아님"},
	{Key: string(SourceMediaConsensus), Name: "현지매체 합의", Pipeline: SourcePipelineCuration, Provides: "현지 매체 2개 이상 표기 합의", Tier: 2, Status: "live", Access: "내부", Note: "값 우선순위는 높지만 후보 승급엔 별도 공식 앵커 필요"},
	{Key: "rss-observation:<domain>", Name: "단일 매체 관측", Pipeline: SourcePipelineCuration, Provides: "단일 현지매체 등장 표기", Tier: 3, Status: "live", Access: "RSS", Note: "후보/alias 관측"},

	{Key: "wikidata / " + string(SourceWikidataLabel), Name: "Wikidata", Pipeline: SourcePipelinePerson, Provides: "전 타입 다국어 라벨·alias·QID", Tier: 5, Status: "live", Access: "무키", AutoPromote: true, Note: "external_ref provider=wikidata는 identity anchor"},
	{Key: string(SourceNaverPeople), Name: "Naver People", Pipeline: SourcePipelinePerson, Provides: "인물 프로필/메타", Tier: 4, Status: "live", Access: "API/검색", AutoPromote: true, Note: "공식 인물 프로필 앵커"},
	{Key: string(SourceKOFIC), Name: "KOFIC/KOBIS", Pipeline: SourcePipelinePerson, Provides: "영화·영화인 공식 DB", Tier: 4, Status: "live", Access: "API키", AutoPromote: true, Note: "movie/person 공식 앵커"},
	{Key: string(SourceKMDb), Name: "KMDb", Pipeline: SourcePipelinePerson, Provides: "영화·인물·TV 관련 한국영상자료원 DB", Tier: 4, Status: "plan", Access: "API키", AutoPromote: true, Note: "서비스키 확보 후 person/work drain"},
	{Key: string(SourceNaverEncyc), Name: "Naver 백과", Pipeline: SourcePipelinePerson, Provides: "인물/작품 설명 역할 토큰", Tier: 6, Status: "live", Access: "API키", ReviewOnly: true, Note: "confirm/review용, 자동 승급 앵커 아님"},

	{Key: string(SourceTMDb), Name: "TMDb", Pipeline: SourcePipelineWorks, Provides: "movie/drama/show 번역·alt title·인물 크레딧", Tier: 4, Status: "live", Access: "API키", AutoPromote: true, Note: "작품 공식 구조화 앵커"},
	{Key: string(SourceNetflix), Name: "Netflix", Pipeline: SourcePipelineWorks, Provides: "OTT 공식 작품 페이지/지역 제목", Tier: 4, Status: "live", Access: "페이지", AutoPromote: true, Note: "공개 API 없음, 공식 URL 앵커"},
	{Key: string(SourceDisney), Name: "Disney+", Pipeline: SourcePipelineWorks, Provides: "OTT 공식 작품 페이지/지역 제목", Tier: 4, Status: "live", Access: "페이지", AutoPromote: true, Note: "공식 URL 앵커"},
	{Key: string(SourceTVING), Name: "TVING", Pipeline: SourcePipelineWorks, Provides: "K-OTT 공식 작품 페이지", Tier: 4, Status: "plan", Access: "페이지", AutoPromote: true, Note: "site search + exact title 검증"},
	{Key: string(SourceWavve), Name: "Wavve", Pipeline: SourcePipelineWorks, Provides: "K-OTT 공식 작품 페이지", Tier: 4, Status: "plan", Access: "페이지", AutoPromote: true, Note: "site search + exact title 검증"},
	{Key: string(SourceBroadcasterOfficial), Name: "방송사 공식 페이지", Pipeline: SourcePipelineWorks, Provides: "KBS/MBC/SBS/JTBC/tvN/Mnet/ENA 프로그램 페이지", Tier: 4, Status: "plan", Access: "페이지", AutoPromote: true, Note: "drama/show 병목 해소용"},
	{Key: string(SourceTVMaze), Name: "TVmaze", Pipeline: SourcePipelineWorks, Provides: "show/person/cast/episode API", Tier: 7, Status: "plan", Access: "무키", ReviewOnly: true, Note: "보조 DB, 공식 앵커 아님"},
	{Key: string(SourceMyDramaList), Name: "MyDramaList", Pipeline: SourcePipelineWorks, Provides: "작품 AKA/번역 제목", Tier: 7, Status: "exp", Access: "throttle", ReviewOnly: true, Note: "커뮤니티 DB"},

	{Key: string(SourceLollapalooza), Name: "Lollapalooza 공식 라인업", Pipeline: SourcePipelineEvents, Provides: "페스티벌/라인업/공연 날짜 공식 앵커", Tier: 4, Status: "plan", Access: "페이지", AutoPromote: true, Note: "festival/event entity 공식 URL"},
	{Key: string(SourceYES24LiveHall), Name: "YES24 LIVE HALL", Pipeline: SourcePipelineEvents, Provides: "공연장 공식 명칭·주소·대관 정보", Tier: 4, Status: "plan", Access: "페이지", AutoPromote: true, Note: "venue entity 공식 URL"},
	{Key: "kbs-awards / broadcaster-awards", Name: "방송사 시상식 공식 페이지", Pipeline: SourcePipelineEvents, Provides: "시상식·수상 부문·수상자 관계", Tier: 4, Status: "plan", Access: "페이지", AutoPromote: true, Note: "수상 부문은 entity보다 relation/fact로 저장 권장"},

	{Key: string(SourceCubeOfficial), Name: "Cube Entertainment 공식 프로필", Pipeline: SourcePipelineMusic, Provides: "아티스트/그룹/멤버 공식명·멤버십", Tier: 4, Status: "plan", Access: "페이지", AutoPromote: true, Note: "Gemini 결과는 여기 URL을 찾는 seed로만 사용"},
	{Key: string(SourceMusicBrainz), Name: "MusicBrainz", Pipeline: SourcePipelineMusic, Provides: "artist/group/recording/release/work", Tier: 4, Status: "live", Access: "1req/s", AutoPromote: true, Note: "아티스트/그룹 공식급 구조화 앵커"},
	{Key: string(SourceITunes), Name: "Apple Music/iTunes Search", Pipeline: SourcePipelineMusic, Provides: "track/album/artist store ID", Tier: 4, Status: "live", Access: "무키", AutoPromote: true, Note: "confirm-only"},
	{Key: string(SourceDiscogs), Name: "Discogs", Pipeline: SourcePipelineMusic, Provides: "release/artist/label", Tier: 4, Status: "live", Access: "무키/토큰", AutoPromote: true, Note: "confirm-only, 커뮤니티 편집 성격도 있어 보수적으로 사용"},
	{Key: string(SourceSpotify), Name: "Spotify", Pipeline: SourcePipelineMusic, Provides: "artist/album/track/show IDs", Tier: 4, Status: "plan", Access: "OAuth", AutoPromote: true, Note: "AI 학습 금지, ID 확인용으로만 사용"},
	{Key: string(SourceWarnerJapan), Name: "일본 레이블 공식 페이지", Pipeline: SourcePipelineMusic, Provides: "일본 발매명·가타카나·JP 음반 정보", Tier: 4, Status: "plan", Access: "페이지/API", AutoPromote: true, Note: "Warner/Universal/Sony Japan 등 공식 레이블"},
	{Key: string(SourceMelon) + " / " + string(SourceGenie) + " / " + string(SourceBugs) + " / " + string(SourceVibe), Name: "국내 DSP 카탈로그", Pipeline: SourcePipelineMusic, Provides: "국내 음원명·앨범·아티스트·트랙리스트", Tier: 4, Status: "plan", Access: "페이지/API", AutoPromote: true, Note: "Melon/Genie/Bugs/VIBE 공식 발매 페이지"},
	{Key: string(SourceQQMusic) + " / " + string(SourceNetEaseMusic) + " / " + string(SourceTencentMusic), Name: "중화권 음원 카탈로그", Pipeline: SourcePipelineMusic, Provides: "중국어권 아티스트명·곡명·앨범명·병기 제목", Tier: 4, Status: "plan", Access: "페이지/API", AutoPromote: true, Note: "직역이 아니라 플랫폼에 등록된 제목만 승급"},
	{Key: string(SourceKOMCA), Name: "KOMCA", Pipeline: SourcePipelineMusic, Provides: "저작물/작가/가수 공식 권리 DB", Tier: 4, Status: "plan", Access: "수동/허가", AutoPromote: true, Note: "자동화 금지 고지. 허가 또는 수동 검증만"},
	{Key: string(SourceYouTubeOfficial), Name: "YouTube 공식 채널/MV", Pipeline: SourcePipelineMusic, Provides: "공식 채널·영상 증거", Tier: 7, Status: "plan", Access: "API키", ReviewOnly: true, Note: "evidence용, 제목 canonical로 자동 채택 금지"},

	{Key: string(SourceNaverSearch), Name: "Naver Search", Pipeline: SourcePipelineDiscovery, Provides: "공식 페이지/뉴스/백과 탐색", Tier: 7, Status: "live", Access: "API키", DiscoveryOnly: true, Note: "source_url 발견용"},
	{Key: string(SourceKakaoSearch), Name: "Kakao/Daum Search", Pipeline: SourcePipelineDiscovery, Provides: "웹문서/동영상/이미지 탐색", Tier: 7, Status: "plan", Access: "API키", DiscoveryOnly: true, Note: "source_url 발견용"},
	{Key: string(SourceGeminiSearch), Name: "Gemini/LLM 검색 결과", Pipeline: SourcePipelineDiscovery, Provides: "공식 소스 후보 URL·검색 쿼리 seed", Tier: 7, Status: "plan", Access: "LLM+검색", DiscoveryOnly: true, Note: "LLM 답변 자체는 승급 근거 불가"},
	{Key: "tool:firecrawl / crawl4ai / browser-use / vane / gpt-researcher", Name: "LLM 검색·크롤링 도구", Pipeline: SourcePipelineDiscovery, Provides: "공식 페이지 수집·본문 추출·반복 검색 자동화", Tier: 7, Status: "plan", Access: "오픈소스/API", DiscoveryOnly: true, Note: "도구는 source가 아니라 evidence collector"},
	{Key: string(SourceBaiduBaike), Name: "Baidu Baike", Pipeline: SourcePipelineDiscovery, Provides: "중국어권 후보명·한자명·별칭 맥락", Tier: 7, Status: "plan", Access: "웹", ReviewOnly: true, Note: "포털/백과 성격. QQ/NetEase 등 공식 카탈로그 교차 필요"},
	{Key: string(SourceNamuWiki), Name: "Namuwiki", Pipeline: SourcePipelineDiscovery, Provides: "후보/alias/맥락 보강", Tier: 7, Status: "plan", Access: "웹", ReviewOnly: true, Note: "소스는 맞지만 커뮤니티라 자동 승급 불가"},
	{Key: string(SourceLocalSearch), Name: "검색 그라운딩(약증거)", Pipeline: SourcePipelineDiscovery, Provides: "빈칸 잠정 보강", Tier: 7, Status: "live", Access: "SearXNG", DiscoveryOnly: true, Note: "만장일치 미달은 official 아님"},

	{Key: string(SourceRomanization), Name: "로마자 재속성", Pipeline: SourcePipelineDerived, Provides: "person/group Latin locale 결정적 재속성", Tier: 7, Status: "live", Access: "오프라인", Note: "파생값, 승급 앵커 아님"},
	{Key: string(SourceOpenCC), Name: "OpenCC zh↔zh_hant", Pipeline: SourcePipelineDerived, Provides: "중국어 간/번체 결정적 변환", Tier: 7, Status: "live", Access: "오프라인", Note: "파생값, 승급 앵커 아님"},
	{Key: string(SourceCodexFallback), Name: "LLM 합성(gemma/codex)", Pipeline: SourcePipelineDerived, Provides: "권위/검색 부재 시 마지막 보강", Tier: 8, Status: "live", Access: "LLM", Note: "자동 승급 근거 불가"},
}

var officialPromotionProviders = []string{
	// Only live, pipeline-wired providers belong here. Planned connector names
	// must not become trust tokens merely because they appear in the registry.
	"wikidata", "tmdb", "kofic", "musicbrainz", "naver-people",
	"netflix", "disney",
}

var officialPromotionSources = []Source{
	// A canonical value's source label is not an identity anchor. Structured
	// providers must also have a validated external_ref; only human/verified
	// correction sources can promote from the value ledger alone.
	SourceOperatorLocked, SourceOperator, SourceCorrectionVerified,
}

var authoritativeIdentityProviders = []string{
	"wikidata", "tmdb", "kofic", "musicbrainz", "naver-people", "netflix", "disney",
}

var strongForeignSources = []Source{
	SourceOperatorLocked, SourceOperator, SourceLocalUsage, SourceMediaConsensus,
	SourceWikidataLabel, SourceTMDb, SourceKOFIC, SourceKMDb, SourceMusicBrainz, SourceNaverPeople,
	SourceCorrectionVerified, SourceNetflix, SourceDisney, SourceITunes, SourceDiscogs,
	SourceCubeOfficial, SourceWarnerJapan, SourceMelon, SourceGenie, SourceBugs, SourceVibe,
	SourceQQMusic, SourceNetEaseMusic, SourceTencentMusic, SourceSpotify, SourceKOMCA,
	SourceOfficialPage, SourceBroadcasterOfficial, SourceOTTOfficial, SourceTVING, SourceWavve,
	SourceWatcha, SourceCoupangPlay, SourceViki, SourceLollapalooza, SourceYES24LiveHall,
	SourceNaverEncyc, SourceYouTubeOfficial,
}

// IsOfficialPromotionProvider reports whether an external_ref provider can act
// as the official identity anchor required for candidate->active promotion.
func IsOfficialPromotionProvider(provider string) bool {
	return containsStringFold(officialPromotionProviders, provider)
}

// IsOfficialPromotionSource reports whether a non-empty canonical_* value from
// this source can act as the official anchor required for candidate promotion.
func IsOfficialPromotionSource(src string) bool {
	for _, s := range officialPromotionSources {
		if strings.EqualFold(string(s), strings.TrimSpace(src)) {
			return true
		}
	}
	return false
}

func OfficialPromotionProviderSQLList() string { return sqlStringList(officialPromotionProviders) }

func OfficialPromotionSourceSQLList() string { return sourceSQLList(officialPromotionSources) }

func AuthoritativeIdentityProviderSQLList() string {
	return sqlStringList(authoritativeIdentityProviders)
}

func StrongEvidenceSourceSQLList() string { return sourceSQLList(strongForeignSources) }

func AuthoritativeForeignSourceStrings() []string {
	out := make([]string, 0, len(strongForeignSources))
	for _, s := range strongForeignSources {
		out = append(out, string(s))
	}
	return out
}

func containsStringFold(vals []string, needle string) bool {
	needle = strings.TrimSpace(needle)
	for _, v := range vals {
		if strings.EqualFold(v, needle) {
			return true
		}
	}
	return false
}

func sourceSQLList(vals []Source) string {
	out := make([]string, 0, len(vals))
	for _, s := range vals {
		out = append(out, string(s))
	}
	return sqlStringList(out)
}

func sqlStringList(vals []string) string {
	quoted := make([]string, 0, len(vals))
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		quoted = append(quoted, "'"+strings.ReplaceAll(v, "'", "''")+"'")
	}
	return strings.Join(quoted, ",")
}

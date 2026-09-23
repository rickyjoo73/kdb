// Package kdbapi exposes the KDB platform read API.
package kdbapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb"
	"github.com/rickyjoo73/kdb/internal/kdb/agents"
	"github.com/rickyjoo73/kdb/internal/kdb/agents/gatekeeper"
	"github.com/rickyjoo73/kdb/internal/kdb/codexcli"
	"github.com/rickyjoo73/kdb/internal/kdb/corrections"
	"github.com/rickyjoo73/kdb/internal/kdb/enrich"
	"github.com/rickyjoo73/kdb/internal/kdb/ratelimit"
	"github.com/rickyjoo73/kdb/internal/kdb/readiness"
	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
)

const (
	defaultLimit      = 20
	maxLimit          = 100
	defaultMatchLimit = 100
	maxMatchLimit     = 200
)

type Store struct {
	Pool *pgxpool.Pool
	// onEnqueue — research_queue 신규 적재 시 즉시 호출(워커 tick nudge). nil 이면 무시.
	// 온디맨드 "빠른 채움": 다음 주기 tick(≤15s)을 기다리지 않고 즉시 발굴 착수하게 한다.
	onEnqueue func()
	// onReviewParked — 근거 부족(review)으로 보류된 키워드의 row id 를 즉시 전달.
	// 오너 계약(07-13): "제대로 된 키워드는 유입 즉시 심사" — 자동 검증기가 주기/백로그
	// 순서를 기다리지 않고 이 키워드부터 바로 검증하게 한다. nil 이면 무시.
	onReviewParked func(rowID string)
	// onDemandCandidate — **소비자가 물었는데 그 행이 아직 candidate 다.**
	//
	//   위 둘은 "새 낱말"을 다룬다. 이건 이미 행이 있는 경우다 — 재요청은 큐 INSERT
	//   가 중복으로 걸러지고 재개 UPDATE 는 precheck_status 가 'legacy'·'review' 인
	//   행만 열어 'pass' 로 닫힌 행은 done 에 머물며, matches 의 기본 status 가
	//   'active' 라 bgEnrich 도 안 걸린다. 그래서 소비자가 몇 번을 물어도 그 행에는
	//   아무 일도 안 일어난다(실측 2026-09-16: 오늘 요청된 낱말 중 candidate 399건,
	//   그중 앵커 없음 386건).
	onDemandCandidate func(entityID string)
}

type RouterOptions struct {
	RequestTimeout time.Duration
	LogRequests    bool
	APIKeys        []string
	// OnResearchEnqueue — 신규 발굴 키워드 적재 시 호출(워커 즉시 nudge). 서버 모드에서 배선.
	OnResearchEnqueue func()
	// OnReviewParked — review 보류 키워드 적재/재요청 시 row id 전달(즉시 자동검증 kick).
	OnReviewParked func(rowID string)
	// OnDemandCandidate — 소비자가 기다리는 candidate 의 entity id 전달(요청 훅).
	OnDemandCandidate func(entityID string)
}

type Entity struct {
	ID              string    `json:"id"`
	// KID — KDB 자체 ID. **주 앵커**이고 소비자가 들고 다니는 값이다(I03).
	// 이름으로 묻지 말고 이것으로 물으면 동명이인이 근본적으로 사라진다.
	// 불변이며 회수하지 않는다 — 병합돼 퇴역한 행의 kid 로 물어도 답할 수 있어야 한다.
	KID             string    `json:"kid"`
	EntityType      string    `json:"entity_type"`
	CanonicalKO     string    `json:"canonical_ko"`
	CanonicalEN     string    `json:"canonical_en,omitempty"`
	CanonicalJA     string    `json:"canonical_ja,omitempty"`
	CanonicalVI     string    `json:"canonical_vi,omitempty"`
	CanonicalZH     string    `json:"canonical_zh,omitempty"`
	CanonicalZHHant string    `json:"canonical_zh_hant,omitempty"`
	CanonicalES     string    `json:"canonical_es,omitempty"`
	CanonicalID     string    `json:"canonical_id,omitempty"`
	CanonicalPTBR   string    `json:"canonical_pt_br,omitempty"`
	Aliases         AliasSets `json:"aliases"`
	CategoryHint    string    `json:"category_hint,omitempty"`
	// OccupationDomain — 사람이면 무슨 영역인가(entertainment·sports·politics·
	// business·media·academia·arts). 근거는 위키데이터 P106 이고, 모르면 빈 문자열이다.
	// 유형(person)을 늘리지 않고 영역을 따로 든 이유는 docs 의 표에 적혀 있다.
	OccupationDomain string `json:"occupation_domain,omitempty"`
	// Gender — male·female·other. 근거는 위키데이터 P21 이고, 모르면 빈 문자열이다.
	// 이름에서 추정하지 않는다 — 지민·현우·서연은 다 양성이다.
	Gender string `json:"gender,omitempty"`
	Confidence      float64   `json:"confidence"`
	Status          string    `json:"status"`
	SourceURLs      []string  `json:"source_urls,omitempty"`
	SourceDomains   []string  `json:"source_domains,omitempty"`
	OperatorLocked  bool      `json:"operator_locked"`
	LastVerifiedAt  time.Time `json:"last_verified_at"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`

	// 동명이인(homonym) 구분 필드 (2026-05-29). person_details LEFT JOIN.
	// 같은 canonical_ko 를 가진 여러 match 를 agency/works/role/birth/disambig
	// 로 구분한다. 기존 필드는 그대로, 아래만 추가 (backward compatible).
	Disambig      string   `json:"disambig,omitempty"`
	PrimaryRole   string   `json:"primary_role,omitempty"`
	Agency        string   `json:"agency,omitempty"`
	BirthYear     int      `json:"birth_year,omitempty"`
	NotableWorks  []string `json:"notable_works,omitempty"`
	NeedsDisambig bool     `json:"needs_disambig,omitempty"`

	// per-locale source(canonical_<loc>_source) — verified_only/provenance 게이팅용 내부
	// 필드. JSON 미직렬화(json:"-")라 응답 형태 불변. lookup/prepare 의 게이팅 helper 가 사용.
	CanonicalENSource     string `json:"-"`
	CanonicalJASource     string `json:"-"`
	CanonicalVISource     string `json:"-"`
	CanonicalZHSource     string `json:"-"`
	CanonicalZHHantSource string `json:"-"`
	CanonicalESSource     string `json:"-"`
	CanonicalIDSource     string `json:"-"`
	CanonicalPTBRSource   string `json:"-"`

	// LocaleProvenance — verified_only/provenance 요청 시 각 locale 값의 출처 라벨
	// (operator-locked|wikidata-label|external-db|media-consensus|romanization|opencc|
	// community-db|wikipedia-langlinks|media-single|llm-only). 평소엔 비어 직렬화 안 됨.
	LocaleProvenance map[string]string `json:"locale_provenance,omitempty"`

	// 엔티티-레벨 정체성 검증 tier(증분2, internal/kdb/verify). "이 항목이 실재하는 올바른
	// K-엔티티인가"(오염/동명이인 아님). per-locale VALUE 신뢰(위 provenance)와 상호보완.
	//   authoritative : Wikidata/TMDb 등 권위앵커. evidenced : wiki/강한source/conf 또는
	//   검색+gemma 확인. unverified : 독립 확증 없음. 미검증(스윕 전)이면 빈 값.
	VerificationTier     string `json:"verification_tier,omitempty"`
	VerificationEvidence string `json:"verification_evidence,omitempty"`

	// AbsentLocales — include_absent 요청 시, 요청 locale 중 값이 없는 것의 이유와 할 일.
	// 평소엔 비어 직렬화 안 됨(응답 형태 불변).
	AbsentLocales map[string]LocaleAbsence `json:"absent_locales,omitempty"`
}

type AliasSets struct {
	KO     []string `json:"ko,omitempty"`
	EN     []string `json:"en,omitempty"`
	JA     []string `json:"ja,omitempty"`
	VI     []string `json:"vi,omitempty"`
	ZH     []string `json:"zh,omitempty"`
	ZHHant []string `json:"zh_hant,omitempty"`
	ES     []string `json:"es,omitempty"`
	ID     []string `json:"id,omitempty"`
	PTBR   []string `json:"pt_br,omitempty"`
}

type EntityFilter struct {
	Query  string
	Type   string
	Status string
	Limit  int
	Offset int
	// 감사/페이지네이션 필터 (2026-06-01). 0/zero 면 미적용.
	MinConfidence float64
	UpdatedSince  time.Time
}

type LookupRequest struct {
	Query  string `json:"query"`
	// KID — KDB 자체 ID 로 묻는다(I03). 주면 이름 매칭을 건너뛰고 **그 대상 하나**를
	// 돌려준다. query 가 `K0000123` 꼴이면 이 칸을 안 줘도 같게 동작한다.
	KID    string `json:"kid,omitempty"`
	Type   string `json:"type,omitempty"`
	Status string `json:"status,omitempty"`
	Limit  int    `json:"limit,omitempty"`
	// VerifiedOnly — true 면 미검증 출처(codex/romanization/wikipedia/rss 등) locale 값을
	// 비우고 검증된 표기만 반환 + locale_provenance 부착. 기본 false(기존 동작 보존).
	VerifiedOnly bool `json:"verified_only,omitempty"`
	// IncludeAbsent — true 면 요청 locale 중 **값이 없는 것의 이유와 할 일**을 붙인다.
	// match 에만 있던 것을 여기에도 둔다(2026-09-15) — 우리가 권하는 문에 없으면
	// 권장을 따를수록 안내를 잃는다.
	IncludeAbsent bool     `json:"include_absent,omitempty"`
	Locales       []string `json:"locales,omitempty"` // include_absent 가 볼 locale. 빈값=주요 8개
}

// PrepareRequest — 외부가 기사 작성 시점에 등장할 한글 고유명사를 미리 던져,
// KDB 가 조회 전에 다국어 번역을 준비하게 한다(받기 + 빠른 준비).
//
// terms 각 원소는 문자열("아이유") 또는 객체({"ko":"박보검","type":"person"}).
// type 힌트(선택)는 매칭·동명이인 구분 정확도를 높인다. KDB 는 K-콘텐츠 엔터만
// 준비하고, 비-K(일반 지명/정치인 등)는 out_of_scope 로 응답한다(등록 안 함).
type PrepareRequest struct {
	Terms   []json.RawMessage `json:"terms"`
	Locales []string          `json:"locales,omitempty"` // 준비할 locale(빈값=주요 8개)
	// VerifiedOnly — true 면 미검증 출처 locale 값은 values 에서 빼고 missing(준비중)으로
	// 분류한다. 검증된 표기만 받고 싶은 소비자용. 기본 false(기존 동작 보존).
	VerifiedOnly bool `json:"verified_only,omitempty"`
	// SourceURL — 출처 기사 URL(batch 레벨, 기사당 1개). 발굴 큐에 저장돼 어느 기사에서
	// 온 고유명사인지 추적 + 향후 역추적 재분석 기반(kstory 등 소비자 요청). term 별 override 는
	// PrepareTerm.SourceURL.
	SourceURL string `json:"source_url,omitempty"`
	// Context — 기사에서 키워드가 실제로 등장한 짧은 문맥. 신규 고유명사의 자동
	// 조사는 type/source/context/type-cue가 함께 입증될 때만 허용한다.
	Context        string `json:"context,omitempty"`
	ArticleID      string `json:"article_id,omitempty"`
	ArticleVersion string `json:"article_version,omitempty"`
	// SuggestionMeta — terms[].suggestions 를 누가 만들었나. producer 가 없으면 제안을 받지
	// 않는다 — 출처 없는 제안은 재료도 아니다.
	SuggestionMeta readiness.SuggestionMeta `json:"suggestion_meta,omitempty"`
}

// PrepareTerm — 파싱된 term(ko + 선택 type).
type PrepareTerm struct {
	Ko        string `json:"ko"`
	Type      string `json:"type,omitempty"`
	SourceURL string `json:"source_url,omitempty"` // term 별 출처 URL(override, 옵션)
	Context   string `json:"context,omitempty"`    // term 별 문맥(batch context override)
	// Suggestions — 소비자가 자기 모델로 만든 표기(locale → 제안). **값이 아니라 재료다.**
	//
	// ★여기에도 붙인다(2026-09-15). 처음엔 readiness.Term 에만 붙였는데, 소비자가 실제로
	//   쓰는 것은 `/v1/prepare` 다(실측: 하루 110회 vs /v1/preparations 누적 7회).
	//   쓰는 문에 안 달면 기능이 닿지 않는다.
	Suggestions map[string]readiness.Suggestion `json:"suggestions,omitempty"`
}

// PrepareItem — term 1건의 준비 상태.
type PrepareItem struct {
	Term       string            `json:"term"`
	// Status — ready | preparing | new | review | unfillable | out_of_scope
	//
	// ★`unfillable` 은 "대상은 맞는데 표기 근거를 못 찾았다"이다 (2026-09-15).
	//   종전엔 이 자리도 `preparing` 이었다. `preparing` 은 곧 "다시 물어보라"는 뜻인데,
	//   실측으로 그렇게 답한 낱말의 절반 이상이 하루가 지나도 안 채워졌고 발굴 큐를
	//   보면 **이미 끝나 있었다**(done · no_match 86 · blocked_precheck 79).
	//   영영 오지 않을 답을 계속 물어보게 두는 것은 답이 아니다.
	Status     string            `json:"status"`
	Type       string            `json:"type,omitempty"`
	EntityID   string            `json:"entity_id,omitempty"`
	// KID — KDB 자체 ID (I03). **주 앵커이고 소비자가 들고 다니는 값이다.**
	//
	// ★prepare 만 이것을 안 보내고 있었다 (2026-09-16 실측). lookup·entities 는
	//   보내는데, 소비자가 가장 많이 쓰는 문이 prepare 다. 여기서 안 주면
	//   "kid 로 조회하라"는 안내가 **받은 적 없는 값을 쓰라는 말**이 된다.
	//   동명이인은 이름으로 못 가리고 entity_id(UUID)는 우리 내부 형식이다 —
	//   kid 가 소비자가 저장해 두고 다시 물을 수 있는 유일한 값이다.
	KID string `json:"kid,omitempty"`
	Values     map[string]string `json:"values,omitempty"`     // 현재 가용 locale 표기
	Provenance map[string]string `json:"provenance,omitempty"` // values 각 locale 의 출처 라벨
	Missing    []string          `json:"missing,omitempty"`    // 아직 준비중인 locale
	// AbsentLocales — Missing 각 locale 의 **이유와 할 일**(2026-09-15).
	//
	// ★missing 은 "없다"만 말하고 "왜"와 "그래서 무엇을"을 안 말했다. 그래서 소비자가
	//   한글을 그대로 발행하거나, 제목을 음역해 버렸다(실측: 작품 제목 6,640칸이
	//   로마자로 채워져 있었다). fill_hint 가 그 갈림길을 알려 준다.
	AbsentLocales map[string]LocaleAbsence `json:"absent_locales,omitempty"`
	// Unavailable — Missing 중 enrich 소스가 소진(exhausted)돼 현재 소스로는 채울 수
	// 없는 locale(감사 07-25: 소스천장 무종결 해소). 재조회 대기 대상이 아님을 통지 —
	// 새 소스가 생기면 다시 채워질 수 있으므로 '현재 기준' 종결이다.
	Unavailable []string `json:"unavailable,omitempty"`
	// HomonymRisk — 이 표제어로 **공통 원장에 다른 대상이 또 있다**는 신호(P4.01).
	// 서빙 상태를 바꾸지 않는다. 종전엔 기존 원장에 같은 이름이 하나뿐이면
	// prepareMatchSafe 가 무조건 safe 로 판정해(koMatches<=1) 새 동명이 있을 가능성
	// 자체가 보이지 않았다. 실측 2026-09-14: 활성 4,163건이 "기존 원장엔 유일한데
	// 흡수분에 같은 이름의 다른 대상이 실재"하는 상태였고, ready 로 서빙된 표제어
	// 3,046건 중 193건이 나중에 교정 요청을 받았다.
	// 동일 대상인지 다른 대상인지의 판정은 P5(동일성 판정) 몫이다 — 여기서는
	// **놓치지 않았다는 사실만** 남긴다.
	HomonymRisk bool `json:"homonym_risk,omitempty"`
	// Resolution — 종결 상태(review · unfillable · out_of_scope)의 **이유와 할 일**.
	// 상태만 주고 이유를 안 주면 소비자는 같은 것을 다시 묻거나 스스로 지어낸다.
	Resolution string `json:"resolution,omitempty"`
}

type PrepareResponse struct {
	Items          []PrepareItem `json:"items"`
	PreparationID  string        `json:"preparation_id,omitempty"`
	TrackingStatus string        `json:"tracking_status,omitempty"`
}

type LookupResponse struct {
	Query string `json:"query"`
	// Matches — **질의가 그 대상의 이름인 것만** 담는다(캐노니컬·별칭, 전 로케일,
	// 정규화 동치). 이름에 질의가 들어 있을 뿐인 것은 Related 로 간다 — lookup_contract.go.
	Matches []Entity `json:"matches"`
	// Related — 부분일치(참고용). «카카오» 로 물었을 때의 카카오뱅크·카카오벤처스가
	// 여기 온다. **status 를 만들지 않는다.** 종전엔 이것들이 matches 에 섞여
	// found 로 나갔다(2026-09-23 소비자 두 곳 신고).
	Related []Entity `json:"related,omitempty"`
	// Status — found | ambiguous | miss | out_of_scope | invalid_type | bad_request.
	//   found        정확일치 1건. 써도 된다
	//   ambiguous    정확일치 2건 이상 — 고르지 않는다(M06). 후보를 보고 소비자가 고른다
	//   miss         정확일치 0건. 발굴 큐에 넣었다(related 가 있을 수 있다)
	//   out_of_scope 검토 종결(비K 판정 tombstone) — 재조회해도 같다. 무한 재폴링 차단
	//   invalid_type 보낸 type 이 유형 목록에 없다(묶음 조회의 항목 단위 통지)
	//   bad_request  항목 자체가 잘못됐다(ko 없음 등) — 묶음 조회의 항목 단위 통지
	Status string `json:"status,omitempty"`
	// Error — 항목 단위 오류의 코드·설명(묶음 조회). 전체 요청이 실패한 것이 아니라
	// **이 항목만** 못 받았다는 뜻이다. 오류 모양은 §5-3 과 같다.
	Error *ItemError `json:"error,omitempty"`
}

// ItemError — 묶음 응답 안에서 항목 하나가 실패한 이유.
//
// ★자리를 비우지 않는다 (2026-09-23 PressLocale 신고 2). 종전엔 잘못된 질의를
//
//	파싱 단계에서 조용히 버려서, 질의 10개에 결과 9개가 왔다. 응답을 **요청 순서로
//	짝짓는 소비자**는 그 순간부터 모든 답이 한 칸씩 밀려 «어느 사람의 표기가 다른
//	사람 이름에 붙는다». 보낸 항목 수와 결과 수는 언제나 같아야 한다.
type ItemError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type BulkLookupRequest struct {
	// Queries — 각 원소는 문자열("아이유") 또는 객체({"ko":"채영","type":"person","context":"…"}).
	//
	// ★객체를 받는다(2026-09-15). 종전엔 []string 이라 **이름마다 유형을 붙일 수 없었다** —
	//   Type 은 묶음 전체에 하나뿐이라 "박보검=person · 폭싹 속았수다=drama" 를 한 번에
	//   보낼 수가 없었다. 그런데 묶음이 권장 경로다. 권장하는 문이 정체성 정보를 못 받으면
	//   동명이인을 가릴 재료가 애초에 안 들어온다(채영(TWICE) vs 채영(CLC)).
	//   문자열도 그대로 받는다 — 기존 소비자가 깨지지 않는다.
	Queries []json.RawMessage `json:"queries"`
	// Type — 묶음 기본 유형. 각 query 객체의 type 이 이것을 덮는다.
	Type   string `json:"type,omitempty"`
	Status string `json:"status,omitempty"`
	Limit  int    `json:"limit,omitempty"`
	// VerifiedOnly — 단건 /v1/lookup 과 **같은 게이트**. 미검증 locale 값을 비우고
	// locale_provenance 를 붙인다.
	//
	// ★없어서 결함이었다(2026-09-15). 소비자가 "이름 조회로는 출처 등급을 알 수 없다"고
	//   했는데 맞는 말이었다 — 단건에는 있고 묶음에는 없었다. 그런데 묶음이 권장 경로다
	//   (한도를 아끼려면 묶어 보내야 한다). 권장하는 문에 게이트가 없으면
	//   **권장을 따를수록 검증 정보를 잃는다.**
	VerifiedOnly bool `json:"verified_only,omitempty"`
	// IncludeAbsent · Locales — 단건과 같은 계약(absent_reason.go).
	IncludeAbsent bool     `json:"include_absent,omitempty"`
	Locales       []string `json:"locales,omitempty"`
}

type BulkLookupResponse struct {
	Results []LookupResponse `json:"results"`
}

// ★hasHomonymChoice 는 splitExactMatches 로 흡수했다 (2026-09-23). 「요청한 이름과
//   정확히 같은가」를 재는 자리가 둘이면 규칙이 갈라진다 — 실제로 이쪽은 canonical_ko
//   하나만 봐서 별칭·다른 로케일로 물은 동명이인을 놓쳤다. lookup_contract.go 한 곳이다.

// BulkQuery — 파싱된 묶음 조회 항목.
type BulkQuery struct {
	Ko      string `json:"ko"`
	Type    string `json:"type,omitempty"`
	Context string `json:"context,omitempty"`
	// Err — 이 항목을 조회할 수 없는 이유(있으면 조회하지 않고 그대로 통지한다).
	// 자리는 지킨다 — ItemError 주석 참조.
	Err *ItemError `json:"-"`
}

// parseBulkQueries — 문자열/객체 혼용을 받는다. **보낸 항목 수와 결과 수는 같다.**
//
// ★종전엔 잘못된 원소를 `continue` 로 버렸다 (2026-09-23 PressLocale 신고 2).
//
//	`{"type":"person"}` 처럼 ko 가 없는 항목을 보내면 200 에 결과가 한 칸 모자란
//	채로 왔고, 오류도 경고도 없었다. 순서로 짝짓는 소비자는 그 뒤 전부가 밀린다.
//	이제 버리지 않고 **오류를 실은 자리**로 남긴다. 묶음 전체를 400 으로 실패시키지
//	않는 것은 그대로다 — 한 항목 때문에 나머지 49건을 잃게 할 수는 없다.
func parseBulkQueries(raw []json.RawMessage, defType string) []BulkQuery {
	out := make([]BulkQuery, 0, len(raw))
	for _, m := range raw {
		var q BulkQuery
		var str string
		if json.Unmarshal(m, &str) == nil {
			q = BulkQuery{Ko: str}
		} else if json.Unmarshal(m, &q) != nil {
			out = append(out, BulkQuery{Err: &ItemError{
				Code:    "bad_request",
				Message: "항목은 문자열(\"아이유\") 또는 객체({\"ko\":\"아이유\"})여야 합니다",
			}})
			continue
		}
		q.Ko = strings.TrimSpace(q.Ko)
		if q.Ko == "" {
			q.Err = &ItemError{Code: "bad_request", Message: "ko 가 필요합니다"}
			out = append(out, q)
			continue
		}
		if q.Type == "" {
			q.Type = defType
		}
		// 단건 조회와 **같은 규칙**을 쓴다(consumerTypeFilter): 미상 표시는 필터에서
		// 빼고, 목록에 없는 값은 조용히 삼키지 않는다.
		filter, ok := consumerTypeFilter(strings.TrimSpace(q.Type))
		if !ok {
			q.Err = &ItemError{
				Code:    "invalid_type",
				Message: "type 이 유형 목록에 없습니다: " + strings.TrimSpace(q.Type) + " (/docs §7-1)",
			}
			q.Type = ""
			out = append(out, q)
			continue
		}
		q.Type = filter
		out = append(out, q)
	}
	return out
}

type MatchEntitiesRequest struct {
	SourceText string `json:"source_text"`
	Locale     string `json:"locale"`
	Limit      int    `json:"limit,omitempty"`
	// 소비자 게이팅 파라미터 (2026-06-01). 번역 핫패스에서 저신뢰 힌트 차단용.
	MinConfidence float64 `json:"min_confidence,omitempty"` // 기본 0.50 floor
	Status        string  `json:"status,omitempty"`         // active|candidate|rejected (빈값=active — rejected tombstone 유출 방지)
	VerifiedOnly  bool    `json:"verified_only,omitempty"`  // operator_locked OR wikidata OR ≥2매체합의만
	// Disambiguate — true 면 기사 본문(source_text)으로 gemma 가 매칭 후보를 검증해 실제로
	// 그 K-엔티티로 언급된 것만 남긴다(일반어·오매칭·동명이의 제거). 핫패스 지연이 생기므로
	// 소비자 opt-in(기본 false=현행 즉답).
	Disambiguate bool `json:"disambiguate,omitempty"`
	// IncludeAbsent — true 면 **요청 locale 값이 없는 대상도 돌려준다**(2026-09-14).
	//
	// ★왜 필요한가. 종전 쿼리는 `locale_name <> ''` 로 걸러서, 그 언어 표기가 없는 대상은
	//   응답에서 **통째로 사라졌다.** 소비자는 그 고유명사가 KDB 에 있는지조차 모르고,
	//   모르니 /v1/corrections 로 표기를 제보할 수도 없다. 빈칸보다 나쁘다 —
	//   빈칸은 "없다"를 말하지만 누락은 아무 말도 안 한다.
	//
	//   운영자 방침(2026-09-14): 있으면 등급과 함께 주고, 없으면 **없다고 말한다.**
	//   그 자리에서 지어내지는 않는다 — 즉석 생성은 같은 이름을 기사마다 다르게 만든다.
	//
	//   기본 false 다. 기존 소비자의 응답 크기·형태를 바꾸지 않는다.
	IncludeAbsent bool `json:"include_absent,omitempty"`
}

type MatchedEntity struct {
	ID              string    `json:"id"`
	// KID — KDB 자체 ID(I03). **이 문으로만 들어오는 소비자도 kid 를 배워야 한다** —
	// 안 실어 주면 "kid 로 물어라"라고 해놓고 kid 를 알 길을 안 주는 셈이다.
	KID             string    `json:"kid"`
	KO              string    `json:"ko"`
	LocaleName      string    `json:"locale_name"`
	EntityType      string    `json:"entity_type"`
	Confidence      float64   `json:"confidence"`
	Status          string    `json:"status"`
	OperatorLocked  bool      `json:"operator_locked"`
	Provenance      string    `json:"provenance"`              // operator-locked|wikidata-label|external-db|media-consensus|wikipedia-langlinks|media-single|llm-only
	LocaleSource    string    `json:"locale_source,omitempty"` // 반환된 locale 값의 raw source 컬럼(canonical_<loc>_source). 소비자 게이팅용.
	SourceURLs      []string  `json:"source_urls,omitempty"`
	UpdatedAt       time.Time `json:"updated_at"`
	SourceAliases   []string  `json:"source_aliases,omitempty"`
	TargetAliases   []string  `json:"target_aliases,omitempty"`
	Note            string    `json:"note,omitempty"`
	Disambig        string    `json:"disambig,omitempty"`         // 동명이인 구분 라벨(예: "(김하늘 배우)"). 비어있으면 단독.
	LocaleAmbiguous bool      `json:"locale_ambiguous,omitempty"` // 반환된 locale_name 이 같은 type 의 다른 active entity 와 겹침 → 번역 시 확인 권장. entity 레벨 needs_disambig(한국어 동명이인)와는 별개 신호(목표 locale 표기 충돌).
	LocaleFallback  bool      `json:"locale_fallback,omitempty"`  // 요청 locale 표기가 없어 locale_name 이 영어(canonical_en)로 폴백됨 → 해당 언어 표기 아님.
	// LocaleAbsent — locale_name 이 빈 이유(2026-09-14). 빈칸이 조용하면 소비자는
	// "없다"와 "있는데 뺐다"를 구별하지 못하고, 실제로 한글을 그대로 발행했다.
	//   no_value          DB 에 그 언어 표기가 없다 → 제보(/v1/corrections) 대상
	//   fallback_en       요청 언어 표기가 없어 locale_name 이 **영어**다. 그대로 쓰면
	//                     그 언어 기사에 영어가 박힌다 — 영어를 참고값으로 삼아 fill_hint
	//                     대로 만들고 /v1/corrections 로 보내 달라는 뜻이다.
	//   llm_only          LLM 추측값이라 서빙에서 뺐다(KDB_SERVE_HIDE_LLM_ONLY=1일 때만)
	//   unverified_source verified_only 요청인데 출처가 검증 등급이 아니다
	LocaleAbsent string `json:"locale_absent,omitempty"`
	// FillHint — 표기가 없을 때 **무엇을 해야 하는가**. locale_absent 가 있을 때만 채운다.
	//   transliterate    소리를 옮겨라(인물·그룹·캐릭터). 뜻을 옮기면 이후→"After" 가 된다.
	//   translate_title  공식 현지 제목이 있으면 그것을, 없으면 뜻을 옮겨라(작품·행사·브랜드).
	// 정한 표기는 /v1/corrections 로 보내 주면 다음 요청부터 우리가 답한다.
	FillHint string `json:"fill_hint,omitempty"`
}

type BulkMatchEntitiesRequest struct {
	SourceTexts   []string `json:"source_texts"`
	Locale        string   `json:"locale"`
	Limit         int      `json:"limit,omitempty"`
	MinConfidence float64  `json:"min_confidence,omitempty"`
	Status        string   `json:"status,omitempty"` // active|candidate|rejected (빈값=active — rejected tombstone 유출 방지)
	VerifiedOnly  bool     `json:"verified_only,omitempty"`
}

// MatchEntitiesResponse — /v1/entities/match 응답. entities 는 종전 그대로이고
// status/candidates 는 M06(문맥 없는 이름) 에서만 붙는 추가 필드다. 평소 응답의
// 바이트 모양을 바꾸지 않으려고 omitempty 로 두었다 — 기존 소비자 4곳은 entities
// 만 읽으므로 회귀가 없고, 새 소비자만 status 를 보고 후보 제시로 분기하면 된다.
type MatchEntitiesResponse struct {
	Entities   []MatchedEntity `json:"entities"`
	Status     string          `json:"status,omitempty"`     // "ambiguous" 일 때만 존재
	Candidates []MatchedEntity `json:"candidates,omitempty"` // status=ambiguous 인 동명 후보들
}

type BulkMatchResult struct {
	SourceText string          `json:"source_text"`
	Entities   []MatchedEntity `json:"entities"`
	Status     string          `json:"status,omitempty"`
	Candidates []MatchedEntity `json:"candidates,omitempty"`
}

type BulkMatchEntitiesResponse struct {
	Results []BulkMatchResult `json:"results"`
}

type ExternalRef struct {
	Provider    string    `json:"provider"`
	ExternalID  string    `json:"external_id"`
	URL         string    `json:"url,omitempty"`
	Confidence  float64   `json:"confidence"`
	Description string    `json:"description,omitempty"`
	FetchedAt   time.Time `json:"fetched_at"`
}

type PersonDetails struct {
	EntityID       string   `json:"entity_id"`
	PrimaryRole    string   `json:"primary_role"`
	SecondaryRoles []string `json:"secondary_roles,omitempty"`
	Groups         []string `json:"groups,omitempty"`
	Agency         string   `json:"agency,omitempty"`
	Gender         string   `json:"gender,omitempty"`
	BirthYear      int      `json:"birth_year,omitempty"`
	NotableWorks   []string `json:"notable_works,omitempty"`
}

type Relation struct {
	Direction    string   `json:"direction"`
	RelationType string   `json:"relation_type"`
	EntityID     string   `json:"entity_id"`
	EntityKO     string   `json:"entity_ko"`
	EntityType   string   `json:"entity_type"`
	Confidence   float64  `json:"confidence"`
	SourceURLs   []string `json:"source_urls,omitempty"`
}

type ObservationRequest struct {
	EntityID     string  `json:"entity_id"`
	Locale       string  `json:"locale"`
	Spelling     string  `json:"spelling"`
	SourceDomain string  `json:"source_domain"`
	SourceURL    string  `json:"source_url,omitempty"`
	Confidence   float64 `json:"confidence,omitempty"`
	Evaluate     bool    `json:"evaluate,omitempty"`
}

type ResearchQueueRequest struct {
	EntityKO            string `json:"entity_ko"`
	RequestedEntityType string `json:"requested_entity_type,omitempty"`
	ContextHint         string `json:"context_hint,omitempty"`
	SourceID            string `json:"source_id,omitempty"`
	SourceURL           string `json:"source_url,omitempty"` // 출처 기사 URL(추적·역추적)
	Origin              string `json:"-"`                    // 서버 지정 경로; 클라이언트가 위조할 수 없음
}

type SiteSearchRequest struct {
	Locale              string   `json:"locale"`
	Query               string   `json:"query,omitempty"`
	Domains             []string `json:"domains,omitempty"`
	LimitDomains        int      `json:"limit_domains,omitempty"`
	MaxResultsPerDomain int      `json:"max_results_per_domain,omitempty"`
	DryRun              bool     `json:"dry_run,omitempty"`
}

type PatchAliasSets struct {
	KO     []string `json:"ko,omitempty"`
	EN     []string `json:"en,omitempty"`
	JA     []string `json:"ja,omitempty"`
	VI     []string `json:"vi,omitempty"`
	ZH     []string `json:"zh,omitempty"`
	ZHHant []string `json:"zh_hant,omitempty"`
	ES     []string `json:"es,omitempty"`
	ID     []string `json:"id,omitempty"`
	PTBR   []string `json:"pt_br,omitempty"`
}

type PatchEntityRequest struct {
	EntityType      *string         `json:"entity_type,omitempty"`
	CanonicalEN     *string         `json:"canonical_en,omitempty"`
	CanonicalJA     *string         `json:"canonical_ja,omitempty"`
	CanonicalVI     *string         `json:"canonical_vi,omitempty"`
	CanonicalZH     *string         `json:"canonical_zh,omitempty"`
	CanonicalZHHant *string         `json:"canonical_zh_hant,omitempty"`
	CanonicalES     *string         `json:"canonical_es,omitempty"`
	CanonicalID     *string         `json:"canonical_id,omitempty"`
	CanonicalPTBR   *string         `json:"canonical_pt_br,omitempty"`
	Aliases         *PatchAliasSets `json:"aliases,omitempty"`
	CategoryHint    *string         `json:"category_hint,omitempty"`
	Notes           *string         `json:"notes,omitempty"`
	Status          *string         `json:"status,omitempty"`
	OperatorLocked  *bool           `json:"operator_locked,omitempty"`
}

type LockEntityRequest struct {
	Locked *bool `json:"locked,omitempty"`
}

type LocaleSpellings struct {
	Locale    string   `json:"locale"`
	Canonical string   `json:"canonical,omitempty"`
	Aliases   []string `json:"aliases,omitempty"`
	Spellings []string `json:"spellings"`
}

func NewRouter(pool *pgxpool.Pool) http.Handler {
	return NewRouterWithOptions(pool, RouterOptions{
		RequestTimeout: 10 * time.Second,
		LogRequests:    true,
	})
}

func NewRouterWithOptions(pool *pgxpool.Pool, opts RouterOptions) http.Handler {
	h := &handler{
		store:    &Store{Pool: pool, onEnqueue: opts.OnResearchEnqueue, onReviewParked: opts.OnReviewParked, onDemandCandidate: opts.OnDemandCandidate},
		bgEnrich: enrich.NewBackgroundTrigger(pool),
		corrections: &corrections.Service{
			Pool: pool,
			WD:   wikidata.New(), // 1차: 권위 외부소스 교차검증
			// 2차: 정정 표기 검증("현재값 vs 제안값 중 무엇이 맞나")은 표기/번역
			// 판단이라 codex 로 라우팅(KDB_LLM_CORRECTION, 기본 codex) — disambig·
			// 작품 fill 과 동일 원칙. 비동기(verifyAsync)라 codex 지연도 무방.
			Judge: codexcli.NewRunner().
				WithProvider(codexcli.RoleProvider("CORRECTION", "codex")).
				WithEffort(codexcli.RoleEffort("CORRECTION", "medium")),
		},
	}
	// match 응답 캐시(match_cache.go). 기본 300초. 0 이면 비활성(캐시·합류 모두 우회).
	matchTTL := 300 * time.Second
	if v := strings.TrimSpace(os.Getenv("KDB_MATCH_CACHE_TTL_SECONDS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			matchTTL = time.Duration(n) * time.Second
		}
	}
	h.matchCache = newMatchCache(matchTTL)
	// A8 MatchMissExtractor (flag KDB_MATCH_LLM_EXTRACT). 오너 방침대로 gemma 라우팅
	// (codex 최소). 미설정이면 nil → match-miss 시 아무것도 안 함(기존 동작 불변).
	if os.Getenv("KDB_MATCH_LLM_EXTRACT") == "1" {
		ex := kdb.NewCodexExtractor()
		ex.Runner = ex.Runner.WithProvider(codexcli.RoleProvider("MATCHEXTRACT", "gemma"))
		h.matchExtractor = ex
	}
	// "추측=빈칸" 서빙 게이트(오너 방침). 기본 on, KDB_SERVE_HIDE_LLM_ONLY=0 으로 해제.
	h.hideLLMServe = os.Getenv("KDB_SERVE_HIDE_LLM_ONLY") != "0"
	// match 기사맥락 판별기(disambiguate=true 요청 시만 사용).
	h.matchJudge = newMatchJudge()
	// 번역 재매칭(음차 제목류 miss 전용) — 키 미설정이면 nil(동작 불변).
	h.translator = kdb.NewGTranslator(pool)
	r := chi.NewRouter()
	if opts.RequestTimeout > 0 {
		r.Use(timeoutMiddleware(opts.RequestTimeout))
	}
	if opts.LogRequests {
		r.Use(requestLogMiddleware)
	}
	if pool != nil {
		r.Use((&versionProvider{pool: pool}).middleware)
	}
	// ★규칙 판본을 **모든 응답에** 싣는다 (2026-09-16). 소비자가 캐시한 종결이
	//   낡았는지 스스로 알 수 있는 유일한 신호다 — X-KDB-Version 은 데이터셋 신호라
	//   매 요청 바뀌어서 이 용도로는 못 쓴다. changelog.go 머리말 참조.
	r.Use(rulesHeader)
	r.Get("/v1/health", h.health)
	// 규칙 변경 이력(무인증) — /docs 와 **같은 표**에서 만든다.
	r.Get("/v1/changelog", h.changelog)
	// 공개 API 문서(무인증) — 클라이언트 온보딩용. kdb.aiinplanet.com/docs.
	r.Get("/docs", h.docs)
	r.Get("/v1/docs", h.docs)
	r.Group(func(protected chi.Router) {
		// IP 기반 rate limit: 키 브루트포스 + lookup-miss 비용 증폭(키 유출 시)을
		// 완화. 정상 소비자엔 넉넉(120/분), 자동화 공격엔 충분히 빡빡.
		protected.Use(ratelimit.New(120, time.Minute).Middleware)
		if len(opts.APIKeys) > 0 || pool != nil {
			protected.Use(newAPIKeyAuthenticator(pool, opts.APIKeys).middleware)
		} else {
			// 인증 미들웨어 미설치 = open mode. 이 경우 requireWriteScope 도 통과(tier 없음)
			// → 쓰기/외부행위 엔드포인트까지 무인증 노출. 테스트가 아닌 한 오설정이므로 경고.
			log.Printf("kdb-api: WARNING no API keys AND no DB pool — /v1 is UNAUTHENTICATED (write/site-search open). set KDB_API_KEYS or DB.")
		}
		// 인증 뒤(=더 안쪽)에 배선 — 소비자 컨텍스트가 채워진 뒤 요청을 기록.
		if pool != nil {
			protected.Use(apiRequestLogger(pool))
		}
		protected.Get("/v1/entities", h.listEntities)
		protected.Get("/v1/kentity/entities", h.commonEntities)
		protected.Get("/v1/kentity/entities/{id}", h.commonEntity)
		protected.Post("/v1/entities/match/bulk", h.bulkMatchEntities)
		protected.Post("/v1/entities/match", h.matchEntities)
		protected.Get("/v1/entities/{id}/external-refs", h.getExternalRefs)
		protected.Get("/v1/entities/{id}/relations", h.getRelations)
		// 파괴적/외부행위 엔드포인트는 write 스코프(env 키)만 — 소비자 read 키 차단.
		protected.With(requireWriteScope).Post("/v1/entities/{id}/site-search", h.siteSearchEntity)
		protected.With(requireWriteScope).Patch("/v1/entities/{id}", h.patchEntity)
		protected.With(requireWriteScope).Post("/v1/entities/{id}/lock", h.lockEntity)
		protected.Get("/v1/entities/{id}", h.getEntity)
		protected.Get("/v1/entities/{id}/spellings", h.getSpellings)
		protected.Get("/v1/persons/{id}", h.getPersonDetails)
		protected.Post("/v1/observations", h.createObservation)
		protected.Post("/v1/corrections", h.createCorrection)
		protected.Get("/v1/corrections/{id}", h.getCorrection)
		protected.Post("/v1/prepare", h.prepare)
		protected.Post("/v1/preparations", h.createPreparation)
		protected.Get("/v1/preparations/{id}", h.getPreparation)
		protected.Post("/v1/preparations/{id}/cancel", h.cancelPreparation)
		protected.With(requireWriteScope).Post("/v1/research-queue", h.createResearchQueue)
		protected.With(requireWriteScope).Get("/v1/qa/work", h.qaWork)
		protected.With(requireWriteScope).Post("/v1/qa/result", h.qaResult)
		// 「내가 물었던 것 중 달라진 것」 — 통보를 구조적으로 대신한다(my_changes.go).
		protected.Get("/v1/my/changes", h.myChanges)
		protected.Post("/v1/lookup", h.lookup)
		protected.Post("/v1/lookup/bulk", h.bulkLookup)
	})
	// ★소비자 경로 관대 처리(2026-07-04, 오너 방침 "보내는 대로 받아 처리"): 일부 소비자
	// (kstory 등)가 /api/ prefix 로 호출(예: /api/health) → /v1/ 로 리라이트해 받는다.
	// chi 라우팅 전에 URL.Path 를 바꾸므로 모든 /api/* 가 /v1/* 핸들러로 간다.
	return apiPrefixAlias(r)
}

// apiPrefixAlias — /api/* 요청을 /v1/* 로 리라이트하는 최상단 wrap. 소비자 경로 불일치 흡수.
func apiPrefixAlias(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			rest := strings.TrimPrefix(r.URL.Path, "/api/")
			// base URL 을 /api 로 잡고 client 가 /v1/... 을 붙이면 /api/v1/... 이 온다.
			// 단순 치환은 이걸 /v1/v1/... 으로 만들어 404 를 냈다. 한 번만 정규화한다.
			// (운영 로그 98,489건에 /api/ 접두어 요청 0건 — 되살릴 동작이 아니라 함정 제거다.)
			if rest == "v1" || strings.HasPrefix(rest, "v1/") {
				r.URL.Path = "/" + rest
			} else {
				r.URL.Path = "/v1/" + rest
			}
		}
		next.ServeHTTP(w, r)
	})
}

func timeoutMiddleware(timeout time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), timeout)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func requestLogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Printf("kdb-api method=%s path=%s query=%q status=%d dur=%s",
			r.Method, r.URL.Path, r.URL.RawQuery, rec.status, time.Since(start))
	})
}

// apiKeyAuthenticator — 인바운드 API 키 검증기. .env 정적 키(KDB_API_KEYS)와 DB 발급
// 소비자 키(kwave_kdb_api_consumers, active)를 합집합으로 허용한다. DB 키는 짧은
// TTL 캐시로 읽고, 매칭 시 last_used_at 를 비동기 갱신한다. pool 이 nil 이거나 DB
// 조회가 실패해도 .env 키만으로 안전하게 degrade 한다.
type apiKeyAuthenticator struct {
	pool    *pgxpool.Pool
	envKeys []string

	mu      sync.Mutex
	dbKeys  map[string]string // key_hash(sha256 hex) -> consumer id
	expires time.Time
}

const apiKeyCacheTTL = 30 * time.Second

func newAPIKeyAuthenticator(pool *pgxpool.Pool, envKeys []string) *apiKeyAuthenticator {
	return &apiKeyAuthenticator{pool: pool, envKeys: compactStrings(envKeys)}
}

// keyTier — 인증된 키의 권한 등급. env 정적 키(운영자)는 write(전체), DB 소비자
// 키(외부 매체)는 read 전용. 컨텍스트로 전달돼 requireWriteScope 가 검사한다.
type keyTier string

const (
	tierWrite keyTier = "write"
	tierRead  keyTier = "read"
)

type ctxKey int

const (
	ctxKeyTier ctxKey = iota
	ctxKeyConsumer
)

func (a *apiKeyAuthenticator) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := requestAPIKey(r)
		tier, consumerID, ok := a.classify(r.Context(), key)
		if key == "" || !ok {
			w.Header().Set("WWW-Authenticate", `Bearer realm="kdb-api"`)
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		ctx := context.WithValue(r.Context(), ctxKeyTier, tier)
		if consumerID != "" {
			ctx = context.WithValue(ctx, ctxKeyConsumer, consumerID)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// classify — 키를 검증하고 (등급, 소비자id, ok)를 반환. env 키 = write(소비자id 없음),
// DB 소비자 키 = read(+소비자 uuid). 소비자id는 요청 로그/가시화에 쓰인다.
func (a *apiKeyAuthenticator) classify(ctx context.Context, key string) (keyTier, string, bool) {
	if validAPIKey(key, a.envKeys) {
		return tierWrite, "", true
	}
	if a.pool == nil {
		return "", "", false
	}
	if id, ok := a.lookupDBKey(ctx, key); ok {
		a.touch(id)
		return tierRead, id, true
	}
	return "", "", false
}

// requireWriteScope — write 등급(env 키)만 통과. 소비자(read) 키가 canonical 재작성·
// lock·외부 site-search 같은 파괴적/외부행위 엔드포인트를 호출하지 못하게 막는다.
func requireWriteScope(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// tier 가 없으면 인증 미들웨어 자체가 미설치(키 미설정 = open 모드/테스트)이므로
		// 기존 보안 모델대로 통과. tier 가 있고 write 가 아닐 때(소비자 read 키)만 차단.
		if tier, ok := r.Context().Value(ctxKeyTier).(keyTier); ok && tier != tierWrite {
			writeError(w, http.StatusForbidden, "this endpoint requires a write-scoped key")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *apiKeyAuthenticator) lookupDBKey(ctx context.Context, key string) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.dbKeys == nil || time.Now().After(a.expires) {
		a.refreshLocked(ctx)
	}
	// 캐시 키는 SHA-256(키) 의 hex 다. 제시된 평문이 아니라 해시로 조회하므로
	// 평문은 DB·메모리 어디에도 남지 않고, map 조회 타이밍이 키 자체를 누설하지
	// 않는다(해시를 만들려면 이미 키를 알아야 함). H4.
	id, ok := a.dbKeys[hashConsumerKey(key)]
	return id, ok
}

// refreshLocked — 호출자가 mu 보유. 실패해도 기존 캐시(없으면 빈 맵)를 유지하고 TTL 갱신.
func (a *apiKeyAuthenticator) refreshLocked(ctx context.Context) {
	a.expires = time.Now().Add(apiKeyCacheTTL)
	rows, err := a.pool.Query(ctx, `SELECT id::text, key_hash FROM kwave_kdb_api_consumers WHERE active`)
	if err != nil {
		if a.dbKeys == nil {
			a.dbKeys = map[string]string{}
		}
		return
	}
	defer rows.Close()
	next := make(map[string]string, 16)
	for rows.Next() {
		var id, h string
		if err := rows.Scan(&id, &h); err != nil {
			continue
		}
		next[h] = id // key_hash -> consumer id
	}
	a.dbKeys = next
}

// hashConsumerKey — 소비자 키 → 저장/조회용 SHA-256 hex. kdbadmin 발급 경로와 동일.
func hashConsumerKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// touch — last_used_at 비동기 갱신. 쓰기 증폭 방지를 위해 1시간 이상 경과 시만 UPDATE.
func (a *apiKeyAuthenticator) touch(id string) {
	pool := a.pool
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = pool.Exec(ctx, `UPDATE kwave_kdb_api_consumers SET last_used_at = now() WHERE id = $1::uuid AND (last_used_at IS NULL OR last_used_at < now() - interval '1 hour')`, id)
	}()
}

func requestAPIKey(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("X-KDB-Key")); v != "" {
		return v
	}
	const prefix = "Bearer "
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(auth) >= len(prefix) && strings.EqualFold(auth[:len(prefix)], prefix) {
		return strings.TrimSpace(auth[len(prefix):])
	}
	return ""
}

func validAPIKey(got string, keys []string) bool {
	for _, want := range keys {
		if subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1 {
			return true
		}
	}
	return false
}

// versionProvider — 데이터셋 버전 신호를 캐시해 모든 응답에 X-KDB-Version 헤더로
// 노출한다(2026-06-01). 소비자가 self-heal 로 값이 바뀐 것을 감지·재현 디버깅에 사용.
// 값 = "<entity수>.<max(updated_at) epoch>". 15s TTL 캐시로 per-request DB 부하 방지.
type versionProvider struct {
	pool    *pgxpool.Pool
	mu      sync.Mutex
	val     string
	expires time.Time
}

func (v *versionProvider) version(ctx context.Context) string {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.val != "" && time.Now().Before(v.expires) {
		return v.val
	}
	v.expires = time.Now().Add(15 * time.Second)
	var cnt int64
	var maxUpd *time.Time
	if err := v.pool.QueryRow(ctx, `SELECT count(*), max(updated_at) FROM kwave_entities`).Scan(&cnt, &maxUpd); err == nil {
		ts := int64(0)
		if maxUpd != nil {
			ts = maxUpd.Unix()
		}
		v.val = fmt.Sprintf("%d.%d", cnt, ts)
	}
	return v.val
}

func (v *versionProvider) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		ver := v.version(ctx)
		cancel()
		if ver != "" {
			w.Header().Set("X-KDB-Version", ver)
		}
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

type handler struct {
	store       *Store
	bgEnrich    *enrich.BackgroundTrigger
	corrections *corrections.Service
	// matchExtractor — A8 MatchMissExtractor: /v1/match 의 자유본문이 0건 매칭일 때
	// 본문에서 K-콘텐츠 한글명을 LLM(gemma) 추출해 research 큐에 적재한다(비동기,
	// 핫패스 보호). nil = 비활성(flag KDB_MATCH_LLM_EXTRACT 미설정). lookup-miss 의
	// enqueueDiscovery 와 동등 — match 는 본문이라 추출이 선행돼야 한다.
	matchExtractor kdb.LLMExtractor
	// hideLLMServe — 서빙 시 codex-fallback(순수 LLM 추측) locale 값을 비운다. 오너
	// 방침(2026-07-04): "추측=빈칸" — 지어낸 다국어 표기 대신 빈칸을 준다(정확도 기본).
	// 출처있는 값(wikidata/tmdb/위키언어판/음역/local-usage/매체관측)은 유지.
	// KDB_SERVE_HIDE_LLM_ONLY=0 으로 끌 수 있음(기본 on).
	hideLLMServe bool
	// matchJudge — /v1/entities/match 의 disambiguate=true 시 기사맥락으로 매칭 후보를
	// 검증하는 gemma 판별기(match_disambig.go). nil 이면 판별 스킵(원본 유지).
	matchJudge *agents.Base
	// matchCache — /v1/entities/match 응답 TTL 캐시 + single-flight(match_cache.go).
	// 실측 중복률 75.2%·재호출의 60%가 10초 이내라 도입. nil = 비활성.
	matchCache *matchCache
	// translator — miss 한글 제목류를 Google 번역 v2 로 영문 원형 복원해 1회 재매칭
	// (읽기 경로 전용, 오너 승인 07-15). nil = 비활성(KDB_GTRANSLATE_KEY 미설정).
	translator *kdb.GTranslator
}

func (h *handler) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := h.store.Pool.Ping(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "db ping failed")
		return
	}
	count, err := h.store.CountEntities(ctx)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "entity count failed")
		return
	}
	body := map[string]any{
		"ok":       true,
		"service":  "kdb-api",
		"phase":    "1A",
		"version":  strings.TrimSpace(os.Getenv("KDB_BUILD_VERSION")),
		"entities": count,
	}
	// ★레인 신호를 헬스에 싣는다 (2026-09-20). 「뽑기만 하고 한 건도 못 쓰는 레인」이
	//   있으면 이름을 내보낸다 — 조용한 0건은 이 저장소가 반복해 데인 병이고,
	//   지금까지는 사람이 로그를 뒤져야 찾았다.
	//
	//   헬스에 싣는 이유: 로그는 앱 수명만큼만 살고 아무도 안 본다(backlog-watch 가
	//   같은 이유로 조용했다). 헬스는 **이미 주기적으로 불린다** — 감시자를 새로
	//   만들지 않고 신호를 밖으로 낸다.
	//
	//   이 조회가 헬스를 죽이면 안 된다. 짧은 예산으로 따로 돌리고, 실패하면 필드를
	//   빼고 나간다. 헬스의 본래 답(ok·entities)은 무슨 일이 있어도 나가야 한다.
	lctx, lcancel := context.WithTimeout(r.Context(), 700*time.Millisecond)
	if lanes := kdb.SilentLanes(lctx, h.store.Pool, 24*time.Hour); len(lanes) > 0 {
		body["lanes_silent"] = lanes
	}
	// ★「한 번도 안 돌 레인」도 내보낸다. 원장은 «돌은 것»만 적으므로 그런 레인은
	//   원장에 없고, 없는 것은 보이지 않는다 — 19회차에 mdl-works 가 티커 없이
	//   CLI 로만 돈다는 것을 사람이 손으로 목록을 만들어 비교해서 찾았다.
	if lanes := kdb.MissingLanes(lctx, h.store.Pool, 24*time.Hour); len(lanes) > 0 {
		body["lanes_missing"] = lanes
	}
	lcancel()
	writeJSON(w, http.StatusOK, body)
}

func (h *handler) listEntities(w http.ResponseWriter, r *http.Request) {
	filter, ferr := filterFromRequest(r)
	if ferr != nil {
		writeError(w, http.StatusBadRequest, ferr.Error())
		return
	}
	list, err := h.store.ListEntities(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entities": list})
}

func (h *handler) getEntity(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := uuid.Parse(id); err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	ent, err := h.store.GetEntity(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	writeJSON(w, http.StatusOK, ent)
}

func (h *handler) getSpellings(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := uuid.Parse(id); err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	ent, err := h.store.GetEntity(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	locale := normalizeLocale(r.URL.Query().Get("locale"))
	if locale == "" {
		locale = "all"
	}
	spellings, err := spellingsForLocale(ent, locale)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":        ent.ID,
		"spellings": spellings,
	})
}

func (h *handler) getExternalRefs(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := uuid.Parse(id); err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	refs, err := h.store.ExternalRefs(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"entity_id":     id,
		"external_refs": refs,
	})
}

func (h *handler) getRelations(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := uuid.Parse(id); err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	relations, err := h.store.Relations(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"entity_id": id,
		"relations": relations,
	})
}

func (h *handler) patchEntity(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := uuid.Parse(id); err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req PatchEntityRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	ent, err := h.store.PatchEntity(r.Context(), id, req)
	if err != nil {
		if strings.Contains(err.Error(), "no patch fields") ||
			strings.Contains(err.Error(), "invalid entity type") ||
			strings.Contains(err.Error(), "invalid status") {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "update failed")
		return
	}
	writeJSON(w, http.StatusOK, ent)
}

func (h *handler) lockEntity(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := uuid.Parse(id); err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	req := LockEntityRequest{}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	locked := true
	if req.Locked != nil {
		locked = *req.Locked
	}
	ent, err := h.store.SetEntityLocked(r.Context(), id, locked)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "update failed")
		return
	}
	writeJSON(w, http.StatusOK, ent)
}

func (h *handler) getPersonDetails(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := uuid.Parse(id); err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	person, err := h.store.PersonDetails(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	writeJSON(w, http.StatusOK, person)
}

func (h *handler) createObservation(w http.ResponseWriter, r *http.Request) {
	var req ObservationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	created, promoted, err := h.store.CreateObservation(r.Context(), req)
	if err != nil {
		if strings.Contains(err.Error(), "required") ||
			strings.Contains(err.Error(), "invalid") ||
			strings.Contains(err.Error(), "unsupported locale") {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "insert failed")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"ok":       true,
		"created":  created,
		"promoted": promoted,
	})
}

// createCorrection — 외부 소비자의 locale 표기 정정 신고. 근거기반 자동 반영
// 또는 운영자 심사 큐 적재(내부 판정은 corrections.Service). read 키 소비자도
// 신고 가능 — 자동 반영은 Wikidata 교차검증을 통과한 건에 한하므로 단일 키가
// 임의로 데이터를 바꿀 수 없다.
// logBadCorrection — 교정 요청 400 거부의 원인 진단용 본문 샘플(512B 캡). 키/시크릿은
// 헤더로만 오므로 본문 로깅은 안전. 소비자측 연동 고장(잘못된 페이로드 자동 재시도
// 루프)을 우리 로그만으로 특정할 수 있게 한다.
func logBadCorrection(r *http.Request, body []byte, reason string) {
	sample := string(body)
	if len(sample) > 512 {
		sample = sample[:512] + "…"
	}
	log.Printf("kdb-api: corrections 400 consumer=%s reason=%s body=%q", reporterID(r), reason, sample)
}

func (h *handler) createCorrection(w http.ResponseWriter, r *http.Request) {
	if h.corrections == nil || h.corrections.Pool == nil {
		writeError(w, http.StatusServiceUnavailable, "corrections unavailable")
		return
	}
	// 본문을 버퍼로 읽는다(최대 64KB) — 400 거부 시 원인 진단용 샘플 로깅에 필요.
	// trendbiz 400 루프(10분 주기·7일 113건)가 본문 미기록 탓에 서버측 원인 특정
	// 불가였던 계측 공백 해소(감사 07-25). 교정 본문은 단문이라 64KB 로 충분.
	body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	var req corrections.Request
	if err := json.Unmarshal(body, &req); err != nil {
		logBadCorrection(r, body, "invalid json")
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	// 양방향 확인 경로: KDB 수정안(proposed)에 대한 클라이언트 응답.
	if req.ConfirmID > 0 {
		res, err := h.corrections.Confirm(r.Context(), req.ConfirmID, req.Accept, reporterID(r))
		if err != nil {
			if strings.Contains(err.Error(), "not awaiting") || strings.Contains(err.Error(), "invalid") {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, "confirm failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": res})
		return
	}
	res, err := h.corrections.Submit(r.Context(), req, reporterID(r))
	if err != nil {
		msg := err.Error()
		// 미보유 고유명사에 대한 정정신고 = 발굴 신호. 리젝하지 않고 research 큐로 접수해
		// 존중한다(방침: 모든 신고를 받고 우리쪽이 검토). 노이즈만 게이트로 거른다.
		if strings.Contains(msg, "no active") && strings.TrimSpace(req.Ko) != "" {
			// tombstone 종결(감사 07-25): 이미 결번 판정된 키워드의 정정신고는
			// 재발굴 대신 종결 통지 — preparing 무한 대기를 만들지 않는다.
			// ★탈출구(같은 날 실사례 "나는 반딧불" — 게이트 문장형 오판으로 실존 히트곡이
			// 기각돼 있었다): 근거 URL 을 동반한 신고는 tombstone 을 우회해 재심 경로로
			// 보낸다. 오거부=최상위 금칙 — 종결 통지가 오판을 영구화해선 안 된다.
			if strings.TrimSpace(req.EvidenceURL) == "" && h.store.Tombstoned(r.Context(), req.Ko) {
				h.logRequestTerms(r, "correction", []loggedTerm{{Ko: strings.TrimSpace(req.Ko),
					Status: "out_of_scope", SourceURL: req.EvidenceURL}})
				writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": map[string]any{
					"status": "out_of_scope", "resolution": "검토 종결된 키워드 — K-엔티티 아님 판정(재조회 불필요).",
				}})
				return
			}
			// ★소비자가 보낸 근거와 사유를 게이트까지 들고 간다(2026-09-16).
			//   여기서 버리고 있었다. 그래서 정정신고가 "근거 없는 라틴 문자열"로
			//   보였고, latin_passthrough 규칙이 오늘만 7개 용어를 문 앞에서 돌려보냈다
			//   — 멜론 앨범·위버스 공식 공지·MBC 기사를 달고 온 신고였다. 근거는
			//   logRequestTerms 로 **판정 뒤에** 기록만 하고 있어서, 원장에는 남고
			//   판단에는 안 쓰이는 상태였다.
			res, _ := h.store.EnqueueResearchDetailed(r.Context(), ResearchQueueRequest{
				EntityKO:  strings.TrimSpace(req.Ko),
				SourceURL: strings.TrimSpace(req.EvidenceURL),
				// 신고 사유는 문맥 단서다. 게이트의 유형 추론(ResolvedType)이 이것을 읽는다.
				ContextHint: strings.TrimSpace(req.Reason),
				Origin:      "correction-miss",
			})
			// ★오너 계약(2026-07-13): "없으면 준비". 근거 부족분은 자동 검증이 근거를
			// 수집해 발굴로 이어가므로 소비자에게는 접수(preparing)로 답한다.
			status, resolution := "preparing", "미보유 키워드 — 자동 검증 후 발굴 진행(준비되면 표기 제공)."
			if res.Decision.Verdict == gatekeeper.IntakePass {
				status, resolution = "queued", "미보유 고유명사 — 발굴 큐 접수(준비 후 표기 제공)."
			} else if res.Decision.Verdict == gatekeeper.IntakeReject {
				status, resolution = "rejected_precheck", "고유명사 입력 규칙에서 기각되어 외부 조사를 시작하지 않음."
			}
			h.logRequestTerms(r, "correction", []loggedTerm{{Ko: strings.TrimSpace(req.Ko), Status: status,
				SourceURL: req.EvidenceURL}})
			writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "result": map[string]any{
				"status": status, "resolution": resolution,
				"precheck_reason": res.Decision.ReasonCode,
			}})
			return
		}
		if strings.Contains(msg, "required") || strings.Contains(msg, "invalid") ||
			strings.Contains(msg, "unsupported") || strings.Contains(msg, "ambiguous") ||
			strings.Contains(msg, "not found") {
			logBadCorrection(r, body, msg)
			// ★거부 사유를 DB 에도 남긴다(2026-07-31). logBadCorrection 은 앱 로그 전용이라
			// 컨테이너 재시작에 사라진다 — 07-25 하츄핑 400 버스트(5시간·108건, 같은 키워드
			// 54회씩 재시도)의 사유를 사후에 특정하지 못한 것이 정확히 이 이유였다.
			// "인입 상시 파악"(오너 지시)은 사유가 남아야 성립한다. mig0092 경로 재사용.
			h.logRequestTerms(r, "correction", []loggedTerm{{
				Ko: strings.TrimSpace(req.Ko), Status: "rejected_400:" + msg,
				SourceURL: req.EvidenceURL,
			}})
			writeError(w, http.StatusBadRequest, msg)
			return
		}
		writeError(w, http.StatusInternalServerError, "correction failed")
		return
	}
	// ★판정은 성공이다 — 기각도 포함해서 (2026-09-23).
	//
	//   종전엔 `rejected` 를 **422** 로 보냈다. 본문은 `ok:true` 에 판정이 담긴 정상
	//   응답인데 상태코드만 오류였다. 상태코드로 성공/실패를 가르는 흔한 클라이언트는
	//   그 응답을 통째로 버린다 — 그러면 «왜 기각됐는지»를 못 읽고, 판정을 기억하지
	//   못하니 같은 신고를 또 보낸다. 실제로 그렇게 됐다: 글로벌 미디어파인이
	//   「나 혼자 산다」ja 를 **10회** 재전송했다(09-19~09-22). 우리 쪽 재사용 로직이
	//   LLM 재호출은 막았지만, 애초에 다시 오지 않아도 될 신고였다.
	//
	//   요청을 처리하지 못한 것이 아니라 **처리해서 «아니다»라고 답한 것**이므로 200 이다.
	//   4xx 는 소비자가 고칠 것이 있을 때만 쓴다(형식 오류·권한·없는 대상).
	status := http.StatusAccepted // queued
	if res.Status == "auto_applied" || res.Status == "rejected" {
		status = http.StatusOK
	}
	h.logRequestTerms(r, "correction", []loggedTerm{{Ko: strings.TrimSpace(req.Ko), Status: res.Status,
		SourceURL: req.EvidenceURL}})
	writeJSON(w, status, map[string]any{"ok": true, "result": res})
}

// getCorrection — 정정 처리 상태 폴링(verifying → auto_applied/proposed/queued/rejected).
func (h *handler) getCorrection(w http.ResponseWriter, r *http.Request) {
	if h.corrections == nil || h.corrections.Pool == nil {
		writeError(w, http.StatusServiceUnavailable, "corrections unavailable")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	st, ok := h.corrections.Get(r.Context(), id)
	if !ok {
		writeError(w, http.StatusNotFound, "correction not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": st})
}

// reporterID — 신고자 식별(평문 키 미저장). 운영자(write 키)는 "operator",
// 소비자(read 키)는 키 해시 prefix. 인증 미설치(open) 시 "anon".
func reporterID(r *http.Request) string {
	if tier, ok := r.Context().Value(ctxKeyTier).(keyTier); ok {
		if tier == tierWrite {
			return "operator"
		}
		if k := requestAPIKey(r); k != "" {
			return "consumer:" + hashConsumerKey(k)[:12]
		}
	}
	return "anon"
}

func (h *handler) createResearchQueue(w http.ResponseWriter, r *http.Request) {
	var req ResearchQueueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	req.Origin = "direct-api"
	res, err := h.store.EnqueueResearchDetailed(r.Context(), req)
	if err != nil {
		if strings.Contains(err.Error(), "required") ||
			strings.Contains(err.Error(), "invalid") {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "enqueue failed")
		return
	}
	status := http.StatusCreated
	if !res.Queued {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{
		"ok":               true,
		"queued":           res.Queued,
		"inserted":         res.Inserted,
		"precheck_status":  res.Decision.Verdict,
		"precheck_reason":  res.Decision.ReasonCode,
		"precheck_version": res.Decision.RuleVersion,
	})
}

func (h *handler) siteSearchEntity(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	entityID, err := uuid.Parse(id)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req SiteSearchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	locale := normalizeLocale(req.Locale)
	if locale == "" {
		writeError(w, http.StatusBadRequest, "locale required")
		return
	}
	if _, _, err := entityLocaleColumns(locale); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := h.store.SiteSearchEntity(r.Context(), entityID, SiteSearchRequest{
		Locale:              locale,
		Query:               req.Query,
		Domains:             req.Domains,
		LimitDomains:        req.LimitDomains,
		MaxResultsPerDomain: req.MaxResultsPerDomain,
		DryRun:              req.DryRun,
	})
	if err != nil {
		if strings.Contains(err.Error(), "not found") ||
			strings.Contains(err.Error(), "required") ||
			strings.Contains(err.Error(), "unsupported") ||
			strings.Contains(err.Error(), "no ") {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "site search failed")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *handler) lookup(w http.ResponseWriter, r *http.Request) {
	var req LookupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	req.Query = strings.TrimSpace(req.Query)
	req.KID = strings.TrimSpace(req.KID)
	if req.KID == "" && looksLikeKID.MatchString(req.Query) {
		req.KID = req.Query // 이름 자리에 kid 를 넣어도 받는다
	}
	if req.KID == "" && req.Query == "" {
		writeError(w, http.StatusBadRequest, "query required")
		return
	}
	// ★자체 ID 로 물으면 **확정 한 건**이다. 이름 매칭·발굴·동명 후보 제시를 전부 건너뛴다.
	//   이 문이 동명이인을 근본적으로 없앤다(I03).
	if req.KID != "" {
		ent, err := h.store.GetEntityByKID(r.Context(), req.KID)
		if err != nil {
			writeJSON(w, http.StatusOK, LookupResponse{
				Query: req.KID, Matches: []Entity{}, Status: "miss"})
			return
		}
		attachLocaleProvenance(&ent)
		if req.VerifiedOnly {
			applyLocaleVerifiedGate(&ent)
		}
		writeJSON(w, http.StatusOK, LookupResponse{
			Query: req.KID, Matches: []Entity{ent}, Status: "found"})
		return
	}
	// ★보낸 type 을 조용히 삼키지 않는다 (2026-09-23 PressLocale 신고 3).
	//   `persson` 오타가 200 miss 로 나가면 소비자는 이미 있는 대상을 새로 등록
	//   요청한다. 미상 표시(unknown·term)는 «유형을 모르겠다»는 뜻이므로 필터에서 뺀다.
	typeFilter, typeOK := consumerTypeFilter(strings.TrimSpace(req.Type))
	if !typeOK {
		writeErrorCode(w, http.StatusBadRequest, "invalid_type",
			"type 이 유형 목록에 없습니다: "+strings.TrimSpace(req.Type)+" (/docs §7-1 의 목록을 보세요. 모르면 빼고 보내시면 됩니다)")
		return
	}
	matches, err := h.store.ListEntities(r.Context(), EntityFilter{
		Query:  req.Query,
		Type:   typeFilter,
		Status: req.Status,
		Limit:  req.Limit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	// ★정확일치와 부분일치를 여기서 가른다 — **게이트보다 먼저**. verified_only /
	//   hide-llm 게이트는 로케일 칸을 비우므로, 그 뒤에 가르면 «일본어 표기로 맞은
	//   대상»이 이름이 지워진 채 부분일치로 강등된다(lookup_contract.go).
	matches, related := splitExactMatches(matches, req.Query)
	// 유형 필터가 같은 이름을 가렸는가 — 가렸으면 «있는데 유형이 다르다»를 알린다.
	typeHidden := false
	if len(matches) == 0 && typeFilter != "" {
		if all, aerr := h.store.ListEntities(r.Context(), EntityFilter{
			Query: req.Query, Status: req.Status, Limit: req.Limit,
		}); aerr == nil {
			if hidden := hiddenByTypeFilter(all, req.Query); len(hidden) > 0 {
				related = append(hidden, related...)
				typeHidden = true
			}
		}
	}
	// 응답은 즉시 — 빈 locale 있는 match 는 background goroutine 에서 enrich.
	// 다음 lookup 부터 채워진 값 반환. 부분일치도 대상이므로 함께 본다.
	if h.bgEnrich != nil {
		for _, m := range append(append([]Entity{}, matches...), related...) {
			if hasEmptyPriorityLocale(m) {
				if id, err := uuid.Parse(m.ID); err == nil {
					h.bgEnrich.Trigger(id)
				}
			}
		}
	}
	// tombstone 종결(감사 07-25): 검토가 끝나 '결번' 판정된 키워드는 번역 재매칭·
	// 재발굴 없이 out_of_scope 로 종결 통지. 소비자 무한 재폴링 차단.
	tombstoned := false
	if len(matches) == 0 && h.store.Tombstoned(r.Context(), req.Query) {
		tombstoned = true
	}
	// 발굴 트리거 (2026-06-01): KDB 에 없는 이름이면 research_queue 에 적재 →
	// research worker 가 on-demand 검색으로 발굴. 핫패스를 막지 않게 async.
	//
	// ★부분일치뿐인 응답("주이"→주이재/이주원, "카카오"→카카오뱅크)도 여기로 온다.
	//   요청한 이름 자체는 미보유이기 때문이다 — 종전에도 발굴 신호는 이 기준으로
	//   보내고 있었다(서빙만 found 라 답했다).
	if len(matches) == 0 && !tombstoned {
		// 번역 재매칭(음차 제목류, 오너 승인 07-15): "스테이 디스 웨이"→"Stay This Way".
		ent, translateHit, _ := h.translateRematch(r.Context(), req.Query, typeFilter)
		if translateHit == "active" {
			// 번역으로 대상을 확정한 것이라 정확일치로 친다(이름 글자는 다르다).
			matches = append(matches, ent)
		} else {
			h.enqueueDiscovery(req.Query, typeFilter)
			if translateHit == "rejected" {
				go func(ko string) {
					// enqueueDiscovery(async) 가 row 를 만든 뒤 오거부 후보 플래그.
					time.Sleep(3 * time.Second)
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					h.store.FlagResearchTranslateRejected(ctx, ko)
				}(req.Query)
			}
		}
	}
	// 게이트는 답(matches)과 참고(related) 양쪽에 **같이** 건다. 한쪽만 걸면
	// 참고 칸이 미검증 값의 우회로가 된다.
	for _, set := range [][]Entity{matches, related} {
		// "추측=빈칸" 서빙 게이트(오너 방침, 2026-07-04): codex 추측 locale 값을 항상 비운다.
		// 출처있는 값(위키/음역/검색확정/매체)은 유지. verified_only(엄격)와 독립·선행.
		if h.hideLLMServe {
			for i := range set {
				stripLLMOnlyLocales(&set[i])
			}
		}
		// verified_only 게이트 (2026-06-29): 미검증 locale 값 제거 + provenance 부착.
		// enrich/발굴 트리거는 위에서 실제 DB 상태로 이미 수행됨(게이트는 응답 직전에만 적용).
		if req.VerifiedOnly {
			for i := range set {
				applyLocaleVerifiedGate(&set[i])
			}
		}
		// ★게이트 **뒤에** 붙인다. verified_only 로 비워진 칸도 "없음"이다 —
		//   게이트 앞에서 계산하면 소비자가 받은 응답과 이유가 어긋난다.
		if req.IncludeAbsent {
			for i := range set {
				set[i].AbsentLocales = absentLocalesFor(set[i], normalizePrepareLocales(req.Locales))
			}
		}
		// ★출처는 묻지 않아도 말해 준다 (2026-09-15). 서빙되는 영문의 33%가 기계번역인데,
		//   소비자는 verified_only 를 쓰지 않는 한 그 사실을 알 방법이 없었다.
		for i := range set {
			attachLocaleProvenance(&set[i])
		}
	}
	if matches == nil {
		matches = []Entity{} // miss 는 에러가 아니라 **빈 배열**이다(문서 §6-3-1). null 을 보내지 않는다.
	}
	lookupStatus := lookupStatusFor(matches)
	if lookupStatus == "miss" && tombstoned {
		lookupStatus = "out_of_scope"
	}
	loggedStatus := lookupStatus
	if typeHidden {
		// ★인입 기록에 남긴다. 「보유한 이름인데 우리 유형과 소비자 유형이 다르다」는
		//   우리 원장의 유형이 틀렸을 수 있다는 신호다 — 실제로 카카오(event_tour)·
		//   삼성전자(brand_place)가 이 신호로 드러났다. 화면에서 모아 볼 수 있어야
		//   레인이 재판정할 대상을 고를 수 있다.
		loggedStatus = "type_mismatch:" + related[0].EntityType
	}
	h.logRequestTerms(r, "lookup", []loggedTerm{{Ko: req.Query, Type: req.Type, Status: loggedStatus}})
	writeJSON(w, http.StatusOK, LookupResponse{
		Query: req.Query, Matches: matches, Related: related, Status: lookupStatus})
}

// loggedTerm — 소비자 요청 본문의 항목 1개(기록용).
type loggedTerm struct {
	Ko, Type, Status, SourceURL string
	HasContext                  bool
}

// logRequestTerms — 요청 "내용"(기사URL·키워드·type·응답상태)을 항목 단위로 기록한다
// (mig 0092, admin /admin/ondemand/requests). 오너 지시(07-13): 매체가 보낸 내용을
// 기사별 요청처럼 보고 빠르게 검토. 핫패스 비차단(async) + 실패 무해(best-effort).
func (h *handler) logRequestTerms(r *http.Request, origin string, terms []loggedTerm) {
	if h.store == nil || h.store.Pool == nil || len(terms) == 0 {
		return
	}
	consumer := reporterID(r)
	group := uuid.New()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		rows := make([][]any, 0, len(terms))
		for _, t := range terms {
			rows = append(rows, []any{group, consumer, origin, t.SourceURL, t.Ko, t.Type, t.HasContext, t.Status})
		}
		_, _ = h.store.Pool.CopyFrom(ctx, pgx.Identifier{"kwave_kdb_request_terms"},
			[]string{"request_group", "consumer_id", "origin", "source_url", "term_ko", "term_type", "has_context", "item_status"},
			pgx.CopyFromRows(rows))
	}()
}

// ★hasNormalizedHit 도 splitExactMatches 로 흡수했다 (2026-09-23). 발굴 신호는
//   원래 이 기준으로 «없다»고 판정하고 있었는데 서빙만 found 라 답했다 — 이제 한
//   판정으로 둘 다 정한다(lookup_contract.go). 이쪽은 ko·en 만 봐서, 일본어 표기로
//   물어 맞은 대상을 «부분일치»로 세어 발굴 큐에 헛것을 넣던 자리이기도 하다.

// enqueueDiscovery — lookup miss 한 이름을 발굴 큐에 넣는다(게이트 통과분만, async).
// match(자유 본문)에는 적용 안 함 — 문장에서 이름을 추출할 수 없으므로(그건 RSS 추출기 몫).
//
// ★소비자가 보낸 **유형을 같이 넘긴다** (2026-09-15).
//
//	종전엔 이름만 넘겼다. 그래서 lookup miss 로 들어온 것은 전부 유형 미상이 되고,
//	게이트가 `missing_or_unsupported_type` 으로 review 에 쌓았다 —
//	그 사유로 쌓인 190건 중 **끝내 채워진 것은 0건**이다.
//
//	정작 소비자들은 유형을 붙여 보내고 있었다(최근 3일 prepare 기준 미디어파인 100% ·
//	presslocale 99.8% · kstory 100% · issuetalk 100%). 받아 놓고 **우리가 버렸다.**
//	유형이 있는 요청은 채움률이 62%인데(5,943 중 3,695), 유형을 잃으면 0%가 된다.
func (h *handler) enqueueDiscovery(query, entityType string) {
	if h.store == nil || h.store.Pool == nil {
		return
	}
	if strings.TrimSpace(query) == "" {
		return
	}
	if !validEntityType(entityType) {
		entityType = ""
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = h.store.EnqueueResearch(ctx, ResearchQueueRequest{
			EntityKO:            strings.TrimSpace(query),
			RequestedEntityType: entityType,
			Origin:              "lookup-miss",
		})
	}()
}

// prepare — 받기 + 빠른 준비. 외부가 기사에 등장할 한글 고유명사를 미리 던지면:
//   - 이미 있고 요청 locale 다 채워짐 → ready (즉시 사용 가능)
//   - 있지만 빈 locale → 백그라운드 enrich 즉시 트리거(orchestrator: TMDb/Wikidata/
//     codex) → preparing. 조회 시점엔 채워져 있음.
//   - 없음 → 발굴 큐 등록 → new (분류·enrich 파이프라인 진입)
//
// 사람 개입(펜딩) 없이 자동 준비된다.
func (h *handler) prepare(w http.ResponseWriter, r *http.Request) {
	var req PrepareRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if len(req.Terms) == 0 {
		writeError(w, http.StatusBadRequest, "terms required")
		return
	}
	if len(req.Terms) > 200 {
		req.Terms = req.Terms[:200]
	}
	want := normalizePrepareLocales(req.Locales)
	items := make([]PrepareItem, 0, len(req.Terms))
	logTerms := make([]loggedTerm, 0, len(req.Terms))
	// 번역 재매칭 fresh 호출 예산(요청당) — 외부 API 지연이 10s 요청 타임아웃을
	// 위협하지 않게 제한. 캐시 히트는 예산을 쓰지 않는다.
	translateBudget := 6
	// 예산을 넘긴 miss 항은 응답 후 백그라운드로 번역 캐시를 워밍(전략5, 2026-07-25)
	// — 다음 폴에서 캐시 히트로 즉시 재매칭. 종전엔 폴당 6건씩 여러 폴에 걸쳐 해소됐다.
	var translatePrefetch []string
	// ★P4.01 입력 계약 — 알려진 이름이 있어도 새 동명을 놓치지 않는다.
	//   표제어 전체를 **한 번에** 조회한다(요청당 1회, 실측 2.2ms/200건).
	kos := make([]string, 0, len(req.Terms))
	for _, raw := range req.Terms {
		if t := parsePrepareTerm(raw); t.Ko != "" {
			kos = append(kos, t.Ko)
		}
	}
	commonHomonyms := h.store.CommonHomonyms(r.Context(), kos)
	for _, raw := range req.Terms {
		pt := parsePrepareTerm(raw)
		if pt.Ko == "" {
			continue
		}
		// 요청 "내용" 기록용 — 소비자가 보낸 원본 type 힌트를 보존(blank 처리 전).
		sentType := pt.Type
		// ★오너 방침(2026-07-04): 요청은 다 받는다. type 힌트가 KDB 의 K-콘텐츠 type 이 아니어도
		//   (예: place) 거부하지 않고, 잘못된 힌트만 비워 unknown 으로 접수한다 — 발굴·분류가 판단.
		if pt.Type != "" && !validEntityType(pt.Type) {
			pt.Type = ""
		}
		// 타입힌트는 하드필터가 아니라 소프트 선호(2026-07-21). 소비자 오힌트(예: person
		// 인데 실재는 channel_outlet/movie)로 진짜 엔티티를 매칭 전에 제외하던 버그 제거 —
		// 모든 타입을 가져와 exactKoMatch 가 힌트 우선·미스면 canonical_ko 로 해소하고,
		// prepareMatchSafe 가 동명이인 오서빙만 차단(오너 방침 07-04 "오힌트 관용").
		matches, err := h.store.ListEntities(r.Context(), EntityFilter{Query: pt.Ko, Status: "active", Limit: 5})
		if err != nil {
			items = append(items, PrepareItem{Term: pt.Ko, Type: pt.Type, Status: "new"})
			logTerms = append(logTerms, loggedTerm{Ko: pt.Ko, Type: sentType, Status: "new",
				SourceURL: firstNonEmpty(pt.SourceURL, req.SourceURL), HasContext: pt.Context != "" || req.Context != ""})
			continue
		}
		// canonical_ko 정확 일치(또는 alias 일치) 우선 — 부분일치 노이즈 배제.
		// type 힌트가 있으면 그 type 우선.
		ent, ok := exactKoMatch(matches, pt.Ko, pt.Type)
		if ok && !prepareMatchSafe(matches, ent, pt.Ko, pt.Type) {
			ok = false // 동명이인 애매 — 오서빙보다 preparing("틀린값보다 빈칸")
		}
		translateHit := ""
		if !ok && translateBudget > 0 {
			// 번역 재매칭(음차 제목류, 오너 승인 07-15). fresh API 호출은 요청당
			// 예산 내로만 — 나머지는 다음 요청에서 캐시로 해소(10s 타임아웃 보호).
			var tEnt Entity
			var fresh bool
			if tEnt, translateHit, fresh = h.translateRematch(r.Context(), pt.Ko, pt.Type); translateHit == "active" {
				ent, ok = tEnt, true
			}
			if fresh {
				translateBudget--
			}
		} else if !ok {
			translatePrefetch = append(translatePrefetch, pt.Ko)
		}
		if !ok {
			// ★오너 방침(2026-07-04): "요청은 다 받아, 거부하지 마 — 판단은 우리가 한다."
			//   소비자가 보낸 term 은 입구에서 게이트키퍼로 거부하지 않고 전부 발굴 큐로 받는다.
			//   K-엔티티 여부·오염은 발굴·분류·Wikidata 검증이 최종 판단(아니면 그때 reject).
			//   무효 입력(빈값·1글자·기호만)만 제외 — 이건 거부가 아니라 처리 불가 입력.
			if strings.TrimSpace(pt.Ko) != "" {
				// tombstone 종결(감사 07-25): 이미 rejected/merged 판정된 키워드는
				// preparing(무한 대기 신호) 대신 out_of_scope 로 종결 통지 — 소비자
				// 재폴링과 autoverify 재조사 예산 낭비를 함께 끊는다.
				if h.store.Tombstoned(r.Context(), pt.Ko) {
					items = append(items, PrepareItem{Term: pt.Ko, Type: pt.Type, Status: "out_of_scope"})
					logTerms = append(logTerms, loggedTerm{Ko: pt.Ko, Type: sentType, Status: "out_of_scope",
						SourceURL: firstNonEmpty(pt.SourceURL, req.SourceURL), HasContext: pt.Context != "" || req.Context != ""})
					continue
				}
				srcURL := pt.SourceURL
				if srcURL == "" {
					srcURL = req.SourceURL // batch 레벨 폴백
				}
				contextHint := pt.Context
				if contextHint == "" {
					contextHint = req.Context
				}
				res, _ := h.store.EnqueueResearchDetailed(r.Context(), ResearchQueueRequest{
					EntityKO: pt.Ko, RequestedEntityType: pt.Type, SourceURL: srcURL,
					ContextHint: contextHint, Origin: "prepare",
				})
				// 번역 원형이 rejected 와 일치 — 게이트 오거부 후보 플래그(점검용, 상태 불변).
				if translateHit == "rejected" {
					go func(ko string) {
						ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
						defer cancel()
						h.store.FlagResearchTranslateRejected(ctx, ko)
					}(pt.Ko)
				}
				// ★오너 계약(2026-07-13): "없으면 준비". 근거 부족(review)은 보류가 아니라
				// KDB 가 자동 검증(IntakeAutoVerifier)으로 근거를 수집해 발굴로 넘어가는
				// 상태이므로 소비자에게는 preparing 으로 답한다. 명백한 쓰레기만 out_of_scope.
				itemStatus := "preparing"
				if res.Decision.Verdict == gatekeeper.IntakePass {
					itemStatus = "new"
				} else if res.Decision.Verdict == gatekeeper.IntakeReject {
					itemStatus = "out_of_scope"
				}
				// ★이미 끝난 발굴이면 그 결말로 답한다 (2026-09-15).
				//   종전엔 같은 낱말을 다시 물어도 **그 낱말에 무슨 일이 있었는지 안 보고**
				//   게이트 판정만 새로 계산해 매번 `preparing` 을 돌려줬다. 큐에 답이
				//   적혀 있는데 읽지 않았다.
				itemResolution := ""
				if st, why, done := statusForFinishedResearch(
					h.store.LastResearchOutcome(r.Context(), pt.Ko)); done {
					itemStatus, itemResolution = st, why
				}
				items = append(items, PrepareItem{Term: pt.Ko, Type: pt.Type,
					Status: itemStatus, Resolution: itemResolution})
				logTerms = append(logTerms, loggedTerm{Ko: pt.Ko, Type: sentType, Status: itemStatus,
					SourceURL: srcURL, HasContext: contextHint != ""})
			} else {
				items = append(items, PrepareItem{Term: pt.Ko, Type: pt.Type, Status: "out_of_scope"})
			}
			continue
		}
		// "추측=빈칸"(오너 방침): codex 추측 locale 은 값에서 빼고 missing(준비중)으로 —
		// 다음 enrich 에서 출처있는 값으로 채워질 때까지 노출 보류.
		if h.hideLLMServe {
			stripLLMOnlyLocales(&ent)
		}
		values, prov, missing := localeValuesAndGaps(ent, want, req.VerifiedOnly)
		it := PrepareItem{Term: pt.Ko, Type: ent.EntityType, EntityID: ent.ID, KID: ent.KID,
			Values: values, Provenance: prov, Missing: missing,
			HomonymRisk: commonHomonyms[pt.Ko]}
		// 소스 소진 locale 은 '기다려도 안 채워짐'을 함께 통지(빈칸 유지 정책과 양립 —
		// 값을 지어내지 않되, 무한 재폴링은 끊는다).
		if len(missing) > 0 {
			it.Unavailable = h.store.ExhaustedLocales(r.Context(), ent.ID, missing)
			// ★missing 은 '없다'만 말한다. **왜**와 **그래서 무엇을**을 같이 준다 —
			//   그게 없어서 소비자가 제목을 음역해 버렸다(실측 6,640칸).
			it.AbsentLocales = absentLocalesFor(ent, missing)
		}
		// readiness 는 소비자가 요청한 locale 만 기준(미지정 시 코어 en) — 오너 결정
		// 2026-07-21 "소비자별 요청 locale만". en 있으면 즉시 ready 로 서빙하고 나머지
		// locale 은 Missing[] 로 알린다("8개 다 차야 ready"가 en 보유 엔티티를 preparing
		// 에 묶던 문제 제거). 반환 payload(values) 는 want 그대로 — 축소하지 않음.
		if prepareReady(missing, req.Locales) {
			it.Status = "ready"
		} else if prepareAllMissingExhausted(missing, it.Unavailable) {
			// 빈 칸이 **전부** 소진됐다. 이미 Unavailable 로 알리고는 있었지만 status 가
			// `preparing` 이면 소비자가 읽는 것은 "다시 물어보라"다 — 필드와 상태가
			// 서로 다른 말을 하고 있었다.
			it.Status = "unfillable"
			it.Resolution = "요청한 locale 이 현재 소스로는 모두 소진됐습니다 — 재조회해도 채워지지 않습니다. 근거 URL 을 정정신고로 보내주시면 재심합니다."
		} else {
			it.Status = "preparing"
		}
		// 미충족 locale 은 ready 여부와 무관하게 즉시 백그라운드 enrich(코어만 차도 보강 지속).
		if len(missing) > 0 && h.bgEnrich != nil {
			if id, err := uuid.Parse(ent.ID); err == nil {
				h.bgEnrich.Trigger(id)
			}
		}
		items = append(items, it)
		logTerms = append(logTerms, loggedTerm{Ko: pt.Ko, Type: sentType, Status: it.Status,
			SourceURL: firstNonEmpty(pt.SourceURL, req.SourceURL), HasContext: pt.Context != "" || req.Context != ""})
	}
	h.logRequestTerms(r, "prepare", logTerms)
	// ★제안 표기를 적어 둔다 (2026-09-23 배선). savePrepareSuggestions 는 09-15 에
	//   「소비자가 실제로 쓰는 문은 /v1/prepare 다」라며 만들어 놓고 **아무 데서도 부르지
	//   않았다.** 그래서 문서 §6-1 이 "제안을 재료로 받는다"고 약속한 채 표는 0행이었다
	//   (실측 09-23: kwave_kdb_suggested_names 전체 0행). 한 소비자가 제안을 보낸 뒤
	//   「우리 제안이 canonical 로 되돌아온다」고 신고했는데, 확인해 보니 제안은 저장조차
	//   되지 않았다 — 값이 같아 보인 것은 잠정 채움 레인이 같은 음역을 만든 것이었다.
	if h.store != nil && h.store.Pool != nil {
		var sugTerms []PrepareTerm
		for _, raw := range req.Terms {
			if t := parsePrepareTerm(raw); t.Ko != "" && len(t.Suggestions) > 0 {
				sugTerms = append(sugTerms, t)
			}
		}
		if len(sugTerms) > 0 {
			resolved := make(map[string]string, len(items))
			for _, it := range items {
				if it.EntityID != "" {
					resolved[it.Term] = it.EntityID
				}
			}
			savePrepareSuggestions(r.Context(), h.store.Pool, req, sugTerms, resolved)
		}
	}
	if len(translatePrefetch) > 0 && h.translator != nil {
		go h.prefetchTranslations(translatePrefetch)
	}
	response := PrepareResponse{Items: items}
	h.trackPreparation(r, req, &response)
	writeJSON(w, http.StatusOK, response)
}

// prefetchTranslations — prepare 번역예산(요청당 6)을 넘긴 miss 항을 응답 후 백그라운드로
// 번역해 캐시에 적재(전략5, 2026-07-25). 다음 폴에서 캐시 히트로 즉시 재매칭된다.
// 상한 30건 — 대량 배치가 외부 번역 API 비용을 증폭하지 않게.
func (h *handler) prefetchTranslations(terms []string) {
	if len(terms) > 30 {
		terms = terms[:30]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	for _, ko := range terms {
		if ctx.Err() != nil {
			return
		}
		_, _, _ = h.translateRematch(ctx, ko, "")
	}
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// parsePrepareTerm — terms 원소를 문자열 또는 {ko,type} 객체로 파싱.
func parsePrepareTerm(raw json.RawMessage) PrepareTerm {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return PrepareTerm{Ko: strings.TrimSpace(s)}
	}
	var o PrepareTerm
	if json.Unmarshal(raw, &o) == nil {
		// ★Suggestions 를 함께 옮긴다 (2026-09-24). 종전엔 네 필드만 복사해 소비자가 보낸
		//   제안이 **여기서 사라졌다** — 저장 배선(savePrepareSuggestions)을 이었어도 받을
		//   것이 없었다. 배포 뒤 prepare 598건 · 소비자 3곳, 제안 표 0행으로 드러났다.
		return PrepareTerm{
			Ko: strings.TrimSpace(o.Ko), Type: strings.TrimSpace(o.Type),
			SourceURL: strings.TrimSpace(o.SourceURL), Context: strings.TrimSpace(o.Context),
			Suggestions: o.Suggestions,
		}
	}
	return PrepareTerm{}
}

// normalizePrepareLocales — 빈값이면 주요 8개, 아니면 정규화된 요청 locale.
func normalizePrepareLocales(in []string) []string {
	if len(in) == 0 {
		return []string{"en", "ja", "vi", "id", "es", "pt_br", "zh", "zh_hant"}
	}
	out := make([]string, 0, len(in))
	for _, l := range in {
		out = append(out, strings.ReplaceAll(normalizeLocale(l), "-", "_"))
	}
	return out
}

// prepareReady — ready 판정: 소비자가 요청한 locale(정규화) 이 모두 채워졌는가.
// 미지정 시 코어(en)만 기준(오너 결정 2026-07-21). missing 은 반환 locale(want) 기준
// 빈칸 목록이고 required ⊆ want 이므로 required 를 missing 집합에 대조해 판정한다.
func prepareReady(missing []string, reqLocales []string) bool {
	required := reqLocales
	if len(required) == 0 {
		required = []string{"en"}
	}
	miss := make(map[string]bool, len(missing))
	for _, m := range missing {
		miss[m] = true
	}
	for _, loc := range required {
		if miss[strings.ReplaceAll(normalizeLocale(loc), "-", "_")] {
			return false
		}
	}
	return true
}

// exactKoMatch — canonical_ko 정확 일치(또는 alias_ko 포함) entity 1건. typeHint
// 가 있으면 그 type 을 우선(동명이인이 type 다를 때 정확도↑).
// translateRematch — miss 한글 제목류를 번역 원형으로 1회 재매칭한다(읽기 경로 전용,
// 오너 승인 07-15). 반환 hit: "active"(ent 유효·서빙 가능) | "rejected"(오거부 후보,
// ent 무효 — 플래그용) | ""(불발). fresh 는 이번에 실제 번역 API 를 호출했는가(예산용).
// 모든 실패는 조용히 "" — 기존 miss 흐름을 절대 막지 않는다.
func (h *handler) translateRematch(ctx context.Context, ko, typeHint string) (ent Entity, hit string, fresh bool) {
	if h.translator == nil {
		return Entity{}, "", false
	}
	translated, fresh, err := h.translator.TitleToEN(ctx, ko, typeHint)
	if err != nil {
		log.Printf("kdb.gtranslate: ko=%q: %v", ko, err)
		return Entity{}, "", fresh
	}
	if translated == "" {
		return Entity{}, "", fresh
	}
	matches, err := h.store.ListEntities(ctx, EntityFilter{Query: translated, Status: "active", Limit: 5})
	if err == nil {
		if m, ok := exactAnyMatch(matches, translated, typeHint); ok {
			// 오매칭 가드: 동명 영문제목의 '다른 한글 고유제목' 작품이면 기각
			// (실측: 광장→The Square 가 더 스퀘어에 오매칭). 틀린값보다 빈칸.
			if !kdb.GTranslateSafeHit(ko, m.CanonicalKO, m.Aliases.KO) {
				log.Printf("kdb.gtranslate: rematch 기각(동명 의심) ko=%q en=%q vs canonical_ko=%q", ko, translated, m.CanonicalKO)
				return Entity{}, "", fresh
			}
			log.Printf("kdb.gtranslate: rematch hit ko=%q en=%q entity=%s", ko, translated, m.ID)
			return m, "active", fresh
		}
	}
	// 오거부 후보: 번역 원형이 rejected 로 존재하면 게이트 오거부일 수 있다(§5 점검용 플래그).
	rej, err := h.store.ListEntities(ctx, EntityFilter{Query: translated, Status: "rejected", Limit: 3})
	if err == nil {
		if _, ok := exactAnyMatch(rej, translated, ""); ok {
			log.Printf("kdb.gtranslate: rejected hit ko=%q en=%q (오거부 후보)", ko, translated)
			return Entity{}, "rejected", fresh
		}
	}
	return Entity{}, "", fresh
}

// FlagResearchTranslateRejected — 번역 원형이 rejected 엔티티와 정확 일치한 miss
// 키워드에 오거부 점검 플래그를 남긴다(리포트 전용 — 큐 상태·엔티티 불변).
func (s *Store) FlagResearchTranslateRejected(ctx context.Context, ko string) {
	_, _ = s.Pool.Exec(ctx, `
UPDATE kwave_entity_research_queue
   SET precheck_flags = array_append(precheck_flags, 'translate_hit_rejected')
 WHERE entity_ko = $1 AND NOT ('translate_hit_rejected' = ANY(precheck_flags))`, ko)
}

// exactAnyMatch — 대소문자 무시 정확 일치(canonical ko/en + aliases ko/en).
// ILIKE 부분일치 결과의 오매칭을 걸러낸다(번역 재매칭은 정확 일치만 신뢰).
// typeHint 가 있으면 그 type 우선, 없으면 첫 정확 일치.
func exactAnyMatch(matches []Entity, q, typeHint string) (Entity, bool) {
	isExact := func(m Entity) bool {
		if strings.EqualFold(m.CanonicalKO, q) || strings.EqualFold(m.CanonicalEN, q) {
			return true
		}
		for _, a := range m.Aliases.KO {
			if strings.EqualFold(a, q) {
				return true
			}
		}
		for _, a := range m.Aliases.EN {
			if strings.EqualFold(a, q) {
				return true
			}
		}
		return false
	}
	if typeHint != "" {
		for _, m := range matches {
			if m.EntityType == typeHint && isExact(m) {
				return m, true
			}
		}
	}
	for _, m := range matches {
		if isExact(m) {
			return m, true
		}
	}
	return Entity{}, false
}

func exactKoMatch(matches []Entity, term, typeHint string) (Entity, bool) {
	if typeHint != "" {
		for _, m := range matches {
			if m.CanonicalKO == term && m.EntityType == typeHint {
				return m, true
			}
		}
	}
	for _, m := range matches {
		if m.CanonicalKO == term {
			return m, true
		}
	}
	for _, m := range matches {
		for _, a := range m.Aliases.KO {
			if a == term {
				return m, true
			}
		}
	}
	// 정규화(공백·문장부호·대소문자 무시) 동치 폴백 — 인테이크 dedup 과 같은 규칙.
	// "쇼 미 더 머니"/"6시내고향"처럼 표기 변형만 다른 요청이 exact 미스로 영원히
	// preparing 에 갇히던 교착 해소. exact 우선순위는 위에서 이미 소진된 뒤라 안전.
	if key := gatekeeper.NormalizedKey(term); key != "" {
		if typeHint != "" {
			for _, m := range matches {
				if m.EntityType == typeHint && gatekeeper.NormalizedKey(m.CanonicalKO) == key {
					return m, true
				}
			}
		}
		for _, m := range matches {
			if gatekeeper.NormalizedKey(m.CanonicalKO) == key {
				return m, true
			}
		}
		for _, m := range matches {
			for _, a := range append(append([]string{}, m.Aliases.KO...), m.Aliases.EN...) {
				if gatekeeper.NormalizedKey(a) == key {
					return m, true
				}
			}
		}
	}
	return Entity{}, false
}

// prepareMatchSafe — 타입힌트 하드필터를 소프트 선호로 바꾼(2026-07-21) 뒤의 오서빙 가드.
// 힌트와 다른 타입이어도 같은 canonical_ko(또는 alias_ko) 의 active 엔티티가 단 하나면
// 서빙(소비자 오힌트 관용). 동명이인으로 여럿이면 힌트가 정확히 한 타입을 집었을 때만
// 안전 — 애매하면 false 로 preparing 유지("틀린값보다 빈칸"). matches 는 active 만.
func prepareMatchSafe(matches []Entity, ent Entity, term, typeHint string) bool {
	if ent.NeedsDisambig {
		return false
	}
	// 정규화 동치로 센다(exact 포함) — exactKoMatch 가 정규화 폴백으로 잡은 엔티티도
	// 동명이인 카운트에 들어가야 "표기 변형 요청은 가드 미적용" 구멍이 안 생긴다.
	key := gatekeeper.NormalizedKey(term)
	koHit := func(m Entity) bool {
		if m.CanonicalKO == term || (key != "" && gatekeeper.NormalizedKey(m.CanonicalKO) == key) {
			return true
		}
		for _, a := range m.Aliases.KO {
			if a == term || (key != "" && gatekeeper.NormalizedKey(a) == key) {
				return true
			}
		}
		return false
	}
	var koMatches, typeMatches int
	for _, m := range matches {
		if !koHit(m) {
			continue
		}
		koMatches++
		if typeHint != "" && m.EntityType == typeHint {
			typeMatches++
		}
	}
	if koMatches <= 1 {
		return true // 단일 엔티티 — 힌트가 틀려도 오서빙 아님
	}
	return typeHint != "" && ent.EntityType == typeHint && typeMatches == 1
}

// localeValuesAndGaps — 요청 locale 의 현재 값 map 과 빈 locale 목록.
// localeSourceFor — locale 의 raw source 컬럼값(canonical_<loc>_source) 반환.
func localeSourceFor(e Entity, loc string) string {
	switch loc {
	case "en":
		return e.CanonicalENSource
	case "ja":
		return e.CanonicalJASource
	case "vi":
		return e.CanonicalVISource
	case "zh":
		return e.CanonicalZHSource
	case "zh_hant":
		return e.CanonicalZHHantSource
	case "es":
		return e.CanonicalESSource
	case "id":
		return e.CanonicalIDSource
	case "pt_br":
		return e.CanonicalPTBRSource
	}
	return ""
}

// localeProvenanceLabel — localeProvenanceExpr(SQL, MatchEntitiesForLocale) 의 Go 미러.
// 반환 locale 값 자체의 출처 라벨을 매긴다. match(SQL)·lookup/prepare(Go) 두 경로의
// provenance 정의를 한 곳에서 일치시켜 드리프트를 막는다. source 미기록(”) 레거시 행은
// 엔티티 전역 휴리스틱(provenanceExpr: wikidata url / ≥2 매체도메인 / wikipedia url) 폴백.
func localeProvenanceLabel(e Entity, source string) string {
	if e.OperatorLocked {
		return "operator-locked"
	}
	switch source {
	case "operator-locked", "operator":
		return "operator-locked"
	case "wikidata-label":
		return "wikidata-label"
	case "tmdb", "musicbrainz", "kofic", "kmdb", "naver-people", "correction-verified", "netflix", "disney", "itunes", "discogs":
		return "external-db"
	case "media-consensus":
		return "media-consensus"
	case "romanization":
		return "romanization"
	case "kana-rule":
		// 가타카나 결정 변환(오너 승인 폴백티어) — 기계번역 아님(규칙). llm-only 와 달리
		// 서빙에서 스트립 안 함(빈칸 대신 출처표기된 규칙값 노출). verified_only 게이트 제외.
		return "rule-transliteration"
	case "kowiki-hanja":
		// 한국어 위키백과 첫 문장의 한자 병기. 위키백과 본문에서 온 값이라 위키 계열로 묶는다.
		return "wikipedia-langlinks"
	case "opencc":
		return "opencc"
	case "mydramalist":
		return "community-db"
	case "gtranslate":
		// 기계번역 폴백(오너 방침 2026-07-16) — llm-only 와 달리 서빙에서 스트립하지
		// 않는다(빈칸 대신 출처표기된 MT 노출). verified_only 게이트에서는 제외.
		return "machine-translation"
	case "gtranslate-raw":
		// 게이트가 흠을 잡은 기계번역(2026-09-14 방침). 빈칸 대신 내보내되 **가장 약한
		// 등급**임을 이름으로 말한다 — 소비자가 이것만 보고 발행할지 스스로 정한다.
		return "machine-translation-ungated"
	case "codex-fallback":
		return "llm-only"
	case "llm-provisional":
		// 근거 검색이 실패한 칸을 LLM 으로 **잠정** 채운 값(prio 9 — 가장 약하다).
		// codex-fallback 과 한 이름으로 묶으면 소비자가 둘을 구분할 수 없다:
		// codex-fallback 은 근거 검색 전에 나온 값이고, 이것은 근거를 찾다 실패한
		// 뒤에 «빈칸보다는 낫다»로 채운 값이다. 무엇에든 밀린다.
		return "llm-provisional"
	case "":
		if len(e.SourceDomains) >= 2 {
			return "media-consensus"
		}
		for _, u := range e.SourceURLs {
			if strings.Contains(strings.ToLower(u), "wikidata") {
				return "wikidata-label"
			}
		}
		for _, u := range e.SourceURLs {
			if strings.Contains(strings.ToLower(u), "wikipedia") {
				return "wikipedia-langlinks"
			}
		}
		return "llm-only"
	}
	if strings.HasPrefix(source, "wikipedia") {
		return "wikipedia-langlinks"
	}
	if strings.HasPrefix(source, "rss-observation") {
		return "media-single"
	}
	return "llm-only"
}

// verifiedProvenances — verified_only 게이트 통과 provenance 집합. localeVerifiedExpr(SQL)
// 와 동치: operator-locked|wikidata-label|external-db|media-consensus 만 검증으로 친다
// (romanization/opencc/wikipedia-langlinks/media-single/community-db/llm-only 는 제외).
var verifiedProvenances = map[string]bool{
	"operator-locked": true, "wikidata-label": true, "external-db": true, "media-consensus": true,
}

func provenanceIsVerified(prov string) bool { return verifiedProvenances[prov] }

// stripLLMOnlyLocales — provenance 가 llm-only(codex-fallback 순수 추측)인 canonical
// locale 값을 비운다(오너 방침 "추측=빈칸", 2026-07-04). verified_only(엄격 게이트)와 달리
// 위키 언어판·음역·검색확정·매체관측 등 출처있는 값은 유지한다 — LLM 이 지어낸 값만 제거.
// canonical_ko 는 정본이라 대상 아님. 서빙 3경로(lookup/prepare/match) 공통 정책.
func stripLLMOnlyLocales(e *Entity) {
	fields := []struct {
		val *string
		loc string
	}{
		{&e.CanonicalEN, "en"}, {&e.CanonicalJA, "ja"}, {&e.CanonicalVI, "vi"},
		{&e.CanonicalZH, "zh"}, {&e.CanonicalZHHant, "zh_hant"}, {&e.CanonicalES, "es"},
		{&e.CanonicalID, "id"}, {&e.CanonicalPTBR, "pt_br"},
	}
	for _, f := range fields {
		if strings.TrimSpace(*f.val) == "" {
			continue
		}
		if localeProvenanceLabel(*e, localeSourceFor(*e, f.loc)) == "llm-only" {
			*f.val = "" // 추측값 → 빈칸(omitempty 라 응답에서 사라짐)
		}
	}
}

// applyLocaleVerifiedGate — verified_only lookup 용: 각 locale 값에 provenance 라벨을 달고
// (LocaleProvenance), 미검증 출처 값은 비운다(omitempty 라 응답에서 사라짐 → 소비자는
// 검증된 표기만 받는다). canonical_ko 는 정본이라 게이트 대상 아님.
func applyLocaleVerifiedGate(e *Entity) {
	prov := map[string]string{}
	// ★locale 목록은 attachLocaleProvenance 와 **공유한다**(localeValueFields).
	//   따로 적어 두면 한쪽에만 locale 이 늘었을 때 그 칸이 출처 없이 나간다.
	for _, f := range localeValueFields(e) {
		if strings.TrimSpace(*f.val) == "" {
			continue
		}
		p := localeProvenanceLabel(*e, localeSourceFor(*e, f.loc))
		if !provenanceIsVerified(p) {
			*f.val = "" // 미검증 — 비움(빈칸>틀린값)
			continue
		}
		prov[f.loc] = p
	}
	if len(prov) > 0 {
		e.LocaleProvenance = prov
	}
}

// localeValuesAndGaps — want locale 별 (값, provenance, 미준비목록). verifiedOnly=true 면
// 미검증 출처(codex/romanization/opencc/wikipedia/rss/community) 값은 값으로 내지 않고
// missing 으로 분류한다(소비자에겐 "준비중"으로 보여 검증 표기만 노출). provenance 맵은
// 실제 반환된 값에만 채운다.
func localeValuesAndGaps(e Entity, want []string, verifiedOnly bool) (map[string]string, map[string]string, []string) {
	get := map[string]string{
		"en": e.CanonicalEN, "ja": e.CanonicalJA, "vi": e.CanonicalVI,
		"id": e.CanonicalID, "es": e.CanonicalES, "pt_br": e.CanonicalPTBR,
		"zh": e.CanonicalZH, "zh_hant": e.CanonicalZHHant,
	}
	values := map[string]string{}
	prov := map[string]string{}
	var missing []string
	for _, loc := range want {
		if _, known := get[loc]; !known {
			continue // 미지원 locale — 조용히 무시(기존 동작)
		}
		v := strings.TrimSpace(get[loc])
		if v != "" {
			p := localeProvenanceLabel(e, localeSourceFor(e, loc))
			if !verifiedOnly || provenanceIsVerified(p) {
				values[loc] = v
				prov[loc] = p
				continue
			}
			// 값은 있으나 미검증(verifiedOnly) — 아래 en-폴백/missing 으로.
		}
		// 빈칸 또는 미검증. 모든 비-en 로케일은 en 표기로 폴백 서빙(오너 2026-07-21, 홀드 제거):
		// en-copy 를 DB 에 쓰지 않고(설계 원칙) 서빙 시점에만 en 값을 provenance='en-fallback'
		// 로 노출 → ready. 라틴권은 로마자가 자연 표기이고, CJK 도 라벨된 로마자 폴백은 "틀린
		// ja/zh 이름"이 아니라 정직한 폴백(가짜값 아님)이며 홀드보다 낫다. 네이티브(공식/MT음차)
		// 가 우선이고, 못 채운 잔여만 폴백 — native 는 비동기 소스로 자동 업그레이드된다.
		if isFallbackLocale(loc) {
			env := strings.TrimSpace(get["en"])
			if env != "" {
				enp := localeProvenanceLabel(e, localeSourceFor(e, "en"))
				if !verifiedOnly || provenanceIsVerified(enp) {
					values[loc] = env
					prov[loc] = "en-fallback"
					continue
				}
			}
		}
		missing = append(missing, loc)
	}
	return values, prov, missing
}

// isFallbackLocale — en 폴백 대상. **라틴 문자 로케일만.**
//
// 한국 고유명사의 표기가 없을 때 로마자(=en)로 폴백해 홀드를 없앤다
// (오너 2026-07-21). vi/es/id/pt_br 는 라틴 문자를 쓰므로 «Kim Soo-hyun» 이
// 그 로케일에서 실제로 통용되는 형태다 — 정직한 폴백이다.
//
// ★ja/zh/zh_hant 는 뺀다 (2026-09-18).
//
//	실측: verified_only 로 서울대학교를 물으면 이렇게 나갔다.
//
//	    "zh": "Seoul National University"   provenance "en-fallback"
//
//	중국어 소비자에게 영어가 중국어라고 나간다. 우리 채움 프롬프트는
//	"NEVER copy the English title into a non-English locale — a field equal to
//	the English title is a TRANSLATION FAILURE" 라고 못 박아 두고, 서빙에서
//	그걸 하고 있었다. 한자/가나 문화권에서 로마자는 «아직 못 찾았다»가 아니라
//	«틀린 표기》다.
//
//	오너 지시(2026-09-17): "우리쪽에서 공식을 사용을 못찾으면 그대로 놔두어야
//	llm 직번역이라도 할수 있도록". 빈칸이면 번역 쪽이 자기 번역을 쓴다.
//	영어를 넣어 두면 그 기회마저 막는다.
func isFallbackLocale(loc string) bool {
	switch loc {
	case "vi", "es", "id", "pt_br":
		return true
	}
	return false
}

// looksLikeEntityName — 발굴 큐 적재 게이트(prepare/lookup 공용). 자율 파이프라인과
// 동일한 gatekeeper.PreGate 를 써서, 외부 API 로 들어오는 노이즈(문장/명령형/일반어
// 꼬리/난수/깨진자소/일반어구)를 진입 시점에 거른다. PreReject = 등록 안 함
// (out_of_scope). PreGray/Keep = 발굴 파이프라인 진입(이후 classify 가 keep/reject).
// 정밀 검증은 worker 의 Wikidata 이름검증 + classify 가 담당.
// basicNameSanity — 이름의 최소 형태(2자+·글자 1개 이상)만 검사. 게이트키퍼 노이즈필터
// 없이, 소비자가 명시 type 으로 큐레이션해 보낸 term 을 발굴 큐에 넣을지 판단(발굴·Wikidata
// 검증이 최종). looksLikeEntityName 의 앞부분과 동일하되 PreGate 는 적용하지 않는다.
func basicNameSanity(q string) bool {
	runes := []rune(strings.TrimSpace(q))
	if len(runes) < 2 {
		return false
	}
	for _, r := range runes {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

func looksLikeEntityName(q string) bool {
	q = strings.TrimSpace(q)
	runes := []rune(q)
	if len(runes) < 2 { // 빈값/1글자
		return false
	}
	hasLetter := false
	for _, r := range runes {
		if unicode.IsLetter(r) { // 숫자/기호만(123, !!!) 거름
			hasLetter = true
			break
		}
	}
	if !hasLetter {
		return false
	}
	// 자율 파이프라인과 동일 게이트: 문장/명령형/일반어꼬리/난수/깨진자소 PreReject.
	return gatekeeper.PreGate(q).Verdict != gatekeeper.PreReject
}

// hasEmptyPriorityLocale — 8개 외국어 (en/ja/vi/id/es/pt-br/zh-hant/zh) 중 빈 칸 있나.
func hasEmptyPriorityLocale(e Entity) bool {
	if e.Status != "active" {
		return false
	}
	return e.CanonicalEN == "" || e.CanonicalJA == "" || e.CanonicalVI == "" ||
		e.CanonicalID == "" || e.CanonicalES == "" || e.CanonicalPTBR == "" ||
		e.CanonicalZHHant == "" || e.CanonicalZH == ""
}

func (h *handler) bulkLookup(w http.ResponseWriter, r *http.Request) {
	var req BulkLookupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if len(req.Queries) == 0 {
		writeError(w, http.StatusBadRequest, "queries required")
		return
	}
	if len(req.Queries) > 50 {
		writeError(w, http.StatusBadRequest, "queries limit is 50")
		return
	}
	queries := parseBulkQueries(req.Queries, req.Type)
	out := BulkLookupResponse{Results: make([]LookupResponse, 0, len(queries))}
	for _, bq := range queries {
		q := bq.Ko
		// 조회할 수 없는 항목도 **자리를 지킨다**(순서로 짝짓는 소비자 보호).
		if bq.Err != nil {
			out.Results = append(out.Results, LookupResponse{
				Query: q, Matches: []Entity{}, Status: bq.Err.Code, Error: bq.Err})
			continue
		}
		matches, err := h.store.ListEntities(r.Context(), EntityFilter{
			Query:  q,
			Type:   bq.Type, // 항목별 유형이 묶음 기본값을 덮는다
			Status: req.Status,
			Limit:  req.Limit,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "query failed")
			return
		}
		// 단건과 **같은 자리, 같은 규칙**으로 정확일치와 부분일치를 가른다(게이트 앞).
		matches, related := splitExactMatches(matches, q)
		// 단건과 같이, 유형 필터가 가린 같은 이름을 알린다.
		if len(matches) == 0 && bq.Type != "" {
			if all, aerr := h.store.ListEntities(r.Context(), EntityFilter{
				Query: q, Status: req.Status, Limit: req.Limit,
			}); aerr == nil {
				if hidden := hiddenByTypeFilter(all, q); len(hidden) > 0 {
					related = append(hidden, related...)
				}
			}
		}
		if len(matches) == 0 {
			h.enqueueDiscovery(q, bq.Type)
		}
		// 단건 lookup 과 같은 게이트를 같은 자리(응답 직전)에 건다. 발굴 트리거는
		// 위에서 실제 DB 상태로 이미 돌았다 — 게이트가 그것을 가리면 안 된다.
		for _, set := range [][]Entity{matches, related} {
			if req.VerifiedOnly {
				for i := range set {
					applyLocaleVerifiedGate(&set[i])
				}
			}
			if req.IncludeAbsent {
				for i := range set {
					set[i].AbsentLocales = absentLocalesFor(set[i], normalizePrepareLocales(req.Locales))
				}
			}
			// 단건과 같은 자리에 출처 라벨을 붙인다 — 권장 경로에만 없으면 권장을 따를수록 잃는다.
			for i := range set {
				attachLocaleProvenance(&set[i])
			}
		}
		if matches == nil {
			matches = []Entity{}
		}
		// 종결 통지도 단건과 같이 준다. 없으면 소비자가 miss 와 out_of_scope 를
		// 구분 못 해 결번 키워드를 무한 재조회한다.
		status := lookupStatusFor(matches)
		if status == "miss" && h.store.Tombstoned(r.Context(), q) {
			status = "out_of_scope"
		}
		out.Results = append(out.Results, LookupResponse{
			Query: q, Matches: matches, Related: related, Status: status})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handler) matchEntities(w http.ResponseWriter, r *http.Request) {
	var req MatchEntitiesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	req.SourceText = strings.TrimSpace(req.SourceText)
	req.Locale = normalizeLocale(req.Locale)
	if req.SourceText == "" {
		writeError(w, http.StatusBadRequest, "source_text required")
		return
	}
	if req.Locale == "" {
		writeError(w, http.StatusBadRequest, "locale required")
		return
	}
	if s := strings.TrimSpace(req.Status); s != "" && !validEntityStatus(s) {
		writeError(w, http.StatusBadRequest, "invalid status")
		return
	}
	// 캐시/합류(match_cache.go) — 소비자가 같은 기사 본문을 반복 전송한다(실측 중복 75.2%,
	// 재호출의 60%가 10초 이내 동시 요청). 조회+판별 전체를 한 단위로 감싸야 8초대 꼬리를
	// 만드는 disambiguate 까지 절약된다. 미스일 때만 아래 compute 가 1회 실행된다.
	entities, err, cached := h.matchCache.do(matchCacheKey(req), func() ([]MatchedEntity, error) {
		ents, qerr := h.store.MatchEntitiesForLocale(r.Context(), req)
		if qerr != nil {
			return nil, qerr
		}
		// 기사맥락 판별(disambiguate=true, 오너 방향): 기사 본문으로 gemma 가 매칭 후보를 검증해
		// 실제로 그 K-엔티티로 언급된 것만 남긴다(일반어·오매칭 제거). 핫패스 보호: opt-in·타임아웃·
		// 실패 시 원본 유지. 결과 0건이면 A8 발굴 트리거로 자연 연결.
		if req.Disambiguate && len(ents) > 0 && h.matchJudge != nil {
			// 판별 예산은 고정 8s 가 아니라 "요청에 남은 시간 - 여유분"으로 잡는다.
			// 고정 8s 는 요청 타임아웃(기본 10s)의 80% 라, 앞단 조회가 조금만 길어져도
			// 판별이 끝나기 전에 요청이 죽어 소비자에겐 504 로만 보였다(2026-08-04 실측
			// p99 8.6s — 8s 판별 상한이 그대로 꼬리가 됨). 남은 예산에서 잘라 쓰면
			// 응답은 항상 타임아웃 안에서 나가고, 판별이 늦으면 원본 매칭이 반환된다.
			if budget := disambiguateBudget(r.Context()); budget > 0 {
				dctx, dcancel := context.WithTimeout(r.Context(), budget)
				ents = disambiguateMatches(dctx, h.matchJudge, req.SourceText, ents)
				dcancel()
			}
		}
		// "추측=빈칸"(오너 방침): 반환 locale_name 의 출처가 codex 추측이면 표기를 비운다.
		// 소비자는 엔티티는 매칭됐으나 검증된 다국어 표기는 아직 없음을 안다(빈칸>틀린값).
		// LocaleSource 는 effectiveSourceExpr 로 실제 반환값(en 폴백 포함)의 출처를 반영.
		// ★캐시 안쪽에서 적용한다 — 캐시된 슬라이스는 요청 간 공유되므로 밖에서 원소를
		// 수정하면 공유 상태를 건드리게 된다. 여기서 최종 서빙형으로 굳혀 저장한다.
		if h.hideLLMServe {
			for i := range ents {
				if ents[i].LocaleSource == "codex-fallback" {
					ents[i].LocaleName = ""
					ents[i].LocaleFallback = false
					ents[i].LocaleAbsent = "llm_only"
				}
			}
		}
		// ★빈칸에 이유와 할 일을 붙인다 (2026-09-14).
		//   종전엔 빈칸이 아무 말도 안 했고, 소비자는 한글을 그대로 발행했다.
		//   이제 "왜 비었는지"와 "그럼 무엇을 하라"를 함께 말한다.
		//   지어내 주지는 않는다 — 즉석 생성은 같은 이름을 기사마다 다르게 만든다.
		for i := range ents {
			switch {
			case ents[i].LocaleName == "":
				if ents[i].LocaleAbsent == "" {
					ents[i].LocaleAbsent = "no_value"
				}
			case ents[i].LocaleFallback:
				// ★영어 폴백도 "없음"이다 (2026-09-14 실측에서 드러난 구멍).
				//   vi 를 물었는데 `Love Is Coming`·`Jo Se-rim` 이 나간다. locale_name 이
				//   비어 있지 않으니 처음 구현은 여기에 아무 안내도 안 붙였다.
				//   그런데 **소비자가 조치해야 하는 자리는 바로 여기다** — 그대로 쓰면
				//   베트남어 기사에 영어가 박힌다. locale_fallback 만으로는 "그래서
				//   무엇을 하라"가 없다. 영어를 **참고값**으로 주고 할 일을 함께 말한다.
				ents[i].LocaleAbsent = "fallback_en"
			default:
				continue
			}
			ents[i].FillHint = kdb.LocaleFillHint(ents[i].EntityType)
		}
		return ents, nil
	})
	if err != nil {
		if strings.Contains(err.Error(), "unsupported locale") {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	// A8: 0건 매칭이면 본문에서 K-콘텐츠 한글명을 추출해 발굴 큐에 적재(비동기).
	// ★캐시 히트면 건너뛴다 — 같은 본문이 79회까지 재전송되므로 매번 적재하면 동일
	// 키워드를 발굴 큐에 중복으로 쌓는다(TTL 안에서는 1회면 충분).
	if len(entities) == 0 && !cached {
		h.enqueueFromText(req.SourceText, req.Locale)
	}
	// M06/I06/I07: 문맥 없이 이름만 왔는데 그 이름이 둘 이상의 UUID 로 갈리면 서버가
	// 대신 고르지 않는다. 신뢰도 1위(=유명한 쪽)나 첫 행을 정답처럼 돌려주면 소비자는
	// 그걸 그대로 저장하고, 잘못된 UUID 에 근거가 쌓인다(I05 위반). ambiguous 로 알리고
	// 후보를 모두 준다 — 고르는 책임은 문맥을 가진 쪽에 있다.
	resp := MatchEntitiesResponse{Entities: entities}
	if cands := ambiguousCandidates(req.SourceText, entities); len(cands) > 0 {
		resp.Status, resp.Candidates = MatchStatusAmbiguous, cands
	}
	writeJSON(w, http.StatusOK, resp)
}

// enqueueFromText — A8 MatchMissExtractor: match 자유본문이 0건일 때 본문에서 K-콘텐츠
// 한글명을 LLM(gemma) 추출 → 게이트 통과분을 research 큐에 적재(ContextHint=match-miss).
// 핫패스를 막지 않게 async. extractor 미설정(flag off) 이면 no-op. lookup-miss 의
// enqueueDiscovery 와 동등하되, match 는 본문이라 이름 추출이 선행된다.
func (h *handler) enqueueFromText(sourceText, locale string) {
	if h.matchExtractor == nil || h.store == nil || h.store.Pool == nil {
		return
	}
	sourceText = strings.TrimSpace(sourceText)
	if sourceText == "" {
		return
	}
	go func() {
		// gemma timeout 240s 방침과 정합 — 추출은 비핫패스라 넉넉히.
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		spellings, err := h.matchExtractor.Extract(ctx, kdb.ExtractInput{
			Locale:      locale,
			Description: sourceText,
		})
		if err != nil || len(spellings) == 0 {
			return
		}
		seen := map[string]bool{}
		for _, sp := range spellings {
			ko := strings.TrimSpace(sp.KoHint)
			if ko == "" || seen[ko] {
				continue
			}
			seen[ko] = true
			_, _ = h.store.EnqueueResearch(ctx, ResearchQueueRequest{
				EntityKO:    ko,
				ContextHint: sourceText,
				Origin:      "match-miss",
			})
		}
	}()
}

func (h *handler) bulkMatchEntities(w http.ResponseWriter, r *http.Request) {
	var req BulkMatchEntitiesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	req.Locale = normalizeLocale(req.Locale)
	if req.Locale == "" {
		writeError(w, http.StatusBadRequest, "locale required")
		return
	}
	if len(req.SourceTexts) == 0 {
		writeError(w, http.StatusBadRequest, "source_texts required")
		return
	}
	if len(req.SourceTexts) > 50 {
		writeError(w, http.StatusBadRequest, "source_texts limit is 50")
		return
	}
	if s := strings.TrimSpace(req.Status); s != "" && !validEntityStatus(s) {
		writeError(w, http.StatusBadRequest, "invalid status")
		return
	}
	out := BulkMatchEntitiesResponse{Results: make([]BulkMatchResult, 0, len(req.SourceTexts))}
	for _, text := range req.SourceTexts {
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		entities, err := h.store.MatchEntitiesForLocale(r.Context(), MatchEntitiesRequest{
			SourceText:    text,
			Locale:        req.Locale,
			Limit:         req.Limit,
			MinConfidence: req.MinConfidence,
			Status:        req.Status,
			VerifiedOnly:  req.VerifiedOnly,
		})
		if err != nil {
			if strings.Contains(err.Error(), "unsupported locale") {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, "query failed")
			return
		}
		// bulk 도 같은 M06 규칙을 적용한다. 여기만 빠지면 소비자가 단건 대신 bulk 로
		// 같은 이름을 물어 자동 선택을 되살릴 수 있다(같은 소비자·같은 위험).
		res := BulkMatchResult{SourceText: text, Entities: entities}
		if cands := ambiguousCandidates(text, entities); len(cands) > 0 {
			res.Status, res.Candidates = MatchStatusAmbiguous, cands
		}
		out.Results = append(out.Results, res)
	}
	writeJSON(w, http.StatusOK, out)
}

// filterFromRequest — 조회 조건을 만든다. 해석하지 못한 커서는 **오류로 돌려준다**.
//
// 종전에는 형식이 틀린 updated_since 를 조용히 버리고 전체 조회로 진행했다. 델타를
// 받으려던 소비자는 200 과 함께 전건을 받고, 그걸 "그 시각 이후 변경분"으로 믿는다.
// 커서가 깨졌다는 사실만 사라지고 잘못된 의미가 남는다. 빈 커서(미지정)는 그대로 전체다.
// (운영 로그 98,489건에 updated_since 사용 0건 — 실사용 회귀 없이 고칠 수 있다.)
func filterFromRequest(r *http.Request) (EntityFilter, error) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	minConf, _ := strconv.ParseFloat(q.Get("min_confidence"), 64)
	var since time.Time
	if v := strings.TrimSpace(q.Get("updated_since")); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return EntityFilter{}, fmt.Errorf("updated_since must be RFC3339")
		}
		since = t
	}
	return EntityFilter{
		Query:         q.Get("q"),
		Type:          q.Get("type"),
		Status:        q.Get("status"),
		Limit:         limit,
		Offset:        offset,
		MinConfidence: minConf,
		UpdatedSince:  since,
	}, nil
}

func (f EntityFilter) normalized() EntityFilter {
	f.Query = strings.TrimSpace(f.Query)
	f.Type = strings.TrimSpace(f.Type)
	f.Status = strings.TrimSpace(f.Status)
	if f.Status == "" {
		f.Status = "active"
	}
	if f.Limit <= 0 {
		f.Limit = defaultLimit
	}
	if f.Limit > maxLimit {
		f.Limit = maxLimit
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	if f.MinConfidence < 0 {
		f.MinConfidence = 0
	}
	if f.MinConfidence > 1 {
		f.MinConfidence = 1
	}
	return f
}

func (r MatchEntitiesRequest) normalized() MatchEntitiesRequest {
	r.SourceText = strings.TrimSpace(r.SourceText)
	r.Locale = normalizeLocale(r.Locale)
	r.Status = strings.TrimSpace(r.Status)
	// 미지정 시 active 로 강제 — lookup(EntityFilter.normalized) 과 대칭.
	// 이 기본값이 없으면 rejected merge-tombstone(예: '카리나' 죽은 중복본 conf0.97)
	// 이 active 정본보다 먼저 소비자에 반환되는 유출이 발생한다. 전체 tier 전수
	// 감사는 match 가 아니라 /v1/entities (status=&type 필터) 로 한다.
	if r.Status == "" {
		r.Status = "active"
	}
	if r.Limit <= 0 {
		r.Limit = defaultMatchLimit
	}
	if r.Limit > maxMatchLimit {
		r.Limit = maxMatchLimit
	}
	// 기존 0.50 floor 를 기본값으로 유지(미지정 시 동작 불변). 소비자가 더 높게 올릴 수 있음.
	if r.MinConfidence <= 0 {
		r.MinConfidence = 0.50
	}
	if r.MinConfidence > 1 {
		r.MinConfidence = 1
	}
	return r
}

func (s *Store) CountEntities(ctx context.Context) (int, error) {
	var count int
	err := s.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM kwave_entities`).Scan(&count)
	return count, err
}

func (s *Store) ListEntities(ctx context.Context, filter EntityFilter) ([]Entity, error) {
	filter = filter.normalized()
	like := "%" + filter.Query + "%"
	// 정규화 정체키(공백·문장부호·대소문자 무시) — 인테이크 dedup(intakeNormalizedKey)
	// 과 동일 규칙. 서빙에만 이 비교가 없으면 "6시내고향"(활성 "6시 내고향")이 miss 로
	// 갈리는데 인테이크는 existing_entity 로 접수를 무시해 영원한 preparing 교착이 된다.
	normKey := gatekeeper.NormalizedKey(filter.Query)
	// 동명이인(homonym): 같은 canonical_ko 의 여러 entity 가 각각 person_details
	// (agency/role/works/birth) 와 disambig 를 달고 모두 반환된다. LEFT JOIN.
	rows, err := s.Pool.Query(ctx, `
SELECT `+entityColumnsQualified+personJoinColumns+`
FROM kwave_entities e
LEFT JOIN kwave_entity_person_details d ON d.entity_id = e.id
WHERE ($1 = '' OR
       e.canonical_ko ILIKE $2 OR
       COALESCE(e.canonical_en, '') ILIKE $2 OR
       COALESCE(e.canonical_ja, '') ILIKE $2 OR
       COALESCE(e.canonical_vi, '') ILIKE $2 OR
       COALESCE(e.canonical_zh, '') ILIKE $2 OR
       COALESCE(e.canonical_zh_hant, '') ILIKE $2 OR
       COALESCE(e.canonical_es, '') ILIKE $2 OR
       COALESCE(e.canonical_id, '') ILIKE $2 OR
       COALESCE(e.canonical_pt_br, '') ILIKE $2 OR
       ($9 <> '' AND (
         lower(regexp_replace(btrim(e.canonical_ko), '[[:space:][:punct:]]+', '', 'g')) = $9 OR
         lower(regexp_replace(btrim(COALESCE(e.canonical_en,'')), '[[:space:][:punct:]]+', '', 'g')) = $9
       )) OR
       EXISTS (
         SELECT 1
         FROM unnest(e.aliases_ko || e.aliases_en || e.aliases_ja || e.aliases_vi ||
                     e.aliases_zh || e.aliases_zh_hant || e.aliases_es || e.aliases_id ||
                     e.aliases_pt_br) AS a(alias)
         WHERE a.alias ILIKE $2
            OR ($9 <> '' AND lower(regexp_replace(btrim(a.alias), '[[:space:][:punct:]]+', '', 'g')) = $9)
       ))
  AND ($3 = '' OR e.entity_type::text = $3)
  AND ($4 = 'all' OR e.status = $4)
  AND ($7 = 0 OR e.confidence >= $7)
  AND ($8::timestamptz IS NULL OR e.updated_at >= $8)
ORDER BY
  CASE
    WHEN $1 <> '' AND e.canonical_ko = $1 THEN 0
    WHEN $9 <> '' AND (
      lower(regexp_replace(btrim(e.canonical_ko), '[[:space:][:punct:]]+', '', 'g')) = $9 OR
      EXISTS (SELECT 1 FROM unnest(e.aliases_ko || e.aliases_en) AS na(alias)
               WHERE lower(regexp_replace(btrim(na.alias), '[[:space:][:punct:]]+', '', 'g')) = $9)
    ) THEN 1
    WHEN $1 <> '' AND (e.canonical_ko ILIKE $2 OR COALESCE(e.canonical_en, '') ILIKE $2) THEN 2
    ELSE 3
  END,
  e.confidence DESC,
  e.updated_at DESC
LIMIT $5 OFFSET $6`, filter.Query, like, filter.Type, filter.Status, filter.Limit, filter.Offset, filter.MinConfidence, updatedSinceArg(filter.UpdatedSince), normKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Entity, 0, filter.Limit)
	for rows.Next() {
		ent, err := scanEntityWithPerson(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ent)
	}
	return out, rows.Err()
}

func (s *Store) GetEntity(ctx context.Context, id string) (Entity, error) {
	row := s.Pool.QueryRow(ctx, `
SELECT `+entityColumnsQualified+personJoinColumns+`
FROM kwave_entities e
LEFT JOIN kwave_entity_person_details d ON d.entity_id = e.id
WHERE e.id = $1::uuid`, id)
	return scanEntityWithPerson(row)
}

// GetEntityByKID — **자체 ID 로 한 행을 확정 조회한다**(I03).
//
// ★이 문이 있어야 kid 가 쓸모가 있다. 소비자가 한 번 대상을 고른 뒤 그 kid 를 들고
//   오면, 이름이 같은 대상이 몇이든 **묻는 대상이 확정**된다 — 동명이인이 사라진다.
//   이름이 바뀌어도(개명·활동명 변경) 같은 kid 다.
//
// ★퇴역한 행도 돌려준다. 병합돼 rejected 가 된 kid 로 물어도 "그 대상은 이제 저쪽"을
//   답할 수 있어야 한다. 소비자가 이미 저장한 kid 를 우리가 무효로 만들면 안 된다.
func (s *Store) GetEntityByKID(ctx context.Context, kid string) (Entity, error) {
	row := s.Pool.QueryRow(ctx, `
SELECT `+entityColumnsQualified+personJoinColumns+`
FROM kwave_entities e
LEFT JOIN kwave_entity_person_details d ON d.entity_id = e.id
WHERE e.kid = $1`, strings.ToUpper(strings.TrimSpace(kid)))
	return scanEntityWithPerson(row)
}

// looksLikeKID — `K` + 7자리. 이름과 겹치지 않는 꼴이라 질의만 보고 가를 수 있다.
var looksLikeKID = regexp.MustCompile(`^[Kk][0-9]{7}$`)

// provenanceExpr — 엔티티의 출처 신뢰도 라벨(SQL). 신뢰도 내림차순 우선.
// operator-locked > wikidata-label > media-consensus(≥2매체) > wikipedia-langlinks > llm-only.
const provenanceExpr = `CASE
    WHEN operator_locked THEN 'operator-locked'
    WHEN EXISTS(SELECT 1 FROM unnest(source_urls) u WHERE u ILIKE '%wikidata%') THEN 'wikidata-label'
    WHEN COALESCE(array_length(source_domains,1),0) >= 2 THEN 'media-consensus'
    WHEN EXISTS(SELECT 1 FROM unnest(source_urls) u WHERE u ILIKE '%wikipedia%') THEN 'wikipedia-langlinks'
    ELSE 'llm-only' END`

// provenanceVerifiedExpr — verified_only 게이트. operator_locked OR wikidata OR ≥2매체합의.
// (wikipedia-langlinks / llm-only 는 미검증으로 제외 — 소비자 요청 정의.)
const provenanceVerifiedExpr = `(operator_locked
    OR EXISTS(SELECT 1 FROM unnest(source_urls) u WHERE u ILIKE '%wikidata%')
    OR COALESCE(array_length(source_domains,1),0) >= 2)`

// effectiveSourceExpr — Match 가 실제로 반환하는 locale_name 값의 source 컬럼.
// target locale 이 비어 canonical_en 으로 폴백하면 en 의 source 를 쓴다(반환값과
// provenance 정합). srcCol = canonical_<loc>_source, targetCol = canonical_<loc>.
func effectiveSourceExpr(targetCol, srcCol string) string {
	return fmt.Sprintf(`CASE WHEN NULLIF(%[1]s,'') IS NOT NULL THEN COALESCE(%[2]s,'')
	         ELSE COALESCE(canonical_en_source,'') END`, targetCol, srcCol)
}

// localeProvenanceExpr — 엔티티 전역이 아니라 '반환된 locale 값 자체'의 출처
// 라벨. en 은 wikidata 인데 ja 는 codex-fallback 인 흔한 경우를 정확히 구분한다.
// source 컬럼이 비어있는 레거시 행만 엔티티 전역 휴리스틱(provenanceExpr)으로 폴백.
func localeProvenanceExpr(effSrc string) string {
	return `CASE
	    WHEN operator_locked THEN 'operator-locked'
	    WHEN (` + effSrc + `) IN ('operator-locked','operator') THEN 'operator-locked'
	    WHEN (` + effSrc + `) = 'wikidata-label' THEN 'wikidata-label'
	    WHEN (` + effSrc + `) IN ('tmdb','musicbrainz','kofic','kmdb','naver-people','correction-verified','netflix','disney','itunes','discogs') THEN 'external-db'
	    WHEN (` + effSrc + `) = 'media-consensus' THEN 'media-consensus'
	    WHEN (` + effSrc + `) LIKE 'wikipedia%' THEN 'wikipedia-langlinks'
	    WHEN (` + effSrc + `) LIKE 'rss-observation%' THEN 'media-single'
	    WHEN (` + effSrc + `) = 'mydramalist' THEN 'community-db'
	    WHEN (` + effSrc + `) = 'romanization' THEN 'romanization'
	    WHEN (` + effSrc + `) = 'kana-rule' THEN 'rule-transliteration'
	    WHEN (` + effSrc + `) = 'opencc' THEN 'opencc'
	    WHEN (` + effSrc + `) = 'gtranslate' THEN 'machine-translation'
	    WHEN (` + effSrc + `) = 'codex-fallback' THEN 'llm-only'
	    WHEN (` + effSrc + `) = '' THEN ` + provenanceExpr + `
	    ELSE 'llm-only' END`
}

// localeVerifiedExpr — verified_only 의 locale 정확 버전. 반환값의 source 가
// 검증 소스(operator/wikidata/external-db(권위 API+교차검증된 정정)/media-consensus)
// 일 때만 통과. source 미기록 레거시 행은 엔티티 전역 게이트로 폴백.
// 권위 source 집합은 source_priority.go::Mark() 의 그룹핑(prio 1~4)과 일치한다.
func localeVerifiedExpr(effSrc string) string {
	return `(operator_locked
	    OR (` + effSrc + `) IN ('operator-locked','operator','wikidata-label','tmdb','musicbrainz','kofic','kmdb','naver-people','correction-verified','media-consensus','netflix','disney','itunes','discogs')
	    OR ((` + effSrc + `) = '' AND ` + provenanceVerifiedExpr + `))`
}

// matchWordBoundaryPredicate — match 의 어절경계 조건. **여기 한 곳에만 있다.**
// 시험이 자기 사본을 들고 있으면 본문이 바뀌어도 시험은 옛 사본을 검사하며 통과한다.
// 동치·속도 시험이 이 상수를 그대로 쓰도록 꺼내 둔다($1 = 본문).
const matchWordBoundaryPredicate = `
        (char_length(canonical_ko) >= 4 AND strpos($1, canonical_ko) > 0)
        OR (char_length(canonical_ko) BETWEEN 2 AND 3 AND strpos($1, canonical_ko) > 0
            AND (canonical_ko !~ '^[가-힣]+$'
                 OR $1 ~ ('(^|[^가-힣])' || canonical_ko ||
                          '(은|는|이|가|을|를|와|과|의|에|에서|에게|한테|도|로|으로|만|까지|부터|보다|처럼|랑|이랑|[^가-힣]|$)')))`

// matchSpecificityExpr — 본문에서 **실제로 맞은 조각의 길이**. match 정렬의 1순위다.
// **여기 한 곳에만 있다**(matchWordBoundaryPredicate 와 같은 이유).
//
// ★왜 필요했나 (2026-09-14, presslocale 실측 신고).
//   "드라마 사랑이 온다 가 방영된다" 를 보내면 이 순서로 나갔다:
//     1) 온다        person  confidence 0.75  (2자)
//     2) 사랑        drama   confidence 0.72  (2자)
//     3) 사랑이 온다 drama   confidence 0.70  (6자)  ← 정답이 꼴찌
//   소비자는 1등을 집어 일본어판에 『オンダ』를 발행했다. 실제로 나갔다.
//
//   원인은 `ORDER BY confidence DESC` 였다. 그 `confidence` 는 **매칭 점수가 아니라
//   kwave_entities.confidence — 대상 자체의 품질 점수**다. 본문에 얼마나 맞았는지와
//   무관한 값이 1순위였고, 특이성(길이)은 동점일 때만 봤다.
//   텍스트 매칭의 기본은 **긴 것이 이긴다**(longest match wins)이다.
//
// ★왜 length(canonical_ko) 가 아닌가. 매칭은 canonical_ko **또는 aliases_ko** 로 붙는다.
//   별칭으로 맞은 행에 정본 길이를 주면 맞지도 않은 조각의 길이로 줄을 세우게 된다.
//   그래서 두 갈래 중 **실제로 본문에 있는 것 중 가장 긴 것**을 쓴다.
//   (canonical 가지의 어절경계 정규식은 strpos>0 을 필요조건으로 포함한다 — 위 주석의
//    동치 증명과 같다. 그래서 여기서는 strpos 만으로 충분하다.)
const matchSpecificityExpr = `GREATEST(
          CASE WHEN strpos($1, canonical_ko) > 0 THEN char_length(canonical_ko) ELSE 0 END,
          COALESCE((SELECT max(char_length(a.alias)) FROM unnest(aliases_ko) AS a(alias)
                     WHERE a.alias <> '' AND char_length(a.alias) >= 2
                       AND position(lower(a.alias) in lower($1)) > 0), 0))`

func (s *Store) MatchEntitiesForLocale(ctx context.Context, req MatchEntitiesRequest) ([]MatchedEntity, error) {
	req = req.normalized()
	targetCol, aliasesCol, err := entityLocaleColumns(req.Locale)
	if err != nil {
		return nil, err
	}
	srcCol := targetCol + "_source"
	effSrc := effectiveSourceExpr(targetCol, srcCol)
	// $1 source_text, $2 min_confidence. 선택 절(status/verified)·limit 은 동적 번호.
	args := []any{req.SourceText, req.MinConfidence}
	// status 필터는 항상 적용한다. 빈값은 active 로 폴백 — normalized() 가 이미
	// 채우지만, 이 함수가 normalized() 없이 직접 호출돼도 rejected tombstone 이
	// 새지 않도록 클로즈 자체에서 방어(번역 핫패스 안전성). match 는 전수 감사
	// 경로가 아니므로 전체 tier 우회는 없다(필요시 /v1/entities).
	st := req.Status
	if st == "" {
		st = "active"
	}
	args = append(args, st)
	statusClause := fmt.Sprintf("\n   AND status = $%d", len(args))
	verifiedClause := ""
	if req.VerifiedOnly {
		verifiedClause = "\n   AND " + localeVerifiedExpr(effSrc)
	}
	args = append(args, req.Limit)
	limitParam := len(args)

	// ★값 없는 대상을 지울 것인가 남길 것인가 (2026-09-14).
	//   기본은 종전대로 지운다 — 기존 소비자의 응답을 바꾸지 않는다.
	//   include_absent=true 면 남긴다. 소비자는 locale_name="" 과 locale_absent="no_value"
	//   를 받고, 그 대상이 KDB 에 **있다는 것**과 표기가 없다는 것을 함께 안다.
	//   (종전엔 통째로 사라져서 제보조차 할 수 없었다.)
	haveValueClause := `COALESCE(NULLIF(` + targetCol + `,''), NULLIF(canonical_en,''), '') <> ''`
	if req.IncludeAbsent {
		haveValueClause = `TRUE`
	}

	q := fmt.Sprintf(`
SELECT id::text,
       COALESCE(kid, ''),
       canonical_ko,
       COALESCE(NULLIF(%[1]s,''), NULLIF(canonical_en,''), '') AS locale_name,
       entity_type::text,
       confidence::float8,
       status,
       operator_locked,
       %[3]s AS provenance,
       %[7]s AS locale_source,
       COALESCE(source_urls, '{}'::text[]),
       updated_at,
       aliases_ko,
       %[2]s AS target_aliases,
       COALESCE(notes,''),
       COALESCE(disambig,'') AS disambig,
       -- locale_ambiguous: 반환되는 locale_name(target locale 값, 없으면 en 폴백)이
       -- 같은 type 의 *다른 active entity* 와 동일한가. 동명이인이 disambig 라벨로
       -- 해소돼도 영문 표기가 같으면 번역 소비자에겐 여전히 모호하므로 신호로 준다.
       -- entity 레벨 물리 needs_disambig(한국어 동명이인 파이프라인)와는 별개 — 그래서
       -- 컬럼/필드명을 달리해 이중-진실원천 혼동을 피한다. 비교 대상 o 는 active 고정
       -- (활성 정본끼리의 표기 충돌만 경고; req.Status 와 무관하게 의미 일관).
       EXISTS (
         SELECT 1 FROM kwave_entities o
          WHERE o.status = 'active' AND o.id <> kwave_entities.id
            AND o.entity_type = kwave_entities.entity_type
            AND COALESCE(NULLIF(o.%[1]s,''), NULLIF(o.canonical_en,''), '')
                = COALESCE(NULLIF(kwave_entities.%[1]s,''), NULLIF(kwave_entities.canonical_en,''), '')
       ) AS locale_ambiguous,
       -- locale_fallback(QW-7): 요청 locale 칸이 비어 canonical_en 으로 폴백한 값인가.
       -- true 면 소비자는 locale_name 이 해당 언어 표기가 아니라 영어 대체임을 안다.
       (NULLIF(%[1]s,'') IS NULL AND NULLIF(canonical_en,'') IS NOT NULL) AS locale_fallback
  FROM kwave_entities
 WHERE %[8]s
   AND confidence >= $2%[4]s%[5]s
   AND (
        -- ★어절경계 매칭(CR-2, 2026-06-28): strpos 부분문자열은 일상어를 인명으로 오매칭한다
        -- (진짜→진, 나비→비, 가을바람→가을). 길이별 가드:
        --  · 1자 정본: 매칭 제외(20건, 오탐 과다).
        --  · 2~3자 순수한글 정본: 어절경계 정규식 — 앞은 비한글, 뒤는 조사 allowlist 또는
        --    비한글/끝일 때만(부분문자열·합성어 오탐 차단, 조사부착 정상매칭은 보존).
        --  · 2~3자 비순수한글(라틴/숫자 혼합, 드묾): strpos(메타문자 이스케이프 회피).
        --  · 4자+ 정본: strpos 유지(긴 정본은 부분문자열 오탐 거의 없음, recall 보존).
        --
        -- ★strpos 를 앞에 세워 정규식 컴파일을 걷어낸다 (2026-09-14, 실측 19배).
        -- 종전엔 2~3자 순수한글 **6,468행마다** 어절경계 패턴을 canonical_ko 로 이어 붙여
        -- **매번 새 정규식을 지어 컴파일**했다. 패턴이 행마다 다르므로 계획이 캐시할 수
        -- 없다 — 한 번의 match 에 정규식 컴파일 6,468회다. 실측 454·464·480ms 중 대부분이
        -- 여기였고(계획 확인), strpos 를 먼저 걸면 24·26·24ms 다. 결과는 5건으로 **동일**.
        --
        -- 왜 결과가 같은가: 저 정규식이 맞으려면 canonical_ko 가 본문에 **글자 그대로**
        -- 들어 있어야 한다(순수한글이라 정규식 메타문자가 없다). 즉 정규식 일치 ⟹ strpos>0
        -- 이므로 strpos 는 **빠뜨릴 수 없는 필수 조건**이고, 앞에 세워도 참인 행을 잃지 않는다.
        -- 비순수한글 가지는 종전에도 strpos 뿐이었으므로 그대로 합쳐진다.
`+matchWordBoundaryPredicate+`
        OR EXISTS (
          SELECT 1
            FROM unnest(aliases_ko) AS a(alias)
           -- D-9: 라틴 alias 대소문자 무시(BTS↔bts). 한글은 lower() 무영향.
           WHERE alias <> '' AND char_length(alias) >= 2 AND position(lower(alias) in lower($1)) > 0
        )
   )
 ORDER BY ` + matchSpecificityExpr + ` DESC, confidence DESC, last_verified_at DESC
 LIMIT $%[6]d`, targetCol, aliasesCol, localeProvenanceExpr(effSrc), statusClause, verifiedClause, limitParam, effSrc, haveValueClause)

	rows, err := s.Pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]MatchedEntity, 0, 16)
	for rows.Next() {
		var e MatchedEntity
		if err := rows.Scan(&e.ID, &e.KID, &e.KO, &e.LocaleName, &e.EntityType, &e.Confidence, &e.Status, &e.OperatorLocked, &e.Provenance, &e.LocaleSource, &e.SourceURLs, &e.UpdatedAt, &e.SourceAliases, &e.TargetAliases, &e.Note, &e.Disambig, &e.LocaleAmbiguous, &e.LocaleFallback); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) CreateObservation(ctx context.Context, req ObservationRequest) (bool, bool, error) {
	entityID, err := uuid.Parse(strings.TrimSpace(req.EntityID))
	if err != nil {
		return false, false, fmt.Errorf("invalid entity_id")
	}
	locale := normalizeLocale(req.Locale)
	if locale == "" {
		return false, false, fmt.Errorf("locale required")
	}
	if _, _, err := entityLocaleColumns(locale); err != nil {
		return false, false, err
	}
	spelling := strings.TrimSpace(req.Spelling)
	if spelling == "" {
		return false, false, fmt.Errorf("spelling required")
	}
	sourceDomain := strings.TrimSpace(req.SourceDomain)
	if sourceDomain == "" {
		return false, false, fmt.Errorf("source_domain required")
	}
	confidence := req.Confidence
	if confidence == 0 {
		confidence = 0.85
	}
	if confidence < 0 || confidence > 1 {
		return false, false, fmt.Errorf("invalid confidence")
	}
	store := kdb.NewObservationStore(s.Pool)
	if err := store.Save(ctx, entityID, kdb.ExtractedSpelling{
		Locale:     locale,
		Spelling:   spelling,
		Confidence: confidence,
	}, sourceDomain, strings.TrimSpace(req.SourceURL)); err != nil {
		return false, false, err
	}
	promoted := false
	if req.Evaluate {
		_, promoted, err = store.EvaluateConsensus(ctx, entityID, locale)
		if err != nil {
			return true, false, err
		}
	}
	return true, promoted, nil
}

type ResearchEnqueueResult struct {
	Queued   bool
	Inserted bool
	Decision gatekeeper.IntakeDecision
}

// EnqueueResearch preserves the historical bool API for internal callers.
// The bool now means provider work was actually admitted/nudged, not merely
// that an audit row was stored. Call EnqueueResearchDetailed when the client
// needs the pass/review/reject reason.
// ExhaustedLocales — missing 중 enrich 소스가 소진(exhausted=true)돼 현재 소스로는
// 채울 수 없는 locale 목록. prepare 응답 unavailable 로 나가 소비자 무한 재폴링을 끊는다.
func (s *Store) ExhaustedLocales(ctx context.Context, entityID string, missing []string) []string {
	if len(missing) == 0 || s.Pool == nil {
		return nil
	}
	fields := make([]string, 0, len(missing))
	for _, l := range missing {
		fields = append(fields, "canonical_"+l)
	}
	// ★`exhausted` 불리언만 보면 **정책으로 멈춘 칸을 영영 못 본다** (2026-09-20 실측).
	//
	//   exhausted 는 `attempts >= 2` 일 때만 선다. 그런데 근거 없는 칸은 L4 를 건너뛰는
	//   정책 스킵(ground-strict-skip)으로 끝나고, 그 경로는 **시도로 세지 않는다.**
	//   그래서 attempts 가 영원히 0 이다:
	//
	//     canonical_zh 의 attempts=0 행 4,564건 중 ground-strict-skip 2,481건(54%)
	//     그중 3,605건은 30일 이전에 멈췄고 가장 오래된 것은 2026-06-13
	//
	//   소비자에게는 그동안 `preparing` 이 나갔다 — 「기다리면 채워진다」는 뜻인데
	//   채워질 일이 없다. 거짓말이다. 주 271회 요청(120낱말)이 그 답을 받고 있었다.
	//
	// ★그래서 「오래 멈춘 정책 스킵」도 소진으로 본다. 값은 건드리지 않는다 —
	//   소비자가 받는 **안내만** 참이 된다(unfillable + fill_hint).
	//   근거가 생겨 칸이 채워지면 recordAttempt 가 행을 지우므로 자동으로 풀린다.
	//   임계를 30일로 둔 이유: 정책 스킵의 쿨다운이 7일이라 그보다 넉넉해야
	//   「아직 재방문 중인 것」을 소진이라 부르지 않는다.
	rows, err := s.Pool.Query(ctx, `
SELECT field FROM kwave_kdb_enrich_attempts
 WHERE entity_id = $1::uuid AND field = ANY($2)
   AND (exhausted
        OR (last_source = 'ground-strict-skip'
            AND last_attempt_at < now() - interval '30 days'))`, entityID, fields)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var f string
		if rows.Scan(&f) == nil {
			out = append(out, strings.TrimPrefix(f, "canonical_"))
		}
	}
	return out
}

// Tombstoned — 정규화 동치의 rejected/merged 엔티티가 있고, 같은 키의 active/candidate
// 는 없는가(=이 키워드는 이미 검토가 끝나 '결번' 판정). true 면 소비자에게 preparing
// 대신 out_of_scope 종결을 통지하고 재발굴/autoverify 예산 낭비를 막는다.
// merged 는 생존자 active 가 있으면 두 번째 EXISTS 에 걸려 false — ① 정규화 서빙이
// 생존자를 정상 매칭하므로 여기 오지도 않는 게 보통이다.
// CommonHomonyms — 주어진 ko 표제어들 중 **공통 원장(kentity_entities)에 대상이 있는** 것을
// 한 번의 질의로 돌려준다(P4.01 입력 계약).
//
// 왜 한 번인가. 표제어마다 질의하면 한 요청(최대 200건)에 200번이 된다.
// 일괄 조회는 실측 **2.2ms / 200건**(kentity_entities_name 인덱스 스캔)이다.
//
// 무엇을 뜻하는가. "이 이름으로 원장에 또 다른 대상이 있다"까지다. 같은 대상인지
// 다른 대상인지는 판정하지 않는다 — 그건 P5(동일성 판정) 몫이다.
func (s *Store) CommonHomonyms(ctx context.Context, terms []string) map[string]bool {
	out := map[string]bool{}
	if s == nil || s.Pool == nil || len(terms) == 0 {
		return out
	}
	rows, err := s.Pool.Query(ctx, `SELECT DISTINCT canonical_ko FROM kentity_entities
 WHERE canonical_ko = ANY($1) AND write_owner='native' AND status <> 'rejected'`, terms)
	if err != nil {
		// 신호가 없다고 서빙을 막지 않는다 — 이 값은 부가 정보다.
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var ko string
		if rows.Scan(&ko) == nil {
			out[ko] = true
		}
	}
	return out
}

func (s *Store) Tombstoned(ctx context.Context, term string) bool {
	key := gatekeeper.NormalizedKey(term)
	if key == "" || s.Pool == nil {
		return false
	}
	var tomb bool
	err := s.Pool.QueryRow(ctx, `
SELECT EXISTS (
  SELECT 1 FROM kwave_entities
   WHERE status IN ('rejected','merged')
     -- ★2026-07-31 김은정 사고가 이 계열의 시작이다. 종결된 187건은 김은정(컬링)·
     -- 서진·가람 처럼 흔한 한국 이름이라 동명의 실존 대상이 있을 가능성이 높은데,
     -- 이름을 tombstone 하면 그 요청이 lookup/prepare 에서 "재조회 불필요"로 막힌다.
     -- ★**이름을 묻어 버리지 못하는 기각**은 tombstone 이 아니다.
     --   판정은 kdb.NotATombstoneSQL 한 자리에서 온다 — 같은 판단을 세 곳이 따로 적었고
     --   그중 둘이 TTL 을 안 빼서 TTL 기각이 영구 차단으로 세탁됐다(2026-09-16 오세훈).
     --     [revert-term:reject]  이 레코드의 QID 가 비-K 다 (이 이름이 없다가 아니다)
     --     [ttl-expire:reject]   기한 내 실증 실패 — 설계 의도가 '재요청 시 재발굴'이다
     --     옛 범위 기각            연예가 아니다 — 범위 확대(0143)로 명제 자체가 죽었다
     --   오거부는 이 저장소의 최상위 금칙이다.
     AND ` + kdb.NotATombstoneSQL("") + `
     AND (lower(regexp_replace(btrim(canonical_ko), '[[:space:][:punct:]]+', '', 'g')) = $1
       OR EXISTS (SELECT 1 FROM unnest(aliases_ko) a
                   WHERE lower(regexp_replace(btrim(a), '[[:space:][:punct:]]+', '', 'g')) = $1))
) AND NOT EXISTS (
  SELECT 1 FROM kwave_entities
   WHERE status IN ('active','candidate')
     AND (lower(regexp_replace(btrim(canonical_ko), '[[:space:][:punct:]]+', '', 'g')) = $1
       OR EXISTS (SELECT 1 FROM unnest(aliases_ko) a
                   WHERE lower(regexp_replace(btrim(a), '[[:space:][:punct:]]+', '', 'g')) = $1))
)`, key).Scan(&tomb)
	return err == nil && tomb
}

func (s *Store) EnqueueResearch(ctx context.Context, req ResearchQueueRequest) (bool, error) {
	res, err := s.EnqueueResearchDetailed(ctx, req)
	return res.Queued, err
}

func (s *Store) EnqueueResearchDetailed(ctx context.Context, req ResearchQueueRequest) (ResearchEnqueueResult, error) {
	var out ResearchEnqueueResult
	entityKO := strings.TrimSpace(req.EntityKO)
	if entityKO == "" {
		return out, fmt.Errorf("entity_ko required")
	}
	entityType := strings.TrimSpace(req.RequestedEntityType)
	if entityType == "" {
		entityType = "unknown"
	}
	if !validEntityType(entityType) {
		return out, fmt.Errorf("invalid entity type")
	}
	// D-15: context_hint 절단(200자) — 기사 본문 통째 저장(최대 2221자) 방지.
	contextHint := strings.TrimSpace(req.ContextHint)
	if rs := []rune(contextHint); len(rs) > 200 {
		contextHint = string(rs[:200])
	}
	var sourceID any
	if strings.TrimSpace(req.SourceID) != "" {
		id, err := uuid.Parse(strings.TrimSpace(req.SourceID))
		if err != nil {
			return out, fmt.Errorf("invalid source_id")
		}
		sourceID = id
	}
	sourceURL := strings.TrimSpace(req.SourceURL)
	if len([]rune(sourceURL)) > 500 {
		sourceURL = string([]rune(sourceURL)[:500])
	}
	origin := strings.TrimSpace(req.Origin)
	if origin == "" {
		origin = "internal"
	}
	if len([]rune(origin)) > 40 {
		origin = string([]rune(origin)[:40])
	}
	gateInput := gatekeeper.IntakeInput{
		FromCorrection: origin == "correction-miss",
		Term:           entityKO,
		EntityType:     entityType,
		Context:        contextHint,
		SourceURL:      sourceURL,
		HasSourceID:    sourceID != nil,
		SourceTrusted:  s.isTrustedIntakeSource(ctx, sourceURL),
	}
	decision := gatekeeper.DecideIntake(gateInput)
	var activeMatches, compatibleMatches int
	_ = s.Pool.QueryRow(ctx, `
SELECT count(*), count(*) FILTER (
         WHERE $2 IN ('unknown','term') OR entity_type::text=$2
       )
 FROM kwave_entities
 WHERE status='active'
   AND (
     lower(regexp_replace(btrim(canonical_ko), '[[:space:][:punct:]]+', '', 'g'))=$1
     OR EXISTS (
       SELECT 1 FROM unnest(COALESCE(aliases_ko,'{}')) a
        WHERE lower(regexp_replace(btrim(a), '[[:space:][:punct:]]+', '', 'g'))=$1
     )
   )`,
		decision.NormalizedKey, entityType).Scan(&activeMatches, &compatibleMatches)
	switch {
	case activeMatches == 1 && compatibleMatches == 1:
		gateInput.ExistingEntity = true
	case activeMatches > 0:
		gateInput.IdentityConflict = true
	}
	var typeConflict bool
	if decision.Verdict != gatekeeper.IntakeReject && !gateInput.ExistingEntity && !gateInput.IdentityConflict && entityType != "unknown" && entityType != "term" {
		_ = s.Pool.QueryRow(ctx, `
SELECT EXISTS (
  SELECT 1 FROM kwave_entity_research_queue q
   WHERE q.intake_normalized_key=$1
     AND q.requested_entity_type::text NOT IN ('unknown','term',$2)
     AND COALESCE(q.precheck_status,'legacy') <> 'reject'
  UNION ALL
  SELECT 1 FROM kwave_entities e
   WHERE (lower(regexp_replace(btrim(e.canonical_ko), '[[:space:][:punct:]]+', '', 'g'))=$1
          OR EXISTS (SELECT 1 FROM unnest(COALESCE(e.aliases_ko,'{}')) a
                      WHERE lower(regexp_replace(btrim(a), '[[:space:][:punct:]]+', '', 'g'))=$1))
     AND e.entity_type::text NOT IN ('unknown','term',$2)
     AND e.status IN ('active','candidate')
)`, decision.NormalizedKey, entityType).Scan(&typeConflict)
	}
	gateInput.TypeConflict = typeConflict
	decision = gatekeeper.DecideIntake(gateInput)
	out.Decision = decision
	entityKO = decision.Normalized
	// ★게이트가 문맥 단서로 유형을 알아냈으면 **큐에도 그 유형으로 적는다** (2026-09-16).
	//   게이트만 알고 원장이 모르면 추론이 다음 판단에 안 남는다 — 다음 라운드가
	//   같은 요청을 다시 term 으로 보고 같은 고민을 반복한다.
	if rt := strings.TrimSpace(decision.ResolvedType); rt != "" && validEntityType(rt) {
		entityType = rt
	}

	// ★요청 훅 (2026-09-16): 소비자가 물었는데 그 행이 아직 **candidate** 면 즉시 민다.
	//
	//   실측(2026-09-16): 오늘 요청된 낱말 중 candidate 행이 이미 있는 것 399건,
	//   그중 위키데이터 앵커 없음 386건. 발굴은 막힌 데가 아니었다(큐 1,281건 전부
	//   done, picked→finished p50 1.7초). 막힌 곳은 발굴 **뒤**였다.
	//
	// ★그 행에 아무 일도 안 일어나는 이유가 셋이다. 장치는 셋 다 있는데 셋 다 못 닿는다.
	//
	//     ① bgEnrich 는 lookup 의 matches 를 보고 거는데, matches 기본 status 가
	//        'active' 다(EntityFilter). candidate 는 애초에 목록에 없다.
	//     ② CandidateEvidenceOne(단건 패스트레인)은 research worker 가 그 행을
	//        **만든 그 순간 한 번만** 부른다. 내일 다시 물어도 다시 불리지 않는다.
	//     ③ 재요청은 큐 INSERT 가 중복으로 걸러지고, 아래 재개 UPDATE 는
	//        `precheck_status IN ('legacy','review')` 만 연다 — 'pass' 로 닫힌 행은
	//        done 에 머물고 워커가 집지 않는다.
	//
	// ★`existing_entity` 로 걸면 안 된다 (처음에 그렇게 썼다가 고쳤다).
	//
	//   그 판정은 바로 위에서 `status='active'` 가 정확히 1건일 때만 켜진다.
	//   candidate 에는 **절대 안 걸린다** — 훅이 한 번도 안 불렸을 것이다.
	//   기준은 "행이 있느냐"가 아니라 **"소비자가 기다리는데 아무도 안 보느냐"**다.
	//
	//   active 가 하나라도 있으면 건너뛴다. 그건 답이 나가는 낱말이고, candidate
	//   쪽은 동명이인 분기이거나 중복이다 — 요청 예산으로 밀 일이 아니다.
	//   기각 판정도 건너뛴다. 되풀이 방지(엔티티당 1시간)는 레인 안에 있다.
	// ★조회는 **요청 경로 밖에서** 한다 (실측 2026-09-16).
	//
	//   정규화 키에 함수 색인이 없어 이 조회는 14,569행 순차 스캔이고 44ms 다.
	//   여기 동기로 두면 miss 응답마다 44ms 가 붙고, 50낱말 bulk 하나면 2.2초다.
	//   오늘 아침에 같은 실수를 한 번 했다 — 메뉴 배지 조회를 렌더 임계 경로에
	//   두었다가 회귀가 잡았다(e2b1281). 그때와 같은 처리를 여기서 먼저 한다.
	//
	//   (바로 위 두 조회도 같은 식을 써서 이미 각각 그 값을 물고 있다. 함수 색인을
	//   하나 놓으면 셋이 같이 빨라지지만 그건 마이그레이션이라 따로 판단할 일이다.)
	if activeMatches == 0 && decision.Verdict != gatekeeper.IntakeReject && s.onDemandCandidate != nil {
		key, typ := decision.NormalizedKey, entityType
		go func() {
			bg, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if id := s.waitingCandidateID(bg, key, typ); id != "" {
				s.onDemandCandidate(id)
			}
		}()
	}
	queueStatus, resolutionStatus, localeStatus, lastOutcome := "done", "review_required", "blocked_precheck", "precheck_review"
	var finishedAt any = time.Now()
	if decision.Verdict == gatekeeper.IntakePass {
		queueStatus, resolutionStatus, localeStatus, lastOutcome = "pending", "unknown", "unknown", ""
		finishedAt = nil
		if decision.ReasonCode == "existing_entity" {
			queueStatus, resolutionStatus, localeStatus, lastOutcome = "done", "active", "complete", "existing_entity"
			finishedAt = time.Now()
		}
	} else if decision.Verdict == gatekeeper.IntakeReject {
		resolutionStatus, lastOutcome = "rejected_precheck", "precheck_reject"
	}
	var inserted string
	err := s.Pool.QueryRow(ctx, `
INSERT INTO kwave_entity_research_queue
  (entity_ko, requested_entity_type, context_hint, source_id, source_url, status,
   finished_at, resolution_status, locale_status, last_outcome,
   precheck_status, precheck_reason, precheck_flags, precheck_rule_version, intake_origin,
   intake_normalized_key)
SELECT $1, $2::kwave_entity_type, NULLIF($3,''), $4::uuid, NULLIF($5,''), $6,
       $7::timestamptz, $8, $9, $10, $11, $12, $13::text[], $14, $15, $16
 WHERE NOT EXISTS (
  SELECT 1
    FROM kwave_entity_research_queue
   WHERE intake_normalized_key = $16
     AND requested_entity_type = $2::kwave_entity_type
)
ON CONFLICT DO NOTHING
RETURNING id::text`, entityKO, entityType, contextHint, sourceID, sourceURL,
		queueStatus, finishedAt, resolutionStatus, localeStatus, lastOutcome,
		string(decision.Verdict), decision.ReasonCode, decision.Flags, decision.RuleVersion, origin,
		decision.NormalizedKey).Scan(&inserted)
	if errors.Is(err, pgx.ErrNoRows) {
		// Preserve an operator decision.  A newly stronger automatic proof may
		// release a legacy/review row, while a weaker repeat can never downgrade
		// an admitted or explicitly rejected item.
		var transitioned string
		switch {
		case decision.ReasonCode == "existing_entity":
			_, _ = s.Pool.Exec(ctx, `
WITH chosen AS (
  SELECT id FROM kwave_entity_research_queue
   WHERE intake_normalized_key=$1
     AND requested_entity_type=$2::kwave_entity_type
   ORDER BY created_at LIMIT 1
)
UPDATE kwave_entity_research_queue q
   SET status='done', finished_at=now(), picked_at=NULL, next_attempt_at=NULL,
       resolution_status='active', locale_status='complete', last_outcome='existing_entity',
       precheck_status='pass', precheck_reason='existing_entity',
       precheck_flags=$3, precheck_rule_version=$4,
       request_count=request_count+1, last_requested_at=now(), last_error=NULL
 WHERE q.id=(SELECT id FROM chosen) AND q.status <> 'in_progress'`,
				decision.NormalizedKey, entityType, decision.Flags, decision.RuleVersion)
		case decision.Verdict == gatekeeper.IntakePass:
			_ = s.Pool.QueryRow(ctx, `
WITH chosen AS (
  SELECT id FROM kwave_entity_research_queue
   WHERE intake_normalized_key=$1
     AND requested_entity_type=$2::kwave_entity_type
   ORDER BY (precheck_status='approved') DESC, created_at
   LIMIT 1
)
UPDATE kwave_entity_research_queue q
   SET status='pending', finished_at=NULL, picked_at=NULL, next_attempt_at=NULL,
       resolution_status='unknown', locale_status='unknown', last_outcome='', last_error=NULL,
       precheck_status='pass', precheck_reason=$5, precheck_flags=$6,
       precheck_rule_version=$7, intake_origin=$8,
       context_hint=COALESCE(NULLIF($3,''),context_hint),
       source_url=COALESCE(NULLIF($4,''),source_url),
       source_id=COALESCE(source_id,$9::uuid),
       request_count=request_count+1, last_requested_at=now()
 WHERE q.id=(SELECT id FROM chosen)
   AND status <> 'in_progress' AND precheck_status IN ('legacy','review')
 RETURNING id::text`, decision.NormalizedKey, entityType, contextHint, sourceURL,
				decision.ReasonCode, decision.Flags, decision.RuleVersion, origin, sourceID).Scan(&transitioned)
		default:
			var repeatID, repeatPrecheck string
			_ = s.Pool.QueryRow(ctx, `
WITH chosen AS (
  SELECT id FROM kwave_entity_research_queue
   WHERE intake_normalized_key=$1
     AND requested_entity_type=$2::kwave_entity_type
   ORDER BY (precheck_status='approved') DESC, created_at
   LIMIT 1
)
UPDATE kwave_entity_research_queue q
   SET request_count=request_count+1, last_requested_at=now(),
       precheck_status=CASE WHEN precheck_status='legacy' THEN $5 ELSE precheck_status END,
       precheck_reason=CASE WHEN precheck_status='legacy' THEN $6 ELSE precheck_reason END,
       precheck_flags=CASE WHEN precheck_status='legacy' THEN $7::text[] ELSE precheck_flags END,
       precheck_rule_version=CASE WHEN precheck_status='legacy' THEN $8 ELSE precheck_rule_version END,
       status=CASE WHEN precheck_status='legacy' AND status='pending' THEN 'done' ELSE status END,
       finished_at=CASE WHEN precheck_status='legacy' AND status='pending' THEN now() ELSE finished_at END,
       resolution_status=CASE WHEN precheck_status='legacy' THEN $9 ELSE resolution_status END,
       locale_status=CASE WHEN precheck_status='legacy' THEN 'blocked_precheck' ELSE locale_status END,
	       last_outcome=CASE WHEN precheck_status='legacy' THEN $10 ELSE last_outcome END,
	       source_id=COALESCE(source_id,$3::uuid),
	       source_url=COALESCE(NULLIF($4,''),source_url)
 WHERE q.id=(SELECT id FROM chosen)
 RETURNING id::text, precheck_status`, decision.NormalizedKey, entityType, sourceID, sourceURL,
				string(decision.Verdict), decision.ReasonCode, decision.Flags, decision.RuleVersion,
				resolutionStatus, lastOutcome).Scan(&repeatID, &repeatPrecheck)
			// 소비자가 다시 찾는 review 키워드 = 지금 수요가 있는 키워드 — 즉시 재검증 kick.
			if repeatID != "" && repeatPrecheck == "review" && s.onReviewParked != nil {
				s.onReviewParked(repeatID)
			}
		}
		if transitioned != "" {
			out.Queued = true
			if s.onEnqueue != nil {
				s.onEnqueue()
			}
		}
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.Inserted = inserted != ""
	out.Queued = out.Inserted && decision.Verdict == gatekeeper.IntakePass && decision.ReasonCode != "existing_entity"
	if out.Queued && s.onEnqueue != nil {
		s.onEnqueue() // pass 신규 적재만 워커 즉시 nudge
	}
	// 신규 review 보류 = "제대로 된 키워드면 바로 심사"(오너) — 자동 검증기 즉시 kick.
	if out.Inserted && decision.Verdict == gatekeeper.IntakeReview && s.onReviewParked != nil {
		s.onReviewParked(inserted)
	}
	return out, nil
}

func (s *Store) isTrustedIntakeSource(ctx context.Context, rawURL string) bool {
	if gatekeeper.ConfiguredTrustedIntakeSourceURL(rawURL) {
		return true
	}
	host, ok := gatekeeper.IntakeSourceHost(rawURL)
	if !ok || s == nil || s.Pool == nil {
		return false
	}
	// ★`discovery_enabled` 를 묻지 않는다 (2026-09-15).
	//
	//   그 칸의 뜻은 "우리가 이 사이트를 크롤링한다"이고, 여기서 물어야 하는 것은
	//   "이 출처를 믿는가"다. **다른 질문**인데 같은 칸으로 답하고 있었다.
	//
	//   그래서 **등록된 소비자가 자기 기사 URL 을 보내도 '출처 근거 없음'** 이 됐다.
	//   실측: mediafine 6,545회 · issuetalk 4,126회 · kstory 3,974회 요청인데
	//   전부 화이트리스트 밖이라 SK하이닉스·국민의힘·연세대학교가 review 에 묶였다.
	//   발행사가 자기 기사를 가리키며 "이 고유명사가 여기 나온다"고 하는 것보다
	//   더 나은 인입 근거는 없다.
	//
	//   화이트리스트에 있으면(크롤링 여부와 무관하게) 신뢰 출처다. 소비자 도메인은
	//   0147 이 discovery_enabled=false 로 넣는다 — 믿되 크롤링하지는 않는다.
	var trusted bool
	_ = s.Pool.QueryRow(ctx, `
SELECT EXISTS(SELECT 1 FROM kwave_news_whitelist
               WHERE lower(regexp_replace(domain, '^www\.', ''))=$1)`, host).Scan(&trusted)
	return trusted
}

func (s *Store) SiteSearchEntity(ctx context.Context, entityID uuid.UUID, req SiteSearchRequest) (*kdb.SiteSearchResponse, error) {
	service := kdb.NewSiteSearchService(s.Pool)
	return service.SearchAndEnqueue(ctx, kdb.SiteSearchRequest{
		EntityID:            entityID,
		Locale:              req.Locale,
		Query:               req.Query,
		Domains:             req.Domains,
		LimitDomains:        req.LimitDomains,
		MaxResultsPerDomain: req.MaxResultsPerDomain,
		DryRun:              req.DryRun,
	})
}

func (s *Store) PatchEntity(ctx context.Context, entityID string, req PatchEntityRequest) (Entity, error) {
	sets := make([]string, 0, 16)
	args := []any{entityID}
	add := func(expr string, value any) {
		args = append(args, value)
		sets = append(sets, fmt.Sprintf(expr, len(args)))
	}

	if req.EntityType != nil {
		v := strings.TrimSpace(*req.EntityType)
		if !validEntityType(v) {
			return Entity{}, fmt.Errorf("invalid entity type")
		}
		add("entity_type = $%d::kwave_entity_type", v)
	}
	addNullable := func(field string, v *string) {
		if v != nil {
			add(field+" = NULLIF($%d,'')", strings.TrimSpace(*v))
		}
	}
	addNullable("canonical_en", req.CanonicalEN)
	addNullable("canonical_ja", req.CanonicalJA)
	addNullable("canonical_vi", req.CanonicalVI)
	addNullable("canonical_zh", req.CanonicalZH)
	addNullable("canonical_zh_hant", req.CanonicalZHHant)
	addNullable("canonical_es", req.CanonicalES)
	addNullable("canonical_id", req.CanonicalID)
	addNullable("canonical_pt_br", req.CanonicalPTBR)
	addNullable("category_hint", req.CategoryHint)
	addNullable("notes", req.Notes)
	if req.Status != nil {
		v := strings.TrimSpace(*req.Status)
		if !validEntityStatus(v) {
			return Entity{}, fmt.Errorf("invalid status")
		}
		add("status = $%d", v)
	}
	if req.OperatorLocked != nil {
		add("operator_locked = $%d", *req.OperatorLocked)
	}
	if req.Aliases != nil {
		addAliases := func(field string, values []string) {
			if values != nil {
				add(field+" = $%d::text[]", compactStrings(values))
			}
		}
		addAliases("aliases_ko", req.Aliases.KO)
		addAliases("aliases_en", req.Aliases.EN)
		addAliases("aliases_ja", req.Aliases.JA)
		addAliases("aliases_vi", req.Aliases.VI)
		addAliases("aliases_zh", req.Aliases.ZH)
		addAliases("aliases_zh_hant", req.Aliases.ZHHant)
		addAliases("aliases_es", req.Aliases.ES)
		addAliases("aliases_id", req.Aliases.ID)
		addAliases("aliases_pt_br", req.Aliases.PTBR)
	}
	if len(sets) == 0 {
		return Entity{}, fmt.Errorf("no patch fields")
	}
	q := `
UPDATE kwave_entities
   SET ` + strings.Join(sets, ",\n       ") + `,
       last_verified_at = now(),
       updated_at = now()
 WHERE id = $1::uuid
 RETURNING ` + entityColumns
	return scanEntity(s.Pool.QueryRow(ctx, q, args...))
}

func (s *Store) SetEntityLocked(ctx context.Context, entityID string, locked bool) (Entity, error) {
	return scanEntity(s.Pool.QueryRow(ctx, `
UPDATE kwave_entities
   SET operator_locked = $2,
       updated_at = now()
 WHERE id = $1::uuid
 RETURNING `+entityColumns, entityID, locked))
}

func (s *Store) Relations(ctx context.Context, entityID string) ([]Relation, error) {
	rows, err := s.Pool.Query(ctx, `
SELECT 'outgoing' AS direction, r.relation_type, e2.id::text, e2.canonical_ko,
       e2.entity_type::text, r.confidence::float8, r.source_urls
  FROM kwave_entity_relations r
  JOIN kwave_entities e2 ON e2.id = r.to_entity_id
 WHERE r.from_entity_id=$1::uuid
UNION ALL
SELECT 'incoming' AS direction, r.relation_type, e1.id::text, e1.canonical_ko,
       e1.entity_type::text, r.confidence::float8, r.source_urls
  FROM kwave_entity_relations r
  JOIN kwave_entities e1 ON e1.id = r.from_entity_id
 WHERE r.to_entity_id=$1::uuid
 ORDER BY 1, 2, 4`, entityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Relation, 0, 8)
	for rows.Next() {
		var rel Relation
		if err := rows.Scan(&rel.Direction, &rel.RelationType, &rel.EntityID, &rel.EntityKO, &rel.EntityType, &rel.Confidence, &rel.SourceURLs); err != nil {
			return nil, err
		}
		out = append(out, rel)
	}
	return out, rows.Err()
}

func (s *Store) ExternalRefs(ctx context.Context, entityID string) ([]ExternalRef, error) {
	rows, err := s.Pool.Query(ctx, `
SELECT provider, external_id, COALESCE(url,''), confidence::float8,
       COALESCE(raw_payload->>'description', raw_payload->>'site', '') AS description,
       fetched_at
  FROM kwave_entity_external_refs
 WHERE entity_id=$1::uuid
 ORDER BY (CASE provider WHEN 'wikidata' THEN 0 ELSE 1 END), provider`, entityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]ExternalRef, 0, 8)
	for rows.Next() {
		var ref ExternalRef
		if err := rows.Scan(&ref.Provider, &ref.ExternalID, &ref.URL, &ref.Confidence, &ref.Description, &ref.FetchedAt); err != nil {
			return nil, err
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}

func (s *Store) PersonDetails(ctx context.Context, entityID string) (PersonDetails, error) {
	var p PersonDetails
	p.EntityID = entityID
	err := s.Pool.QueryRow(ctx, `
SELECT primary_role::text, secondary_roles::text[], groups,
       COALESCE(agency,''), COALESCE(gender::text,''), COALESCE(birth_year, 0),
       notable_works
  FROM kwave_entity_person_details
 WHERE entity_id=$1::uuid`, entityID).Scan(&p.PrimaryRole, &p.SecondaryRoles, &p.Groups,
		&p.Agency, &p.Gender, &p.BirthYear, &p.NotableWorks)
	return p, err
}

const entityColumns = `
  id::text,
  entity_type::text,
  canonical_ko,
  COALESCE(canonical_en, ''),
  COALESCE(canonical_ja, ''),
  COALESCE(canonical_vi, ''),
  COALESCE(canonical_zh, ''),
  COALESCE(canonical_zh_hant, ''),
  COALESCE(canonical_es, ''),
  COALESCE(canonical_id, ''),
  COALESCE(canonical_pt_br, ''),
  aliases_ko,
  aliases_en,
  aliases_ja,
  aliases_vi,
  aliases_zh,
  aliases_zh_hant,
  aliases_es,
  aliases_id,
  aliases_pt_br,
  COALESCE(category_hint, ''),
  confidence::float8,
  source_urls,
  last_verified_at,
  created_at,
  updated_at,
  operator_locked,
  status,
  COALESCE(source_domains, '{}'::text[]),
  COALESCE(canonical_en_source, ''),
  COALESCE(canonical_ja_source, ''),
  COALESCE(canonical_vi_source, ''),
  COALESCE(canonical_zh_source, ''),
  COALESCE(canonical_zh_hant_source, ''),
  COALESCE(canonical_es_source, ''),
  COALESCE(canonical_id_source, ''),
  COALESCE(canonical_pt_br_source, ''),
  COALESCE(verification_tier, ''),
  COALESCE(verification_evidence, ''),
  COALESCE(occupation_domain, ''),
  COALESCE(gender, ''),
  COALESCE(kid, '')`

// personJoinColumns — 동명이인 구분 필드. kwave_entity_person_details 를
// 별칭 d 로 LEFT JOIN 한 SELECT 에서만 사용. entityColumns 뒤에 이어붙인다.
// disambig/needs_disambig 는 kwave_entities 본체에 있으므로 별칭 e 로 참조.
const personJoinColumns = `,
  COALESCE(e.disambig, ''),
  COALESCE(d.primary_role::text, ''),
  COALESCE(d.agency, ''),
  COALESCE(d.birth_year, 0),
  COALESCE(d.notable_works, '{}'::text[]),
  e.needs_disambig`

// entityColumnsQualified — entityColumns 의 각 컬럼을 별칭 e 로 한정한 버전.
// LEFT JOIN 쿼리에서 컬럼 모호성(ambiguous column) 방지. 단순 prefix 가 아닌
// 명시 버전 — id::text 등 표현식 때문에 별도 정의.
const entityColumnsQualified = `
  e.id::text,
  e.entity_type::text,
  e.canonical_ko,
  COALESCE(e.canonical_en, ''),
  COALESCE(e.canonical_ja, ''),
  COALESCE(e.canonical_vi, ''),
  COALESCE(e.canonical_zh, ''),
  COALESCE(e.canonical_zh_hant, ''),
  COALESCE(e.canonical_es, ''),
  COALESCE(e.canonical_id, ''),
  COALESCE(e.canonical_pt_br, ''),
  e.aliases_ko,
  e.aliases_en,
  e.aliases_ja,
  e.aliases_vi,
  e.aliases_zh,
  e.aliases_zh_hant,
  e.aliases_es,
  e.aliases_id,
  e.aliases_pt_br,
  COALESCE(e.category_hint, ''),
  e.confidence::float8,
  e.source_urls,
  e.last_verified_at,
  e.created_at,
  e.updated_at,
  e.operator_locked,
  e.status,
  COALESCE(e.source_domains, '{}'::text[]),
  COALESCE(e.canonical_en_source, ''),
  COALESCE(e.canonical_ja_source, ''),
  COALESCE(e.canonical_vi_source, ''),
  COALESCE(e.canonical_zh_source, ''),
  COALESCE(e.canonical_zh_hant_source, ''),
  COALESCE(e.canonical_es_source, ''),
  COALESCE(e.canonical_id_source, ''),
  COALESCE(e.canonical_pt_br_source, ''),
  COALESCE(e.verification_tier, ''),
  COALESCE(e.verification_evidence, ''),
  COALESCE(e.occupation_domain, ''),
  COALESCE(e.gender, ''),
  COALESCE(e.kid, '')`

type entityScanner interface {
	Scan(dest ...any) error
}

func scanEntity(row entityScanner) (Entity, error) {
	var ent Entity
	err := row.Scan(
		&ent.ID,
		&ent.EntityType,
		&ent.CanonicalKO,
		&ent.CanonicalEN,
		&ent.CanonicalJA,
		&ent.CanonicalVI,
		&ent.CanonicalZH,
		&ent.CanonicalZHHant,
		&ent.CanonicalES,
		&ent.CanonicalID,
		&ent.CanonicalPTBR,
		&ent.Aliases.KO,
		&ent.Aliases.EN,
		&ent.Aliases.JA,
		&ent.Aliases.VI,
		&ent.Aliases.ZH,
		&ent.Aliases.ZHHant,
		&ent.Aliases.ES,
		&ent.Aliases.ID,
		&ent.Aliases.PTBR,
		&ent.CategoryHint,
		&ent.Confidence,
		&ent.SourceURLs,
		&ent.LastVerifiedAt,
		&ent.CreatedAt,
		&ent.UpdatedAt,
		&ent.OperatorLocked,
		&ent.Status,
		&ent.SourceDomains,
		&ent.CanonicalENSource,
		&ent.CanonicalJASource,
		&ent.CanonicalVISource,
		&ent.CanonicalZHSource,
		&ent.CanonicalZHHantSource,
		&ent.CanonicalESSource,
		&ent.CanonicalIDSource,
		&ent.CanonicalPTBRSource,
		&ent.VerificationTier,
		&ent.VerificationEvidence,
		&ent.OccupationDomain,
		&ent.Gender,
		&ent.KID,
	)
	return ent, err
}

// scanEntityWithPerson — entityColumnsQualified + personJoinColumns 순서로
// 동명이인 구분 필드까지 스캔. ListEntities / GetEntity 의 LEFT JOIN 결과용.
func scanEntityWithPerson(row entityScanner) (Entity, error) {
	var ent Entity
	err := row.Scan(
		&ent.ID,
		&ent.EntityType,
		&ent.CanonicalKO,
		&ent.CanonicalEN,
		&ent.CanonicalJA,
		&ent.CanonicalVI,
		&ent.CanonicalZH,
		&ent.CanonicalZHHant,
		&ent.CanonicalES,
		&ent.CanonicalID,
		&ent.CanonicalPTBR,
		&ent.Aliases.KO,
		&ent.Aliases.EN,
		&ent.Aliases.JA,
		&ent.Aliases.VI,
		&ent.Aliases.ZH,
		&ent.Aliases.ZHHant,
		&ent.Aliases.ES,
		&ent.Aliases.ID,
		&ent.Aliases.PTBR,
		&ent.CategoryHint,
		&ent.Confidence,
		&ent.SourceURLs,
		&ent.LastVerifiedAt,
		&ent.CreatedAt,
		&ent.UpdatedAt,
		&ent.OperatorLocked,
		&ent.Status,
		&ent.SourceDomains,
		&ent.CanonicalENSource,
		&ent.CanonicalJASource,
		&ent.CanonicalVISource,
		&ent.CanonicalZHSource,
		&ent.CanonicalZHHantSource,
		&ent.CanonicalESSource,
		&ent.CanonicalIDSource,
		&ent.CanonicalPTBRSource,
		&ent.VerificationTier,
		&ent.VerificationEvidence,
		&ent.OccupationDomain,
		&ent.Gender,
		&ent.KID, // entityColumns 의 마지막 칸 — personJoinColumns 보다 앞이다
		&ent.Disambig,
		&ent.PrimaryRole,
		&ent.Agency,
		&ent.BirthYear,
		&ent.NotableWorks,
		&ent.NeedsDisambig,
	)
	return ent, err
}

func spellingsForLocale(ent Entity, locale string) ([]LocaleSpellings, error) {
	if locale == "all" {
		locales := []string{"ko", "en", "ja", "vi", "zh", "zh-hant", "es", "id", "pt-br"}
		out := make([]LocaleSpellings, 0, len(locales))
		for _, loc := range locales {
			item, err := singleLocaleSpellings(ent, loc)
			if err != nil {
				return nil, err
			}
			out = append(out, item)
		}
		return out, nil
	}
	item, err := singleLocaleSpellings(ent, locale)
	if err != nil {
		return nil, err
	}
	return []LocaleSpellings{item}, nil
}

func singleLocaleSpellings(ent Entity, locale string) (LocaleSpellings, error) {
	locale = normalizeLocale(locale)
	switch locale {
	case "ko":
		return makeSpellings("ko", ent.CanonicalKO, ent.Aliases.KO), nil
	case "en":
		return makeSpellings("en", ent.CanonicalEN, ent.Aliases.EN), nil
	case "ja":
		return makeSpellings("ja", ent.CanonicalJA, ent.Aliases.JA), nil
	case "vi":
		return makeSpellings("vi", ent.CanonicalVI, ent.Aliases.VI), nil
	case "zh":
		return makeSpellings("zh", ent.CanonicalZH, ent.Aliases.ZH), nil
	case "zh-hant":
		return makeSpellings("zh-hant", ent.CanonicalZHHant, ent.Aliases.ZHHant), nil
	case "es":
		return makeSpellings("es", ent.CanonicalES, ent.Aliases.ES), nil
	case "id":
		return makeSpellings("id", ent.CanonicalID, ent.Aliases.ID), nil
	case "pt-br":
		return makeSpellings("pt-br", ent.CanonicalPTBR, ent.Aliases.PTBR), nil
	default:
		return LocaleSpellings{}, fmt.Errorf("unsupported locale: %s", locale)
	}
}

func makeSpellings(locale, canonical string, aliases []string) LocaleSpellings {
	out := LocaleSpellings{
		Locale:    locale,
		Canonical: strings.TrimSpace(canonical),
		Aliases:   compactStrings(aliases),
	}
	out.Spellings = uniqueStrings(append([]string{out.Canonical}, out.Aliases...))
	return out
}

func normalizeLocale(locale string) string {
	locale = strings.ToLower(strings.TrimSpace(locale))
	locale = strings.ReplaceAll(locale, "_", "-")
	return locale
}

func entityLocaleColumns(locale string) (targetCol, aliasesCol string, err error) {
	switch normalizeLocale(locale) {
	case "en":
		return "canonical_en", "aliases_en", nil
	case "ja":
		return "canonical_ja", "aliases_ja", nil
	case "vi":
		return "canonical_vi", "aliases_vi", nil
	case "id", "id-id":
		return "canonical_id", "aliases_id", nil
	case "es", "es-es", "es-mx":
		return "canonical_es", "aliases_es", nil
	case "pt", "pt-br", "pt-pt":
		return "canonical_pt_br", "aliases_pt_br", nil
	// ★간체/번체 분리(CR-3, 2026-06-28): 'zh'(=mainland 기본)는 간체 canonical_zh 로.
	// 이전엔 'zh'→번체로 매핑돼 본토 중국어 소비자가 40% 글자 다른 번체를 받았다(zh≠zh_hant 1638건).
	case "zh", "zh-cn", "zh-hans", "zh-sg":
		return "canonical_zh", "aliases_zh", nil
	case "zh-hant", "zh-tw", "zh-hk", "zh-mo":
		return "canonical_zh_hant", "aliases_zh_hant", nil
	default:
		return "", "", fmt.Errorf("unsupported locale: %s", locale)
	}
}

// validEntityType — 유형 목록의 원본은 kdb.EntityTypes 하나다 (2026-09-16).
//
// 종전엔 여기 switch 로 적혀 있었고, 관리 화면은 **또 다른 목록**을 들고 있었다.
// 그 목록에는 없는 값 4개가 있고 있는 값 14개가 빠져 있어서, 운영자가 화면에서
// 새 유형을 고를 수도 승격시킬 수도 없었다. 목록을 두 벌 적으면 그렇게 된다.
func validEntityType(s string) bool { return kdb.ValidEntityType(s) }

func validEntityStatus(s string) bool {
	switch s {
	case "active", "candidate", "rejected":
		return true
	default:
		return false
	}
}

func compactStrings(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func uniqueStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		key := strings.ToLower(v)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, v)
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// updatedSinceArg — zero time 이면 nil(SQL NULL → 필터 미적용), 아니면 그대로.
func updatedSinceArg(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// writeError — 표준 에러 봉투 {"ok":false,"error":{"code","message"}} (2026-06-01).
// code 는 HTTP status 에서 자동 도출. 기존 호출부 변경 없이 봉투만 구조화.
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{
		"ok": false,
		"error": map[string]string{
			"code":    errorCode(status),
			"message": message,
		},
	})
}

// writeErrorCode — 상태 코드가 정하는 기본 code 대신 **무엇이 틀렸는지 가리키는
// code** 를 쓴다. `bad_request` 는 소비자가 "본문이 깨졌나" 부터 보게 만든다 —
// type 오타라면 `invalid_type` 이라고 말해 주는 편이 한 번에 고치게 한다(§5-3 모양 동일).
func writeErrorCode(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"ok": false,
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}

func errorCode(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "bad_request"
	case http.StatusUnauthorized:
		return "unauthorized"
	case http.StatusForbidden:
		// 문서(§5-3)가 forbidden 이라고 말한다. 여기서 internal 을 돌려주면
		// 소비자가 '우리 결함'으로 읽고 재시도한다 — 재시도로 풀릴 일이 아니다.
		return "forbidden"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusServiceUnavailable:
		return "unavailable"
	default:
		return "internal"
	}
}

// waitingCandidateID — 이 이름으로 **candidate 로만** 앉아 있는 행의 id.
//
// ★active 가 하나라도 있으면 빈 문자열을 돌려준다. 그건 답이 나가는 낱말이고,
//
//	candidate 쪽은 동명이인 분기이거나 중복이다 — 소비자가 기다리는 행이 아니다.
//	여기서 그것까지 밀면 요청과 무관한 일을 요청 예산으로 하는 셈이 된다.
//
// ★유형은 **맞으면 우선, 없으면 무시**다.
//
//	오늘 트래픽의 대부분은 유형을 안 보낸다. 유형을 조건으로 걸면 그 소비자들이
//	보낸 요청은 훅을 한 번도 못 건다 — 정렬로만 쓰고 걸러내지 않는다.
//	같은 이름에 유형이 여럿이면 요청 유형과 맞는 것을, 없으면 최근 것을 고른다.
func (s *Store) waitingCandidateID(ctx context.Context, normKey, entityType string) string {
	if s.Pool == nil || normKey == "" {
		return ""
	}
	var id string
	err := s.Pool.QueryRow(ctx, `
WITH m AS (
  SELECT e.id, e.status, e.entity_type::text AS etype, e.updated_at
    FROM kwave_entities e
   WHERE (lower(regexp_replace(btrim(e.canonical_ko), '[[:space:][:punct:]]+', '', 'g')) = $1
          OR EXISTS (SELECT 1 FROM unnest(COALESCE(e.aliases_ko,'{}')) a
                      WHERE lower(regexp_replace(btrim(a), '[[:space:][:punct:]]+', '', 'g')) = $1))
     AND e.status IN ('active','candidate')
     AND e.operator_locked = false
)
SELECT id::text FROM m
 WHERE status = 'candidate'
   AND NOT EXISTS (SELECT 1 FROM m a WHERE a.status = 'active')
 ORDER BY (etype = $2) DESC, updated_at DESC
 LIMIT 1`, normKey, entityType).Scan(&id)
	if err != nil {
		return ""
	}
	return id
}

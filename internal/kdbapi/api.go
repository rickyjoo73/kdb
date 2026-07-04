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
	"github.com/rickyjoo73/kdb/internal/kdb/agents/gatekeeper"
	"github.com/rickyjoo73/kdb/internal/kdb/codexcli"
	"github.com/rickyjoo73/kdb/internal/kdb/corrections"
	"github.com/rickyjoo73/kdb/internal/kdb/enrich"
	"github.com/rickyjoo73/kdb/internal/kdb/ratelimit"
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
}

type RouterOptions struct {
	RequestTimeout time.Duration
	LogRequests    bool
	APIKeys        []string
	// OnResearchEnqueue — 신규 발굴 키워드 적재 시 호출(워커 즉시 nudge). 서버 모드에서 배선.
	OnResearchEnqueue func()
}

type Entity struct {
	ID              string    `json:"id"`
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
	Type   string `json:"type,omitempty"`
	Status string `json:"status,omitempty"`
	Limit  int    `json:"limit,omitempty"`
	// VerifiedOnly — true 면 미검증 출처(codex/romanization/wikipedia/rss 등) locale 값을
	// 비우고 검증된 표기만 반환 + locale_provenance 부착. 기본 false(기존 동작 보존).
	VerifiedOnly bool `json:"verified_only,omitempty"`
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
}

// PrepareTerm — 파싱된 term(ko + 선택 type).
type PrepareTerm struct {
	Ko        string `json:"ko"`
	Type      string `json:"type,omitempty"`
	SourceURL string `json:"source_url,omitempty"` // term 별 출처 URL(override, 옵션)
}

// PrepareItem — term 1건의 준비 상태.
type PrepareItem struct {
	Term     string            `json:"term"`
	Status   string            `json:"status"` // ready | preparing | new | out_of_scope
	Type     string            `json:"type,omitempty"`
	EntityID string            `json:"entity_id,omitempty"`
	Values   map[string]string `json:"values,omitempty"`  // 현재 가용 locale 표기
	Provenance map[string]string `json:"provenance,omitempty"` // values 각 locale 의 출처 라벨
	Missing  []string          `json:"missing,omitempty"` // 아직 준비중인 locale
}

type PrepareResponse struct {
	Items []PrepareItem `json:"items"`
}

type LookupResponse struct {
	Query   string   `json:"query"`
	Matches []Entity `json:"matches"`
}

type BulkLookupRequest struct {
	Queries []string `json:"queries"`
	Type    string   `json:"type,omitempty"`
	Status  string   `json:"status,omitempty"`
	Limit   int      `json:"limit,omitempty"`
}

type BulkLookupResponse struct {
	Results []LookupResponse `json:"results"`
}

type MatchEntitiesRequest struct {
	SourceText string `json:"source_text"`
	Locale     string `json:"locale"`
	Limit      int    `json:"limit,omitempty"`
	// 소비자 게이팅 파라미터 (2026-06-01). 번역 핫패스에서 저신뢰 힌트 차단용.
	MinConfidence float64 `json:"min_confidence,omitempty"` // 기본 0.50 floor
	Status        string  `json:"status,omitempty"`         // active|candidate|rejected (빈값=active — rejected tombstone 유출 방지)
	VerifiedOnly  bool    `json:"verified_only,omitempty"`  // operator_locked OR wikidata OR ≥2매체합의만
}

type MatchedEntity struct {
	ID             string    `json:"id"`
	KO             string    `json:"ko"`
	LocaleName     string    `json:"locale_name"`
	EntityType     string    `json:"entity_type"`
	Confidence     float64   `json:"confidence"`
	Status         string    `json:"status"`
	OperatorLocked bool      `json:"operator_locked"`
	Provenance     string    `json:"provenance"`              // operator-locked|wikidata-label|external-db|media-consensus|wikipedia-langlinks|media-single|llm-only
	LocaleSource   string    `json:"locale_source,omitempty"` // 반환된 locale 값의 raw source 컬럼(canonical_<loc>_source). 소비자 게이팅용.
	SourceURLs     []string  `json:"source_urls,omitempty"`
	UpdatedAt      time.Time `json:"updated_at"`
	SourceAliases  []string  `json:"source_aliases,omitempty"`
	TargetAliases  []string  `json:"target_aliases,omitempty"`
	Note           string    `json:"note,omitempty"`
	Disambig       string    `json:"disambig,omitempty"`         // 동명이인 구분 라벨(예: "(김하늘 배우)"). 비어있으면 단독.
	LocaleAmbiguous bool     `json:"locale_ambiguous,omitempty"` // 반환된 locale_name 이 같은 type 의 다른 active entity 와 겹침 → 번역 시 확인 권장. entity 레벨 needs_disambig(한국어 동명이인)와는 별개 신호(목표 locale 표기 충돌).
	LocaleFallback  bool     `json:"locale_fallback,omitempty"`  // 요청 locale 표기가 없어 locale_name 이 영어(canonical_en)로 폴백됨 → 해당 언어 표기 아님.
}

type BulkMatchEntitiesRequest struct {
	SourceTexts   []string `json:"source_texts"`
	Locale        string   `json:"locale"`
	Limit         int      `json:"limit,omitempty"`
	MinConfidence float64  `json:"min_confidence,omitempty"`
	Status        string   `json:"status,omitempty"` // active|candidate|rejected (빈값=active — rejected tombstone 유출 방지)
	VerifiedOnly  bool     `json:"verified_only,omitempty"`
}

type BulkMatchResult struct {
	SourceText string          `json:"source_text"`
	Entities   []MatchedEntity `json:"entities"`
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
		store:    &Store{Pool: pool, onEnqueue: opts.OnResearchEnqueue},
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
	// A8 MatchMissExtractor (flag KDB_MATCH_LLM_EXTRACT). 오너 방침대로 gemma 라우팅
	// (codex 최소). 미설정이면 nil → match-miss 시 아무것도 안 함(기존 동작 불변).
	if os.Getenv("KDB_MATCH_LLM_EXTRACT") == "1" {
		ex := kdb.NewCodexExtractor()
		ex.Runner = ex.Runner.WithProvider(codexcli.RoleProvider("MATCHEXTRACT", "gemma"))
		h.matchExtractor = ex
	}
	// "추측=빈칸" 서빙 게이트(오너 방침). 기본 on, KDB_SERVE_HIDE_LLM_ONLY=0 으로 해제.
	h.hideLLMServe = os.Getenv("KDB_SERVE_HIDE_LLM_ONLY") != "0"
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
	r.Get("/v1/health", h.health)
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
		protected.Post("/v1/research-queue", h.createResearchQueue)
		protected.With(requireWriteScope).Get("/v1/qa/work", h.qaWork)
		protected.With(requireWriteScope).Post("/v1/qa/result", h.qaResult)
		protected.Post("/v1/lookup", h.lookup)
		protected.Post("/v1/lookup/bulk", h.bulkLookup)
	})
	return r
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
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"service":  "kdb-api",
		"phase":    "1A",
		"entities": count,
	})
}

func (h *handler) listEntities(w http.ResponseWriter, r *http.Request) {
	filter := filterFromRequest(r)
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
func (h *handler) createCorrection(w http.ResponseWriter, r *http.Request) {
	if h.corrections == nil || h.corrections.Pool == nil {
		writeError(w, http.StatusServiceUnavailable, "corrections unavailable")
		return
	}
	var req corrections.Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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
			if looksLikeEntityName(req.Ko) {
				_, _ = h.store.EnqueueResearch(r.Context(), ResearchQueueRequest{EntityKO: strings.TrimSpace(req.Ko)})
				writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "result": map[string]any{
					"status":     "queued",
					"resolution": "미보유 고유명사 — 발굴 큐 접수(준비 후 표기 제공). 준비되면 재신고 가능.",
				}})
				return
			}
			// false-reject 가시화(2026-06-20): 거부된 ko 를 로그로 남겨 게이트키퍼가
			// 실재 K-엔티티를 노이즈로 오거부하는지 사후 재검증 가능하게 한다.
			log.Printf("kdb-api: correction noise-reject ko=%q reporter=%s (looksLikeEntityName=false)",
				strings.TrimSpace(req.Ko), reporterID(r))
			writeError(w, http.StatusUnprocessableEntity, "noise/out-of-scope ko")
			return
		}
		if strings.Contains(msg, "required") || strings.Contains(msg, "invalid") ||
			strings.Contains(msg, "unsupported") || strings.Contains(msg, "ambiguous") ||
			strings.Contains(msg, "not found") {
			writeError(w, http.StatusBadRequest, msg)
			return
		}
		writeError(w, http.StatusInternalServerError, "correction failed")
		return
	}
	status := http.StatusAccepted // queued
	if res.Status == "auto_applied" {
		status = http.StatusOK
	} else if res.Status == "rejected" {
		status = http.StatusUnprocessableEntity
	}
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
	queued, err := h.store.EnqueueResearch(r.Context(), req)
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
	if !queued {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{
		"ok":     true,
		"queued": queued,
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
	if req.Query == "" {
		writeError(w, http.StatusBadRequest, "query required")
		return
	}
	matches, err := h.store.ListEntities(r.Context(), EntityFilter{
		Query:  req.Query,
		Type:   req.Type,
		Status: req.Status,
		Limit:  req.Limit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	// 응답은 즉시 — 빈 locale 있는 match 는 background goroutine 에서 enrich.
	// 다음 lookup 부터 채워진 값 반환.
	if h.bgEnrich != nil {
		for _, m := range matches {
			if hasEmptyPriorityLocale(m) {
				if id, err := uuid.Parse(m.ID); err == nil {
					h.bgEnrich.Trigger(id)
				}
			}
		}
	}
	// 발굴 트리거 (2026-06-01): KDB 에 없는 이름이면 research_queue 에 적재 →
	// research worker 가 on-demand 검색으로 발굴. 핫패스를 막지 않게 async.
	if len(matches) == 0 {
		h.enqueueDiscovery(req.Query)
	}
	// "추측=빈칸" 서빙 게이트(오너 방침, 2026-07-04): codex 추측 locale 값을 항상 비운다.
	// 출처있는 값(위키/음역/검색확정/매체)은 유지. verified_only(엄격)와 독립·선행.
	if h.hideLLMServe {
		for i := range matches {
			stripLLMOnlyLocales(&matches[i])
		}
	}
	// verified_only 게이트 (2026-06-29): 미검증 locale 값 제거 + provenance 부착.
	// enrich/발굴 트리거는 위에서 실제 DB 상태로 이미 수행됨(게이트는 응답 직전에만 적용).
	if req.VerifiedOnly {
		for i := range matches {
			applyLocaleVerifiedGate(&matches[i])
		}
	}
	writeJSON(w, http.StatusOK, LookupResponse{Query: req.Query, Matches: matches})
}

// enqueueDiscovery — lookup miss 한 이름을 발굴 큐에 넣는다(게이트 통과분만, async).
// match(자유 본문)에는 적용 안 함 — 문장에서 이름을 추출할 수 없으므로(그건 RSS 추출기 몫).
func (h *handler) enqueueDiscovery(query string) {
	if h.store == nil || h.store.Pool == nil {
		return
	}
	if !looksLikeEntityName(query) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = h.store.EnqueueResearch(ctx, ResearchQueueRequest{
			EntityKO:    strings.TrimSpace(query),
			ContextHint: "lookup-miss",
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
	for _, raw := range req.Terms {
		pt := parsePrepareTerm(raw)
		if pt.Ko == "" {
			continue
		}
		// type 힌트가 주어졌는데 KDB 의 K-콘텐츠 type 이 아니면 즉시 out_of_scope.
		if pt.Type != "" && !validEntityType(pt.Type) {
			items = append(items, PrepareItem{Term: pt.Ko, Type: pt.Type, Status: "out_of_scope"})
			continue
		}
		matches, err := h.store.ListEntities(r.Context(), EntityFilter{Query: pt.Ko, Type: pt.Type, Status: "active", Limit: 5})
		if err != nil {
			items = append(items, PrepareItem{Term: pt.Ko, Type: pt.Type, Status: "new"})
			continue
		}
		// canonical_ko 정확 일치(또는 alias 일치) 우선 — 부분일치 노이즈 배제.
		// type 힌트가 있으면 그 type 우선.
		ent, ok := exactKoMatch(matches, pt.Ko, pt.Type)
		if !ok {
			// 모르는 고유명사 → 발굴 큐(분류·enrich 파이프라인이 K-콘텐츠 여부 판단).
			// 노이즈는 looksLikeEntityName 게이트가 거름. K-콘텐츠 아니면 분류가 reject.
			if looksLikeEntityName(pt.Ko) {
				srcURL := pt.SourceURL
				if srcURL == "" {
					srcURL = req.SourceURL // batch 레벨 폴백
				}
				_, _ = h.store.EnqueueResearch(r.Context(), ResearchQueueRequest{
					EntityKO: pt.Ko, RequestedEntityType: pt.Type, SourceURL: srcURL})
				items = append(items, PrepareItem{Term: pt.Ko, Type: pt.Type, Status: "new"})
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
		it := PrepareItem{Term: pt.Ko, Type: ent.EntityType, EntityID: ent.ID, Values: values, Provenance: prov, Missing: missing}
		if len(missing) == 0 {
			it.Status = "ready"
		} else {
			it.Status = "preparing"
			if h.bgEnrich != nil {
				if id, err := uuid.Parse(ent.ID); err == nil {
					h.bgEnrich.Trigger(id) // 즉시 백그라운드 enrich(동시성 캡 내)
				}
			}
		}
		items = append(items, it)
	}
	writeJSON(w, http.StatusOK, PrepareResponse{Items: items})
}

// parsePrepareTerm — terms 원소를 문자열 또는 {ko,type} 객체로 파싱.
func parsePrepareTerm(raw json.RawMessage) PrepareTerm {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return PrepareTerm{Ko: strings.TrimSpace(s)}
	}
	var o PrepareTerm
	if json.Unmarshal(raw, &o) == nil {
		return PrepareTerm{Ko: strings.TrimSpace(o.Ko), Type: strings.TrimSpace(o.Type), SourceURL: strings.TrimSpace(o.SourceURL)}
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

// exactKoMatch — canonical_ko 정확 일치(또는 alias_ko 포함) entity 1건. typeHint
// 가 있으면 그 type 을 우선(동명이인이 type 다를 때 정확도↑).
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
	return Entity{}, false
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
// provenance 정의를 한 곳에서 일치시켜 드리프트를 막는다. source 미기록('') 레거시 행은
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
	case "opencc":
		return "opencc"
	case "mydramalist":
		return "community-db"
	case "codex-fallback":
		return "llm-only"
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
	fields := []struct {
		loc string
		val *string
	}{
		{"en", &e.CanonicalEN}, {"ja", &e.CanonicalJA}, {"vi", &e.CanonicalVI},
		{"zh", &e.CanonicalZH}, {"zh_hant", &e.CanonicalZHHant}, {"es", &e.CanonicalES},
		{"id", &e.CanonicalID}, {"pt_br", &e.CanonicalPTBR},
	}
	for _, f := range fields {
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
		v := strings.TrimSpace(get[loc])
		if v == "" {
			if _, known := get[loc]; known {
				missing = append(missing, loc)
			}
			continue
		}
		p := localeProvenanceLabel(e, localeSourceFor(e, loc))
		if verifiedOnly && !provenanceIsVerified(p) {
			missing = append(missing, loc) // 미검증 — 준비중으로 분류(노출 보류)
			continue
		}
		values[loc] = v
		prov[loc] = p
	}
	return values, prov, missing
}

// looksLikeEntityName — 발굴 큐 적재 게이트(prepare/lookup 공용). 자율 파이프라인과
// 동일한 gatekeeper.PreGate 를 써서, 외부 API 로 들어오는 노이즈(문장/명령형/일반어
// 꼬리/난수/깨진자소/일반어구)를 진입 시점에 거른다. PreReject = 등록 안 함
// (out_of_scope). PreGray/Keep = 발굴 파이프라인 진입(이후 classify 가 keep/reject).
// 정밀 검증은 worker 의 Wikidata 이름검증 + classify 가 담당.
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
	out := BulkLookupResponse{Results: make([]LookupResponse, 0, len(req.Queries))}
	for _, q := range req.Queries {
		q = strings.TrimSpace(q)
		if q == "" {
			continue
		}
		matches, err := h.store.ListEntities(r.Context(), EntityFilter{
			Query:  q,
			Type:   req.Type,
			Status: req.Status,
			Limit:  req.Limit,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "query failed")
			return
		}
		if len(matches) == 0 {
			h.enqueueDiscovery(q)
		}
		out.Results = append(out.Results, LookupResponse{Query: q, Matches: matches})
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
	entities, err := h.store.MatchEntitiesForLocale(r.Context(), req)
	if err != nil {
		if strings.Contains(err.Error(), "unsupported locale") {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	// "추측=빈칸"(오너 방침): 반환 locale_name 의 출처가 codex 추측이면 표기를 비운다.
	// 소비자는 엔티티는 매칭됐으나 검증된 다국어 표기는 아직 없음을 안다(빈칸>틀린값).
	// LocaleSource 는 effectiveSourceExpr 로 실제 반환값(en 폴백 포함)의 출처를 반영.
	if h.hideLLMServe {
		for i := range entities {
			if entities[i].LocaleSource == "codex-fallback" {
				entities[i].LocaleName = ""
				entities[i].LocaleFallback = false
			}
		}
	}
	// A8: 0건 매칭이면 본문에서 K-콘텐츠 한글명을 추출해 발굴 큐에 적재(비동기).
	if len(entities) == 0 {
		h.enqueueFromText(req.SourceText, req.Locale)
	}
	writeJSON(w, http.StatusOK, map[string]any{"entities": entities})
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
			if ko == "" || seen[ko] || !looksLikeEntityName(ko) {
				continue
			}
			seen[ko] = true
			_, _ = h.store.EnqueueResearch(ctx, ResearchQueueRequest{
				EntityKO:    ko,
				ContextHint: "match-miss",
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
		out.Results = append(out.Results, BulkMatchResult{SourceText: text, Entities: entities})
	}
	writeJSON(w, http.StatusOK, out)
}

func filterFromRequest(r *http.Request) EntityFilter {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	minConf, _ := strconv.ParseFloat(q.Get("min_confidence"), 64)
	var since time.Time
	if v := strings.TrimSpace(q.Get("updated_since")); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			since = t
		}
	}
	return EntityFilter{
		Query:         q.Get("q"),
		Type:          q.Get("type"),
		Status:        q.Get("status"),
		Limit:         limit,
		Offset:        offset,
		MinConfidence: minConf,
		UpdatedSince:  since,
	}
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
       EXISTS (
         SELECT 1
         FROM unnest(e.aliases_ko || e.aliases_en || e.aliases_ja || e.aliases_vi ||
                     e.aliases_zh || e.aliases_zh_hant || e.aliases_es || e.aliases_id ||
                     e.aliases_pt_br) AS a(alias)
         WHERE a.alias ILIKE $2
       ))
  AND ($3 = '' OR e.entity_type::text = $3)
  AND ($4 = 'all' OR e.status = $4)
  AND ($7 = 0 OR e.confidence >= $7)
  AND ($8::timestamptz IS NULL OR e.updated_at >= $8)
ORDER BY
  CASE
    WHEN $1 <> '' AND e.canonical_ko = $1 THEN 0
    WHEN $1 <> '' AND (e.canonical_ko ILIKE $2 OR COALESCE(e.canonical_en, '') ILIKE $2) THEN 1
    ELSE 2
  END,
  e.confidence DESC,
  e.updated_at DESC
LIMIT $5 OFFSET $6`, filter.Query, like, filter.Type, filter.Status, filter.Limit, filter.Offset, filter.MinConfidence, updatedSinceArg(filter.UpdatedSince))
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
	    WHEN (` + effSrc + `) = 'opencc' THEN 'opencc'
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

	q := fmt.Sprintf(`
SELECT id::text,
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
 WHERE COALESCE(NULLIF(%[1]s,''), NULLIF(canonical_en,''), '') <> ''
   AND confidence >= $2%[4]s%[5]s
   AND (
        -- ★어절경계 매칭(CR-2, 2026-06-28): strpos 부분문자열은 일상어를 인명으로 오매칭한다
        -- (진짜→진, 나비→비, 가을바람→가을). 길이별 가드:
        --  · 1자 정본: 매칭 제외(20건, 오탐 과다).
        --  · 2~3자 순수한글 정본: 어절경계 정규식 — 앞은 비한글, 뒤는 조사 allowlist 또는
        --    비한글/끝일 때만(부분문자열·합성어 오탐 차단, 조사부착 정상매칭은 보존).
        --  · 2~3자 비순수한글(라틴/숫자 혼합, 드묾): strpos(메타문자 이스케이프 회피).
        --  · 4자+ 정본: strpos 유지(긴 정본은 부분문자열 오탐 거의 없음, recall 보존).
        (char_length(canonical_ko) >= 4 AND strpos($1, canonical_ko) > 0)
        OR (char_length(canonical_ko) BETWEEN 2 AND 3 AND canonical_ko ~ '^[가-힣]+$'
            AND $1 ~ ('(^|[^가-힣])' || canonical_ko ||
                      '(은|는|이|가|을|를|와|과|의|에|에서|에게|한테|도|로|으로|만|까지|부터|보다|처럼|랑|이랑|[^가-힣]|$)'))
        OR (char_length(canonical_ko) BETWEEN 2 AND 3 AND canonical_ko !~ '^[가-힣]+$'
            AND strpos($1, canonical_ko) > 0)
        OR EXISTS (
          SELECT 1
            FROM unnest(aliases_ko) AS a(alias)
           -- D-9: 라틴 alias 대소문자 무시(BTS↔bts). 한글은 lower() 무영향.
           WHERE alias <> '' AND char_length(alias) >= 2 AND position(lower(alias) in lower($1)) > 0
        )
   )
 ORDER BY confidence DESC, length(canonical_ko) DESC, last_verified_at DESC
 LIMIT $%[6]d`, targetCol, aliasesCol, localeProvenanceExpr(effSrc), statusClause, verifiedClause, limitParam, effSrc)

	rows, err := s.Pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]MatchedEntity, 0, 16)
	for rows.Next() {
		var e MatchedEntity
		if err := rows.Scan(&e.ID, &e.KO, &e.LocaleName, &e.EntityType, &e.Confidence, &e.Status, &e.OperatorLocked, &e.Provenance, &e.LocaleSource, &e.SourceURLs, &e.UpdatedAt, &e.SourceAliases, &e.TargetAliases, &e.Note, &e.Disambig, &e.LocaleAmbiguous, &e.LocaleFallback); err != nil {
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

func (s *Store) EnqueueResearch(ctx context.Context, req ResearchQueueRequest) (bool, error) {
	entityKO := strings.TrimSpace(req.EntityKO)
	if entityKO == "" {
		return false, fmt.Errorf("entity_ko required")
	}
	entityType := strings.TrimSpace(req.RequestedEntityType)
	if entityType == "" {
		entityType = "unknown"
	}
	if !validEntityType(entityType) {
		return false, fmt.Errorf("invalid entity type")
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
			return false, fmt.Errorf("invalid source_id")
		}
		sourceID = id
	}
	sourceURL := strings.TrimSpace(req.SourceURL)
	if len([]rune(sourceURL)) > 500 {
		sourceURL = string([]rune(sourceURL)[:500])
	}
	var inserted string
	err := s.Pool.QueryRow(ctx, `
INSERT INTO kwave_entity_research_queue
  (entity_ko, requested_entity_type, context_hint, source_id, source_url, status)
SELECT $1, $2::kwave_entity_type, NULLIF($3,''), $4::uuid, NULLIF($5,''), 'pending'
WHERE NOT EXISTS (
  SELECT 1
    FROM kwave_entity_research_queue
   WHERE entity_ko = $1
     AND requested_entity_type = $2::kwave_entity_type
     AND source_id IS NOT DISTINCT FROM $4::uuid
)
ON CONFLICT DO NOTHING
RETURNING id::text`, entityKO, entityType, contextHint, sourceID, sourceURL).Scan(&inserted)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil // 중복 — 이미 큐에 있음(nudge 불필요)
	}
	if err == nil && inserted != "" && s.onEnqueue != nil {
		s.onEnqueue() // 신규 적재 → 워커 즉시 nudge(pick 대기 ≤15s 제거)
	}
	return err == nil, err
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
  COALESCE(verification_evidence, '')`

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
  COALESCE(e.verification_evidence, '')`

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

func validEntityType(s string) bool {
	switch s {
	case "person", "group", "show", "drama", "movie", "song_album", "agency",
		"channel_outlet", "brand_place", "event_tour", "character", "term", "unknown":
		return true
	default:
		return false
	}
}

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

func errorCode(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "bad_request"
	case http.StatusUnauthorized:
		return "unauthorized"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusServiceUnavailable:
		return "unavailable"
	default:
		return "internal"
	}
}

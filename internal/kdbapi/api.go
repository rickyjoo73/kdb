// Package kdbapi exposes the KDB platform read API.
package kdbapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
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
	"github.com/rickyjoo73/kdb/internal/kdb/enrich"
)

const (
	defaultLimit      = 20
	maxLimit          = 100
	defaultMatchLimit = 100
	maxMatchLimit     = 200
)

type Store struct {
	Pool *pgxpool.Pool
}

type RouterOptions struct {
	RequestTimeout time.Duration
	LogRequests    bool
	APIKeys        []string
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
	Status        string  `json:"status,omitempty"`         // active|candidate|rejected (빈값=필터 없음)
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
	Provenance     string    `json:"provenance"` // operator-locked|wikidata-label|media-consensus|wikipedia-langlinks|llm-only
	SourceURLs     []string  `json:"source_urls,omitempty"`
	UpdatedAt      time.Time `json:"updated_at"`
	SourceAliases  []string  `json:"source_aliases,omitempty"`
	TargetAliases  []string  `json:"target_aliases,omitempty"`
	Note           string    `json:"note,omitempty"`
}

type BulkMatchEntitiesRequest struct {
	SourceTexts   []string `json:"source_texts"`
	Locale        string   `json:"locale"`
	Limit         int      `json:"limit,omitempty"`
	MinConfidence float64  `json:"min_confidence,omitempty"`
	Status        string   `json:"status,omitempty"`
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
		store:    &Store{Pool: pool},
		bgEnrich: enrich.NewBackgroundTrigger(pool),
	}
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
	r.Group(func(protected chi.Router) {
		if len(opts.APIKeys) > 0 || pool != nil {
			protected.Use(newAPIKeyAuthenticator(pool, opts.APIKeys).middleware)
		}
		protected.Get("/v1/entities", h.listEntities)
		protected.Post("/v1/entities/match/bulk", h.bulkMatchEntities)
		protected.Post("/v1/entities/match", h.matchEntities)
		protected.Get("/v1/entities/{id}/external-refs", h.getExternalRefs)
		protected.Get("/v1/entities/{id}/relations", h.getRelations)
		protected.Post("/v1/entities/{id}/site-search", h.siteSearchEntity)
		protected.Patch("/v1/entities/{id}", h.patchEntity)
		protected.Post("/v1/entities/{id}/lock", h.lockEntity)
		protected.Get("/v1/entities/{id}", h.getEntity)
		protected.Get("/v1/entities/{id}/spellings", h.getSpellings)
		protected.Get("/v1/persons/{id}", h.getPersonDetails)
		protected.Post("/v1/observations", h.createObservation)
		protected.Post("/v1/research-queue", h.createResearchQueue)
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
	dbKeys  map[string]string // key -> consumer id
	expires time.Time
}

const apiKeyCacheTTL = 30 * time.Second

func newAPIKeyAuthenticator(pool *pgxpool.Pool, envKeys []string) *apiKeyAuthenticator {
	return &apiKeyAuthenticator{pool: pool, envKeys: compactStrings(envKeys)}
}

func (a *apiKeyAuthenticator) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := requestAPIKey(r)
		if key == "" || !a.valid(r.Context(), key) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="kdb-api"`)
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *apiKeyAuthenticator) valid(ctx context.Context, key string) bool {
	if validAPIKey(key, a.envKeys) {
		return true
	}
	if a.pool == nil {
		return false
	}
	id, ok := a.lookupDBKey(ctx, key)
	if ok {
		a.touch(id)
	}
	return ok
}

func (a *apiKeyAuthenticator) lookupDBKey(ctx context.Context, key string) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.dbKeys == nil || time.Now().After(a.expires) {
		a.refreshLocked(ctx)
	}
	id, ok := a.dbKeys[key]
	return id, ok
}

// refreshLocked — 호출자가 mu 보유. 실패해도 기존 캐시(없으면 빈 맵)를 유지하고 TTL 갱신.
func (a *apiKeyAuthenticator) refreshLocked(ctx context.Context) {
	a.expires = time.Now().Add(apiKeyCacheTTL)
	rows, err := a.pool.Query(ctx, `SELECT id::text, key FROM kwave_kdb_api_consumers WHERE active`)
	if err != nil {
		if a.dbKeys == nil {
			a.dbKeys = map[string]string{}
		}
		return
	}
	defer rows.Close()
	next := make(map[string]string, 16)
	for rows.Next() {
		var id, k string
		if err := rows.Scan(&id, &k); err != nil {
			continue
		}
		next[k] = id
	}
	a.dbKeys = next
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
	store    *Store
	bgEnrich *enrich.BackgroundTrigger
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

// looksLikeEntityName — 발굴 큐 적재 게이트. 노이즈(빈문자/숫자/문장/깨진자소) 거름.
// 정밀 검증은 worker 의 Wikidata 이름검증이 담당 — 여기선 헐겁게 1차 필터만.
func looksLikeEntityName(q string) bool {
	q = strings.TrimSpace(q)
	runes := []rune(q)
	if len(runes) < 2 || len(runes) > 40 {
		return false
	}
	if len(strings.Fields(q)) > 5 { // 문장으로 보이면 거름
		return false
	}
	hasLetter := false
	for _, r := range runes {
		if r >= 0x3130 && r <= 0x318F { // 한글 호환 자모(깨진 자소) → 거름
			return false
		}
		if unicode.IsLetter(r) {
			hasLetter = true
		}
	}
	return hasLetter
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
	writeJSON(w, http.StatusOK, map[string]any{"entities": entities})
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

func (s *Store) MatchEntitiesForLocale(ctx context.Context, req MatchEntitiesRequest) ([]MatchedEntity, error) {
	req = req.normalized()
	targetCol, aliasesCol, err := entityLocaleColumns(req.Locale)
	if err != nil {
		return nil, err
	}
	// $1 source_text, $2 min_confidence. 선택 절(status/verified)·limit 은 동적 번호.
	args := []any{req.SourceText, req.MinConfidence}
	statusClause := ""
	if req.Status != "" {
		args = append(args, req.Status)
		statusClause = fmt.Sprintf("\n   AND status = $%d", len(args))
	}
	verifiedClause := ""
	if req.VerifiedOnly {
		verifiedClause = "\n   AND " + provenanceVerifiedExpr
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
       COALESCE(source_urls, '{}'::text[]),
       updated_at,
       aliases_ko,
       %[2]s AS target_aliases,
       COALESCE(notes,'')
  FROM kwave_entities
 WHERE COALESCE(NULLIF(%[1]s,''), NULLIF(canonical_en,''), '') <> ''
   AND confidence >= $2%[4]s%[5]s
   AND (
        strpos($1, canonical_ko) > 0
        OR EXISTS (
          SELECT 1
            FROM unnest(aliases_ko) AS a(alias)
           WHERE alias <> '' AND char_length(alias) >= 2 AND strpos($1, alias) > 0
        )
   )
 ORDER BY confidence DESC, length(canonical_ko) DESC, last_verified_at DESC
 LIMIT $%[6]d`, targetCol, aliasesCol, provenanceExpr, statusClause, verifiedClause, limitParam)

	rows, err := s.Pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]MatchedEntity, 0, 16)
	for rows.Next() {
		var e MatchedEntity
		if err := rows.Scan(&e.ID, &e.KO, &e.LocaleName, &e.EntityType, &e.Confidence, &e.Status, &e.OperatorLocked, &e.Provenance, &e.SourceURLs, &e.UpdatedAt, &e.SourceAliases, &e.TargetAliases, &e.Note); err != nil {
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
	var sourceID any
	if strings.TrimSpace(req.SourceID) != "" {
		id, err := uuid.Parse(strings.TrimSpace(req.SourceID))
		if err != nil {
			return false, fmt.Errorf("invalid source_id")
		}
		sourceID = id
	}
	var inserted string
	err := s.Pool.QueryRow(ctx, `
INSERT INTO kwave_entity_research_queue
  (entity_ko, requested_entity_type, context_hint, source_id, status)
SELECT $1, $2::kwave_entity_type, NULLIF($3,''), $4::uuid, 'pending'
WHERE NOT EXISTS (
  SELECT 1
    FROM kwave_entity_research_queue
   WHERE entity_ko = $1
     AND requested_entity_type = $2::kwave_entity_type
     AND source_id IS NOT DISTINCT FROM $4::uuid
)
ON CONFLICT DO NOTHING
RETURNING id::text`, entityKO, entityType, strings.TrimSpace(req.ContextHint), sourceID).Scan(&inserted)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
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
  COALESCE(source_domains, '{}'::text[])`

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
  COALESCE(e.source_domains, '{}'::text[])`

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
	case "zh", "zh-hant", "zh-tw", "zh-hk":
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

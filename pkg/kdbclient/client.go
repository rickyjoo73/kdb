// Package kdbclient is a small HTTP client for the KDB platform API.
package kdbclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	BaseURL     string
	APIKey      string
	WorkspaceID string
	HTTPClient  *http.Client
}

type Health struct {
	OK       bool   `json:"ok"`
	Service  string `json:"service"`
	Phase    string `json:"phase"`
	Entities int    `json:"entities"`
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

type LocaleSpellings struct {
	Locale    string   `json:"locale"`
	Canonical string   `json:"canonical,omitempty"`
	Aliases   []string `json:"aliases,omitempty"`
	Spellings []string `json:"spellings"`
}

type SearchOptions struct {
	Query  string
	Type   string
	Status string
	Limit  int
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
}

type MatchedEntity struct {
	KO            string   `json:"ko"`
	LocaleName    string   `json:"locale_name"`
	EntityType    string   `json:"entity_type"`
	SourceAliases []string `json:"source_aliases,omitempty"`
	TargetAliases []string `json:"target_aliases,omitempty"`
	Note          string   `json:"note,omitempty"`
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

type ObservationResponse struct {
	OK       bool `json:"ok"`
	Created  bool `json:"created"`
	Promoted bool `json:"promoted"`
}

type ResearchQueueRequest struct {
	EntityKO            string `json:"entity_ko"`
	RequestedEntityType string `json:"requested_entity_type,omitempty"`
	ContextHint         string `json:"context_hint,omitempty"`
	SourceID            string `json:"source_id,omitempty"`
}

type ResearchQueueResponse struct {
	OK     bool `json:"ok"`
	Queued bool `json:"queued"`
}

type SiteSearchRequest struct {
	Locale              string   `json:"locale"`
	Query               string   `json:"query,omitempty"`
	Domains             []string `json:"domains,omitempty"`
	LimitDomains        int      `json:"limit_domains,omitempty"`
	MaxResultsPerDomain int      `json:"max_results_per_domain,omitempty"`
	DryRun              bool     `json:"dry_run,omitempty"`
}

type SiteSearchResponse struct {
	EntityID        string             `json:"entity_id"`
	CanonicalKO     string             `json:"canonical_ko"`
	Locale          string             `json:"locale"`
	Queries         []string           `json:"queries"`
	DomainsSearched int                `json:"domains_searched"`
	ResultsFound    int                `json:"results_found"`
	Enqueued        int                `json:"enqueued"`
	Duplicates      int                `json:"duplicates"`
	Results         []SiteSearchResult `json:"results"`
}

type SiteSearchResult struct {
	Domain      string `json:"domain"`
	Query       string `json:"query"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	Enqueued    bool   `json:"enqueued"`
	AlreadySeen bool   `json:"already_seen"`
	Error       string `json:"error,omitempty"`
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

func New(baseURL, apiKey string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		APIKey:  strings.TrimSpace(apiKey),
		HTTPClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func NewFromEnv() *Client {
	c := New(os.Getenv("KDB_API_URL"), os.Getenv("KDB_API_KEY"))
	c.WorkspaceID = strings.TrimSpace(os.Getenv("KDB_WORKSPACE_ID"))
	return c
}

func (c *Client) Health(ctx context.Context) (*Health, error) {
	var out Health
	if err := c.get(ctx, "/v1/health", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) SearchEntities(ctx context.Context, opts SearchOptions) ([]Entity, error) {
	q := url.Values{}
	if opts.Query != "" {
		q.Set("q", opts.Query)
	}
	if opts.Type != "" {
		q.Set("type", opts.Type)
	}
	if opts.Status != "" {
		q.Set("status", opts.Status)
	}
	if opts.Limit > 0 {
		q.Set("limit", strconv.Itoa(opts.Limit))
	}
	var out struct {
		Entities []Entity `json:"entities"`
	}
	if err := c.get(ctx, "/v1/entities", q, &out); err != nil {
		return nil, err
	}
	return out.Entities, nil
}

func (c *Client) GetEntity(ctx context.Context, id string) (*Entity, error) {
	var out Entity
	if err := c.get(ctx, "/v1/entities/"+url.PathEscape(id), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) PatchEntity(ctx context.Context, id string, req PatchEntityRequest) (*Entity, error) {
	var out Entity
	if err := c.patchJSON(ctx, "/v1/entities/"+url.PathEscape(id), req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) LockEntity(ctx context.Context, id string, locked bool) (*Entity, error) {
	var out Entity
	if err := c.postJSON(ctx, "/v1/entities/"+url.PathEscape(id)+"/lock", LockEntityRequest{Locked: &locked}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) Spellings(ctx context.Context, id, locale string) ([]LocaleSpellings, error) {
	q := url.Values{}
	if locale != "" {
		q.Set("locale", locale)
	}
	var out struct {
		ID        string            `json:"id"`
		Spellings []LocaleSpellings `json:"spellings"`
	}
	if err := c.get(ctx, "/v1/entities/"+url.PathEscape(id)+"/spellings", q, &out); err != nil {
		return nil, err
	}
	return out.Spellings, nil
}

func (c *Client) CreateObservation(ctx context.Context, req ObservationRequest) (*ObservationResponse, error) {
	var out ObservationResponse
	if err := c.postJSON(ctx, "/v1/observations", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) EnqueueResearch(ctx context.Context, req ResearchQueueRequest) (*ResearchQueueResponse, error) {
	var out ResearchQueueResponse
	if err := c.postJSON(ctx, "/v1/research-queue", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) SiteSearch(ctx context.Context, id string, req SiteSearchRequest) (*SiteSearchResponse, error) {
	var out SiteSearchResponse
	if err := c.postJSON(ctx, "/v1/entities/"+url.PathEscape(id)+"/site-search", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ExternalRefs(ctx context.Context, id string) ([]ExternalRef, error) {
	var out struct {
		EntityID     string        `json:"entity_id"`
		ExternalRefs []ExternalRef `json:"external_refs"`
	}
	if err := c.get(ctx, "/v1/entities/"+url.PathEscape(id)+"/external-refs", nil, &out); err != nil {
		return nil, err
	}
	return out.ExternalRefs, nil
}

func (c *Client) Relations(ctx context.Context, id string) ([]Relation, error) {
	var out struct {
		EntityID  string     `json:"entity_id"`
		Relations []Relation `json:"relations"`
	}
	if err := c.get(ctx, "/v1/entities/"+url.PathEscape(id)+"/relations", nil, &out); err != nil {
		return nil, err
	}
	return out.Relations, nil
}

func (c *Client) PersonDetails(ctx context.Context, id string) (*PersonDetails, error) {
	var out PersonDetails
	if err := c.get(ctx, "/v1/persons/"+url.PathEscape(id), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) Lookup(ctx context.Context, req LookupRequest) (*LookupResponse, error) {
	var out LookupResponse
	if err := c.postJSON(ctx, "/v1/lookup", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) BulkLookup(ctx context.Context, req BulkLookupRequest) (*BulkLookupResponse, error) {
	var out BulkLookupResponse
	if err := c.postJSON(ctx, "/v1/lookup/bulk", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) MatchEntities(ctx context.Context, req MatchEntitiesRequest) ([]MatchedEntity, error) {
	var out struct {
		Entities []MatchedEntity `json:"entities"`
	}
	if err := c.postJSON(ctx, "/v1/entities/match", req, &out); err != nil {
		return nil, err
	}
	return out.Entities, nil
}

func (c *Client) patchJSON(ctx context.Context, path string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	u, err := c.url(path)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, u.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, out)
}

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	u, err := c.url(path)
	if err != nil {
		return err
	}
	if len(q) > 0 {
		u.RawQuery = q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	return c.do(req, out)
}

func (c *Client) postJSON(ctx context.Context, path string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	u, err := c.url(path)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, out)
}

func (c *Client) do(req *http.Request, out any) error {
	if c == nil || strings.TrimSpace(c.BaseURL) == "" {
		return fmt.Errorf("kdbclient: base URL required")
	}
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	if c.WorkspaceID != "" {
		req.Header.Set("X-Workspace-Id", c.WorkspaceID)
	}
	req.Header.Set("Accept", "application/json")

	hc := c.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return fmt.Errorf("kdbclient: %s", e.Error)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) url(path string) (*url.URL, error) {
	if c == nil || strings.TrimSpace(c.BaseURL) == "" {
		return nil, fmt.Errorf("kdbclient: base URL required")
	}
	base, err := url.Parse(strings.TrimRight(c.BaseURL, "/"))
	if err != nil {
		return nil, err
	}
	if base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("kdbclient: absolute base URL required")
	}
	endpoint := strings.TrimLeft(path, "/")
	base.Path = strings.TrimRight(base.Path, "/") + "/" + endpoint
	base.RawQuery = ""
	base.Fragment = ""
	return base, nil
}

package kdbclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSearchEntitiesSendsHeadersAndQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/entities" {
			t.Fatalf("path = %q, want /api/v1/entities", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := r.Header.Get("X-Workspace-Id"); got != "kstory" {
			t.Fatalf("X-Workspace-Id = %q", got)
		}
		if got := r.URL.Query().Get("q"); got != "박미선" {
			t.Fatalf("q = %q", got)
		}
		if got := r.URL.Query().Get("type"); got != "person" {
			t.Fatalf("type = %q", got)
		}
		if got := r.URL.Query().Get("status"); got != "all" {
			t.Fatalf("status = %q", got)
		}
		if got := r.URL.Query().Get("limit"); got != "3" {
			t.Fatalf("limit = %q", got)
		}
		writeTestJSON(t, w, map[string]any{
			"entities": []map[string]any{
				{
					"id":           "entity-1",
					"entity_type":  "person",
					"canonical_ko": "박미선",
					"canonical_ja": "パク・ミソン",
					"aliases": map[string]any{
						"ja": []string{"パクミソン"},
					},
					"confidence": 0.99,
					"status":     "active",
				},
			},
		})
	}))
	defer srv.Close()

	client := New(srv.URL+"/api", "test-key")
	client.WorkspaceID = "kstory"

	got, err := client.SearchEntities(context.Background(), SearchOptions{
		Query:  "박미선",
		Type:   "person",
		Status: "all",
		Limit:  3,
	})
	if err != nil {
		t.Fatalf("SearchEntities returned error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(entities) = %d, want 1", len(got))
	}
	if got[0].CanonicalKO != "박미선" || got[0].CanonicalJA != "パク・ミソン" {
		t.Fatalf("unexpected entity: %+v", got[0])
	}
	if len(got[0].Aliases.JA) != 1 || got[0].Aliases.JA[0] != "パクミソン" {
		t.Fatalf("unexpected aliases: %+v", got[0].Aliases)
	}
}

func TestBulkLookupPostsJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/lookup/bulk" {
			t.Fatalf("path = %q, want /v1/lookup/bulk", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Fatalf("method = %q, want POST", r.Method)
		}
		if got := r.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
			t.Fatalf("Content-Type = %q", got)
		}
		var req BulkLookupRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if len(req.Queries) != 2 || req.Queries[0] != "BTS" || req.Queries[1] != "아이브" {
			t.Fatalf("queries = %+v", req.Queries)
		}
		if req.Type != "group" || req.Limit != 5 {
			t.Fatalf("request = %+v", req)
		}
		writeTestJSON(t, w, map[string]any{
			"results": []map[string]any{
				{
					"query": "BTS",
					"matches": []map[string]any{
						{"id": "entity-1", "entity_type": "group", "canonical_ko": "방탄소년단"},
					},
				},
				{"query": "아이브", "matches": []map[string]any{}},
			},
		})
	}))
	defer srv.Close()

	client := New(srv.URL, "")
	got, err := client.BulkLookup(context.Background(), BulkLookupRequest{
		Queries: []string{"BTS", "아이브"},
		Type:    "group",
		Limit:   5,
	})
	if err != nil {
		t.Fatalf("BulkLookup returned error: %v", err)
	}
	if len(got.Results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(got.Results))
	}
	if got.Results[0].Query != "BTS" || got.Results[0].Matches[0].CanonicalKO != "방탄소년단" {
		t.Fatalf("unexpected response: %+v", got.Results[0])
	}
}

func TestSpellingsDecodesLocaleResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/entities/entity-1/spellings" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("locale"); got != "ja" {
			t.Fatalf("locale = %q", got)
		}
		writeTestJSON(t, w, map[string]any{
			"id": "entity-1",
			"spellings": []map[string]any{
				{
					"locale":    "ja",
					"canonical": "BTS",
					"aliases":   []string{"防弾少年団"},
					"spellings": []string{"BTS", "防弾少年団"},
				},
			},
		})
	}))
	defer srv.Close()

	client := New(srv.URL, "")
	got, err := client.Spellings(context.Background(), "entity-1", "ja")
	if err != nil {
		t.Fatalf("Spellings returned error: %v", err)
	}
	if len(got) != 1 || got[0].Locale != "ja" || len(got[0].Spellings) != 2 {
		t.Fatalf("unexpected spellings: %+v", got)
	}
}

func TestMatchEntitiesPostsSourceText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/entities/match" {
			t.Fatalf("path = %q, want /v1/entities/match", r.URL.Path)
		}
		var req MatchEntitiesRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.SourceText != "BTS가 출연했다" || req.Locale != "ja" || req.Limit != 10 {
			t.Fatalf("request = %+v", req)
		}
		writeTestJSON(t, w, map[string]any{
			"entities": []map[string]any{
				{
					"ko":             "방탄소년단",
					"locale_name":    "BTS",
					"entity_type":    "group",
					"source_aliases": []string{"BTS"},
					"target_aliases": []string{"防弾少年団"},
					"note":           "locked",
				},
			},
		})
	}))
	defer srv.Close()

	client := New(srv.URL, "")
	got, err := client.MatchEntities(context.Background(), MatchEntitiesRequest{
		SourceText: "BTS가 출연했다",
		Locale:     "ja",
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("MatchEntities returned error: %v", err)
	}
	if len(got) != 1 || got[0].KO != "방탄소년단" || got[0].TargetAliases[0] != "防弾少年団" {
		t.Fatalf("unexpected entities: %+v", got)
	}
}

func TestExternalRefs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/entities/entity-1/external-refs" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		writeTestJSON(t, w, map[string]any{
			"entity_id": "entity-1",
			"external_refs": []map[string]any{
				{
					"provider":    "wikidata",
					"external_id": "Q123",
					"url":         "https://www.wikidata.org/wiki/Q123",
					"confidence":  0.9,
					"description": "sample",
					"fetched_at":  "2026-05-26T00:00:00Z",
				},
			},
		})
	}))
	defer srv.Close()

	client := New(srv.URL, "")
	got, err := client.ExternalRefs(context.Background(), "entity-1")
	if err != nil {
		t.Fatalf("ExternalRefs returned error: %v", err)
	}
	if len(got) != 1 || got[0].Provider != "wikidata" || got[0].ExternalID != "Q123" {
		t.Fatalf("unexpected refs: %+v", got)
	}
}

func TestRelations(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/entities/entity-1/relations" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		writeTestJSON(t, w, map[string]any{
			"entity_id": "entity-1",
			"relations": []map[string]any{
				{
					"direction":     "outgoing",
					"relation_type": "member_of",
					"entity_id":     "entity-2",
					"entity_ko":     "방탄소년단",
					"entity_type":   "group",
					"confidence":    0.9,
					"source_urls":   []string{"https://example.com"},
				},
			},
		})
	}))
	defer srv.Close()

	client := New(srv.URL, "")
	got, err := client.Relations(context.Background(), "entity-1")
	if err != nil {
		t.Fatalf("Relations returned error: %v", err)
	}
	if len(got) != 1 || got[0].RelationType != "member_of" || got[0].EntityKO != "방탄소년단" {
		t.Fatalf("unexpected relations: %+v", got)
	}
}

func TestCreateObservation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/observations" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Fatalf("method = %q", r.Method)
		}
		var req ObservationRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.EntityID != "entity-1" || req.Locale != "ja" || req.Spelling != "BTS" || req.SourceDomain != "example.com" {
			t.Fatalf("request = %+v", req)
		}
		writeTestJSON(t, w, map[string]any{"ok": true, "created": true, "promoted": false})
	}))
	defer srv.Close()

	client := New(srv.URL, "")
	got, err := client.CreateObservation(context.Background(), ObservationRequest{
		EntityID:     "entity-1",
		Locale:       "ja",
		Spelling:     "BTS",
		SourceDomain: "example.com",
		Confidence:   0.9,
	})
	if err != nil {
		t.Fatalf("CreateObservation returned error: %v", err)
	}
	if !got.OK || !got.Created || got.Promoted {
		t.Fatalf("unexpected response: %+v", got)
	}
}

func TestEnqueueResearch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/research-queue" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		var req ResearchQueueRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.EntityKO != "뉴진스" || req.RequestedEntityType != "group" {
			t.Fatalf("request = %+v", req)
		}
		writeTestJSON(t, w, map[string]any{"ok": true, "queued": true})
	}))
	defer srv.Close()

	client := New(srv.URL, "")
	got, err := client.EnqueueResearch(context.Background(), ResearchQueueRequest{
		EntityKO:            "뉴진스",
		RequestedEntityType: "group",
		ContextHint:         "article mention",
	})
	if err != nil {
		t.Fatalf("EnqueueResearch returned error: %v", err)
	}
	if !got.Queued {
		t.Fatalf("unexpected response: %+v", got)
	}
}

func TestSiteSearch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/entities/entity-1/site-search" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Fatalf("method = %q", r.Method)
		}
		var req SiteSearchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Locale != "vi" || req.Query != "BTS" || !req.DryRun || req.LimitDomains != 2 || req.MaxResultsPerDomain != 1 {
			t.Fatalf("request = %+v", req)
		}
		if len(req.Domains) != 1 || req.Domains[0] != "example.com" {
			t.Fatalf("domains = %+v", req.Domains)
		}
		writeTestJSON(t, w, map[string]any{
			"entity_id":        "entity-1",
			"canonical_ko":     "방탄소년단",
			"locale":           "vi",
			"domains_searched": 2,
			"results_found":    1,
			"enqueued":         0,
			"duplicates":       0,
			"results": []map[string]any{
				{"domain": "example.com", "query": "BTS", "title": "BTS news", "url": "https://example.com/1"},
			},
		})
	}))
	defer srv.Close()

	client := New(srv.URL, "")
	got, err := client.SiteSearch(context.Background(), "entity-1", SiteSearchRequest{
		Locale:              "vi",
		Query:               "BTS",
		Domains:             []string{"example.com"},
		LimitDomains:        2,
		MaxResultsPerDomain: 1,
		DryRun:              true,
	})
	if err != nil {
		t.Fatalf("SiteSearch returned error: %v", err)
	}
	if got.CanonicalKO != "방탄소년단" || got.ResultsFound != 1 || len(got.Results) != 1 {
		t.Fatalf("unexpected response: %+v", got)
	}
}

func TestPatchAndLockEntity(t *testing.T) {
	var sawPatch, sawLock bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/entities/entity-1":
			sawPatch = true
			var req PatchEntityRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode patch request: %v", err)
			}
			if req.CanonicalJA == nil || *req.CanonicalJA != "BTS" {
				t.Fatalf("patch request = %+v", req)
			}
			writeTestJSON(t, w, map[string]any{
				"id":           "entity-1",
				"entity_type":  "group",
				"canonical_ko": "방탄소년단",
				"canonical_ja": "BTS",
				"aliases":      map[string]any{},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/entities/entity-1/lock":
			sawLock = true
			var req LockEntityRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode lock request: %v", err)
			}
			if req.Locked == nil || !*req.Locked {
				t.Fatalf("lock request = %+v", req)
			}
			writeTestJSON(t, w, map[string]any{
				"id":              "entity-1",
				"entity_type":     "group",
				"canonical_ko":    "방탄소년단",
				"operator_locked": true,
				"aliases":         map[string]any{},
			})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	client := New(srv.URL, "")
	ja := "BTS"
	if _, err := client.PatchEntity(context.Background(), "entity-1", PatchEntityRequest{CanonicalJA: &ja}); err != nil {
		t.Fatalf("PatchEntity returned error: %v", err)
	}
	if _, err := client.LockEntity(context.Background(), "entity-1", true); err != nil {
		t.Fatalf("LockEntity returned error: %v", err)
	}
	if !sawPatch || !sawLock {
		t.Fatalf("sawPatch=%v sawLock=%v", sawPatch, sawLock)
	}
}

func TestPersonDetails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/persons/entity-1" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		writeTestJSON(t, w, map[string]any{
			"entity_id":       "entity-1",
			"primary_role":    "idol",
			"secondary_roles": []string{"singer"},
			"groups":          []string{"BTS"},
			"agency":          "BigHit Music",
			"gender":          "M",
			"birth_year":      1995,
			"notable_works":   []string{"Dynamite"},
		})
	}))
	defer srv.Close()

	client := New(srv.URL, "")
	got, err := client.PersonDetails(context.Background(), "entity-1")
	if err != nil {
		t.Fatalf("PersonDetails returned error: %v", err)
	}
	if got.PrimaryRole != "idol" || got.BirthYear != 1995 || got.Groups[0] != "BTS" {
		t.Fatalf("unexpected person details: %+v", got)
	}
}

func TestErrorResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		writeTestJSON(t, w, map[string]any{"error": "unauthorized"})
	}))
	defer srv.Close()

	client := New(srv.URL, "")
	_, err := client.GetEntity(context.Background(), "entity-1")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("error = %q, want unauthorized", err.Error())
	}
}

func TestBaseURLRequired(t *testing.T) {
	client := New("", "")
	_, err := client.Health(context.Background())
	if err == nil {
		t.Fatal("expected base URL error")
	}
	if !strings.Contains(err.Error(), "base URL required") {
		t.Fatalf("error = %q", err.Error())
	}
}

func TestNewFromEnv(t *testing.T) {
	t.Setenv("KDB_API_URL", "http://kdb-api:9100")
	t.Setenv("KDB_API_KEY", "test-key")
	t.Setenv("KDB_WORKSPACE_ID", "kstory")
	t.Setenv("KDB_API_TIMEOUT_SECONDS", "7")

	client := NewFromEnv()
	if client.BaseURL != "http://kdb-api:9100" {
		t.Fatalf("BaseURL = %q", client.BaseURL)
	}
	if client.APIKey != "test-key" {
		t.Fatalf("APIKey = %q", client.APIKey)
	}
	if client.WorkspaceID != "kstory" {
		t.Fatalf("WorkspaceID = %q", client.WorkspaceID)
	}
	if client.HTTPClient.Timeout.String() != "7s" {
		t.Fatalf("Timeout = %s, want 7s", client.HTTPClient.Timeout)
	}
}

func TestNewFromEnvDefaultsToSameHostAPI(t *testing.T) {
	t.Setenv("KDB_API_URL", "")

	client := NewFromEnv()
	if client.BaseURL != DefaultAPIURL {
		t.Fatalf("BaseURL = %q, want %q", client.BaseURL, DefaultAPIURL)
	}
}

func writeTestJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

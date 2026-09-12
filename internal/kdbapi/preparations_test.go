package kdbapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/rickyjoo73/kdb/internal/kdb/readiness"
	"github.com/rickyjoo73/kdb/internal/testdb"
)

func TestPreparationHTTPAuthenticationIsolationAndCancellation(t *testing.T) {
	t.Setenv("KDB_READINESS_ENABLED", "1")
	pool := testdb.New(t)
	ctx := context.Background()
	b, err := os.ReadFile("../../migrations/0115_kentity_readiness.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(b)); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `CREATE TABLE kwave_kdb_api_consumers(id uuid PRIMARY KEY,key_hash text,active boolean,last_used_at timestamptz);
 INSERT INTO kwave_entities(canonical_ko,canonical_en,canonical_en_source) VALUES('시험인물','Test Person','operator')`); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"fixture-key-a", "fixture-key-b"} {
		if _, err = pool.Exec(ctx, `INSERT INTO kwave_kdb_api_consumers VALUES($1,$2,true,now())`, uuid.New(), hashConsumerKey(key)); err != nil {
			t.Fatal(err)
		}
	}
	h := NewRouterWithOptions(pool, RouterOptions{APIKeys: []string{"fixture-operator"}})
	call := func(method, path, key, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+key)
		r.Header.Set("Idempotency-Key", "article-v1")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	payload := `{"terms":[{"ko":"시험인물","type":"person"}],"locales":["en"]}`
	if w := call("POST", "/v1/preparations", "", payload); w.Code != 401 {
		t.Fatal(w.Code, w.Body.String())
	}
	w := call("POST", "/v1/preparations", "fixture-key-a", payload)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var p readiness.Preparation
	if err = json.Unmarshal(w.Body.Bytes(), &p); err != nil || p.Status != "ready" {
		t.Fatal(p, err)
	}
	path := "/v1/preparations/" + p.ID.String()
	for _, key := range []string{"fixture-key-b", "fixture-operator"} {
		if w = call("GET", path, key, ""); w.Code != 404 {
			t.Fatal("owner isolation", w.Code, w.Body.String())
		}
	}
	if w = call("POST", "/v1/preparations", "fixture-key-a", strings.Replace(payload, `"en"`, `"ja"`, 1)); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = call("POST", path+"/cancel", "fixture-key-a", fmt.Sprintf(`{"revision":%d,"reason":"   "}`, p.Revision)); w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = call("POST", path+"/cancel", "fixture-key-b", fmt.Sprintf(`{"revision":%d,"reason":"wrong owner"}`, p.Revision)); w.Code != 404 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = call("POST", path+"/cancel", "fixture-key-a", fmt.Sprintf(`{"revision":%d,"reason":"article cancelled"}`, p.Revision)); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if err = json.Unmarshal(w.Body.Bytes(), &p); err != nil || p.Status != "cancelled" {
		t.Fatal(p, err)
	}
	t.Setenv("KDB_READINESS_ENABLED", "0")
	if w = call("GET", path, "fixture-key-a", ""); w.Code != http.StatusServiceUnavailable {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestPreparationOpenModeNeverCreatesAnonymousOwner(t *testing.T) {
	h := &handler{}
	r := httptest.NewRequest("POST", "/v1/preparations", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	h.createPreparation(w, r)
	if w.Code != 401 {
		t.Fatal(w.Code, w.Body.String())
	}
}

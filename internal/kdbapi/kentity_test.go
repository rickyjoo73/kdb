package kdbapi

import (
	"context"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/rickyjoo73/kdb/internal/testdb"
)

func TestCommonCatalogHTTPAuthAndFeatureGate(t *testing.T) {
	t.Setenv("KDB_COMMON_ENTITY_ENABLED", "1")
	pool := testdb.New(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `ALTER TABLE kwave_persons ADD name_ko text; INSERT INTO kwave_entities(canonical_ko) VALUES('API 합성')`); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("../../migrations/0116_kentity_core.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(b)); err != nil {
		t.Fatal(err)
	}
	h := NewRouterWithOptions(pool, RouterOptions{APIKeys: []string{"fixture-operator"}})
	call := func(path, key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := call("/v1/kentity/entities", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := call("/v1/kentity/entities", "fixture-operator"); w.Code != 200 || !strings.Contains(w.Body.String(), `"catalog_only":true`) {
		t.Fatal(w.Code, w.Body.String())
	}
	var id string
	if err = pool.QueryRow(ctx, `SELECT id::text FROM kwave_entities LIMIT 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if w := call("/v1/kentity/entities/"+id, "fixture-operator"); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	t.Setenv("KDB_COMMON_ENTITY_ENABLED", "0")
	if w := call("/v1/kentity/entities", "fixture-operator"); w.Code != 503 {
		t.Fatal(w.Code)
	}
}

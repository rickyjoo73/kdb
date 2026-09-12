package kdbapi

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"github.com/rickyjoo73/kdb/internal/kdb/readiness"
	"github.com/rickyjoo73/kdb/internal/testdb"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestCommonPreparationHTTPGateExactLocaleAndRevalidation(t *testing.T) {
	t.Setenv("KDB_READINESS_ENABLED", "1")
	t.Setenv("KDB_COMMON_ENTITY_ENABLED", "1")
	pool := testdb.New(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `ALTER TABLE kwave_persons ADD name_ko text`); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"0115_kentity_readiness.sql", "0116_kentity_core.sql", "0117_kentity_resolution.sql", "0118_kentity_write_ownership.sql", "0119_kentity_common_readiness.sql"} {
		b, err := os.ReadFile("../../migrations/" + m)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, string(b)); err != nil {
			t.Fatal(m, err)
		}
	}
	id, evidence := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO kentity_entities(id,entity_type,canonical_ko,origin_system,status) VALUES($1,'organization','합성 공통 기관','native','candidate')`, id); err != nil {
		t.Fatal(err)
	}
	h := NewRouterWithOptions(pool, RouterOptions{APIKeys: []string{"fixture-operator", "another-operator"}})
	call := func(method, path, key, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+key)
		r.Header.Set("Idempotency-Key", "common-http")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	payload := fmt.Sprintf(`{"catalog":"common","terms":[{"ko":"합성 공통 기관","type":"organization","entity_id":"%s"}],"locales":["en","zh-Hans","pt-BR"]}`, id)
	if w := call("POST", "/v1/preparations", "fixture-operator", payload); w.Code != 503 {
		t.Fatal(w.Code, w.Body.String())
	}
	t.Setenv("KDB_COMMON_READINESS_ENABLED", "1")
	w := call("POST", "/v1/preparations", "fixture-operator", payload)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var p readiness.Preparation
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil || p.PolicyVersion != readiness.CommonPolicyVersion || p.Items[0].Locales[0].State != "policy_blocked" {
		t.Fatal(p, err)
	}
	path := "/v1/preparations/" + p.ID.String()
	if w = call("GET", path, "another-operator", ""); w.Code != 404 {
		t.Fatal("common owner isolation", w.Code)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO kentity_evidence(id,entity_id,provider,source_url,license_code,export_allowed,status,verified_by,verified_at) VALUES($1,$2,'fixture','https://example.test/identity','CC0-1.0',true,'verified','fixture',now())`, evidence, id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO kentity_names(entity_id,locale,value,kind,form,status,evidence_id,source_code) VALUES($1,'en','Common Organization','canonical','recorded','verified',$2,'wikidata-label')`, id, evidence); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE kentity_entities SET status='active',revision=revision+1 WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	w = call("GET", path, "fixture-operator", "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil || p.Items[0].Locales[0].State != "ready" {
		t.Fatal(p, err)
	}
	for _, l := range p.Items[0].Locales {
		if l.Locale != "en" && (l.State != "no_evidence" || l.ReadyAt != nil) {
			t.Fatal(l)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE kentity_evidence SET export_allowed=false WHERE id=$1`, evidence); err != nil {
		t.Fatal(err)
	}
	w = call("GET", path, "fixture-operator", "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil || p.Items[0].Locales[0].State != "policy_blocked" || p.Items[0].Locales[0].Value != "" {
		t.Fatal("cached evidence served", p, err)
	}
	t.Setenv("KDB_COMMON_READINESS_ENABLED", "0")
	if w = call("GET", path, "fixture-operator", ""); w.Code != 503 {
		t.Fatal("gate bypass", w.Code)
	}
}

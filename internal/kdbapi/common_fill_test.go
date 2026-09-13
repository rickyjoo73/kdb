package kdbapi

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"github.com/rickyjoo73/kdb/internal/kdb/readiness"
	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
	"github.com/rickyjoo73/kdb/internal/testdb"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

type commonHTTPSource struct{}

func (commonHTTPSource) Fetch(context.Context, string) (*wikidata.Entity, error) {
	return &wikidata.Entity{QID: "Q9000121", SourceLabels: map[string]string{"ko": "합성 공통 기관", "zh-hans": "共同机构", "pt": "Organização"}, InstanceOf: []string{"Q43229"}}, nil
}
func TestCommonFillHTTPIntakeGateAndExactSourceResult(t *testing.T) {
	t.Setenv("KDB_READINESS_ENABLED", "1")
	t.Setenv("KDB_COMMON_ENTITY_ENABLED", "1")
	t.Setenv("KDB_COMMON_READINESS_ENABLED", "1")
	t.Setenv("KDB_COMMON_FILL_ENABLED", "")
	pool := testdb.New(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `ALTER TABLE kwave_persons ADD name_ko text`); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"0115_kentity_readiness.sql", "0116_kentity_core.sql", "0117_kentity_resolution.sql", "0118_kentity_write_ownership.sql", "0119_kentity_common_readiness.sql", "0121_kentity_common_fill.sql"} {
		b, err := os.ReadFile("../../migrations/" + m)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, string(b)); err != nil {
			t.Fatal(err)
		}
	}
	id, ev := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO kentity_entities(id,entity_type,canonical_ko,origin_system,status) VALUES($1,'organization','합성 공통 기관','native','candidate')`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO kentity_evidence(id,entity_id,provider,source_record_id,source_url,claim_type,license_code,export_allowed,status,verified_by,verified_at) VALUES($1,$2,'wikidata','Q9000121','https://www.wikidata.org/wiki/Q9000121','identity','CC0-1.0',true,'verified','synthetic-review',now())`, ev, id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO kentity_external_ids(entity_id,provider,external_id,status,evidence_id,policy_version) VALUES($1,'wikidata','Q9000121','verified',$2,'kentity-writer-v1')`, id, ev); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO kentity_id_reservations(provider,external_id,entity_id) VALUES('wikidata','Q9000121',$1)`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE kentity_entities SET status='active' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	h := NewRouterWithOptions(pool, RouterOptions{APIKeys: []string{"fixture-operator"}})
	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer fixture-operator")
		r.Header.Set("Idempotency-Key", "common-fill-http")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := call("POST", "/v1/preparations", fmt.Sprintf(`{"catalog":"common","terms":[{"ko":"합성 공통 기관","type":"organization","entity_id":"%s"}],"locales":["zh-Hans","pt-BR"]}`, id))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var p readiness.Preparation
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	for _, l := range p.Items[0].Locales {
		if l.State != "no_evidence" {
			t.Fatal(l)
		}
	}
	t.Setenv("KDB_COMMON_FILL_ENABLED", "1")
	path := "/v1/preparations/" + p.ID.String()
	w = call("GET", path, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "common_auto_fill_queued") {
		t.Fatal(w.Code, w.Body.String())
	}
	worker := &readiness.Worker{Store: &readiness.Store{Pool: pool, CommonEnabled: true, CommonFillEnabled: true}, Source: commonHTTPSource{}}
	for i := 0; i < 2; i++ {
		if ok, err := worker.ProcessCommonOne(ctx); err != nil || !ok {
			t.Fatal(ok, err)
		}
	}
	w = call("GET", path, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	for _, l := range p.Items[0].Locales {
		if l.Locale == "zh-Hans" && (l.State != "ready" || !strings.Contains(string(l.Proof), "policy:common-anchored-fill-v1")) {
			t.Fatal(l)
		}
		if l.Locale == "pt-BR" && (l.State != "no_evidence" || l.Value != "") {
			t.Fatal(l)
		}
	}
}

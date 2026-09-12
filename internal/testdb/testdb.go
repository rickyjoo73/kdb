// Package testdb supplies isolated schemas for opt-in PostgreSQL integration
// tests. It refuses production database names and never uses DATABASE_URL.
package testdb

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func New(t testing.TB) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("KDB_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("KDB_TEST_DATABASE_URL not set (isolated PostgreSQL required)")
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConnConfig.Database != "kdb_workflow_test" {
		t.Fatal("integration tests require database kdb_workflow_test")
	}
	ctx := context.Background()
	admin, err := pgxpool.NewWithConfig(ctx, cfg.Copy())
	if err != nil {
		t.Fatal(err)
	}
	schema := "test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, err := admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
		if err != nil {
			t.Errorf("test schema cleanup: %v", err)
		}
	})
	ddl := `CREATE TABLE kwave_entities (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), canonical_ko text NOT NULL,
 entity_type text NOT NULL DEFAULT 'person', status text NOT NULL DEFAULT 'active',
 confidence numeric NOT NULL DEFAULT .7, operator_locked boolean NOT NULL DEFAULT false, verification_tier text,
 aliases_ko text[] NOT NULL DEFAULT '{}', disambig text, needs_disambig boolean NOT NULL DEFAULT false,
 disambig_reviewed_at timestamptz, notes text, fill_input_hash text NOT NULL DEFAULT 'input-v1',
 source_urls text[] DEFAULT '{}', verification_evidence text, verified_tier_at timestamptz,
 updated_at timestamptz NOT NULL DEFAULT now(), created_at timestamptz NOT NULL DEFAULT now());
 CREATE TABLE kwave_entity_person_details (
 entity_id uuid PRIMARY KEY REFERENCES kwave_entities(id), primary_role text, agency text,
 birth_year int, notable_works text[], groups text[], secondary_roles text[], gender text);
 CREATE TABLE kwave_entity_external_refs (
 entity_id uuid REFERENCES kwave_entities(id), provider text, external_id text,
 url text, confidence numeric, raw_payload jsonb DEFAULT '{}', fetched_at timestamptz DEFAULT now(),
 PRIMARY KEY(entity_id,provider));
 CREATE TABLE kwave_kdb_evidence_refs (
 entity_id uuid REFERENCES kwave_entities(id), lane text, url text, title text, provider text, snippet text,
 UNIQUE(entity_id,url));
 CREATE TABLE kwave_kdb_enrich_attempts (
 entity_id uuid REFERENCES kwave_entities(id), field text, input_hash text NOT NULL DEFAULT '',
 attempts int NOT NULL DEFAULT 0, exhausted boolean NOT NULL DEFAULT false,
 last_attempt_at timestamptz, last_source text, last_reason text, PRIMARY KEY(entity_id,field));
 CREATE TABLE kwave_kdb_dataqa_log (
 entity_id uuid, locale text, old_value text, verdict text, reverted_at timestamptz);
 CREATE TABLE kwave_entity_research_queue (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), entity_ko text, requested_entity_type text DEFAULT 'person',
 status text DEFAULT 'done', precheck_status text DEFAULT 'pass',
 resolution_status text, locale_status text, created_at timestamptz DEFAULT now(), finished_at timestamptz);
 CREATE TABLE kwave_persons (id uuid PRIMARY KEY DEFAULT gen_random_uuid());
 CREATE TABLE kwave_kdb_corrections (id uuid PRIMARY KEY DEFAULT gen_random_uuid(), status text);`
	for _, loc := range []string{"en", "ja", "vi", "id", "es", "pt_br", "zh", "zh_hant"} {
		ddl += "ALTER TABLE kwave_entities ADD canonical_" + loc + " text, ADD canonical_" + loc + "_source text;"
	}
	if _, err := pool.Exec(ctx, ddl); err != nil {
		t.Fatal(err)
	}
	return pool
}

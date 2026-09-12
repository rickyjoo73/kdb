package testdb

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Restored opens only an explicitly named disposable copy, never the original
// restoration or a production DB. Tests own and remove only their UUID fixtures.
func Restored(t testing.TB) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("KDB_MIGRATION_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("KDB_MIGRATION_TEST_DATABASE_URL not set")
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConnConfig.Database != "kdb_platform_migration_test" || cfg.ConnConfig.Host != "127.0.0.1" {
		t.Fatal("requires isolated localhost kdb_platform_migration_test")
	}
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "15000"
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var n int
	if err = pool.QueryRow(context.Background(), `SELECT count(*) FROM pg_trigger WHERE tgname='trg_kdb_fill_hash_refs' AND NOT tgisinternal`).Scan(&n); err != nil || n != 1 {
		t.Fatal("restored production trigger missing", err)
	}
	return pool
}

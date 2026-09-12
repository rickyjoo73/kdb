package kdbadmin

import (
	"context"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Opt-in, read-only validation of complete admin queries against an isolated
// restored snapshot. Never accepts the production DB name or DATABASE_URL.
func TestAdminAgainstRestoredSnapshot(t *testing.T) {
	url := os.Getenv("KDB_RESTORE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("KDB_RESTORE_TEST_DATABASE_URL not set")
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConnConfig.Database != "kdb_workflow_restore_test" {
		t.Fatal("only isolated kdb_workflow_restore_test is allowed")
	}
	cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	s := renderSmokeServer(t)
	s.pool = pool
	for _, path := range []string{"/admin/", "/admin/workflow"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", path, nil).WithContext(ctx)
		start := time.Now()
		if path == "/admin/" {
			s.dashboard(w, r)
		} else {
			s.workflowBoard(w, r)
		}
		elapsed := time.Since(start)
		body := w.Body.String()
		if w.Code != 200 || !strings.Contains(body, "</html>") || strings.Contains(body, "집계 실패") {
			t.Fatalf("%s did not fully render the restored snapshot", path)
		}
		if elapsed > 6*time.Second {
			t.Fatalf("%s exceeded 6s snapshot gate: %v", path, elapsed)
		}
		t.Logf("%s restored snapshot rendered in %v", path, elapsed)
	}
}

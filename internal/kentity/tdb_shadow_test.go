package kentity

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func shadowFixture(t *testing.T) (*Store, TDBBindingBatch) {
	t.Helper()
	s, _ := fixture(t)
	b, err := os.ReadFile("../../migrations/0120_kentity_tdb_shadow.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(context.Background(), string(b)); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile("../../migrations/0122_kentity_tdb_crosswalk.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(context.Background(), string(b)); err != nil {
		t.Fatal(err)
	}
	return s, TDBBindingBatch{Policy: TDBShadowPolicy, Source: "wikidata", License: "CC0", State: "live", Enabled: true, ObservedAt: time.Now().UTC(), Bindings: []TDBBinding{{ID: uuid.New(), QID: "Q123", Method: "synthetic-ID-claim", Score: 0.9}}}
}
func shadowCount(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	if err := s.Pool.QueryRow(context.Background(), `SELECT count(*) FROM `+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func TestTDBShadowDryRunIdempotenceAndNoMasterWrites(t *testing.T) {
	s, b := shadowFixture(t)
	ctx := context.Background()
	r, err := s.ImportTDBBindings(ctx, "fixture", b, false)
	if err != nil || !r.DryRun || r.Created != 1 || shadowCount(t, s, "kentity_tdb_shadows") != 0 {
		t.Fatal(r, err)
	}
	r, err = s.ImportTDBBindings(ctx, "fixture", b, true)
	if err != nil || r.DryRun || r.Created != 1 {
		t.Fatal(r, err)
	}
	src := researchSource()
	old := uuid.New()
	if _, err = s.Pool.Exec(ctx, `INSERT INTO kwave_entities(id,canonical_ko) VALUES($1,'다른 원장 ID')`, old); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, `INSERT INTO kwave_entity_external_refs(entity_id,provider,external_id) VALUES($1,'wikidata','Q123')`, old); err != nil {
		t.Fatal(err)
	}
	roots, names, links := shadowCount(t, s, "kentity_entities"), shadowCount(t, s, "kentity_names"), shadowCount(t, s, "kentity_crosswalks")
	if ok, err := (&TDBShadowWorker{Store: s, Source: src}).ProcessOne(ctx); err != nil || !ok {
		t.Fatal(ok, err)
	}
	rows, err := s.TDBShadows(ctx, "review", 10)
	if err != nil || len(rows) != 1 || rows[0].Proposal == nil || len(rows[0].Proposal.ExistingIDs) != 1 {
		t.Fatal(rows, err)
	}
	seen := map[string]string{}
	for _, n := range rows[0].Proposal.Names {
		seen[n.Locale] = n.Value
		if n.Status != "unverified" {
			t.Fatal(n)
		}
	}
	if seen["pt"] != "Test Person" || seen["pt-BR"] != "" || seen["zh-Hans"] != "" || seen["en"] != "Test Person (politician)" {
		t.Fatal(seen)
	}
	r, err = s.ImportTDBBindings(ctx, "fixture", b, true)
	if err != nil || r.Unchanged != 1 {
		t.Fatal(r, err)
	}
	again, err := s.TDBShadows(ctx, "review", 10)
	if err != nil || len(again) != 1 || again[0].Attempts != 1 || again[0].Generation != rows[0].Generation {
		t.Fatal(again, err)
	}
	if shadowCount(t, s, "kentity_entities") != roots || shadowCount(t, s, "kentity_names") != names || shadowCount(t, s, "kentity_crosswalks") != links {
		t.Fatal("shadow wrote master or crosswalk")
	}
	if shadowCount(t, s, "kentity_tdb_shadow_events") != 2 {
		t.Fatal("missing or duplicate audit")
	}
}
func TestTDBShadowRejectsUnlicensedGeneratedStaleOrMalformedBindings(t *testing.T) {
	for _, scenario := range []string{"aihub", "generated", "blocked", "disabled", "old", "future", "qid", "score", "duplicate", "empty"} {
		t.Run(scenario, func(t *testing.T) {
			s, b := shadowFixture(t)
			switch scenario {
			case "aihub":
				b.Source = "aihub"
			case "generated":
				b.Generated = true
			case "blocked":
				b.State = "blocked"
			case "disabled":
				b.Enabled = false
			case "old":
				b.ObservedAt = time.Now().Add(-25 * time.Hour)
			case "future":
				b.ObservedAt = time.Now().Add(time.Hour)
			case "qid":
				b.Bindings[0].QID = "Q0"
			case "score":
				b.Bindings[0].Score = 2
			case "duplicate":
				b.Bindings = append(b.Bindings, b.Bindings[0])
			case "empty":
				b.Bindings = nil
			}
			if _, err := s.ImportTDBBindings(context.Background(), "fixture", b, true); !errors.Is(err, ErrInvalid) {
				t.Fatal(err)
			}
			if shadowCount(t, s, "kentity_tdb_shadows") != 0 {
				t.Fatal("invalid source accepted")
			}
		})
	}
}
func TestTDBShadowChangedBindingFencesInFlightResult(t *testing.T) {
	s, b := shadowFixture(t)
	ctx := context.Background()
	if _, err := s.ImportTDBBindings(ctx, "fixture", b, true); err != nil {
		t.Fatal(err)
	}
	src := researchSource()
	src.FetchHook = func() {
		b.Bindings[0].Locked = true
		if _, err := s.ImportTDBBindings(ctx, "fixture", b, true); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := (&TDBShadowWorker{Store: s, Source: src}).ProcessOne(ctx); !errors.Is(err, ErrProtected) {
		t.Fatal(err)
	}
	rows, err := s.TDBShadows(ctx, "blocked", 10)
	if err != nil || len(rows) != 1 || rows[0].Proposal != nil || rows[0].Attempts != 0 {
		t.Fatal(rows, err)
	}
	if ok, err := (&TDBShadowWorker{Store: s, Source: src}).ProcessOne(ctx); err != nil || ok {
		t.Fatal("locked binding claimed", ok, err)
	}
}

func TestTDBShadowStaleSnapshotCannotClearNewerLock(t *testing.T) {
	s, b := shadowFixture(t)
	ctx := context.Background()
	old := b
	old.ObservedAt = time.Now().Add(-time.Minute)
	b.Bindings[0].Locked = true
	if _, err := s.ImportTDBBindings(ctx, "fixture", b, true); err != nil {
		t.Fatal(err)
	}
	old.Bindings = append([]TDBBinding(nil), b.Bindings...)
	old.Bindings[0].Locked = false
	if _, err := s.ImportTDBBindings(ctx, "fixture", old, true); !errors.Is(err, ErrProtected) {
		t.Fatal(err)
	}
	rows, err := s.TDBShadows(ctx, "blocked", 10)
	if err != nil || len(rows) != 1 || !rows[0].Locked {
		t.Fatal(rows, err)
	}
}

func TestTDBShadowConcurrentWorkersClaimOnce(t *testing.T) {
	s, b := shadowFixture(t)
	ctx := context.Background()
	if _, err := s.ImportTDBBindings(ctx, "fixture", b, true); err != nil {
		t.Fatal(err)
	}
	var fetched atomic.Int64
	src := researchSource()
	src.FetchHook = func() { fetched.Add(1) }
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := (&TDBShadowWorker{Store: s, Source: src}).ProcessOne(ctx)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if fetched.Load() != 1 {
		t.Fatal("duplicate fetch", fetched.Load())
	}
	rows, err := s.TDBShadows(ctx, "review", 10)
	if err != nil || len(rows) != 1 || rows[0].Attempts != 1 {
		t.Fatal(rows, err)
	}
}
func TestTDBShadowRetriesBudgetAndLeaseRecovery(t *testing.T) {
	s, b := shadowFixture(t)
	ctx := context.Background()
	if _, err := s.ImportTDBBindings(ctx, "fixture", b, true); err != nil {
		t.Fatal(err)
	}
	w := &TDBShadowWorker{Store: s, Source: fakeResearch{Err: errors.New("synthetic failure")}}
	for i := 1; i <= 4; i++ {
		if ok, err := w.ProcessOne(ctx); err != nil || !ok {
			t.Fatal(i, ok, err)
		}
		var attempts int
		var retry *time.Time
		if err := s.Pool.QueryRow(ctx, `SELECT attempts,next_attempt_at FROM kentity_tdb_shadows`).Scan(&attempts, &retry); err != nil || attempts != i || (retry != nil) != (i < 4) {
			t.Fatal(attempts, retry, err)
		}
		if i < 4 {
			if _, err := s.Pool.Exec(ctx, `UPDATE kentity_tdb_shadows SET next_attempt_at=now()`); err != nil {
				t.Fatal(err)
			}
		}
	}
	if ok, err := w.ProcessOne(ctx); err != nil || ok {
		t.Fatal(ok, err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE kentity_tdb_shadows SET state='running',lease_until=now()-interval '1 minute',lease_token=gen_random_uuid()`); err != nil {
		t.Fatal(err)
	}
	if ok, err := w.ProcessOne(ctx); err != nil || ok {
		t.Fatal(ok, err)
	}
	rows, err := s.TDBShadows(ctx, "failed", 10)
	if err != nil || len(rows) != 1 || rows[0].Reason != "lease_expired_budget" {
		t.Fatal(rows, err)
	}
}

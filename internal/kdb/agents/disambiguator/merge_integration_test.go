package disambiguator

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyjoo73/kdb/internal/kdb/agents"
	"github.com/rickyjoo73/kdb/internal/testdb"
)

func mergeFixture(t *testing.T) (*pgxpool.Pool, member, member) {
	t.Helper()
	pool := testdb.New(t)
	ctx := context.Background()
	l, w := uuid.New(), uuid.New()
	for _, row := range []struct {
		id   uuid.UUID
		name string
	}{{l, "병합 시험 별칭"}, {w, "병합 시험 정식명"}} {
		if _, err := pool.Exec(ctx, `INSERT INTO kwave_entities(id,canonical_ko) VALUES($1,$2)`, row.id, row.name); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO kwave_entity_external_refs(entity_id,provider,external_id) VALUES($1,'wikidata','Q123456')`, row.id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE kwave_entities SET source_urls=ARRAY['https://evidence.example/fixture'],
 canonical_en='Fixture name', canonical_en_source='wikidata', verification_tier='evidenced', verification_evidence='fixture' WHERE id=$1`, l); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO kwave_kdb_evidence_refs(entity_id,lane,url,title) VALUES($1,'fixture','https://evidence.example/fixture','fixture')`, l); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO kwave_entity_person_details(entity_id,primary_role,birth_year) VALUES($1,'athlete',1990)`, l); err != nil {
		t.Fatal(err)
	}
	ms, err := readMembers(ctx, pool, []uuid.UUID{l, w})
	if err != nil || len(ms) != 2 {
		t.Fatalf("fixture read: %v %v", ms, err)
	}
	byID := map[uuid.UUID]member{}
	for _, m := range withWellFormed(ms) {
		byID[m.id] = m
	}
	return pool, byID[l], byID[w]
}

func mergeResult(ctx context.Context, pool *pgxpool.Pool, l, w member) agents.ItemResult {
	wid := w.id.String()
	return (&Agent{}).applyMerge(ctx, pool, l, memberResult{ID: l.id.String(), Decision: "merge", SameAs: &wid, Confidence: .95, Reason: "same persisted identity"}, map[string]member{wid: w, l.id.String(): l})
}

func assertNotMerged(t *testing.T, pool *pgxpool.Pool, l, w member) {
	t.Helper()
	var status, value string
	var aliases, urls, refs int
	err := pool.QueryRow(context.Background(), `SELECT l.status, COALESCE(w.canonical_en,''), cardinality(w.aliases_ko), cardinality(w.source_urls),
 (SELECT count(*) FROM kwave_kdb_evidence_refs WHERE entity_id=w.id) FROM kwave_entities l, kwave_entities w WHERE l.id=$1 AND w.id=$2`, l.id, w.id).Scan(&status, &value, &aliases, &urls, &refs)
	if err != nil {
		t.Fatal(err)
	}
	if status != "active" || value != "" || aliases != 0 || urls != 0 || refs != 0 {
		t.Fatalf("partial merge: status=%s value=%s aliases=%d urls=%d refs=%d", status, value, aliases, urls, refs)
	}
}

func TestMergeTransactionCommitsEvidenceAndRetirement(t *testing.T) {
	pool, l, w := mergeFixture(t)
	if got := mergeResult(context.Background(), pool, l, w); got.Action != agents.ActionMerged {
		t.Fatal(got)
	}
	var status, value, source, tier, notes string
	var refs, details int
	if err := pool.QueryRow(context.Background(), `SELECT l.status, w.canonical_en, w.canonical_en_source, w.verification_tier, l.notes,
 (SELECT count(*) FROM kwave_kdb_evidence_refs WHERE entity_id=w.id), (SELECT count(*) FROM kwave_entity_person_details WHERE entity_id=w.id)
 FROM kwave_entities l, kwave_entities w WHERE l.id=$1 AND w.id=$2`, l.id, w.id).Scan(&status, &value, &source, &tier, &notes, &refs, &details); err != nil {
		t.Fatal(err)
	}
	if status != "rejected" || value != "Fixture name" || source != "wikidata" || tier != "evidenced" || refs != 1 || details != 1 || !strings.Contains(notes, w.id.String()) {
		t.Fatalf("incomplete merge: %s %s %s %s %d %d %s", status, value, source, tier, refs, details, notes)
	}
	if got := mergeResult(context.Background(), pool, l, w); got.Action != agents.ActionSkipped {
		t.Fatalf("redelivery: %+v", got)
	}
}

func TestMergeTransactionRollsBackLateFailure(t *testing.T) {
	pool, l, w := mergeFixture(t)
	if _, err := pool.Exec(context.Background(), `ALTER TABLE kwave_entities ADD CONSTRAINT fixture_refuse_retirement CHECK(status<>'rejected')`); err != nil {
		t.Fatal(err)
	}
	if got := mergeResult(context.Background(), pool, l, w); got.Action != agents.ActionErrored {
		t.Fatalf("failed write reported success: %+v", got)
	}
	assertNotMerged(t, pool, l, w)
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM kwave_entity_person_details WHERE entity_id=$1`, l.id).Scan(&n); err != nil || n != 1 {
		t.Fatalf("detail moved after rollback: %d %v", n, err)
	}
}

func TestMergeTransactionGuards(t *testing.T) {
	for _, tc := range []struct {
		name, sql string
		winner    bool
		want      agents.Action
	}{
		{"locked winner", `UPDATE kwave_entities SET operator_locked=true WHERE id=$1`, true, agents.ActionSkipped},
		{"locked loser", `UPDATE kwave_entities SET operator_locked=true WHERE id=$1`, false, agents.ActionSkipped},
		{"stale birth year", `UPDATE kwave_entity_person_details SET birth_year=1980 WHERE entity_id=$1`, false, agents.ActionSkipped},
		{"stale QID", `UPDATE kwave_entity_external_refs SET external_id='Q987654' WHERE entity_id=$1`, true, agents.ActionSkipped},
		{"stale type", `UPDATE kwave_entities SET entity_type='group' WHERE id=$1`, true, agents.ActionSkipped},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool, l, w := mergeFixture(t)
			id := l.id
			if tc.winner {
				id = w.id
			}
			if _, err := pool.Exec(context.Background(), tc.sql, id); err != nil {
				t.Fatal(err)
			}
			if got := mergeResult(context.Background(), pool, l, w); got.Action != tc.want {
				t.Fatal(got)
			}
			assertNotMerged(t, pool, l, w)
		})
	}
}

func TestMergeRequiresIdentityNotNameOrRole(t *testing.T) {
	pool, l, w := mergeFixture(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `DELETE FROM kwave_entity_external_refs`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE kwave_entities SET canonical_ko='동명 선수'`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO kwave_entity_person_details(entity_id,primary_role,birth_year) VALUES($1,'athlete',1990)`, w.id); err != nil {
		t.Fatal(err)
	}
	ms, err := readMembers(ctx, pool, []uuid.UUID{l.id, w.id})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range withWellFormed(ms) {
		if m.id == l.id {
			l = m
		} else {
			w = m
		}
	}
	if got := mergeResult(ctx, pool, l, w); got.Action != agents.ActionQuarantined {
		t.Fatal(got)
	}
	assertNotMerged(t, pool, l, w)
}

func TestMergeConflictingExternalProviderIDs(t *testing.T) {
	pool, l, w := mergeFixture(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO kwave_entity_external_refs(entity_id,provider,external_id) VALUES($1,'imdb','nm111'),($2,'imdb','nm222')`, l.id, w.id); err != nil {
		t.Fatal(err)
	}
	ms, err := readMembers(ctx, pool, []uuid.UUID{l.id, w.id})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range withWellFormed(ms) {
		if m.id == l.id {
			l = m
		} else {
			w = m
		}
	}
	if got := mergeResult(ctx, pool, l, w); got.Action != agents.ActionQuarantined {
		t.Fatal(got)
	}
	assertNotMerged(t, pool, l, w)
}

func TestMergeCannotRetireActiveEntityIntoCandidate(t *testing.T) {
	pool, l, w := mergeFixture(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `UPDATE kwave_entities SET status='candidate' WHERE id=$1`, w.id); err != nil {
		t.Fatal(err)
	}
	ms, err := readMembers(ctx, pool, []uuid.UUID{l.id, w.id})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range withWellFormed(ms) {
		if m.id == l.id {
			l = m
		} else {
			w = m
		}
	}
	if got := mergeResult(ctx, pool, l, w); got.Action != agents.ActionQuarantined {
		t.Fatal(got)
	}
	assertNotMerged(t, pool, l, w)
}

func TestMergeConcurrentReverseRequests(t *testing.T) {
	pool, l, w := mergeFixture(t)
	start := make(chan struct{})
	results := make(chan agents.ItemResult, 2)
	var wg sync.WaitGroup
	for _, pair := range [][2]member{{l, w}, {w, l}} {
		wg.Add(1)
		go func(p [2]member) {
			defer wg.Done()
			<-start
			results <- mergeResult(context.Background(), pool, p[0], p[1])
		}(pair)
	}
	close(start)
	wg.Wait()
	close(results)
	merged := 0
	for r := range results {
		if r.Action == agents.ActionMerged {
			merged++
		} else if r.Action != agents.ActionSkipped && r.Action != agents.ActionErrored {
			t.Fatal(r)
		}
	}
	var active, retired int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FILTER(WHERE status='active'),count(*) FILTER(WHERE status='rejected') FROM kwave_entities`).Scan(&active, &retired); err != nil {
		t.Fatal(err)
	}
	if merged != 1 || active != 1 || retired != 1 {
		t.Fatalf("reverse merge cycle: merged=%d active=%d retired=%d", merged, active, retired)
	}
}

func TestMergeWaitCancellationReleasesLocks(t *testing.T) {
	pool, l, w := mergeFixture(t)
	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackMerge(tx)
	if _, err := tx.Exec(context.Background(), `SELECT id FROM kwave_entities WHERE id=$1 FOR UPDATE`, w.id); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if got := mergeResult(ctx, pool, l, w); got.Action != agents.ActionErrored {
		t.Fatal(got)
	}
	rollbackMerge(tx)
	assertNotMerged(t, pool, l, w)
	if got := mergeResult(context.Background(), pool, l, w); got.Action != agents.ActionMerged {
		t.Fatal(got)
	}
}

func TestInvalidMergePlansNeverPartiallyApply(t *testing.T) {
	for _, mode := range []string{"duplicate", "cycle", "chain", "foreign", "missing survivor verdict"} {
		t.Run(mode, func(t *testing.T) {
			pool, l, w := mergeFixture(t)
			lid, wid := l.id.String(), w.id.String()
			asgs := []memberResult{{ID: lid, Decision: "merge", SameAs: &wid, Confidence: .99}, {ID: wid, Decision: "distinct"}}
			switch mode {
			case "duplicate":
				asgs = append(asgs, asgs[0])
			case "cycle":
				asgs[1].Decision = "merge"
				asgs[1].SameAs = &lid
			case "chain":
				asgs[1].Decision = "merge"
				foreign := uuid.NewString()
				asgs[1].SameAs = &foreign
			case "foreign":
				asgs[0].ID = uuid.NewString()
			case "missing survivor verdict":
				asgs = asgs[:1]
			}
			b, err := json.Marshal(disambigResult{Assignments: asgs})
			if err != nil {
				t.Fatal(err)
			}
			res := newTestAgent(string(b), nil).processCluster(context.Background(), pool, cluster{name: l.ko, members: []member{l, w}})
			for _, r := range res {
				if r.Action != agents.ActionQuarantined {
					t.Fatal(r)
				}
			}
			assertNotMerged(t, pool, l, w)
		})
	}
}

func TestQuarantineIsIdempotentAndHonest(t *testing.T) {
	pool, l, _ := mergeFixture(t)
	a := &Agent{}
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if got := a.quarantine(ctx, pool, l.id, "fixture reason"); got.Action != agents.ActionQuarantined {
			t.Fatal(got)
		}
	}
	var notes string
	if err := pool.QueryRow(ctx, `SELECT notes FROM kwave_entities WHERE id=$1`, l.id).Scan(&notes); err != nil {
		t.Fatal(err)
	}
	if strings.Count(notes, "fixture reason") != 1 {
		t.Fatal(notes)
	}
	if _, err := pool.Exec(ctx, `UPDATE kwave_entities SET status='rejected' WHERE id=$1`, l.id); err != nil {
		t.Fatal(err)
	}
	if got := a.quarantine(ctx, pool, l.id, "do not resurrect"); got.Action != agents.ActionSkipped {
		t.Fatal(got)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if got := a.quarantine(cancelled, pool, l.id, "failed write"); got.Action != agents.ActionErrored {
		t.Fatal(got)
	}
}

func TestValidMergePlanSurvivorLabelDoesNotInvalidateSnapshot(t *testing.T) {
	pool, l, w := mergeFixture(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `CREATE FUNCTION fixture_hash_on_label() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN IF NEW.disambig IS DISTINCT FROM OLD.disambig THEN NEW.fill_input_hash='label-changed'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER fixture_hash_on_label BEFORE UPDATE ON kwave_entities FOR EACH ROW EXECUTE FUNCTION fixture_hash_on_label()`); err != nil {
		t.Fatal(err)
	}
	lid, wid, label := l.id.String(), w.id.String(), "(시험 선수)"
	b, err := json.Marshal(disambigResult{Assignments: []memberResult{
		{ID: wid, Decision: "distinct", Disambig: &label},
		{ID: lid, Decision: "merge", SameAs: &wid, Confidence: .99},
	}})
	if err != nil {
		t.Fatal(err)
	}
	res := newTestAgent(string(b), nil).processCluster(ctx, pool, cluster{name: l.ko, members: []member{l, w}})
	if len(res) != 2 || res[0].Action != agents.ActionMerged || res[1].Action != agents.ActionSplit {
		t.Fatal(res)
	}
}

func TestRepairEvidenceUsesIdentityAndTransaction(t *testing.T) {
	for _, anchored := range []bool{true, false} {
		t.Run(map[bool]string{true: "anchored", false: "name only"}[anchored], func(t *testing.T) {
			pool, l, w := mergeFixture(t)
			ctx := context.Background()
			if _, err := pool.Exec(ctx, `UPDATE kwave_entities SET status='rejected' WHERE id=$1`, l.id); err != nil {
				t.Fatal(err)
			}
			if !anchored {
				if _, err := pool.Exec(ctx, `DELETE FROM kwave_entity_external_refs`); err != nil {
					t.Fatal(err)
				}
			}
			err := repairEvidenceAtomically(ctx, pool, mergePair{loserID: l.id, winnerID: w.id})
			if anchored && err != nil {
				t.Fatal(err)
			}
			if !anchored && err == nil {
				t.Fatal("name-only repair accepted")
			}
			var value string
			if err := pool.QueryRow(ctx, `SELECT COALESCE(canonical_en,'') FROM kwave_entities WHERE id=$1`, w.id).Scan(&value); err != nil {
				t.Fatal(err)
			}
			if (value != "") != anchored {
				t.Fatalf("unexpected repair value %q", value)
			}
		})
	}
}

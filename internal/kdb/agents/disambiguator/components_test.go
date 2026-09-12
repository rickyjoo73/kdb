package disambiguator

import (
	"context"
	"github.com/google/uuid"
	"github.com/rickyjoo73/kdb/internal/kdb/agents"
	"github.com/rickyjoo73/kdb/internal/testdb"
	"testing"
)

func TestConnectedClustersNeverRepeatAnOverlappingMember(t *testing.T) {
	a, b, c, outside := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	ms := []member{{id: a, ko: "갑"}, {id: b, ko: "을"}, {id: c, ko: "병"}}
	groups := connectedClusters(ms, [][2]uuid.UUID{{a, b}, {b, c}, {a, outside}})
	if len(groups) != 1 || len(groups[0].members) != 3 {
		t.Fatal(groups)
	}
	seen := map[uuid.UUID]bool{}
	for _, g := range groups {
		for _, m := range g.members {
			if seen[m.id] {
				t.Fatal("duplicate member")
			}
			seen[m.id] = true
		}
	}
}

func TestSelectedAliasPairStillHasPartnerAtRunTime(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	a, b := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO kwave_entities(id,canonical_ko,status,aliases_ko) VALUES($1,'시험약칭','candidate','{}'),($2,'전혀다른공식명칭','active',ARRAY['시험약칭'])`, a, b); err != nil {
		t.Fatal(err)
	}
	agent := &Agent{}
	ids, err := agent.Select(ctx, pool, 30)
	if err != nil || len(ids) != 2 {
		t.Fatal(ids, err)
	}
	groups, err := agent.clustersFromIDs(ctx, pool, ids)
	if err != nil || len(groups) != 1 || len(groups[0].members) != 2 {
		t.Fatal("Select/Run mismatch", groups, err)
	}
}

func TestRunDatabaseFailureIsNotHealthyNoop(t *testing.T) {
	pool := testdb.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rep, err := (&Agent{}).Run(ctx, pool, agents.RunInput{IDs: []uuid.UUID{uuid.New()}})
	if err == nil || rep.SelfCheck.Pass || len(rep.Results) != 1 || rep.Results[0].Action != agents.ActionErrored {
		t.Fatal(rep, err)
	}
}

package autopilot

import (
	"context"
	"testing"

	"github.com/google/uuid"
	kdbroot "github.com/rickyjoo73/kdb/internal/kdb"
	"github.com/rickyjoo73/kdb/internal/kdb/agents"
	"github.com/rickyjoo73/kdb/internal/testdb"
)

func TestAliasCleanupAndProducerConverge(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	low, high := uuid.New(), uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO kwave_entities(id,canonical_ko,confidence,aliases_ko) VALUES
 ($1,'작품A',.7,ARRAY['공통 표기']),($2,'작품B',.95,ARRAY['공통 표기'])`, low, high)
	if err != nil {
		t.Fatal(err)
	}
	s := New(pool)
	a := &stepAgent{role: agents.RoleStepResolveAliasConflicts, setWide: true, runStep: s.stepResolveAliasConflicts}
	in := agents.RunInput{IDs: []uuid.UUID{uuid.New()}}
	rep, err := a.Run(ctx, pool, in)
	if err != nil || rep.Results[0].Action != agents.ActionFilled {
		t.Fatalf("cleanup mutation hidden as noop: %+v %v", rep, err)
	}
	allowed, err := kdbroot.FilterKoreanAliasAdds(ctx, pool, low, []string{"공통 표기"})
	if err != nil || len(allowed) != 0 {
		t.Fatalf("producer would reinstall removed alias: %v %v", allowed, err)
	}
	rep, err = a.Run(ctx, pool, in)
	if err != nil || rep.Results[0].Action != agents.ActionNoop {
		t.Fatalf("second cleanup should be a noop: %+v %v", rep, err)
	}
}

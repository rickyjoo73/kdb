package personextractor

import (
	"context"
	"github.com/google/uuid"
	"github.com/rickyjoo73/kdb/internal/kdb/agents"
	"github.com/rickyjoo73/kdb/internal/testdb"
	"testing"
)

func TestCommonModeRequiresIdentityMappingBeforeLegacyCopy(t *testing.T) {
	t.Setenv("KDB_COMMON_ENTITY_ENABLED", "1")
	pool := testdb.New(t)
	ctx := context.Background()
	a := &Agent{}
	ids, err := a.Select(ctx, pool, 30)
	if err != nil || len(ids) != 0 {
		t.Fatal(ids, err)
	}
	id := uuid.New()
	if r := a.seedRole(ctx, pool, id, "동명"); r.Action != agents.ActionSkipped {
		t.Fatal(r)
	}
	if r := a.promote(ctx, pool, id, "동명", legacyPerson{role: "actor"}, "same name"); r.Action != agents.ActionSkipped {
		t.Fatal(r)
	}
	if r := a.reconcile(ctx, pool, id, "동명"); r.Action != agents.ActionSkipped {
		t.Fatal(r)
	}
}

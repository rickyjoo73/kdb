package autopilot

import (
	"context"
	"github.com/rickyjoo73/kdb/internal/testdb"
	"testing"
)

func TestCommonModeDoesNotCopyProfilesOrTypesByName(t *testing.T) {
	t.Setenv("KDB_COMMON_ENTITY_ENABLED", "1")
	pool := testdb.New(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `CREATE TYPE person_role AS ENUM('other','actor');
 ALTER TABLE kwave_persons ADD name_ko text,ADD primary_role text;
 INSERT INTO kwave_persons(name_ko,primary_role) VALUES('동명','actor');
 INSERT INTO kwave_entities(canonical_ko,entity_type) VALUES('동명','person'),('동명','person'),('동명','group');`); err != nil {
		t.Fatal(err)
	}
	rep := &Report{}
	(&Sweeper{Pool: pool}).stepSyncPersons(ctx, rep)
	var details, wrong, groups, persons int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM kwave_entity_person_details),(SELECT count(*) FROM kwave_entity_person_details WHERE primary_role<>'other'),(SELECT count(*) FROM kwave_entities WHERE entity_type='group'),(SELECT count(*) FROM kwave_persons)`).Scan(&details, &wrong, &groups, &persons); err != nil {
		t.Fatal(err)
	}
	if details != 2 || wrong != 0 || groups != 1 || persons != 1 || rep.EntityTypeFixed != 0 {
		t.Fatal(details, wrong, groups, persons, rep)
	}
}

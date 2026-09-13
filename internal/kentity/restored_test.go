package kentity

import (
	"context"
	"github.com/google/uuid"
	"github.com/rickyjoo73/kdb/internal/testdb"
	"testing"
)

func TestCommonCatalogAgainstRestoredSchema(t *testing.T) {
	pool := testdb.Restored(t)
	ctx := context.Background()
	s := &Store{Pool: pool}
	// 막아야 하는 것은 **이름 일치로 자동 확정하는 것**이다(I04). legacy_person 연결은
	// 0116 이 이름만 보고 만든 후보이므로 확정이 하나도 없어야 한다.
	// TDB 연결이 확정된 것은 정상이다 — 원본 자기 ID 관측을 근거로 확정한 것이라
	// 이름 일치와 무관하다. 종전 단언은 "확정이 하나도 없다"였는데, 그건 흡수 전의
	// 사실이지 규칙이 아니었다.
	var oldCount, newCount, nameConfirmed int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM kwave_entities),
 (SELECT count(*) FROM kentity_entities c JOIN kwave_entities e ON e.id=c.id WHERE c.origin_system='kdb'),
 (SELECT count(*) FROM kentity_crosswalks WHERE source_system='legacy_person' AND status='confirmed')`).
		Scan(&oldCount, &newCount, &nameConfirmed); err != nil {
		t.Fatal(err)
	}
	if oldCount != newCount {
		t.Fatal("legacy 투영이 어긋났다", oldCount, newCount)
	}
	if nameConfirmed != 0 {
		t.Fatal("이름 일치 후보가 자동 확정됐다", nameConfirmed)
	}
	es, err := s.Search(ctx, "", "person", "", 5)
	if err != nil || len(es) == 0 {
		t.Fatal(es, err)
	}
	if _, err = s.Get(ctx, es[0].ID); err != nil {
		t.Fatal(err)
	}
	mappings, err := s.Mappings(ctx, "review", 5)
	if err != nil || len(mappings) == 0 {
		t.Fatal("restored mapping listing", err)
	}
	for _, m := range mappings {
		if _, err = s.Mapping(ctx, m.SourceID); err != nil {
			t.Fatal("restored mapping detail", err)
		}
	}
	key := uuid.NewString()
	p, err := s.CreateCandidate(ctx, "restore-fixture", key, CandidateInput{KO: "공통구조격리검증", Type: "person", Domains: []string{"politics", "sports"}, Reason: "격리 DB 합성 검사"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, table := range []string{"kentity_candidate_requests", "kentity_audit_events", "kentity_entity_domains", "kentity_names"} {
			if _, err := pool.Exec(ctx, "DELETE FROM "+table+" WHERE entity_id=$1", p.ID); err != nil {
				t.Error(err)
			}
		}
		if _, err := pool.Exec(ctx, `DELETE FROM kentity_entities WHERE id=$1`, p.ID); err != nil {
			t.Error(err)
		}
	})
	if p.Status != "candidate" || p.Origin != "native" || len(p.Domains) != 2 {
		t.Fatal(p)
	}
	t.Logf("legacy UUID projection %d/%d; no name-only confirmed mappings", newCount, oldCount)
}

// 사전과 코드가 따로 놀면 "열었는데 등록이 안 되는" 일이 생긴다. 실제로 brand 가 그랬다 —
// kentity_types.enabled 를 켰는데 Go 의 supportedTypes 에 없어서 상표를 등록할 수 없었다.
func TestSupportedTypesMatchDictionary(t *testing.T) {
	pool := testdb.Restored(t)
	rows, err := pool.Query(context.Background(),
		`SELECT code FROM kentity_types WHERE enabled ORDER BY code`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	db := map[string]bool{}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		db[c] = true
	}
	for c := range db {
		if !supportedTypes[c] {
			t.Errorf("사전에서 열린 유형 %q 를 코드가 거부한다 — 등록할 수 없다", c)
		}
	}
	for c := range supportedTypes {
		if !db[c] {
			t.Errorf("코드가 받는 유형 %q 가 사전에 없거나 닫혀 있다", c)
		}
	}
}

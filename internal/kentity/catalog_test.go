package kentity

import (
	"context"
	"errors"
	"testing"
)

func TestCatalogInventoryFiltersAndRegistrationOrder(t *testing.T) {
	s, legacyID := fixture(t)
	ctx := context.Background()
	politician, err := s.CreateCandidate(ctx, "operator", "politics", CandidateInput{KO: "동명 후보", Type: "person", Domains: []string{"politics", "society"}, Reason: "검수 대기"})
	if err != nil {
		t.Fatal(err)
	}
	athlete, err := s.CreateCandidate(ctx, "operator", "sports", CandidateInput{KO: "동명 후보", Type: "person", Domains: []string{"sports"}, Reason: "별도 대상"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE kentity_entities SET created_at=now()-interval '2 days',updated_at=now() WHERE id=$1`, politician.ID); err != nil {
		t.Fatal(err)
	}
	p, err := s.Catalog(ctx, CatalogFilter{}, 50)
	if err != nil || p.Total != 3 || p.Overview.Total != 3 || p.Overview.Unassigned != 1 || p.Overview.Candidates != 2 || p.Overview.New24h != 2 || len(p.Overview.Domains) != 8 {
		t.Fatal(p, err)
	}
	var politics, society int64
	for _, d := range p.Overview.Domains {
		if d.Code == "politics" {
			politics = d.Total
		}
		if d.Code == "society" {
			society = d.Total
		}
	}
	if politics != 1 || society != 1 {
		t.Fatal("multi-domain inventory lost", p)
	}
	checks := []struct {
		f     CatalogFilter
		count int64
	}{
		{CatalogFilter{Q: "동명", Type: "person", Domain: "politics", Status: "candidate", Origin: "native"}, 1},
		{CatalogFilter{Domain: "unassigned"}, 1},
		{CatalogFilter{Domain: "entertainment"}, 0},
		{CatalogFilter{Domain: "sports", Period: "24h"}, 1},
		{CatalogFilter{Domain: "politics", Period: "24h"}, 0},
		{CatalogFilter{Status: "active", Origin: "native"}, 0},
	}
	for _, c := range checks {
		p, err = s.Catalog(ctx, c.f, 50)
		if err != nil || p.Total != c.count || int64(len(p.Items)) != c.count {
			t.Fatal(c, p, err)
		}
		if c.f.Domain == "unassigned" && p.Items[0].ID != legacyID {
			t.Fatal("legacy origin inferred as entertainment")
		}
	}
	p, err = s.Catalog(ctx, CatalogFilter{Origin: "native"}, 1)
	if err != nil || len(p.Items) != 1 || p.Items[0].ID != athlete.ID || p.Total != 2 {
		t.Fatal("new registration buried by old identity update", p, err)
	}
	p, err = s.Catalog(ctx, CatalogFilter{Origin: "native", Offset: 1}, 1)
	if err != nil || len(p.Items) != 1 || p.Items[0].ID != politician.ID {
		t.Fatal("pagination", p, err)
	}
	p, err = s.Catalog(ctx, CatalogFilter{Q: "' OR true --"}, 50)
	if err != nil || p.Total != 0 {
		t.Fatal("query was not a literal", p, err)
	}
}

func TestCatalogRejectsInvalidFiltersAndDoesNotConcealErrors(t *testing.T) {
	s, _ := fixture(t)
	for _, f := range []CatalogFilter{{Domain: "fake"}, {Sort: "created; SELECT 1"}, {Status: "verified"}, {Origin: "all"}, {Period: "today"}, {Offset: -1}, {Offset: 100001}} {
		if _, err := s.Catalog(context.Background(), f, 50); !errors.Is(err, ErrInvalid) {
			t.Fatal(f, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Catalog(ctx, CatalogFilter{}, 50); err == nil {
		t.Fatal("failure became empty inventory")
	}
}

// 전량 흡수 뒤에는 "유형 미상"이 십만 단위로 쌓인다. 목록에서 그걸 골라낼 수 없으면
// 검수를 시작할 수가 없다 — 필터 값이 실제로 조건을 거는지 확인한다.
func TestCatalogClassifyFilterIsValidated(t *testing.T) {
	for _, v := range []string{"", "pending", "unknown_type", "no_subtype", "needs_disambig"} {
		if !(CatalogFilter{Classify: v}).Valid() {
			t.Fatal("허용돼야 하는 값:", v)
		}
	}
	for _, v := range []string{"pendin", "unknown", "정체불명", "'; DROP TABLE"} {
		if (CatalogFilter{Classify: v}).Valid() {
			t.Fatal("거부돼야 하는 값:", v)
		}
	}
}

package kdbadmin

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/rickyjoo73/kdb/internal/testdb"
)

func TestWorkflowSupplyUsesLiveRetryPolicy(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	blocked, fresh, excluded := uuid.New(), uuid.New(), uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO kwave_entities(id,canonical_ko,entity_type) VALUES($1,'정책보류','person'),($2,'새근거','person'),($3,'유형제외','term');`, blocked, fresh, excluded)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO kwave_kdb_enrich_attempts(entity_id,field,input_hash,last_attempt_at,last_source) VALUES
 ($1,'canonical_ja','input-v1',now(),'ground-strict-skip'),($2,'canonical_ja','old-input',now(),'no-match')`, blocked, fresh)
	if err != nil {
		t.Fatal(err)
	}
	check := func(wantBlocked, wantEligible int64) {
		t.Helper()
		rows, err := loadWorkflowSupply(ctx, pool)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			if r.Locale == "ja" {
				if r.Missing != 3 || r.Blocked != wantBlocked || r.Eligible != wantEligible || r.Excluded != 1 {
					t.Fatalf("incorrect inventory: %+v", r)
				}
				return
			}
		}
		t.Fatal("missing ja inventory")
	}
	check(1, 1)
	if _, err = pool.Exec(ctx, `UPDATE kwave_entities SET fill_input_hash='new-evidence' WHERE id=$1`, blocked); err != nil {
		t.Fatal(err)
	}
	check(0, 2) // New evidence reopens only the affected entity; no blanket reset.
	if _, err = pool.Exec(ctx, `UPDATE kwave_kdb_enrich_attempts SET input_hash='new-evidence',last_attempt_at=now()-interval '91 days' WHERE entity_id=$1`, blocked); err != nil {
		t.Fatal(err)
	}
	check(0, 2) // Safety revisit remains available when external sources change.
}

func TestWorkflowReadinessDoesNotEquateDoneWithReady(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	_, err := pool.Exec(ctx, `INSERT INTO kwave_entity_research_queue(entity_ko,precheck_status) VALUES
 ('없는이름','pass'),('보류','review'),('후보','pass'),('미완성','pass'),('완성','pass'),('동명','pass'),('구분검토','pass');
 INSERT INTO kwave_entities(canonical_ko,status,needs_disambig) VALUES
 ('후보','candidate',false),('미완성','active',false),('완성','active',false),
 ('동명','active',false),('동명','active',false),('구분검토','active',true);
 UPDATE kwave_entities SET canonical_en='EN',canonical_ja='JA',canonical_vi='VI',canonical_id='ID',
 canonical_es='ES',canonical_pt_br='PT',canonical_zh='ZH',canonical_zh_hant='ZHH'
 WHERE canonical_ko IN ('완성','동명','구분검토');`)
	if err != nil {
		t.Fatal(err)
	}
	r, err := loadWorkflowReadiness(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if r.Requests != 7 || r.GateStopped != 1 || r.Missing != 1 || r.Candidate != 1 || r.ActiveGaps != 1 || r.FormsPresent != 1 || r.Ambiguous != 2 {
		t.Fatalf("wrong states: %+v", r)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := loadWorkflowReadiness(cancelled, pool); err == nil {
		t.Fatal("query failure must not become a healthy zero")
	}
}

func TestWorkflowInventoryFailureIsVisible(t *testing.T) {
	s := renderSmokeServer(t)
	var out bytes.Buffer
	err := s.tmpl.ExecuteTemplate(&out, "workflow_board.html", map[string]any{"title": "워크플로우", "inventoryError": true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "현황 집계 실패") || strings.Contains(out.String(), "전 구간 정상 흐름") {
		t.Fatal("inventory failure must be explicit")
	}
}

func TestWorkflowNameInventoryDeduplicatesAliasesNotIdentities(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	_, err := pool.Exec(ctx, `
INSERT INTO kwave_entities(canonical_ko,aliases_ko,status) VALUES
 ('한대상',ARRAY['한대상','반복별칭','반복별칭'],'active'),
 ('다른대상1',ARRAY['동명별칭'],'active'),('다른대상2',ARRAY['동명별칭'],'active'),
 ('퇴역대상',ARRAY['퇴역별칭'],'rejected');
INSERT INTO kwave_entity_research_queue(entity_ko) VALUES
 ('한대상'),('반복별칭'),('동명별칭'),('퇴역별칭'),(NULL);`)
	if err != nil {
		t.Fatal(err)
	}
	r, err := loadWorkflowReadiness(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if r.Requests != 5 || r.ActiveGaps != 2 || r.Ambiguous != 1 || r.Missing != 2 {
		t.Fatalf("alias duplication must not create or collapse identities: %+v", r)
	}
	var missing int
	err = pool.QueryRow(ctx, "SELECT count(*) FROM kwave_entity_research_queue q WHERE "+workflowGateNotServed).Scan(&missing)
	if err != nil {
		t.Fatal(err)
	}
	if missing != 2 {
		t.Fatalf("presence check must ignore rejected and preserve NULL misses: %d", missing)
	}
}

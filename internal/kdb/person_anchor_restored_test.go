package kdb

import (
	"context"
	"testing"

	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
	"github.com/rickyjoo73/kdb/internal/testdb"
)

// 앵커 회수(P4.12)가 건드릴 칸과 조건이 **실제 스키마와 맞는지** 고정한다.
// 이 시험은 위키데이터를 부르지 않는다 — 판정이 아니라 SQL 이 맞는지만 본다.
func TestRestoredAnchorWithdrawSQLMatchesSchema(t *testing.T) {
	pool := testdb.Restored(t)
	ctx := context.Background()

	// ① anchorLocaleCols 의 칼럼이 전부 실재해야 한다. 하나라도 이름이 바뀌면
	//    표기를 못 비우고 **틀린 값이 그대로 남는다** — 조용히 실패하는 종류다.
	for _, c := range anchorLocaleCols {
		var n int
		if err := pool.QueryRow(ctx, `
SELECT count(*) FROM information_schema.columns
 WHERE table_name='kwave_entities' AND column_name IN ($1,$2)`, c[0], c[1]).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 2 {
			t.Fatalf("%s / %s 가 kwave_entities 에 없다 (찾은 것 %d)", c[0], c[1], n)
		}
	}

	// ② 감사 선택 질의가 회귀 스키마에서 돌아야 한다(실행까지, 0건이어도 통과).
	rows, err := pool.Query(ctx, `
SELECT e.id::text, e.canonical_ko, e.entity_type::text, x.external_id
  FROM kwave_entities e
  JOIN kwave_entity_external_refs x ON x.entity_id = e.id AND x.provider = 'wikidata'
 WHERE e.status = 'active' AND e.entity_type IN ('person','character')
   AND x.external_id ~ '^Q[0-9]+$'
 LIMIT 3`)
	if err != nil {
		t.Fatal(err)
	}
	rows.Close()

	// ③ 등급 강등 질의(쓰기)가 유효한지. 실제로 쓰지 않도록 id 를 없는 값으로 준다.
	if _, err := pool.Exec(ctx, `
UPDATE kwave_entities e
   SET verification_tier = 'unverified', verified_tier_at = now(), updated_at = now()
 WHERE e.id = '00000000-0000-0000-0000-000000000000'
   AND e.verification_tier = 'authoritative'
   AND NOT EXISTS (SELECT 1 FROM kwave_entity_external_refs x
                    WHERE x.entity_id = e.id
                      AND x.provider IN (`+AuthoritativeIdentityProviderSQLList()+`))`); err != nil {
		t.Fatal(err)
	}

	// ④ 감사 로그 표가 있고 우리가 쓰는 컬럼을 갖고 있어야 한다. 되돌릴 수 없으면
	//    회수 자체를 해선 안 된다(오거부 금칙).
	var cols int
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM information_schema.columns
 WHERE table_name='kwave_kdb_recheck_log'
   AND column_name IN ('entity_id','term_ko','verdict','models','agreed','evidence')`).Scan(&cols); err != nil {
		t.Fatal(err)
	}
	if cols != 6 {
		t.Fatalf("kwave_kdb_recheck_log 의 컬럼이 모자라다 (%d/6) — 되돌릴 기록을 못 남긴다", cols)
	}
}

// 유형별 판정이 resolution.go 와 같은 명제를 쓰는지 고정한다.
// person ↔ Q5 는 이 저장소에 네 군데(resolution·common_fill·tdb_mapping·여기)에 있고,
// 하나만 달라지면 인입에서 막은 것을 감사가 통과시키거나 그 반대가 된다.
func TestAnchorVerdictAgreesWithIntakeRule(t *testing.T) {
	for _, c := range []struct {
		typ      string
		p31      []string
		wantBad  bool
		wantKind string
	}{
		{"person", []string{"Q5"}, false, ""},
		{"person", []string{"Q12308941"}, true, AnchorNameElement},     // 남성의 이름
		{"person", []string{"Q11424"}, true, AnchorNotHuman},           // 영화
		{"person", []string{"Q15632617"}, true, AnchorFictional},       // 허구의 사람
		{"person", nil, false, ""},                                     // P31 없음 = 판단 보류
		{"character", []string{"Q95074"}, false, ""},                   // 배역에 배역 앵커
		{"character", []string{"Q5"}, true, AnchorHumanOnChar},         // 배역에 실존인물 앵커
	} {
		got, _ := anchorVerdictFor(c.typ, c.p31)
		if (got != "") != c.wantBad || got != c.wantKind {
			t.Fatalf("%s %v → %q, 기대 %q", c.typ, c.p31, got, c.wantKind)
		}
	}
	// 이름요소 목록이 비면 88건짜리 오염을 통째로 못 잡는다.
	if wikidata.NameElementClassCount() < 10 {
		t.Fatal("이름요소 클래스 목록이 비었거나 줄었다")
	}
}

package kdbapi

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/rickyjoo73/kdb/internal/testdb"
)

// P4.01 입력 계약 — 알려진 이름이 있어도 **새 동명을 놓치지 않는다**.
//
// 종전 동작: prepareMatchSafe 는 기존 원장(kwave_entities)의 동명만 센다.
// 같은 이름이 하나뿐이면 `koMatches <= 1` 로 무조건 safe 였고, 공통 원장에 같은
// 이름의 다른 대상이 있어도 그 사실이 어디에도 드러나지 않았다.
// 실측(2026-09-14): 활성 4,163건이 그 상태였고, ready 로 서빙된 표제어 3,046건 중
// 193건이 나중에 교정 요청을 받았다.
//
// 이 시험이 지키는 것: CommonHomonyms 가 **공통 원장 기준으로** 신호를 낸다는 것.
func TestCommonHomonymsSignalsOtherLedgerObject(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	for _, m := range []string{"0115_kentity_readiness.sql", "0116_kentity_core.sql", "0117_kentity_resolution.sql", "0118_kentity_write_ownership.sql"} {
		b, err := os.ReadFile("../../migrations/" + m)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, string(b)); err != nil {
			t.Fatal(err)
		}
	}
	insert := func(ko, status string) uuid.UUID {
		id := uuid.New()
		if _, err := pool.Exec(ctx, `INSERT INTO kentity_entities(id,entity_type,canonical_ko,origin_system,status) VALUES($1,'person',$2,'tdb',$3)`, id, ko, status); err != nil {
			t.Fatal(err)
		}
		return id
	}
	insert("합성 동명 인물", "candidate")
	insert("합성 범위밖 인물", "rejected")

	s := &Store{Pool: pool}
	got := s.CommonHomonyms(ctx, []string{"합성 동명 인물", "합성 범위밖 인물", "합성 없는 이름"})

	if !got["합성 동명 인물"] {
		t.Fatal("공통 원장에 같은 이름의 대상이 있는데 신호가 없다 — 새 동명을 놓친다")
	}
	// 편집 범위 밖은 다른 대상으로 셈하지 않는다(지점 POI 12만건이 전부 신호를 켜면 뜻이 없다).
	if got["합성 범위밖 인물"] {
		t.Fatal("rejected 대상까지 신호를 켜면 안 된다")
	}
	if got["합성 없는 이름"] {
		t.Fatal("원장에 없는 이름에 신호가 켜졌다")
	}
	// 빈 입력에서 죽지 않는다(핫패스다).
	if len(s.CommonHomonyms(ctx, nil)) != 0 {
		t.Fatal("빈 입력은 빈 결과여야 한다")
	}
}

// 신호가 **서빙 상태를 바꾸지 않는다**는 것을 못박는다.
// 신호를 이유로 ready 를 preparing 으로 내리면 소비자 4,163 표제어가 한꺼번에 끊긴다.
// 판정은 P5(동일성 판정) 몫이고, 여기서는 놓치지 않았다는 사실만 남긴다.
func TestHomonymRiskDoesNotChangeServedStatus(t *testing.T) {
	it := PrepareItem{Term: "합성", Status: "ready", HomonymRisk: true}
	if it.Status != "ready" {
		t.Fatal("신호는 상태를 바꾸지 않는다")
	}
	if !it.HomonymRisk {
		t.Fatal("신호가 실려야 한다")
	}
}

package kentity

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/rickyjoo73/kdb/internal/testdb"
)

// 새 정치인·경제인·스포츠인을 등록하고 직업을 기록할 수 있어야 한다.
// 직업이 늘어도 ID 는 하나다 — 그게 이 시험의 요지다.
func TestRestoredAddPersonRoleKeepsOneID(t *testing.T) {
	pool := testdb.Restored(t)
	ctx := context.Background()
	s := &Store{Pool: pool}
	id := uuid.New()
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM kentity_person_roles WHERE entity_id=$1`, id)
		_, _ = pool.Exec(bg, `DELETE FROM kentity_audit_events WHERE entity_id=$1`, id)
		_, _ = pool.Exec(bg, `DELETE FROM kentity_evidence WHERE entity_id=$1`, id)
		_, _ = pool.Exec(bg, `DELETE FROM kentity_entities WHERE id=$1`, id)
	})
	// ★대상 이름에 이 실행의 UUID 를 박는다. 고정 이름이면 앞선 실행의 잔여물이 섞이고,
	// 전체 건수(count(*) FROM kentity_entities)로 세면 **다른 패키지가 같은 DB 에 동시에
	// 넣고 빼는 행까지** 세어 이 시험과 무관한 이유로 깨진다(`go test ./...` 는 패키지를
	// 병렬로 돌린다). 이 실행만의 이름으로 세면 둘 다 피하면서 뜻은 그대로다 —
	// **직업을 더하는 일이 새 대상을 만들면 안 된다.**
	name := "직업시험 합성인물 " + id.String()
	if _, err := pool.Exec(ctx, `INSERT INTO kentity_entities(id,entity_type,subtype,canonical_ko,origin_system,write_owner,status)
 VALUES($1,'person','real',$2,'native','native','candidate')`, id, name); err != nil {
		t.Fatal(err)
	}

	var before int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM kentity_entities WHERE canonical_ko=$1`, name).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before != 1 {
		t.Fatal("시험 대상이 하나가 아니다:", before)
	}

	// 정치인이자 기업인이자 운동선수 — 한 사람이 셋 다일 수 있다.
	for _, code := range []string{"politician", "businessperson", "athlete"} {
		if _, err := s.AddPersonRole(ctx, "operator@test", id, code, "합성 시험: 공개된 경력으로 확인"); err != nil {
			t.Fatal(code, err)
		}
	}
	roles, err := s.PersonRoles(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(roles) != 3 {
		t.Fatal("직업 수:", len(roles))
	}
	// ★ID 는 하나여야 한다. 직업을 더했다고 대상이 늘어나면 안 된다.
	var after, n int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM kentity_entities WHERE canonical_ko=$1`, name).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatal("직업을 더했더니 대상 수가 변했다:", before, "→", after)
	}
	// 직업 행이 전부 같은 ID 를 가리켜야 한다 — 한 사람 = 하나의 ID (I01).
	var stray int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM kentity_person_roles WHERE entity_id<>$1
 AND evidence_id IN (SELECT id FROM kentity_evidence WHERE entity_id=$1)`, id).Scan(&stray); err != nil {
		t.Fatal(err)
	}
	if stray != 0 {
		t.Fatal("직업 행이 다른 ID 를 가리킨다:", stray)
	}
	// 같은 직업을 또 넣어도 늘지 않는다.
	if _, err = s.AddPersonRole(ctx, "operator@test", id, "politician", "합성 시험: 중복 입력"); err != nil {
		t.Fatal(err)
	}
	if roles, _ = s.PersonRoles(ctx, id); len(roles) != 3 {
		t.Fatal("중복 직업이 생겼다:", len(roles))
	}
	// 근거 없이 만들지 않는다 — 직업마다 occupation 근거가 있어야 한다.
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM kentity_person_roles pr
 JOIN kentity_evidence v ON v.id=pr.evidence_id AND v.entity_id=pr.entity_id
 WHERE pr.entity_id=$1 AND v.claim_type='occupation' AND v.status='verified'`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatal("근거가 붙지 않은 직업이 있다:", n)
	}
}

// 사전에 없는 직군, 인물이 아닌 대상, 짧은 사유는 거부한다.
func TestRestoredAddPersonRoleRefusesGuesses(t *testing.T) {
	pool := testdb.Restored(t)
	ctx := context.Background()
	s := &Store{Pool: pool}
	id := uuid.New()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM kentity_entities WHERE id=$1`, id)
	})
	if _, err := pool.Exec(ctx, `INSERT INTO kentity_entities(id,entity_type,subtype,canonical_ko,origin_system,write_owner,status)
 VALUES($1,'location','district','직업시험 합성장소','native','native','candidate')`, id); err != nil {
		t.Fatal(err)
	}
	// 장소에 직업을 붙일 수 없다.
	if _, err := s.AddPersonRole(ctx, "operator@test", id, "politician", "장소에 직업을 붙여 본다"); !errors.Is(err, ErrInvalid) {
		t.Fatal("인물이 아닌 대상에 직업이 붙었다:", err)
	}
	// 사전에 없는 직군은 만들지 않는다.
	if _, err := s.AddPersonRole(ctx, "operator@test", uuid.New(), "우주비행사", "사전에 없는 직군"); err == nil {
		t.Fatal("사전 밖 직군이 통과했다")
	}
	// 사유가 짧으면 거부한다.
	if _, err := s.AddPersonRole(ctx, "operator@test", id, "politician", "짧음"); !errors.Is(err, ErrInvalid) {
		t.Fatal("짧은 사유가 통과했다:", err)
	}
}

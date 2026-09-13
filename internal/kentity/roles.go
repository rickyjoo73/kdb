package kentity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// RolePolicyVersion — 직업 부여에 쓰는 정책 버전. 근거·정책 없이 직업을 만들지 않는다.
const RolePolicyVersion = "kdb-person-roles-v1"

// PersonRole — 한 사람이 가진 직업 하나.
type PersonRole struct {
	ID       uuid.UUID
	RoleCode string
	Status   string
	Reason   string
	AddedBy  string
}

// AddPersonRole — 인물에게 직업을 하나 더한다.
//
// **직업은 정체성이 아니다**(식별 계약 §1.1). 한 사람이 정치인이면서 기업인일 수 있고,
// 배우이면서 가수일 수 있다. 그때 만들어지는 것은 **행 하나**이지 새 UUID 가 아니다.
// 그래서 이 함수는 Entity 를 절대 만들지 않는다 — 있는 Entity 에 행을 더할 뿐이다.
//
// 근거를 함께 만든다. "정보가 있어서 적는다"와 "그렇게 보여서 적는다"를 구분해야 하므로,
// 운영자가 무엇을 보고 판단했는지를 reason 으로 남기고 그것이 occupation 주장의 근거가 된다.
// 기간은 모르면 모른다고 둔다 — 관측 시각을 재임 시작으로 넣지 않는다(I12).
func (s *Store) AddPersonRole(ctx context.Context, actor string, entityID uuid.UUID, roleCode, reason string) (PersonRole, error) {
	var out PersonRole
	roleCode = strings.TrimSpace(roleCode)
	reason = strings.TrimSpace(reason)
	if actor == "" || roleCode == "" || len([]rune(reason)) < 10 || len(reason) > 2000 {
		return out, ErrInvalid
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return out, err
	}
	defer rollback(tx)
	if _, err = tx.Exec(ctx, `SET LOCAL lock_timeout='2s'; SET LOCAL statement_timeout='10s'`); err != nil {
		return out, err
	}

	// 대상이 인물인지 먼저 본다. person 이 아닌 곳에 직업을 붙이는 것은 유형 오류다.
	var entityType, ko string
	var locked bool
	err = tx.QueryRow(ctx, `SELECT entity_type, canonical_ko, operator_locked FROM kentity_entities WHERE id=$1 FOR UPDATE`,
		entityID).Scan(&entityType, &ko, &locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	if entityType != "person" {
		return out, ErrInvalid
	}
	// 잠긴 대상은 운영자가 이미 확정한 것이다. 직업을 더하는 것도 확정을 바꾸는 일이므로
	// 잠금을 먼저 풀게 한다 — 잠가 놓고 값을 넣으면 앞뒤가 안 맞는다.
	if locked {
		return out, ErrProtected
	}

	// 직군 사전에 있고 열려 있는 코드만. 없는 코드를 추측해 만들지 않는다.
	var enabled bool
	err = tx.QueryRow(ctx, `SELECT enabled FROM kentity_role_types WHERE code=$1`, roleCode).Scan(&enabled)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !enabled) {
		return out, ErrInvalid
	}
	if err != nil {
		return out, err
	}

	// 이미 있으면 그대로 돌려준다(같은 직업을 두 번 만들지 않는다).
	err = tx.QueryRow(ctx, `SELECT id, role_code, status, reason, assigned_by FROM kentity_person_roles
 WHERE entity_id=$1 AND role_code=$2 AND status<>'withdrawn'`, entityID, roleCode).
		Scan(&out.ID, &out.RoleCode, &out.Status, &out.Reason, &out.AddedBy)
	if err == nil {
		return out, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}

	sum := sha256.Sum256([]byte(entityID.String() + "|occupation|" + roleCode))
	fp := hex.EncodeToString(sum[:])
	evID := uuid.New()
	if _, err = tx.Exec(ctx, `INSERT INTO kentity_evidence
 (id, entity_id, provider, source_record_id, source_url, claim_type, status,
  license_code, export_allowed, verified_by, verified_at, summary,
  claim_fingerprint, source_observation_hash, independent_origin, claim_payload, source_policy_id)
 VALUES ($1,$2,'operator',$3,$4,'occupation','verified','internal-operator-review',true,$5,now(),$6,$7,$8,'operator',
   jsonb_build_object('role_code',$9::text), (SELECT id FROM kentity_source_policies WHERE provider='operator' AND status='approved' LIMIT 1))`,
		evID, entityID, entityID.String(),
		"https://kdb.aiinplanet.com/admin/kentity/"+entityID.String(),
		actor, ko+" 의 직업: "+roleCode, fp[:32], fp[32:], roleCode); err != nil {
		return out, err
	}

	// 기간은 모르면 모른다고 둔다(precision 기본값 unknown).
	if err = tx.QueryRow(ctx, `INSERT INTO kentity_person_roles
 (entity_id, entity_type, role_code, status, evidence_id, assigned_by, reason, policy_version, verified_by, verified_at)
 VALUES ($1,'person',$2,'verified',$3,$4,$5,$6,$4,now())
 RETURNING id, role_code, status, reason, assigned_by`,
		entityID, roleCode, evID, actor, reason, RolePolicyVersion).
		Scan(&out.ID, &out.RoleCode, &out.Status, &out.Reason, &out.AddedBy); err != nil {
		return out, err
	}

	if _, err = tx.Exec(ctx, `INSERT INTO kentity_audit_events(entity_id, actor, action, reason, after_value)
 VALUES ($1,$2,'person_role_added',$3, jsonb_build_object('role_code',$4::text,'evidence_id',$5::uuid))`,
		entityID, actor, reason, roleCode, evID); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

// PersonRoles — 한 사람의 직업 전부. 여러 개인 것이 정상이다.
func (s *Store) PersonRoles(ctx context.Context, entityID uuid.UUID) ([]PersonRole, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id, role_code, status, reason, assigned_by
 FROM kentity_person_roles WHERE entity_id=$1 AND status<>'withdrawn' ORDER BY role_code`, entityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PersonRole{}
	for rows.Next() {
		var r PersonRole
		if err := rows.Scan(&r.ID, &r.RoleCode, &r.Status, &r.Reason, &r.AddedBy); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// EnabledRoleCodes — 사전에서 열린 직군. 화면의 선택지는 여기서만 나온다.
func (s *Store) EnabledRoleCodes(ctx context.Context) ([]string, error) {
	rows, err := s.Pool.Query(ctx, `SELECT code FROM kentity_role_types WHERE enabled ORDER BY sort_order, code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

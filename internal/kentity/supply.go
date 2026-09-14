package kentity

import (
	"context"

	"github.com/google/uuid"
)

// 공급 개시 대기 — 흡수분 중 **지금 열 수 있는 것**과 **막힌 이유**를 한 곳에서 센다.
//
// ★가드는 docs/p4/activate_absorbed_supply.sql 과 **같아야 한다.** 화면이 "열 수 있다"고
// 하는데 스크립트가 막으면 화면이 거짓말을 하는 것이고, 반대면 열 수 있는 것을 못 본다.
// 그래서 조건문을 여기 한 곳에 모아 두고 스크립트 주석이 이 파일을 가리킨다.
// 둘 중 하나를 고치면 반드시 다른 하나도 고친다.
const (
	// 자체 ID 관측이 공급 자격을 준다. QID 는 보조다(I03) — 외부 항목을 가리키는
	// 출처(kentity_external_ids 가 정체성을 정의하는 출처)는 세지 않는다.
	guardOwnIdentity = `EXISTS (SELECT 1 FROM kentity_evidence v
     JOIN kentity_source_policies p ON p.id = v.source_policy_id AND p.status = 'approved'
                                   AND (p.valid_until IS NULL OR p.valid_until > now())
    WHERE v.entity_id = e.id AND v.claim_type = 'identity' AND v.status = 'verified'
      AND v.provider NOT IN (SELECT DISTINCT provider FROM kentity_external_ids))`

	// 올려도 답할 것이 있어야 한다 — 승인 정책이 받치는 외국어 표기.
	guardForeignName = `EXISTS (SELECT 1 FROM kentity_names n
     JOIN kentity_evidence v ON v.id = n.evidence_id AND v.entity_id = n.entity_id
     JOIN kentity_source_policies p ON p.provider = v.provider AND p.status = 'approved'
                                   AND p.name_export_allowed
                                   AND (p.valid_until IS NULL OR p.valid_until > now())
    WHERE n.entity_id = e.id AND n.locale <> 'ko' AND n.kind = 'canonical'
      AND n.form = 'recorded' AND n.status = 'verified' AND v.status = 'verified'
      AND v.export_allowed)`

	// 같은 이름의 활성 기존 원장 대상이 있으면 개별 검수 — 동명 함정.
	guardNoHomonymTrap = `NOT EXISTS (SELECT 1 FROM kentity_entities k
    WHERE k.canonical_ko = e.canonical_ko AND k.write_owner = 'kdb' AND k.status = 'active')`

	// 근거가 외부 출처를 말하면 그 항목이 지목돼 있어야 한다.
	guardSourceItemPinned = `NOT EXISTS (SELECT 1 FROM kentity_names n
     JOIN kentity_evidence v ON v.id = n.evidence_id AND v.entity_id = n.entity_id
    WHERE n.entity_id = e.id AND n.locale <> 'ko' AND n.kind = 'canonical'
      AND n.form = 'recorded' AND n.status = 'verified'
      AND v.status = 'verified' AND v.export_allowed
      AND v.provider IN (SELECT DISTINCT provider FROM kentity_external_ids)
      AND NOT EXISTS (SELECT 1 FROM kentity_external_ids x
                       WHERE x.entity_id = n.entity_id AND x.provider = v.provider
                         AND x.status = 'verified' AND x.external_id = v.source_record_id))`

	// 흡수분 후보 — 이 화면이 세는 모집단.
	supplyBase = `e.write_owner = 'native' AND e.status = 'candidate' AND NOT e.operator_locked`
)

// SupplyGate — 가드 하나와 그것이 막고 있는 수.
type SupplyGate struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Why    string `json:"why"`
	Blocks int    `json:"blocks"`
}

// SupplyRow — 지금 열 수 있는 대상 한 줄.
type SupplyRow struct {
	ID      uuid.UUID `json:"id"`
	KO      string    `json:"ko"`
	Type    string    `json:"type"`
	Subtype string    `json:"subtype"`
	Locales string    `json:"locales"`
}

// SupplyPage — 공급 개시 대기 화면의 자료.
type SupplyPage struct {
	Candidates int          `json:"candidates"`
	Openable   int          `json:"openable"`
	Gates      []SupplyGate `json:"gates"`
	Rows       []SupplyRow  `json:"rows"`
}

// Supply — 모집단·개시 가능 수·가드별 차단 수·상위 목록을 한 번에 읽는다.
func (s *Store) Supply(ctx context.Context, limit int) (SupplyPage, error) {
	var p SupplyPage
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	gates := []struct{ key, label, why, cond string }{
		{"own_identity", "자체 ID 근거 없음", "QID 만으로는 열지 않는다(I03). 자체 관측 근거가 있어야 한다", guardOwnIdentity},
		{"foreign_name", "공급할 외국어 표기 없음", "올려도 답할 것이 없다", guardForeignName},
		{"homonym_trap", "활성 동명이 기존 원장에 있음", "뉴스 표제어가 엉뚱한 대상으로 답해질 자리 — 개별 검수", guardNoHomonymTrap},
		{"source_item", "출처 항목 미지목", "근거가 외부 출처를 말하면서 어느 항목인지 없다 — 확인할 수 없는 권리·출처 주장", guardSourceItemPinned},
	}
	// 유형 미상은 조건이 짧아 따로 둔다.
	const guardTyped = `e.entity_type <> 'unknown'`

	q := `SELECT
  count(*),
  count(*) FILTER (WHERE ` + guardTyped + ` AND ` + guardOwnIdentity + ` AND ` + guardForeignName + `
                     AND ` + guardNoHomonymTrap + ` AND ` + guardSourceItemPinned + `),
  count(*) FILTER (WHERE NOT (` + guardTyped + `)),
  count(*) FILTER (WHERE NOT (` + guardOwnIdentity + `)),
  count(*) FILTER (WHERE NOT (` + guardForeignName + `)),
  count(*) FILTER (WHERE NOT (` + guardNoHomonymTrap + `)),
  count(*) FILTER (WHERE NOT (` + guardSourceItemPinned + `))
 FROM kentity_entities e WHERE ` + supplyBase
	var typed, own, foreign, homonym, item int
	if err := s.Pool.QueryRow(ctx, q).Scan(&p.Candidates, &p.Openable, &typed, &own, &foreign, &homonym, &item); err != nil {
		return p, err
	}
	p.Gates = []SupplyGate{
		{Key: "typed", Label: "유형 미상", Why: "무엇인지 모르는 것을 공급하지 않는다(I02)", Blocks: typed},
		{Key: gates[0].key, Label: gates[0].label, Why: gates[0].why, Blocks: own},
		{Key: gates[1].key, Label: gates[1].label, Why: gates[1].why, Blocks: foreign},
		{Key: gates[2].key, Label: gates[2].label, Why: gates[2].why, Blocks: homonym},
		{Key: gates[3].key, Label: gates[3].label, Why: gates[3].why, Blocks: item},
	}

	rows, err := s.Pool.Query(ctx, `SELECT e.id, e.canonical_ko, e.entity_type, COALESCE(e.subtype,''),
   COALESCE((SELECT string_agg(DISTINCT n.locale, ' ' ORDER BY n.locale) FROM kentity_names n
              WHERE n.entity_id = e.id AND n.locale <> 'ko' AND n.kind='canonical'
                AND n.form='recorded' AND n.status='verified'),'')
 FROM kentity_entities e
 WHERE `+supplyBase+` AND `+guardTyped+` AND `+guardOwnIdentity+` AND `+guardForeignName+`
   AND `+guardNoHomonymTrap+` AND `+guardSourceItemPinned+`
 ORDER BY e.updated_at DESC, e.id LIMIT $1`, limit)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	p.Rows = []SupplyRow{}
	for rows.Next() {
		var r SupplyRow
		if err := rows.Scan(&r.ID, &r.KO, &r.Type, &r.Subtype, &r.Locales); err != nil {
			return p, err
		}
		p.Rows = append(p.Rows, r)
	}
	return p, rows.Err()
}

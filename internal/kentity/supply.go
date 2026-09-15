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

	// homonymTypeCompatible — 흡수분 유형 e 와 기존 원장 유형 k 가 **같은 대상일 수 있는가.**
	//
	// ★한 번 틀리게 썼다(2026-09-15, 같은 날 고침). 처음엔 대응표를
	//   `k.entity_type IN ('drama','movie','show','song_album','character')` 처럼 **기존
	//   원장(kwave) 유형 이름**으로 썼다. 그런데 `k` 는 `kentity_entities` 다 — 거기엔
	//   canonical 13코드만 들어간다. drama·character·group·event_tour 는 **그 표에 존재하지
	//   않는 값**이라 그 가지들은 한 번도 참이 될 수 없었다. 죽은 가지다.
	//   실측 피해는 1짝(work↔work)뿐이었지만, 0136 이 투영을 고쳐 character·event·
	//   concept 이 실제로 들어오기 시작하면 조용히 엉뚱하게 맞기 시작한다.
	//   → 양쪽 다 kentity 유형이므로 **표도 kentity↔kentity 로** 쓴다.
	//
	// ★모르면 "같을 수 있다"로 둔다(보수적).
	//   unknown 은 정체 미확정이라 아무것과도 다르다고 단정할 수 없다.
	//   concept 은 일반어(옛 term)라 대응 자체가 없다.
	//
	// 실측 동명 짝 유형 조합(2026-09-15, 5,179짝):
	//   person↔person 3,341 · location↔work 768 · location↔person 630 · person↔work 262
	//   location↔organization 99 · person↔organization 39 · unknown↔work 14
	//   event↔work 13 · unknown↔person 10 · event↔organization 2 · work↔work 1
	homonymTypeCompatible = `(
     e.entity_type::text = 'unknown' OR k.entity_type::text = 'unknown'
     OR e.entity_type::text = 'concept' OR k.entity_type::text = 'concept'
     OR e.entity_type::text = k.entity_type::text
     OR (e.entity_type::text = 'organization' AND k.entity_type::text = 'company')
     OR (e.entity_type::text = 'company'      AND k.entity_type::text = 'organization')
     OR (e.entity_type::text = 'brand'        AND k.entity_type::text IN ('company','organization','location'))
     OR (e.entity_type::text = 'location'     AND k.entity_type::text = 'brand')
     OR (e.entity_type::text = 'organization' AND k.entity_type::text = 'brand')
     OR (e.entity_type::text = 'company'      AND k.entity_type::text = 'brand')
   )`

	// homonymProvablyDistinct — 이름은 같지만 **다른 대상임이 증명되는가.**
	//
	// ★왜 필요했나 (2026-09-15). 종전 가드는 이름만 보고 **무조건** 막았다:
	//     NOT EXISTS (같은 canonical_ko 인 활성 kdb 대상)
	//   판정을 기록할 자리는 이미 있는데(kentity_crosswalks.candidate_ids, 후보 10,231건)
	//   가드가 그것을 읽지 않으니 **답을 알아도 영원히 막혔다.**
	//
	//   운영자 규칙은 이미 답을 정해 두었다:
	//     I01  한 사람/한 대상 = 하나의 ID. 겸업으로 쪼개지 않는다.
	//          ID 가 갈리는 유일한 이유는 **다른 대상**이라는 것이다.
	//     I05  동명이인은 분리한다.
	//   그러면 "다른 대상임이 증명되면 분리해서 공급"이 규칙의 귀결이다. 막는 것이 I05 위반이다.
	//
	// ★판정 저장표를 새로 만들지 않는다. 저장된 의견은 늙는다 — 근거를 직접 본다.
	//   ⑴ 유형이 대응하지 않으면 다른 대상이다.
	//   ⑵ 양쪽 다 위키데이터 항목을 갖고 **서로 다르면** 다른 대상이다.
	//      같은 항목이면 같은 대상이므로 **막힌 채로 둔다** — 따로 공급하면 I01 위반이다.
	//
	//   실측(2026-09-15): 흡수분↔기존원장 동명 5,159 중
	//     유형 불일치 1,838 · 다른 QID 66  → 1,904건이 이 가드로 풀린다
	//     같은 QID 1,565                  → 계속 막힌다(옳다. P5 채택 대상이다)
	//     판정 못 함 1,708                → 계속 막힌다(M06: 후보를 보여주고 자동 선택하지 않는다)
	// homonymSameWikidataItem — 양쪽이 **같은 위키데이터 항목**을 가리키는가.
	//
	// ★같은 항목이면 유형이 무엇이든 **같은 대상이다** (2026-09-15).
	//   종전엔 두 근거를 OR 로 묶었다. 그래서 "유형이 안 맞는다"가 혼자서 '다른 대상'을
	//   선언했고, **같은 QID 를 가진 쌍도 갈려 나갔다.** 0136 으로 배역 3,946건이
	//   character 로 돌아오자 그 구멍이 드러났다 — 흡수분 person 과 기존 원장 character 가
	//   같은 항목을 가리키면서 서로 다른 대상으로 통과했다(회귀 실측 58건, I01 위반).
	//   실존 인물에 배역 앵커가 붙어 있던 계열(character 178 중 136)이 정확히 이 자리다.
	//
	//   유형은 우리가 붙인 이름표이고 QID 는 세상이 붙인 항목이다. 둘이 어긋나면
	//   **항목이 이긴다** — 어긋났다는 것은 우리 이름표가 틀렸다는 뜻이지
	//   대상이 둘이라는 뜻이 아니다.
	homonymSameWikidataItem = `EXISTS (
          SELECT 1 FROM kentity_external_ids nx, kwave_entity_external_refs kx
           WHERE nx.entity_id = e.id AND nx.provider = 'wikidata' AND nx.status = 'verified'
             AND kx.entity_id = k.id AND kx.provider = 'wikidata'
             AND nx.external_id = kx.external_id)`

	homonymProvablyDistinct = `(
     NOT ` + homonymSameWikidataItem + `
     AND (
       NOT ` + homonymTypeCompatible + `
       OR EXISTS (
            SELECT 1 FROM kentity_external_ids nx, kwave_entity_external_refs kx
             WHERE nx.entity_id = e.id AND nx.provider = 'wikidata' AND nx.status = 'verified'
               AND kx.entity_id = k.id AND kx.provider = 'wikidata'
               AND nx.external_id <> kx.external_id)
     )
   )`

	// 같은 이름의 활성 기존 원장 대상이 있으면 개별 검수 — 동명 함정.
	// **다만 다른 대상임이 증명되면 함정이 아니다**(I05). 위 homonymProvablyDistinct 참조.
	guardNoHomonymTrap = `NOT EXISTS (SELECT 1 FROM kentity_entities k
    WHERE k.canonical_ko = e.canonical_ko AND k.write_owner = 'kdb' AND k.status = 'active'
      AND NOT ` + homonymProvablyDistinct + `)`

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

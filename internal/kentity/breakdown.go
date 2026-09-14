package kentity

import "context"

// 분류 현황 — 원장의 **모양**을 한눈에 본다.
//
// 종전엔 목록에 필터(분야·유형·상태·출처·분류상태)만 있었다. 무엇을 찾을지 **이미 알아야**
// 쓸 수 있는 구조였고, 557,412건이 어떤 모양인지 볼 곳이 없었다.
// 여기 각 줄은 그대로 목록 필터로 이어진다 — 세어 보고 눌러 들어간다.
//
// ★"편집 범위 밖"을 빼고 세지 않는다. 130,134건을 안 보이게 하면 원장이 실제보다
//   작아 보인다. 범위 안과 밖을 **나란히** 둔다(P4.01-a 에서 같은 실수를 했다).

type BreakdownRow struct {
	Key     string `json:"key"`     // 필터에 넣을 값
	Sub     string `json:"sub"`     // 세부(있으면)
	Label   string `json:"label"`   // 사람이 읽는 이름
	InScope int64  `json:"in_scope"`
	Out     int64  `json:"out"`
}

type BreakdownPage struct {
	Types   []BreakdownRow `json:"types"`
	Domains []BreakdownRow `json:"domains"`
	Status  []BreakdownRow `json:"status"`
	Origins []BreakdownRow `json:"origins"`
	Total   int64          `json:"total"`
}

func (s *Store) Breakdown(ctx context.Context) (BreakdownPage, error) {
	var p BreakdownPage
	scan := func(q string) ([]BreakdownRow, error) {
		rows, err := s.Pool.Query(ctx, q)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []BreakdownRow{}
		for rows.Next() {
			var r BreakdownRow
			if err := rows.Scan(&r.Key, &r.Sub, &r.InScope, &r.Out); err != nil {
				return nil, err
			}
			out = append(out, r)
		}
		return out, rows.Err()
	}
	var err error
	// 유형 × 세부. 세부가 없는 것은 빈 문자열로 — "세부를 모른다"도 정보다.
	if p.Types, err = scan(`SELECT e.entity_type, COALESCE(e.subtype,''),
   count(*) FILTER (WHERE e.status <> 'rejected'),
   count(*) FILTER (WHERE e.status = 'rejected')
 FROM kentity_entities e GROUP BY 1,2 ORDER BY 3 DESC, 1, 2`); err != nil {
		return p, err
	}
	// 분야. 한 대상이 여러 분야를 가질 수 있으므로 합이 총수와 다르다 — 화면이 그렇게 말한다.
	if p.Domains, err = scan(`SELECT COALESCE(d.domain,'unassigned'), '',
   count(*) FILTER (WHERE e.status <> 'rejected'),
   count(*) FILTER (WHERE e.status = 'rejected')
 FROM kentity_entities e LEFT JOIN kentity_entity_domains d ON d.entity_id = e.id
 GROUP BY 1 ORDER BY 3 DESC`); err != nil {
		return p, err
	}
	if p.Status, err = scan(`SELECT e.status, '',
   count(*) FILTER (WHERE e.status <> 'rejected'),
   count(*) FILTER (WHERE e.status = 'rejected')
 FROM kentity_entities e GROUP BY 1 ORDER BY 3 DESC`); err != nil {
		return p, err
	}
	// 원래 출처 — 흡수분과 기존 원장을 가른다.
	if p.Origins, err = scan(`SELECT e.origin_system, e.write_owner,
   count(*) FILTER (WHERE e.status <> 'rejected'),
   count(*) FILTER (WHERE e.status = 'rejected')
 FROM kentity_entities e GROUP BY 1,2 ORDER BY 3 DESC`); err != nil {
		return p, err
	}
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM kentity_entities`).Scan(&p.Total); err != nil {
		return p, err
	}
	return p, nil
}

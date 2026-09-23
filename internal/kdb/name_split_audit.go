package kdb

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 이미 갈라져 앉은 것을 **보이게** 한다 — 인입 가드는 앞으로 들어올 것만 막는다.
// 이 저장소가 여러 번 밟은 자리다: 레인을 고쳐도 그 레인이 남긴 것은 아무도 안 걷는다.
//
// 여기서 **판정하지 않는다.** 갈라진 쌍은 두 종류이고 둘은 처방이 반대다:
//   · 같은 대상이 두 ID 를 가졌다      → 병합 (하하/하동훈 · KCM/강창모)
//   · 별칭이 애초에 틀렸다             → 별칭 제거 (「원희」 brand_place 에 사람 별칭)
// 어느 쪽인지는 근거가 정한다(I06·D-37). 그래서 이 감사는 읽기만 하고 줄을 세운다.

// NameSplit — 한 이름이 두 ID 에 앉아 있는 쌍.
type NameSplit struct {
	SplitID     uuid.UUID
	SplitKO     string
	SplitType   string
	SplitStatus string
	OwnerID     uuid.UUID
	OwnerKO     string
	OwnerType   string
	OwnerStatus string
	SameType    bool
	// Requests — 최근 30일 그 이름으로 들어온 소비자 요청 수. 무엇부터 풀지의 순서다.
	Requests int
}

// AuditNameSplits — 자기 정본 이름이 **다른 활성 대상의 별칭**으로도 등록된 대상들.
// 읽기 전용.
func AuditNameSplits(ctx context.Context, pool *pgxpool.Pool, limit int) ([]NameSplit, error) {
	if pool == nil || limit <= 0 {
		return nil, nil
	}
	rows, err := pool.Query(ctx, `
SELECT x.id, x.canonical_ko, x.entity_type::text, x.status,
       y.id, y.canonical_ko, y.entity_type::text, y.status,
       (x.entity_type = y.entity_type) AS same_type,
       COALESCE((SELECT count(*) FROM kwave_kdb_request_terms r
                  WHERE r.term_ko = x.canonical_ko
                    AND r.created_at > now() - interval '30 days'), 0) AS reqs
  FROM kwave_entities x
  JOIN kwave_entities y
    ON y.status = 'active' AND y.id <> x.id
   AND x.canonical_ko = ANY (y.aliases_ko || y.aliases_en || y.aliases_ja || y.aliases_vi
                             || y.aliases_zh || y.aliases_zh_hant || y.aliases_es
                             || y.aliases_id || y.aliases_pt_br)
 WHERE x.status = 'active'
 ORDER BY reqs DESC, x.canonical_ko
 LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NameSplit
	for rows.Next() {
		var s NameSplit
		if err := rows.Scan(&s.SplitID, &s.SplitKO, &s.SplitType, &s.SplitStatus,
			&s.OwnerID, &s.OwnerKO, &s.OwnerType, &s.OwnerStatus, &s.SameType, &s.Requests); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

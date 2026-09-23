package kdb

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 한 대상 = 하나의 ID. 이름은 키가 아니다 — 식별 계약 I01·I05·I13.
//
// 두 방향의 사고가 있고 서로 반대다.
//
//  ① 같은 이름, 다른 대상 → **각자 ID**. 「아리랑」은 민요이자 식당이자 호텔일 수 있다.
//     이건 문제가 아니라 정상이고, 가르는 것은 이름이 아니라 **유형과 구분값**이다.
//     DB 가 이미 강제한다: UNIQUE (canonical_ko, entity_type, COALESCE(disambig,'')).
//     그래서 원본을 아무리 많이 흡수해도 이 축에서는 망가지지 않는다.
//
//  ② 한 대상, 여러 이름 → **하나의 ID**. 실명·예명·별명·닉네임·로마자 표기는
//     그 대상의 이름 변이일 뿐 새 대상이 아니다. 하하=하동훈, KCM=강창모,
//     예리=김예림, 위너=WINNER 는 각각 **한 사람/한 팀**이다.
//
// 이 파일이 막는 것은 ②다. 실측(2026-09-20) 활성 원장에 **307건**이 이미 그렇게 갈라져
// 있었다 — 어떤 대상의 별칭으로 이미 등록된 이름이 «처음 보는 낱말»로 들어와 두 번째
// UUID 를 받았다. 인입 경로가 `canonical_ko` 만 보고 별칭을 안 봤기 때문이다.
//
// 갈라지면 무엇이 깨지나: 소비자마다 저장한 ID 가 달라지고, 근거·표기·정정이 두 곳으로
// 나뉘어 어느 쪽도 완전하지 않게 된다(I01 주석과 같은 이유).

// NameClaim — 이 이름을 자기 것이라 주장하는 대상 하나.
type NameClaim struct {
	ID          uuid.UUID
	CanonicalKO string
	EntityType  string
	Status      string
	// ViaAlias — 정본이 아니라 별칭으로 주장한다. 정본 주장과 같은 무게로 두지 않는다.
	ViaAlias bool
}

// ClaimsForName — 이 이름을 주장하는 대상 전부(기각 제외). 로케일 9칸의 별칭을 모두 본다.
// 별칭을 ko 만 보면 영문 예명(WINNER)·로마자 닉네임이 그대로 새 대상이 된다.
func ClaimsForName(ctx context.Context, pool *pgxpool.Pool, name string) ([]NameClaim, error) {
	if pool == nil || name == "" {
		return nil, nil
	}
	rows, err := pool.Query(ctx, `
SELECT id, canonical_ko, entity_type::text, status, false
  FROM kwave_entities
 WHERE status <> 'rejected' AND canonical_ko = $1
UNION ALL
SELECT id, canonical_ko, entity_type::text, status, true
  FROM kwave_entities
 WHERE status <> 'rejected' AND canonical_ko <> $1
   AND $1 = ANY (aliases_ko || aliases_en || aliases_ja || aliases_vi || aliases_zh
                 || aliases_zh_hant || aliases_es || aliases_id || aliases_pt_br)`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NameClaim
	for rows.Next() {
		var c NameClaim
		if err := rows.Scan(&c.ID, &c.CanonicalKO, &c.EntityType, &c.Status, &c.ViaAlias); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SoleAliasOwner — 「새 UUID 를 만들면 안 되는 경우」를 판정한다.
//
// 참을 돌려주는 조건은 하나뿐이다: **정본으로 주장하는 대상이 하나도 없고, 별칭으로
// 주장하는 대상이 정확히 하나일 때.** 그때 이 이름은 그 대상의 이름 변이다.
//
// 정본 주장이 하나라도 있으면 거짓이다 — 그 이름을 정본으로 쓰는 대상이 따로 있다는
// 뜻이고(「원희」가 brand_place 의 정본이면서 「아일릿 원희」의 별칭인 경우), 그때
// 별칭 주장자에게 붙이면 다른 대상을 삼킨다.
//
// 별칭 주장이 둘 이상이어도 거짓이다. 어느 쪽인지는 근거가 정하는 것이지 순서나
// confidence 가 정하지 않는다(I06 — 근거 없는 자동 병합 금지).
func SoleAliasOwner(claims []NameClaim) (uuid.UUID, bool) {
	var alias []NameClaim
	for _, c := range claims {
		if !c.ViaAlias {
			return uuid.Nil, false
		}
		alias = append(alias, c)
	}
	if len(alias) != 1 {
		return uuid.Nil, false
	}
	return alias[0].ID, true
}

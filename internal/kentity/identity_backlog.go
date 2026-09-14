package kentity

import (
	"context"

	"github.com/google/uuid"
)

// 동일인 판정 대기열 — **두 원장에 같은 대상이 있는 자리**를 한 화면에 모은다.
//
// 왜 필요한가. 흡수한 뒤 "한 대상 = 하나의 ID"(I01)가 깨진 자리가 세 갈래로 생겼는데,
// 어느 화면에서도 볼 수 없었다. 기존 원장 화면은 기존 원장만 보고, 공통 원장 화면은
// 공통 원장만 본다. **겹치는 곳이 문제인데 겹쳐 보는 화면이 없었다.**
//
// 이 대기열은 P4.07(동일인 판정)의 입력이고, P4.03~P4.06(인물 원천)이 여기 막혀 있다 —
// 기존 원장이 이미 쓰는 QID 를 가진 흡수분은 판정 전에는 열 수 없다.
//
// 판정하지 않는다. **세어서 보여줄 뿐이다** — 어느 쪽이 맞는지는 근거를 보고 사람이나
// 승인된 정책이 정한다(I01·D-37).

// IdentityOverlap — 한 갈래의 요약.
type IdentityOverlap struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Why   string `json:"why"`
	Count int    `json:"count"`
}

// IdentityPair — 겹친 한 쌍. 왼쪽이 흡수분, 오른쪽이 상대.
type IdentityPair struct {
	CommonID    uuid.UUID `json:"common_id"`
	KO          string    `json:"ko"`
	Type        string    `json:"type"`
	Subtype     string    `json:"subtype"`
	QID         string    `json:"qid"`
	OtherSide   string    `json:"other_side"`   // 기존 원장 이름 또는 흡수분 이름
	OtherDetail string    `json:"other_detail"` // 유형 등 구별 실마리
}

// IdentityBacklogPage — 화면 자료.
type IdentityBacklogPage struct {
	Overlaps []IdentityOverlap `json:"overlaps"`
	Pairs    []IdentityPair    `json:"pairs"`
	Kind     string            `json:"kind"`
}

const (
	// 같은 위키데이터 항목을 기존 원장이 이미 쓰고 있다 — 가장 확실한 동일 대상 신호다.
	overlapSharedQID = `EXISTS (SELECT 1 FROM kentity_external_ids x
     JOIN kwave_entity_external_refs r ON r.provider = 'wikidata' AND r.external_id = x.external_id
    WHERE x.entity_id = e.id AND x.provider = 'wikidata')`
	// 같은 ko 이름의 **활성** 기존 원장 대상이 있다 — 같은 대상일 수도, 동명일 수도.
	overlapActiveName = `EXISTS (SELECT 1 FROM kwave_entities k
    WHERE k.canonical_ko = e.canonical_ko AND k.status = 'active')`
	// 흡수분 안에서 같은 ko 이름이 둘 이상 — 흡수가 나눠 담은 것인지 정말 다른지.
	overlapInternal = `EXISTS (SELECT 1 FROM kentity_entities k2
    WHERE k2.canonical_ko = e.canonical_ko AND k2.write_owner = 'native'
      AND k2.status <> 'rejected' AND k2.id <> e.id)`

	backlogBase = `e.write_owner = 'native' AND e.status <> 'rejected'`
)

var backlogKinds = map[string]struct{ label, why, cond string }{
	"shared_qid": {"같은 위키데이터 항목을 기존 원장이 쓴다",
		"가장 확실한 동일 대상 신호다. 판정 전에는 흡수분을 열지 않는다(I01).", overlapSharedQID},
	"active_name": {"같은 이름의 활성 기존 원장 대상이 있다",
		"같은 대상일 수도, 동명일 수도 있다. 근거를 보고 가른다.", overlapActiveName},
	"internal": {"흡수분 안에 같은 이름이 둘 이상",
		"흡수가 하나를 여럿으로 나눠 담았을 수 있다.", overlapInternal},
}

// IdentityBacklog — 갈래별 수를 세고, kind 가 주어지면 그 갈래의 실제 쌍을 보여준다.
func (s *Store) IdentityBacklog(ctx context.Context, kind string, limit int) (IdentityBacklogPage, error) {
	var p IdentityBacklogPage
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var shared, active, internal int
	if err := s.Pool.QueryRow(ctx, `SELECT
   count(*) FILTER (WHERE `+overlapSharedQID+`),
   count(*) FILTER (WHERE `+overlapActiveName+`),
   count(*) FILTER (WHERE `+overlapInternal+`)
  FROM kentity_entities e WHERE `+backlogBase).Scan(&shared, &active, &internal); err != nil {
		return p, err
	}
	p.Overlaps = []IdentityOverlap{
		{Key: "shared_qid", Label: backlogKinds["shared_qid"].label, Why: backlogKinds["shared_qid"].why, Count: shared},
		{Key: "active_name", Label: backlogKinds["active_name"].label, Why: backlogKinds["active_name"].why, Count: active},
		{Key: "internal", Label: backlogKinds["internal"].label, Why: backlogKinds["internal"].why, Count: internal},
	}
	k, ok := backlogKinds[kind]
	if !ok {
		p.Kind = ""
		p.Pairs = []IdentityPair{}
		return p, nil
	}
	p.Kind = kind
	rows, err := s.Pool.Query(ctx, `SELECT e.id, e.canonical_ko, e.entity_type, COALESCE(e.subtype,''),
   COALESCE((SELECT x.external_id FROM kentity_external_ids x
              WHERE x.entity_id = e.id AND x.provider='wikidata' LIMIT 1),''),
   COALESCE((SELECT k.canonical_ko FROM kwave_entities k
              WHERE k.canonical_ko = e.canonical_ko ORDER BY (k.status='active') DESC LIMIT 1),''),
   COALESCE((SELECT k.entity_type::text || ' · ' || k.status FROM kwave_entities k
              WHERE k.canonical_ko = e.canonical_ko ORDER BY (k.status='active') DESC LIMIT 1),'')
 FROM kentity_entities e
 WHERE `+backlogBase+` AND `+k.cond+`
 ORDER BY e.canonical_ko, e.id LIMIT $1`, limit)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	p.Pairs = []IdentityPair{}
	for rows.Next() {
		var r IdentityPair
		if err := rows.Scan(&r.CommonID, &r.KO, &r.Type, &r.Subtype, &r.QID, &r.OtherSide, &r.OtherDetail); err != nil {
			return p, err
		}
		p.Pairs = append(p.Pairs, r)
	}
	return p, rows.Err()
}

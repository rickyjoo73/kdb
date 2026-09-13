package kentity

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Catalog is the operator's inventory, not a count of translation-ready names.
type CatalogFilter struct {
	Q, Type, Domain, Status, Origin, Period, Sort string
	// Classify — 분류 검수 대기분을 찾는 필터. 전량 흡수 뒤에는 "유형 미상"이 십만 단위로
	// 쌓이는데, 그걸 목록에서 골라낼 수단이 없으면 검수를 시작할 수가 없다.
	Classify string
	Offset   int
}

type CatalogRow struct {
	Entity
	CreatedAt, UpdatedAt time.Time
}

type DomainCount struct {
	Code, Label       string
	Total, Candidates int64
}

type CatalogOverview struct {
	Total, Candidates, Unassigned, New24h int64
	// ★등록 수와 **실제 공급 가능한 표기 수**는 다른 수다. 원장에 536,322건이 들어와도
	// 검증된 표기가 242건이면 소비자가 쓸 수 있는 것은 242건이다. 둘을 같은 칸에 두면
	// 큰 수가 작은 수를 가린다 — 그래서 함께 센다(원장 P3.07 금지사항).
	VerifiedNames    int64 // 검증된 표기 수 = 실제 공급 가능
	PendingClassify  int64 // 분류 검수 대기
	UnknownType      int64 // 유형 미상
	Domains          []DomainCount
}

type CatalogPage struct {
	Items    []CatalogRow
	Total    int64
	Overview CatalogOverview
}

func (f CatalogFilter) Valid() bool {
	return len([]rune(f.Q)) <= 200 && (f.Type == "" || supportedTypes[f.Type]) &&
		(f.Domain == "" || f.Domain == "unassigned" || supportedDomains[f.Domain]) &&
		(f.Status == "" || f.Status == "active" || f.Status == "candidate" || f.Status == "rejected" || f.Status == "retired") &&
		(f.Origin == "" || f.Origin == "kdb" || f.Origin == "tdb" || f.Origin == "native") &&
		(f.Period == "" || f.Period == "24h") && (f.Sort == "" || f.Sort == "created" || f.Sort == "updated") &&
		(f.Classify == "" || f.Classify == "pending" || f.Classify == "unknown_type" ||
			f.Classify == "no_subtype" || f.Classify == "needs_disambig") &&
		f.Offset >= 0 && f.Offset <= 100000
}

const catalogWhere = ` WHERE ($1='' OR strpos(lower(e.canonical_ko),lower($1))>0)
 AND ($2='' OR e.entity_type=$2)
 AND ($3='' OR ($3='unassigned' AND NOT EXISTS(SELECT 1 FROM kentity_entity_domains d WHERE d.entity_id=e.id))
 OR EXISTS(SELECT 1 FROM kentity_entity_domains d WHERE d.entity_id=e.id AND d.domain=$3))
 AND ($4='' OR e.status=$4) AND ($5='' OR e.origin_system=$5)
 AND ($6='' OR e.created_at>=now()-interval '24 hours')
 AND ($7='' OR ($7='pending' AND e.classification_status='pending')
            OR ($7='unknown_type' AND e.entity_type='unknown')
            OR ($7='no_subtype' AND e.subtype IS NULL)
            OR ($7='needs_disambig' AND e.classification_reason LIKE '%구분값 미상%'))`

// ★목록 질의에서 to_jsonb(e) 를 걷어냈다 (2026-09-14).
// `COALESCE(to_jsonb(e)->>'write_owner', e.origin_system)` 은 write_owner 열이 없던 시절의
// 호환 장치였다. 지금 이 열은 NOT NULL DEFAULT 'native' 이라 장치가 할 일이 없는데,
// **전체 행 참조(whole-row var)** 라서 계획이 병렬을 잃고 55만 행을 직렬로 훑었다 —
// 목록 8건을 뽑는 데 6.08초. 열을 직접 참조하니 0.33초다(18배). 운영 실측.
// 이 6초가 대시보드의 6초 예산을 통째로 먹어서, 뒤따르던 집계 두 개는 시작도 못 하고
// "조회 실패"로 떨어졌다. 화면에 빨간 배너 셋이 떴지만 원인은 하나였다.
// One read-only snapshot keeps the list, total and domain cards consistent.
// Filters are parameters; only two fixed order expressions can reach SQL.
func (s *Store) Catalog(ctx context.Context, f CatalogFilter, limit int) (CatalogPage, error) {
	var p CatalogPage
	if !f.Valid() || limit < 1 || limit > 100 {
		return p, ErrInvalid
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return p, err
	}
	defer rollback(tx)
	err = tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE status='candidate'),
 count(*) FILTER(WHERE NOT EXISTS(SELECT 1 FROM kentity_entity_domains d WHERE d.entity_id=e.id)),
 count(*) FILTER(WHERE created_at>=now()-interval '24 hours'),
 count(*) FILTER(WHERE classification_status='pending'),
 count(*) FILTER(WHERE entity_type='unknown') FROM kentity_entities e`).Scan(
		&p.Overview.Total, &p.Overview.Candidates, &p.Overview.Unassigned, &p.Overview.New24h,
		&p.Overview.PendingClassify, &p.Overview.UnknownType)
	if err != nil {
		return p, err
	}
	// 검증된 표기만 센다. 보관만 된 표기(unverified)는 공급되지 않으므로 사용 가능 수가 아니다.
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM kentity_names WHERE status='verified'`).
		Scan(&p.Overview.VerifiedNames); err != nil {
		return p, err
	}
	domains, err := tx.Query(ctx, `SELECT d.code,d.label_ko,count(ed.entity_id),count(ed.entity_id) FILTER(WHERE e.status='candidate')
 FROM kentity_domains d LEFT JOIN kentity_entity_domains ed ON ed.domain=d.code
 LEFT JOIN kentity_entities e ON e.id=ed.entity_id GROUP BY d.code,d.label_ko
 ORDER BY CASE d.code WHEN 'politics' THEN 1 WHEN 'economy' THEN 2 WHEN 'society' THEN 3 WHEN 'sports' THEN 4 WHEN 'entertainment' THEN 5 WHEN 'travel' THEN 6 WHEN 'government' THEN 7 ELSE 8 END`)
	if err != nil {
		return p, err
	}
	for domains.Next() {
		var d DomainCount
		if err = domains.Scan(&d.Code, &d.Label, &d.Total, &d.Candidates); err != nil {
			domains.Close()
			return p, err
		}
		p.Overview.Domains = append(p.Overview.Domains, d)
	}
	err = domains.Err()
	domains.Close()
	if err != nil {
		return p, err
	}
	args := []any{strings.TrimSpace(f.Q), f.Type, f.Domain, f.Status, f.Origin, f.Period, f.Classify}
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM kentity_entities e`+catalogWhere, args...).Scan(&p.Total); err != nil {
		return p, err
	}
	order := "e.created_at DESC,e.id"
	if f.Sort == "updated" {
		order = "e.updated_at DESC,e.id"
	}
	rows, err := tx.Query(ctx, `SELECT e.id,e.entity_type,COALESCE(e.subtype,''),e.canonical_ko,e.origin_system,e.status,e.operator_locked,e.revision,
 e.write_owner, ARRAY(SELECT d.domain FROM kentity_entity_domains d WHERE d.entity_id=e.id ORDER BY d.domain),e.created_at,e.updated_at
 FROM kentity_entities e`+catalogWhere+` ORDER BY `+order+` LIMIT $8 OFFSET $9`, append(args, limit, f.Offset)...)
	if err != nil {
		return p, err
	}
	for rows.Next() {
		var e CatalogRow
		if err = rows.Scan(&e.ID, &e.Type, &e.Subtype, &e.KO, &e.Origin, &e.Status, &e.Locked, &e.Revision, &e.WriteOwner, &e.Domains, &e.CreatedAt, &e.UpdatedAt); err != nil {
			rows.Close()
			return p, err
		}
		p.Items = append(p.Items, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return p, err
	}
	return p, tx.Commit(ctx)
}

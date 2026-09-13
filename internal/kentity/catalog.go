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
	Offset                                        int
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
	Domains                               []DomainCount
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
		f.Offset >= 0 && f.Offset <= 100000
}

const catalogWhere = ` WHERE ($1='' OR strpos(lower(e.canonical_ko),lower($1))>0)
 AND ($2='' OR e.entity_type=$2)
 AND ($3='' OR ($3='unassigned' AND NOT EXISTS(SELECT 1 FROM kentity_entity_domains d WHERE d.entity_id=e.id))
 OR EXISTS(SELECT 1 FROM kentity_entity_domains d WHERE d.entity_id=e.id AND d.domain=$3))
 AND ($4='' OR e.status=$4) AND ($5='' OR e.origin_system=$5)
 AND ($6='' OR e.created_at>=now()-interval '24 hours')`

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
 count(*) FILTER(WHERE created_at>=now()-interval '24 hours') FROM kentity_entities e`).Scan(
		&p.Overview.Total, &p.Overview.Candidates, &p.Overview.Unassigned, &p.Overview.New24h)
	if err != nil {
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
	args := []any{strings.TrimSpace(f.Q), f.Type, f.Domain, f.Status, f.Origin, f.Period}
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM kentity_entities e`+catalogWhere, args...).Scan(&p.Total); err != nil {
		return p, err
	}
	order := "e.created_at DESC,e.id"
	if f.Sort == "updated" {
		order = "e.updated_at DESC,e.id"
	}
	rows, err := tx.Query(ctx, `SELECT e.id,e.entity_type,COALESCE(e.subtype,''),e.canonical_ko,e.origin_system,e.status,e.operator_locked,e.revision,
 COALESCE(to_jsonb(e)->>'write_owner',e.origin_system), ARRAY(SELECT d.domain FROM kentity_entity_domains d WHERE d.entity_id=e.id ORDER BY d.domain),e.created_at,e.updated_at
 FROM kentity_entities e`+catalogWhere+` ORDER BY `+order+` LIMIT $7 OFFSET $8`, append(args, limit, f.Offset)...)
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

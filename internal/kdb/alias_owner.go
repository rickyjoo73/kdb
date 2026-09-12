package kdb

import (
	"context"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AliasOwner is an input to the existing Korean-alias conflict policy, not an
// identity/merge decision. An ambiguous alias must never merge two entities.
type AliasOwner struct {
	ID          uuid.UUID
	CanonicalKO string
	Confidence  float64
}

// PreferredAliasOwner is shared by the producer and cleanup worker. A producer
// must not reinstall an alias that the cleanup worker will immediately remove.
// Multiple exact-name owners (legitimate homonyms) remain ambiguous.
func PreferredAliasOwner(alias string, owners []AliasOwner) uuid.UUID {
	var exact []uuid.UUID
	for _, owner := range owners {
		if owner.CanonicalKO == alias {
			exact = append(exact, owner.ID)
		}
	}
	if len(exact) == 1 {
		return exact[0]
	}
	if len(exact) > 1 || len(owners) == 0 {
		return uuid.Nil
	}
	ranked := append([]AliasOwner(nil), owners...)
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].Confidence > ranked[j].Confidence })
	if len(ranked) == 1 || ranked[0].Confidence-ranked[1].Confidence >= 0.1-1e-9 {
		return ranked[0].ID
	}
	return uuid.Nil
}

// FilterKoreanAliasAdds previews the same active, unlocked owner set used by
// stepResolveAliasConflicts, including the proposed recipient. This does not
// reject shared aliases merely because another entity has the same spelling.
func FilterKoreanAliasAdds(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID, vals []string) ([]string, error) {
	rows, err := pool.Query(ctx, `
SELECT id, canonical_ko, confidence, COALESCE(aliases_ko,'{}'::text[])
  FROM kwave_entities
 WHERE status='active' AND operator_locked=false
   AND (id=$1 OR aliases_ko && $2::text[])`, id, vals)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var recipient *AliasOwner
	byAlias := map[string][]AliasOwner{}
	for rows.Next() {
		var owner AliasOwner
		var aliases []string
		if err := rows.Scan(&owner.ID, &owner.CanonicalKO, &owner.Confidence, &aliases); err != nil {
			return nil, err
		}
		if owner.ID == id {
			copy := owner
			recipient = &copy
			continue
		}
		for _, alias := range aliases {
			byAlias[alias] = append(byAlias[alias], owner)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Locked/candidate recipients are outside this cleanup policy.
	if recipient == nil {
		return vals, nil
	}
	var allowed []string
	for _, alias := range vals {
		winner := PreferredAliasOwner(alias, append(byAlias[alias], *recipient))
		if winner == uuid.Nil || winner == id {
			allowed = append(allowed, alias)
		}
	}
	return allowed, nil
}

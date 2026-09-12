package kdb

import (
	"testing"

	"github.com/google/uuid"
)

func TestPreferredAliasOwner(t *testing.T) {
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	for _, tc := range []struct {
		name   string
		owners []AliasOwner
		want   uuid.UUID
	}{
		{"no owners", nil, uuid.Nil},
		{"only owner", []AliasOwner{{a, "다른 이름", .7}}, a},
		{"existing cleanup winner", []AliasOwner{{a, "원문A", .7}, {b, "원문B", .95}}, b},
		{"exact name wins", []AliasOwner{{a, "별칭", .7}, {b, "다른 이름", .95}}, a},
		{"homonyms stay ambiguous", []AliasOwner{{a, "별칭", .7}, {b, "별칭", .95}}, uuid.Nil},
		{"small confidence gap", []AliasOwner{{a, "A", .9}, {b, "B", .95}}, uuid.Nil},
		{"exact margin", []AliasOwner{{a, "A", .8}, {b, "B", .9}}, b},
		{"two strong owners prevent exclusion", []AliasOwner{{a, "A", .7}, {b, "B", .95}, {c, "C", .94}}, uuid.Nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := PreferredAliasOwner("별칭", tc.owners); got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

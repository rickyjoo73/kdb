package kdb

import (
	"testing"

	"github.com/rickyjoo73/kdb/internal/kdb/musicbrainz"
)

func TestMusicBrainzArtistResourceContract(t *testing.T) {
	tests := []struct {
		entityType string
		resource   string
		allowed    bool
	}{
		{"group", "artist", true},
		{"song_album", "", false},
		{"person", "", false},
		{"movie", "", false},
		{"unknown", "", false},
	}
	for _, tc := range tests {
		gotResource, gotAllowed := musicBrainzResourceForEntityType(tc.entityType)
		if gotResource != tc.resource || gotAllowed != tc.allowed {
			t.Errorf("type %q: got (%q,%v), want (%q,%v)",
				tc.entityType, gotResource, gotAllowed, tc.resource, tc.allowed)
		}
	}
}

func TestMusicBrainzGroupCandidateRequiresGroupSubtype(t *testing.T) {
	base := musicbrainz.Artist{ID: "mbid", Name: "MONSTER", Score: 100}
	base.Type = "Person"
	if isMusicBrainzGroupMatch("MONSTER", base) {
		t.Fatal("same-name solo artist must not anchor a KDB group")
	}
	base.Type = "Group"
	if !isMusicBrainzGroupMatch("MONSTER", base) {
		t.Fatal("exact-name Group artist should satisfy the group contract")
	}
	base.Name = "Monster X"
	if isMusicBrainzGroupMatch("MONSTER", base) {
		t.Fatal("fuzzy name must not satisfy the group contract")
	}
}

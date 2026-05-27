package kdb

import "testing"

func TestPriority_ordering(t *testing.T) {
	if Priority(SourceOperatorLocked) >= Priority(SourceMediaConsensus) {
		t.Errorf("operator-locked must outrank media-consensus")
	}
	if Priority(SourceMediaConsensus) >= Priority(SourceWikidataLabel) {
		t.Errorf("media-consensus must outrank wikidata-label (운영자 정공법)")
	}
	if Priority(SourceWikidataLabel) >= Priority(SourceWikipediaLanglinks) {
		t.Errorf("wikidata-label must outrank wikipedia-langlinks")
	}
	if Priority(SourceWikipediaLanglinks) >= Priority(SourceWikipediaSitelink) {
		t.Errorf("wikipedia-langlinks must outrank wikipedia-sitelink")
	}
	if Priority(SourceUnknown) <= Priority(SourceWikipediaSitelink) {
		t.Errorf("unknown must be lower priority than any named source")
	}
}

func TestShouldReplace(t *testing.T) {
	cases := []struct {
		name                   string
		cur                    Source
		curVal                 string
		incoming               Source
		newVal                 string
		wantReplace, wantDrift bool
	}{
		{"operator-locked never replaced",
			SourceOperatorLocked, "Park Shin Hye",
			SourceMediaConsensus, "Park Sin Hye",
			false, false},
		{"operator never replaced",
			SourceOperator, "X",
			SourceWikidataLabel, "Y",
			false, false},
		{"media-consensus replaces wikidata",
			SourceWikidataLabel, "Park Shin-hye",
			SourceMediaConsensus, "Park Sin Hye",
			true, false},
		{"media-consensus replaces langlinks",
			SourceWikipediaLanglinks, "old",
			SourceMediaConsensus, "new",
			true, false},
		{"wikidata replaces langlinks (incoming priority lower number = higher rank)",
			SourceWikipediaLanglinks, "old",
			SourceWikidataLabel, "new",
			true, false},
		{"langlinks does NOT replace wikidata",
			SourceWikidataLabel, "wikidata-val",
			SourceWikipediaLanglinks, "langlinks-val",
			false, false},
		{"same priority same value = no-op",
			SourceMediaConsensus, "Jimin",
			SourceMediaConsensus, "Jimin",
			false, false},
		{"same priority different value = drift, no replace",
			SourceMediaConsensus, "Jimin",
			SourceMediaConsensus, "JiMin",
			false, true},
		{"sitelink replaces unknown",
			SourceUnknown, "",
			SourceWikipediaSitelink, "title",
			true, false},
		{"sitelink does NOT replace wikidata",
			SourceWikidataLabel, "x",
			SourceWikipediaSitelink, "y",
			false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotR, gotD := ShouldReplace(c.cur, c.curVal, c.incoming, c.newVal)
			if gotR != c.wantReplace || gotD != c.wantDrift {
				t.Errorf("ShouldReplace(%s/%q → %s/%q) = replace=%v drift=%v; want replace=%v drift=%v",
					c.cur, c.curVal, c.incoming, c.newVal, gotR, gotD, c.wantReplace, c.wantDrift)
			}
		})
	}
}

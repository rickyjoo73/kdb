package kdb

import "testing"

func TestPriority_ordering(t *testing.T) {
	// 운영자 정공법: operator > L(media-consensus) > l(rss-observation) > O(api) > W(wikidata) > w(wiki 보조) > codex
	if Priority(SourceOperatorLocked) >= Priority(SourceMediaConsensus) {
		t.Errorf("operator-locked must outrank media-consensus")
	}
	if Priority(SourceMediaConsensus) >= Priority(SourceRSSObservation) {
		t.Errorf("media-consensus must outrank rss-observation (단일 매체)")
	}
	if Priority(SourceRSSObservation) >= Priority(SourceTMDb) {
		t.Errorf("rss-observation must outrank 권위 API (현지 매체 표기가 우선)")
	}
	if Priority(SourceTMDb) >= Priority(SourceWikidataLabel) {
		t.Errorf("권위 API must outrank wikidata-label")
	}
	if Priority(SourceWikidataLabel) >= Priority(SourceWikipediaLanglinks) {
		t.Errorf("wikidata-label must outrank wikipedia-langlinks")
	}
	if Priority(SourceWikipediaLanglinks) >= Priority(SourceCodexFallback) {
		t.Errorf("wikipedia 보조 must outrank codex-fallback")
	}
	if Priority(SourceUnknown) <= Priority(SourceCodexFallback) {
		t.Errorf("unknown must be lower priority than any named source")
	}

	// rss-observation:<domain> 형식도 같은 priority.
	if Priority(Source("rss-observation:vnexpress.net")) != Priority(SourceRSSObservation) {
		t.Errorf("rss-observation:<domain> must share priority with bare rss-observation")
	}
}

func TestMark(t *testing.T) {
	cases := []struct {
		s    Source
		want string
	}{
		{SourceOperatorLocked, "🔒"},
		{SourceOperator, "🔒"},
		{SourceMediaConsensus, "L"},
		{SourceRSSObservation, "l"},
		{Source("rss-observation:vnexpress.net"), "l"},
		{SourceTMDb, "O"},
		{SourceKOFIC, "O"},
		{SourceMusicBrainz, "O"},
		{SourceWikidataLabel, "W"},
		{SourceWikipediaLanglinks, "w"},
		{SourceWikipediaSitelink, "w"},
		{SourceCodexFallback, "?"},
	}
	for _, c := range cases {
		if got := Mark(c.s); got != c.want {
			t.Errorf("Mark(%s) = %q, want %q", c.s, got, c.want)
		}
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
		{"media-consensus replaces TMDb",
			SourceTMDb, "old-tmdb",
			SourceMediaConsensus, "new-media",
			true, false},
		{"rss-observation replaces wikidata",
			SourceWikidataLabel, "wikidata-val",
			Source("rss-observation:vnexpress.net"), "vnexpress-val",
			true, false},
		{"rss-observation replaces TMDb",
			SourceTMDb, "tmdb-val",
			Source("rss-observation:globo.com"), "globo-val",
			true, false},
		{"TMDb replaces wikidata",
			SourceWikidataLabel, "wd-val",
			SourceTMDb, "tmdb-val",
			true, false},
		{"wikidata does NOT replace TMDb",
			SourceTMDb, "tmdb-val",
			SourceWikidataLabel, "wd-val",
			false, false},
		{"wikidata replaces langlinks",
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
		{"two rss-observation domains different value = drift",
			Source("rss-observation:vnexpress.net"), "Park Shin Hye",
			Source("rss-observation:tuoitre.vn"), "Park Sin Hye",
			false, true},
		{"sitelink replaces unknown",
			SourceUnknown, "",
			SourceWikipediaSitelink, "title",
			true, false},
		{"codex-fallback does NOT replace wikidata",
			SourceWikidataLabel, "x",
			SourceCodexFallback, "y",
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

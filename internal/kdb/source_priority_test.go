package kdb

import (
	"os"
	"regexp"
	"strconv"
	"testing"
)

// TestSQLPriorityMatchesGo — 최신 kdb_source_priority SQL 함수(0078)가 Go
// Priority() 와 1:1 인지 파일을 직접 파싱해 검증한다. 둘 중 하나만 바꾸면 실패
// → 0050 때처럼 드리프트(권위 API 가 SQL 에서 99로 떨어지던) 재발 차단.
func TestSQLPriorityMatchesGo(t *testing.T) {
	const path = "../../migrations/0082_kdb_romanization_source.sql"
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(body)

	// rss-observation 은 prefix(LIKE) 매칭.
	likeRe := regexp.MustCompile(`WHEN s LIKE 'rss-observation%'\s+THEN\s+(\d+)`)
	m := likeRe.FindStringSubmatch(sql)
	if m == nil {
		t.Fatal("migration missing rss-observation LIKE branch")
	}
	if got, _ := strconv.Atoi(m[1]); got != Priority(SourceRSSObservation) {
		t.Errorf("SQL rss-observation prio=%d, Go=%d", got, Priority(SourceRSSObservation))
	}

	// 모든 exact-match source 는 'WHEN s = ''<token>'' THEN N' 한 줄씩.
	exact := []Source{
		SourceOperatorLocked, SourceOperator, SourceLocalUsage, SourceMediaConsensus,
		SourceTMDb, SourceKOFIC, SourceKMDb, SourceMusicBrainz, SourceNaverPeople,
		SourceCorrectionVerified, SourceNetflix, SourceDisney,
		SourceWikidataLabel, SourceWikipediaLanglinks, SourceWikipediaSitelink,
		SourceWikipediaZhVariant, SourceLocalSearch, SourceMyDramaList, SourceRomanization, SourceCodexFallback,
	}
	for _, s := range exact {
		re := regexp.MustCompile(`WHEN s = '` + regexp.QuoteMeta(string(s)) + `'\s+THEN\s+(\d+)`)
		mm := re.FindStringSubmatch(sql)
		if mm == nil {
			t.Errorf("migration missing WHEN branch for %q", s)
			continue
		}
		sqlPrio, _ := strconv.Atoi(mm[1])
		if sqlPrio != Priority(s) {
			t.Errorf("source %q: SQL prio=%d, Go Priority=%d (drift!)", s, sqlPrio, Priority(s))
		}
	}

	// ELSE = unknown priority.
	elseRe := regexp.MustCompile(`ELSE\s+(\d+)`)
	em := elseRe.FindStringSubmatch(sql)
	if em == nil {
		t.Fatal("migration missing ELSE branch")
	}
	if got, _ := strconv.Atoi(em[1]); got != Priority(SourceUnknown) {
		t.Errorf("SQL ELSE prio=%d, Go unknown=%d", got, Priority(SourceUnknown))
	}
}

func TestPriority_ordering(t *testing.T) {
	// 운영자 정공법: operator > L(media-consensus) > l(rss-observation) > O(api) > W(wikidata) > w(wiki 보조) > codex
	if Priority(SourceOperatorLocked) >= Priority(SourceMediaConsensus) {
		t.Errorf("operator-locked must outrank media-consensus")
	}
	if Priority(SourceLocalUsage) >= Priority(SourceMediaConsensus) {
		t.Errorf("local-usage(검색그라운드 확정 현지표기) must outrank media-consensus")
	}
	if Priority(SourceLocalUsage) != Priority(SourceOperatorLocked) {
		t.Errorf("local-usage must share top tier(1) with operator-locked")
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
	if Priority(SourceWikipediaLanglinks) >= Priority(SourceLocalSearch) {
		t.Errorf("wikipedia 보조 must outrank local-search (권위 보조 > 검색 잠정)")
	}
	if Priority(SourceLocalSearch) >= Priority(SourceCodexFallback) {
		t.Errorf("local-search(검색그라운드) must outrank codex-fallback(LLM 합성)")
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
		{SourceLocalUsage, "★"},
		{SourceMediaConsensus, "L"},
		{SourceRSSObservation, "l"},
		{Source("rss-observation:vnexpress.net"), "l"},
		{SourceTMDb, "O"},
		{SourceKOFIC, "O"},
		{SourceMusicBrainz, "O"},
		{SourceCorrectionVerified, "C"},
		{SourceWikidataLabel, "W"},
		{SourceWikipediaLanglinks, "w"},
		{SourceWikipediaSitelink, "w"},
		{SourceLocalSearch, "s"},
		{SourceMyDramaList, "m"},
		{SourceNetflix, "O"},
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
		{"local-usage replaces media-consensus (검색그라운드 확정 > 매체합의)",
			SourceMediaConsensus, "Park Sin Hye",
			SourceLocalUsage, "Park Shin-hye",
			true, false},
		{"local-usage replaces wikidata",
			SourceWikidataLabel, "wd-val",
			SourceLocalUsage, "lu-val",
			true, false},
		{"media-consensus does NOT replace local-usage",
			SourceLocalUsage, "lu-val",
			SourceMediaConsensus, "media-val",
			false, false},
		{"operator-locked never replaced by local-usage (사람 잠금 보호)",
			SourceOperatorLocked, "human-val",
			SourceLocalUsage, "lu-val",
			false, false},
		{"two local-usage different value = drift",
			SourceLocalUsage, "old-lu",
			SourceLocalUsage, "new-lu",
			false, true},
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

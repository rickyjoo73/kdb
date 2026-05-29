package wikidata

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestSearchAndFetch_live — KDB 도메인 단위 통합 테스트.
// 네트워크 사용. CI 회피용 KDB_SKIP_LIVE=1 으로 skip.
func TestSearchAndFetch_live(t *testing.T) {
	if os.Getenv("KDB_SKIP_LIVE") != "" {
		t.Skip("KDB_SKIP_LIVE set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	c := New()
	c.UserAgent = "kdb-test/0.1 (https://kdb.aiinplanet.com)"

	cases := []struct {
		query       string
		expectQID   string
		wantLocales []string
	}{
		{"박신혜", "Q497785", []string{"ko", "en", "ja", "vi", "zh_hant", "es", "id", "pt_br"}},
		{"BTS", "Q13580495", []string{"ko", "en", "ja"}},
		{"오징어 게임", "Q106582931", []string{"ko", "en", "ja", "vi", "es", "pt_br"}},
	}

	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			ent, cand, err := c.SearchAndFetch(ctx, tc.query)
			if err != nil {
				t.Fatalf("SearchAndFetch: %v", err)
			}
			if ent == nil {
				t.Fatalf("no entity returned (cand=%+v)", cand)
			}
			if tc.expectQID != "" && ent.QID != tc.expectQID {
				t.Errorf("QID = %s, want %s (cand=%+v)", ent.QID, tc.expectQID, cand)
			}
			for _, loc := range tc.wantLocales {
				if v := ent.Labels[loc]; v == "" {
					t.Errorf("Labels[%s] empty (got labels=%v)", loc, ent.Labels)
				}
			}
			t.Logf("%s → QID=%s, labels=%d, aliases=%d, sitelinks=%d",
				tc.query, ent.QID, len(ent.Labels), len(ent.Aliases), len(ent.Sitelinks))
		})
	}
}

func TestIsKWaveDescription(t *testing.T) {
	yes := []string{
		"South Korean actress and singer (born 1990)",
		"South Korean musical group; boy band",
		"한국의 가수",
		"K-pop girl group",
	}
	no := []string{
		"international airport in Bratislava",
		"documentary that goes \"behind the scenes\"",
		"",
		"American actor",
	}
	for _, d := range yes {
		if !IsKWaveDescription(d) {
			t.Errorf("expected match: %q", d)
		}
	}
	for _, d := range no {
		if IsKWaveDescription(d) {
			t.Errorf("expected non-match: %q", d)
		}
	}
	_ = strings.ContainsAny // keep import warning at bay
}

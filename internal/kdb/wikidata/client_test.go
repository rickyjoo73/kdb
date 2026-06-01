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

func TestNormalizeName(t *testing.T) {
	cases := map[string]string{
		"Park Bo-gum": "parkbogum",
		"park bo gum": "parkbogum",
		"パク・ボゴム":      "パクボゴム",
		"  BTS  ":     "bts",
		"J.Y. Park":   "jypark",
	}
	for in, want := range cases {
		if got := normalizeName(in); got != want {
			t.Errorf("normalizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestEntityMatchesQuery — 오매칭 방지 가드. 박보검 검색에 엉뚱한 인물(허성진)
// entity 가 오면 거부, 진짜 박보검 entity(라벨/별칭 일치)는 채택해야 한다.
func TestEntityMatchesQuery(t *testing.T) {
	wrong := &Entity{ // 검색은 "박보검"인데 fetch 된 건 다른 인물
		QID:    "Q_WRONG",
		Labels: map[string]string{"ko": "허성진", "ja": "ホ・ソンジン", "en": "Heo Sung-jin"},
	}
	if entityMatchesQuery("박보검", wrong) {
		t.Error("expected REJECT: 박보검 vs 허성진 entity")
	}

	right := &Entity{ // 진짜 박보검 (ko 라벨 일치)
		QID:    "Q15977222",
		Labels: map[string]string{"ko": "박보검", "en": "Park Bo-gum", "ja": "パク・ボゴム"},
	}
	if !entityMatchesQuery("박보검", right) {
		t.Error("expected ACCEPT: 박보검 ko label match")
	}

	// 로마자 검색이 en 라벨과 일치(표기차 포함).
	if !entityMatchesQuery("Park Bogum", right) {
		t.Error("expected ACCEPT: Park Bogum vs en label Park Bo-gum")
	}

	// 별칭(개명 전 이름 등)으로도 매칭되어야 한다 — 동일인 보존.
	aliased := &Entity{
		QID:     "Q47666529",
		Labels:  map[string]string{"ko": "이시안", "en": "Lee Si-an"},
		Aliases: map[string][]string{"ko": {"이윤진"}},
	}
	if !entityMatchesQuery("이윤진", aliased) {
		t.Error("expected ACCEPT: 이윤진 matches via ko alias")
	}

	if entityMatchesQuery("박보검", nil) {
		t.Error("nil entity must not match")
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

func TestCleanLanglinkTitle(t *testing.T) {
	cases := map[string]string{
		"Park Bo-gum":      "Park Bo-gum",
		"IVE (音楽グループ)": "IVE",
		"이름 (배우)":        "이름",
		"이름（가수）":         "이름",
		"  パク・ボゴム  ":     "パク・ボゴム",
		"(only paren)":     "(only paren)", // 맨 앞 괄호는 제거 안 함(빈 결과 방지)
	}
	for in, want := range cases {
		if got := cleanLanglinkTitle(in); got != want {
			t.Fatalf("cleanLanglinkTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLanglinkTitles(t *testing.T) {
	e := &Entity{SiteTitles: map[string]string{
		"jawiki":     "パク・ボゴム",
		"zhwiki":     "朴寶劍",
		"enwiki":     "Park Bo-gum",
		"kowiki":     "박보검",       // ko 제외
		"frwiki":     "Park Bo-gum", // 미지원 → 제외
		"ptwiki":     "Park Bo-gum (ator)",
	}}
	got := e.LanglinkTitles()
	want := map[string]string{"ja": "パク・ボゴム", "zh_hant": "朴寶劍", "en": "Park Bo-gum", "pt_br": "Park Bo-gum"}
	if len(got) != len(want) {
		t.Fatalf("LanglinkTitles len = %d (%v), want %d", len(got), got, len(want))
	}
	for loc, w := range want {
		if len(got[loc]) != 1 || got[loc][0] != w {
			t.Fatalf("LanglinkTitles[%s] = %v, want [%s]", loc, got[loc], w)
		}
	}
}

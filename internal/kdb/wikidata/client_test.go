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
		// ★괄호가 이름의 일부인 것을 지킨다 — 권위값 업그레이드가 라벨 전체에 이 함수를
		//   쓰기 시작하면서 드러났다. 종전엔 `f(x)` 가 `f` 가 됐다.
		"f(x)":             "f(x)",
		"Ne(o)mu":          "Ne(o)mu",
		"Girls (Group) Go": "Girls (Group) Go", // 끝이 아니면 안 뗀다
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

// zh-hans 를 **요청만 하고 버리면** 안 된다 — 간체 칸의 근거가 그것뿐인 경우가 있다.
func TestSimplifiedChineseLabelIsNotDropped(t *testing.T) {
	if got := wikidataLangToKDB("zh-hans"); got != "zh" {
		t.Fatalf("zh-hans → %q, 기대 \"zh\" — 요청해 놓고 버리면 raw zh(자체 미상)가 간체 칸에 들어간다", got)
	}
	// 요청 목록과 접기 목록이 어긋나면 안 된다. 요청한 lang 은 전부 접을 수 있어야 한다.
	order := map[string]bool{}
	for _, l := range wikidataLabelOrder {
		order[l] = true
	}
	for _, l := range wikidataLangs {
		if wikidataLangToKDB(l) != "" && !order[l] {
			t.Errorf("%s 를 요청하고 KDB 키로 접을 수도 있는데 순회 목록에 없다 — 조용히 버려진다", l)
		}
	}
	// 간체가 번체보다 먼저 와야 first-write-wins 가 간체를 고른다.
	iHans, iZh := -1, -1
	for i, l := range wikidataLabelOrder {
		if l == "zh-hans" {
			iHans = i
		}
		if l == "zh" {
			iZh = i
		}
	}
	if iHans < 0 || iZh < 0 || iHans > iZh {
		t.Fatalf("zh-hans(%d) 가 zh(%d) 보다 앞이어야 한다", iHans, iZh)
	}
}

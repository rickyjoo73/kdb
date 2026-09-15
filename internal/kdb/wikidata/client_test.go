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

// zh-hans 는 **접지 않고 SourceLabels 에 보존**한다 — 그것이 이 패키지의 계약이다.
//
// ★한 번 잘못 짚었다(2026-09-15). 간체 칸에 번체가 들어간 것을 보고 "zh-hans 를 요청만
// 하고 버린다"고 진단해 wikidataLangToKDB 에 넣었는데, 회귀가 잡았다 — Labels 는 변종을
// 접은 옛 계약이고 Labels["zh"]=raw zh 를 TestFetchPreservesOriginalLocaleAndUnmodifiedLabels
// 가 고정한다. zh-hans 는 버려지지 않고 있었다. 간체가 필요한 쪽(enrich)이 SourceLabels 를
// 직접 본다. 이 시험은 그 계약 — **요청한다 · 원문 그대로 보존한다 · 접지 않는다** — 을 잡는다.
func TestSimplifiedChineseLabelIsPreservedButNotFolded(t *testing.T) {
	if !strings.Contains(strings.Join(wikidataLangs, "|"), "zh-hans") {
		t.Fatal("zh-hans 를 요청하지 않는다 — 간체의 유일한 근거가 그것뿐인 항목이 있다")
	}
	if got := wikidataLangToKDB("zh-hans"); got != "" {
		t.Fatalf("zh-hans 를 KDB 키 %q 로 접었다 — Labels[\"zh\"] 는 raw zh 라는 옛 계약이 깨진다", got)
	}
	// 접기 목록에 zh-hans 가 있으면 first-write-wins 로 raw zh 를 밀어낸다.
	for _, l := range wikidataLabelOrder {
		if l == "zh-hans" {
			t.Fatal("wikidataLabelOrder 에 zh-hans 가 있다 — Labels[\"zh\"] 가 raw zh 가 아니게 된다")
		}
	}
}

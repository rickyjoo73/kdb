package kdbadmin

import (
	"bytes"
	"context"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/rickyjoo73/kdb/internal/kentity"
	"github.com/rickyjoo73/kdb/internal/testdb"
)

func TestCatalogPageLinksPreserveAllFilters(t *testing.T) {
	f := kentity.CatalogFilter{Q: "김 동명 & <script>", Type: "person", Domain: "sports", Status: "candidate", Origin: "tdb", Period: "24h", Sort: "created"}
	u, err := url.Parse(catalogPageURL(f, 50))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("q") != f.Q || q.Get("type") != f.Type || q.Get("domain") != f.Domain || q.Get("status") != f.Status || q.Get("origin") != f.Origin || q.Get("period") != f.Period || q.Get("sort") != f.Sort || q.Get("offset") != "50" {
		t.Fatal(u)
	}
}

func TestEntityCenterUsesRealInventoryAndHidesFailedCounts(t *testing.T) {
	s := renderSmokeServer(t)
	for _, failed := range []bool{false, true} {
		data := map[string]any{"commonEnabled": true, "catalogError": failed, "catalog": kentity.CatalogPage{Overview: kentity.CatalogOverview{Total: 987654, Unassigned: 23}}, "overview": dashboardOverview{}}
		var b bytes.Buffer
		if err := s.tmpl.ExecuteTemplate(&b, "dashboard.html", data); err != nil {
			t.Fatal(err)
		}
		body := b.String()
		if !strings.Contains(body, "고유명사 등록 화면") || !strings.Contains(body, "/admin/kentity") {
			t.Fatal("main entry missing")
		}
		if failed && (strings.Contains(body, "987654") || !strings.Contains(body, "고유명사 현황 조회 실패")) {
			t.Fatal("failure pretends success")
		}
		if !failed && (!strings.Contains(body, "987654") || !strings.Contains(body, "분야 미지정") || !strings.Contains(body, "최근 등록된 고유명사")) {
			t.Fatal("inventory not visible")
		}
	}
}

func TestCommonNavigationNearHome(t *testing.T) {
	t.Setenv("KDB_COMMON_ENTITY_ENABLED", "1")
	items := activeNavItems("/admin/kentity")
	if len(items) < 2 || items[1].Path != "/admin/kentity" || !items[1].Active {
		t.Fatal("new work buried in settings", items)
	}
}

func TestEntityCenterAgainstRestoredInventory(t *testing.T) {
	t.Setenv("KDB_COMMON_ENTITY_ENABLED", "1")
	s := renderSmokeServer(t)
	s.pool = testdb.Restored(t)
	for _, path := range []string{"/admin/", "/admin/kentity", "/admin/kentity?domain=politics&status=candidate", "/admin/kentity?domain=unassigned&origin=kdb&offset=50"} {
		start := time.Now()
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", path, nil)
		if path == "/admin/" {
			s.dashboard(w, r)
		} else {
			s.commonEntityList(w, r)
		}
		body := w.Body.String()
		if w.Code != 200 || !strings.Contains(body, "</html>") || strings.Contains(body, "조회 실패") || strings.Contains(body, "집계 실패") {
			t.Fatal("restored inventory failed", path, w.Code)
		}
		if time.Since(start) > 6*time.Second {
			t.Fatal("inventory rendering exceeded 6s", path)
		}
		t.Logf("%s restored inventory rendered in %s", path, time.Since(start))
	}
}

// 앵커 검수 화면이 실제 스키마에서 그려지는지 고정한다.
//
// ★"판정이 0건"과 "아직 안 봤다"는 다른 말이다. 화면이 둘을 구분하지 못하면
// 오늘 고친 계열(불변식이 '위반 0 ✓'를 찍는 동안 110건이 틀린 근거로 나가고 있었다)을
// 화면으로 옮겨놓는 것이 된다. 그래서 everChecked 를 따로 센다.
func TestAnchorReviewRendersAgainstRestored(t *testing.T) {
	t.Setenv("KDB_COMMON_ENTITY_ENABLED", "1")
	s := renderSmokeServer(t)
	s.pool = testdb.Restored(t)
	for _, path := range []string{"/admin/entities/anchors", "/admin/entities/anchors?verdict=name-element"} {
		w := httptest.NewRecorder()
		s.anchorReview(w, httptest.NewRequest("GET", path, nil))
		body := w.Body.String()
		if w.Code != 200 || !strings.Contains(body, "</html>") {
			t.Fatalf("%s → %d", path, w.Code)
		}
		if strings.Contains(body, "조회 실패") || strings.Contains(body, "집계 실패") {
			t.Fatalf("%s 가 실패 배너를 띄웠다", path)
		}
	}

	// ★판정 표가 없는 환경(0138 이전 DB·새 복원)에서도 열려야 한다.
	//   회귀가 정확히 이것으로 실패했다 — 표가 없자 500 이었다. "아직 안 봤다"를
	//   보여주려고 만든 화면이 하필 그 상태에서만 안 열리면 쓸모가 없다.
	var exists bool
	if err := s.pool.QueryRow(context.Background(),
		`SELECT to_regclass('public.kwave_kdb_anchor_audit') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		w := httptest.NewRecorder()
		s.anchorReview(w, httptest.NewRequest("GET", "/admin/entities/anchors", nil))
		if w.Code != 200 || !strings.Contains(w.Body.String(), "안 봤음") {
			t.Fatalf("판정 표가 없을 때 500 이 나거나 '안 봤음'을 안 보여준다 (%d)", w.Code)
		}
	}
}

// 근거를 겹쳐 읽는 규칙을 고정한다. 여기가 오늘 두 번 갈린 자리다.
//
//	황해     우리 en `The Yellow Sea`(tmdb)  QID `Hwang Hae`  → 다른 대상 · 앵커가 틀림
//	씨스타19  우리 en `Sistar19`              QID `Sistar19`   → 같은 대상 · 유형이 틀림
//
// ★한국어 라벨은 절대 안 본다 — 무용가 `가비` 와 2012년 영화 `가비` 가 같은 한글이다.
//   한 번 그렇게 읽었다가 무용가를 영화로 만들 뻔했다.
// ★우리 en 이 위키데이터에서 왔으면 일치는 순환이다. 증거가 아니다.
func TestAnchorReadingUsesOnlyIndependentEnglish(t *testing.T) {
	for _, c := range []struct{ verdict, en, src, label, want string }{
		{"type-mismatch", "The Yellow Sea", "tmdb", "Hwang Hae", "다른 대상"},
		{"type-mismatch", "Sistar19", "musicbrainz", "Sistar19", "같은 대상"},
		{"type-mismatch", "Sistar19", "wikidata-label", "Sistar19", "판정 보류"},
		{"type-mismatch", "무진", "", "Muzin", "판정 보류"},
		{"type-mismatch", "", "tmdb", "Muzin", "판정 보류"},
		{"name-element", "아무거나", "tmdb", "Mina", "앵커를 뗀다"},
	} {
		got := readAnchor(c.verdict, c.en, c.src, c.label)
		if !strings.Contains(got, c.want) {
			t.Errorf("%q/%q(%s)/%q → %q, %q 를 기대", c.verdict, c.en, c.src, c.label, got, c.want)
		}
	}
}

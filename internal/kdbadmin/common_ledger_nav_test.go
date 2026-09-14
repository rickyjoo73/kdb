package kdbadmin

import (
	"bytes"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/rickyjoo73/kdb/internal/kentity"
)

// 공통 원장이 **메뉴에 없었다**(2026-09-14 발견). /admin/kentity 아래에 목록·상세·채택·
// 잠금·직업·원천매핑·TDB 그림자까지 다 구현돼 있는데 링크가 하나도 없어서 URL 을 아는
// 사람만 들어갔다. 흡수분 537,841건이 화면에서 통째로 안 보였다.
//
// 화면이 없는 기능은 없는 기능과 구별되지 않는다. 다시 빠지지 않게 못박는다.
func TestNavExposesCommonLedger(t *testing.T) {
	want := map[string]string{
		"/admin/kentity":          "공통 원장 목록",
		"/admin/kentity/supply":   "공급 개시 대기",
		"/admin/kentity/mappings": "원천 매핑",
		"/admin/kentity/tdb":      "TDB 그림자",
	}
	got := map[string]bool{}
	for _, it := range navItems() {
		got[it.Path] = true
	}
	for path, what := range want {
		if !got[path] {
			t.Fatalf("메뉴에 %s (%s) 가 없다 — 링크 없는 화면은 없는 화면이다", path, what)
		}
	}
	// 두 원장을 구별할 수 있어야 한다. 종전엔 둘 다 "고유명사 DB" 였다.
	var legacy string
	for _, it := range navItems() {
		if it.Path == "/admin/entities" {
			legacy = it.Title
		}
	}
	if !strings.Contains(legacy, "기존") {
		t.Fatalf("기존 원장 메뉴가 공통 원장과 구별되지 않는다: %q", legacy)
	}
}

// 공급 개시 화면은 **막는 이유를 말해야** 한다. 수치만 있고 이유가 없으면
// 운영자가 무엇을 해야 할지 알 수 없다.
func TestSupplyPageShowsGateReasons(t *testing.T) {
	s := renderSmokeServer(t)
	data := map[string]any{
		"title": "공급 개시 대기",
		"nav":   navItems(),
		"supply": kentity.SupplyPage{
			Candidates: 414587, Openable: 12,
			Gates: []kentity.SupplyGate{
				{Key: "typed", Label: "유형 미상", Why: "무엇인지 모르는 것을 공급하지 않는다(I02)", Blocks: 16476},
				{Key: "source_item", Label: "출처 항목 미지목", Why: "확인할 수 없는 권리·출처 주장", Blocks: 29169},
			},
			Rows: []kentity.SupplyRow{{ID: uuid.New(), KO: "합성 대상", Type: "event", Subtype: "festival_series", Locales: "ja zh-Hans"}},
		},
	}
	var b bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&b, "kentity_supply.html", data); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, must := range []string{
		"414587", "유형 미상", "출처 항목 미지목",
		"무엇인지 모르는 것을 공급하지 않는다(I02)", // 이유가 실려야 한다
		"29169",
		"합성 대상",
		"QID 는 <b>보조</b>", // 앵커 우선순위를 화면이 말한다(I03)
	} {
		if !strings.Contains(out, must) {
			t.Fatalf("화면에 %q 가 없다", must)
		}
	}
}

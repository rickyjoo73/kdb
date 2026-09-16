package kdbadmin

import (
	"os"
	"strings"
	"testing"
)

// ★못 센 것을 0 으로 적지 않는다 (2026-09-16).
//
//	0 은 "일이 없다"는 뜻이고 빈 맵은 "모른다"는 뜻이다. 템플릿이 >0 만 그리므로
//	모르면 배지가 아예 안 뜬다. 이 저장소는 조용한 0 에 여러 번 데였다 —
//	같은 날에만 두 번 더 있었다(거짓 «검색없음» · 묶음 조회 실패를 없음으로).
func TestUncountedIsNotZero(t *testing.T) {
	// pool 이 없으면 nil 이어야 한다. 빈 맵도 0 맵도 아니다.
	if got := navBadgeCounts(nil); got != nil {
		t.Errorf("pool 없이 %v 를 돌려줬다 — 못 센 것이 '없음'으로 보인다", got)
	}
	// 붙이는 쪽도 빈 것을 받으면 손대지 않는다.
	items := []NavItem{{Title: "신규 후보", Path: "/admin/kdb/inbox"}}
	out := applyNavBadges(items, nil)
	if out[0].BadgeCount != 0 {
		t.Error("셈이 없는데 배지를 붙였다")
	}
}

func TestBadgesLandOnTheRightMenus(t *testing.T) {
	items := []NavItem{
		{Title: "② 심사", Section: true},
		{Title: "신규 후보", Path: "/admin/kdb/inbox"},
		{Title: "발굴 큐", Path: "/admin/ondemand/queue"},
		{Title: "없는 화면", Path: "/admin/nowhere"},
	}
	out := applyNavBadges(items, map[string]int{
		"/admin/kdb/inbox":      60,
		"/admin/ondemand/queue": 0,
	})
	if out[0].BadgeCount != 0 {
		t.Error("구역 머리글에 배지가 붙었다")
	}
	if out[1].BadgeCount != 60 {
		t.Errorf("신규 후보 배지=%d, 60 이어야 한다", out[1].BadgeCount)
	}
	if out[2].BadgeCount != 0 {
		t.Errorf("발굴 큐 배지=%d, 0 이어야 한다", out[2].BadgeCount)
	}
	if out[3].BadgeCount != 0 {
		t.Error("셈에 없는 경로에 배지가 붙었다")
	}
}

// ★배지는 **그 화면이 세는 것과 같은 조건**이어야 한다. 눌러 보고 다르면
// 그 뒤로 아무도 안 믿는다. 조건을 베낀 출처를 주석으로 남겼는지 지킨다.
func TestBadgeQueryNamesItsSource(t *testing.T) {
	b, err := os.ReadFile("nav_badges.go")
	if err != nil {
		t.Fatalf("nav_badges.go 를 못 읽었다: %v", err)
	}
	src := string(b)
	for _, h := range []string{
		"handlers_inbox.go", "handlers_ondemand.go", "handlers_entities.go",
		"handlers_anchor_review.go", "corrections.ListPending", "handlers_quality.go",
	} {
		if !strings.Contains(src, h) {
			t.Errorf("%s 를 베꼈다는 표시가 없다 — 배지와 화면이 갈라져도 알 수 없다", h)
		}
	}
	// 배지를 다는 모든 경로는 실제 메뉴에 있어야 한다.
	paths := map[string]bool{}
	for _, it := range navItems() {
		if !it.Section {
			paths[it.Path] = true
		}
	}
	for _, p := range []string{
		"/admin/kdb/inbox", "/admin/ondemand/queue", "/admin/entities/conflicts",
		"/admin/entities/anchors", "/admin/corrections", "/admin/quality/verification",
		"/admin/entities/locale-gaps",
	} {
		if !paths[p] {
			t.Errorf("%s 에 배지를 다는데 그런 메뉴가 없다 — 영영 안 보인다", p)
		}
	}
}

// ★실패도 캐시해야 한다 (2026-09-16).
//
//	안 그러면 DB 가 느릴 때 **모든 관리 화면이** 매번 3초를 기다린다. 배지 하나
//	때문에 콘솔 전체가 느려지는 것은 맞바꿀 만한 거래가 아니다.
func TestFailureIsCachedToo(t *testing.T) {
	b, err := os.ReadFile("nav_badges.go")
	if err != nil {
		t.Fatalf("nav_badges.go 를 못 읽었다: %v", err)
	}
	src := string(b)
	if !strings.Contains(src, "navBadgeVals, navBadgeAt = nil, time.Now()") {
		t.Error("실패를 캐시하지 않는다 — DB 가 느리면 매 분 다시 묻는다")
	}
}

// ★렌더는 **DB 를 기다리면 안 된다** (2026-09-16, 회귀가 잡아냈다).
//
//	처음엔 렌더 안에서 직접 셌다. 대조 실험이 갈랐다:
//	  main            전부 통과
//	  이 변경 얹으면   TestEntityCenterAgainstRestoredInventory 실패
//	                  ("common inventory: timeout: context deadline exceeded")
//	배지 쿼리는 운영 38ms · 복원 DB 374ms 다. 그것을 모든 화면의 임계 경로에
//	얹으면 이미 빠듯한 화면이 넘어간다. 배지 하나로 콘솔을 느리게 할 수는 없다.
func TestRenderNeverWaitsForTheDatabase(t *testing.T) {
	b, err := os.ReadFile("nav_badges.go")
	if err != nil {
		t.Fatalf("nav_badges.go 를 못 읽었다: %v", err)
	}
	src := string(b)
	// 세는 일은 뒤에서 돈다.
	if !strings.Contains(src, "go refreshNavBadges(pool)") {
		t.Error("뒤에서 세지 않는다 — 렌더가 DB 를 기다린다")
	}
	// 읽는 쪽은 요청 컨텍스트를 아예 받지 않는다. 받으면 언젠가 그것으로 기다리게 된다.
	if strings.Contains(src, "func navBadgeCounts(ctx context.Context") {
		t.Error("읽는 쪽이 요청 컨텍스트를 받는다 — 기다리는 코드가 자라날 자리다")
	}
	// 뒤에서 도는 쪽은 요청이 끝나도 살아야 하므로 Background 여야 한다.
	if !strings.Contains(src, "context.WithTimeout(context.Background()") {
		t.Error("뒤에서 도는 셈이 요청 컨텍스트에 매여 있다 — 요청이 끝나면 취소된다")
	}
}

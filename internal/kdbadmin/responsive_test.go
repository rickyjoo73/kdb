package kdbadmin

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// 390px(모바일)와 1440px(데스크톱)에서 화면이 깨지지 않는지는 브라우저 없이 픽셀로 잴 수
// 없다. 대신 **깨지는 원인**은 정적으로 잡을 수 있다. 이 저장소에서 실제로 깨져 있던 것은
// 셋이었다: 스크롤 래퍼 없는 넓은 표 9개, 390px 에서 3열을 강제하는 격자 1개, 모바일에서
// 240px 를 먹고 앉아 본문에 150px 만 남기는 고정 사이드바 1개.
//
// 브라우저 검사를 대체하지는 않는다. 같은 원인이 다시 들어오는 것을 막을 뿐이다.

var (
	reTable     = regexp.MustCompile(`<table\b`)
	reMinW      = regexp.MustCompile(`min-w-\[`)
	reGridCols  = regexp.MustCompile(`class="([^"]*\bgrid-cols-(\d+)\b[^"]*)"`)
	reResponsive = regexp.MustCompile(`(sm|md|lg|xl|2xl):grid-cols-`)
	reInputTag  = regexp.MustCompile(`<(input|select|textarea)\b[^>]*$`)
)

func templateFiles(t *testing.T) map[string]string {
	t.Helper()
	paths, err := filepath.Glob("templates/*.html")
	if err != nil || len(paths) == 0 {
		t.Fatal("템플릿을 못 찾음", err)
	}
	out := map[string]string{}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		out[filepath.Base(p)] = string(b)
	}
	return out
}

// lineOf — 바이트 위치를 줄 번호로. 실패 메시지를 클릭 가능하게 만든다.
func lineOf(s string, pos int) int { return strings.Count(s[:pos], "\n") + 1 }

// hasScrollAncestor — 넓은 요소 앞쪽에 가로 스크롤 컨테이너가 있는가.
// 조상 관계를 정확히 파싱하는 대신 앞 400자를 본다. 이 저장소의 템플릿은 표 바로 위에
// 래퍼를 두는 관례라 이걸로 충분하고, 놓치는 쪽보다 과하게 잡는 쪽이 낫다.
func hasScrollAncestor(s string, pos int) bool {
	back := s[max(0, pos-400):pos]
	return strings.Contains(back, "overflow-x-auto") || strings.Contains(back, "overflow-auto")
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// TestWideContentScrollsInsteadOfOverflowing — 390px 에서 페이지 전체가 가로로 밀리지 않는다.
func TestWideContentScrollsInsteadOfOverflowing(t *testing.T) {
	for name, s := range templateFiles(t) {
		for _, m := range reTable.FindAllStringIndex(s, -1) {
			if !hasScrollAncestor(s, m[0]) {
				t.Errorf("templates/%s:%d 표에 가로 스크롤 래퍼가 없다 — 390px 에서 페이지 전체가 밀린다", name, lineOf(s, m[0]))
			}
		}
		for _, m := range reMinW.FindAllStringIndex(s, -1) {
			// 폼 입력의 min-w 는 flex-wrap 으로 줄바꿈되므로 대상이 아니다.
			if reInputTag.MatchString(s[max(0, m[0]-300):m[0]]) {
				continue
			}
			if !hasScrollAncestor(s, m[0]) {
				t.Errorf("templates/%s:%d 최소 폭이 지정된 블록에 스크롤 래퍼가 없다", name, lineOf(s, m[0]))
			}
		}
	}
}

// TestGridsCollapseOnNarrowScreens — 390px 에서 3열 이상을 강제하지 않는다.
func TestGridsCollapseOnNarrowScreens(t *testing.T) {
	for name, s := range templateFiles(t) {
		for _, m := range reGridCols.FindAllStringSubmatchIndex(s, -1) {
			class := s[m[2]:m[3]]
			n, _ := strconv.Atoi(s[m[4]:m[5]])
			if n < 3 || reResponsive.MatchString(class) {
				continue
			}
			t.Errorf("templates/%s:%d grid-cols-%d 에 반응형 대응이 없다 — 390px 에서 셀이 눌린다", name, lineOf(s, m[0]), n)
		}
	}
}

// TestStandaloneLayoutsAreResponsive — partials 레이아웃을 쓰지 않고 자체 <html> 을 가진
// 템플릿은 viewport meta 와 좁은 화면 대응을 스스로 해야 한다. entity_homonyms.html 이
// 그런 경우인데, 고정 폭 사이드바가 390px 에서 본문을 150px 로 눌러놨었다.
func TestStandaloneLayoutsAreResponsive(t *testing.T) {
	for name, s := range templateFiles(t) {
		if !strings.Contains(strings.ToLower(s), "<!doctype") {
			continue
		}
		if !strings.Contains(s, `name="viewport"`) {
			t.Errorf("templates/%s: 자체 레이아웃인데 viewport meta 가 없다", name)
		}
		for _, m := range regexp.MustCompile(`<aside\b[^>]*class="([^"]*)"`).FindAllStringSubmatchIndex(s, -1) {
			class := s[m[2]:m[3]]
			if strings.Contains(class, "w-") && !strings.Contains(class, "hidden") && !strings.Contains(class, "md:") {
				t.Errorf("templates/%s:%d 고정 폭 사이드바가 좁은 화면에서 접히지 않는다", name, lineOf(s, m[0]))
			}
		}
	}
}

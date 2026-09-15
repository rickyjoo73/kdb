package kdb

import (
	"context"
	"testing"
)

// ★관측의 출처는 **가져온 페이지**다 (2026-09-15).
//
//	종전엔 화이트리스트 매체 이름을 적었다. 검색이 그 도메인 밖 페이지를 주면
//	유튜브 한 페이지가 브라질 매체 5곳으로 남았다 — 매체합의가 조작된다.
//	실측 683묶음 · 관측 2,426건 · 대상 168건, 그중 151건이 원장 칸까지 갔다.
func TestObservationSourceIsThePageWeActuallyRead(t *testing.T) {
	cases := []struct {
		link, fallback, want string
	}{
		{"https://www.instagram.com/p/abc/", "daebak.tokyo", "instagram.com"},
		{"https://store.steampowered.com/app/1", "koari.net", "store.steampowered.com"},
		{"https://m.blog.naver.com/x/1", "koari.net", "m.blog.naver.com"},
		// 피드 자신의 항목이면 결과가 같다 — 달라지는 것은 밖에서 왔을 때뿐이다.
		{"https://koari.net/news/1", "koari.net", "koari.net"},
		// 링크를 못 읽으면 폴백. 출처를 지어내지 않는다.
		{"", "koari.net", "koari.net"},
		{"::not a url::", "koari.net", "koari.net"},
	}
	for _, c := range cases {
		if got := observationSource(c.link, c.fallback); got != c.want {
			t.Fatalf("observationSource(%q,%q) = %q, want %q", c.link, c.fallback, got, c.want)
		}
	}
}

// ★일반 기사 수집은 **명시해야 켜진다** (2026-09-15 운영자 지시).
//
//	"더이상 일반 한국어 기사를 가져오는 경우 등 사용하지 말자."
//	env 하나가 빠졌다고 수집이 되살아나면 안 되므로, 아무 설정이 없을 때
//	PollerTick 은 pool 을 **건드리지 않고** 돌아와야 한다(nil pool 이 그 증거다).
func TestRSSPollingStaysOffUnlessExplicitlyEnabled(t *testing.T) {
	t.Setenv("KDB_DISABLE_RSS_POLLING", "")
	t.Setenv("KDB_ENABLE_RSS_POLLING", "")
	PollerTick(context.Background(), nil) // nil pool — 진입하면 panic 한다
}

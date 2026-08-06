package kdb

import (
	"testing"

	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
)

// 전부 실제 QID 와 실제 레이블이다. 별칭을 인정했다가 4건을 오염시킨 뒤 만든 회귀 테스트다.
func TestWikidataAnchorMatches(t *testing.T) {
	cases := []struct {
		name string
		ko   string
		ent  *wikidata.Entity
		want bool
	}{
		{
			name: "ko 레이블 일치 — 통과",
			ko:   "구성환",
			ent:  &wikidata.Entity{Labels: map[string]string{"ko": "구성환"}},
			want: true,
		},
		{
			name: "표기차 흡수(공백·중점) — 통과",
			ko:   "옥씨부인전",
			ent:  &wikidata.Entity{Labels: map[string]string{"ko": "옥씨 부인전"}},
			want: true,
		},
		{
			name: "ko 레이블 없음 — 판별 불가라 막지 않는다",
			ko:   "어떤작품",
			ent:  &wikidata.Entity{Labels: map[string]string{"en": "Some Work"}},
			want: true,
		},
		// ↓ 여기부터가 별칭 분기를 지운 이유. 넷 다 예전엔 통과해서 값을 오염시켰다.
		{
			name: "별칭만 일치 — 스트리밍 서비스가 그룹으로 들어왔다(Q87730005)",
			ko:   "바이브",
			ent: &wikidata.Entity{
				Labels:  map[string]string{"ko": "네이버 VIBE", "en": "Naver VIBE"},
				Aliases: map[string][]string{"ko": {"바이브", "VIBE"}},
			},
			want: false,
		},
		{
			name: "별칭만 일치 — 이스포츠 구단이 SEVENTEEN DK 로 들어왔다(Q85976326)",
			ko:   "DK",
			ent: &wikidata.Entity{
				Labels:  map[string]string{"ko": "Dplus Kia", "en": "Dplus Kia"},
				Aliases: map[string][]string{"ko": {"DK", "담원"}},
			},
			want: false,
		},
		{
			name: "별칭만 일치 — DAY6 제이가 ENHYPEN 제이로 들어왔다(Q26220991)",
			ko:   "제이",
			ent: &wikidata.Entity{
				Labels:  map[string]string{"ko": "Jae", "en": "Jae Park"},
				Aliases: map[string][]string{"ko": {"제이", "박제형"}},
			},
			want: false,
		},
		{
			name: "별칭만 일치 — 본명 항목(Q114690838)",
			ko:   "라미",
			ent: &wikidata.Entity{
				Labels:  map[string]string{"ko": "김성경"},
				Aliases: map[string][]string{"ko": {"라미"}},
			},
			want: false,
		},
		{
			name: "정본 비어있음 — 판별 불가라 채택하지 않는다",
			ko:   "",
			ent:  &wikidata.Entity{Labels: map[string]string{"ko": "무엇"}},
			want: false,
		},
		{
			name: "엔티티 nil",
			ko:   "무엇",
			ent:  nil,
			want: false,
		},
	}
	for _, c := range cases {
		if got := wikidataAnchorMatches(c.ko, c.ent); got != c.want {
			t.Errorf("%s: wikidataAnchorMatches(%q) = %v, want %v", c.name, c.ko, got, c.want)
		}
	}
}

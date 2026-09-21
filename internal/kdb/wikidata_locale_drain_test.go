package kdb

import (
	"strings"
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
		// 옛 사례는 전부 사람·짧은 활동명이다 — person 으로 돌려 옛 기대를 그대로 지킨다.
		if got := wikidataAnchorMatches(c.ko, "person", c.ent); got != c.want {
			t.Errorf("%s: wikidataAnchorMatches(%q) = %v, want %v", c.name, c.ko, got, c.want)
		}
	}
}

// TestRefillClauseCoversEveryLocaleItUpdates — SELECT 와 UPDATE 의 대칭을 잠근다.
//
// 이 드레인이 실제로 밟은 버그다(2026-09-16). UPDATE 는 wikidataLocaleTargets 8개를
// 전부 덮는데 SELECT 의 재선택 조건은 ja/zh/zh_hant 세 개의 _source 만 봤다. 그래서
// canonical_en 이 gtranslate 로 채워진 행은 **빈칸도 아니고 감시 대상도 아니라서**
// 영영 다시 집히지 않았다 — 앵커를 새로 붙여도 영문 표기가 기계번역인 채로 남는다.
// 실측 496건, 조건을 맞추니 대상이 1,932 → 4,631 로 늘었다.
//
// 두 목록이 다시 갈라지면 조용히 같은 일이 난다(아무도 실패하지 않고 그냥 안 채워진다).
// 그래서 "조용함"을 테스트로 바꾼다.
func TestRefillClauseCoversEveryLocaleItUpdates(t *testing.T) {
	clause := wikidataLocaleRefillClause("e", "$2")
	for _, loc := range wikidataLocaleTargets {
		if !strings.Contains(clause, "COALESCE(e.canonical_"+loc+",'')=''") {
			t.Errorf("로케일 %q 의 빈칸 조건이 SELECT 에 없다 — 빈칸인 행이 재선택되지 않는다", loc)
		}
		if !strings.Contains(clause, "COALESCE(e.canonical_"+loc+"_source,'') = ANY($2)") {
			t.Errorf("로케일 %q 의 출처 조건이 SELECT 에 없다 — 기계값이 영영 갱신되지 않는다 "+
				"(en 이 정확히 이 이유로 496건 방치됐다)", loc)
		}
	}
	// 파라미터를 그대로 흘려보내는지도 본다. 상수로 박으면 wikidataOverwritableSources
	// 와 갈라진다.
	if strings.Contains(clause, "'gtranslate'") {
		t.Error("출처 목록을 SQL 에 상수로 박았다 — wikidataOverwritableSources 와 갈라진다")
	}
}

// ★09-21 — 별칭 확장을 거둬들인 뒤의 규칙. 괄호 앞부분만 넓히고(비사람), 우리 별칭은 쓰지 않는다.
func TestWikidataAnchorMatches_괄호앞만(t *testing.T) {
	cases := []struct {
		name, ko, etype, label string
		want                   bool
	}{
		{"괄호 앞부분(펜타곤)", "펜타곤(PENTAGON)", "group", "펜타곤", true},
		{"괄호 앞부분(DAY6)", "DAY6(데이식스)", "group", "DAY6", true},
		// ★거둬들인 이유 — 별칭으로만 맞는 것은 이제 통과하지 않는다(바이브→네이버 VIBE).
		{"별칭으로만 맞는 그룹은 막는다(바이브)", "바이브", "group", "네이버 VIBE", false},
		{"사람은 괄호도 넓히지 않는다", "제이(DAY6)", "person", "제이", false},
		{"시즌 괄호는 떼지 않는다", "하트시그널(시즌2)", "show", "하트시그널", false},
		{"메모 괄호는 떼어도 된다(바깥이 진짜 제목)", "천천히 강렬하게(가제)", "drama", "천천히 강렬하게", true},
		{"연도 괄호도", "눈물이 더 가까운 사람 (2026)", "drama", "눈물이 더 가까운 사람", true},
	}
	for _, c := range cases {
		ent := &wikidata.Entity{Labels: map[string]string{"ko": c.label}}
		if got := wikidataAnchorMatches(c.ko, c.etype, ent); got != c.want {
			t.Errorf("%s: got %v, want %v (names=%v)", c.name, got, c.want, anchorCheckNames(c.ko, c.etype))
		}
	}
}

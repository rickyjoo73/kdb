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
		if got := wikidataAnchorMatches(c.ko, "person", nil, c.ent); got != c.want {
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

// ★43회차 측정 — 이미 붙은 **옳은** 앵커를 정본 표기 차이로 버리던 것. 그룹·기관·채널은
// 우리 별칭·괄호 앞부분으로도 맞춘다. 사람·캐릭터는 옛 기준(정본만)을 지킨다.
func TestWikidataAnchorMatches_우리_별칭과_괄호앞(t *testing.T) {
	cases := []struct {
		name    string
		ko      string
		etype   string
		aliases []string
		label   string
		want    bool
	}{
		{"영문 정본 · 한글 별칭(Stray Kids)", "Stray Kids", "group", []string{"스트레이 키즈"}, "스트레이 키즈", true},
		{"한글 정본 · 영문 라벨(엔시티)", "엔시티", "group", []string{"NCT"}, "NCT", true},
		{"괄호 앞부분(펜타곤)", "펜타곤(PENTAGON)", "group", nil, "펜타곤", true},
		{"괄호 앞부분(DAY6)", "DAY6(데이식스)", "group", []string{"데이식스"}, "데이식스", true},
		{"기관 정식명(국방부)", "국방부", "government_body", []string{"대한민국 국방부"}, "대한민국 국방부", true},
		// 사람·캐릭터는 넓히지 않는다.
		{"사람은 별칭으로 넓히지 않는다(카리나)", "Karina", "person", []string{"카리나"}, "카리나", false},
		{"캐릭터도(타잔)", "타잔", "character", []string{"이승용"}, "이승용", false},
		// 시즌 표지를 떨군 별칭은 상위 시리즈 라벨과 맞으면 안 된다.
		{"시즌 표지를 떨군 별칭은 버린다", "흑백요리사: 요리 계급 전쟁2", "show", []string{"흑백요리사"}, "흑백요리사", false},
		{"괄호가 시즌 표지면 괄호 앞을 쓰지 않는다", "하트시그널(시즌2)", "show", nil, "하트시그널", false},
		{"무관한 라벨은 여전히 막는다", "웨이브", "channel_outlet", []string{"WAVVE"}, "네이버 VIBE", false},
	}
	for _, c := range cases {
		ent := &wikidata.Entity{Labels: map[string]string{"ko": c.label}}
		if got := wikidataAnchorMatches(c.ko, c.etype, c.aliases, ent); got != c.want {
			t.Errorf("%s: got %v, want %v (names=%v)", c.name, got, c.want, anchorCheckNames(c.ko, c.etype, c.aliases))
		}
	}
}

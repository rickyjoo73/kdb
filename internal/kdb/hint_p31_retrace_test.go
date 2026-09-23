package kdb

import (
	"testing"

	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
)

// TestHintP31DecideMovesOnlyWhenAllThreeAgree — 소비자 힌트를 뒤집는 것은 **셋이 모두
// 맞을 때뿐**이다: P31 이 한 유형만 가리키고, 저장 유형과 다르고, 앵커가 이 대상이다.
func TestHintP31DecideMovesOnlyWhenAllThreeAgree(t *testing.T) {
	ent := func(title string, p31 ...string) *wikidata.Entity {
		return &wikidata.Entity{
			InstanceOf: p31,
			SiteTitles: map[string]string{"kowiki": title},
			Labels:     map[string]string{}, SourceLabels: map[string]string{},
			Aliases: map[string][]string{},
		}
	}
	for _, c := range []struct {
		name, stored, ko string
		e                *wikidata.Entity
		want, why        string
	}{
		// 카카오가 event_tour 로 굳었던 모양 — 기업 클래스만 가진 앵커, 제목 일치.
		{"옮긴다", "event_tour", "카카오", ent("카카오 (기업)", "Q6881511"), "company", hintP31Move},
		// 가수가 song_album 으로 굳은 모양(박학기) — 사람 앵커, 제목 일치.
		{"사람으로", "song_album", "박학기", ent("박학기", "Q5"), "person", hintP31Move},
		// ★틀린 앵커: 구단에 사람 QID. 제목이 다르면 옮기지 않는다.
		{"틀린앵커", "sports_team", "두산 베어스", ent("홍길동", "Q5"), "", hintP31Identity},
		// 제목이 없고 라벨도 없으면 확인 못 함 — 옮기지 않는다.
		{"제목없음", "event_tour", "카카오", ent("", "Q6881511"), "", hintP31Identity},
		// P31 도 같은 말.
		{"같음", "company", "카카오", ent("카카오", "Q6881511"), "", hintP31Same},
		// business 는 agency/company 를 못 가른다.
		{"못가름", "brand_place", "삼성전자", ent("삼성전자", "Q4830453"), "", hintP31Ambiguous},
		// organization 만 있으면 결정 못 함.
		{"일반클래스", "brand_place", "네이버", ent("네이버", "Q43229"), "", hintP31NoClass},
		// ★person 에서는 꺼내지 않는다 — 동명 작품 앵커가 흔하다.
		{"사람유지", "person", "인사이더", ent("인사이더", "Q11424"), "", hintP31PersonHeld},
		// 이름 항목은 어떤 유형의 근거도 아니다.
		{"이름항목", "event_tour", "경남", ent("경남", "Q3409032", "Q6881511"), "", hintP31NameElement},
	} {
		got, why := hintP31Decide(c.stored, c.ko, c.e)
		if got != c.want || why != c.why {
			t.Errorf("%s: (%q,%q), 기대 (%q,%q)", c.name, got, why, c.want, c.why)
		}
	}
}

// 제목이 없어도 ko 라벨이나 별칭이 맞으면 이 대상이다.
func TestAnchorIsThisNameUsesLabelAndAlias(t *testing.T) {
	e := &wikidata.Entity{
		SiteTitles: map[string]string{}, SourceLabels: map[string]string{},
		Labels:  map[string]string{"ko": "케이 뱅크"},
		Aliases: map[string][]string{"ko": {"K뱅크"}},
	}
	if !anchorIsThisName(e, "케이뱅크") {
		t.Error("라벨 공백차를 흡수하지 못했다")
	}
	if !anchorIsThisName(e, "K뱅크") {
		t.Error("별칭을 보지 않았다")
	}
	if anchorIsThisName(e, "카카오뱅크") {
		t.Error("다른 이름을 같다고 했다")
	}
}

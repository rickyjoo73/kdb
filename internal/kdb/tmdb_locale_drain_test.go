package kdb

// tmdb-locale 선정 조건 회귀 테스트.
//
// ★이 파일이 존재하는 이유(08-15 실측): 손으로 적은 WHERE 가 `ja/zh/zh_hant` 3개만
// 검사하는 동안 기록은 7개 로케일에 하고 있었다. 그래서 ja/zh/zh_hant 가 모두 상위
// 출처로 차 있고 vi/es/id/pt_br 만 비어 있는 작품은 **영영 선택되지 않았다** — 실측
// 672건. `유부녀 킬러` 가 TMDb 에 vi(Sát Thủ Nội Trợ)·es·id 가 있는데도 못 받은 게
// 이 구멍이다.
//
// 선정 조건과 기록 대상이 다시 갈라지면 여기서 깨진다.

import (
	"strings"
	"testing"
)

func TestTMDbLocaleFillCondCoversEveryWriteTarget(t *testing.T) {
	cond := tmdbLocaleFillCond("ANY($2)", "ANY($3)")

	// 기록 대상 전부가 선정 조건에 있어야 한다 — 빈칸 조건과 출처 조건 양쪽으로.
	for _, loc := range tmdbLocaleTargets {
		if !strings.Contains(cond, "COALESCE(e.canonical_"+loc+",'')=''") {
			t.Errorf("선정 조건에 %s 빈칸 검사가 없다 — 이 로케일만 비어 있는 작품은 영영 선택되지 않는다", loc)
		}
		if !strings.Contains(cond, "COALESCE(e.canonical_"+loc+"_source,'')=ANY($2)") {
			t.Errorf("선정 조건에 %s 출처 검사가 없다 — 저신뢰 출처를 덮을 기회가 없다", loc)
		}
	}

	// en 은 별도 파라미터(tmdbEnOverwritable)를 쓴다.
	if !strings.Contains(cond, "COALESCE(e.canonical_en,'')=''") ||
		!strings.Contains(cond, "COALESCE(e.canonical_en_source,'')=ANY($3)") {
		t.Error("선정 조건이 en 을 다루지 않는다 — en 재동기 대상이 선택되지 않는다")
	}
}

func TestTMDbLocaleGapOrderCoversEveryTarget(t *testing.T) {
	order := tmdbLocaleGapOrder()
	for _, loc := range tmdbLocaleTargets {
		if !strings.Contains(order, "canonical_"+loc) {
			t.Errorf("정렬식에 %s 가 빠졌다 — 빈칸 많은 순 정렬이 실제 빈칸 수를 반영하지 못한다", loc)
		}
	}
}

// tmdbEnOverwritable 은 tmdbOverwritableSources 를 **복사**해야 한다. append 가
// 밑배열을 공유하면 en 쪽 'tmdb' 추가가 다른 로케일 규칙까지 오염시킨다 — 그러면
// wikidata·media 로 채운 ja/zh 를 tmdb 가 덮을 수 있게 된다.
func TestTMDbEnOverwritableDoesNotLeakIntoLocaleRule(t *testing.T) {
	for _, s := range tmdbOverwritableSources {
		if s == "tmdb" {
			t.Fatal("tmdbOverwritableSources 에 'tmdb' 가 새어 들어갔다 — 로케일 칸을 자기 소스로 덮게 된다")
		}
	}
	if !containsString(tmdbEnOverwritable, "tmdb") {
		t.Error("en 규칙에 'tmdb' 가 없다 — 자기 소스가 정정한 값을 되읽지 못한다")
	}
	for _, s := range tmdbOverwritableSources {
		if !containsString(tmdbEnOverwritable, s) {
			t.Errorf("en 규칙이 %q 를 잃었다 — 저신뢰 출처를 덮을 기회가 준다", s)
		}
	}
}

func TestTMDbEnAcceptable(t *testing.T) {
	cases := []struct {
		in   string
		want bool
		why  string
	}{
		{"A Bona Fide Killer", true, "정상 영문 제목"},
		{"Sh**ting Stars", true, "기호가 섞여도 라틴 문자가 있으면 통과"},
		{"OK! Madam", true, "구두점 포함"},
		{"고딩형사", false, "한글 원제 되비침 — en 칸에 들어가면 안 된다"},
		{"犯罪者", false, "한자 — 오부착 앵커 유래"},
		{"人妻キラー", false, "가나"},
		{"", false, "빈 문자열"},
		{"2026", false, "라틴 문자가 하나도 없다"},
	}
	for _, c := range cases {
		if got := tmdbEnAcceptable(c.in); got != c.want {
			t.Errorf("tmdbEnAcceptable(%q) = %v, want %v (%s)", c.in, got, c.want, c.why)
		}
	}
}

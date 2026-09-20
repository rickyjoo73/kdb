package kdb

import (
	"strings"
	"testing"
	"time"
)

// ★이 시험이 지키는 것 (2026-09-20).
//
//	하루에 같은 자리에서 세 번 데였다. 전부 「레인이 무엇을 했는가」를 몰라서다:
//	  zhwiki 가 389건 채웠다고 말하는 동안 원장은 0건 바뀌었다.
//	이 원장은 scanned 와 applied 를 **절대 섞지 않는다** — 그게 이 파일의 전부다.

func TestLaneRun_선정과_적용은_다른_수다(t *testing.T) {
	r := NewLaneRun("zhwiki", false)
	r.Scan(389)
	for i := 0; i < 389; i++ {
		r.Skip("라틴→라틴이라 바꿀 근거 없음")
	}
	if r.Scanned != 389 {
		t.Errorf("scanned=%d, want 389", r.Scanned)
	}
	if r.Applied != 0 {
		t.Errorf("applied=%d — 한 건도 안 썼는데 쓴 것으로 세면 안 된다", r.Applied)
	}
	if !r.SilentZero() {
		t.Error("뽑았는데 0건을 썼다 — 조용한 0건으로 판정해야 한다")
	}
	if !strings.Contains(r.Summary(), "조용한0건") {
		t.Errorf("요약이 그 사실을 말하지 않는다: %s", r.Summary())
	}
}

func TestLaneRun_사유를_센다(t *testing.T) {
	r := NewLaneRun("org-anchor", true)
	r.Scan(25)
	for i := 0; i < 18; i++ {
		r.Skip("no-hit")
	}
	for i := 0; i < 3; i++ {
		r.Skip("name-mismatch")
	}
	r.Skip("")
	if r.Reasons["no-hit"] != 18 || r.Reasons["name-mismatch"] != 3 {
		t.Errorf("사유 계수가 틀렸다: %+v", r.Reasons)
	}
	if r.Reasons["(사유없음)"] != 1 {
		t.Error("빈 사유도 버리지 않고 이름을 붙여 센다 — 뭉뚱그린 0건을 막는 것이 목적이다")
	}
	if r.Skipped != 22 {
		t.Errorf("skipped=%d, want 22", r.Skipped)
	}
	s := r.Summary()
	if !strings.Contains(s, "no-hit") || !strings.Contains(s, "[dry]") {
		t.Errorf("요약에 사유·dry 표시가 없다: %s", s)
	}
}

func TestLaneRun_정상_수확(t *testing.T) {
	r := NewLaneRun("itunes", false)
	r.Scan(10)
	for i := 0; i < 7; i++ {
		r.Apply()
	}
	for i := 0; i < 3; i++ {
		r.Skip("문자셋 위반")
	}
	if r.SilentZero() {
		t.Error("7건을 썼는데 조용한 0건으로 판정했다")
	}
	if strings.Contains(r.Summary(), "조용한0건") {
		t.Errorf("요약이 틀렸다: %s", r.Summary())
	}
}

func TestIsSilentZero_한_회차만으로는_판정하지_않는다(t *testing.T) {
	// 대상이 없어서 0건인 회차는 정상이다. 여러 회차 동안 뽑기만 하는 것이 문제다.
	if IsSilentZero(1, 5, 0, 3) {
		t.Error("한 회차만 보고 조용한 0건이라 하면 안 된다 — 대상이 없을 수 있다")
	}
	if !IsSilentZero(5, 40, 0, 3) {
		t.Error("5회차 동안 40건을 뽑고 0건을 썼으면 조용한 0건이다")
	}
	if IsSilentZero(5, 40, 1, 3) {
		t.Error("한 건이라도 썼으면 조용한 0건이 아니다")
	}
	if IsSilentZero(5, 0, 0, 3) {
		t.Error("뽑은 것이 없으면 조용한 0건이 아니라 대상이 없는 것이다")
	}
	if !IsSilentZero(3, 1, 0, 0) {
		t.Error("minRuns 가 0 이면 기본값 3 을 써야 한다")
	}
}

func TestLaneRun_nil은_터지지_않는다(t *testing.T) {
	// 계측기가 본체를 죽이는 것이 가장 나쁘다.
	var r *LaneRun
	r.Scan(3)
	r.Apply()
	r.Skip("x")
	if r.SilentZero() || r.Summary() != "" {
		t.Error("nil 원장이 값을 만들어내면 안 된다")
	}
}

func TestLaneRun_요약의_사유는_많은_것부터(t *testing.T) {
	r := NewLaneRun("x", false)
	r.Scan(10)
	r.Skip("적음")
	for i := 0; i < 5; i++ {
		r.Skip("많음")
	}
	top := r.topReasons(1)
	if len(top) != 1 || top["많음"] != 5 {
		t.Errorf("가장 많은 사유를 먼저 보여야 한다: %+v", top)
	}
}

// ★연속 판정 — 한두 회차 0건은 병이 아니다. 이 경계가 틀리면 경보가 늑대소년이 된다.
func TestCountSilentStreak(t *testing.T) {
	cases := []struct {
		name string
		runs []LaneRunCounts
		want int
	}{
		{"연속 3회 뽑기만", []LaneRunCounts{{429, 0}, {430, 0}, {425, 0}}, 3},
		{"최근에 한 건 썼으면 0", []LaneRunCounts{{429, 1}, {430, 0}, {425, 0}}, 0},
		{"중간에 쓴 회차에서 멈춘다", []LaneRunCounts{{429, 0}, {430, 0}, {425, 7}, {400, 0}}, 2},
		{"뽑은 것이 없는 회차에서 멈춘다", []LaneRunCounts{{429, 0}, {0, 0}, {425, 0}}, 1},
		{"기록 없음", nil, 0},
		{"대상이 계속 없었을 뿐", []LaneRunCounts{{0, 0}, {0, 0}, {0, 0}}, 0},
	}
	for _, c := range cases {
		if got := CountSilentStreak(c.runs); got != c.want {
			t.Errorf("%s: CountSilentStreak = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestSilentStreak_경보_임계(t *testing.T) {
	// 임계가 1 이면 「대상이 잠깐 없던 회차」마다 울린다. 3 이어야 한다.
	if silentStreakAlert < 3 {
		t.Errorf("silentStreakAlert=%d — 너무 낮으면 경보가 늑대소년이 된다", silentStreakAlert)
	}
	if silentStreakWindow < silentStreakAlert {
		t.Error("보는 창이 임계보다 작으면 연속을 셀 수 없다")
	}
}

func TestDemandGapInterval_시간단위로_출렁이지_않는다(t *testing.T) {
	// 매 레인 회차마다 다시 재면 수요는 안 변하는데 부하만 는다.
	if demandGapInterval < time.Hour {
		t.Errorf("demandGapInterval=%v — 너무 짧으면 레인마다 전수 대조를 돌린다", demandGapInterval)
	}
}

// ★배선 목록은 원장에 적는 이름과 **같아야** 한다. 이름이 어긋나면 「안 도는 레인」
// 판정이 영영 거짓 양성을 낸다 — 19회차에 손으로 만든 목록을 코드로 옮긴 것이므로
// 그 목록이 실제 배선과 갈라지는 것을 여기서 막는다.
func TestWiredLanes_모양이_옳다(t *testing.T) {
	if len(WiredLanes) < 10 {
		t.Errorf("배선 목록이 %d개다 — 실제 배선(13개)보다 적으면 안 도는 레인을 놓친다", len(WiredLanes))
	}
	seen := map[string]bool{}
	for _, l := range WiredLanes {
		if l == "" {
			t.Error("빈 이름이 있다")
		}
		if seen[l] {
			t.Errorf("%q 가 두 번 있다", l)
		}
		seen[l] = true
	}
	for _, must := range []string{"zhwiki-title", "localfill", "mdl-works", "kmdb"} {
		if !seen[must] {
			t.Errorf("%q 가 배선 목록에 없다", must)
		}
	}
}

// ★헬스 신호는 합이 아니라 **연속**으로 판정한다 (2026-09-21, 24회차 실측).
//
//	합으로 보던 때 24시간 실측에서 양쪽으로 다 틀렸다. 아래 두 경우가 그 실측이다.
func TestSilentLanesFromRuns_합으로는_틀렸던_두_경우(t *testing.T) {
	byLane := map[string][]LaneRunCounts{
		// 거짓 양성: 90회차 중 한 회차만 1행을 뽑아 못 채웠다. 1분 티커라 회차 수만
		// 쌓였고 합으로 보면 scanned=1·applied=0 이라 「병」이 됐다.
		"tmdb-candidates": {{0, 0}, {0, 0}, {0, 0}, {1, 0}, {0, 0}},
		// 거짓 음성: 연속 다섯 회차 같은 10행을 뽑아 매번 0건. 창 앞쪽의 한 회차가
		// 2건을 써서 합은 applied=2 — 건강해 보였다.
		"localfill": {{10, 0}, {10, 0}, {10, 0}, {10, 0}, {10, 0}, {10, 2}},
		// 진짜 신호: 뽑기만 하는 것이 계속된다.
		"zhwiki-title": {{429, 0}, {430, 0}, {425, 0}},
		// 건강: 가장 최근에 썼다.
		"itunes-songs": {{17, 14}, {17, 0}, {17, 0}, {17, 0}},
	}
	got := silentLanesFromRuns(byLane, silentStreakAlert)
	want := []string{"localfill", "zhwiki-title"}
	if len(got) != len(want) {
		t.Fatalf("신호 = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("신호 = %v, want %v (이름순으로 고정한다)", got, want)
		}
	}
}

func TestSilentLanesFromRuns_기록이_없으면_조용하다(t *testing.T) {
	if got := silentLanesFromRuns(nil, 3); len(got) != 0 {
		t.Errorf("기록이 없는데 신호를 만들었다: %v", got)
	}
	// 임계가 0 이면 기본값을 쓴다 — 0 을 그대로 쓰면 모든 레인이 신호가 된다.
	byLane := map[string][]LaneRunCounts{"x": {{0, 0}}}
	if got := silentLanesFromRuns(byLane, 0); len(got) != 0 {
		t.Errorf("임계 0 을 그대로 쓰면 안 된다: %v", got)
	}
}

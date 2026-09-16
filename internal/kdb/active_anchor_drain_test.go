package kdb

import (
	"os"
	"strings"
	"testing"
)

// ★두 레인이 **같은 판정**을 써야 한다.
//
//	후보 레인과 active 레인이 각자 검색·판정 로직을 들면, 검색어 넓히기·상위 5건
//	규칙·오류와 «없음» 가르기 중 하나만 한쪽에서 바뀌어도 같은 낱말이 상태에 따라
//	다른 답을 받는다. 이 저장소가 "네 곳이 같은 명제를 들고 있다"고 경고한 계열이다.
func TestBothAnchorLanesShareTheSameLookup(t *testing.T) {
	b, err := os.ReadFile("org_anchor_drain.go")
	if err != nil {
		t.Fatalf("org_anchor_drain.go 를 못 읽었다: %v", err)
	}
	src := string(b)
	if !strings.Contains(src, "findAnchorQID(ctx, cl, it.ko, it.typ)") {
		t.Error("후보 레인이 findAnchorQID 를 안 쓴다 — 검색 로직이 둘로 갈렸다")
	}
	// 후보 레인 안에 자체 검색 루프가 남아 있으면 안 된다.
	i := strings.Index(src, "func DrainOrgAnchors(")
	if i < 0 {
		t.Fatal("DrainOrgAnchors 를 못 찾았다")
	}
	body := src[i:]
	if j := strings.Index(body, "\nfunc "); j > 0 {
		body = body[:j]
	}
	for _, dup := range []string{"cl.Search(ctx", "cl.Fetch(ctx", "orgAnchorVerdict("} {
		if strings.Contains(body, dup) {
			t.Errorf("DrainOrgAnchors 안에 %q 가 남아 있다 — findAnchorQID 와 두 벌이다", dup)
		}
	}

	a, err := os.ReadFile("active_anchor_drain.go")
	if err != nil {
		t.Fatalf("active_anchor_drain.go 를 못 읽었다: %v", err)
	}
	if !strings.Contains(string(a), "findAnchorQID(ctx, cl, it.ko, it.typ)") {
		t.Error("active 레인이 findAnchorQID 를 안 쓴다")
	}
	for _, dup := range []string{"orgAnchorVerdict(", "cl.Search(ctx"} {
		if strings.Contains(string(a), dup) {
			t.Errorf("active 레인이 %q 를 직접 부른다 — 판정이 두 벌이 된다", dup)
		}
	}
}

// ★active 레인은 **승급하지 않는다.** 이미 active 인 행에 status 를 건드릴 이유가 없고,
// 앵커가 붙었다는 것과 표기가 검증됐다는 것은 다르다.
func TestActiveLaneNeverChangesStatusOrTier(t *testing.T) {
	b, err := os.ReadFile("active_anchor_drain.go")
	if err != nil {
		t.Fatalf("active_anchor_drain.go 를 못 읽었다: %v", err)
	}
	src := string(b)
	for _, banned := range []string{"SET status", "verification_tier", "confidence ="} {
		if strings.Contains(src, banned) {
			t.Errorf("active 레인이 %q 를 쓴다 — 앵커만 붙여야 한다", banned)
		}
	}
}

// ★한국 근거가 없으면(hold) **아무것도 쓰지 않는다.**
//
//	후보 레인이 이 자리에서 5건 중 4건을 틀렸다 — 공군=«공군»이라는 개념,
//	레 미제라블=프랑스 뮤지컬, 금도끼 은도끼=이솝 우화. 이름과 유형이 맞아도
//	한국 것인지 모르면 우리가 결정할 일이 아니다.
func TestHoldWritesNothingInBothLanes(t *testing.T) {
	for _, f := range []string{"org_anchor_drain.go", "active_anchor_drain.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("%s 를 못 읽었다: %v", f, err)
		}
		src := string(b)
		i := strings.Index(src, `case "hold":`)
		if i < 0 {
			t.Errorf("%s 에 hold 분기가 없다", f)
			continue
		}
		block := src[i:min3(i+700, len(src))]
		for _, w := range []string{"INSERT INTO kwave_entity_external_refs", "pool.Exec"} {
			if strings.Contains(block, w) {
				t.Errorf("%s 의 hold 분기가 %q 로 쓴다 — 근거 없이 앵커를 붙인다", f, w)
			}
		}
	}
}

// ★active 레인은 제 레인이 있는 유형을 건드리지 않는다.
//
//	person·group·song_album·movie·drama 는 전용 카탈로그 레인(wdperson·musicbrainz·
//	itunes·tmdb·kofic·kmdb)이 있고 그쪽이 이름검색보다 정확하다. 둘이 같은 행을 두고
//	다투면 앵커가 오간다. character 는 오늘 감사에서 human-on-character 가 139건
//	나왔다 — 캐릭터 이름으로 검색하면 그 배우가 잡힌다.
func TestActiveLaneAvoidsTypesWithTheirOwnLane(t *testing.T) {
	for _, t2 := range []string{"person", "group", "song_album", "movie", "drama", "character"} {
		for _, have := range ActiveAnchorTypes {
			if have == t2 {
				t.Errorf("ActiveAnchorTypes 에 %q 가 들어 있다 — 전용 레인과 다툰다", t2)
			}
		}
	}
	// 그리고 담당할 유형은 실제로 들어 있어야 한다(실측 잔량이 큰 것들).
	for _, want := range []string{"show", "event_tour", "agency", "channel_outlet", "brand_place"} {
		found := false
		for _, have := range ActiveAnchorTypes {
			if have == want {
				found = true
			}
		}
		if !found {
			t.Errorf("ActiveAnchorTypes 에 %q 가 없다 — 앵커 없는 active 가 가장 많이 쌓인 유형이다", want)
		}
	}
}

// ★쿨다운 칸이 후보 레인과 **달라야 한다.** 같은 칸을 쓰면 한쪽이 본 것을
// 다른 쪽이 본 것으로 착각해 영영 안 보는 행이 생긴다.
func TestLanesUseSeparateCooldownFields(t *testing.T) {
	o, _ := os.ReadFile("org_anchor_drain.go")
	a, _ := os.ReadFile("active_anchor_drain.go")
	if !strings.Contains(string(o), "'wdorg'") {
		t.Error("후보 레인의 쿨다운 칸(wdorg)이 없다")
	}
	if !strings.Contains(string(a), "'wdactive'") {
		t.Error("active 레인의 쿨다운 칸(wdactive)이 없다")
	}
	if strings.Contains(string(a), "'wdorg'") {
		t.Error("active 레인이 후보 레인의 쿨다운 칸을 쓴다 — 서로의 기록을 자기 것으로 읽는다")
	}
}

func min3(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ★만들어 놓고 **부르는 곳이 없으면 없는 것과 같다.**
//
//	오늘만 이 결함을 일곱 번 만났고 그중 셋은 내가 만들었다(요청훅 집계 ·
//	네이버 예산 집계 · 이 레인). 시험으로 고정한다.
func TestActiveAnchorHasACaller(t *testing.T) {
	b, err := os.ReadFile("../../cmd/kdb/main.go")
	if err != nil {
		t.Skipf("main.go 를 못 읽었다: %v", err)
	}
	src := string(b)
	if !strings.Contains(src, "kdb.DrainActiveAnchors(") {
		t.Fatal("아무도 DrainActiveAnchors 를 안 부른다 — 코드가 죽어 있다")
	}
	// 기본은 dry-run 이어야 한다. active 는 이미 서빙 중이라 되돌리기가 비싸다.
	i := strings.Index(src, `os.Args[1] == "active-anchor"`)
	if i < 0 {
		t.Fatal("active-anchor CLI 가 없다")
	}
	blk := src[i:min3(i+400, len(src))]
	if !strings.Contains(blk, "dry := true") && !strings.Contains(blk, "dry = true") &&
		!strings.Contains(blk, ", dry := 100, true") && !strings.Contains(blk, "n, dry := 100, true") {
		t.Error("기본이 dry-run 이 아니다 — 서빙 중인 행에 바로 쓴다")
	}
}

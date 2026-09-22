package kdb

import "testing"

// ★이 시험이 지키는 것 (2026-09-23 실측).
//
//	한자 병기는 «다른 대상의 한자»를 가져오기 쉽다. 해병대 → 海兵隊(일반 개념 문서) ·
//	신협 → 信用協同組合(리다이렉트) · 하동군청 → 河東郡(리다이렉트). 앵커가 없으면
//	제목이 정확히 같고 한자가 네 글자 이상일 때만 쓴다.

func TestKowikiHanjaFrom(t *testing.T) {
	cases := []struct {
		name, title, ko, lead string
		minLen                int
		want                  string
	}{
		{"기관", "한국도로공사", "한국도로공사",
			"한국도로공사(韓國道路公社, Korea Expressway Corporation)는 대한민국의 고속도로 설치 및 관리와", 4, "韓國道路公社"},
		{"기관2", "신용보증기금", "신용보증기금",
			"신용보증기금(信用保證基金, Korea Credit Guarantee Fund, KODIT)은 1974년 제정된", 4, "信用保證基金"},
		{"짧은 한자는 앵커 없이 안 쓴다", "해병대", "해병대",
			"해병대(海兵隊, 영어: Marine Corps)는 육상 및 해상에서", 4, ""},
		{"앵커가 있으면 두 글자도 쓴다", "이순신", "이순신",
			"이순신(李舜臣, 1545년 4월 28일 ~ 1598년 12월 16일)은 조선 중기의 무신이다.", 2, "李舜臣"},
		{"첫 괄호가 한자가 아니면 안 쓴다", "숏박스", "숏박스",
			"숏박스(Shortbox)는 대한민국의 코미디 유튜브 채널이다.", 2, ""},
		{"한글이 섞이면 안 쓴다", "가나다", "가나다",
			"가나다(가나다라, 한국어)는", 2, ""},
		{"첫 문장이 이름으로 시작하지 않으면 안 쓴다", "무언가", "무언가",
			"이 문서는 무언가(無言歌)에 대한 설명이다.", 2, ""},
		{"빈 본문", "x", "x", "", 2, ""},
	}
	for _, c := range cases {
		if got := kowikiHanjaFrom(c.title, c.ko, c.lead, c.minLen); got != c.want {
			t.Errorf("%s: kowikiHanjaFrom = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestKowikiTitleFromURL(t *testing.T) {
	if got := kowikiTitleFromURL("https://ko.wikipedia.org/wiki/%ED%95%9C%EA%B5%AD%EB%8F%84%EB%A1%9C%EA%B3%B5%EC%82%AC"); got != "한국도로공사" {
		t.Errorf("제목을 못 꺼냈다: %q", got)
	}
	if got := kowikiTitleFromURL("https://ko.wikipedia.org/wiki/%EC%86%A1%EA%B3%A8%EB%A7%A4_(%EB%B0%B4%EB%93%9C)"); got != "송골매 (밴드)" {
		t.Errorf("괄호 제목을 못 꺼냈다: %q", got)
	}
	if kowikiTitleFromURL("") != "" || kowikiTitleFromURL("https://example.com/x") != "" {
		t.Error("사이트링크가 없으면 빈 제목이어야 한다")
	}
}

// TestKowikiHanjaDisplacesProvisional — 잠정값 칸을 **고를 수 있어야** 한다.
func TestKowikiHanjaDisplacesProvisional(t *testing.T) {
	found := false
	for _, m := range MachineFilledSourcesWeakerThan(SourceKoWikiHanja) {
		if m == string(SourceLLMProvisional) {
			found = true
		}
	}
	if !found {
		t.Error("한자 레인이 잠정 칸을 못 고른다 — 고치는 레인인데 대상이 없다")
	}
	if Priority(SourceKoWikiHanja) >= Priority(SourceGTranslate) {
		t.Error("한자가 기계번역보다 낮다")
	}
	if Priority(SourceKoWikiHanja) <= Priority(SourceWikidataLabel) {
		t.Error("한자가 위키데이터 라벨보다 높다 — 권위 순서가 뒤집힌다")
	}
	wired := false
	for _, l := range WiredLanes {
		if l == "kowiki-hanja" {
			wired = true
		}
	}
	if !wired {
		t.Error("kowiki-hanja 가 WiredLanes 에 없다")
	}
}

// ★앵커가 틀릴 수 있다 (2026-09-23 운영 첫 회차).
//
//	권성준(나폴리 맛피아)의 위키데이터 앵커가 남성훈을 가리켰고, 레인이 그 문서의 한자
//	南星薰 을 가져왔다. 앵커를 믿되 **문서 제목이 우리 이름인지** 한 번 더 본다.
func TestKowikiTitleIsOurs(t *testing.T) {
	if !kowikiTitleIsOurs("한국도로공사", "한국도로공사", "") {
		t.Error("같은 제목을 거부했다")
	}
	if !kowikiTitleIsOurs("송골매 (밴드)", "송골매", "") {
		t.Error("괄호 꼬리를 못 뗐다")
	}
	if !kowikiTitleIsOurs("나폴리 맛피아", "권성준", "나폴리 맛피아|Napoli Matpia") {
		t.Error("별칭으로도 인정해야 한다")
	}
	if kowikiTitleIsOurs("남성훈", "권성준", "나폴리 맛피아") {
		t.Error("앵커가 가리킨 **다른 사람**의 문서를 통과시켰다 — 그 사람의 한자를 가져온다")
	}
	if kowikiTitleIsOurs("", "권성준", "") || kowikiTitleIsOurs("하동군", "하동군청", "") {
		t.Error("다른 문서를 통과시켰다")
	}
}

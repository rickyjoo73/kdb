package verify

import "testing"

// 실측 케이스 고정(2026-07-31). 아래 근거들은 전부 실제로 승급을 만들어낸 것들이다 —
// 게이트가 이걸 다시 통과시키면 회귀다.
func TestFilterRelevantHitsDropsObservedJunk(t *testing.T) {
	cases := []struct {
		ko    string
		lines []string
		want  int // 남아야 할 개수
	}{
		{ko: "셔더로드", want: 0, lines: []string{
			"Google India — ", "Google — ", "Google Search - A new kind of help — ",
			"Google's products and services - About Google — ", "Google - Wikipedia — ",
		}},
		{ko: "은체", want: 0, lines: []string{
			"How to Get Help in Windows 11: 10 Ways That Actually Work — ",
			"How to Get Help in Windows 11 & 10: 18 Fast Methods — ",
			"How to get Help in Windows 11 [Fast] - MSPoweruser — ",
		}},
		{ko: "정중식", want: 0, lines: []string{
			"Government Payments | BancNet — ", "Bancnetonline Login — ",
			"Bureau of Internal Revenue — ", "eFPS Home - eFiling and Payment System — ",
		}},
		{ko: "마이크 윌 메이드잇", want: 0, lines: []string{
			"마이크 테스트 – 온라인으로 내 마이크 테스트하기 | Microphone — ",
			"TOP 7 방송용 마이크 추천 순위 (2026 순위) - 리뷰프로 — ",
			"마이크 - 나무위키 — ",
		}},
		{ko: "HYBE LABELS", want: 0, lines: []string{
			"Mountain Bike News, Photos, Videos & Event — ",
			"Valider votre compte YouTube — ",
			"Field Test: 2022 YT Capra - The Speedy All — ",
		}},
		// 정상 승급(마시로)은 전건 통과해야 한다 — 게이트가 진짜 근거를 죽이면 안 된다.
		{ko: "마시로", want: 4, lines: []string{
			"메이딘 마시로, 180도 달라진 이미지..금발+스모키 비주얼 — ",
			"메이딘 마시로, 금발·스모키 메이크업 파격 변신…'24/11' 발매 기대감 — ",
			"마시로, 금발+스모키 비주얼…시크 아우라 완성 — ",
			"'솔로 데뷔' 메이딘 마시로, 반전 매력 선사…넓은 스펙트럼 입증 — ",
		}},
	}
	for _, c := range cases {
		hits := make([]evHit, 0, len(c.lines))
		for _, l := range c.lines {
			hits = append(hits, evHit{Line: l, URL: "https://x/" + l})
		}
		kept, dropped := filterRelevantHits(hits, c.ko)
		if len(kept) != c.want {
			t.Errorf("%s: 남은 근거 %d건 (기대 %d), 제외 %d — kept=%v", c.ko, len(kept), c.want, dropped, kept)
		}
	}
}

// 괄호 병기는 안팎 어느 쪽으로 보도돼도 통과해야 한다(오거부 방지).
func TestFilterRelevantHitsParentheticalVariants(t *testing.T) {
	ko := "셋 더 템포(SET THE TEMPO)"
	hits := []evHit{
		{Line: "SET THE TEMPO, 신곡 발표 — 스니펫", URL: "https://a"},
		{Line: "셋더템포 컴백 확정 — 스니펫", URL: "https://b"},
		{Line: "Google - Wikipedia — 스니펫", URL: "https://c"},
	}
	kept, _ := filterRelevantHits(hits, ko)
	if len(kept) != 2 {
		t.Fatalf("괄호 병기 변형 매칭 실패: kept=%d %v", len(kept), kept)
	}
}

// 띄어쓰기·대소문자 차이로 진짜 근거를 버리면 안 된다.
func TestFilterRelevantHitsNormalization(t *testing.T) {
	hits := []evHit{
		{Line: "hybe labels 공식 채널 개설 — 스니펫", URL: "https://a"},
		{Line: "HYBELABELS 구독자 급증 — 스니펫", URL: "https://b"},
	}
	kept, _ := filterRelevantHits(hits, "HYBE LABELS")
	if len(kept) != 2 {
		t.Fatalf("정규화 매칭 실패: kept=%d %v", len(kept), kept)
	}
}

// 변형을 못 만드는 이름(1글자)이면 거르지 않는다 — 판단 근거가 없을 때 종전 동작 유지.
func TestFilterRelevantHitsNoVariantsPassesThrough(t *testing.T) {
	hits := []evHit{{Line: "아무 내용 — 스니펫", URL: "https://a"}}
	kept, dropped := filterRelevantHits(hits, "A")
	if len(kept) != 1 || dropped != 0 {
		t.Fatalf("변형 없는 이름에서 거름 발생: kept=%d dropped=%d", len(kept), dropped)
	}
}

package verify

import "testing"

// 이 리팩터(2026-07-31, 근거 URL 보존)의 유일한 실질 리스크는 **판정 프롬프트 입력이
// 바뀌는 것**이다. gatherEvidenceHits 가 만든 Line 이 종전 gatherEvidenceQuery 의 문자열과
// 다르면 gemma 판정이 달라지고 승급 결과가 조용히 바뀐다. 목적은 추적성 확보지 판정 변경이
// 아니므로 그 경계를 테스트로 고정한다.

func TestEvHitLinesPreservesOrderAndContent(t *testing.T) {
	hits := []evHit{
		{Line: "첫 기사 — 스니펫1", URL: "https://n.news.naver.com/1", Provider: "naver-news"},
		{Line: "둘째 기사 — 스니펫2", URL: "https://example.com/2", Provider: "searxng"},
		{Line: "셋째 기사 — 스니펫3", URL: "https://n.news.naver.com/3", Provider: "naver-news"},
	}
	got := evHitLines(hits)
	want := []string{"첫 기사 — 스니펫1", "둘째 기사 — 스니펫2", "셋째 기사 — 스니펫3"}
	if len(got) != len(want) {
		t.Fatalf("길이 불일치: got %d want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] got %q want %q", i, got[i], want[i])
		}
	}
}

// URL 이 없는 증거(검색 백엔드가 링크를 안 준 경우)도 판정 입력에는 반드시 남아야 한다.
// URL 유무로 hits 를 거르면 근거가 줄어 승급률이 떨어진다 — 적재(recordEvidenceRefs)에서만
// 거른다.
func TestEvHitLinesKeepsHitsWithoutURL(t *testing.T) {
	hits := []evHit{
		{Line: "URL 없는 증거 — 스니펫", URL: ""},
		{Line: "URL 있는 증거 — 스니펫", URL: "https://example.com/x"},
	}
	got := evHitLines(hits)
	if len(got) != 2 {
		t.Fatalf("URL 없는 증거가 판정 입력에서 누락됨: got %d want 2 (%v)", len(got), got)
	}
	if got[0] != "URL 없는 증거 — 스니펫" {
		t.Errorf("순서/내용 변형: got %q", got[0])
	}
}

func TestEvHitLinesEmpty(t *testing.T) {
	if got := evHitLines(nil); len(got) != 0 {
		t.Errorf("nil 입력에 빈 슬라이스가 아님: %v", got)
	}
}

package kdb

import (
	"testing"

	"github.com/rickyjoo73/kdb/internal/kdb/naver"
)

func encycResult(items ...naver.Item) *naver.SearchResult {
	return &naver.SearchResult{Total: len(items), Items: items}
}

// 인물: encyc 설명의 역할 단서(배우)가 언급 32rune 근처 — type 힌트 없이도 person 특정.
func TestEvidenceFromSearchResult_EncycPerson(t *testing.T) {
	res := encycResult(naver.Item{
		Title:       "<b>박보검</b>",
		Description: "대한민국의 <b>배우</b>이다. 2011년 영화로 데뷔하였다.",
		Link:        "https://terms.naver.com/entry.naver?docId=123",
	})
	ev, ok := evidenceFromSearchResult("박보검", autoVerifyTypesToTry("", nil), res, "auto_evidence_encyc")
	if !ok {
		t.Fatal("expected evidence pass")
	}
	if ev.Type != "person" {
		t.Fatalf("type=%q, want person", ev.Type)
	}
	if ev.SourceURL != "https://terms.naver.com/entry.naver?docId=123" {
		t.Fatalf("source=%q", ev.SourceURL)
	}
	if ev.Decision.Verdict != "pass" {
		t.Fatalf("verdict=%q", ev.Decision.Verdict)
	}
}

// 작품: 인용부호 언급 + work type — 뉴스 근거로 drama 통과.
func TestEvidenceFromSearchResult_NewsQuotedDrama(t *testing.T) {
	res := encycResult(naver.Item{
		Title:       "&apos;성난 사람들&apos; 시즌2 제작 확정",
		Description: "넷플릭스 시리즈 &apos;성난 사람들&apos;이 방영을 앞두고 있다.",
		Link:        "https://n.news.naver.com/article/001/0001",
	})
	ev, ok := evidenceFromSearchResult("성난 사람들", autoVerifyTypesToTry("drama", nil), res, "auto_evidence_news")
	if !ok {
		t.Fatal("expected evidence pass")
	}
	if ev.Type != "drama" {
		t.Fatalf("type=%q, want drama", ev.Type)
	}
}

// 일반어(비작품 type): 증거가 있어도 게이트의 일반어 가드가 유지돼야 한다.
func TestEvidenceFromSearchResult_AmbiguousCommonStaysBlocked(t *testing.T) {
	res := encycResult(naver.Item{
		Title:       "건강",
		Description: "건강은 정신적, 신체적으로 양호한 상태를 말한다.",
		Link:        "https://terms.naver.com/entry.naver?docId=999",
	})
	if _, ok := evidenceFromSearchResult("건강", autoVerifyTypesToTry("person", nil), res, "auto_evidence_encyc"); ok {
		t.Fatal("common noun must not pass as person")
	}
}

// 언급 자체가 없으면 증거가 아니다.
func TestEvidenceFromSearchResult_NoMention(t *testing.T) {
	res := encycResult(naver.Item{
		Title:       "다른 항목",
		Description: "전혀 무관한 설명이다.",
		Link:        "https://terms.naver.com/entry.naver?docId=1",
	})
	if _, ok := evidenceFromSearchResult("이상한신인이름", autoVerifyTypesToTry("", nil), res, "auto_evidence_encyc"); ok {
		t.Fatal("expected no evidence")
	}
}

// 소비자 type 힌트가 틀린 경우: 증거가 가리키는 type 으로 재지정된다(판단은 우리가).
func TestEvidenceFromSearchResult_TypeReassigned(t *testing.T) {
	res := encycResult(naver.Item{
		Title:       "폭싹 속았수다",
		Description: "대한민국의 드라마이다. 아이유가 주연을 맡았다.",
		Link:        "https://terms.naver.com/entry.naver?docId=77",
	})
	ev, ok := evidenceFromSearchResult("폭싹 속았수다", autoVerifyTypesToTry("person", nil), res, "auto_evidence_encyc")
	if !ok {
		t.Fatal("expected evidence pass")
	}
	if ev.Type != "drama" {
		t.Fatalf("type=%q, want drama (reassigned from person)", ev.Type)
	}
}

// 신뢰할 수 없는 링크(사설IP 등)는 증거로 쓰지 않는다.
func TestEvidenceFromSearchResult_BadLink(t *testing.T) {
	res := encycResult(naver.Item{
		Title:       "박보검",
		Description: "대한민국의 배우이다.",
		Link:        "http://127.0.0.1/x",
	})
	if _, ok := evidenceFromSearchResult("박보검", autoVerifyTypesToTry("person", nil), res, "auto_evidence_encyc"); ok {
		t.Fatal("loopback link must not pass")
	}
}

func TestAutoVerifyTypesToTry(t *testing.T) {
	if got := autoVerifyTypesToTry("person", nil); got[0] != "person" || len(got) != len(autoVerifyTypePriority) {
		t.Fatalf("concrete hint should lead: %v", got)
	}
	for _, hint := range []string{"", "unknown", "term", "place"} {
		if got := autoVerifyTypesToTry(hint, nil); len(got) != len(autoVerifyTypePriority) || got[0] != autoVerifyTypePriority[0] {
			t.Fatalf("hint %q should fall back to priority list: %v", hint, got)
		}
	}
	// 형제 row 의 구체 type 이 기본 우선순위보다 앞선다(unknown row + person 형제 케이스).
	if got := autoVerifyTypesToTry("", []string{"show", "person", "junk"}); got[0] != "show" || got[1] != "person" {
		t.Fatalf("sibling types should lead: %v", got)
	}
	if got := autoVerifyTypesToTry("drama", []string{"drama", "person"}); got[0] != "drama" || got[1] != "person" {
		t.Fatalf("dedupe hint+sibling: %v", got)
	}
}

// 형제 row 가 person 을 요구하는 키워드: 뉴스에 show 단서("출연")와 person 단서(배우)가
// 함께 있어도 person 을 먼저 대조해 형제와 충돌 없는 type 으로 수렴한다(여회현 케이스).
func TestEvidenceFromSearchResult_SiblingTypeWins(t *testing.T) {
	res := encycResult(naver.Item{
		Title:       "배우 여회현, 새 드라마 출연 확정",
		Description: "여회현이 차기작에 출연한다.",
		Link:        "https://n.news.naver.com/article/001/0002",
	})
	ev, ok := evidenceFromSearchResult("여회현", autoVerifyTypesToTry("", []string{"person"}), res, "auto_evidence_news")
	if !ok {
		t.Fatal("expected evidence pass")
	}
	if ev.Type != "person" {
		t.Fatalf("type=%q, want person (sibling priority)", ev.Type)
	}
}

func TestAutoVerifyClean(t *testing.T) {
	got := autoVerifyClean("<b>박보검</b> &quot;굿보이&quot;")
	if got != `박보검 "굿보이"` {
		t.Fatalf("clean=%q", got)
	}
}

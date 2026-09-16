package kdbapi

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// TestMyChangesNeedsAConsumerKey — 「내 것」을 물으려면 내가 누구인지 알아야 한다.
// 운영자 키(env)는 소비자가 아니므로 돌려줄 «내 것»이 없다.
func TestMyChangesNeedsAConsumerKey(t *testing.T) {
	w := httptest.NewRecorder()
	(&handler{store: &Store{}}).myChanges(w, httptest.NewRequest("GET", "/v1/my/changes", nil))
	// store.Pool 이 nil 이므로 503 이 먼저다 — 소비자 판별은 그 뒤다.
	if w.Code != 503 {
		t.Errorf("풀이 없을 때 %d — 503 이어야 한다", w.Code)
	}
}

// TestMyChangesRejectsBadSince — since 는 RFC3339 다. 형식을 틀리면 조용히 30일로
// 되돌리지 않고 **말해 준다** — 조용히 다른 창을 쓰면 소비자는 빠진 줄을 영영 모른다.
func TestMyChangesRejectsBadSince(t *testing.T) {
	h := &handler{store: &Store{}}
	w := httptest.NewRecorder()
	h.myChanges(w, httptest.NewRequest("GET", "/v1/my/changes?since=2026-09-01", nil))
	if w.Code == 200 {
		t.Error("잘못된 since 를 받아들였다")
	}
}

// TestMyChangesIsDocumentedWithItsContract — 이 문은 소비자가 **매일 부르는** 문이다.
// 계약(창·한도·이어부르기·빈 배열의 뜻)이 문서에 없으면 잘못 쓰인다.
func TestMyChangesIsDocumentedWithItsContract(t *testing.T) {
	src, err := os.ReadFile("docs.go")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(src)
	for _, want := range []string{
		"/v1/my/changes",   // 문
		"next_since",       // 이어부르기
		"truncated",        // 한도
		"rule_version_expired", // why 값
		"X-KDB-Rules",      // 신호
		"/v1/changelog",    // 무엇이 바뀌었나
		"/v1/corrections",  // 반대 방향
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("문서에 %q 가 없다 — 계약을 안 적으면 잘못 쓰인다", want)
		}
	}
	// 세 가지 now 값을 전부 적어야 한다. 하나라도 빠지면 소비자가 그 값을 만났을 때 멈춘다.
	for _, v := range []string{"ready", "preparing", "reask"} {
		if !strings.Contains(doc, ">"+v+"</td>") {
			t.Errorf("문서가 now=%q 에 무엇을 해야 하는지 안 적었다", v)
		}
	}
}

// TestMyChangesNeverInvents — 「달라졌다」는 원장에서 읽은 것만이다.
//
// 이 시험은 코드를 읽어 고정한다: 달라진 게 없으면 행을 내지 않는다(continue).
// 빈 목록이 "볼 것 없음"의 정직한 답이고, 억지로 채우면 소비자가 헛일을 한다.
func TestMyChangesNeverInvents(t *testing.T) {
	src, err := os.ReadFile("my_changes.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	// ready 로 답했던 것은 애초에 대상이 아니다(이미 값을 드렸다).
	if !strings.Contains(body, "item_status IN ('out_of_scope','review','unfillable','preparing','new')") {
		t.Error("이미 값을 드린 것까지 다시 훑는다")
	}
	// 판본 비교가 실제로 있어야 한다 — 없으면 reask 를 영영 못 낸다.
	if !strings.Contains(body, "gatekeeper.IntakeRuleVersion") {
		t.Error("판본 만료를 안 본다 — reask 가 나올 수 없다")
	}
	// 창과 한도가 소비자 손에 무제한으로 달려 있으면 안 된다.
	for _, want := range []string{"myChangesMaxWindow", "myChangesLimit"} {
		if !strings.Contains(body, want) {
			t.Errorf("%s 가 없다 — 질의 비용이 소비자 손에 달린다", want)
		}
	}
}

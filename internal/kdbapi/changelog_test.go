package kdbapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/rickyjoo73/kdb/internal/kdb/agents/gatekeeper"
)

// TestBumpingTheRuleVersionRequiresSayingWhy — 판본을 올렸으면 **무엇이 바뀌었는지
// 적어야 한다.**
//
// ★이 시험이 있는 이유 (2026-09-16). 판본 한 줄을 올리면 소비자가 캐시한 종결이
// 전부 만료된다 — 그게 이 장치의 목적이다. 그런데 무엇이 바뀌었는지 안 적으면
// 소비자는 "다시 물으라"는 말만 듣고 **왜인지 모른 채** 전체를 다시 묻는다.
// 판본을 올리는 손과 이력을 적는 손이 같아야 한다.
func TestBumpingTheRuleVersionRequiresSayingWhy(t *testing.T) {
	if len(ruleChanges) == 0 {
		t.Fatal("규칙 이력이 비었다")
	}
	if ruleChanges[0].Version != gatekeeper.IntakeRuleVersion {
		t.Errorf("판본을 %q 로 올렸는데 이력 맨 앞은 %q 다 — 변경 이력을 적어라",
			gatekeeper.IntakeRuleVersion, ruleChanges[0].Version)
	}
	seen := map[string]bool{}
	for _, c := range ruleChanges {
		if c.Version == "" || c.Date == "" || c.Summary == "" {
			t.Errorf("이력이 비어 있다: %+v", c)
		}
		if seen[c.Version] {
			t.Errorf("판본이 중복이다: %q", c.Version)
		}
		seen[c.Version] = true
		// reask 라고 말하면 **무엇이 만료되는지**도 말해야 한다. 안 그러면 소비자가
		// 무엇을 버려야 하는지 모른다.
		if c.Reask && len(c.Affects) == 0 {
			t.Errorf("%s: reask 인데 affects 가 비었다 — 무엇을 다시 물어야 하는지 모른다", c.Version)
		}
		if len(c.Details) == 0 {
			t.Errorf("%s: 사람이 읽을 줄이 없다", c.Version)
		}
	}
}

// TestChangelogAndDocsComeFromOneTable — 문서와 엔드포인트가 **같은 표**에서 나온다.
// 두 벌을 손으로 적으면 한쪽이 뒤처지고, 뒤처진 쪽을 소비자가 읽는다.
func TestChangelogAndDocsComeFromOneTable(t *testing.T) {
	src, err := os.ReadFile("docs.go")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(src)
	if !strings.Contains(doc, ruleChangesSlot) {
		t.Errorf("문서에 %s 자리표가 없다 — 이력이 안 그려진다", ruleChangesSlot)
	}
	if !strings.Contains(doc, "renderRuleChangesHTML()") {
		t.Error("문서가 공용 표에서 이력을 만들지 않는다")
	}
	rendered := renderRuleChangesHTML()
	for _, c := range ruleChanges {
		if !strings.Contains(rendered, c.Version) {
			t.Errorf("%s 가 문서 이력에 안 그려졌다", c.Version)
		}
	}
	// 자리표가 실제로 **채워져** 나가는지. 안 채워지면 소비자가 주석을 본다.
	w := httptest.NewRecorder()
	(&handler{}).docs(w, httptest.NewRequest("GET", "/docs", nil))
	body := w.Body.String()
	for _, slot := range []string{ruleChangesSlot, rulesVersionSlot} {
		if strings.Contains(body, slot) {
			t.Errorf("자리표 %s 가 안 채워진 채 나갔다", slot)
		}
	}
	if !strings.Contains(body, gatekeeper.IntakeRuleVersion) {
		t.Error("문서에 현재 판본이 안 나온다")
	}
}

// TestEveryResponseCarriesTheRuleVersion — 소비자가 **캐시한 종결이 낡았는지 알 수 있는
// 유일한 신호**다. 한 경로라도 빠지면 그 경로만 쓰는 소비자는 영영 모른다.
func TestEveryResponseCarriesTheRuleVersion(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	w := httptest.NewRecorder()
	rulesHeader(inner).ServeHTTP(w, httptest.NewRequest("GET", "/v1/anything", nil))
	if got := w.Header().Get("X-KDB-Rules"); got != gatekeeper.IntakeRuleVersion {
		t.Errorf("X-KDB-Rules=%q, 기대 %q", got, gatekeeper.IntakeRuleVersion)
	}
	// 데이터셋 판본과 **다른 값**이어야 한다. 같으면 규칙 신호로 못 쓴다.
	if strings.Contains(gatekeeper.IntakeRuleVersion, ".") &&
		strings.Count(gatekeeper.IntakeRuleVersion, ".") > 1 {
		t.Error("규칙 판본이 데이터셋 판본 모양이다 — 둘을 혼동하게 된다")
	}
}

// TestChangelogEndpointTellsConsumerWhatToDo — 값만 주고 뜻을 안 주면 안 읽힌다.
func TestChangelogEndpointTellsConsumerWhatToDo(t *testing.T) {
	w := httptest.NewRecorder()
	(&handler{}).changelog(w, httptest.NewRequest("GET", "/v1/changelog", nil))
	if w.Code != 200 {
		t.Fatalf("code=%d", w.Code)
	}
	var got changelogResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Current != gatekeeper.IntakeRuleVersion {
		t.Errorf("current=%q", got.Current)
	}
	if len(got.Changes) != len(ruleChanges) {
		t.Errorf("이력 %d건, 기대 %d건", len(got.Changes), len(ruleChanges))
	}
	for _, want := range []string{"X-KDB-Rules", "/v1/my/changes"} {
		if !strings.Contains(got.HowToUse, want) {
			t.Errorf("how_to_use 가 %q 를 안 말한다", want)
		}
	}
}

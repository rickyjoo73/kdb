package kdbapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rickyjoo73/kdb/internal/testdb"
)

// 기존 API replay (R01·R02·R05·R06·R07·R12) — KDB_API_COMPATIBILITY_CASES.md
//
// 이 패키지의 DB 시험은 지금까지 전부 합성 축약 스키마였다. 즉 "복원 DB 통합"의 근거가
// 아니었고, 실제로 운영 제약(예: kwave_entities_homonym_key)은 합성 스키마에 없어서
// 안 걸렸다. 여기서는 복원본을 상대로 실제 라우터를 통과시킨다.
//
// R03/R04/R09/R10/R11 은 TDB 트리가 필요해 이 저장소에서 실행할 수 없다.
// R08 은 교정 쓰기 경로라 별도 정리 계약이 필요해 P1.10 으로 넘긴다.

func replayRouter(t *testing.T) http.Handler {
	t.Helper()
	return NewRouterWithOptions(testdb.Restored(t), RouterOptions{APIKeys: []string{"fixture-operator"}})
}

func replayCall(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	r.Header.Set("Authorization", "Bearer fixture-operator")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// TestReplayR01ErrorEnvelope — 오류 봉투가 status/code/message 를 보존하는가.
// 공식 client 가 `error` 를 문자열로 기대해 decode 를 버리는 문제는 client 쪽이며,
// 서버 계약은 중첩 봉투를 유지한다. 여기서 고정해 둔다.
func TestReplayR01ErrorEnvelope(t *testing.T) {
	h := replayRouter(t)
	w := replayCall(t, h, "POST", "/v1/entities/match", `{"locale":"ja"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatal("source_text 없는 match 는 400 이어야 한다:", w.Code)
	}
	var env struct {
		OK    bool `json:"ok"`
		Error struct {
			Code, Message string
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatal(err, w.Body.String())
	}
	if env.OK || env.Error.Code == "" || env.Error.Message == "" {
		t.Fatal("봉투에 code/message 가 없다:", w.Body.String())
	}
}

// TestReplayR02APIPrefixAlias — /api/health 와 /api/v1/health 두 legacy 조합.
// 종전에는 후자가 /v1/v1/health 가 돼 404 였다. 한 번만 정규화한다.
func TestReplayR02APIPrefixAlias(t *testing.T) {
	h := replayRouter(t)
	for _, path := range []string{"/v1/health", "/api/health", "/api/v1/health"} {
		if w := replayCall(t, h, "GET", path, ""); w.Code != http.StatusOK {
			t.Errorf("%s → %d (200 이어야 한다)", path, w.Code)
		}
	}
}

// TestReplayR05LocaleMatrix — route 마다 locale 규칙이 다르다. 그 차이를 고정한다.
// match 는 별칭을 정규화하고 미지원은 400. durable preparations 는 엄격히 거부.
// legacy prepare 는 요청 단계에서 거부하지 않는다(오너 방침 "요청은 다 받는다").
func TestReplayR05LocaleMatrix(t *testing.T) {
	h := replayRouter(t)
	for _, loc := range []string{"zh-Hans", "ZH_hant", "pt-PT", "ja"} {
		w := replayCall(t, h, "POST", "/v1/entities/match", fmt.Sprintf(`{"source_text":"테스트","locale":%q}`, loc))
		if w.Code != http.StatusOK {
			t.Errorf("match locale %s → %d (정규화돼 200 이어야 한다): %s", loc, w.Code, w.Body.String())
		}
	}
	for _, loc := range []string{"de", "th", "xx"} {
		w := replayCall(t, h, "POST", "/v1/entities/match", fmt.Sprintf(`{"source_text":"테스트","locale":%q}`, loc))
		if w.Code != http.StatusBadRequest {
			t.Errorf("match locale %s → %d (미지원이라 400 이어야 한다)", loc, w.Code)
		}
	}
	// legacy prepare 는 미지원 locale 을 요청 단계에서 거부하지 않는다.
	t.Setenv("KDB_READINESS_ENABLED", "")
	w := replayCall(t, h, "POST", "/v1/prepare", `{"terms":[{"ko":"테스트"}],"locales":["xx"]}`)
	if w.Code == http.StatusBadRequest {
		t.Error("legacy prepare 가 요청 단계에서 locale 을 거부했다 — 기존 계약 변경")
	}
}

// TestReplayR06PrepareTermCap — terms 201개를 보내면 200개로 조용히 자른다.
// 목표 불변조건은 "accepted/truncated 수를 노출"이지만, 그건 응답 모양 변경이라
// 소비자 계약 검토가 필요하다(P1.10). 여기서는 현행 cap 이 실제로 200 인지 고정한다.
func TestReplayR06PrepareTermCap(t *testing.T) {
	h := replayRouter(t)
	terms := make([]string, 0, 201)
	for i := 0; i < 201; i++ {
		terms = append(terms, fmt.Sprintf(`{"ko":"합성용어%03d"}`, i))
	}
	w := replayCall(t, h, "POST", "/v1/prepare",
		`{"terms":[`+strings.Join(terms, ",")+`],"locales":["ja"]}`)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String()[:min(300, len(w.Body.String()))])
	}
	var res struct {
		Items []struct{} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Items) > 200 {
		t.Fatal("cap 200 을 넘겼다:", len(res.Items))
	}
	t.Logf("terms 201 → items %d (cap 200, 초과분은 조용히 잘린다 — 노출은 P1.10)", len(res.Items))
}

// TestReplayR12InvalidCursorIsRejected — 해석 못 하는 델타 커서를 조용히 무시하지 않는다.
// 종전에는 전체 조회로 진행해 소비자가 전건을 "변경분"으로 받았다.
func TestReplayR12InvalidCursorIsRejected(t *testing.T) {
	h := replayRouter(t)
	if w := replayCall(t, h, "GET", "/v1/entities?updated_since=not-a-time&limit=1", ""); w.Code != http.StatusBadRequest {
		t.Errorf("형식이 틀린 updated_since → %d (400 이어야 한다): %s", w.Code, w.Body.String())
	}
	if w := replayCall(t, h, "GET", "/v1/entities?updated_since=2026-01-01T00:00:00Z&limit=1", ""); w.Code != http.StatusOK {
		t.Errorf("정상 RFC3339 → %d (200 이어야 한다): %s", w.Code, w.Body.String())
	}
	if w := replayCall(t, h, "GET", "/v1/entities?limit=1", ""); w.Code != http.StatusOK {
		t.Errorf("커서 미지정 → %d (200 이어야 한다)", w.Code)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

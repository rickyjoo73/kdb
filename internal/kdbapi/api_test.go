package kdbapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestEntityFilterNormalized(t *testing.T) {
	got := (EntityFilter{Query: "  BTS  ", Limit: 500}).normalized()
	if got.Query != "BTS" {
		t.Fatalf("query trim = %q", got.Query)
	}
	if got.Status != "active" {
		t.Fatalf("default status = %q", got.Status)
	}
	if got.Limit != maxLimit {
		t.Fatalf("max limit = %d", got.Limit)
	}

	got = (EntityFilter{Status: "all", Limit: -1}).normalized()
	if got.Status != "all" {
		t.Fatalf("status = %q", got.Status)
	}
	if got.Limit != defaultLimit {
		t.Fatalf("default limit = %d", got.Limit)
	}
}

func TestNormalizeLocale(t *testing.T) {
	cases := map[string]string{
		" JA ":    "ja",
		"zh_Hant": "zh-hant",
		"pt_BR":   "pt-br",
	}
	for in, want := range cases {
		if got := normalizeLocale(in); got != want {
			t.Fatalf("normalizeLocale(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSpellingsForLocale(t *testing.T) {
	ent := Entity{
		CanonicalKO: "방탄소년단",
		CanonicalEN: "BTS",
		Aliases: AliasSets{
			KO: []string{"방탄", " 방탄소년단 "},
			EN: []string{"Bangtan Boys", "BTS"},
		},
	}

	got, err := spellingsForLocale(ent, "en")
	if err != nil {
		t.Fatalf("spellingsForLocale: %v", err)
	}
	want := []LocaleSpellings{{
		Locale:    "en",
		Canonical: "BTS",
		Aliases:   []string{"Bangtan Boys", "BTS"},
		Spellings: []string{"BTS", "Bangtan Boys"},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("spellings = %#v, want %#v", got, want)
	}

	if _, err := spellingsForLocale(ent, "fr"); err == nil {
		t.Fatal("expected unsupported locale error")
	}
}

func TestAPIKeyMiddleware(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	handler := newAPIKeyAuthenticator(nil, []string{"kdb_live_test"}).middleware(next)

	req := httptest.NewRequest(http.MethodGet, "/v1/entities", nil)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("missing key status = %d", res.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/entities", nil)
	req.Header.Set("Authorization", "Bearer kdb_live_test")
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusNoContent {
		t.Fatalf("bearer key status = %d", res.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/entities", nil)
	req.Header.Set("X-KDB-Key", "kdb_live_test")
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusNoContent {
		t.Fatalf("x-kdb-key status = %d", res.Code)
	}
}

func TestEntityLocaleColumns(t *testing.T) {
	cases := []struct {
		locale  string
		target  string
		aliases string
	}{
		{locale: "ja", target: "canonical_ja", aliases: "aliases_ja"},
		{locale: "pt_BR", target: "canonical_pt_br", aliases: "aliases_pt_br"},
		{locale: "zh-HK", target: "canonical_zh_hant", aliases: "aliases_zh_hant"},
	}
	for _, c := range cases {
		target, aliases, err := entityLocaleColumns(c.locale)
		if err != nil {
			t.Fatalf("entityLocaleColumns(%q): %v", c.locale, err)
		}
		if target != c.target || aliases != c.aliases {
			t.Fatalf("entityLocaleColumns(%q) = %s/%s", c.locale, target, aliases)
		}
	}

	if _, _, err := entityLocaleColumns("fr"); err == nil {
		t.Fatal("expected unsupported locale error")
	}
}

func TestMatchEntitiesRequestNormalized(t *testing.T) {
	got := (MatchEntitiesRequest{SourceText: "  BTS  ", Locale: "PT_BR", Limit: 500}).normalized()
	if got.SourceText != "BTS" {
		t.Fatalf("SourceText = %q", got.SourceText)
	}
	if got.Locale != "pt-br" {
		t.Fatalf("Locale = %q", got.Locale)
	}
	if got.Limit != maxMatchLimit {
		t.Fatalf("Limit = %d, want %d", got.Limit, maxMatchLimit)
	}

	got = (MatchEntitiesRequest{}).normalized()
	if got.Limit != defaultMatchLimit {
		t.Fatalf("default Limit = %d, want %d", got.Limit, defaultMatchLimit)
	}
}

func TestMatchEntitiesRouteValidation(t *testing.T) {
	handler := NewRouterWithOptions(nil, RouterOptions{})
	req := httptest.NewRequest(http.MethodPost, "/v1/entities/match", strings.NewReader(`{"locale":"ja"}`))
	res := httptest.NewRecorder()

	handler.ServeHTTP(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", res.Code)
	}
	if !strings.Contains(res.Body.String(), "source_text required") {
		t.Fatalf("body = %q", res.Body.String())
	}
}

func TestReadSubrouteIDValidation(t *testing.T) {
	handler := NewRouterWithOptions(nil, RouterOptions{})
	cases := []string{
		"/v1/entities/not-a-uuid/external-refs",
		"/v1/entities/not-a-uuid/relations",
		"/v1/persons/not-a-uuid",
	}
	for _, path := range cases {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		res := httptest.NewRecorder()

		handler.ServeHTTP(res, req)

		if res.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want 400", path, res.Code)
		}
		if !strings.Contains(res.Body.String(), "invalid id") {
			t.Fatalf("%s body = %q", path, res.Body.String())
		}
	}
}

func TestWriteRouteValidation(t *testing.T) {
	handler := NewRouterWithOptions(nil, RouterOptions{})
	cases := []struct {
		method string
		path   string
		body   string
		want   string
	}{
		{
			method: http.MethodPost,
			path:   "/v1/observations",
			body:   `{"locale":"ja","spelling":"BTS","source_domain":"example.com"}`,
			want:   "invalid entity_id",
		},
		{
			method: http.MethodPost,
			path:   "/v1/research-queue",
			body:   `{"requested_entity_type":"group"}`,
			want:   "entity_ko required",
		},
		{
			method: http.MethodPatch,
			path:   "/v1/entities/not-a-uuid",
			body:   `{}`,
			want:   "invalid id",
		},
		{
			method: http.MethodPost,
			path:   "/v1/entities/not-a-uuid/lock",
			body:   `{}`,
			want:   "invalid id",
		},
		{
			method: http.MethodPost,
			path:   "/v1/entities/not-a-uuid/site-search",
			body:   `{}`,
			want:   "invalid id",
		},
		{
			method: http.MethodPost,
			path:   "/v1/entities/00000000-0000-0000-0000-000000000001/site-search",
			body:   `{}`,
			want:   "locale required",
		},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
		res := httptest.NewRecorder()

		handler.ServeHTTP(res, req)

		if res.Code != http.StatusBadRequest {
			t.Fatalf("%s %s status = %d, want 400", c.method, c.path, res.Code)
		}
		if !strings.Contains(res.Body.String(), c.want) {
			t.Fatalf("%s %s body = %q, want %q", c.method, c.path, res.Body.String(), c.want)
		}
	}
}

func TestMatchEntitiesRequestGatingDefaults(t *testing.T) {
	got := (MatchEntitiesRequest{SourceText: "x", Locale: "ja"}).normalized()
	if got.MinConfidence != 0.50 {
		t.Fatalf("default min_confidence = %v, want 0.50", got.MinConfidence)
	}
	got = (MatchEntitiesRequest{SourceText: "x", Locale: "ja", MinConfidence: 0.9, Status: " active "}).normalized()
	if got.MinConfidence != 0.9 {
		t.Fatalf("min_confidence override = %v", got.MinConfidence)
	}
	if got.Status != "active" {
		t.Fatalf("status trim = %q", got.Status)
	}
	got = (MatchEntitiesRequest{SourceText: "x", Locale: "ja", MinConfidence: 5}).normalized()
	if got.MinConfidence != 1 {
		t.Fatalf("min_confidence clamp = %v, want 1", got.MinConfidence)
	}
}

func TestEntityFilterMinConfidenceClamp(t *testing.T) {
	if got := (EntityFilter{MinConfidence: 5}).normalized(); got.MinConfidence != 1 {
		t.Fatalf("clamp high = %v", got.MinConfidence)
	}
	if got := (EntityFilter{MinConfidence: -1}).normalized(); got.MinConfidence != 0 {
		t.Fatalf("clamp low = %v", got.MinConfidence)
	}
}

func TestMatchInvalidStatusRejected(t *testing.T) {
	handler := NewRouterWithOptions(nil, RouterOptions{})
	req := httptest.NewRequest(http.MethodPost, "/v1/entities/match",
		strings.NewReader(`{"source_text":"BTS","locale":"ja","status":"pending"}`))
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", res.Code)
	}
	if !strings.Contains(res.Body.String(), "invalid status") {
		t.Fatalf("body = %q", res.Body.String())
	}
}

func TestBulkMatchRouteValidation(t *testing.T) {
	handler := NewRouterWithOptions(nil, RouterOptions{})
	cases := []struct {
		body string
		want string
	}{
		{`{"source_texts":["x"]}`, "locale required"},
		{`{"locale":"ja"}`, "source_texts required"},
		{`{"locale":"ja","source_texts":["x"],"status":"pending"}`, "invalid status"},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodPost, "/v1/entities/match/bulk", strings.NewReader(c.body))
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != http.StatusBadRequest {
			t.Fatalf("body %q status = %d, want 400", c.body, res.Code)
		}
		if !strings.Contains(res.Body.String(), c.want) {
			t.Fatalf("body %q resp = %q, want %q", c.body, res.Body.String(), c.want)
		}
	}
}

func TestErrorEnvelopeStructured(t *testing.T) {
	handler := NewRouterWithOptions(nil, RouterOptions{})
	req := httptest.NewRequest(http.MethodPost, "/v1/entities/match", strings.NewReader(`{"locale":"ja"}`))
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	var env struct {
		OK    bool `json:"ok"`
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode error envelope: %v (body=%q)", err, res.Body.String())
	}
	if env.OK {
		t.Fatal("ok should be false")
	}
	if env.Error.Code != "bad_request" {
		t.Fatalf("error.code = %q, want bad_request", env.Error.Code)
	}
	if env.Error.Message != "source_text required" {
		t.Fatalf("error.message = %q", env.Error.Message)
	}
}

func TestLooksLikeEntityName(t *testing.T) {
	ok := []string{"박보검", "방탄소년단", "아는 형님", "Park Bo Gum", "NewJeans"}
	for _, q := range ok {
		if !looksLikeEntityName(q) {
			t.Fatalf("looksLikeEntityName(%q) = false, want true", q)
		}
	}
	bad := []string{
		"",                              // 빈값
		"x",                             // 너무 짧음
		"123",                           // 숫자만(글자 없음)
		"!!!",                           // 기호만
		"임ㅇ원희",                       // 깨진 자소
		"오늘 방탄소년단이 새 앨범을 발표 했다고 한다", // 문장(>5 어절)
	}
	for _, q := range bad {
		if looksLikeEntityName(q) {
			t.Fatalf("looksLikeEntityName(%q) = true, want false", q)
		}
	}
}

func TestValidEntityTypeAndStatus(t *testing.T) {
	if !validEntityType("group") || validEntityType("sports_team") {
		t.Fatal("entity type validation mismatch")
	}
	if !validEntityStatus("candidate") || validEntityStatus("pending") {
		t.Fatal("entity status validation mismatch")
	}
}

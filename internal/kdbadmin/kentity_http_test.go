package kdbadmin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rickyjoo73/kdb/internal/kentity"
	"github.com/rickyjoo73/kdb/internal/testdb"
)

func TestCommonEntityHTTPRolesCSRFIdempotencyAndRevocation(t *testing.T) {
	t.Setenv("KDB_COMMON_ENTITY_ENABLED", "1")
	pool := testdb.New(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `ALTER TABLE kwave_persons ADD name_ko text; CREATE TABLE kwave_kdb_admin_users(email text PRIMARY KEY,role text,enabled boolean); INSERT INTO kwave_kdb_admin_users VALUES('fixture@test','viewer',true); INSERT INTO kwave_persons(name_ko) VALUES('검수 예시')`); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("../../migrations/0116_kentity_core.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(b)); err != nil {
		t.Fatal(err)
	}
	secret := []byte("common-admin-synthetic-secret")
	session := encodeSession(secret, "fixture@test", sessionMaxAge)
	h := NewRouter(pool, Options{SessionSecret: secret})
	call := func(method, path string, form url.Values) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	form := url.Values{"_csrf": {csrfToken(secret, session)}, "ko": {"동명이인 합성"}, "type": {"person"}, "domains": {"politics", "sports"}, "reason": {"합성 후보 등록 검증"}, "request_key": {"same-request"}}
	for _, path := range []string{"/admin/kentity", "/admin/kentity/mappings"} {
		if w := call("GET", path, nil); w.Code != 200 || strings.Contains(w.Body.String(), "조회 실패") {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	if w := call("POST", "/admin/kentity/candidates", form); w.Code != 403 {
		t.Fatal("viewer write", w.Code)
	}
	if _, err = pool.Exec(ctx, `UPDATE kwave_kdb_admin_users SET role='operator'`); err != nil {
		t.Fatal(err)
	}
	form.Del("_csrf")
	if w := call("POST", "/admin/kentity/candidates", form); w.Code != 403 {
		t.Fatal("csrf bypass", w.Code)
	}
	form.Set("_csrf", csrfToken(secret, session))
	w := call("POST", "/admin/kentity/candidates", form)
	if w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}
	path := w.Header().Get("Location")
	if w = call("POST", "/admin/kentity/candidates", form); w.Code != 303 || w.Header().Get("Location") != path {
		t.Fatal("duplicate candidate", w.Code)
	}
	if w = call("GET", path, nil); w.Code != 200 || strings.Contains(w.Body.String(), "조회 실패") {
		t.Fatal(w.Code, w.Body.String())
	}
	form.Set("reason", "다른 요청 내용")
	if w = call("POST", "/admin/kentity/candidates", form); w.Code != 409 {
		t.Fatal("reused key with changed input", w.Code)
	}
	form.Set("reason", strings.Repeat("가", 20000))
	if w = call("POST", "/admin/kentity/candidates", form); w.Code != 400 {
		t.Fatal("oversized form parsed before bound", w.Code)
	}
	var sourceID uuid.UUID
	if err = pool.QueryRow(ctx, `SELECT id FROM kwave_persons LIMIT 1`).Scan(&sourceID); err != nil {
		t.Fatal(err)
	}
	if w = call("GET", "/admin/kentity/mappings/"+sourceID.String(), nil); w.Code != 200 || strings.Contains(w.Body.String(), "조회 실패") {
		t.Fatal(w.Code, w.Body.String())
	}
	b, err = os.ReadFile("../../migrations/0117_kentity_resolution.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(b)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KDB_ENTITY_RESOLVER_ENABLED", "1")
	research := url.Values{"_csrf": {csrfToken(secret, session)}, "revision": {"1"}, "reason": {"신규 분야 누락 조사 요청 합성 검증"}}
	if w = call("GET", path, nil); w.Code != 200 || !strings.Contains(w.Body.String(), "누락 대상 자동 조사 요청") {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = call("POST", path+"/research", research); w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = call("POST", path+"/research", research); w.Code != 303 {
		t.Fatal("research duplicate", w.Code)
	}
	var jobID string
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM kentity_resolution_jobs`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if err = pool.QueryRow(ctx, `SELECT id::text FROM kentity_resolution_jobs LIMIT 1`).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	research.Set("job_id", jobID)
	research.Set("generation", "0")
	if _, err = pool.Exec(ctx, `UPDATE kwave_kdb_admin_users SET role='viewer'`); err != nil {
		t.Fatal(err)
	}
	if w = call("POST", path+"/research/cancel", research); w.Code != 403 {
		t.Fatal("viewer cancel", w.Code)
	}
	if w = call("GET", path, nil); w.Code != 200 || strings.Contains(w.Body.String(), "<button class=\"border rounded p-2\">자동 조사 취소") {
		t.Fatal("viewer action shown", w.Code)
	}
	if _, err = pool.Exec(ctx, `UPDATE kwave_kdb_admin_users SET role='operator'`); err != nil {
		t.Fatal(err)
	}
	research.Del("_csrf")
	if w = call("POST", path+"/research/cancel", research); w.Code != 403 {
		t.Fatal("cancel csrf", w.Code)
	}
	research.Set("_csrf", csrfToken(secret, session))
	if w = call("POST", path+"/research/cancel", research); w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}
	var state string
	if err = pool.QueryRow(ctx, `SELECT state FROM kentity_resolution_jobs LIMIT 1`).Scan(&state); err != nil || state != "cancelled" {
		t.Fatal(state, err)
	}
	proposal, _ := json.Marshal([]kentity.Proposal{{ObservedAt: time.Now().UTC(), QID: "Q123", SourceURL: "https://www.wikidata.org/wiki/Q123", License: "CC0-1.0", Names: []kentity.Name{{Locale: "en", Value: "Synthetic Person", Form: "recorded", Source: "wikidata-label"}}}})
	if _, err = pool.Exec(ctx, `UPDATE kentity_resolution_jobs SET state='review',generation=1,result=$1`, proposal); err != nil {
		t.Fatal(err)
	}
	approve := url.Values{"_csrf": {csrfToken(secret, session)}, "job_id": {jobID}, "generation": {"1"}, "qid": {"Q123"}, "locales": {"en"}, "reason": {"합성 HTTP 검수 승인 사유"}, "identity_facts": {"독립 식별자와 소속 및 활동을 대조한 합성 HTTP 동일인 검수입니다."}, "attested": {"yes"}}
	if _, err = pool.Exec(ctx, `UPDATE kwave_kdb_admin_users SET role='viewer'`); err != nil {
		t.Fatal(err)
	}
	if w = call("POST", path+"/research/approve", approve); w.Code != 403 {
		t.Fatal("viewer approve", w.Code)
	}
	if _, err = pool.Exec(ctx, `UPDATE kwave_kdb_admin_users SET role='operator'`); err != nil {
		t.Fatal(err)
	}
	approve.Del("_csrf")
	if w = call("POST", path+"/research/approve", approve); w.Code != 403 {
		t.Fatal("approve csrf", w.Code)
	}
	approve.Set("_csrf", csrfToken(secret, session))
	if w = call("POST", path+"/research/approve", approve); w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = call("POST", path+"/research/approve", approve); w.Code != 409 {
		t.Fatal("double HTTP approval", w.Code)
	}
	if w = call("GET", path, nil); w.Code != 200 || !strings.Contains(w.Body.String(), "선택한 기록 표기가 검수되었습니다") {
		t.Fatal(w.Code, w.Body.String())
	}
	b, err = os.ReadFile("../../migrations/0118_kentity_write_ownership.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(b)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KDB_ENTITY_OWNERSHIP_ENABLED", "1")
	legacyID := uuid.New()
	if _, err = pool.Exec(ctx, `INSERT INTO kwave_entities(id,canonical_ko,status,notes) VALUES($1,'합성 사회 기관','rejected','연예 범위 외라는 합성 기각')`, legacyID); err != nil {
		t.Fatal(err)
	}
	store := &kentity.Store{Pool: pool}
	e, err := store.Get(ctx, legacyID)
	if err != nil {
		t.Fatal(err)
	}
	l, err := store.LegacyOwnership(ctx, legacyID)
	if err != nil {
		t.Fatal(err)
	}
	legacyPath := "/admin/kentity/" + legacyID.String()
	adopt := url.Values{"_csrf": {csrfToken(secret, session)}, "revision": {strconv.FormatInt(e.Revision, 10)}, "fingerprint": {l.Fingerprint}, "type": {"organization"}, "domains": {"society"}, "reason": {"기존 연예 범위 기각을 공통 사회 분야의 새로운 검수로 전환하는 합성 요청"}, "source_url": {"https://example.test/scope"}, "attested": {"yes"}}
	if w = call("GET", legacyPath, nil); w.Code != 200 || !strings.Contains(w.Body.String(), "같은 UUID로 공통 검수 전환") {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, err = pool.Exec(ctx, `UPDATE kwave_kdb_admin_users SET role='viewer'`); err != nil {
		t.Fatal(err)
	}
	if w = call("POST", legacyPath+"/adopt", adopt); w.Code != 403 {
		t.Fatal("viewer adopt", w.Code)
	}
	if _, err = pool.Exec(ctx, `UPDATE kwave_kdb_admin_users SET role='operator'`); err != nil {
		t.Fatal(err)
	}
	adopt.Del("_csrf")
	if w = call("POST", legacyPath+"/adopt", adopt); w.Code != 403 {
		t.Fatal("adopt csrf", w.Code)
	}
	adopt.Set("_csrf", csrfToken(secret, session))
	if w = call("POST", legacyPath+"/adopt", adopt); w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = call("POST", legacyPath+"/adopt", adopt); w.Code != 409 {
		t.Fatal("duplicate adoption", w.Code)
	}
	if w = call("GET", legacyPath, nil); w.Code != 200 || !strings.Contains(w.Body.String(), "현재 쓰기 책임 공통 Entity") || strings.Contains(w.Body.String(), "조회 실패") {
		t.Fatal(w.Code, w.Body.String())
	}
	e, err = store.Get(ctx, legacyID)
	if err != nil {
		t.Fatal(err)
	}
	lock := url.Values{"_csrf": {csrfToken(secret, session)}, "revision": {strconv.FormatInt(e.Revision, 10)}, "locked": {"true"}, "reason": {"합성 공통 Entity 보호 잠금 설정"}}
	if _, err = pool.Exec(ctx, `UPDATE kwave_kdb_admin_users SET role='viewer'`); err != nil {
		t.Fatal(err)
	}
	if w = call("POST", legacyPath+"/lock", lock); w.Code != 403 {
		t.Fatal("viewer lock", w.Code)
	}
	if _, err = pool.Exec(ctx, `UPDATE kwave_kdb_admin_users SET role='operator'`); err != nil {
		t.Fatal(err)
	}
	lock.Del("_csrf")
	if w = call("POST", legacyPath+"/lock", lock); w.Code != 403 {
		t.Fatal("lock csrf", w.Code)
	}
	lock.Set("_csrf", csrfToken(secret, session))
	if w = call("POST", legacyPath+"/lock", lock); w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = call("POST", legacyPath+"/lock", lock); w.Code != 409 {
		t.Fatal("stale lock", w.Code)
	}
	if w = call("GET", "/admin/kentity/tdb", nil); w.Code != 503 {
		t.Fatal("shadow gate", w.Code)
	}
	b, err = os.ReadFile("../../migrations/0120_kentity_tdb_shadow.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(b)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KDB_TDB_SHADOW_ENABLED", "1")
	b, err = os.ReadFile("../../migrations/0122_kentity_tdb_crosswalk.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(b)); err != nil {
		t.Fatal(err)
	}
	if w = call("GET", "/admin/kentity/tdb", nil); w.Code != 200 || strings.Contains(w.Body.String(), "원장 조회 실패") {
		t.Fatal("shadow route", w.Code)
	}
	store = &kentity.Store{Pool: pool}
	batch := kentity.TDBBindingBatch{Policy: kentity.TDBShadowPolicy, Source: "wikidata", License: "CC0", State: "live", Enabled: true, ObservedAt: time.Now(), Bindings: []kentity.TDBBinding{{ID: uuid.New(), QID: "Q777", Type: "person", Method: "synthetic", Score: 0.9}}}
	if _, err = store.ImportTDBBindings(ctx, "fixture", batch, true); err != nil {
		t.Fatal(err)
	}
	shadows, err := store.TDBShadows(ctx, "", 10)
	if err != nil || len(shadows) != 1 {
		t.Fatal(shadows, err)
	}
	shadow := shadows[0]
	shadowResult, _ := json.Marshal(kentity.Proposal{KO: "합성 TDB 후보", QID: "Q777", SourceURL: "https://www.wikidata.org/wiki/Q777", License: "CC0-1.0", ObservedAt: time.Now(), InstanceOf: []string{"Q5"}, Names: []kentity.Name{{Locale: "ko", Value: "합성 TDB 후보", Form: "recorded", Source: "wikidata-label"}}})
	if _, err = pool.Exec(ctx, `UPDATE kentity_tdb_shadows SET state='review',generation=1,result=$2 WHERE id=$1`, shadow.ID, shadowResult); err != nil {
		t.Fatal(err)
	}
	tdbPath := "/admin/kentity/tdb/" + shadow.ID.String()
	if w = call("GET", tdbPath, nil); w.Code != 503 {
		t.Fatal("mapping gate", w.Code)
	}
	t.Setenv("KDB_TDB_MAPPING_ENABLED", "1")
	if w = call("GET", tdbPath, nil); w.Code != 200 || strings.Contains(w.Body.String(), "상세 조회 실패") {
		t.Fatal(w.Code, w.Body.String())
	}
	tdbForm := url.Values{"_csrf": {csrfToken(secret, session)}, "generation": {"1"}, "fingerprint": {shadow.Fingerprint}, "domains": {"sports"}, "reason": {"합성 TDB 미검증 후보 등록 테스트"}}
	if _, err = pool.Exec(ctx, `UPDATE kwave_kdb_admin_users SET role='viewer'`); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"candidate", "mapping", "recheck"} {
		if w = call("POST", tdbPath+"/"+action, tdbForm); w.Code != 403 {
			t.Fatal("viewer tdb write", action, w.Code)
		}
	}
	if w = call("GET", tdbPath, nil); w.Code != 200 || strings.Contains(w.Body.String(), "공통 미검증 후보로 등록") {
		t.Fatal("viewer tdb detail", w.Code)
	}
	if _, err = pool.Exec(ctx, `UPDATE kwave_kdb_admin_users SET role='operator'`); err != nil {
		t.Fatal(err)
	}
	tdbForm.Del("_csrf")
	for _, action := range []string{"candidate", "mapping", "recheck"} {
		if w = call("POST", tdbPath+"/"+action, tdbForm); w.Code != 403 {
			t.Fatal("tdb csrf", action, w.Code)
		}
	}
	tdbForm.Set("_csrf", csrfToken(secret, session))
	if w = call("POST", tdbPath+"/candidate", tdbForm); w.Code != 303 {
		t.Fatal("tdb registration", w.Code, w.Body.String())
	}
	if w = call("POST", tdbPath+"/candidate", tdbForm); w.Code != 303 {
		t.Fatal("tdb registration replay", w.Code, w.Body.String())
	}
	mapping, err := store.TDBMapping(ctx, shadow.ID)
	if err != nil || mapping.EntityID == nil {
		t.Fatal(mapping, err)
	}
	tdbForm.Set("revision", strconv.FormatInt(mapping.Revision, 10))
	tdbForm.Set("entity_id", mapping.EntityID.String())
	tdbForm.Set("entity_revision", "1")
	tdbForm.Set("decision", "confirmed")
	tdbForm.Set("evidence_url", "https://example.test/tdb-identity")
	tdbForm.Set("identity_facts", "동일 외부 ID와 원본의 소속 및 활동 영역을 대조한 합성 식별 사실입니다.")
	if w = call("POST", tdbPath+"/mapping", tdbForm); w.Code != 400 {
		t.Fatal("tdb missing attestation", w.Code)
	}
	tdbForm.Set("attested", "yes")
	if w = call("POST", tdbPath+"/mapping", tdbForm); w.Code != 303 {
		t.Fatal("tdb mapping", w.Code, w.Body.String())
	}
	if w = call("POST", tdbPath+"/mapping", tdbForm); w.Code != 409 {
		t.Fatal("tdb stale decision", w.Code)
	}
	if w = call("POST", tdbPath+"/recheck", tdbForm); w.Code != 303 {
		t.Fatal("tdb recheck", w.Code, w.Body.String())
	}
	if _, err = pool.Exec(ctx, `UPDATE kwave_kdb_admin_users SET role='viewer'`); err != nil {
		t.Fatal(err)
	}
	if w = call("GET", "/admin/kentity/tdb", nil); w.Code != 200 {
		t.Fatal("shadow viewer", w.Code)
	}
	if _, err = pool.Exec(ctx, `UPDATE kwave_kdb_admin_users SET enabled=false`); err != nil {
		t.Fatal(err)
	}
	if w = call("GET", path, nil); w.Code != 403 {
		t.Fatal("revoked account", w.Code)
	}
	if w = call("GET", "/admin/kentity/tdb", nil); w.Code != 403 {
		t.Fatal("revoked shadow access", w.Code)
	}
	if w = call("GET", tdbPath, nil); w.Code != 403 {
		t.Fatal("revoked tdb detail", w.Code)
	}
}

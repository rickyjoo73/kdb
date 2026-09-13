package kdbapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/rickyjoo73/kdb/internal/kdb/readiness"
	"github.com/rickyjoo73/kdb/internal/testdb"
)

// 실 복원 스키마에서만 뜻이 있는 값들. 합성 축약 스키마에는 여기 걸리는 제약이 없다.
const (
	homonymKO   = "가나다동명시험인물" // 복원본의 실제 정본과 겹치지 않는 합성 이름
	homonymTerm = homonymKO
)

// TestRestoredHomonymMatchIsAmbiguous — M06.
//
// 문맥 없이 이름만 왔고 그 이름이 두 UUID 로 갈린다. 서버는 골라 주면 안 된다
// (I06/I07, 식별 계약 §4 "첫 행·유명인·높은 인지도 자동 선택 금지").
// 소비자가 entities[0] 을 정답으로 저장하는 현행 관행이 바로 이 사고의 경로였다.
func TestRestoredHomonymMatchIsAmbiguous(t *testing.T) {
	pool := testdb.Restored(t)
	ctx := context.Background()
	a, b := uuid.New(), uuid.New()
	// ★정리는 넣기 *전에* 건다. 넣다가 실패하면 t.Fatal 이 즉시 끝내버려서, 뒤에
	// 등록한 Cleanup 은 아예 등록되지 않고 앞서 들어간 행만 남는다(실제로 겪음 —
	// 남은 행이 다음 실행의 후보 집합에 끼어들어 엉뚱한 실패를 만들었다).
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM kwave_entities WHERE id = ANY($1)`, []uuid.UUID{a, b})
	})
	// legacy kwave_entities_homonym_key 는 (canonical_ko, entity_type, coalesce(disambig,''))
	// 가 유일해야 공존을 허용한다. 즉 이 픽스처는 "라벨까지 이미 붙은" 가장 잘 정리된
	// 상태다. 그런 상태에서도 문맥 없는 요청은 고를 수 없다는 것이 M06 의 요지다.
	for _, f := range []struct {
		id           uuid.UUID
		en, disambig string
	}{{a, "Homonym Singer", "(가수)"}, {b, "Homonym Actor", "(배우)"}} {
		if _, err := pool.Exec(ctx, `INSERT INTO kwave_entities(id,entity_type,canonical_ko,canonical_en,disambig,status,confidence,last_verified_at) VALUES($1,'person',$2,$3,$4,'active',0.9,now())`, f.id, homonymKO, f.en, f.disambig); err != nil {
			t.Fatal(err)
		}
	}

	h := NewRouterWithOptions(pool, RouterOptions{APIKeys: []string{"fixture-operator"}})
	r := httptest.NewRequest("POST", "/v1/entities/match", strings.NewReader(
		fmt.Sprintf(`{"source_text":%q,"locale":"ja"}`, homonymTerm)))
	r.Header.Set("Authorization", "Bearer fixture-operator")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}

	// 원시 map 으로 읽는다 — 구조체로 받으면 "id 가 없다"는 단언 자체가 불가능하다.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err, w.Body.String())
	}
	if _, ok := raw["id"]; ok {
		t.Fatal("문맥 없는 이름에 단일 id 를 골라 줬다:", w.Body.String())
	}
	var status string
	_ = json.Unmarshal(raw["status"], &status)
	if status != MatchStatusAmbiguous {
		t.Fatal("status:", status, w.Body.String())
	}
	var cands []MatchedEntity
	if err := json.Unmarshal(raw["candidates"], &cands); err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(cands))
	for _, c := range cands {
		got = append(got, c.ID)
	}
	want := []string{a.String(), b.String()}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatal("후보 집합이 다르다 got=", got, "want=", want)
	}

	// 문맥이 붙으면 종전 동작이어야 한다 — 기사 본문까지 ambiguous 로 바꾸면 번역 핫패스가 막힌다.
	r2 := httptest.NewRequest("POST", "/v1/entities/match", strings.NewReader(
		fmt.Sprintf(`{"source_text":%q,"locale":"ja"}`, homonymTerm+" 가 신곡을 냈다")))
	r2.Header.Set("Authorization", "Bearer fixture-operator")
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, r2)
	if w2.Code != 200 {
		t.Fatal(w2.Code, w2.Body.String())
	}
	var raw2 map[string]json.RawMessage
	if err := json.Unmarshal(w2.Body.Bytes(), &raw2); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw2["status"]; ok {
		t.Fatal("문맥 있는 본문까지 ambiguous 로 바뀌었다:", w2.Body.String())
	}
	if _, ok := raw2["entities"]; !ok {
		t.Fatal("entities 배열이 사라졌다(기존 계약 회귀):", w2.Body.String())
	}
}

// TestRestoredHomonymPreparationBindsDistinctIDs — M07.
//
// 같은 이름 두 항목을 한 요청에 담고 각각 다른 UUID 로 묶는다. 준비 항목이
// 서로의 대상 ID 를 침범하지 않아야 하고, 대상을 지정하지 않은 항목은
// 후보가 있어도 자동으로 해소되면 안 된다(§14.1 / I05).
func TestRestoredHomonymPreparationBindsDistinctIDs(t *testing.T) {
	t.Setenv("KDB_READINESS_ENABLED", "1")
	t.Setenv("KDB_COMMON_ENTITY_ENABLED", "1")
	t.Setenv("KDB_COMMON_READINESS_ENABLED", "1")
	t.Setenv("KDB_COMMON_FILL_ENABLED", "")
	pool := testdb.Restored(t)
	ctx := context.Background()
	a, b := uuid.New(), uuid.New()
	var prepID uuid.UUID
	t.Cleanup(func() {
		bg := context.Background()
		if prepID != uuid.Nil {
			_, _ = pool.Exec(bg, `DELETE FROM kentity_preparations WHERE id=$1`, prepID)
		}
		_, _ = pool.Exec(bg, `DELETE FROM kentity_entities WHERE id = ANY($1)`, []uuid.UUID{a, b})
	})
	for _, id := range []uuid.UUID{a, b} {
		if _, err := pool.Exec(ctx, `INSERT INTO kentity_entities(id,entity_type,canonical_ko,origin_system,write_owner,status) VALUES($1,'person',$2,'native','native','candidate')`, id, homonymKO); err != nil {
			t.Fatal(err)
		}
	}

	h := NewRouterWithOptions(pool, RouterOptions{APIKeys: []string{"fixture-operator"}})
	body := fmt.Sprintf(`{"catalog":"common","locales":["ja"],"terms":[
 {"ko":%[1]q,"type":"person","entity_id":%[2]q},
 {"ko":%[1]q,"type":"person","entity_id":%[3]q},
 {"ko":%[1]q,"type":"person"}]}`, homonymKO, a, b)
	r := httptest.NewRequest("POST", "/v1/preparations", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer fixture-operator")
	r.Header.Set("Idempotency-Key", "m07-"+a.String())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var p readiness.Preparation
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err, w.Body.String())
	}
	prepID = p.ID
	if len(p.Items) != 3 {
		t.Fatal("항목 수", len(p.Items))
	}
	// ① 지정한 두 항목은 각자 다른 UUID 에 묶인다.
	for i, want := range []uuid.UUID{a, b} {
		got := p.Items[i].BoundEntityID
		if got == nil || *got != want {
			t.Fatalf("ordinal %d bound=%v want=%v", i, got, want)
		}
	}
	// ② 대상을 안 준 항목은 후보가 둘이어도 해소되지 않는다.
	if p.Items[2].IdentityState != "ambiguous" {
		t.Fatal("미지정 항목 상태:", p.Items[2].IdentityState)
	}
	if p.Items[2].BoundEntityID != nil || p.Items[2].EntityID != nil {
		t.Fatal("미지정 항목이 자동 선택됐다:", p.Items[2])
	}
	if len(p.Items[2].CandidateIDs) < 2 {
		t.Fatal("후보를 둘 다 보여주지 않았다:", p.Items[2].CandidateIDs)
	}
	// ③ 저장 대상 ID 는 DB 에도 서로 다르게 적혀 있다.
	var distinct int
	if err := pool.QueryRow(ctx, `SELECT count(DISTINCT bound_entity_id) FROM kentity_preparation_items WHERE preparation_id=$1 AND bound_entity_id IS NOT NULL`, p.ID).Scan(&distinct); err != nil {
		t.Fatal(err)
	}
	if distinct != 2 {
		t.Fatal("묶인 대상 UUID 종류", distinct)
	}
}

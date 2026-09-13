package kdbadmin

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/rickyjoo73/kdb/internal/testdb"
)

// TestHomonymPickerAgainstRestored — P1.08 최소 UI.
//
// 확인하는 것 넷: ① 동명 후보 두 명이 한 화면에 함께 뜨는가 ② 각자의 저장 대상 ID 가
// 보이는가 ③ 언어별 표기가 후보마다 따로 보이는가 ④ 직업 필터가 겸업을 잘라내지
// 않는가. ④가 핵심이다 — 한 사람이 배우이자 가수면 "가수"로 걸러도 남아야 한다.
func TestHomonymPickerAgainstRestored(t *testing.T) {
	pool := testdb.Restored(t)
	s := renderSmokeServer(t)
	s.pool = pool
	ctx := context.Background()

	// ★이름은 실행마다 다르게 만든다. 종전엔 고정 상수였고 kdbapi 의 동명 시험이
	// 같은 이름·같은 disambig 를 써서, 병렬 실행에서 나중 쪽이
	// kwave_entities_homonym_key 에 걸려 죽었다(전체 실행 3/3 재현, 단독은 통과).
	a, b := uuid.New(), uuid.New()
	ko := "가나다동명시험인물-" + a.String()[:8]
	// 정리는 넣기 전에 건다 — 넣다가 실패하면 뒤에 둔 Cleanup 은 등록되지 않는다.
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM kwave_entity_person_details WHERE entity_id = ANY($1)`, []uuid.UUID{a, b})
		_, _ = pool.Exec(bg, `DELETE FROM kwave_entities WHERE id = ANY($1)`, []uuid.UUID{a, b})
	})
	// A: 배우이면서 가수(겸업) — 한 사람, 하나의 ID. B: 같은 이름의 다른 사람.
	// disambig 가 다르지 않으면 legacy kwave_entities_homonym_key 가 두 번째 사람을 거부한다.
	if _, err := pool.Exec(ctx, `INSERT INTO kwave_entities(id,entity_type,canonical_ko,canonical_ja,disambig,status,confidence) VALUES($1,'person',$2,'表記-A','(배우)','active',0.9),($3,'person',$2,'表記-B','(가수)','active',0.8)`, a, ko, b); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO kwave_entity_person_details(entity_id,primary_role,secondary_roles,agency,birth_year) VALUES($1,'actor',ARRAY['singer']::person_role[],'합성 소속사 A',1990),($2,'singer','{}'::person_role[],'합성 소속사 B',1975)`, a, b); err != nil {
		t.Fatal(err)
	}

	get := func(path string) string {
		t.Helper()
		w := httptest.NewRecorder()
		s.entityHomonyms(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatal(path, w.Code)
		}
		return w.Body.String()
	}

	body := get("/admin/entities/homonyms")
	for _, want := range []string{ko, a.String(), b.String(), "表記-A", "表記-B", "합성 소속사 A", "합성 소속사 B"} {
		if !strings.Contains(body, want) {
			t.Fatal("화면에 없다:", want)
		}
	}
	// 겸업은 ID 를 나누는 이유가 아니라는 것을 화면이 말해야 한다.
	if !strings.Contains(body, "한 사람 = 하나의 ID") {
		t.Fatal("정체성 규칙 안내가 없다")
	}

	// 직업 필터: A 의 부직업으로 걸러도 A 가 남아야 한다(겸업 절단 금지).
	filtered := get("/admin/entities/homonyms?role=singer")
	if !strings.Contains(filtered, a.String()) {
		t.Fatal("부직업으로 필터하니 겸업자가 사라졌다 — 한 사람의 직업을 하나만 본 것")
	}
	// 후보를 고르려면 그룹 전원이 보여야 한다.
	if !strings.Contains(filtered, b.String()) {
		t.Fatal("필터가 같은 그룹의 다른 후보를 지웠다 — 비교가 불가능해진다")
	}
	// 아무도 갖지 않은 직업으로 거르면 이 그룹은 사라져야 한다(필터가 실제로 동작).
	if none := get("/admin/entities/homonyms?role=politician"); strings.Contains(none, a.String()) {
		t.Fatal("직업 필터가 아무것도 거르지 않았다")
	}
}

// TestHomonymRoutesAreRegisteredAndCSRFProtected — 핸들러가 있는데 라우터에 없어
// 404 로 죽어 있던 경로다. 살린 김에 CSRF 가 실제로 붙었는지 라우터를 통해 확인한다.
func TestHomonymRoutesAreRegisteredAndCSRFProtected(t *testing.T) {
	h := NewRouter(nil, Options{SessionSecret: []byte("test-secret-test-secret-0123456789")})
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/admin/entities/"+uuid.NewString()+"/disambig", strings.NewReader("disambig=(가수)"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(w, r)
	if w.Code == 404 {
		t.Fatal("disambig 경로가 다시 등록에서 빠졌다")
	}
	if w.Code == 303 || w.Code == 200 {
		t.Fatal("토큰 없는 POST 가 통과했다:", w.Code)
	}
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, httptest.NewRequest("GET", "/admin/entities/homonyms", nil))
	if w2.Code == 404 {
		t.Fatal("동명 후보 화면이 다시 등록에서 빠졌다")
	}
}

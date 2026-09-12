package kdbadmin

import (
	"bytes"
	"github.com/google/uuid"
	"github.com/rickyjoo73/kdb/internal/kentity"
	"strings"
	"testing"
	"time"
)

func tdbMappingPreview(state string) map[string]any {
	id := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	source := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	target := uuid.MustParse("33333333-3333-4333-8333-333333333333")
	typ, _ := kentity.TDBTypeFor("person")
	m := &kentity.TDBMapping{Shadow: kentity.TDBShadow{ID: id, TDBID: source, QID: "Q123", SourceType: "person", State: "review", Generation: 2, Fingerprint: strings.Repeat("a", 64), ObservedAt: time.Now(), Proposal: &kentity.Proposal{KO: "합성 독립 후보", ObservedAt: time.Now()}}, Status: "unmapped", Type: typ, Fresh: true, CanRecheck: true, Events: []kentity.TDBMappingEvent{{Actor: "synthetic-test", Action: "binding_staged", At: time.Now()}}}
	if state != "new" && state != "error" {
		m.EntityID = &target
		m.Revision = 2
		m.Status = "review"
		m.Candidates = []kentity.Entity{{ID: target, KO: "합성 독립 후보", Type: "person", Status: "candidate", Origin: "tdb", WriteOwner: "native", Revision: 1}}
	}
	if state == "confirmed" {
		m.Status = "confirmed"
		m.Current = true
	}
	if state == "stale" || state == "locked" {
		m.Fresh = false
		m.CanRecheck = false
	}
	if state == "locked" {
		m.Shadow.Locked = true
	}
	return map[string]any{"title": "TDB 공통 연결 관리", "nav": activeNavItems("/admin/kentity/tdb/" + id.String()), "mapping": m, "loadError": state == "error", "canManage": state != "viewer", "csrf": "synthetic-fixture-only"}
}
func TestTDBMappingUISeparatesRegistrationIdentityAndLocaleReview(t *testing.T) {
	t.Setenv("KDB_COMMON_ENTITY_ENABLED", "1")
	t.Setenv("KDB_TDB_SHADOW_ENABLED", "1")
	t.Setenv("KDB_TDB_MAPPING_ENABLED", "1")
	s := renderSmokeServer(t)
	for _, state := range []string{"new", "candidate", "confirmed", "viewer", "stale", "locked", "error"} {
		var b bytes.Buffer
		if err := s.tmpl.ExecuteTemplate(&b, "tdb_mapping.html", tdbMappingPreview(state)); err != nil {
			t.Fatal(err)
		}
		body := b.String()
		if strings.Count(body, `aria-current="page"`) != 1 || !strings.Contains(body, "후보 등록, 동일 대상 연결, 언어 표기 승인은 별개") {
			t.Fatal("navigation or policy", state)
		}
		if state == "new" && !strings.Contains(body, "공통 미검증 후보로 등록") {
			t.Fatal("missing registration")
		}
		if state == "viewer" && strings.Contains(strings.ReplaceAll(body, `method="POST" action="/admin/logout"`, ""), `method="POST"`) {
			t.Fatal("viewer write")
		}
		if (state == "stale" || state == "locked" || state == "confirmed") && strings.Contains(body, "이 UUID로 연결 검수 저장") {
			t.Fatal("stale or already approved action", state)
		}
	}
}

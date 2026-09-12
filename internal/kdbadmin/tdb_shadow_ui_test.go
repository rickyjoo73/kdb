package kdbadmin

import (
	"bytes"
	"github.com/google/uuid"
	"github.com/rickyjoo73/kdb/internal/kentity"
	"strings"
	"testing"
	"time"
)

func shadowPreview(state string) map[string]any {
	d := map[string]any{"title": "TDB 연결 비교", "nav": activeNavItems("/admin/kentity/tdb"), "state": "", "loadError": state == "error"}
	if state != "empty" && state != "error" {
		r := kentity.TDBShadow{ID: uuid.MustParse("22222222-2222-4222-8222-222222222222"), TDBID: uuid.MustParse("11111111-1111-4111-8111-111111111111"), QID: "Q123", Method: "synthetic-ID-only", Score: 0.9, State: state, ObservedAt: time.Now()}
		if state == "review" {
			r.Attempts = 1
			r.Proposal = &kentity.Proposal{KO: "합성 비교 항목", QID: "Q123", ObservedAt: time.Now(), License: "CC0-1.0", ExistingIDs: []uuid.UUID{r.TDBID}, Names: []kentity.Name{{Locale: "en", Value: "Synthetic Source Label"}, {Locale: "pt", Value: "Synthetic Person"}}}
		}
		d["items"] = []kentity.TDBShadow{r}
	}
	return d
}
func TestTDBShadowUISeparatesClaimsFromApprovedNames(t *testing.T) {
	t.Setenv("KDB_COMMON_ENTITY_ENABLED", "1")
	t.Setenv("KDB_TDB_SHADOW_ENABLED", "1")
	s := renderSmokeServer(t)
	for _, state := range []string{"pending", "review", "blocked", "empty", "error"} {
		var b bytes.Buffer
		if err := s.tmpl.ExecuteTemplate(&b, "tdb_shadow.html", shadowPreview(state)); err != nil {
			t.Fatal(err)
		}
		body := b.String()
		if !strings.Contains(body, "동일인 연결") || strings.Contains(strings.ReplaceAll(body, `method="POST" action="/admin/logout"`, ""), `method="POST"`) || strings.Count(body, `aria-current="page"`) != 1 {
			t.Fatal("misleading or unsafe shadow UI", state)
		}
		if state == "review" && !strings.Contains(body, "미검증 · 준비 완료 아님") {
			t.Fatal("unverified source shown ready")
		}
	}
}

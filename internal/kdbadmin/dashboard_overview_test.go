package kdbadmin

import (
	"bytes"
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rickyjoo73/kdb/internal/testdb"
)

func TestDashboardOverviewUnitsAndTimeWindow(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	_, err := pool.Exec(ctx, `
INSERT INTO kwave_entities(canonical_ko,canonical_en,status,verification_tier) VALUES
 ('동명','same','active','authoritative'),('동명','same','active','evidenced'),
 ('미검증',NULL,'active','unverified'),('미분류',NULL,'active',NULL),
 ('후보',NULL,'candidate','authoritative'),('기각',NULL,'rejected','authoritative');
INSERT INTO kwave_persons DEFAULT VALUES;
INSERT INTO kwave_kdb_corrections(status) VALUES('pending'),('proposed'),('accepted');
INSERT INTO kwave_entity_research_queue(entity_ko,status,created_at,finished_at) VALUES
 ('대기','pending',now(),NULL),('진행','in_progress',now(),NULL),
 ('오래걸림','done',now()-interval '3 days',now()-interval '1 hour'),
 ('빠른종료','done',now()-interval '1 minute',now()),
 ('실패종료','failed',now()-interval '4 minutes',now()),
 ('지난종료','done',now()-interval '4 days',now()-interval '2 days');`)
	if err != nil {
		t.Fatal(err)
	}
	o, err := loadDashboardOverview(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	want := dashboardOverview{Active: 4, LegacyPersons: 1, Candidates: 1, Corrections: 2,
		ConflictGroups: 2, Authoritative: 1, Evidenced: 1, Unverified: 1, Untiered: 1,
		Pending: 1, InProgress: 1, Finished24h: 3, OverTwoMinutes24h: 2}
	if o != want {
		t.Fatalf("got %+v; want %+v", o, want)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := loadDashboardOverview(cancelled, pool); err == nil {
		t.Fatal("cancelled query must not return healthy zero counts")
	}
}

func TestDashboardEmptyAndErrorAreNotSuccess(t *testing.T) {
	s := renderSmokeServer(t)
	for _, failed := range []bool{false, true} {
		var out bytes.Buffer
		err := s.tmpl.ExecuteTemplate(&out, "dashboard.html", map[string]any{
			"title": "운영 개요", "overview": dashboardOverview{Active: 987654},
			"overviewError": failed, "supplyError": failed,
		})
		if err != nil {
			t.Fatal(err)
		}
		body := out.String()
		for _, misleading := range []string{"% 공식", "자동 파이프라인이 정상 가동", "즉시 채움", "강제 채움"} {
			if strings.Contains(body, misleading) {
				t.Fatalf("misleading status: %s", misleading)
			}
		}
		if failed {
			if !strings.Contains(body, "운영 현황 집계 실패") || !strings.Contains(body, "다국어 현황 집계 실패") ||
				strings.Contains(body, "987654") || strings.Contains(body, "집계된 언어 누락이 없습니다") {
				t.Fatal("failed query must hide invalid metrics and empty-state claims")
			}
		} else if !strings.Contains(body, "미계측") || !strings.Contains(body, "소요 시간 집계 대상 없음") {
			t.Fatal("no completed work must not imply zero readiness latency")
		}
	}
}

func TestDashboardHandlerKeepsDatabaseErrorsPrivate(t *testing.T) {
	s := renderSmokeServer(t)
	s.pool = testdb.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := httptest.NewRequest("GET", "/admin/", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	s.dashboard(w, r)
	body := w.Body.String()
	if !strings.Contains(body, "운영 현황 집계 실패") || strings.Contains(body, "context canceled") {
		t.Fatal("handler must show generic failure, not database/internal error details")
	}
	if !strings.Contains(body, `aria-current="page"`) {
		t.Fatal("current navigation missing")
	}
}

func TestActiveNavigationUsesLongestPath(t *testing.T) {
	for path, expected := range map[string]string{
		"/admin/": "/admin", "/admin/entities/a-uuid": "/admin/entities",
		"/admin/entities/conflicts": "/admin/entities/conflicts",
		"/admin/entities/review":    "/admin/entities/review", "/admin/entity-not-found": "",
	} {
		var active []string
		for _, n := range activeNavItems(path) {
			if n.Active {
				active = append(active, n.Path)
			}
		}
		if strings.Join(active, ",") != expected {
			t.Errorf("%s: active %v; want %s", path, active, expected)
		}
	}
}

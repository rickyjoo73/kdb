package kdbadmin

import (
	"bytes"
	"github.com/rickyjoo73/kdb/internal/kentity"
	"os"
	"path/filepath"
	"testing"
)

// Optional synthetic fixtures for browser tests. No DB, auth cookies, user
// records or credentials are used. The directory must already exist.
func TestAdminUIPreviewFixtures(t *testing.T) {
	dir := os.Getenv("KDB_ADMIN_PREVIEW_DIR")
	if dir == "" {
		t.Skip("KDB_ADMIN_PREVIEW_DIR not set")
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		t.Fatal("preview directory must already exist")
	}
	s := renderSmokeServer(t)
	t.Setenv("KDB_COMMON_ENTITY_ENABLED", "1")
	t.Setenv("KDB_TDB_SHADOW_ENABLED", "1")
	t.Setenv("KDB_TDB_MAPPING_ENABLED", "1")
	for _, state := range []string{"new", "candidate", "confirmed", "viewer", "stale", "locked", "class_conflict", "error"} {
		writePreview(t, s, dir, "tdb-mapping-"+state+".html", "tdb_mapping.html", tdbMappingPreview(state))
	}
	for _, state := range []string{"pending", "review", "blocked", "empty", "error"} {
		writePreview(t, s, dir, "tdb-shadow-"+state+".html", "tdb_shadow.html", shadowPreview(state))
	}
	writePreview(t, s, dir, "preparations-common.html", "preparations.html", commonReadinessPreview())
	writePreview(t, s, dir, "preparations-common-fill.html", "preparations.html", commonFillPreview())
	writePreview(t, s, dir, "kentity-auto.html", "kentity.html", commonAutoEntityPreview())
	for _, state := range []string{"operator", "viewer", "error", "adopted", "locked"} {
		writePreview(t, s, dir, "ownership-"+state+".html", "kentity.html", ownershipPreview(state))
	}
	for _, state := range []string{"pending", "running", "review", "approval", "no_match", "failed", "viewer", "empty", "error"} {
		writePreview(t, s, dir, "resolution-"+state+".html", "kentity.html", resolutionPreview(state))
	}
	for _, state := range []string{"list", "detail", "error", "viewer", "empty"} {
		writePreview(t, s, dir, "kentity-"+state+".html", "kentity.html", commonPreview(state))
		writePreview(t, s, dir, "mappings-"+state+".html", "kentity_mappings.html", mappingPreview(state))
	}
	for _, state := range []string{"list", "empty", "error", "detail", "viewer"} {
		writePreview(t, s, dir, "preparations-"+state+".html", "preparations.html", preparationPreview(state))
	}
	supply := []wfSupply{
		{Locale: "en", Missing: 46, Eligible: 12, Blocked: 30, PolicyBlocked: 20, Excluded: 4},
		{Locale: "ja", Missing: 1039, Eligible: 6, Blocked: 1033, PolicyBlocked: 980},
		{Locale: "zh", Missing: 1462, Eligible: 7, Blocked: 1455, PolicyBlocked: 1200},
	}
	for _, state := range []string{"populated", "empty", "error"} {
		o := dashboardOverview{}
		if state == "populated" {
			o = dashboardOverview{Active: 12644, LegacyPersons: 5698, Candidates: 546,
				Corrections: 2, ConflictGroups: 73, Authoritative: 9000, Evidenced: 3000,
				Unverified: 600, Untiered: 44, Pending: 3, InProgress: 2, Finished24h: 130, OverTwoMinutes24h: 4}
		}
		data := map[string]any{"title": "운영 개요", "nav": activeNavItems("/admin"),
			"overview": o, "overviewError": state == "error", "supplyError": state == "error",
			"observedAt": "UI 테스트용 예시 데이터 · 운영 수치 아님"}
		data["commonEnabled"], data["catalogError"] = true, state == "error"
		data["catalog"] = kentity.CatalogPage{}
		if state == "populated" {
			data["supply"] = supply
			data["catalog"] = commonPreview("list")["catalog"]
		}
		writePreview(t, s, dir, "dashboard-"+state+".html", "dashboard.html", data)
	}
	writePreview(t, s, dir, "workflow.html", "workflow_board.html", map[string]any{
		"title": "워크플로우 보드", "nav": activeNavItems("/admin/workflow"),
		"now": "테스트 예시", "readiness": wfReadiness{Requests: 130, GateStopped: 39, Candidate: 34, ActiveGaps: 32, FormsPresent: 25},
		"supply": supply, "lastAutopilot": "12분 전", "lastResolve": "5분 전", "lastFill": "30분 전",
		"tiles":  []wfTile{{Label: "작업 종료 24h", Value: "130", Href: "/admin/ondemand/queue"}},
		"stages": []*wfStage{{Key: "INTAKE", Label: "① 유입 대기", CountLabel: "대기", EmptyText: "현재 대기 없음", Accent: "bg-sky-50"}},
	})
}

func writePreview(t *testing.T, s *Server, dir, filename, template string, data map[string]any) {
	t.Helper()
	var out bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&out, template, data); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, filename), out.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
}

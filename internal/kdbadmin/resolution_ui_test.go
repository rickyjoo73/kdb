package kdbadmin

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rickyjoo73/kdb/internal/kentity"
)

func resolutionPreview(state string) map[string]any {
	d := commonPreview("detail")
	d["resolverEnabled"] = true
	d["csrf"] = "synthetic-fixture-only"
	j := &kentity.Resolution{ID: uuid.MustParse("44444444-4444-4444-8444-444444444444"), State: state, Attempts: 1, Generation: 1, UpdatedAt: time.Date(2026, 9, 12, 5, 0, 0, 0, time.UTC)}
	switch state {
	case "empty":
		d["canResearch"] = true
	case "error":
		d["researchError"] = true
	case "review", "viewer", "approval":
		j.State = "review"
		j.Proposals = []kentity.Proposal{{QID: "Q123", KO: "합성 정치·스포츠 인물", Description: "브라우저 검사용 합성 원천 기록", SourceURL: "https://example.test/entity", License: "CC0-1.0", ExistingIDs: []uuid.UUID{uuid.MustParse("22222222-2222-4222-8222-222222222222")}, Names: []kentity.Name{{Locale: "en", Value: "Synthetic Person", Status: "unverified"}, {Locale: "pt", Value: "Synthetic Person", Status: "unverified"}}}}
		d["resolution"] = j
		if state == "approval" {
			j.Proposals[0].ExistingIDs = nil
			d["canApproveResearch"] = true
		}
	default:
		d["resolution"] = j
		d["canCancelResearch"] = state == "running" || state == "pending" || state == "failed"
	}
	return d
}
func TestResolutionUITellsResearchApartFromApproval(t *testing.T) {
	t.Setenv("KDB_COMMON_ENTITY_ENABLED", "1")
	s := renderSmokeServer(t)
	for _, state := range []string{"pending", "running", "review", "approval", "no_match", "failed", "viewer", "empty", "error"} {
		var b bytes.Buffer
		if err := s.tmpl.ExecuteTemplate(&b, "kentity.html", resolutionPreview(state)); err != nil {
			t.Fatal(err)
		}
		body := b.String()
		if !strings.Contains(body, "번역 승인 표기에 포함하지 않습니다") {
			t.Fatal("research counted as ready")
		}
		if state == "error" && (!strings.Contains(body, "자동 조사 원장 조회 실패") || strings.Contains(body, "아직 자동 조사를 요청하지")) {
			t.Fatal("error as empty")
		}
		if state == "viewer" && strings.Contains(body, "<button class=\"border rounded p-2\">자동 조사 취소") {
			t.Fatal("viewer mutation")
		}
		if state == "viewer" && strings.Contains(body, "선택 정체성·기록 표기 승인") {
			t.Fatal("viewer approval")
		}
	}
}

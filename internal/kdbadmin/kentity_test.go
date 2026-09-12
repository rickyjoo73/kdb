package kdbadmin

import (
	"bytes"
	"github.com/google/uuid"
	"github.com/rickyjoo73/kdb/internal/kentity"
	"strings"
	"testing"
)

func commonPreview(state string) map[string]any {
	e := kentity.Entity{ID: uuid.MustParse("22222222-2222-4222-8222-222222222222"), KO: "공통 Entity 합성 예시", Type: "person", Origin: "native", Status: "candidate", Revision: 1, Domains: []string{"politics", "sports"}, Names: []kentity.Name{{Locale: "ko", Value: "공통 Entity 합성 예시", Kind: "canonical", Form: "unknown", Status: "unverified", Source: "operator-candidate", Owner: "native"}}}
	data := map[string]any{"title": "공통 Entity 관리", "nav": activeNavItems("/admin/kentity"), "requestKey": "synthetic-key", "csrf": "synthetic-fixture-only", "mappingReview": 3}
	switch state {
	case "list":
		data["items"] = []kentity.Entity{e}
		data["canCreate"] = true
	case "detail":
		data["entity"] = &e
		data["detail"] = true
	case "error":
		data["loadError"] = true
	case "viewer":
		data["items"] = []kentity.Entity{e}
	}
	return data
}

func TestCommonCatalogTemplatesAndUnverifiedWarning(t *testing.T) {
	t.Setenv("KDB_COMMON_ENTITY_ENABLED", "1")
	s := renderSmokeServer(t)
	for _, state := range []string{"list", "detail", "error", "viewer", "empty"} {
		var b bytes.Buffer
		if err := s.tmpl.ExecuteTemplate(&b, "kentity.html", commonPreview(state)); err != nil {
			t.Fatal(err)
		}
		body := b.String()
		if state == "error" && !strings.Contains(body, "공통 Entity 조회 실패") {
			t.Fatal("error concealed")
		}
		if state == "viewer" && strings.Contains(body, "미검증 후보로 등록") {
			t.Fatal("viewer offered write")
		}
		if state == "detail" && !strings.Contains(body, "자동 발행용 승인 표기로 사용하지 않습니다") {
			t.Fatal("candidate counted as serving ready")
		}
	}
}

func mappingPreview(state string) map[string]any {
	id := uuid.MustParse("33333333-3333-4333-8333-333333333333")
	p := kentity.Profile{ID: id, KO: "동명이인 합성 후보", EN: "Synthetic Person", Role: "actor", Agency: "합성 소속", BirthYear: "1990", Revision: 1, Fingerprint: "synthetic-target"}
	m := kentity.Mapping{SourceID: id, Status: "review", Reason: "이름 일치 후보로 동일인 여부 검수 필요", Revision: 1, Fingerprint: "synthetic-source", Source: p, Candidates: []kentity.Profile{p}, CandidateIDs: []uuid.UUID{id}}
	d := map[string]any{"title": "인물 연결 검수", "nav": activeNavItems("/admin/kentity/mappings"), "csrf": "synthetic-fixture-only", "status": "review"}
	switch state {
	case "list":
		d["items"] = []kentity.Mapping{m}
	case "detail", "viewer":
		d["detail"] = true
		d["mapping"] = &m
		d["canDecide"] = state == "detail"
	case "error":
		d["loadError"] = true
	}
	return d
}
func TestMappingReviewTemplateRoleAndEvidenceWarnings(t *testing.T) {
	t.Setenv("KDB_COMMON_ENTITY_ENABLED", "1")
	s := renderSmokeServer(t)
	for _, state := range []string{"list", "detail", "viewer", "error", "empty"} {
		var b bytes.Buffer
		if err := s.tmpl.ExecuteTemplate(&b, "kentity_mappings.html", mappingPreview(state)); err != nil {
			t.Fatal(err)
		}
		body := b.String()
		if state == "viewer" && strings.Contains(body, "이 UUID로 연결 승인 기록") {
			t.Fatal("viewer write shown")
		}
		if state == "detail" && (!strings.Contains(body, "자동 복사하지 않습니다") || !strings.Contains(body, "entity_fingerprint")) {
			t.Fatal("review policy missing")
		}
	}
}

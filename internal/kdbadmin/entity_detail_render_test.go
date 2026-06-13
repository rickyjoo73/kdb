package kdbadmin

import (
	"bytes"
	"html/template"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestEntityDetailRendersAllSections 는 entity_detail.html 이 핸들러가 채우는
// 모든 관련 섹션(다국어 표기·9개 alias·인물상세·동명이인 형제·관계·외부ref·
// resolution·research 이력·observations)을 실제로 렌더하는지 검증한다.
// (이전 버그: 핸들러는 다 가져오는데 템플릿이 일부만 출력.)
func TestEntityDetailRendersAllSections(t *testing.T) {
	sub, err := fs.Sub(templatesFS, "templates")
	if err != nil {
		t.Fatalf("templates sub: %v", err)
	}
	tmpl := template.New("").Funcs(funcMap())
	tmpl, err = tmpl.ParseFS(sub, "*.html")
	if err != nil {
		t.Fatalf("parse templates: %v", err)
	}

	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	by := 1990
	cc := 3
	ms := 1200
	score := 0.91
	e := entityDetail{
		ID:    uuid.New(),
		Type:  "person",
		Ko:    "유재석",
		En:    mark("Yoo Jae-suk", "wikidata-label"),
		Ja:    mark("ユ・ジェソク", "codex"),
		Vi:    mark("Yoo Jae-suk", "codex"),
		IDLang: mark("Yoo Jae-suk", "codex"),
		Es:     mark("Yoo Jae-suk", "codex"),
		PtBr:   mark("Yoo Jae-suk", "codex"),
		ZhHant: mark("劉在錫", "codex"),
		Zh:     mark("刘在锡", "codex"),
		AliasesKo:     []string{"유느님"},
		AliasesEn:     []string{"Yu Jae-seok"},
		AliasesJa:     []string{"ユ・ジェソク"},
		AliasesVi:     []string{"Yoo Jae Suk"},
		AliasesID:     []string{"Yoo Jae Suk"},
		AliasesEs:     []string{"Yoo Jae Suk"},
		AliasesPtBr:   []string{"Yoo Jae Suk"},
		AliasesZhHant: []string{"劉在錫"},
		AliasesZh:     []string{"刘在锡"},
		Confidence:    0.99,
		Status:        "active",
		Disambig:      "(Antenna)",
		NeedsDisambig: true,
		SourceURLs:    []string{"https://example.com/a"},
		CreatedAt:     now,
		UpdatedAt:     now,
		LastVerifiedAt: now,
		Person: &personDetail{
			PrimaryRole:    "host",
			SecondaryRoles: []string{"comedian"},
			Groups:         []string{},
			Agency:         "Antenna",
			Gender:         "male",
			BirthYear:      &by,
			NotableWorks:   []string{"무한도전", "런닝맨"},
		},
		Siblings: []siblingRow{{
			ID: uuid.New(), EntityType: "person", Disambig: "(FNC Entertainment)",
			PrimaryRole: "singer", Agency: "FNC", Confidence: 0.8,
			Status: "active", NeedsDisambig: true,
		}},
		Relations: []relationRow{{
			Direction: "from→to", RelationType: "member_of",
			EntityID: uuid.New(), EntityKo: "무한도전", EntityType: "show",
			Confidence: 0.9, SourceURLs: []string{"https://x"},
		}},
		ExternalRefs: []externalRefRow{{
			Provider: "wikidata", ExternalID: "Q489728",
			URL: "https://www.wikidata.org/wiki/Q489728", Confidence: 0.95, FetchedAt: now,
		}},
		Resolution: []resolutionRow{{
			Provider: "wikidata", Status: "matched",
			CandidateCount: &cc, MatchScore: &score, DurationMs: &ms,
			ErrorText: "", AttemptedAt: now,
		}},
		History: []historyRow{{
			Status: "completed", Type: "person", Attempts: 1,
			Hint: "host", LastError: "", CreatedAt: now,
		}},
		Observations: []observationRow{{
			ID: 1, Locale: "ja", Spelling: "ユ・ジェソク",
			SourceDomain: "news.example", SourceURL: "https://news.example/1",
			ObservedAt: now, Confidence: 0.88,
		}},
	}

	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "entity_detail.html", map[string]any{
		"title": e.Ko, "e": e, "flash": "", "page": "/admin/entities",
	}); err != nil {
		t.Fatalf("execute entity_detail.html: %v", err)
	}
	out := buf.String()

	mustContain := []string{
		"인물 상세",
		"동명이인 형제",
		"(FNC Entertainment)", // sibling disambig
		"관계 (1)",
		"외부 레퍼런스 (1)",
		"resolution 시도 (1)",
		"research 큐 이력 (1)",
		"최근 observations (1)",
		"(Antenna)",       // own disambig in heading
		"needs_disambig",  // flag rendered
		// 9개 alias 언어 모두
		">ko<", ">en<", ">ja<", ">vi<", ">zh<", ">zh-Hant<", ">es<", ">id<", ">pt-BR<",
		// 인물 필드
		"무한도전", "Antenna", "1990",
		// 외부 ref / resolution 값
		"Q489728", "matched",
	}
	for _, s := range mustContain {
		if !strings.Contains(out, s) {
			t.Errorf("rendered output missing %q", s)
		}
	}
}

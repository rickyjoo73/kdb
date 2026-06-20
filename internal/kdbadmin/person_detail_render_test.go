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

// TestPersonDetailRendersAllLocales 는 person_detail.html 이 9개 언어 표기 전체(채워진
// 것 + 누락)와 인물 필드를 실제로 렌더하는지 검증한다. 배포 시 ParseFS 실패=앱 크래시라
// 파싱+실행을 사전 보장한다.
func TestPersonDetailRendersAllLocales(t *testing.T) {
	sub, err := fs.Sub(templatesFS, "templates")
	if err != nil {
		t.Fatalf("templates sub: %v", err)
	}
	tmpl, err := template.New("").Funcs(funcMap()).ParseFS(sub, "*.html")
	if err != nil {
		t.Fatalf("parse templates: %v", err)
	}

	by := 1983
	x := personRow{
		ID:             uuid.New(),
		NameKo:         "아이유",
		NameEn:         "IU",
		NameJa:         "アイユー",
		NameZh:         "IU",
		NameZhHant:     "IU",
		NameVi:         "", // 누락 — '— 누락' 으로 렌더돼야 함
		NameEs:         "IU",
		NameId:         "IU",
		NamePtBr:       "",
		Role:           "singer",
		SecondaryRoles: []string{"actor"},
		Aliases:        []string{"이지은"},
		Agency:         "EDAM",
		Groups:         []string{},
		BirthYear:      &by,
		Gender:         "F",
		NotableWorks:   []string{"호텔 델루나"},
		Confidence:     0.97,
		OperatorLocked: true,
		SourceURLs:     []string{"https://www.wikidata.org/wiki/Q485480"},
		LastVerifiedAt: time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC),
	}
	x.Locales = personLocales(x, false)

	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "person_detail.html", map[string]any{
		"title": x.NameKo, "x": x, "entityID": "abc-123", "page": "/admin/persons",
	}); err != nil {
		t.Fatalf("execute person_detail.html: %v", err)
	}
	out := buf.String()

	mustContain := []string{
		"언어별 표기 (9개 locale)",
		"아이유", "IU", "アイユー", // 채워진 표기
		"누락",                // 빈 locale(vi/pt_br) 표시
		"EDAM", "1983", "호텔 델루나", "이지은",
		"다국어 SSOT 엔티티 보기", // entityID 있을 때 교차링크
		">KO<", ">EN<", ">JA<", ">ZH<", ">ZH-Hant<", ">VI<", ">ES<", ">ID<", ">PT-BR<",
	}
	for _, s := range mustContain {
		if !strings.Contains(out, s) {
			t.Errorf("person_detail 렌더 결과에 %q 누락", s)
		}
	}
}

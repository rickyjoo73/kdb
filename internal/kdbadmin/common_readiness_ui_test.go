package kdbadmin

import (
	"bytes"
	"encoding/json"
	"github.com/rickyjoo73/kdb/internal/kdb/readiness"
	"github.com/rickyjoo73/kdb/internal/kentity"
	"strings"
	"testing"
)

func commonReadinessPreview() map[string]any {
	d := preparationPreview("detail")
	d["commonPolicy"] = true
	d["canRetry"] = false
	p := d["request"].(*readiness.Preparation)
	p.PolicyVersion = readiness.CommonPolicyVersion
	p.Items[0].Locales[0].Proof = json.RawMessage(`{"policy_version":"common-reviewed-names-v1","entity_id":"11111111-1111-4111-8111-111111111111","entity_revision":2,"name":{"locale":"en","value":"Synthetic Person","name_revision":1,"source_url":"https://example.test/identity"}}`)
	p.Items[0].Locales[0].Reason = "reviewed exact-locale recorded name; not a claim of official naming"
	return d
}
func TestCommonReadinessLinksAndProofUI(t *testing.T) {
	s := renderSmokeServer(t)
	var b bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&b, "preparations.html", commonReadinessPreview()); err != nil {
		t.Fatal(err)
	}
	body := b.String()
	if strings.Contains(body, `href="/admin/entities/11111111-1111-4111-8111-111111111111"`) || strings.Contains(body, "제한 재시도 요청") || !strings.Contains(body, "사용한 표기·근거 버전") || !strings.Contains(body, "공통 검수 정책") {
		t.Fatal("common policy UI mismatch")
	}
}

func commonFillPreview() map[string]any {
	d := commonReadinessPreview()
	d["commonFillEnabled"], d["canRetry"] = true, true
	d["commonRetryLocales"] = map[string]bool{"0/ja": true}
	p := d["request"].(*readiness.Preparation)
	p.Items[0].Locales = []readiness.Locale{
		{Locale: "en", State: "ready", Value: "Synthetic Person", Source: "wikidata-label", Reason: "common_auto_recorded_name", Proof: json.RawMessage(`{"name":{"verification_method":"policy:common-anchored-fill-v1"},"human_locale_review":false}`)},
		{Locale: "ja", State: "failed", Reason: "source_error"},
		{Locale: "pt-BR", State: "no_evidence", Reason: "no reviewed exact-locale canonical name"},
	}
	return d
}
func TestCommonFillUIDistinguishesAutomaticVerificationAndEligibleRetry(t *testing.T) {
	s := renderSmokeServer(t)
	var b bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&b, "preparations.html", commonFillPreview()); err != nil {
		t.Fatal(err)
	}
	body := b.String()
	if !strings.Contains(body, "사람의 언어 검수") || strings.Count(body, "제한 재시도 요청") != 1 || strings.Contains(body, `name="locale" value="pt-BR"`) {
		t.Fatal("automatic policy or retry availability misleading")
	}
}

func commonAutoEntityPreview() map[string]any {
	d := commonPreview("detail")
	e := d["entity"].(*kentity.Entity)
	e.Status = "active"
	e.Names = append(e.Names, kentity.Name{Locale: "ja", Value: "テスト人物", Kind: "canonical", Form: "recorded", Status: "verified", Source: "wikidata-label", Owner: "native", Verification: "policy:common-anchored-fill-v1", SourceURL: "https://example.test/recorded-label"})
	return d
}
func TestCommonEntityShowsAutomaticCheckAndSourceLink(t *testing.T) {
	s := renderSmokeServer(t)
	var b bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&b, "kentity.html", commonAutoEntityPreview()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "검수된 정체성 기반 자동 확인") || !strings.Contains(b.String(), `href="https://example.test/recorded-label"`) {
		t.Fatal("automatic verification provenance missing")
	}
}

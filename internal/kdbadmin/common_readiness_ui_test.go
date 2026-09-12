package kdbadmin

import (
	"bytes"
	"encoding/json"
	"github.com/rickyjoo73/kdb/internal/kdb/readiness"
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

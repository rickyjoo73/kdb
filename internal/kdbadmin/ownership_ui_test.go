package kdbadmin

import (
	"bytes"
	"github.com/rickyjoo73/kdb/internal/kentity"
	"strings"
	"testing"
)

func ownershipPreview(state string) map[string]any {
	d := commonPreview("detail")
	e := d["entity"].(*kentity.Entity)
	e.Origin = "kdb"
	e.WriteOwner = "kdb"
	e.Status = "rejected"
	e.Type = "organization"
	d["ownershipEnabled"] = true
	d["legacyScope"] = &kentity.LegacyOwnership{Status: "rejected", Reason: "실재성 문제가 아닌 기존 연예 범위 밖이라는 합성 기각 사유", Fingerprint: "synthetic-source"}
	d["canAdoptLegacy"] = state == "operator"
	if state == "error" {
		d["ownershipError"] = true
	}
	if state == "adopted" || state == "locked" {
		e.WriteOwner = "native"
		e.Status = "candidate"
		e.Locked = state == "locked"
		d["ownershipEnabled"] = false
		d["canManageCommonLock"] = true
	}
	return d
}
func TestOwnershipUISeparatesProvenanceFromWriteAuthority(t *testing.T) {
	s := renderSmokeServer(t)
	for _, state := range []string{"operator", "viewer", "error", "adopted", "locked"} {
		var b bytes.Buffer
		if err := s.tmpl.ExecuteTemplate(&b, "kentity.html", ownershipPreview(state)); err != nil {
			t.Fatal(err)
		}
		body := b.String()
		if state == "viewer" && strings.Contains(body, "같은 UUID로 공통 검수 전환</button>") {
			t.Fatal("viewer write")
		}
		if state == "error" && !strings.Contains(body, "기존 기각 근거를 조회하지 못했습니다") {
			t.Fatal("error concealed")
		}
		if state == "adopted" && (!strings.Contains(body, "현재 쓰기 책임 공통 Entity") || !strings.Contains(body, "자동 발행용 승인 표기로 사용하지 않습니다")) {
			t.Fatal("origin confused with writer")
		}
	}
}

package kdb

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestSoleAliasOwner(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	for _, tc := range []struct {
		name   string
		claims []NameClaim
		want   uuid.UUID
		ok     bool
	}{
		{"주장 없음 — 진짜 새 대상이다", nil, uuid.Nil, false},
		{
			"별칭 하나 — 이름 변이다. 새 UUID 를 만들면 한 사람이 둘이 된다",
			[]NameClaim{{ID: a, CanonicalKO: "하하", ViaAlias: true}},
			a, true,
		},
		{
			"정본이 따로 있다 — 「원희」(brand_place)를 「아일릿 원희」가 삼키면 안 된다",
			[]NameClaim{
				{ID: a, CanonicalKO: "원희", ViaAlias: false},
				{ID: b, CanonicalKO: "아일릿 원희", ViaAlias: true},
			},
			uuid.Nil, false,
		},
		{
			"정본 하나뿐 — 기존 동명 경로가 처리한다",
			[]NameClaim{{ID: a, CanonicalKO: "아리랑", ViaAlias: false}},
			uuid.Nil, false,
		},
		{
			"별칭 주장 둘 — 근거가 정할 일이지 순서가 정하지 않는다(I06)",
			[]NameClaim{
				{ID: a, CanonicalKO: "가", ViaAlias: true},
				{ID: b, CanonicalKO: "나", ViaAlias: true},
			},
			uuid.Nil, false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := SoleAliasOwner(tc.claims)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("got (%s,%v), want (%s,%v)", got, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestClaimsForNameReadsEveryAliasLocale — 별칭을 ko 만 보면 영문 예명이 그대로
// 새 대상이 된다(위너/WINNER). 9칸을 다 보는지 질의문으로 고정한다.
func TestClaimsForNameReadsEveryAliasLocale(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(repoRoot(t), "internal", "kdb", "name_identity.go"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, col := range []string{
		"aliases_ko", "aliases_en", "aliases_ja", "aliases_vi", "aliases_zh",
		"aliases_zh_hant", "aliases_es", "aliases_id", "aliases_pt_br",
	} {
		if !strings.Contains(string(src), col) {
			t.Fatalf("ClaimsForName 이 %s 를 안 본다 — 그 로케일의 예명은 새 대상이 된다", col)
		}
	}
}

// TestEntityCreationPathsCheckAliases — 대상을 **만드는** 경로는 전부 「이 이름이
// 이미 어떤 대상의 이름 변이인가」를 먼저 물어야 한다(I13).
//
// 2026-09-20 실측: 활성 원장 307건이 이미 갈라져 있었다(하하/하동훈 · KCM/강창모 ·
// 예리/김예림 · 위너/WINNER). 인입 경로 셋 중 둘이 `canonical_ko` 만 보고 별칭을
// 안 봤기 때문이다. 리뷰로 잡히는 종류가 아니라서 정적 가드로 못을 박는다.
//
// 새 레인이 대상을 만들려면 이 시험이 먼저 막는다. 막힌 사람이 할 일은 가드를
// 지우는 것이 아니라 ClaimsForName/SoleAliasOwner 를 부르는 것이다.
func TestEntityCreationPathsCheckAliases(t *testing.T) {
	root := repoRoot(t)
	const create = "INSERT INTO kwave_entities"
	// 둘 중 하나가 있으면 별칭을 본 것이다 — 공용 판정기를 부르거나, 자기 질의에서
	// 별칭 칸을 직접 본다(demand_evidence 가 후자다).
	markers := []string{"ClaimsForName", "aliases_ko"}

	var offenders []string
	var checked int
	err := filepath.Walk(filepath.Join(root, "internal"), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		src := string(b)
		if !strings.Contains(src, create) {
			return nil
		}
		checked++
		for _, m := range markers {
			if strings.Contains(src, m) {
				return nil
			}
		}
		rel, _ := filepath.Rel(root, path)
		offenders = append(offenders, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if checked == 0 {
		t.Fatal("대상 생성 경로를 하나도 못 찾았다 — 가드가 아무것도 안 지키고 있다")
	}
	if len(offenders) > 0 {
		t.Fatalf("대상을 만들면서 별칭을 안 보는 경로: %v\n"+
			"  이 이름이 이미 어떤 대상의 별명·예명·닉네임이면 새 UUID 를 만들면 안 된다(I13).\n"+
			"  ClaimsForName + SoleAliasOwner 를 부르고, 주인이 있으면 붙이기만 하라.", offenders)
	}
}

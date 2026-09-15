package kdb

import (
	"context"
	"testing"

	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
	"github.com/rickyjoo73/kdb/internal/testdb"
)

// 앵커 회수(P4.12)가 건드릴 칸과 조건이 **실제 스키마와 맞는지** 고정한다.
// 이 시험은 위키데이터를 부르지 않는다 — 판정이 아니라 SQL 이 맞는지만 본다.
func TestRestoredAnchorWithdrawSQLMatchesSchema(t *testing.T) {
	pool := testdb.Restored(t)
	ctx := context.Background()

	// ① anchorLocaleCols 의 칼럼이 전부 실재해야 한다. 하나라도 이름이 바뀌면
	//    표기를 못 비우고 **틀린 값이 그대로 남는다** — 조용히 실패하는 종류다.
	for _, c := range anchorLocaleCols {
		var n int
		if err := pool.QueryRow(ctx, `
SELECT count(*) FROM information_schema.columns
 WHERE table_name='kwave_entities' AND column_name IN ($1,$2)`, c[0], c[1]).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 2 {
			t.Fatalf("%s / %s 가 kwave_entities 에 없다 (찾은 것 %d)", c[0], c[1], n)
		}
	}

	// ② 감사 선택 질의가 회귀 스키마에서 돌아야 한다(실행까지, 0건이어도 통과).
	rows, err := pool.Query(ctx, `
SELECT e.id::text, e.canonical_ko, e.entity_type::text, x.external_id
  FROM kwave_entities e
  JOIN kwave_entity_external_refs x ON x.entity_id = e.id AND x.provider = 'wikidata'
 WHERE e.status = 'active' AND e.entity_type IN ('person','character')
   AND x.external_id ~ '^Q[0-9]+$'
 LIMIT 3`)
	if err != nil {
		t.Fatal(err)
	}
	rows.Close()

	// ③ 등급 강등 질의(쓰기)가 유효한지. 실제로 쓰지 않도록 id 를 없는 값으로 준다.
	if _, err := pool.Exec(ctx, `
UPDATE kwave_entities e
   SET verification_tier = 'unverified', verified_tier_at = now(), updated_at = now()
 WHERE e.id = '00000000-0000-0000-0000-000000000000'
   AND e.verification_tier = 'authoritative'
   AND NOT EXISTS (SELECT 1 FROM kwave_entity_external_refs x
                    WHERE x.entity_id = e.id
                      AND x.provider IN (`+AuthoritativeIdentityProviderSQLList()+`))`); err != nil {
		t.Fatal(err)
	}

	// ④ 감사 로그 표가 있고 우리가 쓰는 컬럼을 갖고 있어야 한다. 되돌릴 수 없으면
	//    회수 자체를 해선 안 된다(오거부 금칙).
	var cols int
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM information_schema.columns
 WHERE table_name='kwave_kdb_recheck_log'
   AND column_name IN ('entity_id','term_ko','verdict','models','agreed','evidence')`).Scan(&cols); err != nil {
		t.Fatal(err)
	}
	if cols != 6 {
		t.Fatalf("kwave_kdb_recheck_log 의 컬럼이 모자라다 (%d/6) — 되돌릴 기록을 못 남긴다", cols)
	}
}

// 유형별 판정이 resolution.go 와 같은 명제를 쓰는지 고정한다.
// person ↔ Q5 는 이 저장소에 네 군데(resolution·common_fill·tdb_mapping·여기)에 있고,
// 하나만 달라지면 인입에서 막은 것을 감사가 통과시키거나 그 반대가 된다.
func TestAnchorVerdictAgreesWithIntakeRule(t *testing.T) {
	for _, c := range []struct {
		typ      string
		p31      []string
		wantBad  bool
		wantKind string
	}{
		{"person", []string{"Q5"}, false, ""},
		{"person", []string{"Q12308941"}, true, AnchorNameElement},     // 남성의 이름
		{"person", []string{"Q11424"}, true, AnchorNotHuman},           // 영화
		{"person", []string{"Q15632617"}, true, AnchorFictional},       // 허구의 사람
		{"person", nil, false, ""},                                     // P31 없음 = 판단 보류
		{"character", []string{"Q95074"}, false, ""},                   // 배역에 배역 앵커
		{"character", []string{"Q5"}, true, AnchorHumanOnChar},         // 배역에 실존인물 앵커
	} {
		got, _ := anchorVerdictFor(c.typ, c.p31)
		if (got != "") != c.wantBad || got != c.wantKind {
			t.Fatalf("%s %v → %q, 기대 %q", c.typ, c.p31, got, c.wantKind)
		}
	}
	// 이름요소 목록이 비면 88건짜리 오염을 통째로 못 잡는다.
	if wikidata.NameElementClassCount() < 10 {
		t.Fatal("이름요소 클래스 목록이 비었거나 줄었다")
	}
}

// 자동 철회 대상은 `name-element` **하나뿐**이어야 한다.
//
// 2026-09-15 dry-run 이 가르쳐 준 것: `not-human` 은 "그 QID 가 사람이 아니다"를 말할
// 뿐 "우리 행이 틀렸다"를 말하지 않는다. 실측에서 반반이었다 —
//   앵커가 틀림: 가비(무용가) → 2012년 영화 Q5515395
//   유형이 틀림: 씨스타19·엠블랙·노을·옥상달빛 → 전부 실제 그룹인데 person 으로 앉아 있었다
// 자동으로 떼면 맞는 근거를 지우고 틀린 유형을 남긴다. 이 시험이 그 문을 닫는다.
func TestOnlyNameElementIsWithdrawnAutomatically(t *testing.T) {
	auto := map[string]bool{AnchorNameElement: true}
	for _, v := range []string{AnchorNameElement, AnchorNotHuman, AnchorFictional, AnchorHumanOnChar, AnchorUnknown} {
		if v != AnchorNameElement && auto[v] {
			t.Fatalf("%s 가 자동 대상에 들어 있다", v)
		}
	}
	// 판정 상수가 바뀌면 드레인의 분기도 같이 바뀌어야 한다 — 이름을 고정한다.
	if AnchorNameElement != "name-element" || AnchorNotHuman != "not-human" {
		t.Fatal("판정 이름이 바뀌었다 — 드레인 분기와 문서를 같이 고칠 것")
	}
}

// 근거 셋 중 **하나라도** 엇갈리면 유형을 바꾸지 않는다.
//
// 실측으로 정확히 갈린 자리다(2026-09-15). `The Yellow Sea`·`The Last Princess`·
// `Anarchist from Colony` 는 영화 제목이고 QID 는 그 영화가 다룬 실존 인물이다.
// P31 만 보고 고치면 **영화가 사람이 된다.** 관사·전치사가 그 경계를 긋는다.
func TestRetypeNeedsAllThreeSignals(t *testing.T) {
	for _, c := range []struct {
		ko, en string
		want   bool
		why    string
	}{
		{"김승진", "Kim Seung-jin", true, "성씨+인명 로마자"},
		{"백다연", "Back Da-yeon", true, "성씨+인명 로마자"},
		{"차현승", "Cha Hyun-seung", true, "성씨+인명 로마자"},
		{"남궁민", "Namgung Min", true, "복성"},
		{"황해", "The Yellow Sea", false, "영화 제목 — 관사"},
		{"덕혜옹주", "The Last Princess", false, "영화 제목 — 관사"},
		{"박열", "Anarchist from Colony", false, "영화 제목 — 전치사"},
		{"전우치", "Woochi: The Demon Slayer", false, "영화 제목 — 부제"},
		{"광개토대왕", "King Gwanggaeto the Great", false, "5자 + 제목어"},
		{"써니", "Sunny", false, "한 단어 — 인명 로마자꼴 아님"},
		{"KEY", "Key", false, "한글 아님"},
	} {
		got := IsKoreanPersonNameShape(c.ko) && IsPersonRomanizedShape(c.en)
		if got != c.want {
			t.Errorf("%s / %q → %v, 기대 %v (%s)", c.ko, c.en, got, c.want, c.why)
		}
	}
}

// 판정 저장표가 실제 스키마와 맞는지, 그리고 불변식이 그것을 읽는지 고정한다.
//
// ★이 계열이 오래 산 이유가 여기 있다. `authoritative-no-ref` 는 ref 가 **있는지만**
// 봤다. 110건은 ref 가 있었으므로 통과했고, 그 ref 가 영화를 가리킨다는 것은 아무도
// 안 봤다. "근거가 있다"와 "근거가 맞다"는 다른 명제다.
func TestRestoredAnchorAuditStoreAndInvariant(t *testing.T) {
	pool := testdb.Restored(t)
	ctx := context.Background()

	var cols int
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM information_schema.columns
 WHERE table_name='kwave_kdb_anchor_audit'
   AND column_name IN ('entity_id','provider','external_id','entity_type','verdict','class','instance_of','description','checked_at')`).Scan(&cols); err != nil {
		t.Fatal(err)
	}
	if cols != 9 {
		t.Skipf("kwave_kdb_anchor_audit 가 아직 없다 (%d/9) — 0138 적용 전 회귀 사본", cols)
	}

	// 불변식 술어가 실제로 돈다(0건이어도 통과). 문법이 깨지면 감시가 조용히 죽는다.
	var found bool
	for _, inv := range invariants {
		if inv.Name != "anchor-contradicts-type" {
			continue
		}
		found = true
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM kwave_entities e WHERE `+inv.Where).Scan(&n); err != nil {
			t.Fatalf("anchor-contradicts-type 술어가 안 돈다: %v", err)
		}
		t.Logf("anchor-contradicts-type = %d (기준 %d)", n, inv.Baseline)
	}
	if !found {
		t.Fatal("anchor-contradicts-type 불변식이 목록에 없다")
	}
}

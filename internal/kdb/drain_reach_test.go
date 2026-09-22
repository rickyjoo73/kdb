package kdb

import (
	"context"
	"testing"

	"github.com/rickyjoo73/kdb/internal/testdb"
)

// 드레인이 고르는 기계값 목록은 **자기보다 등급이 낮은 것만** 이어야 한다.
//
// ★왜 등급이 중요한가. 쓰기는 can_replace_canonical / ShouldReplace 가 막는다 —
// 같은 등급이면 안 바뀐다. 그러니 같은 등급까지 고르면 외부 API 를 부르고 아무것도
// 안 쓰고 **쿨다운만 태운다.** mydramalist·opencc 는 romanization 과 같은 7등급이라
// 정확히 그 꼴이 된다. 조용한 0건은 이 저장소가 이미 한 번 데인 계열이다.
func TestDrainsOnlyPickSourcesTheyCanActuallyReplace(t *testing.T) {
	for _, incoming := range []Source{
		SourceITunes, SourceDiscogs, SourceNetflix, SourceDisney,
		SourceMyDramaList, SourceWikidataLabel, SourceOpenCC,
	} {
		got := MachineFilledSourcesWeakerThan(incoming)
		if len(got) == 0 {
			t.Errorf("%s: 고를 것이 하나도 없다 — 드레인이 통째로 놀게 된다", incoming)
		}
		for _, m := range got {
			replace, _ := ShouldReplace(Source(m), "옛값", incoming, "새값")
			if !replace {
				t.Errorf("%s 가 %s 를 골랐지만 덮어쓸 수 없다 — 부르고 아무것도 안 쓴다", incoming, m)
			}
		}
	}
}

// 드레인은 **자기 출처를 다시 고르면 안 된다.** 자기 출력을 영원히 되씹는다.
func TestADrainNeverPicksItsOwnOutput(t *testing.T) {
	for _, s := range []Source{SourceOpenCC, SourceRomanization, SourceGTranslate, SourceCodexFallback} {
		for _, m := range MachineFilledSourcesWeakerThan(s) {
			if Source(m) == s {
				t.Fatalf("%s 가 자기 자신을 골랐다", s)
			}
		}
	}
}

// 실측으로 확인한 주력 기계 출처가 **권위 API 의 사정거리 안**에 있어야 한다.
//
// ★2026-09-15 실측. 노래·앨범 칸의 출처는 gtranslate 46.1% · romanization 16.7% ·
// codex 10.8% · opencc 6.1% 로 79.7% 가 기계값인데, 공식 현지제목을 쥔 iTunes 는
// codex 만 보느라 1,261건 중 241건에만 닿았다.
func TestAuthoritativeAPIsSeeTheBigMachineSources(t *testing.T) {
	have := map[string]bool{}
	for _, s := range MachineFilledSourcesWeakerThan(SourceITunes) {
		have[s] = true
	}
	for _, want := range []Source{
		SourceGTranslate, SourceRomanization, SourceCodexFallback, SourceOpenCC, SourceKanaRule,
	} {
		if !have[string(want)] {
			t.Fatalf("iTunes 가 %s 를 못 본다 — 그 출처로 채워진 칸은 공식 제목이 있어도 안 바뀐다", want)
		}
	}
	// mydramalist(7)는 같은 7등급인 romanization·opencc 를 **보면 안 된다**.
	for _, m := range MachineFilledSourcesWeakerThan(SourceMyDramaList) {
		if Source(m) == SourceRomanization || Source(m) == SourceOpenCC {
			t.Fatalf("mydramalist 가 같은 등급 %s 를 골랐다 — 부르고 아무것도 안 쓴다", m)
		}
	}
}

// 넓힌 선택 조건이 **실제 스키마에서 돈다.** 문법이 깨지면 드레인들이 err 에서
// 조용히 0건을 돌려준다 — 안 도는 것과 할 일 없는 것이 구별되지 않는다.
func TestRestoredWidenedDrainSelectionsRun(t *testing.T) {
	pool := testdb.Restored(t)
	ctx := context.Background()

	for _, c := range []struct {
		name, sql string
		src       Source
	}{
		{"itunes/discogs (song_album)", `
SELECT count(*) FROM kwave_entities
 WHERE status='active' AND entity_type='song_album'
   AND ARRAY[canonical_ja_source,canonical_zh_source,canonical_zh_hant_source] && $1::text[]`, SourceITunes},
		{"mdl (작품 ja)", `
SELECT count(*) FROM kwave_entities e
 WHERE status='active' AND operator_locked=false AND entity_type IN ('drama','show','movie')
   AND (canonical_ja='' OR canonical_ja IS NULL OR canonical_ja_source = ANY($1::text[]))`, SourceMyDramaList},
		{"ott (작품 현지제목)", `
SELECT count(*) FROM kwave_entities e
 WHERE status='active' AND operator_locked=false AND entity_type IN ('drama','show','movie')
   AND ARRAY[canonical_ja_source,canonical_zh_source,canonical_zh_hant_source,
             canonical_vi_source,canonical_id_source,canonical_es_source,canonical_pt_br_source] && $1::text[]`, SourceNetflix},
	} {
		var wide int
		if err := pool.QueryRow(ctx, c.sql, MachineFilledSourcesWeakerThan(c.src)).Scan(&wide); err != nil {
			t.Fatalf("%s: 넓힌 조건이 안 돈다: %v", c.name, err)
		}
		var narrow int
		if err := pool.QueryRow(ctx, c.sql, []string{string(SourceCodexFallback)}).Scan(&narrow); err != nil {
			t.Fatalf("%s: 종전 조건이 안 돈다: %v", c.name, err)
		}
		t.Logf("%-28s 종전(codex만) %5d → 넓힌 뒤 %5d", c.name, narrow, wide)
		if wide < narrow {
			t.Fatalf("%s: 넓혔는데 대상이 줄었다 (%d < %d)", c.name, wide, narrow)
		}
	}
}

// 거르지 않는 형태(빈 소스)는 **전부** 돌려줘야 한다.
//
// ★회귀가 잡았다. 등급 상한을 99 로 두었더니 기계값이 전부 7~9 등급이라 `> 99` 가
// 언제나 거짓이 되어 **빈 목록**이 나왔다. 넓힌 드레인이 통째로 0건을 고르는데
// 에러는 안 난다 — 정확히 '조용한 0건' 이다.
func TestUnfilteredListReturnsEveryMachineSource(t *testing.T) {
	all := MachineFilledSources()
	// 잠정 표기(2026-09-23)가 일곱째다 — 등급은 맨 아래지만 «드레인이 고를 수 있는 기계값»
	// 목록에는 반드시 들어가야 한다. 빠져 있던 동안 권위 값이 잠정 칸을 못 골랐다.
	if len(all) != 7 {
		t.Fatalf("거르지 않았는데 %d개다: %v", len(all), all)
	}
	for _, want := range []Source{
		SourceCodexFallback, SourceGTranslate, SourceGTranslateRaw,
		SourceKanaRule, SourceRomanization, SourceOpenCC, SourceLLMProvisional,
	} {
		found := false
		for _, m := range all {
			if Source(m) == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s 가 빠졌다", want)
		}
	}
}

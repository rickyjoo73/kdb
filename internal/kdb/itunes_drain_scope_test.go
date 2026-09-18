package kdb

import (
	"os"
	"strings"
	"testing"
)

// TestITunesGuardMatchesSelect — 고르는 목록과 거르는 목록이 같은지.
//
// 실측(2026-09-18): 2026-09-15 에 SELECT 와 UPDATE 는 «기계값 전체»로 넓혔는데
// 루프 안 가드만 codex-fallback 으로 남아 있었다. gtranslate·romanization·opencc 행
// **1,025건이 뽑히자마자 버려졌다.** 조회만 늘고 아무것도 안 바뀌는 조용한 0건이다.
// 같은 파일 주석이 그 함정을 경고하고 있었는데 정작 그 줄이 함정이었다.
func TestITunesGuardMatchesSelect(t *testing.T) {
	src, err := os.ReadFile("itunes_drain.go")
	if err != nil {
		t.Fatalf("itunes_drain.go 읽기 실패: %v", err)
	}
	body := string(src)
	if strings.Contains(body, `c.src != "codex-fallback"`) {
		t.Error("루프 가드가 codex-fallback 만 본다 — SELECT 확장이 무효가 된다")
	}
	if !strings.Contains(body, "itunesUpgradable(c.src)") {
		t.Error("가드가 SELECT 와 같은 목록(MachineFilledSourcesWeakerThan)을 쓰지 않는다")
	}
	// 실제로 같은 목록인지 값으로 확인한다.
	for _, s := range []string{"gtranslate", "romanization", "codex-fallback", "opencc"} {
		if !itunesUpgradable(s) {
			t.Errorf("%q 가 대조 대상에서 빠졌다 — SELECT 는 뽑는데 루프가 버린다", s)
		}
	}
	// 권위 출처는 건드리면 안 된다.
	for _, s := range []string{"wikidata-label", "tmdb", "operator-locked", "itunes"} {
		if itunesUpgradable(s) {
			t.Errorf("%q 를 기계값으로 취급한다 — 권위 값을 덮을 수 있다", s)
		}
	}
}

// TestITunesCoversEnglish — en 이 대조 대상인지.
//
// active song_album 1,944건 중 **1,565건의 canonical_en 이 기계값**(gtranslate 46%)인데
// iTunes 대조는 ja/zh/zh_hant 만 보고 있었다. 가장 큰 칸을 안 보고 있었던 것이다.
func TestITunesCoversEnglish(t *testing.T) {
	src, err := os.ReadFile("itunes_drain.go")
	if err != nil {
		t.Fatalf("itunes_drain.go 읽기 실패: %v", err)
	}
	body := string(src)
	if !strings.Contains(body, `{"en", it.en, it.enS}`) {
		t.Error("en 이 대조 칸 목록에 없다")
	}
	if !strings.Contains(body, "canonical_en_source] && $2::text[]") {
		t.Error("SELECT 조건이 en 출처를 보지 않는다 — en 만 기계값인 행이 안 뽑힌다")
	}
	// ★en 칸의 검색어는 한국어 원제여야 한다. en 이 기계값인데 그것으로 찾으면
	//   자기가 만든 값으로 자기를 확인하는 셈이다.
	if !strings.Contains(body, `if c.loc == "en" {
				term = it.ko
			}`) {
		t.Error("en 칸이 한국어 원제로 검색하지 않는다 — 기계값 en 으로 찾으면 자기확인이 된다")
	}
	// ja/zh/hant 는 종전대로 en 우선 검색을 유지해야 한다.
	if !strings.Contains(body, `COALESCE(NULLIF(canonical_en,''), canonical_ko) AS term`) {
		t.Error("ja/zh/hant 의 en 우선 검색이 사라졌다 — MB·iTunes 는 K-곡을 라틴으로 저장한다")
	}
}

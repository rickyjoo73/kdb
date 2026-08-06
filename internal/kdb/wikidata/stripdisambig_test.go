package wikidata

import "testing"

// 전부 실제 값이다 — 떼야 하는 것은 위키데이터에서 실제로 받아온 오염 레이블이고,
// 지켜야 하는 것은 이 DB 에 이미 정상으로 들어있는 값이다.
func TestStripDisambig(t *testing.T) {
	strip := map[string]string{
		"アイビー (歌手)":           "アイビー",
		"San E（ラッパー）":         "San E",
		"Ivy (韓國歌手)":          "Ivy",
		"IMFACT (音楽グループ)":     "IMFACT",
		"Spica (音楽グループ)":      "Spica",
		"J-Walk (韓国の音楽グループ)":  "J-Walk",
		"Deux (組合)":           "Deux",
		"Gary (韩国歌手)":         "Gary",
		"Unicorn (女子团体)":      "Unicorn",
		"Delivery (韓國網路劇)":    "Delivery",
		"George（歌手）":          "George",
		"Daybreak (乐队)":       "Daybreak",
		"Roller Coaster (组合)": "Roller Coaster",
		"超能力者 (映画)":           "超能力者",
		"GOOD DAY(音楽グループ)":    "GOOD DAY",
		"ペ・ナラ (俳優)":           "ペ・ナラ",
	}
	for in, want := range strip {
		if got := StripDisambig(in); got != want {
			t.Errorf("StripDisambig(%q) = %q, want %q", in, got, want)
		}
	}

	// 괄호가 이름의 일부인 값 — 손대면 안 된다.
	keep := []string{
		"티빙(TVING)", // 이 저장소가 괄호 라틴 규칙에서 확인한 정상 표기
		"아이브(IVE)",
		"Boy With Luv (Feat. Halsey)",
		"H.I.T",
		"4minute",
		"Cherry Bullet",
		"BLACKPINK",
		"（하나）",          // 전체가 괄호 — 뗄 머리가 없다
		"IZ*ONE (아이즈원)", // 안쪽에 한글 → 분류 딱지 아님
		"NCT 127",
		"",
		// ★독음/한자 병기 — 분류 딱지가 아니라 이름의 일부다. "괄호 안이 CJK 면 뗀다"는
		// 규칙을 쓰면 여기가 전부 깨진다(실데이터로 확인하고 규칙을 바꾼 지점).
		"4WARD(フォワード)",
		"BINGO(ビンゴ)",
		"Gnarly（ナーリー）",
		"HYNN（ヒン）",
		"HAWWAH（夏渦）",
		"Forever（フォーエバー）",
		"Heaven Can Wait（ヘブン・キャン・ウェイト）",
		"2026 サンダラパクファン・コーンアジアツアー REPRISM（リプリズム）",
	}
	for _, in := range keep {
		if got := StripDisambig(in); got != in {
			t.Errorf("StripDisambig(%q) = %q, 원본 유지해야 함", in, got)
		}
	}
}

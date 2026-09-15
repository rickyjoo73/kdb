package kdb

import "testing"

// 가나 성씨는 **음역이라 1:1** 이다 — 이 감사가 서는 근거 전부가 여기 있다.
//
// ★한자로는 못 한다. 강은 姜·康·強 이 다 쓰이고 간체·번체까지 갈린다. 실제로
// 한자 성씨표로 재 보니 1,287건이 걸렸는데 대부분이 표가 틀린 것이었다
// (강나리 康百合 · 권나라 权娜拉 — 둘 다 정상인데 표가 번체 權 을 기대했다).
func TestKanaSurnameIsOneToOne(t *testing.T) {
	for _, c := range []struct{ ko, wantSurname string }{
		{"김성훈", "キム"},
		{"하정우", "ハ"},
		{"이열음", "イ"},
		{"박보검", "パク"},
		{"최예나", "チェ"},
	} {
		got := kanaSurnameOf(NameToKatakana(c.ko))
		if got != c.wantSurname {
			t.Errorf("%s → 성씨 %q, 기대 %q (규칙값 %q)", c.ko, got, c.wantSurname, NameToKatakana(c.ko))
		}
	}
}

// 실측으로 본 오염이 **잡혀야** 한다.
//
//	ja ハ・ジョンウ   김성훈 · 하정우 · 하종우 · 하지우   ← 김성훈에게 하정우의 표기
//	ja キム・ソヒョン  김서형 · 김소현 · 김소현(뮤지컬)
func TestTheObservedContaminationIsCaught(t *testing.T) {
	for _, c := range []struct {
		ko, storedJA string
		wantFlag     bool
	}{
		{"김성훈", "ハ・ジョンウ", true},  // 실측 오염
		{"하정우", "ハ・ジョンウ", false}, // 같은 값이지만 이쪽은 주인이다
		{"김서형", "キム・ソヒョン", false}, // 성씨는 맞다 — 이 감사는 판정하지 않는다
		{"이열음", "イ・ヨルム", false},
		{"이현정", "イ・ヨルム", false}, // 성씨가 같아 못 잡는다. 과잉검출보다 낫다
	} {
		wantSur := kanaSurnameOf(NameToKatakana(c.ko))
		gotSur := kanaSurnameOf(c.storedJA)
		flagged := wantSur != "" && gotSur != "" && wantSur != gotSur
		if flagged != c.wantFlag {
			t.Errorf("%s / %s → 어긋남 %v, 기대 %v (규칙 성씨 %q, 저장 성씨 %q)",
				c.ko, c.storedJA, flagged, c.wantFlag, wantSur, gotSur)
		}
	}
}

// 성씨를 가를 수 없으면 **판정하지 않는다.** 근거 없이 멀쩡한 값을 지우면 안 된다(D-37).
func TestNoVerdictWithoutASeparableSurname(t *testing.T) {
	for _, ja := range []string{"ソラ", "ジス", "", "ナヨン"} {
		if got := kanaSurnameOf(ja); got != "" {
			t.Errorf("구분점 없는 %q 에서 성씨를 뽑았다: %q", ja, got)
		}
	}
}

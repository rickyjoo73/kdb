package kdb

import "testing"

// ★이 시험이 지키는 것 (2026-09-20).
//
//	종명이 들어오는 유형 `term` 은 TitleTypes 에 있어서 지시가 `translate_title`,
//	즉 «뜻을 옮겨라»였다. 소비자가 그대로 따르면 전어→鲭鱼(고등어) 가 재현된다.
//	우리가 틀린 지시를 보내고 있었다는 뜻이다.

func TestLocaleFillHintFor_학명이_있으면_표준통용명(t *testing.T) {
	// 전어: 유형은 term(=제목류)이지만 학명이 있으므로 분류군이다.
	if got := LocaleFillHintFor("term", "Konosirus punctatus"); got != FillHintUseStandardName {
		t.Fatalf("got %q, want %q — 뜻을 옮기라고 하면 鲭鱼(고등어)가 나간다", got, FillHintUseStandardName)
	}
}

func TestLocaleFillHintFor_학명이_없으면_유형대로(t *testing.T) {
	// `term` 에는 분류군이 아닌 것도 들어 있다. 유형 표를 통째로 옮기면 그것들이 같이 틀린다.
	if got := LocaleFillHintFor("term", ""); got != FillHintTranslateTitle {
		t.Errorf("term/학명없음 = %q, want %q", got, FillHintTranslateTitle)
	}
	if got := LocaleFillHintFor("person", ""); got != FillHintTransliterate {
		t.Errorf("person = %q, want %q", got, FillHintTransliterate)
	}
}

func TestLocaleFillHintFor_학명은_유형을_이긴다(t *testing.T) {
	// 유형이 무엇이든 분류군이면 표준 통용명이다 — 사람 유형에 학명이 붙는 일은
	// 없어야 하지만, 붙었다면 그건 관측이고 관측이 이긴다.
	if got := LocaleFillHintFor("person", "Attelabidae"); got != FillHintUseStandardName {
		t.Fatalf("got %q, want %q", got, FillHintUseStandardName)
	}
}

func TestLocaleFillHint_기존_계약_불변(t *testing.T) {
	// 옛 호출부는 그대로 동작해야 한다.
	if LocaleFillHint("drama") != FillHintTranslateTitle || LocaleFillHint("group") != FillHintTransliterate {
		t.Fatal("유형만 보는 옛 판정이 바뀌었다")
	}
}

func TestLocaleFillHintFor_공백은_학명이_아니다(t *testing.T) {
	if got := LocaleFillHintFor("term", "   "); got != FillHintTranslateTitle {
		t.Fatalf("got %q — 공백을 학명으로 읽으면 안 된다", got)
	}
}

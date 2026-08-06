package kdb

import "testing"

// 아래 입력은 전부 2026-08-06 운영 DB 실측값이다 — 추측 케이스가 아니다.
func TestParenLatinName(t *testing.T) {
	cases := []struct {
		name string
		ko   string
		want string
	}{
		// ── 채워야 하는 것 (en 빈칸 80건 중 실제 대상) ──
		{"그룹 공식표기", "넬(Nell)", "Nell"},
		{"숫자 포함 그룹명", "데이식스(DAY6)", "DAY6"},
		{"약칭", "비투비(BTOB)", "BTOB"},
		{"대문자 그룹", "엑소(EXO)", "EXO"},
		{"활동명", "김준수(XIA)", "XIA"},
		{"활동명2", "김디지(DEEGIE)", "DEEGIE"},
		{"공백 있는 곡명", "왓 유 라이크(What U Like)", "What U Like"},
		{"전부 대문자 곡명", "유 앤 아이(YOU AND I)", "YOU AND I"},
		// 플랫폼 한글명↔영문명. 방송사/플랫폼을 마커로 막으면 이게 죽는다 —
		// 그래서 일부러 막지 않았다.
		{"플랫폼", "티빙(TVING)", "TVING"},
		// 가운데 with 는 막지 않는다. 첫/마지막 토큰만 본다.
		{"가운데 마커는 통과", "보이 위드 러브(Boy With Luv)", "Boy With Luv"},

		// ── 버려야 하는 것 ──
		// 한자가 섞이면 어디까지가 이름인지 정할 수 없다.
		{"비ASCII 혼재", "찬(灿/Lucid)", ""},
		// 1글자는 잡음과 구분이 안 된다(DrainLatinKoToEN 의 2자 하한과 동일 기준).
		{"1글자", "유(U)", ""},
		// 크레딧 — 이걸 통과시키면 canonical_en 이 "Feat. skaiwater" 가 된다.
		{"feat 크레딧", "모노(Feat. skaiwater)", ""},
		{"파트 마커", "웨어 투 나우(Part.1 : Yellow Light)", ""},
		{"버전 마커는 마지막 토큰에도", "리프리즘(Reprism Ver.)", ""},
		{"인스트", "사랑(Inst.)", ""},
		// 괄호 짝이 깨진 실측 데이터.
		{"중첩/깨진 괄호", "자유롭게 날아 (Feat. 우기(YUQI))", ""},
		// 연도·숫자만 — 이름이 아니다.
		{"숫자만", "무한도전(2024)", ""},
		// 후보가 둘이면 어느 게 이름인지 모른다.
		{"ASCII 괄호 2개", "제목(ABC)(DEF)", ""},
		{"괄호 없음", "프리킨 슈즈", ""},
		{"한글 괄호", "아이유(가수)", ""},
		{"빈 문자열", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ParenLatinName(c.ko); got != c.want {
				t.Errorf("ParenLatinName(%q) = %q, want %q", c.ko, got, c.want)
			}
		})
	}
}

func TestParenTokens(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"Feat. skaiwater", []string{"feat", "skaiwater"}},
		{"Part.2", []string{"part", "2"}},
		{"DAY6", []string{"day6"}},
		{"Boy With Luv", []string{"boy", "with", "luv"}},
		{"Reprism Ver.", []string{"reprism", "ver"}},
	}
	for _, c := range cases {
		got := parenTokens(c.in)
		if len(got) != len(c.want) {
			t.Fatalf("parenTokens(%q) = %v, want %v", c.in, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("parenTokens(%q) = %v, want %v", c.in, got, c.want)
			}
		}
	}
}

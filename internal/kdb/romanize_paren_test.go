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

// 역패턴 — 라틴표기가 괄호 **바깥**에 있는 경우. ParenLatinName 이 못 잡는 자리다.
func TestPrefixLatinName(t *testing.T) {
	cases := []struct {
		name string
		ko   string
		want string
	}{
		// ── 채워야 하는 것 (실측) ──
		{"라틴 접두 + 한글 주석", "1'ONLY(원앤온리)", "1'ONLY"},
		{"주석 2개(한자·한글)", "HAWWAH (夏渦)(하와)", "HAWWAH"},
		{"공백 있는 접두", "Love Story(러브 스토리)", "Love Story"},

		// ── 버려야 하는 것 ──
		// 괄호 뒤에 글자가 더 있으면 접두부만 떼는 건 제목을 훼손한다.
		{"괄호 뒤 잔여", "Where To Now? (Part.2) : NOWHERE(웨어 투 나우)", ""},
		// 괄호 안에 라틴이 있으면 주석이 아니다 — 제목 일부이거나 마커다.
		{"괄호 안 라틴", "제목(Part.2)", ""},
		{"괄호 안 라틴2", "넬(Nell)", ""}, // 이건 ParenLatinName 담당
		// 괄호가 없으면 접두부를 이름으로 볼 근거가 없다. 공식 영문제목은 "Like OOH-AHH".
		{"괄호 없는 혼합", "OOH-AHH하게", ""},
		{"접두가 한글", "아이유(가수)", ""},
		{"접두 1글자", "U(유)", ""},
		{"마커 접두", "Inst.(인스트)", ""},
		{"빈 문자열", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := PrefixLatinName(c.ko); got != c.want {
				t.Errorf("PrefixLatinName(%q) = %q, want %q", c.ko, got, c.want)
			}
		})
	}
}

// 두 추출기는 같은 마커 규칙을 써야 한다. 갈라지면 같은 문자열이 어느 경로로 들어왔는지에
// 따라 다르게 판정된다 — 이번 작업 전체가 없애려는 규칙 분화다.
func TestBothExtractors_같은_마커규칙(t *testing.T) {
	if ParenLatinName("제목(Live)") != "" || PrefixLatinName("Live(라이브)") != "" {
		t.Error("마커 판정이 두 경로에서 갈렸다")
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

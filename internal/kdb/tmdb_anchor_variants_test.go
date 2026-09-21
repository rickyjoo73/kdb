package kdb

import (
	"reflect"
	"testing"
)

// ★변형 질의 — 43회차 표본에서 본 모양을 그대로 고정한다.
//
//	붙어야 할 것(정답이었던 5건의 형태)과, 절대 만들면 안 되는 것(상위 시리즈·메모)을
//	함께 적는다. 앵커 하나가 틀리면 tmdb-locale 이 7칸을 한꺼번에 오염시킨다.
func TestTMDbAnchorVariants(t *testing.T) {
	cases := []struct {
		name    string
		ko      string
		aliases []string
		want    []string
	}{
		{"괄호 메모 가제는 버린다", "천천히 강렬하게(가제)", nil, []string{"천천히 강렬하게"}},
		{"괄호 안이 정식 제목이면 둘 다", "꽃파당(조선혼담공작소 꽃파당)", nil, []string{"꽃파당", "조선혼담공작소 꽃파당"}},
		{"괄호 안 라틴은 버린다", "안단테 (Andante)", nil, []string{"안단테"}},
		{"연도 메모는 버린다", "눈물이 더 가까운 사람 (2026)", nil, []string{"눈물이 더 가까운 사람"}},
		{"약칭은 별칭으로 푼다", "냉부해", []string{"냉장고를 부탁해"}, []string{"냉장고를 부탁해"}},
		{"전각 괄호도", "스튜디오 춤（STUDIO CHOOM）", nil, []string{"스튜디오 춤"}},
		// ★콜론 앞부분은 만들지 않는다 — 상위 시리즈(시즌 1)에 붙을 수 있다.
		{"콜론 앞은 만들지 않는다", "흑백요리사: 요리 계급 전쟁 시즌2", nil, nil},
		// 띄어쓰기만 다른 별칭은 정규화 키가 원래 제목과 같다 — 이미 물어보고 없었던 질의다.
		{"원래 제목과 같은 키의 별칭은 뺀다", "나가수", []string{"나가수", "나 가수"}, nil},
		// 괄호 바깥 `배짱` 은 원래 질의(배짱배짱)와 다른 새 질의다. 별칭과 겹쳐도 한 번만.
		{"중복은 한 번만", "배짱(배짱)", []string{"배짱"}, []string{"배짱"}},
		{"한 글자 별칭은 뺀다", "몽글", []string{"몽"}, nil},
	}
	for _, c := range cases {
		got := tmdbAnchorVariants(c.ko, c.aliases)
		if !reflect.DeepEqual(got, c.want) && !(len(got) == 0 && len(c.want) == 0) {
			t.Errorf("%s: tmdbAnchorVariants(%q, %v) = %v, want %v", c.name, c.ko, c.aliases, got, c.want)
		}
	}
}

func TestTMDbAnchorVariants_최대넷(t *testing.T) {
	got := tmdbAnchorVariants("가나다", []string{"하나둘", "셋넷다", "다섯여섯", "일곱여덟", "아홉열하나"})
	if len(got) != 4 {
		t.Errorf("변형은 최대 4개 — TMDb 레이트 예산: got %d", len(got))
	}
}

// ★44회차 운영에서 틀린 두 건 — 시즌 표지를 떨군 별칭이 상위 시리즈에 붙었다.
//
//	그리고 같은 12건 중 **맞았던** 것들(원장에 같은 작품이 두 개체로 중복돼 있던 것)은
//	계속 붙어야 한다. 막는 기준은 「같은 ID 에 둘」이 아니라 「표지를 떨군 변형」이다.
func TestTMDbAnchorVariants_시즌표지를_떨구지_않는다(t *testing.T) {
	cases := []struct {
		name    string
		ko      string
		aliases []string
		want    []string
	}{
		{"사랑과 전쟁2 — 상위 시리즈 별칭은 버린다", "부부클리닉-사랑과 전쟁2", []string{"부부클리닉 사랑과 전쟁"}, nil},
		{"흑백요리사2 — 상위 시리즈 별칭은 버린다", "흑백요리사: 요리 계급 전쟁2", []string{"흑백요리사"}, nil},
		{"같은 표지를 가진 별칭은 쓴다", "흑백요리사: 요리 계급 전쟁2", []string{"흑백요리사 시즌2", "흑백요리사2"}, []string{"흑백요리사 시즌2", "흑백요리사2"}},
		{"다른 표지는 버린다", "쇼미더머니6", []string{"쇼미더머니5"}, nil},
		// 맞았던 것들 — 표지가 없으니 영향 없이 계속 붙는다.
		{"약칭 그대로", "냉부해", []string{"냉장고를 부탁해"}, []string{"냉장고를 부탁해"}},
		{"괄호 부제 그대로", "꽃파당(조선혼담공작소 꽃파당)", nil, []string{"꽃파당", "조선혼담공작소 꽃파당"}},
	}
	for _, c := range cases {
		got := tmdbAnchorVariants(c.ko, c.aliases)
		if !reflect.DeepEqual(got, c.want) && !(len(got) == 0 && len(c.want) == 0) {
			t.Errorf("%s: tmdbAnchorVariants(%q, %v) = %v, want %v", c.name, c.ko, c.aliases, got, c.want)
		}
	}
}

func TestTMDbSeasonMarker(t *testing.T) {
	cases := map[string]string{
		"부부클리닉-사랑과 전쟁2": "2", "흑백요리사: 요리 계급 전쟁 시즌2": "2", "쇼미더머니6": "6",
		"나는 솔로 3기": "3", "비밀의 숲 2부": "2", "Where To Now? Part.2": "2", "무한도전 II": "2",
		"냉장고를 부탁해": "", "2026 한일가왕전": "", "꽃파당": "",
	}
	for in, want := range cases {
		if got := tmdbSeasonMarker(in); got != want {
			t.Errorf("tmdbSeasonMarker(%q) = %q, want %q", in, got, want)
		}
	}
}

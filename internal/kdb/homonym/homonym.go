// Package homonym — 동명이인(same-name people) 구분 판정.
//
// 같은 한글 이름을 쓰는 서로 다른 실존 인물(예: 윤성호 감독 vs 윤성호 가수)을
// 별개 entity 로 유지해야 하는지(=충돌) 판정한다. 판정 신호는 보수적이다:
// 비어있거나 호환되면 "같은 사람일 수 있음", 명확히 다르면 "충돌(분리)".
//
// 보수 원칙: 신호가 충돌하면 절대 자동 merge 하지 않는다.
package homonym

import "strconv"
import "strings"

// PersonSignals — 한 사람을 식별하는 약한 신호 묶음.
type PersonSignals struct {
	PrimaryRole  string   // person_role enum 값 ("director","singer","other" 등)
	Agency       string   // 소속사 / 음반사
	NotableWorks []string // 대표 작품/필모그래피
	BirthYear    int      // 출생연도 (0 = 미상)
}

// Conflict — 두 신호 묶음이 "서로 다른 사람"임을 강하게 시사하면 true.
//
// 판정 규칙 (하나라도 강한 불일치면 충돌):
//   - agency: 둘 다 비어있지 않고 서로 다르면 충돌.
//   - birth_year: 둘 다 0 이 아니고 서로 다르면 충돌.
//   - primary_role: 둘 다 의미있는(="other" 아닌) 값이고 서로 다르면 충돌.
//   - notable_works: 둘 다 비어있지 않은데 교집합이 0(disjoint)이면 충돌.
//
// 신호가 비어있거나(미상) 호환되면 false(=같은 사람일 수 있음, 충돌 아님).
func Conflict(a, b PersonSignals) bool {
	if ne(a.Agency, b.Agency) {
		return true
	}
	if a.BirthYear != 0 && b.BirthYear != 0 && a.BirthYear != b.BirthYear {
		return true
	}
	if roleMeaningful(a.PrimaryRole) && roleMeaningful(b.PrimaryRole) &&
		!strings.EqualFold(strings.TrimSpace(a.PrimaryRole), strings.TrimSpace(b.PrimaryRole)) {
		return true
	}
	if len(nonEmpty(a.NotableWorks)) > 0 && len(nonEmpty(b.NotableWorks)) > 0 &&
		disjoint(a.NotableWorks, b.NotableWorks) {
		return true
	}
	return false
}

// SuggestDisambig — 신호로부터 사람-친화 disambig 라벨을 만든다.
// 우선순위: agency > primary_role > birth_year. 없으면 "".
func SuggestDisambig(s PersonSignals) string {
	if a := strings.TrimSpace(s.Agency); a != "" {
		return "(" + a + ")"
	}
	if roleMeaningful(s.PrimaryRole) {
		return "(" + strings.TrimSpace(s.PrimaryRole) + ")"
	}
	if s.BirthYear != 0 {
		return "(" + strconv.Itoa(s.BirthYear) + ")"
	}
	return ""
}

func roleMeaningful(r string) bool {
	r = strings.ToLower(strings.TrimSpace(r))
	return r != "" && r != "other"
}

// ne — 둘 다 비어있지 않고 (대소문자 무시) 서로 다르면 true.
func ne(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	return a != "" && b != "" && !strings.EqualFold(a, b)
}

// disjoint — 두 작품 목록이 교집합이 전혀 없으면 true (정규화: trim+lower).
func disjoint(a, b []string) bool {
	set := map[string]bool{}
	for _, x := range a {
		x = strings.ToLower(strings.TrimSpace(x))
		if x != "" {
			set[x] = true
		}
	}
	for _, y := range b {
		y = strings.ToLower(strings.TrimSpace(y))
		if y != "" && set[y] {
			return false
		}
	}
	return true
}

func nonEmpty(xs []string) []string {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			out = append(out, x)
		}
	}
	return out
}

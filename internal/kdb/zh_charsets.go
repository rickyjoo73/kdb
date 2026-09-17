package kdb

// zh_charsets — 간체/번체 판정과 **안전한** 자체 변환.
//
// ★왜 opencc.Convert 를 쓰지 않나 (2026-09-17 실측).
//
//	OpenCC 의 s2t 는 «간체 → 번체» 를 통째로 한다. 그런데 간체와 번체 양쪽에서
//	정자인 글자까지 건드린다:
//
//	    朴(박) → 樸    姜(강) → 薑    于 → 於    里 → 裡    准 → 準    台 → 臺
//
//	한국 성씨 «박»은 간체·번체 모두 朴 이다. 그런데 우리 opencc 레인이 박씨
//	133건을 樸 으로 바꿔 놨었다. 고유명사만 담는 저장소에서 이 변환은 **오염을
//	만드는 쪽**이다.
//
//	그래서 여기서는 «그 자체에서만 정자인 글자»만 바꾼다. 양쪽에서 정자인 글자는
//	손대지 않는다. 朴·姜 뿐 아니라 于·里·准·台·采 까지 한꺼번에 막힌다 —
//	두 글자짜리 예외 목록(zhProperNounKeep)으로는 이 종류를 다 못 막는다.
//
//	대가: 문장형 제목에서 里面 → 裡面 같은 어휘 수준 변환을 포기한다. 우리가 담는
//	것은 인명·작품명이고, 대만도 里 를 쓴다. 틀린 글자를 만드는 쪽이 더 나쁘다.
//
// ★왜 이체자를 변환하지 않나. 昇 → 升 은 왕복하지 않는다(升 의 번체는 升).
//	昇 은 본토에서도 인명에 살아 있는 글자다. 姜昇希 를 姜升希 로 바꾸면 그건
//	수리가 아니라 새 오염이다. 감지는 하되 변환하지 않고 사람에게 남긴다.

import "strings"

var (
	zhTradOnly = runeSet(zhTradOnlyRunes)
	zhHansOnly = runeSet(zhHansOnlyRunes)
	zhT2S      = runePairs(zhT2SFrom, zhT2STo)
	zhS2T      = runePairs(zhS2TFrom, zhS2TTo)
)

func runeSet(s string) map[rune]bool {
	m := make(map[rune]bool, len(s)/3)
	for _, r := range s {
		m[r] = true
	}
	return m
}

func runePairs(from, to string) map[rune]rune {
	dst := []rune(to)
	m := make(map[rune]rune, len(dst))
	i := 0
	for _, r := range from {
		if i >= len(dst) {
			break
		}
		m[r] = dst[i]
		i++
	}
	return m
}

// ContainsTradOnly — 번체에서만 정자인 글자가 섞여 있나. canonical_zh(간체 칸)에
// 대해 참이면 그 칸은 오염이다.
func ContainsTradOnly(s string) bool { return containsAnyRune(s, zhTradOnly) }

// ContainsHansOnly — 간체에서만 정자인 글자가 섞여 있나. canonical_zh_hant 에
// 대해 참이면 오염이다. 대만 표준형(秘·床·群 …)은 집합에서 이미 빠져 있다.
func ContainsHansOnly(s string) bool { return containsAnyRune(s, zhHansOnly) }

func containsAnyRune(s string, set map[rune]bool) bool {
	for _, r := range s {
		if set[r] {
			return true
		}
	}
	return false
}

// ZhToSimplified — 번체전용 글자만 간체로 바꾼다.
// ok=false 는 «왕복하지 않는 이체자가 있어 자동 변환하면 안 된다»는 뜻이다.
func ZhToSimplified(s string) (string, bool) { return convertOnly(s, zhTradOnly, zhT2S) }

// ZhToTraditional — 간체전용 글자만 번체로 바꾼다. ok 의 의미는 위와 같다.
func ZhToTraditional(s string) (string, bool) { return convertOnly(s, zhHansOnly, zhS2T) }

func convertOnly(s string, only map[rune]bool, table map[rune]rune) (string, bool) {
	var b strings.Builder
	b.Grow(len(s))
	ok := true
	for _, r := range s {
		if only[r] {
			if m, hit := table[r]; hit {
				b.WriteRune(m)
				continue
			}
			ok = false // 이체자 — 원문 그대로 두고 보류를 알린다
		}
		b.WriteRune(r)
	}
	return b.String(), ok
}

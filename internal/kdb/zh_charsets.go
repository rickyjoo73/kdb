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
		// ★검증 표가 먼저다. 전용 집합에 없는 글자도 여기 실릴 수 있다 —
		//   嶋 는 일본 국자라 OpenCC 의 번체 집합에 없지만, 본토 표기는 岛 다.
		//   («전용 글자만 본다» 규칙만 두면 眞嶋優 가 真嶋优 로 어중간하게 남는다.)
		if m, hit := zhVariantVerified[r]; hit {
			b.WriteRune(m)
			continue
		}
		if only[r] {
			if m, hit := table[r]; hit {
				b.WriteRune(m)
				continue
			}
			if !zhKeepVerified[r] {
				ok = false // 아직 안 본 이체자 — 원문 그대로 두고 보류를 알린다
			}
		}
		b.WriteRune(r)
	}
	return b.String(), ok
}

// zhVariantVerified — 왕복 검사가 «불안정»이라 막았지만 **사람이 확인한** 간화쌍.
//
// 왕복 검사(s2t(t2s(C))==C)는 昇→升→升 같은 인명 이체자를 막으려고 넣었다. 그런데
// 그 그물에 진짜 간화쌍도 함께 걸린다 — 勛 의 간체는 분명히 勋 인데, 勋 의 번체가
// 勳(다른 이체자)이라서 왕복이 깨진다. 사전만으로는 둘을 못 가른다.
//
// ★그래서 실물 26건을 gpt-5.6-sol·gpt-5.6-luna·gemma4 세 모델에 같은 프롬프트로
//   물어 대조했다(2026-09-17 저녁). 두 codex 가 일치한 것만 여기 싣는다.
//   gemma 는 «고쳤다»면서 번체를 그대로 남긴 답이 26건 중 5건이었다(尹啟相→尹啟相,
//   許楠儁→许楠儁, 金昇淵→金昇淵, 眞嶋優→真島) — 그래서 gemma 답은 근거로 쓰지 않았다.
//
// ★모델이 **글자가 아니라 이름을 바꾸자**고 한 것은 싣지 않는다. 李儁→李仁,
//   讚美→许赞美, 孔昇延→孔升妍 은 자체 변환이 아니라 표기 자체의 교체다. 그건
//   이 레인의 일이 아니고 정정·합의 레인이 근거를 갖고 할 일이다. 여기서는
//   글자만 고친다 — 李儁→李俊, 讚美→赞美, 孔昇延은 그대로.
var zhVariantVerified = map[rune]rune{
	'傭': '佣', // 고용노동부 僱傭勞動部 → 雇佣劳动部 (僱→雇 는 왕복 안정 표에 이미 있다)
	'勛': '勋', // 이명훈 李明勛 → 李明勋 · 영훈 泳勛 → 泳勋
	'啟': '启', // 윤계상 尹啟相 → 尹启相
	'儁': '俊', // 허남준 許楠儁 → 许楠俊
	'製': '制', // CJ제일제당 CJ第一製糖 → CJ第一制糖 (오너 잠금 — 값만 고치고 자물쇠 유지)
	'裡': '里', // 세상 어디에도 없는 착한 남자 世上哪裡… → 世上哪里…
	'眞': '真', // 마시마 眞嶋優 → 真岛优
	'嶋': '岛',
	'讚': '赞', // 허찬미 讚美 → 赞美
}

// zhKeepVerified — 번체 전용으로 분류됐지만 **본토 인명에서 그대로 쓰는** 글자.
//
// 昇: 通用规范汉字表 가 인명용으로 인정한 규범자다. 李昇基·姜昇希·金昇珍 …
//
//	보유 15건 중 13건을 두 codex 가 «그대로»라 답했다. 機械로 升 으로 바꾸면
//	이름이 달라진다.
//
// 汎: 김범 金汎. 泛 으로 바꾸면 다른 이름이 된다.
//
// 여기 실린 글자는 변환을 막지도, 보류를 만들지도 않는다 — 그대로 두고 통과시킨다.
var zhKeepVerified = map[rune]bool{'昇': true, '汎': true}

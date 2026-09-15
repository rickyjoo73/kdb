package readiness

// locale_code — 원장의 locale 코드로 맞춰 적는다.
//
// ★왜 (2026-09-15 실측). 운영 로그에 이 오류가 **한 시간에 30번** 찍히고 있었다:
//
//	preparations.go:169: legacy prepare tracking:
//	  insert or update on table "kentity_locale_readiness" violates
//	  foreign key constraint "kentity_locale_readiness_locale_fk"
//
//	`kentity_locales` 의 코드는 `pt-BR`·`zh-Hant`·`zh-Hans` 처럼 **대문자를 쓰는데**,
//	API 는 `pt_br`·`zh_hant` 를 쓴다. 서빙 경로에는 변환이 있었지만(normalizePrepareLocales)
//	추적 경로는 소비자가 보낸 문자열을 **그대로** 넣었다. 그래서 en·ja·vi·es 처럼
//	두 표기가 같은 locale 만 기록되고, 나머지를 요청한 소비자는 준비 추적이 통째로
//	실패했다 — 응답은 `tracking_status:"unavailable"` 로 정직했지만 그 소비자는
//	`/v1/preparations` 로 진행 상황을 볼 방법이 없었다.
//
// ★모르는 코드는 **버린다**. 하나가 틀려서 배치 전체가 실패하면, 아는 locale 의
//   추적까지 같이 잃는다. 버린 것은 원장에 없는 locale 이므로 채울 수도 없다.

import "strings"

// localeLedgerCode — kentity_locales.code 의 정규 표기. 없으면 "".
var localeLedgerCode = map[string]string{
	"de": "de", "en": "en", "es": "es", "fr": "fr", "id": "id", "ja": "ja",
	"ko": "ko", "ru": "ru", "th": "th", "vi": "vi", "zh": "zh",
	"pt-br": "pt-BR", "zh-hans": "zh-Hans", "zh-hant": "zh-Hant",
}

// NormalizeLedgerLocales — 소비자 표기를 원장 코드로 옮기고, 모르는 것은 버린다.
// 순서는 보존하고 중복은 제거한다.
func NormalizeLedgerLocales(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, l := range in {
		key := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(l, "_", "-")))
		code, ok := localeLedgerCode[key]
		if !ok || seen[code] {
			continue
		}
		seen[code] = true
		out = append(out, code)
	}
	return out
}

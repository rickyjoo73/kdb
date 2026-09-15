package readiness

import (
	"reflect"
	"testing"
)

// ★원장 코드와 API 표기가 달라 준비 추적이 시간당 30건 실패하고 있었다 (2026-09-15).
//
//	kentity_locales 는 `pt-BR`·`zh-Hant`, API 는 `pt_br`·`zh_hant`.
//	서빙 경로에는 변환이 있었는데 추적 경로에는 없었다.
func TestLedgerLocaleCodes(t *testing.T) {
	cases := []struct {
		in   []string
		want []string
	}{
		{[]string{"pt_br", "zh_hant"}, []string{"pt-BR", "zh-Hant"}},
		{[]string{"PT-BR", " zh-hans "}, []string{"pt-BR", "zh-Hans"}},
		{[]string{"en", "ja", "vi", "es"}, []string{"en", "ja", "vi", "es"}},
		// 모르는 코드는 버린다 — 하나 때문에 배치 전체를 잃으면 아는 것의 추적까지 잃는다.
		{[]string{"en", "kr", "zz", "ja"}, []string{"en", "ja"}},
		// 중복은 한 번만.
		{[]string{"zh_hant", "zh-Hant", "ZH-HANT"}, []string{"zh-Hant"}},
		{nil, []string{}},
	}
	for _, c := range cases {
		if got := NormalizeLedgerLocales(c.in); !reflect.DeepEqual(got, c.want) {
			t.Fatalf("NormalizeLedgerLocales(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

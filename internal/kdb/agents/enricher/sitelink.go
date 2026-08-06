package enricher

// sitelink.go — L3.2(Wikipedia langlink) 적용 대상 계산 (2026-08-06).
//
// 표기 변환 자체는 여기 없다. wikidata.Entity.LanglinkTitles() 하나만 쓴다 —
// enrich/orchestrator 가 이미 그걸 쓰고 있고, 같은 데이터에 두 번째 규칙을 두면
// 어느 레인이 먼저 손댔는지에 따라 결과가 갈린다.

// fieldsCoveredBy — want 중 m 이 실제로 값을 가진 칸만 남긴다.
//
// 왜 필요한가: applyLocaleMap 은 want 전체에 tried 를 찍는다. 소스가 값을 갖지도 않은
// 칸까지 "시도했다"로 기록하면, 원장이 "어떤 소스도 못 채움"이라는 거짓 판정을 남긴다
// (a022567 이 고친 것과 같은 결함 — 정책이 안 물어본 칸을 소진으로 세지 않는다).
func fieldsCoveredBy(want []string, m map[string][]string) []string {
	out := make([]string, 0, len(want))
	for _, col := range want {
		code, ok := localeToCode[col]
		if !ok {
			continue
		}
		if vals, has := m[code]; has && len(vals) > 0 {
			out = append(out, col)
		}
	}
	return out
}

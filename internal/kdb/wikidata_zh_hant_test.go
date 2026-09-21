package kdb

import "testing"

// ★41회차 표본의 세 건 — 위키데이터 평문 zh 가 번체였는데 어느 칸에도 안 들어갔다.
func TestWikidataZhHant(t *testing.T) {
	cases := []struct {
		name   string
		labels map[string]string
		want   string
	}{
		{"명시 번체 라벨이 먼저", map[string]string{"zh_hant": "韓承起", "zh": "韩承起"}, "韓承起"},
		{"평문 zh 가 번체면 번체 칸으로(수운잡방)", map[string]string{"zh": "需雲雜方"}, "需雲雜方"},
		{"평문 zh 가 번체면 번체 칸으로(부산장신대)", map[string]string{"zh": "釜山長神大學校"}, "釜山長神大學校"},
		{"평문 zh 가 번체면 번체 칸으로(한승기)", map[string]string{"zh": "韓承起"}, "韓承起"},
		// 공통 글자뿐이면 간체 칸이 받고 opencc 가 번체를 만든다 — 여기선 안 건드린다.
		{"공통 글자뿐이면 비운다", map[string]string{"zh": "金珍妮"}, ""},
		// 간체면 당연히 번체 칸 근거가 아니다.
		{"간체는 번체 칸 근거가 아니다", map[string]string{"zh": "韩承起"}, ""},
		{"라벨이 없으면 비운다", map[string]string{}, ""},
	}
	for _, c := range cases {
		if got := wikidataZhHant(c.labels); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

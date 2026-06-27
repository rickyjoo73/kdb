package kdb

import "testing"

// TestOttTitleMatchesKo — OTT(넷플릭스/디즈니) ko-제목 앵커. 실제 오염 사례(키워드 퍼지매치로
// 다른 작품 반환)를 거부하고, 시즌·구두점 변형 동일작은 인정하는지 고정한다.
// 회귀 시 resolveOTTID 가 오매칭을 채택해 잘못된 현지제목을 박는다(2026-06-27 오염 6건 사례).
func TestOttTitleMatchesKo(t *testing.T) {
	cases := []struct {
		title, ko string
		want      bool
	}{
		// 정상 매치(시즌-strip / 내부콤마 / 구두점·공백차)
		{"굿파트너, 지금 시청하세요 | 넷플릭스 - Netflix", "굿파트너2", true},
		{"모범형사, 지금 시청하세요 | 넷플릭스", "모범형사2", true},
		{"우리, 태양을 흔들자, 지금 시청하세요 | 넷플릭스", "우리 태양을 흔들자", true},
		{"킹덤: 아신전, 지금 시청하세요 | 넷플릭스 공식 사이트", "킹덤: 아신전", true},
		{"월간남친, 지금 시청하세요 | 넷플릭스", "월간 남친", true},
		{"무빙 | 디즈니+", "무빙", true},
		// ★오염 거부(다른 작품인데 키워드 일부 겹침) — 2026-06-27 실제 사례
		{"눈물의 여왕, 지금 시청하세요 | 넷플릭스 - Netflix", "여왕의 집", false},
		{"첫사랑은 처음이라서, 지금 시청하세요 | 넷플릭스 공식", "첫 번째 남자", false},
		{"어쩌다 전원일기, 지금 시청하세요 | 넷플릭스", "전원일기", false}, // 부분포함 거부
		{"보통의 가족, 지금 시청하세요 | 넷플릭스", "사랑의 가족", false},
		{"닥터 이방인, 지금 시청하세요 | 넷플릭스", "퍼스트 닥터", false},
		// 빈 입력
		{"", "무빙", false},
		{"무빙 | 디즈니+", "", false},
	}
	for _, c := range cases {
		if got := ottTitleMatchesKo(c.title, c.ko); got != c.want {
			t.Errorf("ottTitleMatchesKo(%q, %q) = %v, want %v", c.title, c.ko, got, c.want)
		}
	}
}

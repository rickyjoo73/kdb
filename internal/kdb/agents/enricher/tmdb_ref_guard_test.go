package enricher

// TMDb 식별자 기록 가드 회귀 테스트.
//
// ★실제 사고(2026-08-15): 이 캐스케이드가 Enrich 의 **느슨한** 매치(정규화 제목 일치)로
// 곧장 external_ref 를 쓰고 ON CONFLICT 로 기존 external_id 까지 덮고 있었다. 그래서
//
//	14:43  tmdb-anchor 가 `범죄자` 를 foreign-original 로 **거부**(정확·유일 일치했지만
//	       /tv/322571 은 일본 드라마 `犯罪者` 라 original_language≠ko)
//	15:01  이 캐스케이드가 **같은 앵커를 생성** → 로케일 칸까지 그 작품 값으로 재오염
//	       (ja `Hanzaisha`, zh `犯罪者`)
//
// 두 레인이 각각 자기 명제엔 충실한데 합쳐서 한 방향 덫이 됐다. 운영자가 오부착을 고쳐
// 재부착해 둔 id 도 같은 경로로 조용히 되돌아간다(`소울메이트` 는 ko-KR 검색 1위가 동명의
// 영어권 영화 Soulm8te 다).
//
// 규칙: 제목 회수는 느슨해도 되지만 **정체 단언은 앵커 드레인과 같은 엄격도**여야 한다.

import (
	"errors"
	"testing"
)

func TestShouldRecordTMDbRef(t *testing.T) {
	cases := []struct {
		name                           string
		looseID, strictID              int
		ambiguous, foreign, seasonOnly bool
		err                            error
		want                           bool
	}{
		{name: "엄격 매처와 일치하면 적는다", looseID: 110316, strictID: 110316, want: true},
		{name: "원작언어가 ko 가 아니면 적지 않는다 — 범죄자/犯罪者 사고",
			looseID: 322571, strictID: 0, foreign: true, want: false},
		{name: "동명작이 여럿이면 적지 않는다",
			looseID: 12345, strictID: 0, ambiguous: true, want: false},
		{name: "프랜차이즈 접힘이면 적지 않는다",
			looseID: 900001, strictID: 0, seasonOnly: true, want: false},
		{name: "느슨한 매치와 엄격한 매치가 다른 작품이면 적지 않는다 — 소울메이트/Soulm8te 사고",
			looseID: 1307118, strictID: 617882, want: false},
		{name: "전송실패는 적지 않는다 — 못 물어본 것을 맞다로 바꾸지 않는다",
			looseID: 110316, strictID: 110316, err: errors.New("timeout"), want: false},
		{name: "엄격 매처가 무매칭이면 적지 않는다",
			looseID: 110316, strictID: 0, want: false},
	}
	for _, c := range cases {
		got := shouldRecordTMDbRef(c.looseID, c.strictID, c.ambiguous, c.foreign, c.seasonOnly, c.err)
		if got != c.want {
			t.Errorf("%s: shouldRecordTMDbRef = %v, want %v", c.name, got, c.want)
		}
	}
}

package kdb

import (
	"testing"

	"github.com/rickyjoo73/kdb/internal/kdb/itunes"
)

// ★이 시험이 지키는 것 (2026-09-21 47회차).
//
//	KR 스토어에는 곡이 없고 MV 만 있어서, 한글 음역 제목의 수록곡·일본 발매곡은 영영 candidate 였다.
//	기사가 괄호로 함께 적은 원제로 JP 스토어를 본다. 문장은 전부 실제 기사 힌트에서 옮겼다.

func TestItunesOrigTitle_기사에서_원제를_뽑는다(t *testing.T) {
	cases := []struct{ name, hint, ko, want string }{
		{"A 한글 뒤 괄호",
			`'톡-토크'(Tock-Talk), ' 스프링 레인 '(Spring Rain) 등 일본 오리지널 신곡 3곡과`,
			"스프링 레인", "Spring Rain"},
		{"A 따옴표 안 괄호",
			`일본 네 번째 싱글 ‘ 카케라 -운명의 조각- (KAKERA -運命のピース-)’의 트랙리스트를 공개했다.`,
			"카케라 -운명의 조각-", "KAKERA -運命のピース-"},
		{"B 원제 뒤 괄호 + 깨진 글자",
			`일본 네 번째 싱글 'KAKERA -運命のピ?ス-'( 카케라-운메이노피스- )의 하이라이트 메들리가 공개됐다.`,
			"카케라-운메이노피스-", "KAKERA -運命のピ?ス-"},
		{"B 따옴표 안에서 괄호",
			`첫 싱글 ‘ゆらゆら -運命の花-( 유라유라 -운메이노하나- )’로 ‘더블 플래티넘’ 인증을 받았으며`,
			"유라유라 -운메이노하나-", "ゆらゆら -運命の花-"},
		{"B 앨범",
			`일본 첫 미니앨범 'UNI☆Sparkle!( 유니☆스파클! )'은 발매 직후 오리콘`,
			"유니☆스파클!", "UNI☆Sparkle!"},
		{"B 따옴표 안에서 붙은 괄호",
			`일본 두 번째 EP '回帰LOVE(회귀LOVE)'를 완성하기 위해`,
			"회귀LOVE", "回帰LOVE"},
		{"C 따옴표 없이 — 앞 낱말을 삼키지 않는다",
			`일본 두 번째 EP 回帰LOVE(회귀LOVE)를 완성하기 위해`,
			"회귀LOVE", "回帰LOVE"},
		{"한글 괄호는 원제가 아니다",
			`임영웅의 '살아온 우리에게'(신곡) 가 수록됐다`,
			"살아온 우리에게", ""},
		{"괄호가 없으면 없다",
			`타이틀곡 ‘또또’를 비롯해 ‘모이세’, ‘ 부디 행복해질 것 ’ 등 신곡 6곡`,
			"부디 행복해질 것", ""},
		{"숫자·기호만이면 원제가 아니다",
			`'다중관점'(2025) 을 발표했다`,
			"다중관점", ""},
	}
	for _, c := range cases {
		if got := itunesOrigTitle(c.hint, c.ko); got != c.want {
			t.Errorf("%s: itunesOrigTitle = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestItunesOrigTitleEq_깨진_글자만_한_칸_허용(t *testing.T) {
	if !itunesOrigTitleEq("KAKERA -運命のピ?ス-", "KAKERA -運命のピース-") {
		t.Error("가나 사이의 깨진 「?」 는 한 글자로 봐야 한다")
	}
	if itunesOrigTitleEq("KAKERA -運命のピ?ス-", "KAKERA -運命のピーース-") {
		t.Error("와일드카드는 정확히 한 글자다")
	}
	if !itunesOrigTitleEq("Why?", "Why") {
		t.Error("라틴 뒤 물음표는 제목 부호다 — 정규화가 지운다")
	}
	if itunesOrigTitleEq("Spring Rain", "Spring Rain (Single Version)") {
		t.Error("정확일치다 — 버전 꼬리가 붙으면 다른 트랙이다")
	}
	if !itunesOrigTitleEq("Fallin’ World", "Fallin' World") {
		t.Error("따옴표 모양 차이는 같은 제목이다")
	}
}

func onewArtist() itunesArtist {
	return itunesArtist{ko: "온유", search: "ONEW", names: []string{"온유", "onew"}}
}

func TestItunesPickOrigHit_기사_가수로만_고른다(t *testing.T) {
	res := []itunes.Track{
		{TrackName: "Spring rain", ArtistName: "UNIS", CollectionName: "The 2nd Mini Album 'SWICY' - EP", TrackID: 1},
		{TrackName: "Spring Rain", ArtistName: "ONEW", CollectionName: "KAKERA -運命のピース- - EP", TrackID: 2},
	}
	hit, title, coll, amb := itunesPickOrigHit(res, "Spring Rain", []itunesArtist{onewArtist()})
	if amb || hit == nil || hit.TrackID != 2 || title != "Spring Rain" || coll {
		t.Fatalf("기사에 온유만 있으면 ONEW 의 곡이어야 한다: hit=%+v title=%q coll=%v amb=%v", hit, title, coll, amb)
	}
	// 기사에 두 가수가 다 나오면 어느 쪽인지 말할 수 없다.
	unis := itunesArtist{ko: "유니스", search: "UNIS", names: []string{"유니스", "unis"}}
	if hit, _, _, amb := itunesPickOrigHit(res, "Spring Rain", []itunesArtist{onewArtist(), unis}); !amb || hit != nil {
		t.Errorf("같은 원제에 기사 가수 둘이 맞으면 모호로 버려야 한다: hit=%+v amb=%v", hit, amb)
	}
}

func TestItunesPickOrigHit_동명곡을_막는다(t *testing.T) {
	// 「신 포도」 는 미지의 곡이다. 스토어 첫머리는 르세라핌의 Sour Grapes 다.
	res := []itunes.Track{{TrackName: "Sour Grapes", ArtistName: "LE SSERAFIM", TrackID: 9}}
	miji := itunesArtist{ko: "미지", search: "Miji", names: []string{"미지", "miji"}}
	if hit, _, _, _ := itunesPickOrigHit(res, "Sour Grapes", []itunesArtist{miji}); hit != nil {
		t.Errorf("기사에 없는 가수의 동명곡을 골랐다: %+v", hit)
	}
	// 「전야」 — RIIZE 가 팬미팅에서 부른 EXO 곡. 기사에는 RIIZE 만 있다.
	res = []itunes.Track{{TrackName: "The Eve", ArtistName: "EXO", TrackID: 7}}
	riize := itunesArtist{ko: "라이즈(RIIZE)", search: "RIIZE", names: []string{"라이즈riize", "riize"}}
	if hit, _, _, _ := itunesPickOrigHit(res, "The Eve", []itunesArtist{riize}); hit != nil {
		t.Errorf("기사 가수가 아닌 원곡 가수로 승급했다: %+v", hit)
	}
	// 가수가 하나도 확인되지 않으면 아무것도 고르지 않는다.
	if hit, _, _, _ := itunesPickOrigHit(res, "The Eve", nil); hit != nil {
		t.Error("가수 없이 제목만으로 골랐다")
	}
}

func TestItunesPickOrigHit_앨범은_꼬리를_떼고_맞춘다(t *testing.T) {
	res := []itunes.Track{{TrackName: "ギミサマ☆", ArtistName: "UNIS", CollectionName: "UNI☆Sparkle! - EP", TrackID: 3, CollectionID: 30}}
	unis := itunesArtist{ko: "유니스", search: "UNIS", names: []string{"유니스", "unis"}}
	hit, title, coll, _ := itunesPickOrigHit(res, "UNI☆Sparkle!", []itunesArtist{unis})
	if hit == nil || !coll || title != "UNI☆Sparkle!" {
		t.Fatalf("EP 는 collectionName 으로 맞아야 한다: hit=%+v title=%q coll=%v", hit, title, coll)
	}
}

func TestItunesArtistMatches(t *testing.T) {
	zb1 := itunesArtist{ko: "제로베이스원", names: []string{"제로베이스원", "zerobaseone"}}
	if !zb1.matches("ZEROBASEONE") || !zb1.matches("ZEROBASEONE (ZB1)") {
		t.Error("표기 꼬리가 붙은 같은 가수를 못 알아봤다")
	}
	iu := itunesArtist{ko: "아이유", names: []string{"아이유", "iu"}}
	if !iu.matches("IU") {
		t.Error("두 글자 이름도 정확일치는 맞다")
	}
	if iu.matches("Fiuna") || iu.matches("IU Piano") {
		t.Error("두 글자 이름으로 품기 비교를 하면 아무 이름에나 걸린다")
	}
}

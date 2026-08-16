package kdb

import (
	"fmt"
	"testing"

	"github.com/rickyjoo73/kdb/internal/kdb/itunes"
)

// testRoster — 이름마다 서로 다른 엔티티 id 를 준다(별도 아티스트로 센다).
func testRoster(names ...string) *kArtistRoster {
	r := &kArtistRoster{names: map[string]string{}}
	for i, n := range names {
		r.names[normArtistName(n)] = fmt.Sprintf("e%d", i)
	}
	return r
}

// testRosterSameEntity — 표기가 여럿이지만 **같은 엔티티**인 경우(`BTS`↔`방탄소년단`).
func testRosterSameEntity(names ...string) *kArtistRoster {
	r := &kArtistRoster{names: map[string]string{}}
	for _, n := range names {
		r.names[normArtistName(n)] = "same"
	}
	return r
}

// TestNormCatalogTitle — 괄호 주석과 대소문자·구두점을 접는다.
func TestNormCatalogTitle(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Work(워크)", "work"},
		{"운전만해 (We Ride)", "운전만해"},
		{"SEXY LOVE", "sexylove"},
		{"Sexy Love", "sexylove"},
		{"사랑..하시렵니까?", "사랑하시렵니까"},
		{"WE:DISCONNECT", "wedisconnect"},
	}
	for _, c := range cases {
		if got := normCatalogTitle(c.in); got != c.want {
			t.Errorf("normCatalogTitle(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

// TestArtistRosterSplit — 합작 표기를 조각내 각각 대조한다.
//
// ★실측(08-16): `APT.` 의 iTunes artistName 이 "로제 & Bruno Mars" 라서, 로제가 명부에
// 있는데도 조회에 실패했다. 정답인데 표기 때문에 놓친 계층이 11건 중 3건이었다.
func TestArtistRosterSplit(t *testing.T) {
	r := testRoster("로제", "전영록", "TWICE", "델리스파이스")
	cases := []struct {
		artist string
		want   bool
	}{
		{"로제 & Bruno Mars", true},
		{"전영록 & ChaeA", true},
		{"TWICE", true},
		{"So!YoON!, Phum Viphurit", false},
		{"Allen Toussaint", false},
		{"Cosmograf", false},
		{"", false},
	}
	for _, c := range cases {
		if _, _, got := r.has(c.artist); got != c.want {
			t.Errorf("has(%q) = %v; want %v", c.artist, got, c.want)
		}
	}
	// 부분문자열은 걸리면 안 된다 — `진`(BTS)이 온갖 이름 안에서 잡히면 최상위 등급이 샌다.
	short := testRoster("진")
	if _, _, ok := short.has("진성"); ok {
		t.Error("`진` 이 `진성` 안에서 걸렸다 — 조각 단위 완전일치여야 한다")
	}
	if _, _, ok := short.has("진"); !ok {
		t.Error("정확히 같은 이름은 걸려야 한다")
	}
	// nil 명부는 안전해야 한다.
	var nilR *kArtistRoster
	if _, _, ok := nilR.has("로제"); ok {
		t.Error("nil 명부가 참을 반환했다")
	}
}

// TestArtistNameSpaceIsSignificant — 아티스트명에서 공백은 접으면 안 된다.
//
// ★실전 오탐(08-16): 우리 DB 의 `니키`(canonical_en="NI KI")가 공백을 지우니 인도네시아
// 가수 `NIKI` 와 같아졌고, RM 의 앨범 `Indigo` 가 88rising & NIKI 의 곡에 붙었다.
func TestArtistNameSpaceIsSignificant(t *testing.T) {
	if normArtistName("NI KI") == normArtistName("NIKI") {
		t.Error("`NI KI` 와 `NIKI` 가 같아졌다 — 사람 이름에서 공백은 유의미하다")
	}
	// 구두점은 계속 지운다.
	same := [][2]string{
		{"T-ara", "Tara"}, {"pH-1", "PH1"}, {"방탄소년단", " 방탄소년단 "},
		{"SUPER JUNIOR", "Super Junior"},
	}
	for _, p := range same {
		if normArtistName(p[0]) != normArtistName(p[1]) {
			t.Errorf("%q 와 %q 가 달라졌다 (%q vs %q)",
				p[0], p[1], normArtistName(p[0]), normArtistName(p[1]))
		}
	}
	// 명부에서도 실제로 갈려야 한다.
	r := testRoster("니키", "NI KI")
	if _, _, ok := r.has("88rising & NIKI"); ok {
		t.Error("`NIKI` 가 `NI KI` 명부에 걸렸다")
	}
}

// TestITunesPick — 제목만으로는 부족하다. 결과 전체에서 아티스트까지 맞는 걸 찾아야 한다.
func TestITunesPick(t *testing.T) {
	tr := func(title, artist string, id int64) itunes.Track {
		return itunes.Track{TrackName: title, ArtistName: artist, TrackID: id,
			TrackViewURL: "https://music.apple.com/kr/x"}
	}
	roster := testRoster("티아라", "T-ara", "여자아이들", "로제", "윤하")

	t.Run("정상", func(t *testing.T) {
		got, v, _ := iTunesPick("사건의 지평선", "Event Horizon",
			[]itunes.Track{tr("사건의 지평선", "윤하", 1)}, roster)
		if got == nil || got.TrackID != 1 {
			t.Fatalf("정답을 못 집었다 (verdict=%s)", v)
		}
	})

	t.Run("커버 음원이 먼저 와도 원곡을 집는다", func(t *testing.T) {
		// ★실측: `퀸카` 검색 최상위가 신기원(커버), `젠틀맨` 이 투맨(커버)이었다.
		got, v, _ := iTunesPick("퀸카", "Queencard", []itunes.Track{
			tr("퀸카", "신기원", 10),
			tr("퀸카", "여자아이들", 11),
		}, roster)
		if got == nil {
			t.Fatalf("원곡을 못 집었다 (verdict=%s)", v)
		}
		if got.TrackID != 11 {
			t.Errorf("커버(%d)를 집었다 — 명부에 있는 아티스트를 우선해야 한다", got.TrackID)
		}
	})

	t.Run("해외 동명곡은 기각", func(t *testing.T) {
		// ★실측: `NUMBER NINE`(티아라)이 Allen Toussaint 에, `WE:DISCONNECT` 가
		// 영국 밴드 Cosmograf 에 걸렸다. KR 스토어도 해외 카탈로그를 싣는다.
		got, v, reason := iTunesPick("WE:DISCONNECT", "",
			[]itunes.Track{tr("WE:DISCONNECT", "Cosmograf", 20)}, roster)
		if got != nil {
			t.Fatal("해외 동명곡을 붙였다")
		}
		if v != "artist-unknown" {
			t.Errorf("verdict = %q; want artist-unknown", v)
		}
		if reason == "" {
			t.Error("기각에는 사유가 있어야 한다")
		}
	})

	t.Run("카탈로그에 없음", func(t *testing.T) {
		got, v, _ := iTunesPick("바이트 나우", "Bite Now",
			[]itunes.Track{tr("전혀 다른 곡", "티아라", 30)}, roster)
		if got != nil {
			t.Fatal("제목이 다른데 붙였다")
		}
		if v != "no-catalog-match" {
			t.Errorf("verdict = %q; want no-catalog-match", v)
		}
	})

	t.Run("en 제목으로도 일치", func(t *testing.T) {
		got, _, _ := iTunesPick("아이시 앤 스파이시", "Icy and Spicy",
			[]itunes.Track{tr("Icy and Spicy", "로제", 40)}, roster)
		if got == nil {
			t.Fatal("canonical_en 일치를 못 잡았다")
		}
	})

	t.Run("괄호 주석은 접는다", func(t *testing.T) {
		got, _, _ := iTunesPick("운전만해 (We Ride)", "",
			[]itunes.Track{tr("운전만해", "티아라", 50)}, roster)
		if got == nil {
			t.Fatal("괄호 주석 때문에 놓쳤다")
		}
	})

	t.Run("리믹스·커버보다 원곡이 먼저다", func(t *testing.T) {
		// ★실전 오탐(08-16): `마이 유니버스` 가 Stray Kids 커버에 붙었다. 괄호를 떼면
		// "My Universe (승민 & 아이엔) [feat. 창빈]" 이 원곡과 같아 보이고, 정답인
		// `Coldplay X BTS` 는 ` X ` 구분자 미처리로 명부 조회에 실패했다.
		r2 := testRoster("방탄소년단", "BTS", "Stray Kids")
		got, v, _ := iTunesPick("마이 유니버스", "My Universe", []itunes.Track{
			tr("My Universe", "Coldplay X BTS", 100),
			tr("My Universe (승민 & 아이엔) [feat. 창빈]", "Stray Kids", 101),
		}, r2)
		if got == nil {
			t.Fatalf("원곡을 못 집었다 (verdict=%s)", v)
		}
		if got.TrackID != 100 {
			t.Errorf("커버(%d)를 집었다 — 괄호까지 같은 원곡이 우선이어야 한다", got.TrackID)
		}
	})

	t.Run("명부 아티스트가 둘이면 고르지 않는다", func(t *testing.T) {
		// ★실전(08-16): `Ice Cream` 은 블랙핑크(×Selena Gomez)도, 유나도 갖고 있다.
		// 우리 song_album 엔티티엔 아티스트 필드가 없어 어느 쪽인지 알 수 없다 —
		// 최상위 등급을 추측으로 줄 수 없으므로 기각한다.
		r2 := testRoster("블랙핑크", "유나")
		got, v, reason := iTunesPick("Ice Cream", "", []itunes.Track{
			tr("Ice Cream", "블랙핑크", 200),
			tr("Ice Cream", "유나", 201),
		}, r2)
		if got != nil {
			t.Fatalf("모호한데 골랐다 (id=%d)", got.TrackID)
		}
		if v != "ambiguous-artists" {
			t.Errorf("verdict = %q; want ambiguous-artists", v)
		}
		if reason == "" {
			t.Error("기각에는 사유가 있어야 한다")
		}
	})

	t.Run("같은 아티스트의 여러 버전은 모호하지 않다", func(t *testing.T) {
		r2 := testRoster("방탄소년단")
		got, v, _ := iTunesPick("Life Goes On", "", []itunes.Track{
			tr("Life Goes On", "방탄소년단", 210),
			tr("Life Goes On", "방탄소년단", 211),
		}, r2)
		if got == nil {
			t.Fatalf("같은 아티스트의 중복판을 모호로 봤다 (verdict=%s)", v)
		}
	})

	t.Run("같은 엔티티의 두 표기는 하나로 센다", func(t *testing.T) {
		// ★실측(08-16): `마이 유니버스` 가 "BTS, 방탄소년단 둘 이상"으로 잘못 기각됐다.
		// 유일성을 이름으로 세면 같은 것을 둘로 센다 — 엔티티 id 로 세야 한다.
		r2 := testRosterSameEntity("BTS", "방탄소년단")
		got, v, reason := iTunesPick("마이 유니버스", "My Universe", []itunes.Track{
			tr("My Universe", "Coldplay X BTS", 300),
			tr("My Universe (SUGA's Remix)", "Coldplay & 방탄소년단", 301),
		}, r2)
		if got == nil {
			t.Fatalf("같은 엔티티를 둘로 세어 기각했다 (verdict=%s reason=%s)", v, reason)
		}
		if got.TrackID != 300 {
			t.Errorf("리믹스(%d)를 집었다 — 괄호까지 같은 원곡이 우선이어야 한다", got.TrackID)
		}
	})

	t.Run("X 구분자로 나뉜 합작", func(t *testing.T) {
		r2 := testRoster("BTS")
		if _, _, ok := r2.has("Coldplay X BTS"); !ok {
			t.Error("` X ` 로 나뉜 합작 표기를 못 갈랐다")
		}
		// 이름 안의 알파벳 x 는 가르면 안 된다.
		r3 := testRoster("Maxwell")
		if _, _, ok := r3.has("Maxwell"); !ok {
			t.Error("이름 안의 x 를 갈라 `Maxwell` 을 놓쳤다")
		}
	})
}

// TestITunesMinTitleFloor — 1~2글자 제목은 조회 대상이 아니다.
//
// ★`BE`(방탄소년단 앨범)가 은혁의 곡 `be` 에 붙었다(08-16 실측). 은혁도 우리 명부에
// 있는 한국 아티스트라 **아티스트 가드로는 못 막는다** — 애초에 묻지 않아야 한다.
func TestITunesMinTitleFloor(t *testing.T) {
	tooShort := []string{"BE", "3D", "잼", "가"}
	for _, s := range tooShort {
		if len([]rune(normCatalogTitle(s))) >= iTunesMinTitleRunes {
			t.Errorf("%q 가 하한을 통과했다 — 정확일치가 우연이 된다", s)
		}
	}
	ok := []string{"APT.", "FANCY", "슈퍼 참치", "Work(워크)"}
	for _, s := range ok {
		if len([]rune(normCatalogTitle(s))) < iTunesMinTitleRunes {
			t.Errorf("%q 가 하한에 걸렸다 — 식별정보가 충분하다", s)
		}
	}
}

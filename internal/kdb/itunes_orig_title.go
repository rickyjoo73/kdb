package kdb

// itunes_orig_title — 한글 음역 제목으로는 못 찾는 곡을 **기사가 함께 적은 원제**로 찾는다.
//
// ★왜 (2026-09-21 47회차). itunes-candidates 는 KR 스토어만 본다. 그런데 **KR 스토어에는
//   곡 카탈로그가 없고 뮤직비디오만 있다** — 「Dynamite BTS」 를 물어도 kind 가 전부
//   music-video 다(JP 스토어는 같은 질의에 song 8건). 그래서 이 레인은 MV 가 있는 타이틀곡만
//   잡았고(누적 승급 116), 수록곡·일본 발매곡은 구조적으로 못 잡았다(no_match 718).
//
//   결핍 원장 unmet 의 56% 가 「이미 candidate 로 있는데 승급을 못 한」 것이고, 곡의 상위가
//   정확히 이 모양이었다:
//
//     스프링 레인(요청 13) · 카케라-운메이노피스-(7) · 유라유라 -운메이노하나-(6) …
//
//   기사는 원제를 **괄호로 함께 적는다** — `'KAKERA -運命のピース-'( 카케라-운메이노피스- )`,
//   `' 스프링 레인 '(Spring Rain)`. 한글 음역은 어느 스토어에도 없지만 원제는 JP 스토어에 있다.
//
// ★관문은 기존 레인과 같은 세기다 — 제목 **정확일치** + 가수 일치. 다른 점 하나:
//   가수를 「기사에 나온 가장 긴 이름 하나」가 아니라 **기사에 나온 active 가수 전부**와
//   대조한다. 기존 추정은 자주 틀렸다(스프링 레인→이예진 · 카케라→하이라이트 ·
//   디어 마이 크레이지 솔메이트→이지). 전부와 대조하되, 맞는 가수가 **둘 이상이면 버린다.**
//
//   실측 시뮬레이션(원제가 뽑힌 37곡, JP 스토어): 13곡 적중 · 13곡 모두 기사 원문과 맞음.
//   가수 관문이 옳게 막은 것 — 「신 포도」(미지의 곡인데 스토어에는 르세라핌 Sour Grapes) ·
//   「전야」(RIIZE 가 팬미팅에서 부른 EXO 곡 — 기사에는 RIIZE 만 있다).

import (
	"context"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyjoo73/kdb/internal/kdb/itunes"
)

// itunesOrigQuotes — 기사가 제목을 감싸는 따옴표·괄호류.
const itunesOrigQuotes = `'"‘’“”「」『』<>《》〈〉`

var (
	itunesOrigQ        = regexp.QuoteMeta(itunesOrigQuotes)
	itunesCollSuffixRE = regexp.MustCompile(`(?i)\s+-\s+(single|ep)$`)
	// C 모양은 공백을 안 받는다 — 따옴표가 없으면 원제가 어디서 시작하는지 모르고, 공백을
	// 받으면 앞 낱말까지 삼킨다(「EP 回帰LOVE」).
	itunesOrigBareRunesRE = `[A-Za-z0-9\p{Hiragana}\p{Katakana}\p{Han}☆♡!.\-]`
)

// itunesOrigKoPattern — 한글 제목을 기사 안에서 찾는 정규식. 기사는 띄어쓰기를 자주
// 바꾸므로(「유니 스」·「스프링 레인 」) 글자 사이 공백을 자유롭게 둔다.
func itunesOrigKoPattern(ko string) string {
	var parts []string
	for _, r := range ko {
		if unicode.IsSpace(r) {
			continue
		}
		parts = append(parts, regexp.QuoteMeta(string(r)))
	}
	return strings.Join(parts, `\s*`)
}

// itunesOrigUsable — 원제 후보가 쓸 만한가. 한글이 섞이면 원제가 아니다(부제·가수명일 수 있다).
func itunesOrigUsable(s string) string {
	s = strings.TrimSpace(strings.Trim(strings.TrimSpace(s), itunesOrigQuotes))
	if s == "" || len([]rune(s)) > 80 {
		return ""
	}
	useful := false
	for _, r := range s {
		if unicode.Is(unicode.Hangul, r) {
			return ""
		}
		if (r < 0x80 && unicode.IsLetter(r)) || unicode.In(r, unicode.Hiragana, unicode.Katakana, unicode.Han) {
			useful = true
		}
	}
	if !useful {
		return ""
	}
	return s
}

// itunesOrigTitle — 기사 힌트에서 한글 제목 ko 옆에 적힌 원제를 뽑는다. 없으면 "".
//
//	A  ko 뒤 괄호      ' 스프링 레인 '(Spring Rain) · ‘카케라 -운명의 조각- (KAKERA -運命のピース-)’
//	B  원제 뒤 괄호    'KAKERA -運命のピ?ス-'( 카케라-운메이노피스- ) · ‘ゆらゆら -運命の花-( 유라유라 … )’
//	C  따옴표 없는 B   EP 回帰LOVE(회귀LOVE) — 공백 없는 한 덩어리만
func itunesOrigTitle(hint, ko string) string {
	hint = strings.TrimSpace(hint)
	kp := itunesOrigKoPattern(ko)
	if hint == "" || kp == "" {
		return ""
	}
	a := regexp.MustCompile(kp + `\s*[` + itunesOrigQ + `]*\s*\(([^()]{1,80})\)`)
	for _, m := range a.FindAllStringSubmatch(hint, -1) {
		if o := itunesOrigUsable(m[1]); o != "" {
			return o
		}
	}
	b := regexp.MustCompile(`[` + itunesOrigQ + `]([^` + itunesOrigQ + `()]{1,80})[` + itunesOrigQ + `]*\s*\(\s*` + kp + `\s*\)`)
	for _, m := range b.FindAllStringSubmatch(hint, -1) {
		if o := itunesOrigUsable(m[1]); o != "" {
			return o
		}
	}
	c := regexp.MustCompile(`(` + itunesOrigBareRunesRE + `{2,60})\(\s*` + kp + `\s*\)`)
	for _, m := range c.FindAllStringSubmatch(hint, -1) {
		if o := itunesOrigUsable(m[1]); o != "" {
			return o
		}
	}
	return ""
}

// itunesOrigWild — 기사 원문의 **깨진 글자**. 수집 단계에서 「ー」 같은 문자가 「?」 로
// 깨진 채 들어온다(`KAKERA -運命のピ?ス-`). 한자·가나 사이에 낀 「?」 만 한 글자 와일드카드로
// 본다 — 「Why?」 의 물음표는 제목의 일부이고 정규화가 원래 지운다.
const itunesOrigWild = '\uFFFD'

func itunesOrigNormWant(orig string) []rune {
	rs := []rune(orig)
	for i := 1; i+1 < len(rs); i++ {
		if rs[i] == '?' && rs[i-1] > 0x7F && rs[i+1] > 0x7F {
			rs[i] = itunesOrigWild
		}
	}
	return []rune(itunesNormTitle(string(rs)))
}

// itunesOrigTitleEq — 스토어 제목이 원제와 **정확히** 같은가(정규화 + 깨진 글자 한 칸 허용).
func itunesOrigTitleEq(orig, got string) bool {
	w, g := itunesOrigNormWant(orig), []rune(itunesNormTitle(got))
	if len(w) == 0 || len(w) != len(g) {
		return false
	}
	for i := range w {
		if w[i] != itunesOrigWild && w[i] != g[i] {
			return false
		}
	}
	return true
}

// itunesArtist — 기사에 나온 active 가수 하나와 그 이름들(정규화). search 는 스토어 검색에
// 붙일 라틴 이름이다(JP 스토어는 가수를 라틴으로 적는다 — 온유→ONEW).
type itunesArtist struct {
	ko     string
	search string
	names  []string
}

func (a itunesArtist) matches(storeArtist string) bool {
	an := itunesNormTitle(storeArtist)
	if an == "" {
		return false
	}
	for _, n := range a.names {
		if n == "" {
			continue
		}
		if n == an {
			return true
		}
		// 「ZEROBASEONE」 ↔ 「ZEROBASEONE (ZB1)」 처럼 한쪽이 다른 쪽을 품는 경우. 세 글자
		// 미만은 품기 비교를 안 한다 — 「IU」 가 아무 이름에나 들어간다.
		if len([]rune(n)) >= 3 && len([]rune(an)) >= 3 && (strings.Contains(an, n) || strings.Contains(n, an)) {
			return true
		}
	}
	return false
}

// itunesPickOrigHit — 검색 결과에서 원제 정확일치 + 기사 가수 일치인 것을 고른다.
//
// 앨범(EP)은 collectionName 으로 맞는다 — 스토어는 「UNI☆Sparkle! - EP」 처럼 꼬리를 단다.
// 맞는 가수가 **둘 이상이면 고르지 않는다**(ambiguous=true). 같은 원제를 두 가수가 가진 경우
// 기사 언급만으로는 어느 쪽인지 말할 수 없다.
func itunesPickOrigHit(res []itunes.Track, orig string, artists []itunesArtist) (hit *itunes.Track, title string, viaCollection, ambiguous bool) {
	matchedArtist := ""
	for i := range res {
		t := &res[i]
		var ttl string
		coll := false
		switch {
		case itunesOrigTitleEq(orig, t.TrackName):
			ttl = t.TrackName
		case itunesOrigTitleEq(orig, itunesCollSuffixRE.ReplaceAllString(strings.TrimSpace(t.CollectionName), "")):
			ttl = itunesCollSuffixRE.ReplaceAllString(strings.TrimSpace(t.CollectionName), "")
			coll = true
		default:
			continue
		}
		var who string
		for _, a := range artists {
			if a.matches(t.ArtistName) {
				who = a.ko
				break
			}
		}
		if who == "" {
			continue
		}
		if matchedArtist != "" && matchedArtist != who {
			return nil, "", false, true
		}
		if hit == nil {
			matchedArtist, hit, title, viaCollection = who, t, strings.TrimSpace(ttl), coll
		}
	}
	return hit, title, viaCollection, false
}

// itunesOrigMaxArtists — 원제 하나에 가수별로 검색하는 최대 횟수. iTunes 는 분당 ~20회라
// 기사에 이름이 여럿 나와도 긴 이름부터 셋만 본다.
const itunesOrigMaxArtists = 3

// itunesOrigSearch — 원제를 기사 가수별로 JP 스토어에 물어 고른다.
//
// 원제만으로 물으면 흔한 제목(「Spring Rain」)은 다른 가수의 곡이 위를 차지해 찾는 곡이
// 25위 안에도 없다. 그래서 가수의 라틴 이름을 붙여 묻는다. 결과는 **모아서 한 번에** 고른다 —
// 가수 A 로 물은 결과에 가수 B(역시 기사에 나온)의 같은 원제가 섞이면 모호로 버려야 한다.
func itunesOrigSearch(ctx context.Context, cl *itunes.Client, orig string, artists []itunesArtist) (hit *itunes.Track, title string, viaCollection, ambiguous bool) {
	if cl == nil || orig == "" {
		return nil, "", false, false
	}
	var all []itunes.Track
	asked := 0
	for _, a := range artists {
		if a.search == "" || asked >= itunesOrigMaxArtists || ctx.Err() != nil {
			continue
		}
		asked++
		res, err := cl.Search(ctx, strings.ReplaceAll(orig, "?", " ")+" "+a.search, "jp", 10)
		time.Sleep(3 * time.Second) // iTunes ~20req/min 예의
		if err != nil {
			continue
		}
		all = append(all, res...)
	}
	return itunesPickOrigHit(all, orig, artists)
}

// coMentionActiveArtists — 기사 힌트에 나온 active 인물·그룹 **전부**(긴 이름부터).
// coMentionActiveArtist 와 같은 조건이되 하나만 고르지 않는다.
func coMentionActiveArtists(ctx context.Context, pool *pgxpool.Pool, hint, selfKo string) []itunesArtist {
	h := strings.TrimSpace(hint)
	if pool == nil || h == "" {
		return nil
	}
	rows, err := pool.Query(ctx, `
SELECT e.canonical_ko, COALESCE(e.canonical_en,''), COALESCE(e.aliases_en,'{}'::text[]),
       COALESCE(e.canonical_ja,''), max(char_length(n.nm)) AS l
  FROM kwave_entities e,
       LATERAL unnest(ARRAY[e.canonical_ko] || e.aliases_ko) AS n(nm)
 WHERE e.status='active' AND e.entity_type::text IN ('person','group')
   AND char_length(n.nm) BETWEEN 2 AND 20
   AND n.nm <> $2 AND e.canonical_ko <> $2
   AND position(n.nm IN $1) > 0
 GROUP BY e.id, e.canonical_ko, e.canonical_en, e.aliases_en, e.canonical_ja
 ORDER BY l DESC
 LIMIT 8`, h, selfKo)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []itunesArtist
	for rows.Next() {
		var ko, en, ja string
		var aliasesEn []string
		var l int
		if rows.Scan(&ko, &en, &aliasesEn, &ja, &l) != nil {
			continue
		}
		a := itunesArtist{ko: ko}
		for _, n := range append([]string{ko, en, ja}, aliasesEn...) {
			if nn := itunesNormTitle(n); nn != "" {
				a.names = append(a.names, nn)
			}
			if a.search == "" && isMostlyASCII(n) {
				a.search = strings.TrimSpace(n)
			}
		}
		out = append(out, a)
	}
	return out
}

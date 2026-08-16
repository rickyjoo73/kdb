package kdb

// itunes_anchor_drain.go — unverified **song_album** 에 iTunes 카탈로그 앵커를 붙인다.
// 키 없음. 엔티티당 API 1회.
//
// ★왜 만들었나(2026-08-16). ko.wikipedia 앵커 레인을 소진한 뒤 unverified 가 2,729건
// 남았고 그중 **song_album 이 667건으로 최대 버킷**이다. 곡은 위키백과 문서가 잘 없고
// 뉴스에도 안 실린다 — 앞의 두 레인이 구조적으로 못 잡는 계층이다.
//
// 그런데 **iTunes 클라이언트는 이미 있었다.** 다만 `DrainITunesSongs` 는 ja/zh 값이
// `codex-fallback` 인 칸의 **현지표기 확인**용이라 선정 조건이 좁았고, 다른 카탈로그
// 드레인들(musicbrainz·kofic·kopis)은 전부 `status='candidate'` 만 본다 — 즉 **승격
// 심사용이지, 이미 active 인데 unverified 인 엔티티엔 한 번도 겨눠진 적이 없다.**
// 08-16 의 "굳이 네이버 API 가 필요한가?"(→Google News RSS 가 이미 있었다)와 같은 발견이다.
//
// 실측(unverified song_album 40건 표본, KR 스토어):
//
//	제목 정확일치 ∧ 아티스트가 우리 명부에 있음   18건 (45%)  ← 전수 검수해 18/18 정확
//	제목은 맞는데 아티스트가 명부에 없음          11건 (27%)
//	카탈로그에 정확일치 없음                     11건 (27%)
//
// ★아티스트 가드가 필수인 이유: **KR 스토어도 해외 카탈로그를 싣는다.** 제목만 보면
// `NUMBER NINE`(티아라) 이 Allen Toussaint 의 곡에, `WE:DISCONNECT` 가 영국 프로그레시브
// 밴드 Cosmograf 에 걸린다. 게다가 **커버·노래방 음원**이 원곡보다 먼저 나온다
// (`퀸카`→신기원, `젠틀맨`→투맨). 그래서 첫 제목일치를 집으면 안 되고, **결과 전체에서
// 명부에 있는 아티스트를 가진 것**을 찾아야 한다 — 이 한 가지가 두 함정을 함께 막는다.
//
// ★`itunes` 는 이미 authoritativeIdentityProviders 에 있다(source_policy.go). 즉 이
// 앵커 하나로 곧바로 **authoritative** 다. 최상위 등급을 주는 레인이라 가드를 느슨하게
// 할 수 없다 — 확신이 없으면 기각한다(빈칸 > 틀린값).

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb/httpx"
	"github.com/rickyjoo73/kdb/internal/kdb/itunes"
)

// iTunesAnchorField — 원장 field. 성공·기각 모두 남긴다.
const iTunesAnchorField = "itunes-anchor"

// iTunesRateInterval — iTunes Search 는 **분당 약 20회**에서 429 를 던진다(실측:
// 0.35s 간격으로 돌렸더니 40건 중 22건이 429). 3.2초면 분당 ~19회로 그 아래다.
const iTunesRateInterval = 3200 * time.Millisecond

// iTunesFailStop — 연속 전송실패가 이만큼이면 회차를 접는다. 429 가 계속 나는 상황에서
// 남은 후보를 태우면 **다음 회차의 몫까지 없애면서 아무것도 못 얻는다.**
const iTunesFailStop = 5

// iTunesParenRe — 제목 꼬리·중간의 괄호 주석(`Work(워크)`, `운전만해 (We Ride)`).
var iTunesParenRe = regexp.MustCompile(`[(（\[].*?[)）\]]`)

// normCatalogTitle — 제목 비교용 정규화. 괄호 주석을 떼고 영숫자·한글만 남긴다.
// (`Work(워크)` ↔ `Work`, `SEXY LOVE` ↔ `Sexy Love`)
func normCatalogTitle(s string) string { return normAlnum(iTunesParenRe.ReplaceAllString(s, "")) }

// normTitleStrict — 괄호를 **떼지 않은** 정규화. 같은 제목의 다른 녹음을 구별한다.
//
// ★괄호 제거는 양날이다(2026-08-16 실측). `Work(워크)`↔`Work` 를 맞추려면 필요하지만,
// 그 때문에 **`My Universe (승민 & 아이엔) [feat. 창빈]`(Stray Kids 커버)이 원곡과 같아
// 보인다.** 그래서 선정을 2단으로 나눈다 — 1단은 괄호까지 그대로 같은 것만, 없으면
// 2단에서 괄호를 떼고 본다. 원곡이 있으면 원곡이 이긴다.
func normTitleStrict(s string) string { return normAlnum(s) }

func normAlnum(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// iTunesMinTitleRunes — 정규화 제목이 이보다 짧으면 아예 조회하지 않는다.
//
// ★`BE`(방탄소년단 앨범)가 **은혁의 곡 `be`** 에 붙었다(08-16 실측). 1~2글자 제목은
// 식별정보가 거의 없어 정확일치가 곧 우연이다. 아티스트 가드도 못 막는다 — 은혁도
// 우리 명부에 있는 한국 아티스트이기 때문이다. **가드를 더 얹는 게 아니라 애초에
// 물어보지 않는 게 맞다.** 3으로 둬서 `APT.`(→`apt`)는 살리고 `BE`·`3D`는 버린다.
const iTunesMinTitleRunes = 3

// artistSplitRe — 합작 표기를 나눈다. `로제 & Bruno Mars`, `전영록 & ChaeA`,
// `So!YoON!, Phum Viphurit` 처럼 **정답인데 결합 표기라서** 명부 조회에 실패하던 계층이
// 실측 11건 중 3건이었다.
//
// `x`(콜라보 표기)는 **공백으로 둘러싸인 것만** 가른다 — 그러지 않으면 아티스트명 안의
// 알파벳 x 를 갈라 엉뚱한 조각을 만든다. 이 구분자가 없어서 `마이 유니버스` 의 정답
// (`Coldplay X BTS`)이 명부 조회에 실패했고, 그 자리를 Stray Kids 커버가 차지했다.
var artistSplitRe = regexp.MustCompile(`(?i)(?:\s*(?:&|,|feat\.?|featuring|with|＆)\s*|\s+x\s+)`)

// kArtistRoster — 우리가 K-엔티티로 보유한 아티스트 표기 집합(ko·en 양쪽).
//
// tier 를 가리지 않는다. 이 명부가 답하는 질문은 "이 아티스트가 검증됐는가"가 아니라
// **"이 아티스트가 우리가 다루는 K-엔티티인가"** 다 — 후자면 그 곡도 우리 것이다.
// (실측: `풍악을 울려라`→장민호 는 장민호 본인이 unverified 였지만 매칭은 정확했다.)
// ★값이 bool 이 아니라 **엔티티 id** 인 이유(2026-08-16 실측): 유일성 판정을 이름으로
// 하면 `BTS` 와 `방탄소년단` 이 서로 다른 아티스트로 세어져 `마이 유니버스` 가
// "명부 아티스트 둘 이상"으로 잘못 기각된다. 같은 것을 둘로 세면 안 된다.
type kArtistRoster struct{ names map[string]string }

// loadKArtistRoster — 명부를 한 번 읽어 온다(드레인 1회당 1 쿼리).
func loadKArtistRoster(ctx context.Context, pool *pgxpool.Pool) *kArtistRoster {
	r := &kArtistRoster{names: map[string]string{}}
	rows, err := pool.Query(ctx, `
SELECT id::text, canonical_ko, COALESCE(canonical_en,'') FROM kwave_entities
 WHERE status='active' AND entity_type IN ('person','group','agency')`)
	if err != nil {
		log.Printf("kdb.itunes-anchor: 아티스트 명부 적재 실패 — 레인을 건너뛴다 (%v)", err)
		return nil
	}
	defer rows.Close()
	for rows.Next() {
		var id, ko, en string
		if rows.Scan(&id, &ko, &en) != nil {
			continue
		}
		for _, n := range []string{ko, en} {
			if k := normArtistName(n); k != "" {
				r.names[k] = id
			}
		}
	}
	return r
}

// normArtistName — 아티스트명 비교용 정규화. 구두점은 지우되 **공백은 보존**한다.
//
// ★제목과 같은 규칙을 쓰면 안 된다(2026-08-16 실측). 제목 정규화는 공백까지 지우는데,
// 그러면 우리 DB 의 `니키`(canonical_en=**"NI KI"**)가 인도네시아 가수 **`NIKI`** 와
// 같아진다. 그 충돌로 RM 의 앨범 `Indigo` 가 **88rising & NIKI** 의 곡에 붙었다.
// 사람 이름에서 공백은 유의미하다 — 제목에서와 달리 접으면 안 된다.
//
// 구두점은 계속 지운다(`T-ara`↔`T ara` 가 아니라 `T-ara`↔`Tara` 를 맞추기 위해서다).
func normArtistName(s string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if space && b.Len() > 0 {
				b.WriteRune(' ')
			}
			space = false
			b.WriteRune(r)
		case unicode.IsSpace(r):
			space = true
		}
	}
	return b.String()
}

// has — 아티스트 표기(합작 포함)가 명부에 있는가. 있으면 걸린 조각을 준다.
//
// **부분문자열이 아니라 조각 단위 완전일치**다. 부분일치를 허용하면 `진`(BTS)이 온갖
// 이름 안에서 걸린다 — 08-16 의 ko.wikipedia 이름사전이 밟은 것과 같은 함정이다.
// 반환 (걸린 표기, 그 엔티티 id, 걸렸는가).
func (r *kArtistRoster) has(artist string) (string, string, bool) {
	if r == nil || strings.TrimSpace(artist) == "" {
		return "", "", false
	}
	for _, part := range artistSplitRe.Split(artist, -1) {
		if k := normArtistName(part); k != "" {
			if id, ok := r.names[k]; ok {
				return strings.TrimSpace(part), id, true
			}
		}
	}
	return "", "", false
}

// iTunesPick — 검색 결과에서 **제목이 맞고 아티스트가 명부에 있는** 트랙을 고른다.
//
// 첫 제목일치를 집지 않는 게 핵심이다. iTunes 는 커버·노래방 음원을 원곡보다 앞에
// 내놓기도 하고, KR 스토어에도 동명의 해외곡이 있다. 결과 전체를 훑어 아티스트까지
// 맞는 것을 찾고, 없으면 **기각한다**(최상위 등급을 주는 레인이라 추측하지 않는다).
//
// 두 번째 반환값은 원장에 남길 사유다 — 왜 못 붙였는지 남겨야 다음 세션이 되짚는다.
// ★2단 선정이다. 1단은 괄호까지 그대로 같은 것만 본다(원곡). 1단이 비면 2단에서
// 괄호를 떼고 본다(`Work(워크)`↔`Work` 같은 표기차). 이 순서가 없으면 리믹스·커버가
// 원곡보다 앞에 있을 때 그쪽을 집는다 — 실측으로 겪었다.
func iTunesPick(ko, en string, tracks []itunes.Track, roster *kArtistRoster) (*itunes.Track, string, string) {
	strictKo, strictEn := normTitleStrict(ko), normTitleStrict(en)
	looseKo, looseEn := normCatalogTitle(ko), normCatalogTitle(en)

	name := func(t *itunes.Track) string {
		if t.TrackName != "" {
			return t.TrackName
		}
		return t.CollectionName
	}
	var titleHit *itunes.Track
	// scan — 제목이 맞는 후보 중 명부 아티스트를 가진 것을 **전부** 모은다.
	// 서로 다른 아티스트가 둘 이상이면 고르지 않는다(아래 유일성 규칙).
	scan := func(norm func(string) string, wantKo, wantEn string) (*itunes.Track, []string) {
		var pick *itunes.Track
		var artists []string
		seen := map[string]bool{}
		for i := range tracks {
			t := &tracks[i]
			n := norm(name(t))
			if n == "" || (n != wantKo && (wantEn == "" || n != wantEn)) {
				continue
			}
			if titleHit == nil {
				titleHit = t
			}
			who, id, ok := roster.has(t.ArtistName)
			if !ok {
				continue
			}
			if seen[id] {
				// 같은 아티스트의 다른 버전(싱글/앨범판)이거나 **같은 엔티티의 다른 표기**
				// (`BTS`↔`방탄소년단`)다. 하나로 센다.
				continue
			}
			seen[id] = true
			artists = append(artists, who)
			if pick == nil {
				pick = t
			}
		}
		return pick, artists
	}
	for _, p := range []struct {
		norm           func(string) string
		wantKo, wantEn string
	}{
		{normTitleStrict, strictKo, strictEn},
		{normCatalogTitle, looseKo, looseEn},
	} {
		pick, artists := scan(p.norm, p.wantKo, p.wantEn)
		switch len(artists) {
		case 0:
			continue // 다음 단계(괄호 제거)에서 다시 본다
		case 1:
			return pick, "", ""
		default:
			// ★유일하지 않으면 고르지 않는다(2026-08-16). 흔한 제목은 명부 아티스트
			// 여럿이 같은 이름의 곡을 갖는다(`Ice Cream` — 블랙핑크 ∧ 유나). 우리
			// song_album 엔티티엔 아티스트 필드가 없어 어느 쪽인지 알 방법이 없고,
			// 첫 번째를 집는 건 곧 추측이다. **최상위 등급을 추측으로 주지 않는다.**
			return nil, "ambiguous-artists",
				"제목이 같은 우리 명부 아티스트가 둘 이상: " + firstRunes(strings.Join(artists, ", "), 60)
		}
	}
	if titleHit != nil {
		return nil, "artist-unknown",
			"제목은 일치하나 아티스트가 우리 명부에 없음: " + firstRunes(titleHit.ArtistName, 40)
	}
	return nil, "no-catalog-match", "iTunes KR 카탈로그에 제목 정확일치 없음"
}

// DrainITunesAnchors — unverified song_album 에 iTunes 앵커를 붙인다.
// 반환 (붙인 수, 조회한 수).
func DrainITunesAnchors(ctx context.Context, pool *pgxpool.Pool, cl *itunes.Client, limit int) (anchored, checked int) {
	if pool == nil || cl == nil {
		return 0, 0
	}
	if limit <= 0 {
		limit = 50
	}
	roster := loadKArtistRoster(ctx, pool)
	if roster == nil || len(roster.names) == 0 {
		// 명부 없이 돌면 제목만으로 최상위 등급을 주게 된다 — 그럴 바엔 안 돈다.
		log.Printf("kdb.itunes-anchor: 아티스트 명부가 비어 있어 회차를 건너뛴다")
		return 0, 0
	}
	rows, err := pool.Query(ctx, `
SELECT id::text, canonical_ko, COALESCE(canonical_en,'') FROM kwave_entities e
 WHERE status='active' AND operator_locked=false
   AND entity_type='song_album'
   AND verification_tier='unverified'
   AND COALESCE(canonical_ko,'') <> ''
   AND NOT EXISTS (SELECT 1 FROM kwave_entity_external_refs r
                    WHERE r.entity_id=e.id AND r.provider='itunes')
   AND `+FillRetryPredicate("e", "$2")+`
 ORDER BY e.created_at ASC
 LIMIT $1`, limit, iTunesAnchorField)
	if err != nil {
		log.Printf("kdb.itunes-anchor: 선정: %v", err)
		return 0, 0
	}
	type item struct{ id, ko, en string }
	var todo []item
	for rows.Next() {
		var it item
		if rows.Scan(&it.id, &it.ko, &it.en) == nil {
			todo = append(todo, it)
		}
	}
	rows.Close()
	if len(todo) == 0 {
		return 0, 0
	}
	lim := httpx.NewLimiter(iTunesRateInterval)
	fails := 0

	for _, it := range todo {
		if ctx.Err() != nil {
			break
		}
		// ★짧은 제목은 조회 자체를 안 한다 — 호출을 아끼려는 게 아니라, 정확일치가
		// 우연이라 **아티스트 가드로도 못 막기** 때문이다(`BE`→은혁의 `be`).
		if len([]rune(normCatalogTitle(it.ko))) < iTunesMinTitleRunes {
			MarkFillAttempt(ctx, pool, it.id, iTunesAnchorField, "title-too-short",
				"제목이 너무 짧아 정확일치가 우연이 된다: "+it.ko)
			checked++ // 판정이다(원장에 남는다) — 조회를 안 했을 뿐이다
			continue
		}
		if lim.Wait(ctx) != nil {
			break
		}
		tracks, serr := cl.Search(ctx, it.ko, "kr", 12)
		if serr != nil {
			// ★전송실패는 판정이 아니다 — 마킹 없이 넘긴다. 429 를 기각으로 적으면
			// 멀쩡한 후보가 쿼터 사정 때문에 90일 낙인을 받는다.
			fails++
			log.Printf("kdb.itunes-anchor: 조회 실패 %q — 마킹 없이 넘김 (%v)", it.ko, serr)
			if fails >= iTunesFailStop {
				log.Printf("kdb.itunes-anchor: 연속 실패 %d회 — 회차 종료(남은 %d건은 다음 회차)",
					fails, len(todo)-checked)
				break
			}
			continue
		}
		fails = 0
		// ★checked 는 **판정에 도달한 건수**만 센다(전송실패 제외). 야간 드레인의
		// 소진 판정이 이 값을 쓴다 — 전송실패를 세면 외부 API 가 죽었을 때 레인이
		// "아직 처리 중"으로 보여 창이 닫힐 때까지 헛돈다.
		checked++
		t, verdict, reason := iTunesPick(it.ko, it.en, tracks, roster)
		if t == nil {
			MarkFillAttempt(ctx, pool, it.id, iTunesAnchorField, verdict, reason)
			continue
		}
		url := t.ViewURL()
		if _, err := pool.Exec(ctx, `
INSERT INTO kwave_entity_external_refs (entity_id, provider, external_id, url, confidence, raw_payload, fetched_at)
VALUES ($1::uuid,'itunes',$2,$3,0.85,'{}'::jsonb,now())
ON CONFLICT (entity_id, provider) DO NOTHING`,
			it.id, fmt.Sprintf("%d", t.TrackID), url); err != nil {
			log.Printf("kdb.itunes-anchor: ref 적재 %q: %v", it.ko, err)
			continue
		}
		if url != "" {
			_, _ = pool.Exec(ctx, `
UPDATE kwave_entities
   SET source_urls = (SELECT ARRAY(SELECT DISTINCT u
                        FROM unnest(COALESCE(source_urls,'{}'::text[]) || ARRAY[$2::text]) u
                       WHERE u <> '')),
       updated_at = now()
 WHERE id = $1::uuid`, it.id, url)
		}
		ClearFillAttempt(ctx, pool, it.id, iTunesAnchorField)
		anchored++
		log.Printf("kdb.itunes-anchor: %q → %d (%s)", it.ko, t.TrackID, firstRunes(t.ArtistName, 30))
	}
	log.Printf("kdb.itunes-anchor: 완료 anchored=%d /%d", anchored, checked)
	return anchored, checked
}

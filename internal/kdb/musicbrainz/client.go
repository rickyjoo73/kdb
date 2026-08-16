// Package musicbrainz — MusicBrainz API client for music groups/artists.
//
// 인증 불필요 (UA + 1 req/s rate limit). K-pop 그룹/가수의 다국어 alias
// (특히 ja/en) 보강용. vi/pt-br/es/id 는 약함 — 보조 layer.
package musicbrainz

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rickyjoo73/kdb/internal/kdb/httpx"
)

const (
	apiBase    = "https://musicbrainz.org/ws/2"
	defaultUA  = "kdb-bootstrap/0.1 (https://kdb.aiinplanet.com)"
	minBetween = 1100 * time.Millisecond // 1 req/s 정중하게.
)

// Client — MusicBrainz HTTP client. 단일 인스턴스 권장. limiter 가 1 req/s 페이싱을
// 동시성 안전하게 보장하므로 병렬 goroutine 이 공유해도 안전하다.
type Client struct {
	HTTPClient *http.Client
	UserAgent  string
	limiter    *httpx.Limiter
}

func New() *Client {
	return &Client{
		HTTPClient: &http.Client{Timeout: 10 * time.Second},
		UserAgent:  defaultUA,
		limiter:    httpx.NewLimiter(minBetween),
	}
}

// Artist — wbsearch 결과 1건.
type Artist struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Type           string `json:"type,omitempty"`
	Country        string `json:"country,omitempty"`
	Disambiguation string `json:"disambiguation,omitempty"`
	Score          int    `json:"score,omitempty"`
}

// Search — name + 한국 (country=KR) 우선 매칭. K-pop 외 매체 후보는 제외.
func (c *Client) Search(ctx context.Context, name string) ([]Artist, error) {
	return c.search(ctx, name, "")
}

// SearchGroups searches only MusicBrainz artist resources whose subtype is
// Group. The /artist endpoint also returns solo people; an exact name alone
// cannot prove that a KDB group is the same identity.
func (c *Client) SearchGroups(ctx context.Context, name string) ([]Artist, error) {
	return c.search(ctx, name, "group")
}

// SearchGroupsAnywhere — SearchGroups 와 같되 **country:KR 조건을 빼고** 찾는다.
//
// ★왜 필요한가(2026-08-17 실측). MusicBrainz 의 country 는 자주 비어 있다. 25건 표본에서
// `호라이즌`→`HORI7ON`(한글 별칭 `호라이즌` 보유)이 **country 미기재라는 이유만으로**
// 종전 검색에 안 잡혔다. 그렇다고 country 조건을 그냥 없애면 오답이 쏟아진다 — 같은
// 표본에서 `턴즈`→Turns(러시아 밴드), `AGD`→AGD(폴란드), `들고양이들`→Wildcats(old-time
// music) 8건이 걸렸다.
//
// **그래서 이 함수는 그 자체로 안전하지 않다.** 호출측이 국적 증명을 따로 걸어야 한다
// (앵커 레인은 `country=KR ∨ 한글 별칭 일치`를 요구한다). 승급 경로는 종전 SearchGroups
// 를 계속 쓴다 — 이름만 바꾼 게 아니라 **가드의 책임이 호출측으로 옮겨간 것**이라
// 함수를 따로 둔다.
func (c *Client) SearchGroupsAnywhere(ctx context.Context, name string) ([]Artist, error) {
	return c.searchScoped(ctx, name, "group", false)
}

func (c *Client) search(ctx context.Context, name, artistType string) ([]Artist, error) {
	return c.searchScoped(ctx, name, artistType, true)
}

func (c *Client) searchScoped(ctx context.Context, name, artistType string, countryKR bool) ([]Artist, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil
	}
	q := url.Values{}
	query := `artist:"` + name + `"`
	if countryKR {
		query += ` AND country:KR`
	}
	if artistType != "" {
		query += ` AND type:` + artistType
	}
	q.Set("query", query)
	q.Set("fmt", "json")
	q.Set("limit", "5")
	body, err := c.get(ctx, "/artist?"+q.Encode())
	if err != nil {
		return nil, err
	}
	var resp struct {
		Artists []Artist `json:"artists"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("mb search decode: %w", err)
	}
	return resp.Artists, nil
}

// AliasByLocale — MBID 로 locale 별 alias 가져옴. KDB 컬럼 키로 매핑.
type AliasByLocale map[string][]string // ko/en/ja/zh/...

// artistDetail — /artist/{mbid}?inc=aliases 파싱 결과. byLocale() 로 KDB 키
// 매핑 alias 를, matchesQuery() 로 동명이인 오매칭 가드를 제공한다.
type artistDetail struct {
	Name    string
	Aliases []struct {
		Name    string `json:"name"`
		Locale  string `json:"locale"`
		Type    string `json:"type"`
		Primary bool   `json:"primary"`
	}
}

func (c *Client) fetchDetail(ctx context.Context, mbid string) (*artistDetail, error) {
	if strings.TrimSpace(mbid) == "" {
		return nil, nil
	}
	body, err := c.get(ctx, "/artist/"+mbid+"?inc=aliases&fmt=json")
	if err != nil {
		return nil, err
	}
	var d artistDetail
	if err := json.Unmarshal(body, &d); err != nil {
		return nil, fmt.Errorf("mb fetch decode: %w", err)
	}
	return &d, nil
}

// byLocale — KDB canonical 키 별 alias 맵. primary name 도 en 으로 보존.
func (d *artistDetail) byLocale() AliasByLocale {
	out := AliasByLocale{}
	for _, a := range d.Aliases {
		kdbKey := mbLocaleToKDB(a.Locale)
		if kdbKey == "" {
			continue
		}
		if !contains(out[kdbKey], a.Name) {
			out[kdbKey] = append(out[kdbKey], a.Name)
		}
	}
	// MusicBrainz 의 primary name 도 보존 (보통 en).
	if d.Name != "" && !contains(out["en"], d.Name) {
		out["en"] = append(out["en"], d.Name)
	}
	return out
}

// matchesQuery — primary name 또는 alias(locale 무관, 전체) 중 하나라도 query 와
// 정규화 일치하면 true. wikidata.entityMatchesQuery 와 동일한 오매칭 가드.
func (d *artistDetail) matchesQuery(want string) bool {
	if want == "" {
		return false
	}
	if normalizeName(d.Name) == want {
		return true
	}
	for _, a := range d.Aliases {
		if normalizeName(a.Name) == want {
			return true
		}
	}
	return false
}

// AliasNames — 그 아티스트의 **모든 별칭 표기를 locale 구분 없이** 그대로 준다.
//
// byLocale() 은 `mbLocaleToKDB` 가 아는 locale 만 남긴다. 그런데 MusicBrainz 의 별칭은
// **locale 이 비어 있는 경우가 흔하다** — 실측에서 `JUSTB` 의 `저스트비`, `HORI7ON` 의
// `호라이즌` 이 그렇다. 국적 증명에 쓰려면 그 표기들이 필요하므로 거르지 않고 준다.
// 값 채움에는 쓰지 말 것(locale 을 모르는 표기라 어느 칸에 넣을지 알 수 없다).
func (c *Client) AliasNames(ctx context.Context, mbid string) ([]string, error) {
	d, err := c.fetchDetail(ctx, mbid)
	if err != nil || d == nil {
		return nil, err
	}
	out := make([]string, 0, len(d.Aliases)+1)
	if d.Name != "" {
		out = append(out, d.Name)
	}
	for _, a := range d.Aliases {
		if a.Name != "" && !contains(out, a.Name) {
			out = append(out, a.Name)
		}
	}
	return out, nil
}

// FetchAliases — inc=aliases. (검증 없는 raw fetch — 가능하면 FindAliases 사용.)
func (c *Client) FetchAliases(ctx context.Context, mbid string) (AliasByLocale, error) {
	d, err := c.fetchDetail(ctx, mbid)
	if err != nil || d == nil {
		return nil, err
	}
	return d.byLocale(), nil
}

// ArtistURL — MBID 의 사람이 열어볼 수 있는 정식 주소.
func ArtistURL(mbid string) string {
	mbid = strings.TrimSpace(mbid)
	if mbid == "" {
		return ""
	}
	return "https://musicbrainz.org/artist/" + mbid
}

// FindAliases — name 검색 후, 반환된 artist 의 name/alias 가 query 와 정규화
// 일치하는(= 실제로 그 인물/그룹인) 첫 후보의 **MBID 와** alias 를 반환한다.
// country:KR 검색이 fuzzy 매칭으로 엉뚱한 아티스트의 top hit 을 돌려줄 때 그 표기가
// canonical 로 흘러드는 오염(박보검-class)을 막는다. 검증 통과 후보가 없으면 "", nil, nil.
//
// ★MBID 를 함께 돌려주는 이유(2026-08-03). 종전 시그니처는 alias 만 반환해서
// 호출자가 "어느 아티스트로 확정한 건가"를 기록할 방법이 없었다. 그 결과 값에는
// canonical_*_source='musicbrainz' 라벨이 붙는데 external_ref 는 없는 상태가
// 484건 쌓였고(api-source-no-ref), 라벨만 보고는 되짚기·재검증이 불가능했다.
// 이 시스템에서 네 번째로 확인된 "포인터를 버리고 값만 쓴다" 계열 결함이다
// (① 기사 URL 5ed2af4 ② gemma 근거문자열 acccac8 ③ 이것 ④ kofic external_id=영어제목).
// 동일 판정을 두 번 하지 않도록 시그니처 자체를 바꿨다 — 호출자가 MBID 를 받고도
// 안 쓰는 건 눈에 띄지만, 애초에 안 주면 안 쓴 걸 아무도 모른다.
func (c *Client) FindAliases(ctx context.Context, name string) (string, AliasByLocale, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", nil, nil
	}
	artists, err := c.Search(ctx, name)
	if err != nil {
		return "", nil, err
	}
	want := normalizeName(name)
	if want == "" {
		return "", nil, nil
	}
	const maxProbe = 3 // top hit 이 오매칭이면 몇 후보만 더 확인 (rate-limit 비용).
	for i, art := range artists {
		if i >= maxProbe {
			break
		}
		d, err := c.fetchDetail(ctx, art.ID)
		if err != nil || d == nil {
			continue
		}
		// 검색 결과의 name 자체도 후보 (fetch 가 누락한 표기 보완).
		if normalizeName(art.Name) == want || d.matchesQuery(want) {
			out := d.byLocale()
			if len(out) > 0 {
				return art.ID, out, nil
			}
		}
	}
	return "", nil, nil
}

// Recording — /recording·/release-group 검색 결과 1건(공통 형태).
// Artists 는 artist-credit 의 크레딧명+아티스트 정식명을 평탄화한 목록.
type Recording struct {
	ID      string
	Title   string
	Score   int
	Artists []string
}

type recordingSearchResp struct {
	Recordings []struct {
		ID           string `json:"id"`
		Title        string `json:"title"`
		Score        int    `json:"score"`
		ArtistCredit []struct {
			Name   string `json:"name"`
			Artist struct {
				Name string `json:"name"`
			} `json:"artist"`
		} `json:"artist-credit"`
	} `json:"recordings"`
	ReleaseGroups []struct {
		ID           string `json:"id"`
		Title        string `json:"title"`
		Score        int    `json:"score"`
		ArtistCredit []struct {
			Name   string `json:"name"`
			Artist struct {
				Name string `json:"name"`
			} `json:"artist"`
		} `json:"artist-credit"`
	} `json:"release-groups"`
}

// SearchRecordings — 곡 제목+아티스트 스코프 검색 (2026-07-23 Phase1: song_album
// candidate 승급 드레인용). 아티스트 스코프 없는 호출은 오염 위험(동명 외국곡)이라
// 지원하지 않는다 — artist 빈 문자열이면 nil 반환.
func (c *Client) SearchRecordings(ctx context.Context, title, artist string) ([]Recording, error) {
	return c.searchWork(ctx, "recording", "recording", title, artist)
}

// SearchReleaseGroups — 앨범(release-group) 제목+아티스트 스코프 검색.
func (c *Client) SearchReleaseGroups(ctx context.Context, title, artist string) ([]Recording, error) {
	return c.searchWork(ctx, "release-group", "releasegroup", title, artist)
}

func (c *Client) searchWork(ctx context.Context, resource, field, title, artist string) ([]Recording, error) {
	title = strings.TrimSpace(title)
	artist = strings.TrimSpace(artist)
	if title == "" || artist == "" {
		return nil, nil
	}
	q := url.Values{}
	q.Set("query", field+`:"`+title+`" AND artist:"`+artist+`"`)
	q.Set("fmt", "json")
	q.Set("limit", "5")
	body, err := c.get(ctx, "/"+resource+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	var resp recordingSearchResp
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("mb %s decode: %w", resource, err)
	}
	var out []Recording
	appendRec := func(id, t string, score int, credits []struct {
		Name   string `json:"name"`
		Artist struct {
			Name string `json:"name"`
		} `json:"artist"`
	}) {
		r := Recording{ID: id, Title: t, Score: score}
		for _, ac := range credits {
			if ac.Name != "" {
				r.Artists = append(r.Artists, ac.Name)
			}
			if ac.Artist.Name != "" && ac.Artist.Name != ac.Name {
				r.Artists = append(r.Artists, ac.Artist.Name)
			}
		}
		out = append(out, r)
	}
	for _, r := range resp.Recordings {
		appendRec(r.ID, r.Title, r.Score, r.ArtistCredit)
	}
	for _, r := range resp.ReleaseGroups {
		appendRec(r.ID, r.Title, r.Score, r.ArtistCredit)
	}
	return out, nil
}

// ArtistMatches — 검색 결과의 아티스트 크레딧 중 하나가 기대 아티스트와 정규화
// 일치(또는 포함)하는지. 곡 승급의 아티스트 스코프 게이트.
func (r Recording) ArtistMatches(want string) bool {
	nw := normalizeName(want)
	if len([]rune(nw)) < 2 {
		return false
	}
	for _, a := range r.Artists {
		na := normalizeName(a)
		if na == nw {
			return true
		}
		if len([]rune(na)) >= 2 && (strings.Contains(na, nw) || strings.Contains(nw, na)) {
			return true
		}
	}
	return false
}

// normalizeName — 이름 비교용 정규화: 소문자 + 공백/중점/하이픈/마침표 제거.
// wikidata.normalizeName 와 동일 규칙 (표기차 흡수).
func normalizeName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch r {
		case ' ', '\t', '·', '・', '-', '.', '_', '\'', '"', ',':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// NameMatches reports whether two artist names are the same after applying
// MusicBrainz's conservative spelling normalization. Search scores alone are
// not an identity proof: a song title can receive a high score against an
// artist with the same text. Candidate promotion callers must require this
// exact normalized-name check in addition to their entity/resource contract.
func NameMatches(a, b string) bool {
	na := normalizeName(a)
	return na != "" && na == normalizeName(b)
}

// --- helpers --------------------------------------------------------------

func (c *Client) get(ctx context.Context, path string) ([]byte, error) {
	// rate limit — 동시성 안전 페이싱(1 req/s). 병렬 goroutine 이 c 를 공유해도
	// 호출이 minBetween 간격으로 직렬화된다.
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+path, nil)
	if err != nil {
		return nil, err
	}
	ua := c.UserAgent
	if ua == "" {
		ua = defaultUA
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "application/json")
	resp, err := httpx.Do(c.HTTPClient, req, 2)
	if err != nil {
		return nil, fmt.Errorf("mb http: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("mb not found")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mb status %d", resp.StatusCode)
	}
	limited := io.LimitReader(resp.Body, 1<<20) // 1 MiB cap
	return io.ReadAll(limited)
}

// mbLocaleToKDB — MusicBrainz locale → KDB canonical key.
// 무관 locale (de/fr/it 등) 은 빈 문자열 → drop.
func mbLocaleToKDB(loc string) string {
	switch strings.ToLower(loc) {
	case "ko":
		return "ko"
	case "en":
		return "en"
	case "ja":
		return "ja"
	case "vi":
		return "vi"
	case "zh":
		return "zh"
	case "zh-hant", "zh_hant":
		return "zh_hant"
	case "es":
		return "es"
	case "id":
		return "id"
	case "pt-br", "pt_br":
		return "pt_br"
	}
	return ""
}

func contains(arr []string, s string) bool {
	for _, x := range arr {
		if x == s {
			return true
		}
	}
	return false
}

// Package naver — 네이버 Open API(검색) 클라이언트. 검색기반 오염판별의 "한국어
// 정체성 앵커". encyc(지식백과) 설명의 역할 토큰으로 엔티티 정체성을 confirm 한다.
//
// ★핵심 설계(실측 근거): encyc 는 "확인(confirm)" 용이지 "거부" 용이 아니다.
//   - 작품/드라마/영화: 설명에 유형이 또렷이 나옴("…대한민국의 드라마이다") → 강한 confirm.
//   - 흔한 인물명(예: 김수현): 상위 결과가 역사인물 등 엉뚱하게 랭크됨 → 역할토큰이
//     안 보여도 오염이 아닐 수 있으므로 자동 거부 금지, "review" 플래그로만.
//
// 쿼터 1,000/일. 호출은 아껴 쓰고(호출부에서 batch 상한), 결과는 캐싱 권장.
package naver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyjoo73/kdb/internal/kdb/apikeys"
	"github.com/rickyjoo73/kdb/internal/kdb/sourcehealth"
)

type Item struct {
	Title       string `json:"title"`
	Link        string `json:"link"`
	Description string `json:"description"`
}

type SearchResult struct {
	Total int    `json:"total"`
	Items []Item `json:"items"`
}

type Client struct {
	id, secret string
	http       *http.Client
	mu         sync.Mutex
	last       time.Time
	minGap     time.Duration
}

// New — env(KDB_NAVER_CLIENT_ID/SECRET)에서 자격증명을 읽어 클라이언트를 만든다.
func New() (*Client, error) {
	id := strings.TrimSpace(os.Getenv("KDB_NAVER_CLIENT_ID"))
	sec := strings.TrimSpace(os.Getenv("KDB_NAVER_CLIENT_SECRET"))
	return NewWithCredentials(id, sec)
}

// NewFromSettings reads DB-backed admin settings first, then .env fallback.
func NewFromSettings(ctx context.Context, pool *pgxpool.Pool) (*Client, error) {
	id, _ := apikeys.Resolve(ctx, pool, "KDB_NAVER_CLIENT_ID")
	sec, _ := apikeys.Resolve(ctx, pool, "KDB_NAVER_CLIENT_SECRET")
	return NewWithCredentials(id, sec)
}

func NewWithCredentials(id, sec string) (*Client, error) {
	id = strings.TrimSpace(id)
	sec = strings.TrimSpace(sec)
	if id == "" || sec == "" {
		return nil, fmt.Errorf("naver: KDB_NAVER_CLIENT_ID/SECRET 미설정")
	}
	return &Client{
		id: id, secret: sec,
		http:   &http.Client{Timeout: 15 * time.Second},
		minGap: 300 * time.Millisecond, // 초당 ~3콜 이하 (예의상 throttle)
	}, nil
}

func (c *Client) throttle() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if d := c.minGap - time.Since(c.last); d > 0 {
		time.Sleep(d)
	}
	c.last = time.Now()
}

// Search — kind: "encyc"(지식백과) | "news" | "blog" 등. display 는 1~10 권장.
func (c *Client) Search(ctx context.Context, kind, query string, display int) (*SearchResult, error) {
	if display <= 0 {
		display = 5
	}
	c.throttle()
	u := fmt.Sprintf("https://openapi.naver.com/v1/search/%s.json?query=%s&display=%d",
		kind, url.QueryEscape(query), display)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Naver-Client-Id", c.id)
	req.Header.Set("X-Naver-Client-Secret", c.secret)
	resp, err := c.http.Do(req)
	if err != nil {
		sourcehealth.Record("naver", 0, err)
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		sourcehealth.Record("naver", resp.StatusCode, nil)
		return nil, fmt.Errorf("naver %s: http %d", kind, resp.StatusCode)
	}
	sourcehealth.Record("naver", http.StatusOK, nil)
	var out SearchResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// --- 정체성 검증 (오염판별) ------------------------------------------------

// Verdict — encyc 정체성 판정 결과.
//
//	confirmed : 기대 역할 토큰이 encyc 설명에 나타남 = 실재 K-엔티티(오염 아님)
//	review    : encyc 항목은 있으나 역할 토큰 불일치 = 애매(동명이인 랭킹 or 오염). 사람/gemma 판단 대상
//	no_entry  : encyc 항목 없음(total=0) = 니치이거나 정크. 약한 의심
type Verdict struct {
	Ko       string
	Type     string
	Status   string
	Total    int
	Evidence string
}

// roleTokens — entity_type 별 encyc 설명에서 확인할 역할/유형 토큰(부분일치).
//
// ★거름망을 뗀다 (2026-09-15, 범위 확대 0143).
//   종전 설계 주석은 이랬다: "K-엔터테인먼트 역할만 담는다. 정치인/역사인물·학자 등은
//   **일부러 제외** → K-범위밖 오염이 review 로 걸리게 함(거름망 역할)."
//
//   범위가 "한국의 인물·작품·조직·기관"으로 넓어지면서 그 거름망이 정확히 반대로
//   일하게 됐다. 같은 계열의 거름망이 위키데이터 쪽에도 있었고(isKEntertainerDesc)
//   같은 날 뗐다. 한쪽만 떼면 다른 쪽이 그대로 막는다 — 실제로 그럴 뻔했다.
//
//   "한국 대상인가"는 여전히 묻는다. 뗀 것은 "연예인가"뿐이다.
var roleTokens = map[string][]string{
	"person": {
		// 연예
		"배우", "가수", "감독", "모델", "방송인", "아이돌", "래퍼", "코미디언", "개그맨", "성우",
		"작곡가", "작사가", "프로듀서", "연예인", "탤런트", "안무가", "댄서", "무용수", "유튜버",
		"인플루언서", "크리에이터", "스트리머", "셰프", "요리사", "쇼호스트", "뮤지컬", "트로트",
		"밴드", "DJ", "MC", "방송",
		// 스포츠
		"선수", "국가대표", "감독관", "코치", "구단주", "야구", "축구", "농구", "배구", "골프",
		"씨름", "태권도", "유도", "복싱", "쇼트트랙", "피겨", "프로게이머",
		// 정치·행정·법조
		"정치인", "대통령", "국회의원", "의원", "장관", "차관", "시장", "도지사", "군수", "구청장",
		"당대표", "원내대표", "대변인", "판사", "검사", "변호사", "외교관", "대사", "공무원",
		// 경제
		"기업인", "회장", "대표이사", "사장", "창업자", "경영인", "은행장", "경제학자",
		// 학계·언론·의료
		"교수", "학자", "연구원", "박사", "과학자", "역사학자", "언론인", "기자", "아나운서",
		"앵커", "논설위원", "의사", "한의사", "약사", "작가", "소설가", "시인", "평론가",
	},
	"group":          {"그룹", "걸그룹", "보이그룹", "밴드", "듀오", "아이돌", "트리오", "혼성"},
	"drama":          {"드라마", "미니시리즈", "연속극", "시트콤", "웹드라마"},
	"movie":          {"영화", "작품"},
	"show":           {"예능", "프로그램", "쇼", "방송", "리얼리티", "버라이어티"},
	"song_album":     {"노래", "음반", "앨범", "싱글", "곡", "수록곡", "타이틀곡", "OST", "음원"},
	"agency":         {"기획사", "엔터테인먼트", "레이블", "소속사", "매니지먼트", "회사", "기업", "법인"},
	"channel_outlet": {"방송", "채널", "방송사", "언론", "신문", "매체", "OTT", "플랫폼", "미디어"},
	"brand_place":    {"브랜드", "장소", "지역", "명소", "관광", "건물", "도시", "전시관", "미술관", "박물관", "카페", "레스토랑", "식당", "호텔", "리조트", "테마파크", "쇼핑몰", "매장", "스튜디오", "공간"},
	"event_tour":     {"공연", "콘서트", "투어", "행사", "축제", "페스티벌", "팬미팅", "내한"},
	"character":      {"캐릭터", "등장인물", "배역", "주인공"},

	// ── 정치·경제·시사·스포츠 유형 (0143)
	"political_party": {"정당", "여당", "야당", "원내", "정치", "창당"},
	"government_body": {"부처", "청", "위원회", "공단", "공사", "공공기관", "정부", "행정", "지자체", "국회", "법원", "검찰", "기관"},
	"company":         {"기업", "회사", "법인", "주식회사", "그룹", "상장", "제조", "제조업", "금융", "은행", "증권", "보험", "전자", "화학", "건설", "통신"},
	"organization":    {"협회", "재단", "단체", "학회", "노조", "조합", "연맹", "사단법인", "재단법인", "기구", "위원회"},
	"sports_team":     {"구단", "프로팀", "국가대표팀", "선수단", "축구단", "야구단", "농구단", "배구단", "팀"},
	"school":          {"학교", "대학", "대학교", "고등학교", "중학교", "초등학교", "대학원", "교육기관", "학원"},
}

var tagRe = regexp.MustCompile(`<[^>]+>`)

func stripTags(s string) string { return strings.TrimSpace(tagRe.ReplaceAllString(s, "")) }

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// VerifyIdentity — ko(canonical_ko) 를 encyc 에서 찾아 entityType 역할과 대조.
// display=5 개 상위 결과 중 어느 하나라도 기대 역할 토큰을 담으면 confirmed.
func (c *Client) VerifyIdentity(ctx context.Context, ko, entityType string) (Verdict, error) {
	v := Verdict{Ko: ko, Type: entityType}
	res, err := c.Search(ctx, "encyc", ko, 5)
	if err != nil {
		return v, err
	}
	v.Total = res.Total
	if res.Total == 0 || len(res.Items) == 0 {
		v.Status = "no_entry"
		return v, nil
	}
	tokens := roleTokens[entityType]
	for _, it := range res.Items {
		desc := stripTags(it.Description)
		for _, tok := range tokens {
			if strings.Contains(desc, tok) {
				v.Status = "confirmed"
				v.Evidence = stripTags(it.Title) + " | " + truncate(desc, 60)
				return v, nil
			}
		}
	}
	// 역할 토큰 미발견 = 애매(동명이인 랭킹 or 오염). 자동 거부하지 않고 review.
	v.Status = "review"
	v.Evidence = stripTags(res.Items[0].Title) + " | " + truncate(stripTags(res.Items[0].Description), 60)
	return v, nil
}

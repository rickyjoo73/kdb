package apikeys

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ProbeResult — 한 API 연결 테스트 결과.
type ProbeResult struct {
	Title   string
	OK      bool
	Detail  string
	Skipped bool // 키 미설정/테스트 불가
}

var probeClient = &http.Client{Timeout: 12 * time.Second}

// Probe — 외부 API 연결을 실제 호출로 점검. admin "연결 테스트" / CLI api-test 용.
func Probe(ctx context.Context, pool *pgxpool.Pool) []ProbeResult {
	var out []ProbeResult

	// TMDb — /authentication (Bearer 토큰 검증).
	if token, _ := Resolve(ctx, pool, "KDB_TMDB_API_TOKEN"); token != "" {
		ok, det := probeBearer(ctx, "https://api.themoviedb.org/3/authentication", token, "success")
		out = append(out, ProbeResult{Title: "TMDb", OK: ok, Detail: det})
	} else {
		out = append(out, ProbeResult{Title: "TMDb", Skipped: true, Detail: "키 미설정"})
	}

	// KOFIC — 일별 박스오피스(어제) 조회.
	if key, _ := Resolve(ctx, pool, "KDB_KOFIC_API_KEY"); key != "" {
		dt := time.Now().AddDate(0, 0, -1).Format("20060102")
		u := "http://www.kobis.or.kr/kobisopenapi/webservice/rest/boxoffice/searchDailyBoxOfficeList.json?key=" +
			url.QueryEscape(key) + "&targetDt=" + dt
		ok, det := probeContains(ctx, u, "boxOfficeResult")
		out = append(out, ProbeResult{Title: "KOFIC", OK: ok, Detail: det})
	} else {
		out = append(out, ProbeResult{Title: "KOFIC", Skipped: true, Detail: "키 미설정"})
	}

	// KMDb — 키 있을 때만.
	if key, _ := Resolve(ctx, pool, "KDB_KMDB_API_KEY"); key != "" {
		out = append(out, ProbeResult{Title: "KMDb", OK: true, Detail: "키 설정됨(엔드포인트 미구현)"})
	} else {
		out = append(out, ProbeResult{Title: "KMDb", Skipped: true, Detail: "승인 대기/키 미설정"})
	}

	// Naver Search — 지식백과 1건 조회.
	if id, _ := Resolve(ctx, pool, "KDB_NAVER_CLIENT_ID"); strings.TrimSpace(id) != "" {
		if sec, _ := Resolve(ctx, pool, "KDB_NAVER_CLIENT_SECRET"); strings.TrimSpace(sec) != "" {
			u := "https://openapi.naver.com/v1/search/encyc.json?query=" + url.QueryEscape("BTS") + "&display=1"
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
			req.Header.Set("X-Naver-Client-Id", strings.TrimSpace(id))
			req.Header.Set("X-Naver-Client-Secret", strings.TrimSpace(sec))
			ok, det := doProbe(req, "items")
			out = append(out, ProbeResult{Title: "Naver Search", OK: ok, Detail: det})
		} else {
			out = append(out, ProbeResult{Title: "Naver Search", Skipped: true, Detail: "KDB_NAVER_CLIENT_SECRET 미설정"})
		}
	} else {
		out = append(out, ProbeResult{Title: "Naver Search", Skipped: true, Detail: "KDB_NAVER_CLIENT_ID 미설정"})
	}

	// Kakao/Daum Search — 웹문서 discovery 후보.
	if key, _ := Resolve(ctx, pool, "KDB_KAKAO_REST_API_KEY"); strings.TrimSpace(key) != "" {
		u := "https://dapi.kakao.com/v2/search/web?query=" + url.QueryEscape("BTS") + "&size=1"
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		req.Header.Set("Authorization", "KakaoAK "+strings.TrimSpace(key))
		ok, det := doProbe(req, "documents")
		out = append(out, ProbeResult{Title: "Kakao/Daum Search", OK: ok, Detail: det})
	} else {
		out = append(out, ProbeResult{Title: "Kakao/Daum Search", Skipped: true, Detail: "키 미설정"})
	}

	// MusicBrainz — 무인증 검색.
	{
		ok, det := probeContains(ctx, "https://musicbrainz.org/ws/2/artist?query=bts&fmt=json&limit=1", "artists")
		out = append(out, ProbeResult{Title: "MusicBrainz", OK: ok, Detail: det})
	}
	// Apple iTunes Search — 무인증 검색.
	{
		ok, det := probeContains(ctx, "https://itunes.apple.com/search?term=BTS&entity=song&limit=1&country=KR", "resultCount")
		out = append(out, ProbeResult{Title: "Apple iTunes Search", OK: ok, Detail: det})
	}
	// Discogs — 무토큰도 search 허용, 토큰이 있으면 rate-limit 완화.
	{
		u := "https://api.discogs.com/database/search?q=" + url.QueryEscape("BTS") + "&type=release&per_page=1"
		if token, _ := Resolve(ctx, pool, "KDB_DISCOGS_TOKEN"); strings.TrimSpace(token) != "" {
			u += "&token=" + url.QueryEscape(strings.TrimSpace(token))
		}
		ok, det := probeContains(ctx, u, "results")
		out = append(out, ProbeResult{Title: "Discogs", OK: ok, Detail: det})
	}
	// Spotify — client credentials token 발급으로 키쌍 검증.
	out = append(out, probeSpotify(ctx, pool))
	// YouTube Data API — 공식 채널/영상 evidence 탐색용.
	if key, _ := Resolve(ctx, pool, "KDB_YOUTUBE_API_KEY"); strings.TrimSpace(key) != "" {
		u := "https://www.googleapis.com/youtube/v3/search?part=snippet&type=channel&maxResults=1&q=" + url.QueryEscape("BTS") + "&key=" + url.QueryEscape(strings.TrimSpace(key))
		ok, det := probeContains(ctx, u, "items")
		out = append(out, ProbeResult{Title: "YouTube Data API", OK: ok, Detail: det})
	} else {
		out = append(out, ProbeResult{Title: "YouTube Data API", Skipped: true, Detail: "키 미설정"})
	}
	// Watchmode — 스트리밍 availability/locale title 후보(선택).
	if key, _ := Resolve(ctx, pool, "KDB_WATCHMODE_API_KEY"); strings.TrimSpace(key) != "" {
		u := "https://api.watchmode.com/v1/search/?apiKey=" + url.QueryEscape(strings.TrimSpace(key)) + "&search_field=name&search_value=" + url.QueryEscape("Squid Game")
		ok, det := probeContains(ctx, u, "title_results")
		out = append(out, ProbeResult{Title: "Watchmode", OK: ok, Detail: det})
	} else {
		out = append(out, ProbeResult{Title: "Watchmode", Skipped: true, Detail: "키 미설정"})
	}
	// Wikidata — 무인증 검색.
	{
		ok, det := probeContains(ctx, "https://www.wikidata.org/w/api.php?action=wbsearchentities&search=bts&language=en&format=json", "search")
		out = append(out, ProbeResult{Title: "Wikidata", OK: ok, Detail: det})
	}
	// Google News RSS — 무인증.
	{
		ok, det := probeContains(ctx, "https://news.google.com/rss/search?q=BTS&hl=ko&gl=KR&ceid=KR:ko", "<item>")
		out = append(out, ProbeResult{Title: "Google News RSS", OK: ok, Detail: det})
	}
	// Gemma 게이트웨이 — OpenAI 호환 /v1/models (Bearer). 대량 LLM provider 연결 점검.
	if base, _ := Resolve(ctx, pool, "KDB_GEMMA_BASE_URL"); strings.TrimSpace(base) != "" {
		if key, _ := Resolve(ctx, pool, "KDB_GEMMA_API_KEY"); strings.TrimSpace(key) != "" {
			ok, det := probeBearer(ctx, strings.TrimRight(base, "/")+"/v1/models", key, "data")
			out = append(out, ProbeResult{Title: "Gemma 게이트웨이", OK: ok, Detail: det})
		} else {
			out = append(out, ProbeResult{Title: "Gemma 게이트웨이", Skipped: true, Detail: "KDB_GEMMA_API_KEY 미설정"})
		}
	} else {
		out = append(out, ProbeResult{Title: "Gemma 게이트웨이", Skipped: true, Detail: "KDB_GEMMA_BASE_URL 미설정"})
	}
	return out
}

func probeSpotify(ctx context.Context, pool *pgxpool.Pool) ProbeResult {
	id, _ := Resolve(ctx, pool, "KDB_SPOTIFY_CLIENT_ID")
	sec, _ := Resolve(ctx, pool, "KDB_SPOTIFY_CLIENT_SECRET")
	id = strings.TrimSpace(id)
	sec = strings.TrimSpace(sec)
	if id == "" {
		return ProbeResult{Title: "Spotify", Skipped: true, Detail: "KDB_SPOTIFY_CLIENT_ID 미설정"}
	}
	if sec == "" {
		return ProbeResult{Title: "Spotify", Skipped: true, Detail: "KDB_SPOTIFY_CLIENT_SECRET 미설정"}
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://accounts.spotify.com/api/token",
		strings.NewReader(url.Values{"grant_type": []string{"client_credentials"}}.Encode()))
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(id+":"+sec)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	ok, det := doProbe(req, "access_token")
	return ProbeResult{Title: "Spotify", OK: ok, Detail: det}
}

func probeBearer(ctx context.Context, u, token, needle string) (bool, string) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("accept", "application/json")
	return doProbe(req, needle)
}

func probeContains(ctx context.Context, u, needle string) (bool, string) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("User-Agent", "mediafine-kdb-apitest/1.0 ( rickyjoo@aiinad.com )")
	return doProbe(req, needle)
}

func doProbe(req *http.Request, needle string) (bool, string) {
	start := time.Now()
	resp, err := probeClient.Do(req)
	if err != nil {
		return false, "연결 실패: " + err.Error()
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	ms := time.Since(start).Milliseconds()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Sprintf("HTTP %d (%dms)", resp.StatusCode, ms)
	}
	if needle != "" && !strings.Contains(string(body), needle) {
		return false, fmt.Sprintf("HTTP 200 이나 응답 형식 이상 (%dms)", ms)
	}
	return true, fmt.Sprintf("OK %dms", ms)
}

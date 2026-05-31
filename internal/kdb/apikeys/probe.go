package apikeys

import (
	"context"
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

	// MusicBrainz — 무인증 검색.
	{
		ok, det := probeContains(ctx, "https://musicbrainz.org/ws/2/artist?query=bts&fmt=json&limit=1", "artists")
		out = append(out, ProbeResult{Title: "MusicBrainz", OK: ok, Detail: det})
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
	return out
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

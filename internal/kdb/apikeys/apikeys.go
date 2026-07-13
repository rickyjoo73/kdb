// Package apikeys — 외부 API 키 레지스트리 + 해석(DB 우선, .env fallback).
//
// 운영자 지시(2026-05-31): KDB 가 사용하는 모든 API 를 admin 설정 페이지에 등록하고
// 키를 입력/편집할 수 있게 한다. 키는 kwave_kdb_api_settings(DB)에 저장되며, 없으면
// 환경변수(.env)를 쓴다. enrich 클라이언트는 Resolve() 로 키를 얻는다.
package apikeys

import (
	"context"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Spec — 하나의 외부 API 메타데이터(admin 페이지 표시용).
type Spec struct {
	EnvVar      string   // 환경변수명 = DB key name. "" 이면 키 불필요 API.
	Title       string   // 표시 이름
	Purpose     string   // 용도
	IssueURL    string   // 키 발급처 (키 필요 시)
	EntityTypes []string // 이 API 가 채우는 entity_type
	Keyless     bool     // 키 없이 동작(무인증/CLI)
}

// Specs — KDB 가 사용하는 모든 API. admin 페이지가 이 목록을 등록·표시한다.
func Specs() []Spec {
	return []Spec{
		{EnvVar: "KDB_TMDB_API_TOKEN", Title: "TMDb (API Read Token v4)", Purpose: "해외/국내 영화·드라마·예능 다국어 제목·방영일·메타",
			IssueURL: "https://www.themoviedb.org/settings/api", EntityTypes: []string{"movie", "drama", "show"}},
		{EnvVar: "KDB_TMDB_API_KEY", Title: "TMDb (API Key v3, 선택)", Purpose: "TMDb v3 호환 키(v4 토큰 우선)",
			IssueURL: "https://www.themoviedb.org/settings/api", EntityTypes: []string{"movie", "drama", "show"}},
		{EnvVar: "KDB_KOFIC_API_KEY", Title: "KOFIC (영화진흥위원회 KOBIS)", Purpose: "국내 영화 박스오피스·영화/인물 DB",
			IssueURL: "https://www.kobis.or.kr/kobisopenapi", EntityTypes: []string{"movie"}},
		{EnvVar: "KDB_KMDB_API_KEY", Title: "KMDb (한국영상자료원)", Purpose: "국내 영화 상세·인물·스틸",
			IssueURL: "https://www.kmdb.or.kr/info/api/apiList", EntityTypes: []string{"movie"}},
		{EnvVar: "KDB_NAVER_CLIENT_ID", Title: "Naver Search Client ID", Purpose: "지식백과/뉴스/웹문서 탐색·정체성 검증",
			IssueURL: "https://developers.naver.com/docs/serviceapi/search/", EntityTypes: []string{"all"}},
		{EnvVar: "KDB_NAVER_CLIENT_SECRET", Title: "Naver Search Client Secret", Purpose: "Naver Search API 인증 secret",
			IssueURL: "https://developers.naver.com/docs/serviceapi/search/", EntityTypes: []string{"all"}},
		{EnvVar: "KDB_KAKAO_REST_API_KEY", Title: "Kakao/Daum Search REST Key", Purpose: "공식 페이지/영상/웹문서 discovery 후보",
			IssueURL: "https://developers.kakao.com/docs/latest/ko/daum-search/dev-guide", EntityTypes: []string{"all"}},
		{EnvVar: "", Title: "MusicBrainz", Purpose: "그룹/가수 다국어 표기·관계 (무인증)", Keyless: true,
			IssueURL: "https://musicbrainz.org", EntityTypes: []string{"group", "person"}},
		{EnvVar: "", Title: "Apple iTunes Search", Purpose: "곡/앨범/아티스트 store ID 확인 (무인증)", Keyless: true,
			IssueURL: "https://developer.apple.com/library/archive/documentation/AudioVideo/Conceptual/iTuneSearchAPI/", EntityTypes: []string{"song_album", "group", "person"}},
		{EnvVar: "KDB_DISCOGS_TOKEN", Title: "Discogs Token (선택)", Purpose: "release/artist confirm, 무토큰보다 rate-limit 완화",
			IssueURL: "https://www.discogs.com/developers", EntityTypes: []string{"song_album", "group", "person"}},
		{EnvVar: "KDB_SPOTIFY_CLIENT_ID", Title: "Spotify Client ID", Purpose: "artist/album/track/show ID 확인(계획)",
			IssueURL: "https://developer.spotify.com/documentation/web-api", EntityTypes: []string{"song_album", "group", "person", "show"}},
		{EnvVar: "KDB_SPOTIFY_CLIENT_SECRET", Title: "Spotify Client Secret", Purpose: "Spotify client credentials 인증(계획)",
			IssueURL: "https://developer.spotify.com/documentation/web-api", EntityTypes: []string{"song_album", "group", "person", "show"}},
		{EnvVar: "KDB_YOUTUBE_API_KEY", Title: "YouTube Data API Key", Purpose: "공식 채널/MV/방송 클립 증거 탐색(리뷰용)",
			IssueURL: "https://developers.google.com/youtube/v3/docs/search/list", EntityTypes: []string{"song_album", "group", "person", "show"}},
		{EnvVar: "KDB_WATCHMODE_API_KEY", Title: "Watchmode API Key (선택)", Purpose: "스트리밍 availability/locale title 후보(계획)",
			IssueURL: "https://api.watchmode.com/docs", EntityTypes: []string{"movie", "drama", "show"}},
		{EnvVar: "", Title: "Wikidata", Purpose: "모든 entity 9개 언어 + sitelinks·claims (무인증)", Keyless: true,
			IssueURL: "https://www.wikidata.org", EntityTypes: []string{"all"}},
		{EnvVar: "", Title: "Google News RSS", Purpose: "RSS 수집(국내/해외) + '모르면 검색' 문맥 (무인증)", Keyless: true,
			IssueURL: "https://news.google.com/rss", EntityTypes: []string{"all"}},
		{EnvVar: "", Title: "Codex (gpt-5.5)", Purpose: "하이브리드 고난도 LLM — 동명이인·dataqa·현지화(작품 공식명)·corrections 검증 (CLI/ChatGPT OAuth)", Keyless: true,
			IssueURL: "", EntityTypes: []string{"all"}},
		{EnvVar: "KDB_GEMMA_API_KEY", Title: "Gemma 통합 게이트웨이 (ai)", Purpose: "하이브리드 대량 LLM — 추출·분류·게이트키퍼·reconcile·인물필드 (OpenAI 호환). base=KDB_GEMMA_BASE_URL, model=KDB_GEMMA_MODEL",
			IssueURL: "", EntityTypes: []string{"all"}},
	}
}

// Resolve — envVar 키 값을 DB(kwave_kdb_api_settings) 우선, 없으면 환경변수에서 얻는다.
// 반환 source: "db" | "env" | "" (미설정).
func Resolve(ctx context.Context, pool *pgxpool.Pool, envVar string) (value, source string) {
	if envVar == "" {
		return "", ""
	}
	if pool != nil {
		var v string
		if err := pool.QueryRow(ctx,
			`SELECT value FROM kwave_kdb_api_settings WHERE name=$1`, envVar).Scan(&v); err == nil && v != "" {
			return v, "db"
		}
	}
	if v := os.Getenv(envVar); v != "" {
		return v, "env"
	}
	return "", ""
}

// Set — DB 에 키 저장(upsert). 빈 값이면 삭제(= .env fallback 으로 복귀).
func Set(ctx context.Context, pool *pgxpool.Pool, name, value, by string) error {
	if value == "" {
		_, err := pool.Exec(ctx, `DELETE FROM kwave_kdb_api_settings WHERE name=$1`, name)
		return err
	}
	_, err := pool.Exec(ctx, `
INSERT INTO kwave_kdb_api_settings (name, value, updated_at, updated_by)
VALUES ($1, $2, now(), $3)
ON CONFLICT (name) DO UPDATE SET value=EXCLUDED.value, updated_at=now(), updated_by=EXCLUDED.updated_by`,
		name, value, by)
	return err
}

// Mask — 표시용 마스킹. 값 전체를 노출하지 않고 끝 4자만.
func Mask(v string) string {
	if v == "" {
		return ""
	}
	r := []rune(v)
	if len(r) <= 4 {
		return "••••"
	}
	return "••••" + string(r[len(r)-4:])
}

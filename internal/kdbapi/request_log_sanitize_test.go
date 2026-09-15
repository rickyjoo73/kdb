package kdbapi

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// 운영 DB 로그에서 발견: invalid byte sequence for encoding "UTF8": 0xeb
// `q = q[:500]` 이 한글(3바이트)을 반 토막 내 INSERT 가 통째로 실패했고,
// detached goroutine 이 `_, _ =` 로 오류를 버려 아무도 모르게 요청 기록이 사라졌다.
func TestSanitizeLogTextKeepsUTF8Valid(t *testing.T) {
	q := "source_text=" + strings.Repeat("해운대해수욕장", 80) // 500바이트를 훌쩍 넘김
	if utf8.ValidString(q[:500]) {
		t.Fatal("전제가 틀렸다: 500바이트 절단이 글자 경계와 맞았다 — 픽스처를 바꿔야 한다")
	}
	got := sanitizeLogText(q, 500)
	if !utf8.ValidString(got) {
		t.Fatalf("잘린 글자가 남았다 — Postgres 가 INSERT 를 거부한다: %q", got)
	}
	if len(got) > 500 {
		t.Fatalf("상한 초과 len=%d", len(got))
	}
}

func TestSanitizeLogTextDropsBrokenEncoding(t *testing.T) {
	got := sanitizeLogText("q="+string([]byte{0xeb, 0xb0})+"값", 500)
	if !utf8.ValidString(got) {
		t.Fatalf("깨진 바이트가 남았다: %q", got)
	}
}

// ★권장 경로를 열었으면 그 경로를 보는 창도 같이 열어야 한다 (2026-09-15).
//
//	lookup/bulk 를 객체 배열(queries)로 바꾸면서 요청 프리뷰 추출기를 안 고쳤다.
//	소비자가 문서대로 묶음으로 옮겨오자 관리 화면의 프리뷰가 전부 빈칸이 됐다 —
//	무엇을 물었는지 기록이 안 남았다. 실측: 15:33 bulk 200 · 2051ms · 프리뷰 없음.
func TestBulkQueriesShowUpInTheRequestLog(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{
			name: "객체 배열",
			body: `{"queries":[{"ko":"채영","type":"person"},{"ko":"폭싹 속았수다","type":"drama"}],"verified_only":true}`,
			want: "채영, 폭싹 속았수다",
		},
		{
			name: "문자열 배열(기존 소비자)",
			body: `{"queries":["아이유","뉴진스"]}`,
			want: "아이유, 뉴진스",
		},
		{
			name: "섞여 있어도",
			body: `{"queries":["아이유",{"ko":"채영","type":"person"}]}`,
			want: "아이유, 채영",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := extractRequestKeyword([]byte(c.body)); got != c.want {
				t.Fatalf("preview = %q, want %q", got, c.want)
			}
		})
	}
}

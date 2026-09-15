package kdbapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"
)

// apiRequestLogger — 인증 통과한 /v1/* 클라이언트 요청을 DB(kwave_kdb_api_requests)에
// 비동기 기록한다. 대시보드 "클라이언트 요청" 가시화의 데이터 원천. 인증 미들웨어가
// 컨텍스트에 심은 소비자 id·tier 를 읽으므로 반드시 auth 뒤에 배선한다(=더 안쪽).
// health/docs 는 잡음이라 제외. 응답을 블록하지 않도록 INSERT 는 detached goroutine.
//
// ★요청 키워드는 대부분 POST 본문(JSON: source_text/terms 등)에 오므로 URL RawQuery 만
// 로깅하면 admin 에 표시할 게 없다. POST 본문을 버퍼링(핸들러용으로 복원)해 요청 키워드
// 프리뷰를 추출·기록한다(query 우선순위: URL RawQuery > 본문 프리뷰).
func apiRequestLogger(pool *pgxpool.Pool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			// POST 본문에서 요청 키워드 프리뷰 추출 (본문은 핸들러용으로 반드시 복원).
			bodyPreview := ""
			if r.Method == http.MethodPost && r.Body != nil &&
				strings.HasPrefix(r.URL.Path, "/v1/") &&
				r.URL.Path != "/v1/health" && r.URL.Path != "/v1/docs" {
				if buf, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)); err == nil {
					_ = r.Body.Close()
					r.Body = io.NopCloser(bytes.NewReader(buf))
					bodyPreview = extractRequestKeyword(buf)
				}
			}

			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			if pool == nil || !strings.HasPrefix(r.URL.Path, "/v1/") ||
				r.URL.Path == "/v1/health" || r.URL.Path == "/v1/docs" {
				return
			}
			cid, _ := r.Context().Value(ctxKeyConsumer).(string)
			tier, _ := r.Context().Value(ctxKeyTier).(keyTier)
			q := r.URL.RawQuery
			if q == "" {
				q = bodyPreview
			}
			q = sanitizeLogText(q, 500)
			status := rec.status
			method := r.Method
			path := r.URL.Path
			dur := int(time.Since(start).Milliseconds())
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				var cidArg any
				if cid != "" {
					cidArg = cid
				}
				_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_api_requests (consumer_id, tier, method, path, query, status, duration_ms)
VALUES ($1,$2,$3,$4,$5,$6,$7)`, cidArg, string(tier), method, path, q, status, dur)
			}()
		})
	}
}

// sanitizeLogText — 로그용 문자열을 **UTF-8 로 유효하게** 만들고 글자 경계에서 자른다.
//
// 종전엔 `q = q[:500]` 이었다. 한글은 UTF-8 에서 3바이트라 500바이트 경계가 글자 한가운데
// 떨어지면 잘린 조각(0xEB… 같은 선행 바이트)만 남는다. Postgres 는 그런 값을
// `invalid byte sequence for encoding "UTF8"` 로 거부하고, INSERT 가 통째로 실패한다.
// INSERT 는 detached goroutine 에서 `_, _ =` 로 오류를 버리므로 **아무도 모르게** 사라진다 —
// 요청은 정상 처리되고 감사 로그에만 구멍이 난다. 운영 DB 로그를 읽다가 발견했다.
//
// 잘못된 바이트는 버리고(클라이언트가 보낸 깨진 인코딩도 여기서 걸러진다),
// 저장 의도였던 500바이트 상한은 지키되 **마지막 온전한 글자까지만** 남긴다.
func sanitizeLogText(s string, maxBytes int) string {
	s = strings.ToValidUTF8(s, "")
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// extractRequestKeyword — /v1 POST 본문(JSON)에서 요청한 키워드/용어를 사람이 읽을 수 있는
// 프리뷰로 뽑는다. match=source_text, prepare/lookup=terms(문자열 또는 {ko,type}),
// 그 외 흔한 단일 필드(q/text/ko/name) 폴백. 실패시 "".
func extractRequestKeyword(body []byte) string {
	body = bytes.TrimSpace(body)
	if len(body) == 0 || body[0] != '{' {
		return ""
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return ""
	}
	if s := jsonPreviewString(m["source_text"]); s != "" {
		return s
	}
	// ★`queries` 를 빠뜨리면 **권장 경로가 통째로 안 보인다** (2026-09-15).
	//   lookup/bulk 를 객체 배열로 바꾸면서 여기를 같이 안 고쳤다. 소비자가 문서대로
	//   묶음으로 옮겨오자 관리 화면의 요청 프리뷰가 전부 빈칸이 됐다 — 무엇을 물었는지
	//   기록이 안 남는다. 새 문을 열 때 그 문을 보는 창도 같이 열어야 한다.
	for _, k := range []string{"terms", "queries", "source_texts", "names"} {
		if arr := jsonPreviewArray(m[k]); len(arr) > 0 {
			return strings.Join(arr, ", ")
		}
	}
	for _, k := range []string{"q", "text", "ko", "name", "query"} {
		if s := jsonPreviewString(m[k]); s != "" {
			return s
		}
	}
	return ""
}

// jsonPreviewString — RawMessage 가 JSON 문자열이면 trim 해서 반환, 아니면 "".
func jsonPreviewString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	return ""
}

// jsonPreviewArray — 문자열 배열 또는 {ko,name,text} 객체 배열에서 최대 20개 라벨 추출.
func jsonPreviewArray(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var arr []json.RawMessage
	if json.Unmarshal(raw, &arr) != nil {
		return nil
	}
	out := make([]string, 0, len(arr))
	for i, el := range arr {
		if i >= 20 {
			break
		}
		if s := jsonPreviewString(el); s != "" {
			out = append(out, s)
			continue
		}
		var obj map[string]json.RawMessage
		if json.Unmarshal(el, &obj) == nil {
			for _, k := range []string{"ko", "name", "text"} {
				if s := jsonPreviewString(obj[k]); s != "" {
					out = append(out, s)
					break
				}
			}
		}
	}
	return out
}

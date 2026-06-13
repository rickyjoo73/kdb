// Package gemma — ai2 게이트웨이(OpenAI 호환)의 gemma 모델 호출. codex 대체/보완용
// LLM. codex(ChatGPT OAuth, http_error 빈발·분 단위 지연)와 달리 일반 HTTP API 라
// 빠르고 동시성도 높일 수 있다. 신뢰는 호출측 가드(문자셋·source 위계·외부검색
// 우선)가 보장하므로, gemma 도 codex 와 동일한 저신뢰(LLM 합성) 등급으로 다룬다.
//
// codexcli.Runner.Run 이 KDB_LLM_PROVIDER=gemma 일 때 이 패키지로 디스패치한다.
package gemma

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// gemma 동시성 — codex 의 OAuth single-flight 제약이 없으므로 높게.
var sem = make(chan struct{}, concurrency())

func concurrency() int {
	if v := os.Getenv("KDB_GEMMA_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			return n
		}
	}
	// 게이트웨이 실측: 4~6 동시는 절반이 타임아웃(사실상 직렬). 2 로 캡해 큐 폭주·
	// 타임아웃을 줄인다(KDB_GEMMA_CONCURRENCY 로 조정).
	return 2
}

// Configured — gemma 게이트웨이가 설정됐는지(base url + key).
func Configured() bool {
	return strings.TrimSpace(os.Getenv("KDB_GEMMA_BASE_URL")) != "" &&
		strings.TrimSpace(os.Getenv("KDB_GEMMA_API_KEY")) != ""
}

type chatReq struct {
	Model       string         `json:"model"`
	Messages    []message      `json:"messages"`
	Temperature float64        `json:"temperature"`
	TopP        float64        `json:"top_p"`
	Stream      bool           `json:"stream"`
	ChatKwargs  map[string]any `json:"chat_template_kwargs,omitempty"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResp struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Complete — prompt 를 gemma chat completion 으로 보내고, JSON 응답만 추출해 반환.
// schema 는 strict 강제는 못 하지만 프롬프트에 출력 형식을 못박아 JSON 을 유도한다
// (codex 의 --output-schema 대체). 반환은 codexcli.Run 과 동일 계약(json.RawMessage).
func Complete(ctx context.Context, prompt string, schema []byte) (json.RawMessage, error) {
	base := strings.TrimRight(os.Getenv("KDB_GEMMA_BASE_URL"), "/")
	key := os.Getenv("KDB_GEMMA_API_KEY")
	model := os.Getenv("KDB_GEMMA_MODEL")
	if model == "" {
		model = "gemma-4-26b-a4b"
	}
	if base == "" || key == "" {
		return nil, errors.New("gemma: KDB_GEMMA_BASE_URL/API_KEY 미설정")
	}

	// 동시성 슬롯(부모 ctx 존중).
	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	sys := "You output ONLY a single minified JSON value. No markdown code fences, no prose, no explanation."
	if len(schema) > 0 {
		sys += " The JSON MUST conform to this schema:\n" + string(schema)
	}
	body, _ := json.Marshal(chatReq{
		Model: model,
		Messages: []message{
			{Role: "system", Content: sys},
			{Role: "user", Content: prompt},
		},
		// 창작 억제: temperature 0 + top_p 0.1 로 가장 확률 높은 토큰만(가이드 준수↑).
		Temperature: 0,
		TopP:        0.1,
		Stream:      false,
		// ★추론 OFF — 실측: enable_thinking=false 면 4.5s→0.77s(6배), 토큰 189→14.
		// 표기/번역은 thinking 불필요(지식 회상). 게이트웨이 부하·지연 급감.
		ChatKwargs: map[string]any{"enable_thinking": false},
	})

	timeout := 120 * time.Second
	if v := os.Getenv("KDB_GEMMA_TIMEOUT_MS"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			timeout = time.Duration(ms) * time.Millisecond
		}
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, base+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gemma: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gemma: http %d", resp.StatusCode)
	}
	var cr chatResp
	if err := json.Unmarshal(raw, &cr); err != nil {
		return nil, fmt.Errorf("gemma decode: %w", err)
	}
	if cr.Error != nil {
		return nil, fmt.Errorf("gemma: %s", cr.Error.Message)
	}
	if len(cr.Choices) == 0 {
		return nil, errors.New("gemma: empty choices")
	}
	js := extractJSON(cr.Choices[0].Message.Content)
	if js == "" {
		return nil, errors.New("gemma: 응답에서 JSON 추출 실패")
	}
	if !json.Valid([]byte(js)) {
		return nil, fmt.Errorf("gemma: invalid JSON output")
	}
	return json.RawMessage(js), nil
}

// extractJSON — gemma 응답에서 JSON 본문만 추출. ```json fence 제거 + 첫 { 또는 [
// 부터 짝 맞는 끝까지. 모델이 prose 를 섞어도 견고하게 파싱.
func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	// ```json ... ``` fence 제거
	if i := strings.Index(s, "```"); i >= 0 {
		s = s[i+3:]
		if strings.HasPrefix(strings.ToLower(s), "json") {
			s = s[4:]
		}
		if j := strings.LastIndex(s, "```"); j >= 0 {
			s = s[:j]
		}
		s = strings.TrimSpace(s)
	}
	// 첫 { 또는 [ 위치
	start := -1
	var open, close byte
	for i := 0; i < len(s); i++ {
		if s[i] == '{' {
			start, open, close = i, '{', '}'
			break
		}
		if s[i] == '[' {
			start, open, close = i, '[', ']'
			break
		}
	}
	if start < 0 {
		return ""
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(s); i++ {
		c := s[i]
		switch {
		case esc:
			esc = false
		case c == '\\' && inStr:
			esc = true
		case c == '"':
			inStr = !inStr
		case inStr:
		case c == open:
			depth++
		case c == close:
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

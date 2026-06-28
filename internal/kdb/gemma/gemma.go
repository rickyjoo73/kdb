// Package gemma — ai1/ai2 게이트웨이(OpenAI 호환)의 Gemma 모델 호출. codex 대체/보완용
// LLM. codex(ChatGPT OAuth, http_error 빈발·분 단위 지연)와 달리 일반 HTTP API 라
// 빠르고 동시성도 높일 수 있다. 신뢰는 호출측 가드(문자셋·source 위계·외부검색
// 우선)가 보장하므로, gemma 도 codex 와 동일한 저신뢰(LLM 합성) 등급으로 다룬다.
//
// ai1(gemma4-moe:latest, MoE — 고품질): reasoning 모드 기본 → max_tokens 2048+.
// ai2(gemma-4-26b-a4b, 4bit — 고속): enable_thinking=false → 짧은 직접 출력.
// 두 모델 모두 content 가 빈 경우 reasoning 필드를 fallback 으로 참조한다.
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
	"sync/atomic"
	"time"
)

// gemmaEndpoint — (base url, model) 쌍. ai1(MoE 고품질)·ai2(4bit 고속)처럼 엔드포인트마다
// 모델이 다르므로 쌍으로 관리한다.
type gemmaEndpoint struct{ base, model string }

var gemmaRR uint64 // 라운드로빈 카운터

// gemmaEndpoints — KDB_GEMMA_BASE_URL(CSV) × KDB_GEMMA_MODEL(CSV, 병렬) → 엔드포인트 목록.
// 단일 값이면 1개(하위호환). 모델이 URL 보다 적으면 마지막 모델 재사용.
func gemmaEndpoints() []gemmaEndpoint {
	urls := splitCSVne(os.Getenv("KDB_GEMMA_BASE_URL"))
	models := splitCSVne(os.Getenv("KDB_GEMMA_MODEL"))
	eps := make([]gemmaEndpoint, 0, len(urls))
	for i, u := range urls {
		m := "gemma-4-26b-a4b"
		if i < len(models) {
			m = models[i]
		} else if len(models) > 0 {
			m = models[len(models)-1]
		}
		eps = append(eps, gemmaEndpoint{base: strings.TrimRight(u, "/"), model: m})
	}
	return eps
}

// pickGemmaEndpoint — 라운드로빈으로 엔드포인트 1개 선택(ai1↔ai2 부하분산).
func pickGemmaEndpoint() (gemmaEndpoint, bool) {
	eps := gemmaEndpoints()
	if len(eps) == 0 {
		return gemmaEndpoint{}, false
	}
	i := atomic.AddUint64(&gemmaRR, 1) - 1
	return eps[int(i%uint64(len(eps)))], true
}

// splitCSVne — 콤마 분리 + 공백제거 + 빈값 제외.
func splitCSVne(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// gemma 동시성 — codex 의 OAuth single-flight 제약이 없으므로 높게.
var sem = make(chan struct{}, concurrency())

func concurrency() int {
	if v := os.Getenv("KDB_GEMMA_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			return n
		}
	}
	// MoE reasoning 모드: 요청당 2-7s. 4 동시가 실측 sweet-spot (KDB_GEMMA_CONCURRENCY 조정).
	return 4
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
	MaxTokens   int            `json:"max_tokens,omitempty"`
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
			Content   string `json:"content"`
			Reasoning string `json:"reasoning"` // MoE reasoning 모드(ai1) fallback
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
	ep, ok := pickGemmaEndpoint() // ai1↔ai2 라운드로빈
	key := os.Getenv("KDB_GEMMA_API_KEY")
	if !ok || key == "" {
		return nil, errors.New("gemma: KDB_GEMMA_BASE_URL/API_KEY 미설정")
	}
	base, model := ep.base, ep.model

	// 동시성 슬롯(부모 ctx 존중).
	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	// 시스템 프롬프트 — 신뢰 기반(환각 금지)을 강하게 못박는다. role 별 상세 규칙은
	// user 프롬프트(BuildFillLocalePrompt 등)에 있고, 여기선 전역 안전 규칙을 건다.
	sys := strings.Join([]string{
		"너는 한국 K-엔터테인먼트 고유명사의 현지 통용 표기/번역 DB 보조자다. 절대 규칙:",
		"1) 확실한 사실만 출력한다. 조금이라도 불확실하면 그 값은 반드시 빈 문자열(\"\"). 추측·창작·음역 지어내기 절대 금지 — 모르면 빈칸이 정답이다(틀린 값은 신뢰를 무너뜨린다).",
		"2) 입력에 검색결과/컨텍스트(Wikidata·sitelink 등)가 주어지면 그것을 최우선·유일 근거로 삼는다. 컨텍스트에 없고 확실히 모르면 빈칸(검색해도 없으면 비워두는 것이 옳다).",
		"3) 인물/그룹 = 현지 음역, 드라마/영화/예능 = 공식 현지 제목(번역). 비영어 칸에 영어를 복사하지 않는다.",
		"4) 문자셋 엄수: ja=가나/한자, zh·zh-hant=한자, en·vi·es·id·pt-br=라틴. 위반 값은 빈칸으로.",
		"5) 출력은 JSON 값 하나만. 코드펜스(```)·설명·사고과정 절대 금지.",
	}, "\n")
	if len(schema) > 0 {
		sys += "\n\nJSON 은 다음 schema 를 따른다:\n" + string(schema)
	}
	// max_tokens: reasoning 모델(ai1 gemma4-moe)은 thinking 먼저 출력 후 JSON 답 —
	// 충분한 토큰이 없으면 content 가 비어 있음. 2048 로 여유 확보.
	// ai2(enable_thinking=false 비reasoning) 는 실제로 훨씬 적은 토큰 씀 — 낭비 없음.
	maxTok := 2048
	if v := os.Getenv("KDB_GEMMA_MAX_TOKENS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxTok = n
		}
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
		MaxTokens:   maxTok,
		Stream:      false,
		// ai2(llama.cpp): enable_thinking=false → 추론 OFF, 0.77s 직접 출력.
		// ai1(Ollama): 이 파라미터 무시 → reasoning 기본 활성. content 로 JSON 출력.
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
	msg := cr.Choices[0].Message
	js := extractJSON(msg.Content)
	// reasoning 모델(ai1): content 가 비어 있으면 reasoning 필드에서 마지막 JSON 추출.
	if js == "" && strings.TrimSpace(msg.Reasoning) != "" {
		js = extractLastJSON(msg.Reasoning)
	}
	if js == "" {
		return nil, errors.New("gemma: 응답에서 JSON 추출 실패")
	}
	if !json.Valid([]byte(js)) {
		return nil, fmt.Errorf("gemma: invalid JSON output")
	}
	return json.RawMessage(js), nil
}

// extractLastJSON — reasoning 필드 fallback: 마지막 { ... } 블록을 추출한다.
// MoE 모델이 reasoning 안에 최종 JSON 을 출력할 때 마지막 것이 정답.
func extractLastJSON(s string) string {
	last := ""
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '}' {
			// 이 } 와 짝이 맞는 { 를 역으로 탐색
			depth := 0
			for j := i; j >= 0; j-- {
				if s[j] == '}' {
					depth++
				} else if s[j] == '{' {
					depth--
					if depth == 0 {
						candidate := strings.TrimSpace(s[j : i+1])
						if json.Valid([]byte(candidate)) {
							last = candidate
						}
						break
					}
				}
			}
			if last != "" {
				return last
			}
		}
	}
	return last
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

// Package kdb — LLM extractor (Codex 5.5 medium via bridge).
//
// CLAUDE.md §1.3.6 정공법: KDB platform 의 RSS 추출 LLM 은
// Codex 5.5 medium 전용. Gemma X, Opus X. Opus bridge 와 동일 패턴
// (scripts/codex_extract_bridge.py — host CLI subprocess).
//
// 정공법:
//   - 직접 OpenAI API 호출 코드 0 (CLAUDE.md §1.3.6 박제).
//   - OPENAI_API_KEY 는 NEVER mediafine .env.
//   - 모든 호출은 HTTP POST http://host.docker.internal:9002/extract.
//   - cheap-gate 통과 item 만 (외부 호출 cost 관리).
//   - bridge down = 그 item silent skip, 다른 item 영향 X.
package kdb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ExtractedSpelling — Codex 응답의 한 entry. ko_hint 와 locale 별 표기.
type ExtractedSpelling struct {
	KoHint     string  `json:"ko_hint"`
	Locale     string  `json:"locale"`
	Spelling   string  `json:"spelling"`
	Confidence float64 `json:"confidence"`
}

// LLMExtractor — RSS item 에서 K-content 표기를 추출하는 인터페이스.
// 구현체: CodexExtractor (운영자 default), NoopExtractor (테스트).
type LLMExtractor interface {
	Extract(ctx context.Context, req ExtractInput) ([]ExtractedSpelling, error)
}

// ExtractInput — extractor 입력.
type ExtractInput struct {
	Locale      string
	Title       string
	Description string
	Hints       []EntityHint // cheap-gate 매칭 결과 — Codex 가 우선 검토
}

// CodexExtractor — host bridge (scripts/codex_extract_bridge.py) HTTP 호출.
type CodexExtractor struct {
	BridgeURL  string // 기본 http://host.docker.internal:9002/extract
	HTTPClient *http.Client
	Model      string // 기본 gpt-5.5-codex
	Effort     string // 기본 medium
}

// NewCodexExtractor — env 또는 기본값.
//   - KDB_CODEX_BRIDGE_URL (default: http://codex-bridge:9002/kdb_extract)
//     운영자 bridge 컨테이너 (mediafine-codex-bridge-1) 의 /kdb_extract endpoint.
//     mediafine-app-1 과 같은 docker network — 내부 hostname 'codex-bridge'.
//   - CODEX_MODEL          (default: gpt-5.5)
//   - CODEX_REASONING_EFFORT (default: medium)
func NewCodexExtractor() *CodexExtractor {
	url := os.Getenv("KDB_CODEX_BRIDGE_URL")
	if url == "" {
		url = "http://codex-bridge:9002/kdb_extract"
	}
	model := os.Getenv("CODEX_MODEL")
	if model == "" {
		model = "gpt-5.5"
	}
	effort := os.Getenv("CODEX_REASONING_EFFORT")
	if effort == "" {
		effort = "medium"
	}
	return &CodexExtractor{
		BridgeURL: url,
		Model:     model,
		Effort:    effort,
		HTTPClient: &http.Client{
			Timeout: 60 * time.Second, // Codex 추론은 reasoning_effort 영향 — medium 은 5~30s 예상
		},
	}
}

// Extract — bridge 에 POST. 응답 JSON 을 []ExtractedSpelling 으로.
// Agent (SRE) 권고: circuit breaker 체크 (10회 연속 fail → 5분 차단).
func (c *CodexExtractor) Extract(ctx context.Context, in ExtractInput) ([]ExtractedSpelling, error) {
	if c == nil || c.BridgeURL == "" {
		return nil, fmt.Errorf("codex extractor not configured")
	}
	if BreakerIsOpen() {
		return nil, fmt.Errorf("circuit breaker open — codex bridge too many failures")
	}
	hints := make([]map[string]string, 0, len(in.Hints))
	for _, h := range in.Hints {
		hints = append(hints, map[string]string{
			"entity_id":    h.EntityID.String(),
			"canonical_ko": h.CanonicalKo,
			"matched":      h.Matched,
		})
	}

	payload := map[string]interface{}{
		"model":            c.Model,
		"reasoning_effort": c.Effort,
		"locale":           in.Locale,
		"title":            in.Title,
		"description":      in.Description,
		"hints":            hints,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.BridgeURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build req: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		BreakerRecordResult(false)
		return nil, fmt.Errorf("bridge http: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		BreakerRecordResult(false)
		return nil, fmt.Errorf("bridge status %d", resp.StatusCode)
	}

	var out struct {
		Spellings []ExtractedSpelling `json:"spellings"`
		Error     string              `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		BreakerRecordResult(false)
		return nil, fmt.Errorf("decode: %w", err)
	}
	if out.Error != "" {
		BreakerRecordResult(false)
		return nil, fmt.Errorf("bridge error: %s", out.Error)
	}
	BreakerRecordResult(true)

	// validate + cleanup
	clean := make([]ExtractedSpelling, 0, len(out.Spellings))
	for _, s := range out.Spellings {
		s.Spelling = strings.TrimSpace(s.Spelling)
		s.KoHint = strings.TrimSpace(s.KoHint)
		s.Locale = strings.TrimSpace(s.Locale)
		if s.Spelling == "" || s.KoHint == "" || s.Locale == "" {
			continue
		}
		if s.Confidence < 0 || s.Confidence > 1 {
			continue
		}
		clean = append(clean, s)
	}
	return clean, nil
}

// NoopExtractor — bridge 없는 환경 (테스트, dev) 에서 사용. 항상 빈 응답.
type NoopExtractor struct{}

func (NoopExtractor) Extract(ctx context.Context, in ExtractInput) ([]ExtractedSpelling, error) {
	return nil, nil
}

// ─── Codex audit (이전 codex_audit.go 흡수) ────────────────────────

// CodexAuditor — kwave_kdb_codex_runs INSERT helper.
type CodexAuditor struct {
	Pool *pgxpool.Pool
}

// NewCodexAuditor — 기본 생성자.
func NewCodexAuditor(pool *pgxpool.Pool) *CodexAuditor {
	return &CodexAuditor{Pool: pool}
}

// AuditCall — 한 Codex 호출 결과를 audit row 로 저장.
//   - status: 'ok' / 'http_error' / 'parse_error' / 'no_spelling' / 'bridge_offline'
//   - rawSpellings: extractor.Extract 반환값 (nil 가능)
//   - errText: status != 'ok' 시 채움
//
// Agent (Architect) 권고 2026-05-25: status='ok' + spelling_count>0 호출은
// **미저장** (운영자는 에러만 본다 — 21k/주 row 저장 효과 0). 에러/no_spelling
// 만 INSERT — admin 페이지 가독성 + storage 90% 절감.
func (a *CodexAuditor) AuditCall(
	ctx context.Context,
	cycleID int64,
	sourceDomain, locale string,
	title, desc string,
	hintCount int,
	status string,
	spellings []ExtractedSpelling,
	durationMs int,
	errText string,
) {
	// ok + spelling 1개 이상 = 정상 — 저장 안 함.
	if status == "ok" && len(spellings) > 0 {
		return
	}

	var rawJSON interface{}
	if len(spellings) > 0 {
		if b, err := json.Marshal(spellings); err == nil {
			rawJSON = string(b)
		}
	}
	var cid interface{}
	if cycleID > 0 {
		cid = cycleID
	}
	_, err := a.Pool.Exec(ctx, `
INSERT INTO kwave_kdb_codex_runs
  (cycle_id, source_domain, locale, rss_title, rss_desc, hint_count,
   status, spelling_count, duration_ms, error_text, raw_spellings, ran_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::jsonb, now())`,
		cid, sourceDomain, locale, truncate(title, 500), truncate(desc, 1000),
		hintCount, status, len(spellings), durationMs, nullIfEmpty(errText), rawJSON)
	if err != nil {
		log.Printf("kdb.CodexAuditor: %v", err)
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

// Package claudejudge — claude CLI(Claude Code, --model sonnet) 헤드리스 호출로 K-엔티티
// 최종 판정. ★2단계 판정의 "최종 판사": Gemma(거름망, 싸고 넓게)가 플래그한 의심군만 받아
// 정확히 판정한다. 실측(2026-06-29)으로 Gemma 약점을 양방향 교정 확인:
//   - false-neg: 로버트 패틴슨(헐리우드) → Gemma 통과, Claude k_entity=false (정확)
//   - 과-플래그: 선데이(실존 K가수) → Gemma 의심, Claude k_entity=true (구제)
//
// 실행: 컨테이너 마운트 경로의 self-contained 바이너리를 exec(node 불요), 구독 인증은
// HOME(=/app, .claude.json + .claude/.credentials.json 마운트). 저볼륨(플래그된 것만) +
// 직렬화(구독/리소스 경합 방지) + per-call timeout. 비용↑이므로 거름망 통과분에만 쓴다.
package claudejudge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Verdict — 최종 판정 결과.
type Verdict struct {
	KEntity bool   `json:"k_entity"` // 실제 K-엔터 고유명사인가
	Reason  string `json:"reason"`
}

// Client — claude CLI 판정기.
type Client struct {
	Bin   string
	Home  string
	Model string
}

var callMu sync.Mutex // claude 호출 직렬화(구독 인증·리소스 경합 방지)

// New — env 기반 구성. KDB_CLAUDE_BIN/HOME/MODEL 로 재정의 가능.
func New() *Client {
	return &Client{Bin: resolveBin(), Home: envOr("KDB_CLAUDE_HOME", "/app"), Model: envOr("KDB_CLAUDE_MODEL", "sonnet")}
}

func envOr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

// resolveBin — claude 실행파일 경로. symlink(/app/.local/bin/claude)은 호스트 절대경로를
// 가리켜 컨테이너에서 깨지므로, versions 디렉터리에서 최신 버전 바이너리를 직접 고른다.
func resolveBin() string {
	if v := strings.TrimSpace(os.Getenv("KDB_CLAUDE_BIN")); v != "" {
		return v
	}
	matches, _ := filepath.Glob("/app/.local/share/claude/versions/*")
	var bins []string
	for _, m := range matches {
		if fi, err := os.Stat(m); err == nil && !fi.IsDir() && fi.Mode()&0111 != 0 {
			bins = append(bins, m)
		}
	}
	if len(bins) == 0 {
		return "claude" // PATH 폴백
	}
	sort.Strings(bins) // 버전 문자열 정렬 → 최신
	return bins[len(bins)-1]
}

// Available — 바이너리가 실제 존재하는지(미설치 환경에서 호출측이 폴백하도록).
func (c *Client) Available() bool {
	if c.Bin == "claude" {
		_, err := exec.LookPath("claude")
		return err == nil
	}
	fi, err := os.Stat(c.Bin)
	return err == nil && !fi.IsDir()
}

// JudgeKEntity — ko/type/현지표기를 주고 K-엔터 고유명사 여부 최종 판정. timeout 기본 90s.
func (c *Client) JudgeKEntity(ctx context.Context, ko, etype string, spellings map[string]string) (*Verdict, error) {
	callMu.Lock()
	defer callMu.Unlock()

	var sp strings.Builder
	for _, loc := range []string{"en", "ja", "zh", "vi"} {
		if v := strings.TrimSpace(spellings[loc]); v != "" {
			fmt.Fprintf(&sp, " %s=%q", loc, v)
		}
	}
	prompt := fmt.Sprintf(`너는 한국 엔터테인먼트(K-pop/K-drama/영화/예능/배우/그룹/소속사/작품 등) 전용 엔티티 DB의 최종 게이트키퍼다.
다음 항목이 *실제 K-엔터테인먼트 고유명사*인지 판정하라.
- 실제 K-엔터(한국 또는 한국에서 K-엔터로 활동) → k_entity=true
- 외국 유명인/외국 매체/일반어/문장/오염/비-K 대상 → k_entity=false
- 단 K-pop 외국적 멤버나 한국에서 활동하는 외국인은 true.
항목: ko=%q type=%s%s
오직 JSON 한 줄로만 답하라: {"k_entity": true 또는 false, "reason": "간단한 근거"}`, ko, etype, sp.String())

	cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, c.Bin, "-p", prompt, "--model", c.Model, "--output-format", "text")
	cmd.Env = append(os.Environ(), "HOME="+c.Home)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("claude exec: %w", err)
	}
	return parseVerdict(string(out))
}

// parseVerdict — claude 출력(```json 펜스/잡텍스트 포함 가능)에서 첫 JSON 객체를 추출.
func parseVerdict(s string) (*Verdict, error) {
	i := strings.Index(s, "{")
	j := strings.LastIndex(s, "}")
	if i < 0 || j <= i {
		return nil, fmt.Errorf("claude: no json in output: %.120q", s)
	}
	var v Verdict
	if err := json.Unmarshal([]byte(s[i:j+1]), &v); err != nil {
		return nil, fmt.Errorf("claude: json parse: %w (%.120q)", err, s[i:j+1])
	}
	return &v, nil
}

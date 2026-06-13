package corrections

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/rickyjoo73/kdb/internal/kdb"
)

// judge — 정정 검증용 codex 추상화(테스트 fake 주입). codexcli.Runner 가 만족.
type judge interface {
	Run(ctx context.Context, prompt string, schema []byte) (json.RawMessage, error)
}

// verifySchema — codex 정정 검증 응답(strict).
var verifySchema = []byte(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "additionalProperties": false,
  "required": ["verdict", "correct_value", "confidence", "reason"],
  "properties": {
    "verdict": { "type": "string", "enum": ["suggested", "current", "other", "unknown"],
      "description": "suggested=클라 제안이 맞다. current=현재 KDB 값이 맞다(제안 틀림). other=둘 다 아니고 correct_value 가 맞다. unknown=판단 불가." },
    "correct_value": { "type": "string", "description": "그 locale 에서 실제 통용되는 올바른 표기/제목. verdict=current 면 현재값, suggested 면 제안값, other 면 제3의 값." },
    "confidence": { "type": "number" },
    "reason": { "type": "string", "maxLength": 300 }
  }
}`)

// verifyVerdict — codex 판정.
type verifyVerdict struct {
	Verdict      string  `json:"verdict"`
	CorrectValue string  `json:"correct_value"`
	Confidence   float64 `json:"confidence"`
	Reason       string  `json:"reason"`
}

// buildVerifyPrompt — 정정 검증 프롬프트. codex 에게 "현재값 vs 제안값 중 무엇이 그
// locale 의 올바른 표기/제목인지, 둘 다 아니면 올바른 값"을 묻는다(codex 를 제대로
// 활용해 품질 판단).
func buildVerifyPrompt(ko, etype, locale, current, suggested string, known map[string]string) string {
	var kn []string
	for _, k := range []string{"en", "ja", "zh", "vi", "es", "id", "pt_br", "zh_hant"} {
		if v := strings.TrimSpace(known[k]); v != "" {
			kn = append(kn, fmt.Sprintf("  - %s: %q", k, v))
		}
	}
	knLines := "  (none)"
	if len(kn) > 0 {
		knLines = strings.Join(kn, "\n")
	}
	cur := current
	if strings.TrimSpace(cur) == "" {
		cur = "(empty)"
	}
	isWork := etype == "drama" || etype == "movie" || etype == "show" || etype == "song_album"
	taskLine := "For person/group this is a localized SPELLING (romanization) task."
	if isWork {
		taskLine = "For drama/movie/show/song_album this is the OFFICIAL LOCALIZED TITLE (translation), not romanization."
	}
	lines := []string{
		"You verify a proposed correction to a Korean K-content entity's localized form.",
		"Output JSON only — a schema is enforced.",
		"",
		fmt.Sprintf("Korean canonical: %q  (entity_type: %s)", ko, etype),
		"Known good spellings in other locales (use as cross-reference):",
		knLines,
		fmt.Sprintf("Target locale to fix: %s", locale),
		fmt.Sprintf("Current KDB value: %q", cur),
		fmt.Sprintf("Client-suggested value: %q", suggested),
		"",
		taskLine,
		"Decide which is the form that media in this locale ACTUALLY use:",
		"- verdict=current  → the current KDB value is already correct (reject the suggestion).",
		"- verdict=suggested→ the client's suggestion is correct.",
		"- verdict=other    → neither; put the correct official form in correct_value.",
		"- verdict=unknown  → you cannot determine confidently (do NOT guess).",
		"correct_value MUST be the actually-correct form for that locale and pass its script norms",
		"(ja=kana/kanji, zh/zh-hant=Han, en/vi/es/id/pt-br=Latin). confidence ≥0.8 only when sure.",
	}
	return strings.Join(lines, "\n")
}

// verify — Wikidata 로 판정 안 된 정정을 codex 로 검증한다. 반환: (판정, 호출됨).
func (s *Service) verify(ctx context.Context, eid uuid.UUID, ko, etype, locale, current, suggested string) (verifyVerdict, bool) {
	if s.Judge == nil {
		return verifyVerdict{}, false
	}
	known := s.knownSpellings(ctx, eid)
	prompt := buildVerifyPrompt(ko, etype, locale, current, suggested, known)
	raw, err := s.Judge.Run(ctx, prompt, verifySchema)
	if err != nil {
		return verifyVerdict{}, false
	}
	var v verifyVerdict
	if json.Unmarshal(raw, &v) != nil {
		return verifyVerdict{}, false
	}
	return v, true
}

// verifyAsync — 백그라운드 codex 검증 + 종결(HTTP 핸들러 밖, fresh ctx). 신뢰
// 기반: confidence 임계값 + 문자셋 가드 + can_replace 가드 모두 통과해야 반영.
func (s *Service) verifyAsync(id int64, eid uuid.UUID, ko, etype, loc, col, cur, suggested string) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("kdb.corrections.verify: #%d panic: %v", id, rec)
			_ = s.finalize(context.Background(), id, "pending", "검증 중 오류 — 운영자 심사", "")
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()

	v, ok := s.verify(ctx, eid, ko, etype, loc, cur, suggested)
	if !ok {
		_ = s.finalize(ctx, id, "pending", "codex 검증 실패/불가 — 운영자 심사", "")
		return
	}
	switch {
	case v.Verdict == "current" && v.Confidence >= 0.7:
		_ = s.finalize(ctx, id, "rejected", "검증 결과 현재 값이 정확: "+v.Reason, "")
	case v.Verdict == "suggested" && v.Confidence >= 0.8 && kdb.IsValidSpellingForLocale(loc, suggested):
		s.finalizeApply(ctx, id, eid, col, suggested, "codex 검증: 제안이 정확 — 반영. "+v.Reason)
	case v.Verdict == "other" && v.Confidence >= 0.8 &&
		strings.TrimSpace(v.CorrectValue) != "" && kdb.IsValidSpellingForLocale(loc, v.CorrectValue):
		// KDB 가 제3의 올바른 값을 안다 → 수정안 회신(proposed), 클라 확인 대기.
		_, _ = s.Pool.Exec(ctx, `UPDATE kwave_kdb_corrections
			SET status='proposed', proposed_value=$2,
			    resolution='codex 검증: KDB 수정안(확인 필요): '||$3 WHERE id=$1`,
			id, v.CorrectValue, v.Reason)
	default:
		_ = s.finalize(ctx, id, "pending", "codex 검증 불확실 — 운영자 심사: "+v.Reason, "")
	}
}

// finalize — 검증 종결(반영 없이 상태/이유만). proposed/pending 은 미해결로 둔다.
func (s *Service) finalize(ctx context.Context, id int64, status, resolution, _ string) error {
	resolvedClause := ", resolved_at=now()"
	if status == "pending" || status == "proposed" || status == "verifying" {
		resolvedClause = ""
	}
	_, err := s.Pool.Exec(ctx, `UPDATE kwave_kdb_corrections
		SET status=$2, resolution=$3`+resolvedClause+` WHERE id=$1`, id, status, resolution)
	return err
}

// finalizeApply — 검증된 값을 반영(source=correction-verified) + 종결.
func (s *Service) finalizeApply(ctx context.Context, id int64, eid uuid.UUID, col, value, resolution string) {
	applied, old, err := s.apply(ctx, eid, col, value, "correction-verified")
	if err != nil {
		_ = s.finalize(ctx, id, "pending", "반영 중 오류 — 운영자 심사", "")
		return
	}
	if !applied {
		_ = s.finalize(ctx, id, "pending", "검증됐으나 현재 값이 보호됨 — 운영자 심사", "")
		return
	}
	_, _ = s.Pool.Exec(ctx, `UPDATE kwave_kdb_corrections
		SET status='auto_applied', returned_value=$2, resolution=$3, resolved_at=now() WHERE id=$1`,
		id, old, resolution)
}

// Status — 정정 1건의 현재 상태(클라이언트 폴링용). GET /v1/corrections/{id}.
type Status struct {
	ID         int64  `json:"correction_id"`
	Status     string `json:"status"`
	Locale     string `json:"locale,omitempty"`
	Suggested  string `json:"suggested,omitempty"`
	Proposed   string `json:"proposed,omitempty"` // KDB 수정안(status=proposed 면 이걸 confirm)
	Resolution string `json:"resolution,omitempty"`
}

// Get — correction_id 로 현재 상태 조회.
func (s *Service) Get(ctx context.Context, id int64) (Status, bool) {
	var st Status
	err := s.Pool.QueryRow(ctx, `
SELECT id, status, locale, suggested_value, proposed_value, resolution
  FROM kwave_kdb_corrections WHERE id=$1`, id).Scan(
		&st.ID, &st.Status, &st.Locale, &st.Suggested, &st.Proposed, &st.Resolution)
	if err != nil {
		return Status{}, false
	}
	return st, true
}

// knownSpellings — entity 의 채워진 locale 표기들(검증 교차참조용).
func (s *Service) knownSpellings(ctx context.Context, eid uuid.UUID) map[string]string {
	out := map[string]string{}
	var en, ja, zh, vi, es, id, pt, zhh string
	err := s.Pool.QueryRow(ctx, `
SELECT COALESCE(canonical_en,''),COALESCE(canonical_ja,''),COALESCE(canonical_zh,''),
       COALESCE(canonical_vi,''),COALESCE(canonical_es,''),COALESCE(canonical_id,''),
       COALESCE(canonical_pt_br,''),COALESCE(canonical_zh_hant,'')
  FROM kwave_entities WHERE id=$1`, eid).Scan(&en, &ja, &zh, &vi, &es, &id, &pt, &zhh)
	if err != nil {
		return out
	}
	out["en"], out["ja"], out["zh"], out["vi"] = en, ja, zh, vi
	out["es"], out["id"], out["pt_br"], out["zh_hant"] = es, id, pt, zhh
	return out
}

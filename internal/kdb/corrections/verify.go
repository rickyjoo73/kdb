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
	"github.com/rickyjoo73/kdb/internal/kdb/codexcli"
)

// modelLabel — 원장에 적을 **실제로 판정한 모델 이름**.
//
// ★2026-09-16 에 codex 를 폐기했다. 그런데 이 파일은 계속 "codex 검증" 이라 적고
//
//	있었다 — 원장에 608건, 폐기한 당일에도 20건이 그렇게 들어갔다. RoleProvider 는
//	설정이 무엇이든 gemma 를 돌려주므로 판정한 것은 gemma 다.
//	이름표가 사실과 다르면 다음 사람이 그 이름표를 믿고 엉뚱한 곳을 판다 —
//	오늘 아침 내가 그 착시에 한 번 걸렸다(컨테이너의 codex 를 손으로 불러 401 을
//	받고는 "앱이 codex 를 부른다"고 보고했다).
func modelLabel() string { return codexcli.RoleProvider("CORRECTION", "gemma") }

// judgeP — 답한 공급자까지 돌려주는 판정기. codexcli.Runner 가 만족한다.
// 없으면(시험 fake 등) 종전처럼 Run 만 쓰고 이름표는 라우팅 설정으로 적는다.
type judgeP interface {
	RunP(ctx context.Context, prompt string, schema []byte) (json.RawMessage, string, error)
}

// judge — 정정 검증용 LLM 추상화(테스트 fake 주입). codexcli.Runner 가 만족.
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
		taskLine = "For drama/movie/show/song_album prefer the OFFICIAL LOCALIZED TITLE (translation)."
	}
	// ★음역 허용 (오너 지시 2026-09-17: "인정해").
	//
	//   종전 문구는 작품에 대해 "OFFICIAL LOCALIZED TITLE (translation), **not
	//   romanization**" 이었다. 그래서 공식 제목이 아직 없는 작품은 소비자가 음역을
	//   보내 줘도 전부 기각·보류로 빠졌다 — 대기 25건 중 12건이 정확히 이것이었다
	//   (한집살림 → Hanjip Sallim / ハンジプ・サルリム, 대한민국 1교시 → Daehanminguk Ilgyosi).
	//
	//   기다린다고 공식 제목이 생기지 않는다. 그 사이 빈칸으로 나간다.
	//
	//   ★순서를 고정한다: 공식 제목이 **있으면** 그것이 이긴다. 음역은 **없을 때만**
	//     받는다. 이 순서를 흐리면 음역이 실제 공식 제목을 밀어내는 사고가 난다 —
	//     그건 «빈칸 > 틀린값» 위반이고, 이 저장소가 소스 우선순위표를 만든 이유다.
	fallbackLine := "If NO official/established localized title exists in this locale, a faithful " +
		"transliteration of the Korean is ACCEPTABLE — it is better than leaving the field empty. " +
		"But if an official title DOES exist, the official title always wins over a transliteration: " +
		"never accept a transliteration that would displace it. A transliteration must actually " +
		"reflect the Korean pronunciation; do not accept an invented or mistaken one."
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
		fallbackLine,
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
// ★판정과 **실제로 답한 공급자**를 함께 돌려준다 (2026-09-16 저녁).
//
//	라우팅이 codex 를 가리켜도 상한 소진·인증 실패·장애로 gemma 가 답할 수 있다.
//	그때 라우팅 설정 이름을 원장에 적으면 거짓말이 된다 — 그렇게 «codex 검증》 이
//	608건 쌓였고, 나는 그 이름표를 믿고 엉뚱한 곳을 팠다.
//
//	공급자를 Service 필드에 담아 두면 동시 판정이 서로 덮어쓴다(경합). 값으로 넘긴다.
func (s *Service) verify(ctx context.Context, eid uuid.UUID, ko, etype, locale, current, suggested string) (v verifyVerdict, by string, ok bool) {
	if s.Judge == nil {
		return verifyVerdict{}, "", false
	}
	known := s.knownSpellings(ctx, eid)
	prompt := buildVerifyPrompt(ko, etype, locale, current, suggested, known)
	var raw json.RawMessage
	var err error
	if jp, okp := s.Judge.(judgeP); okp {
		raw, by, err = jp.RunP(ctx, prompt, verifySchema)
	} else {
		raw, err = s.Judge.Run(ctx, prompt, verifySchema)
	}
	if strings.TrimSpace(by) == "" {
		by = modelLabel()
	}
	if err != nil {
		return verifyVerdict{}, by, false
	}
	if json.Unmarshal(raw, &v) != nil {
		return verifyVerdict{}, by, false
	}
	return v, by, true
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

	v, by, ok := s.verify(ctx, eid, ko, etype, loc, cur, suggested)
	if !ok {
		_ = s.finalize(ctx, id, "pending", by+" 검증 실패/불가 — 운영자 심사", "")
		return
	}
	switch {
	// ★**빈칸이 «정확》할 수는 없다** (2026-09-16 실측).
	//
	//   종전엔 현재 값이 비었는지 보지 않고 verdict=="current" 면 기각했다. 그래서
	//   소비자가 일본어 표기를 보내 줬는데 "현재 값이 정확합니다" 로 거절하고 그
	//   자리를 **빈칸으로 남겼다.** 기각 통보를 받은 소비자는 다시 보내지 않는다.
	//
	//   실측: 기각 중 현재 값이 비었던 394건 가운데 **지금도 비어 있는 것이 18건**,
	//   그중 17건이 ja 다 — 도시의 거리·아미새·여우비·봉숭아학당·싱드컵…
	//   6월 24일 것도 아직 빈칸이다.
	//
	//   빈칸일 때 "현재가 맞다"는 판정은 **판정이 아니라 모순**이다. 기각하지 않고
	//   운영자에게 보낸다. 제안을 바로 반영하지도 않는다 — 문턱을 낮추는 것은
	//   다른 일이고, 여기서 지킬 것은 «소비자가 준 답을 버리지 않는 것》이다.
	case v.Verdict == "current" && strings.TrimSpace(cur) == "":
		// ★modelLabel() 이 아니라 by 다. modelLabel() 은 «어디로 보내라고 설정돼
		//   있는가》이고 by 는 «실제로 누가 답했는가》다. 둘이 갈라진 채로 원장에
		//   적어서 610건이 codex 라고 적혀 있었는데 판정한 것은 gemma 였다.
		_ = s.finalize(ctx, id, "pending",
			by+" 검증이 «현재 값이 정확》이라 했으나 현재 값이 빈칸이다 — 운영자 심사: "+v.Reason, "")
	case v.Verdict == "current" && v.Confidence >= 0.7:
		// 이 분기만 판정자 이름이 통째로 빠져 있었다(2026-09-17 실측 — codex 를 켜고
		// 첫 판정을 보니 «검증 결과 현재 값이 정확: …》 으로 누가 판정했는지 없었다).
		_ = s.finalize(ctx, id, "rejected", by+" 검증 결과 현재 값이 정확: "+v.Reason, "")
	case v.Verdict == "suggested" && v.Confidence >= 0.8 && kdb.IsValidSpellingForLocale(loc, suggested):
		s.finalizeApply(ctx, id, eid, col, suggested, by, by+" 검증: 제안이 정확 — 반영. "+v.Reason)
	case v.Verdict == "other" && v.Confidence >= 0.8 &&
		strings.TrimSpace(v.CorrectValue) != "" && kdb.IsValidSpellingForLocale(loc, v.CorrectValue):
		// KDB 가 제3의 올바른 값을 안다 → 수정안 회신(proposed), 클라 확인 대기.
		_, _ = s.Pool.Exec(ctx, `UPDATE kwave_kdb_corrections
			SET status='proposed', proposed_value=$2,
			    resolution=$4||' 검증: KDB 수정안(확인 필요): '||$3 WHERE id=$1`,
			id, v.CorrectValue, v.Reason, by)
	default:
		_ = s.finalize(ctx, id, "pending", by+" 검증 불확실 — 운영자 심사: "+v.Reason, "")
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
//
// ★by(실제로 답한 공급자)를 인자로 받는다. 반영이 막히는 경로에서도 «누가 검증했는지》
//
//	를 원장에 적어야 하기 때문이다. 이 분기 하나가 라벨 없이 남아 있던 것을
//	TestEveryLedgerBranchNamesTheJudge 가 잡았다 — 사람이 일곱 번째 분기를 세지 못한다.
func (s *Service) finalizeApply(ctx context.Context, id int64, eid uuid.UUID, col, value, by, resolution string) {
	applied, old, err := s.apply(ctx, eid, col, value, "correction-verified")
	if err != nil {
		// 판정이 아니라 쓰기 실패다 — 판정자 이름을 붙이면 오히려 오해를 준다.
		_ = s.finalize(ctx, id, "pending", "반영 중 오류 — 운영자 심사", "")
		return
	}
	if !applied {
		_ = s.finalize(ctx, id, "pending", by+" 검증됐으나 현재 값이 보호됨 — 운영자 심사", "")
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

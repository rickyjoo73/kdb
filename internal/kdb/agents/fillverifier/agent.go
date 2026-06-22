// Package fillverifier — re-verifies unverified LLM-synthesized locale values
// (canonical_<loc>_source='codex-fallback') against the authoritative Wikidata
// label for the entity's stored QID, and either UPGRADES the source label (when
// the LLM guess matches the official label) or REPLACES the value with the
// official label (when they differ and a gemma judge confirms the official one
// is the correct locale representation). Source-exhausted / source-absent fields
// are left as-is.
//
// Why this exists: ~39% of all locale values are codex-fallback — the lowest
// trust tier (an LLM guess made when no authoritative source had the value at
// fill time). Many of those entities DO have a verified Wikidata QID whose label
// set has since caught up. This agent reclaims those, shrinking the unverified
// share without re-running the expensive full enrich cascade.
//
// Safety: every UPDATE is guarded by `AND canonical_<loc>_source='codex-fallback'`
// so it can ONLY ever touch codex-fallback values — never operator-locked,
// media-consensus, api, or wikidata values. Replacements snapshot the old value
// to kwave_kdb_dataqa_log (revert-capable). The LLM judge runs on gemma (codex
// usage is reserved for quality-critical roles); on any judge error we keep the
// existing value (conservative). Opt-in: registered only when
// KDB_FILLVERIFY_ENABLED=1, and supervised by Hermes like the other role agents.
package fillverifier

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	kdb "github.com/rickyjoo73/kdb/internal/kdb"
	"github.com/rickyjoo73/kdb/internal/kdb/agents"
	"github.com/rickyjoo73/kdb/internal/kdb/codexcli"
	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
)

// EnabledEnv gates registration (flag + canary; default off).
const EnabledEnv = "KDB_FILLVERIFY_ENABLED"

const attemptField = "fillverify" // enrich_attempts ledger key (7d cooldown)

var errBadInput = errors.New("fillverifier: bad judge input")

// localeCols is the set of canonical locale columns this agent re-verifies, with
// their *_source companions and the Wikidata Labels map key for each.
var localeCols = []struct{ Loc, Col, SrcCol, WDKey string }{
	{"en", "canonical_en", "canonical_en_source", "en"},
	{"ja", "canonical_ja", "canonical_ja_source", "ja"},
	{"vi", "canonical_vi", "canonical_vi_source", "vi"},
	{"id", "canonical_id", "canonical_id_source", "id"},
	{"es", "canonical_es", "canonical_es_source", "es"},
	{"pt_br", "canonical_pt_br", "canonical_pt_br_source", "pt_br"},
	{"zh", "canonical_zh", "canonical_zh_source", "zh"},
	{"zh_hant", "canonical_zh_hant", "canonical_zh_hant_source", "zh_hant"},
}

// Agent re-verifies codex-fallback locale values.
type Agent struct {
	wd    *wikidata.Client
	judge *agents.Base // gemma equivalence judge (differ-case only)
}

// New builds a FillVerifier with the live Wikidata client and a gemma-routed
// judge. codex 사용 최소화 방침에 따라 판정은 gemma(FILLVERIFY 역할, KDB_LLM_FILLVERIFY
// 로 재정의 가능)로 라우팅한다.
func New(r *codexcli.Runner) *Agent {
	if r == nil {
		r = codexcli.NewRunner()
	}
	jr := r.WithProvider(codexcli.RoleProvider("FILLVERIFY", "gemma")).
		WithEffort(codexcli.RoleEffort("FILLVERIFY", "low"))
	return &Agent{
		wd:    wikidata.New(),
		judge: agents.NewBase(jr, judgeLLMRole()),
	}
}

func (a *Agent) Role() agents.Role           { return agents.RoleFillVerifier }
func (a *Agent) Criterion() agents.Criterion { return Criterion{} }

// Select returns active, non-locked entities that (a) have ≥1 codex-fallback
// locale value, (b) have a stored Wikidata QID (so re-verification is a cheap
// Fetch, not a search), and (c) were not fill-verified in the last 7 days (the
// enrich_attempts cooldown that converges the loop). Oldest-touched first.
func (a *Agent) Select(ctx context.Context, pool *pgxpool.Pool, budget int) ([]uuid.UUID, error) {
	if pool == nil {
		return nil, nil
	}
	if budget <= 0 {
		budget = 50
	}
	const q = `
SELECT e.id FROM kwave_entities e
 WHERE e.status='active' AND e.operator_locked = false
   AND EXISTS (SELECT 1 FROM kwave_entity_external_refs x
                WHERE x.entity_id = e.id AND x.provider ILIKE 'wikidata%'
                  AND x.external_id ILIKE 'Q%')
   AND (e.canonical_en_source='codex-fallback' OR e.canonical_ja_source='codex-fallback'
     OR e.canonical_vi_source='codex-fallback' OR e.canonical_id_source='codex-fallback'
     OR e.canonical_es_source='codex-fallback' OR e.canonical_pt_br_source='codex-fallback'
     OR e.canonical_zh_source='codex-fallback' OR e.canonical_zh_hant_source='codex-fallback')
   AND NOT EXISTS (SELECT 1 FROM kwave_kdb_enrich_attempts a
                    WHERE a.entity_id = e.id AND a.field = $2
                      AND a.last_attempt_at > now() - interval '7 days')
 ORDER BY e.updated_at ASC
 LIMIT $1`
	rows, err := pool.Query(ctx, q, budget, attemptField)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return ids, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// row is one entity's re-verification input.
type row struct {
	ko   string
	etype string
	vals map[string]string // loc -> current canonical value
	srcs map[string]string // loc -> current source
}

// Run re-verifies each selected id. Never drops an id: each ends filled
// (upgraded/replaced ≥1 field), noop (nothing to do / no source), or errored.
func (a *Agent) Run(ctx context.Context, pool *pgxpool.Pool, in agents.RunInput) (agents.RunReport, error) {
	rep := agents.RunReport{Role: a.Role(), RunID: in.RunID, StartedAt: time.Now(),
		SelfCheck: agents.SelfCheck{Pass: true}}
	rep.Selected = len(in.IDs)
	for _, id := range in.IDs {
		rep.Results = append(rep.Results, a.verifyOne(ctx, pool, id))
	}
	rep.Summarize()
	return rep, nil
}

func (a *Agent) verifyOne(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) agents.ItemResult {
	res := agents.ItemResult{ID: id, Action: agents.ActionNoop, Source: "wikidata", Reason: "no source label"}
	// Always record the attempt (cooldown), regardless of outcome.
	defer a.recordAttempt(ctx, pool, id)

	r, err := a.loadRow(ctx, pool, id)
	if err != nil {
		res.Action, res.Reason = agents.ActionErrored, "load: "+err.Error()
		return res
	}
	qid, err := a.loadQID(ctx, pool, id)
	if err != nil || qid == "" {
		res.Reason = "no wikidata qid"
		return res
	}
	ent, err := a.wd.Fetch(ctx, qid)
	if err != nil || ent == nil {
		res.Action, res.Reason = agents.ActionErrored, "wikidata fetch failed"
		return res
	}

	var upgraded, replaced int
	for _, lc := range localeCols {
		if r.srcs[lc.Loc] != string(kdb.SourceCodexFallback) {
			continue // only ever touch codex-fallback fields
		}
		wd := strings.TrimSpace(ent.Labels[lc.WDKey])
		if wd == "" {
			continue // no authoritative label for this locale
		}
		cur := strings.TrimSpace(r.vals[lc.Loc])
		if normEq(cur, wd) {
			// Value already present and matches the official label → upgrade the
			// SOURCE only. No new value is written, so the locale charset guard
			// does NOT apply here: Wikidata legitimately labels some entities in
			// Latin even for zh (e.g. group names like "f(x)"/"ARTMS"), and that
			// value is already in the column — we're just re-attributing it from
			// codex-fallback to the source that confirms it.
			if a.upgradeSource(ctx, pool, id, lc.Col, lc.SrcCol) {
				upgraded++
			}
			continue
		}
		// Differs → we would WRITE wd as a NEW value, so enforce the locale charset
		// guard first (never introduce a script-invalid value), then let gemma judge.
		if !kdb.IsValidSpellingForLocale(lc.Loc, wd) {
			continue
		}
		// Conservative: keep on uncertain / judge error.
		v := a.judgeReplace(ctx, judgeInput{Ko: r.ko, Locale: lc.Loc, EntityType: r.etype, Current: cur, Wikidata: wd})
		if v != "use_source" {
			continue
		}
		if a.replaceValue(ctx, pool, id, lc.Col, lc.SrcCol, cur, wd) {
			replaced++
		}
	}

	switch {
	case upgraded+replaced > 0:
		res.Action = agents.ActionFilled
		res.Reason = formatOutcome(upgraded, replaced)
	default:
		res.Reason = "no codex-fallback field confirmed by wikidata"
	}
	return res
}

// upgradeSource flips a matching field's source codex-fallback → wikidata-label
// (value unchanged). Guarded so it only ever affects codex-fallback rows.
func (a *Agent) upgradeSource(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID, col, srcCol string) bool {
	q := "UPDATE kwave_entities SET " + srcCol + "=$2, updated_at=now() " +
		"WHERE id=$1 AND " + srcCol + "='codex-fallback'"
	ct, err := pool.Exec(ctx, q, id, string(kdb.SourceWikidataLabel))
	return err == nil && ct.RowsAffected() > 0
}

// replaceValue overwrites a codex-fallback value with the authoritative Wikidata
// label, snapshotting the old value to dataqa_log (revert-capable). Guarded so
// it only ever affects codex-fallback rows.
func (a *Agent) replaceValue(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID, col, srcCol, old, val string) bool {
	q := "UPDATE kwave_entities SET " + col + "=$2, " + srcCol + "=$3, updated_at=now() " +
		"WHERE id=$1 AND " + srcCol + "='codex-fallback'"
	ct, err := pool.Exec(ctx, q, id, val, string(kdb.SourceWikidataLabel))
	if err != nil || ct.RowsAffected() == 0 {
		return false
	}
	loc := strings.TrimSuffix(strings.TrimPrefix(srcCol, "canonical_"), "_source")
	_, _ = pool.Exec(ctx,
		`INSERT INTO kwave_kdb_dataqa_log (entity_id, locale, old_value, old_source, verdict, reason, model)
		 VALUES ($1,$2,$3,'codex-fallback','fillverify-replace',$4,'gemma')`,
		id, loc, old, "wikidata label adopted over unverified value: "+val)
	return true
}

func (a *Agent) recordAttempt(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) {
	_, _ = pool.Exec(ctx,
		`INSERT INTO kwave_kdb_enrich_attempts (entity_id, field, attempts, last_attempt_at, last_source)
		 VALUES ($1,$2,1,now(),'wikidata')
		 ON CONFLICT (entity_id, field)
		 DO UPDATE SET attempts = kwave_kdb_enrich_attempts.attempts + 1,
		               last_attempt_at = now(), last_source='wikidata'`,
		id, attemptField)
}

func (a *Agent) loadRow(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) (*row, error) {
	r := &row{vals: map[string]string{}, srcs: map[string]string{}}
	var en, ja, vi, idn, es, ptbr, zh, zhh string
	var enS, jaS, viS, idS, esS, ptbrS, zhS, zhhS string
	err := pool.QueryRow(ctx, `
SELECT canonical_ko, entity_type::text,
       COALESCE(canonical_en,''),      COALESCE(canonical_en_source,''),
       COALESCE(canonical_ja,''),      COALESCE(canonical_ja_source,''),
       COALESCE(canonical_vi,''),      COALESCE(canonical_vi_source,''),
       COALESCE(canonical_id,''),      COALESCE(canonical_id_source,''),
       COALESCE(canonical_es,''),      COALESCE(canonical_es_source,''),
       COALESCE(canonical_pt_br,''),   COALESCE(canonical_pt_br_source,''),
       COALESCE(canonical_zh,''),      COALESCE(canonical_zh_source,''),
       COALESCE(canonical_zh_hant,''), COALESCE(canonical_zh_hant_source,'')
  FROM kwave_entities WHERE id=$1`, id).Scan(
		&r.ko, &r.etype,
		&en, &enS, &ja, &jaS, &vi, &viS, &idn, &idS,
		&es, &esS, &ptbr, &ptbrS, &zh, &zhS, &zhh, &zhhS)
	if err != nil {
		return nil, err
	}
	r.vals = map[string]string{"en": en, "ja": ja, "vi": vi, "id": idn, "es": es, "pt_br": ptbr, "zh": zh, "zh_hant": zhh}
	r.srcs = map[string]string{"en": enS, "ja": jaS, "vi": viS, "id": idS, "es": esS, "pt_br": ptbrS, "zh": zhS, "zh_hant": zhhS}
	return r, nil
}

func (a *Agent) loadQID(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) (string, error) {
	var qid string
	err := pool.QueryRow(ctx, `
SELECT external_id FROM kwave_entity_external_refs
 WHERE entity_id=$1 AND provider ILIKE 'wikidata%' AND external_id ILIKE 'Q%'
 ORDER BY confidence DESC NULLS LAST, fetched_at DESC NULLS LAST
 LIMIT 1`, id).Scan(&qid)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(qid), nil
}

// --- gemma judge ----------------------------------------------------------

type judgeInput struct {
	Ko, Locale, EntityType, Current, Wikidata string
}

type judgeResult struct {
	Verdict string `json:"verdict"` // use_source | keep | uncertain
	Reason  string `json:"reason"`
}

// judgeReplace returns the gemma verdict for a differing field; "keep" on any
// failure (conservative — never replace on doubt).
func (a *Agent) judgeReplace(ctx context.Context, in judgeInput) string {
	if a.judge == nil {
		return "keep"
	}
	var res judgeResult
	if err := a.judge.CallJSON(ctx, in, &res); err != nil {
		return "keep"
	}
	v := strings.TrimSpace(res.Verdict)
	if v == "use_source" {
		return "use_source"
	}
	return "keep"
}

var judgeSchema = []byte(`{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "verdict": {"type": "string", "enum": ["use_source", "keep", "uncertain"]},
    "reason": {"type": "string"}
  },
  "required": ["verdict"]
}`)

func judgeLLMRole() agents.LLMRole {
	return agents.LLMRole{
		Role:   agents.RoleFillVerifier,
		Schema: judgeSchema,
		BuildPrompt: func(in any) (string, error) {
			ji, ok := in.(judgeInput)
			if !ok {
				return "", errBadInput
			}
			return buildJudgePrompt(ji), nil
		},
	}
}

func buildJudgePrompt(ji judgeInput) string {
	var b strings.Builder
	b.WriteString("당신은 한국 대중문화(K-콘텐츠) 고유명사의 다국어 표기 검수자입니다.\n")
	b.WriteString("아래 엔티티의 ")
	b.WriteString(ji.Locale)
	b.WriteString(" 표기에 대해, 현재 값(LLM이 검증 없이 생성)과 Wikidata 공식 라벨 중 어느 것이 올바른지 판정하세요.\n\n")
	b.WriteString("한국어 정식명: " + ji.Ko + "\n")
	b.WriteString("종류: " + ji.EntityType + "\n")
	b.WriteString("목표 locale: " + ji.Locale + "\n")
	b.WriteString("현재 값(미검증): " + ji.Current + "\n")
	b.WriteString("Wikidata 공식 라벨: " + ji.Wikidata + "\n\n")
	b.WriteString("규칙:\n")
	b.WriteString("- Wikidata 공식 라벨이 이 locale의 올바른 표기이면 verdict=\"use_source\".\n")
	b.WriteString("- 현재 값이 더 정확하거나 Wikidata 라벨이 동일 인물/작품이 아닌 듯하면 verdict=\"keep\".\n")
	b.WriteString("- 확신이 없으면 verdict=\"uncertain\".\n")
	b.WriteString("- zh=간체, zh_hant=번체 한자여야 하며 로마자 표기는 부적합.\n")
	b.WriteString("JSON 한 개만 출력: {\"verdict\":\"use_source|keep|uncertain\",\"reason\":\"...\"}\n")
	return b.String()
}

// --- helpers --------------------------------------------------------------

// normEq compares two locale strings after the project's standard normalization
// (lowercase + strip spaces/·/・/-/./_/'/"/, ) so cosmetic variants count equal.
func normEq(a, b string) bool { return normForCompare(a) == normForCompare(b) }

func normForCompare(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	repl := strings.NewReplacer(
		" ", "", "\t", "", "·", "", "・", "", "-", "", ".", "",
		"_", "", "'", "", "\"", "", ",", "")
	return repl.Replace(s)
}

func formatOutcome(upgraded, replaced int) string {
	parts := make([]string, 0, 2)
	if upgraded > 0 {
		parts = append(parts, "upgraded:"+strconv.Itoa(upgraded))
	}
	if replaced > 0 {
		parts = append(parts, "replaced:"+strconv.Itoa(replaced))
	}
	return strings.Join(parts, " ")
}

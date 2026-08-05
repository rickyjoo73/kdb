// Package enricher — the Enricher role agent
// (docs/KDB_HERMES_AGENTS_DESIGN.md §B.4, owner request #2).
//
// Goal: every record in BOTH the entity DB (kwave_entities locale canonicals +
// aliases_ko) AND the person DB (kwave_entity_person_details) ends with NO
// fillable empty field — looping across Hermes cycles until no fillable gap
// remains or every remaining gap is provably source-exhausted.
//
// Per target record the agent:
//  1. computes the set of EMPTY fillable fields, skipping ones already marked
//     source-exhausted (the kwave_kdb_enrich_attempts ledger, migration 0062),
//  2. runs the cascade for ONLY the still-empty fields:
//     L2 MusicBrainz → L3 Wikidata claims → L4 gpt-5.5 (locale + person fill),
//  3. persists, NEVER overwriting an existing non-empty value, and
//  4. records per-field attempts; a field tried by every layer and still empty
//     is marked exhausted so the loop terminates and converges to 0 gaps.
//
// Prioritization (per owner): foreign-presence rows (canonical_en<>”) first;
// Korean-only rows are low priority (still filled if selected, never block).
//
// Wired under Hermes (opt-in, KDB_HERMES_ENABLED) refining step:EnrichEmpty.
package enricher

import (
	"context"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb/agents"
	"github.com/rickyjoo73/kdb/internal/kdb/aijudge"
	"github.com/rickyjoo73/kdb/internal/kdb/codexcli"
	"github.com/rickyjoo73/kdb/internal/kdb/musicbrainz"
	"github.com/rickyjoo73/kdb/internal/kdb/tmdb"
	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
)

// maxAttempts — a field tried this many times across cascades without success
// is marked source-exhausted (terminates the loop on genuinely unknowable
// fields). Each Run is at most one cascade pass per field.
const maxAttempts = 2

// totalFillableFields is the count of distinct fillable fields a person entity
// can have (9 locale/alias + 7 person-detail). Used by Select's convergence
// guard: a row whose exhausted-field count reaches this has no actionable gap.
const totalFillableFields = 16

var errBadInput = errBadInputT{}

type errBadInputT struct{}

func (errBadInputT) Error() string { return "enricher: bad prompt input type" }

// localeFields are the fillable canonical_* locale columns (request#2: en/ja/
// vi/id/es/pt_br/zh/zh_hant) plus aliases_ko.
var localeFields = []string{
	"canonical_en", "canonical_ja", "canonical_vi", "canonical_id",
	"canonical_es", "canonical_pt_br", "canonical_zh", "canonical_zh_hant",
	"aliases_ko",
}

// personFields are the fillable kwave_entity_person_details columns.
var personFields = []string{
	"agency", "birth_year", "gender", "groups",
	"notable_works", "secondary_roles", "primary_role",
}

// localeToCode maps a canonical_* column to the locale code used by the fill
// cascade / orchestrator helpers.
var localeToCode = map[string]string{
	"canonical_en": "en", "canonical_ja": "ja", "canonical_vi": "vi",
	"canonical_id": "id", "canonical_es": "es", "canonical_pt_br": "pt_br",
	"canonical_zh": "zh", "canonical_zh_hant": "zh_hant",
}

// tmdbEnricher is the subset of *tmdb.Client the cascade needs (interface so
// tests inject a fake). Enrich returns KDB-locale → [official title], tmdb id.
type tmdbEnricher interface {
	Enrich(ctx context.Context, token, ko, entityType string) (map[string][]string, int, error)
}

// sourceClients abstracts the external lookup sources so tests inject fakes.
type sourceClients struct {
	mb   *musicbrainz.Client
	wd   *wikidata.Client
	tmdb tmdbEnricher // drama/movie/show 공식 현지 제목(작품 번역의 정답 소스)
}

// Agent is the Enricher role agent.
type Agent struct {
	localeBase *agents.Base // L4 locale fill (kdb_fill_locale)
	personBase *agents.Base // L4 person-detail fill (kdb_fill_person)
	src        sourceClients
}

// New builds an Enricher with the default codex transport + live source clients.
func New(r *codexcli.Runner) *Agent {
	if r == nil {
		r = codexcli.NewRunner()
	}
	fr := r.WithEffort(codexcli.RoleEffort("FILL", "medium"))
	// 다국어 locale 채움: Wikidata/TMDb/사이트검색이 먼저 채우고 LLM 은 최후 보루.
	// FILL 기본값 → gemma (MoE 품질로 K-엔터 locale 충분). KDB_LLM_FILL=codex 로
	// 오버라이드 시 고난도 작품 공식명 번역에 codex 사용 가능.
	// FILLPERSON: 인물 단순 사실(소속사·역할·출생연도) → gemma.
	localeRunner := fr.WithProvider(codexcli.RoleProvider("FILL", "gemma"))
	personRunner := fr.WithProvider(codexcli.RoleProvider("FILLPERSON", "gemma"))
	return &Agent{
		localeBase: agents.NewBase(localeRunner, localeLLMRole()),
		personBase: agents.NewBase(personRunner, personLLMRole()),
		src:        sourceClients{mb: musicbrainz.New(), wd: wikidata.New(), tmdb: tmdb.New()},
	}
}

// NewWith builds an Enricher from explicit bases (tests inject fake runners and
// nil source clients to exercise the cascade deterministically).
func NewWith(localeBase, personBase *agents.Base, mb *musicbrainz.Client, wd *wikidata.Client) *Agent {
	return &Agent{localeBase: localeBase, personBase: personBase, src: sourceClients{mb: mb, wd: wd}}
}

func localeLLMRole() agents.LLMRole {
	return agents.LLMRole{
		Role:   agents.RoleEnricher,
		Schema: codexcli.FillLocaleSchema,
		BuildPrompt: func(in any) (string, error) {
			fi, ok := in.(*aijudge.FillInput)
			if !ok {
				return "", errBadInput
			}
			var wd *codexcli.Wikidata
			if fi.Wikidata != nil {
				wd = &codexcli.Wikidata{QID: fi.Wikidata.QID, Label: fi.Wikidata.Label, Description: fi.Wikidata.Description}
			}
			return codexcli.BuildFillLocalePrompt(fi.Ko, fi.EntityType, fi.PrimaryRole, fi.AliasesKo, fi.Known, fi.Missing, wd, fi.Sitelinks), nil
		},
	}
}

func personLLMRole() agents.LLMRole {
	return agents.LLMRole{
		Role:   agents.RoleEnricher,
		Schema: codexcli.FillPersonSchema,
		BuildPrompt: func(in any) (string, error) {
			pi, ok := in.(personFillInput)
			if !ok {
				return "", errBadInput
			}
			return codexcli.BuildFillPersonPrompt(pi.Ko, pi.PrimaryRole, pi.Agency, pi.Groups, pi.Works, pi.Missing, nil), nil
		},
	}
}

func (a *Agent) Role() agents.Role           { return agents.RoleEnricher }
func (a *Agent) Criterion() agents.Criterion { return Criterion{} }

// Select returns up to budget ids to enrich this cycle. Foreign-presence rows
// (canonical_en<>”) that still have a non-exhausted fillable gap come first;
// then Korean-only rows. A row whose every gap is already exhausted within the
// cooldown is excluded so the agent converges instead of re-selecting forever.
func (a *Agent) Select(ctx context.Context, pool *pgxpool.Pool, budget int) ([]uuid.UUID, error) {
	if pool == nil {
		return nil, nil
	}
	if budget <= 0 {
		budget = 20
	}
	// 각 fillable 필드는 "비어있음 AND (그 필드가) source-exhausted 아님" 일 때만
	// gap 으로 센다. exhausted 집합은 entity 별 1회 집계(ex.fields)해 필드명과 대조.
	// 이렇게 해야 정당하게 빈 필드(예: 솔로 배우의 groups)가 maxAttempts 후 exhausted
	// 로 마킹되면 그 entity 가 selection 에서 빠져 → 0 으로 수렴한다. (이전 가드는
	// "exhausted 개수 < 16" 이라, 실제 gap 이 전부 exhausted 여도 16 미만이면 계속
	// 재선택돼 Noop 으로 budget 을 잠식했다 — 진짜 빈 entity 가 굶던 원인.)
	rows, err := pool.Query(ctx, `
SELECT e.id
  FROM kwave_entities e
  LEFT JOIN kwave_entity_person_details d ON d.entity_id = e.id
  LEFT JOIN LATERAL (
    SELECT COALESCE(array_agg(a.field) FILTER (
             WHERE a.last_attempt_at > now() - interval '7 days'
               AND (a.exhausted OR a.last_source = 'ground-strict-skip')), '{}') AS fields
      FROM kwave_kdb_enrich_attempts a WHERE a.entity_id = e.id
  ) ex ON true
 WHERE e.status='active'
   -- operator_locked 포함: enrich 는 empty-only 라 운영자 값 보존, 빈 칸만 채움
   -- (잠긴 유명 인물의 빈 locale 이 영구 빈칸으로 굶던 버그 수정).
	AND e.entity_type NOT IN ('unknown','term')
   AND (
        (COALESCE(e.canonical_en,'')=''      AND NOT 'canonical_en'      = ANY(ex.fields))
     OR (COALESCE(e.canonical_ja,'')=''      AND NOT 'canonical_ja'      = ANY(ex.fields))
     OR (COALESCE(e.canonical_vi,'')=''      AND NOT 'canonical_vi'      = ANY(ex.fields))
     OR (COALESCE(e.canonical_id,'')=''      AND NOT 'canonical_id'      = ANY(ex.fields))
     OR (COALESCE(e.canonical_es,'')=''      AND NOT 'canonical_es'      = ANY(ex.fields))
     OR (COALESCE(e.canonical_pt_br,'')=''   AND NOT 'canonical_pt_br'   = ANY(ex.fields))
     OR (COALESCE(e.canonical_zh,'')=''      AND NOT 'canonical_zh'      = ANY(ex.fields))
     OR (COALESCE(e.canonical_zh_hant,'')='' AND NOT 'canonical_zh_hant' = ANY(ex.fields))
     OR ((e.aliases_ko IS NULL OR array_length(e.aliases_ko,1) IS NULL)
                                             AND NOT 'aliases_ko'        = ANY(ex.fields))
     OR (e.entity_type='person' AND (
            (COALESCE(d.agency,'')=''                  AND NOT 'agency'          = ANY(ex.fields))
         OR (d.birth_year IS NULL                      AND NOT 'birth_year'      = ANY(ex.fields))
         OR (COALESCE(d.gender,'')=''                  AND NOT 'gender'          = ANY(ex.fields))
         OR (array_length(d.groups,1) IS NULL          AND NOT 'groups'          = ANY(ex.fields))
         OR (array_length(d.notable_works,1) IS NULL   AND NOT 'notable_works'   = ANY(ex.fields))
         OR (array_length(d.secondary_roles,1) IS NULL AND NOT 'secondary_roles' = ANY(ex.fields))
         OR ((d.primary_role IS NULL OR d.primary_role='other')
                                                       AND NOT 'primary_role'    = ANY(ex.fields))
        ))
   )
 ORDER BY (COALESCE(e.canonical_en,'')<>'') DESC, e.confidence DESC, e.updated_at ASC
 LIMIT $1`, budget)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}

// Run enriches each selected id; every id ends with exactly one accounted
// Action (filled when a gap was closed, noop when nothing fillable remained,
// skipped when all gaps are source-exhausted, errored on a row-load failure).
func (a *Agent) Run(ctx context.Context, pool *pgxpool.Pool, in agents.RunInput) (agents.RunReport, error) {
	rep := agents.RunReport{
		Role: a.Role(), RunID: in.RunID, StartedAt: time.Now(),
		Selected: len(in.IDs), SelfCheck: agents.SelfCheck{Pass: true},
	}
	// KDB_STEP_FANOUT>1 이면 병렬 enrich — enrichOne 은 entity 별 독립이고 쓰기는
	// empty-only 가드라 동시 실행 안전. 인덱스 고정 기록이라 결과 순서/개수 보존
	// (item-conservation self-check 유지). LLM 상한은 gemma/codex 전역 sem 이 강제.
	workers := fanoutWorkers()
	if workers <= 1 || len(in.IDs) <= 1 {
		for _, id := range in.IDs {
			rep.Results = append(rep.Results, a.enrichOne(ctx, pool, id))
		}
	} else {
		if workers > len(in.IDs) {
			workers = len(in.IDs)
		}
		results := make([]agents.ItemResult, len(in.IDs))
		jobs := make(chan int, len(in.IDs))
		for i := range in.IDs {
			jobs <- i
		}
		close(jobs)
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range jobs {
					if ctx.Err() != nil {
						results[i] = agents.ItemResult{ID: in.IDs[i], Action: agents.ActionErrored, Reason: "ctx canceled"}
						continue
					}
					results[i] = a.enrichOne(ctx, pool, in.IDs[i])
				}
			}()
		}
		wg.Wait()
		rep.Results = append(rep.Results, results...)
	}
	rep.SelfCheck = a.selfCheck(rep.Results)
	rep.Summarize()
	return rep, nil
}

// fanoutWorkers — autopilot.stepFanout 과 같은 env (import cycle 회피용 로컬 파서).
func fanoutWorkers() int {
	if v := os.Getenv("KDB_STEP_FANOUT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 1
}

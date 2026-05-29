# KDB Hermes Multi-Agent System — Design & Implementation Plan

Status: DESIGN ONLY (no code/DB/container changes). Authored 2026-05-29.
Scope: evolve the current `internal/kdb/autopilot` sweeper into an accountable
multi-agent system: each functional responsibility owned by a role-scoped
gpt-5.5 agent (precise role prompt + strict I/O contract + self-check),
supervised by a **Hermes** agent that verifies each agent actually did its job
(incidents, retries, operator report).

Reference pattern (Nous "Hermes" function-calling): an explicit role/system
prompt + tool/output contracts expressed as JSON Schema + strict structured
output validated against that schema, in a loop where tool/verification results
feed back to the model. There is no built-in supervisor/retry layer in that
project — that is application-level, which is exactly the Hermes supervisor we
design in §C. This maps cleanly onto KDB's existing call path:
`aijudge.Classify`/`aijudge.FillLocale` → `codexcli.Runner.Run(ctx, prompt,
schema) (json.RawMessage, error)` (`internal/kdb/codexcli/codexcli.go:65`), with
embedded schemas in `internal/kdb/codexcli/schemas.go` (`ClassifySchema`,
`FillLocaleSchema`) and prompt builders in `internal/kdb/codexcli/prompts.go`.
Two LLM roles exist today (classify, fill_locale); the registry below adds the
rest.

---

## A. Current-state findings + quantified gap/junk numbers

All numbers from live `kdb-db` (`docker exec kdb-db psql -U kdb -d kdb`),
2026-05-29.

### A.0 Population

| metric | count |
|---|---|
| total entities | 1,670 |
| active | 895 |
| candidate | 749 |
| rejected | 26 |
| entity_type='person' (all) | 659 |
| entity_type='person' AND active | 657 |
| `kwave_persons` (legacy) total | 663 |
| `kwave_entity_person_details` total | 663 |

### A.1 Person extraction/migration gap (owner request #1)

| metric | count |
|---|---|
| active persons with **no** `kwave_entity_person_details` row | **0** |
| active persons with **no** `kwave_persons` row | **0** |
| `kwave_persons` rows with no matching person entity | **4** |

**Finding.** The *migration plumbing already works* — `stepSyncPersons`
(`internal/kdb/autopilot/sweep.go:134-167`) plus the inline person-sync in
`stepClassifyUnknown` (`sweep.go:385-397`) and `stepPromoteConsensus`
(`sweep.go:466-478`) keep `kwave_persons` and `kwave_entity_person_details` in
lock-step with `entity_type='person'`. So request #1 is **not a row-existence
gap; it is a content gap** (see A.2): every person has a details row, but the
content is partial — `primary_role` is still the default `'other'` for 36 of 657,
and `groups` is empty for 446. The 4 orphan `kwave_persons` rows
(이홍내/전소영/레이 아미/이재) are names whose entity is non-person or rejected —
candidates for a one-time reconciliation. **Recommendation:** keep
`kwave_entities`+`kwave_entity_person_details` as canonical; the PersonExtractor
agent's job becomes *seeding/upgrading the role + detail fields*, not creating
rows.

### A.2 Comprehensive gap-fill (owner request #2) — the real problem

Locale canonical / alias empties among **active** entities (n=895):

| field | empty (all active) | empty among foreign-presence (has `canonical_en`, n=792) |
|---|---|---|
| canonical_en | 103 | — |
| canonical_ja | (low) | 0 |
| canonical_es | — | 40 |
| canonical_pt_br | — | 45 |
| aliases_ko (null/empty) | high | 177 |

Person-detail empties among **active** persons (all have a details row, n=657):

| field | empty | % |
|---|---|---|
| agency | 267 | 41% |
| birth_year | 151 | 23% |
| gender | 125 | 19% |
| notable_works | 115 | 18% |
| groups | **446** | **68%** |
| primary_role = 'other' or null | 36 | 5% |

**Finding (headline, corrected).** Person details are **partially filled, not
empty** — the enrich cascade already populates agency/birth_year/notable_works
for many. The biggest remaining person gap is **groups (446/657 = 68% empty)**,
which **no code path ever writes** (the only person-detail writer,
`persistPersonClaims`, `internal/kdb/enrich/orchestrator.go:258-285`, writes only
agency/birth_year/notable_works). The other partial gaps trace to:

1. The only person-detail writers are `persistPersonClaims` (orchestrator.go:258,
   Wikidata claims P264/P463/P108/P569/P800) and `persistPersonSignals`
   (`sweep.go:565-588`, from `aijudge.ClassifyResult`), **both gated on
   `hasGlobalPresence`** — Korean-only persons get nothing. Neither writes
   `gender`, `groups`, or `secondary_roles`: `ClassifyResult`
   (`internal/kdb/aijudge/client.go:51-60`) has **no Gender/Groups/
   SecondaryRoles fields**, and `persistPersonClaims` has a `TODO(tmdb/kofic)`
   for filmography (orchestrator.go:237). So those three columns have **no writer
   at all** — groups is the visible 68% hole.
2. The locale cascade (`Orchestrator.Enrich`, invoked from `stepPromoteConsensus`
   sweep.go:481, `stepEnrichEmpty` sweep.go:516, `stepQualityReview`
   sweep.go:676) fills `canonical_*` well (fp_no_ja=0), but es/pt_br still lag
   (40/45) and **`aliases_ko` is empty for 177/792 foreign-presence rows** — no
   step harvests Korean aliases.

So the Enricher's job is: (a) add writers for gender/groups/secondary_roles, (b)
loop the cascade *to exhaustion* including person-detail fields, (c) remove the
Korean-only skip for foreign-presence rows, (d) harvest aliases_ko.

**Prioritization (per owner): foreign-presence first** — 792/895 active rows have
`canonical_en`; the Enricher should process those before Korean-only rows.

### A.3 Homonym precision (owner request #3)

Today: `needs_disambig=true` rows = **0**; same-name active *person* clusters =
**0**; rows with a `disambig` value set = **0**. (The DB column is `disambig`;
there is **no `merged_into` column** — a merge today is modeled by
`status='rejected'` + a `notes` breadcrumb, as `stepRepairBrokenJamo` does at
sweep.go:236-241.)

The wiring that *exists*: `markHomonymsIfConflict` (`sweep.go:593-634`) compares
incoming person signals against same-name actives via `homonym.Conflict`
(`internal/kdb/homonym/homonym.go`) and, on conflict, sets `needs_disambig=true`
on both — it **never auto-merges and never auto-splits** ("자동 merge/split 은
하지 않음 (보수적)", sweep.go:592). `stepRepairBrokenJamo` (`sweep.go:176-244`)
handles *jamo-broken* strings: if the lone-jamo-stripped form
(`hangul.StripLoneJamo`, `internal/kdb/hangul/validate.go`) matches an active
canonical, it folds source_domains+aliases into the good row and rejects the
broken one — this is the only typo→alias merge that exists, and it only covers
broken-jamo, not ordinary misspellings or nicknames.

**Findings / gaps for #3:**
- `aliasmatch.Find` (`internal/kdb/aliasmatch/matcher.go:40`) exists and already
  computes alias (1.0) / abbreviation (0.85) / pg_trgm-typo (≥0.6) matches — but
  it is **not called from the autopilot cycle at all** (only candidate ingestion
  paths). Its near-name/typo scoring is the missing prior for typo→alias merge.
- There is no path that, for an *ordinary* typo or a nickname, decides "this is
  the same person → add as `aliases_ko` to the correct canonical and reject the
  duplicate." Only broken-jamo gets that treatment.
- `markHomonymsIfConflict` is a leak-by-design: it marks `needs_disambig` and
  stops; with 0 rows currently flagged, either nothing has conflicted yet or the
  flag is being cleared elsewhere — either way no agent ever *resolves* the flag.

### A.4 Candidate gatekeeping (owner request #4)

Among 749 candidates:

| junk signal | count |
|---|---|
| contains a space | 230 |
| length > 20 chars | 7 |
| sentence punctuation `[.!?]` | 9 |
| isolated jamo `[ㄱ-ㅎㅏ-ㅣ]` | 0 |
| hangul + latin/digit mix | 44 |
| **any junk signal (union: space ∪ len>20 ∪ punct ∪ jamo)** | **232 (≈31%)** |

**Finding.** ~31% of the 749 candidates trip at least one junk signal — dominated
by space-containing strings (230), which are mostly multi-word phrases/sentences
rather than proper nouns. The good news: the classify-time reject path **works
and is acted on** — `stepReviewCandidates` (`sweep.go:253-299`) plus the classify
steps auto-reject `entity_type='term' && confidence ≤ 0.40` and set
`status='rejected'`, so the LLM "reject" is **not** leaking today. The problem is
that 230 space/phrase candidates and 44 mixed-script candidates sit in the pool
waiting for an LLM call that may never reach them (batch-limited), and there is
**no deterministic hard-reject** for obvious phrase/sentence/mixed-script junk in
`candidates.go` (`Observe` only filters jamo via `hangul.IsCleanKorean`,
candidates.go:55). The improvement: (a) a deterministic pre-filter that
hard-rejects multi-word phrases, len>20, and mixed-script before they consume LLM
budget, and (b) route the gray band through a dedicated Gatekeeper contract
rather than the generic classify prompt.

### A.5 Existing supervision substrate (owner request #6)

- `kwave_kdb_codex_runs` columns (verified): `source_domain, locale, rss_title,
  hint_count, status, spelling_count, duration_ms, error_text, ran_at` (plus
  `id`). It is **extraction-shaped**, not incident-shaped; `bridge_health.go`
  reuses it by stuffing sentinel values (`source_domain='__bridge_health__'`,
  `status='bridge-offline'|'bridge-recovered'`, see `recordBridgeAudit`
  sweep-adjacent at `bridge_health.go:71-80`). There is **no `severity`,
  `detail`, `resolved`, `entity_id`, or `run_id` column** (my earlier assumption
  was wrong) — so a proper Hermes incident model needs a new table.
- `bridge_health.go` already implements the "Hermes" notion in spirit:
  `BridgeHealthCheck` (line 46) does N-consecutive-fail → open incident →
  recover, and there is a circuit breaker (`BreakerIsOpen`/`BreakerRecordResult`,
  lines 128-158). This is the supervision primitive to generalize.
- The autopilot `Run` (`sweep.go:105-125`) calls 8 steps; each step swallows its
  own errors (log-only) and there is **no per-step success-criteria check, no
  run_id, no retry**. `runStep`-style wrapping does not exist — steps are called
  directly.

---

## B. Agent registry

Location: new package tree `internal/kdb/agents/`, one sub-package per role, each
reusing `codexcli.Runner.Run(ctx, prompt, schema)` for the gpt-5.5 call (same
path `aijudge` uses today). A shared contract lives in
`internal/kdb/agents/agent.go`:

```go
package agents

type RunInput struct {
    RunID  string
    IDs    []uuid.UUID // selected this cycle
    Budget int
}

type ItemResult struct {
    ID     uuid.UUID
    Action string          // filled|merged|rejected|kept|split|quarantined|noop
    Before json.RawMessage // snapshot for audit / leak detection
    After  json.RawMessage
    Source string          // musicbrainz|wikidata|gpt-5.5|heuristic
    Conf   float64
    Reason string
}

type RunReport struct {
    Role        string
    RunID       string
    Selected    int
    Acted       int
    Quarantined int
    Results     []ItemResult
    SelfCheck   SelfCheck
}

type SelfCheck struct {
    Pass   bool
    Checks []Check // {Name string; Pass bool; Detail string}
}

type Agent interface {
    Role() string
    Select(ctx context.Context, pool *pgxpool.Pool, budget int) ([]uuid.UUID, error)
    Run(ctx context.Context, pool *pgxpool.Pool, in RunInput) (RunReport, error)
    // Baseline+Verify metric for Hermes success criteria (§C) provided per role.
}
```

Every gpt-5.5 call uses `codexcli.Runner.Run` with a strict embedded schema;
output is JSON-validated before any DB write. On schema-validation failure the
item is quarantined (recorded), never silently dropped. New schemas live beside
the existing ones under `scripts/codex_schemas/` and
`internal/kdb/codexcli/schemas`.

### B.1 Classifier (exists — keep, minor tighten)

- **Responsibility:** assign `entity_type` to one term, or reject (term + low
  conf). This already works (`aijudge.Classify` + `BuildClassifyPrompt`).
- **Input:** `{ ko, spellings{}, source_domains[], notes }` (current
  `ClassifyInput`).
- **Output schema:** current `ClassifySchema` — `{ entity_type, confidence,
  primary_role?, agency?, birth_year?, notable_works?, needs_search?, reason }`.
- **Tighten:** add `gender`, `groups`, `secondary_roles` to `ClassifyResult`
  (`aijudge/client.go:51`) and the prompt so person facts captured at
  classify-time are not thrown away (these three columns have no writer today —
  groups is 68% empty). Keep the reject behaviour.
- **Success criteria (Hermes):** sampled re-classification agreement ≥ threshold;
  every `term`+low-conf verdict resulted in `status='rejected'`.
- **Self-check:** confidence ≥ minConfFor(media) or deferred, exactly as today.

### B.2 CandidateGatekeeper (new — owner #4)

- **Responsibility:** decide keep vs reject for each candidate *before* spending a
  classify call, combining deterministic heuristics + a gpt-5.5 gray-band call.
- **Input:** `{ term, heuristic_flags[], source_domains[] }`.
- **Output schema:** `{ verdict: proper_noun|fragment|sentence|common_noun|
  misclassified|uncertain, keep: bool, confidence, canonical_suggestion?,
  reason }`.
- **Prompt outline:** "Strict gatekeeper for a proper-noun K-content entity DB.
  KEEP only a proper noun naming a real person/group/work/place/org/brand/event.
  REJECT full sentences, clauses, common nouns, generic descriptors,
  particle/verb fragments, isolated jamo, number/measure strings. If a proper
  noun is buried in extra text, return the cleaned form in canonical_suggestion.
  uncertain → human."
- **Deterministic pre-filter (new, in `candidates.go`):** hard-reject the 44
  hangul+latin mixes (unless whitelisted brand pattern), the 230 spaced/multi-word
  + 7 long (>20) fragments, and josa/verb-tail strings — these need no LLM. Only
  the gray band hits gpt-5.5.
- **Success criteria (Hermes):** post-run candidate junk-ratio drops; sample audit
  (re-graded by a second gpt-5.5 pass) agrees ≥ 90%; **no accepted candidate
  still trips a hard junk signal.**

### B.3 PersonExtractor (new — owner #1, now a *content* role)

- **Responsibility:** for each active person whose `primary_role='other'` (36) or
  detail fields empty (groups 446, agency 267, birth_year 151, gender 125,
  notable_works 115), upgrade `primary_role`/`secondary_roles`/`groups` and seed
  agency/gender/birth_year/notable_works from classify-grade knowledge; reconcile
  the 4 orphan `kwave_persons` rows (이홍내/전소영/레이 아미/이재) once.
- **Input:** `{ entity_id, canonical_ko, current{role, agency, …} }`.
- **Output schema:** `{ primary_role: person_role, secondary_roles[], groups[],
  agency?, gender?, birth_year?, notable_works[] }` — only confident fields;
  nulls left for the Enricher.
- **Prompt outline:** "Given a Korean person and current (often empty) details,
  produce structured person details from well-known facts only. Never invent an
  agency or birth year; leave unknown null."
- **Success criteria (Hermes):** count of `primary_role='other'` active persons
  AND count of empty `groups` strictly decrease; no invalid enum written;
  birth_year in 1900–2025.

### B.4 Enricher (new — owner #2, the gap-fill loop)

- **Responsibility:** for a target entity, fill EVERY empty fillable field across
  `kwave_entities` (canonical_en/ja/es/vi/id/pt_br/zh/zh_hant, aliases_ko) AND
  `kwave_entity_person_details` (agency, groups, gender, birth_year,
  notable_works, secondary_roles) via an **ordered cascade that MERGES** rather
  than first-hit: L2 `internal/kdb/musicbrainz` → L3 `internal/kdb/wikidata` →
  L4 gpt-5.5 (`codexcli` fill-locale + a new fill-person-details prompt). Loop
  until no fillable gap remains or sources exhausted.
- **Input:** `{ entity_id, canonical_ko, canonical_en?, entity_type,
  missing_fields[] }`.
- **Output schema (L4):** object keyed by requested `missing_fields`, each
  `{ value, confidence, evidence }`.
- **Refactor of `enrich/orchestrator.go`:** today `Orchestrator.Enrich`
  (orchestrator.go:155) fills only the 8 `canonical_*` locales and (via
  `persistPersonClaims`, orchestrator.go:258) agency/birth_year/notable_works
  from Wikidata claims — gated on `snapHasGlobalPresence` (orchestrator.go:221).
  Extend it to (a) compute `missing_fields` including gender/groups/
  secondary_roles + aliases_ko, (b) add writers for those (no writer exists
  today), (c) keep honoring `source_priority.go`'s `ShouldReplace`
  (orchestrator.go:427) so a higher-priority source is not overwritten, (d) loop
  while the gap set shrinks. Wire it as the body of
  `stepEnrichEmpty`/`stepQualityReview`.
- **Prioritization:** Selector orders foreign-presence (`canonical_en<>''`)
  first.
- **Success criteria (Hermes):** `fillable_gaps_before − fillable_gaps_after > 0`
  for the batch; no field regresses non-empty → empty; no lower-priority source
  overwrote a higher one.
- **Self-check:** every written value carries a source; arrays de-duplicated.

### B.5 Disambiguator (new — owner #3)

- **Responsibility:** for each same-name person cluster AND each near-name
  candidate (from `aliasmatch`), decide per item: **same person** (typo/nickname
  → add to winner `aliases_ko`, reject the duplicate via
  `status='rejected'`+notes, re-point its `kwave_entity_person_details`), **distinct
  person** (set `disambig`, clear `needs_disambig`), or **uncertain →
  quarantine**. Uses `hangul.StripLoneJamo` (validate.go:58) + `aliasmatch.Find`
  score (matcher.go:40) + `homonym.Conflict` (homonym.go:30) as priors, gpt-5.5
  for judgement, agency/works/role as evidence. Note: there is no `merged_into`
  column — a merge is modeled as `status='rejected'` + a `notes` breadcrumb (the
  pattern `stepRepairBrokenJamo` already uses, sweep.go:236-241).
- **Input:** `{ name, members:[{ id, agency, works[], role, alias_score }] }`.
- **Output schema:** `{ assignments:[{ id, decision: merge|distinct|uncertain,
  same_as?, disambig?, reason, confidence }] }`.
- **Prompt outline:** "A member that is a misspelling, jamo error, or nickname of
  another member is the SAME person → decision=merge, same_as=correct canonical
  id (the well-formed, highest-evidence form is the winner). Assign a
  disambiguator only when members are genuinely different real people. Thin
  evidence → uncertain."
- **Write behaviour (fixes the A.3 leak):** merge → winner gets loser's
  canonical+aliases appended to `aliases_ko`, loser rejected; distinct → set
  `disambig`; uncertain → keep `needs_disambig=true` AND record a review item so
  it is *accounted*, not silently parked.
- **Success criteria (Hermes):** every cluster member ends merged /
  distinct-with-disambig / quarantined-with-review — **no member left
  `needs_disambig=true` without a review record** (leak detector); merged aliases
  actually present on the winner.
- **Self-check:** never merge two members with distinct non-empty agencies; never
  pick a malformed (jamo/typo) form as winner.

---

## C. Hermes supervisor

### C.1 Model — per role, per cycle

1. **Plan:** `agent.Select` returns ids; Hermes snapshots a baseline metric for
   the role's success criterion (Enricher: `fillable_gaps` over the set;
   Gatekeeper: junk ratio; Disambiguator: `needs_disambig` count;
   PersonExtractor: `primary_role='other'` count). Keyed by `run_id` (UUID/cycle).
2. **Run:** `agent.Run` with a per-cycle budget → `RunReport` with per-item
   before/after.
3. **Verify:** re-measure metric, evaluate criterion; run a **sample audit** for
   Classifier/Gatekeeper/Disambiguator (re-ask gpt-5.5 on a random N, require
   agreement ≥ threshold).
4. **Decide:** met → record an ok run row; failed → open an incident
   (severity-classified) with the failing check + counts in `detail`.
5. **Retry:** exponential backoff with jitter, capped. Distinguish *transient*
   (bridge/timeout → retry; reuse the existing breaker `BreakerIsOpen`) from
   *logical* (criterion unmet though calls succeeded → at most one retry, then
   incident + quarantine the batch; never loop forever).

### C.2 Leak audit (silent drops) — the core accountability check

Enforce an **item-conservation invariant** per role:
`selected == acted + quarantined + explicitly_skipped(with reason)`.
Any selected id that is neither changed, quarantined, nor skipped-with-reason is
a **leak** → Hermes opens a `warning` incident "leak: N selected, M unaccounted"
listing ids. Computed from `RunReport.Results` before/after, no DB diffing
needed. This directly catches today's patterns: `markHomonymsIfConflict` parking
rows on `needs_disambig` with no resolver (A.3), and any classify path that
neither rejects nor advances an item.

### C.3 Storage — new incident table

`kwave_kdb_codex_runs` is extraction-shaped and lacks severity/detail/run_id, so
add a dedicated table (new migration, e.g. `0060_hermes_runs.sql`):

```sql
CREATE TABLE kwave_kdb_hermes_runs (
  id          bigserial PRIMARY KEY,
  run_id      uuid NOT NULL,
  role        text NOT NULL,          -- PersonExtractor | Enricher | ...
  status      text NOT NULL,          -- ok | incident | retried | leak
  severity    text,                   -- info | warning | critical
  selected    int, acted int, quarantined int, leaked int,
  metric_before numeric, metric_after numeric,
  detail      jsonb,                  -- failing checks, audit %, ids
  resolved    boolean NOT NULL DEFAULT false,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ON kwave_kdb_hermes_runs (run_id);
CREATE INDEX ON kwave_kdb_hermes_runs (role, created_at DESC);
```

Generalize `bridge_health.go`'s open/recover/breaker logic into a
`hermes.Supervise(role, agent)` helper that writes here. Bridge-health rows can
migrate onto this table too (role=`Bridge`).

### C.4 Operator report (admin page)

New handler + template (repo already has `internal/kdbadmin/handlers_kdb.go`,
`templates/kdb_codex_runs.html`, `kdb_observations.html`): a "Hermes" page
grouped by `run_id` showing per role selected/acted/quarantined/leaked, criterion
pass/fail, sample-audit agreement %, open incidents with resolve buttons; a top
banner from `BridgeHealthCheck`.

---

## D. Mapping onto / refactor of existing autopilot steps

| current (file:line) | becomes | change |
|---|---|---|
| `Run` step list `sweep.go:105-125` | Hermes cycle driver | each step wrapped plan→run→verify→retry, tagged run_id |
| `stepRepairBrokenJamo` `sweep.go:176` | Disambiguator prior (jamo) | keep behaviour; feed StripLoneJamo into the merge decision |
| `stepSyncPersons` `sweep.go:134` | PersonExtractor (row-existence part) | keep row sync; add role/detail seeding; reconcile 4 orphans |
| `stepReviewCandidates` `sweep.go:253` | CandidateGatekeeper | add deterministic pre-filter + gray-band contract; keep reject |
| `stepClassifyUnknown` `sweep.go:311` | Classifier | extend result struct/prompt with gender/groups/secondary_roles |
| `stepPromoteConsensus` `sweep.go:404` | Classifier + Enricher | unchanged promote; enrich call becomes Enricher loop |
| `persistPersonSignals` `sweep.go:565` | folds into PersonExtractor/Enricher | currently only agency/birth/works behind hasGlobalPresence |
| `markHomonymsIfConflict` `sweep.go:593` | Disambiguator select stage | stop parking on needs_disambig; route to resolver + review |
| `stepEnrichEmpty` `sweep.go:489` + `enrich/orchestrator.go` | Enricher (locale+detail loop) | write person-detail cols; merge L2→L3→L4; loop to exhaustion |
| `stepQualityReview` `sweep.go:654` | Enricher (low-conf variant) | same cascade; keeps the Wikidata→conf bump |
| `stepResolveAliasConflicts` `sweep.go:704` | Disambiguator (alias dedup) | keep heuristic; add conservation/leak accounting |
| `bridge_health.go` open/recover/breaker | `hermes.Supervise` core | generalize to per-role incidents on the new table |

---

## E. Ordered implementation plan (PR-sized, with dependencies)

1. **PR-1 Agent scaffolding** — `internal/kdb/agents/agent.go` (interfaces,
   RunReport, SelfCheck, schema-validate helper). No behaviour change. []
2. **PR-2 Hermes core + incident table** — migration `0060_hermes_runs.sql`;
   `internal/kdb/hermes/` plan→run→verify→retry + conservation/leak check +
   run_id; generalize `bridge_health.go`. Wrap the 8 existing steps as
   no-op-criteria agents first (identical behaviour, now audited). [PR-1]
3. **PR-3 Classifier result extension** — add gender/groups/secondary_roles to
   `ClassifyResult` + prompt; persist them. Smallest fix that starts populating
   the no-writer detail columns (groups 68% empty). [PR-1,2]
4. **PR-4 PersonExtractor** — upgrade primary_role off `'other'`, seed detail
   fields, reconcile 4 orphan `kwave_persons`. Replaces the sync-only behaviour.
   [PR-1,2,3]
5. **PR-5 Enricher cascade** — refactor `enrich/orchestrator.go` to merge
   L2→L3→L4 with `source_priority`, write person-detail + aliases + remaining
   locales, loop to exhaustion; foreign-presence first. Replaces
   `stepEnrichEmpty`/`stepQualityReview` bodies. [PR-1,2,4]
6. **PR-6 CandidateGatekeeper** — deterministic pre-filter (mixed-script/spaced/
   long/josa) + gray-band gpt-5.5 + sample audit. Replaces
   `stepReviewCandidates`. [PR-1,2]
7. **PR-7 Disambiguator** — act on merge/distinct/uncertain; alias merge + reject
   duplicate; quarantine-with-review; wire `aliasmatch`. Replaces
   `markHomonymsIfConflict`+`stepResolveAliasConflicts`. [PR-1,2]
8. **PR-8 Hermes operator report** — admin page grouped by run_id. [PR-2]
9. **PR-9 E2E harness** (§F) + reconcile/retire dead `kwave_persons` writes. [all]

**Recommended first three:** PR-1, PR-2, PR-3 — install accountability
(scaffolding + supervisor + leak audit), then the smallest high-value fix
(Classifier capturing gender/groups/secondary_roles) that begins draining the
no-writer person-detail backlog (groups 68% empty) before the larger
Enricher/Disambiguator refactors.

---

## F. E2E test plan

1. **Fixtures (test DB only):** junk candidates (spaced, long, mixed-script,
   josa-tail), clean candidates, a person with `primary_role='other'` + empty
   groups, a foreign-presence entity missing es/pt_br/aliases_ko + groups, and
   a same-name person cluster with (a) a genuine distinct person, (b) a jamo typo
   of a canonical, (c) a nickname.
2. **Stubbed gpt-5.5:** fake `codexcli.Runner.Run` returning canned schema-valid
   JSON per role, plus fault modes (malformed JSON, timeout, low confidence) to
   assert quarantine vs retry vs incident.
3. **Per-role assertions:** Classifier reject → `status='rejected'`; Gatekeeper
   rejects all junk fixtures, keeps clean, audit ≥ 90%, zero accepted rows trip a
   hard signal; PersonExtractor upgrades role off `'other'` and gap count drops;
   Enricher `fillable_gaps` strictly decreases, no regression, foreign-presence
   first, provenance recorded, groups gets a writer; Disambiguator: typo+nickname become winner
   `aliases_ko` with losers rejected, distinct person gets `disambig`, thin case
   quarantined-with-review.
4. **Conservation invariant:** for every role, assert
   `selected == acted + quarantined + skipped`; inject a forced silent drop and
   assert Hermes opens a `leak` incident (test the detector itself).
5. **Hermes loop:** inject logical failure (Enricher fills nothing) → one retry,
   then incident with correct severity, batch quarantined, no infinite loop;
   inject transient bridge timeout → breaker/backoff then success.
6. **Report:** admin Hermes page renders one group per run_id with correct
   counts and resolvable incidents.
7. **Regression guard:** re-run the §A junk/gap SQL against the post-cycle fixture
   DB and assert numbers moved the right way (junk ratio ↓, fillable gaps ↓,
   `primary_role='other'` ↓).

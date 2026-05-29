# KDB Workflow Leak Audit

Authored 2026-05-29 (request #6). Scope: trace the full autopilot/agent
workflow end to end and, for **each stage**, determine whether items can be
**silently dropped** (advanced/consumed with no reject / error / quarantine /
skip accounting), then cross-check that against the `agents.ItemConservation`
(leak detector) coverage.

A "silent drop" here means: an item the stage *selected* or *received* leaves
the stage neither (a) state-changed (filled/merged/rejected/split), (b)
quarantined, (c) explicitly skipped-with-reason, nor (d) errored — i.e. it
vanishes from accounting. The conservation invariant
(`agents.ItemConservation`, `internal/kdb/agents/agent.go:192`) is the
mechanical check: every selected id must appear in `RunReport.Results` exactly
once with an *accounted* `Action`
(`accountedActions`, `internal/kdb/agents/agent.go:74`); a `skipped` Action only
counts when it carries a `Reason` (`agent.go:206`).

The Hermes supervisor runs this detector after every audited agent run
(`internal/kdb/hermes/supervisor.go:174`) and, on a shortfall, records a
`status='leak'` row and logs it (`supervisor.go:202-211`).

---

## Pipeline stages (poller → … → persons-sync)

| # | Stage | Entry point | Conservation-covered when Hermes ON? |
|---|---|---|---|
| 0 | RSS poll / ingest | `internal/kdb/tick.go`, `candidates.go:Observe` | **NO** (pre-agent; see §0) |
| 1 | Candidate gatekeeping | `stepReviewCandidates` (sweep.go) **+** `gatekeeper.Agent` | step: weak / agent: **YES** |
| 2 | Classify unknown | `stepClassifyUnknown` (sweep.go) | step-wrapper: **YES** (delta-derived) |
| 3 | Promote consensus | `stepPromoteConsensus` (sweep.go) | step-wrapper: **YES** |
| 4 | Enrich (locale + person) | `stepEnrichEmpty` / `stepQualityReview` **+** `enricher.Agent` | step-wrapper: YES / agent: **YES** |
| 5 | Disambiguate / homonyms | `stepRepairBrokenJamo` / `stepResolveAliasConflicts` **+** `disambiguator.Agent` | step-wrapper: YES / agent: **YES** |
| 6 | Persons sync | `stepSyncPersons` **+** `personextractor.Agent` | step-wrapper: set-wide (trivial) / agent: **YES** |

"Step-wrapper" = the legacy sweep step wrapped as an audited agent by
`stepAgent` (`internal/kdb/autopilot/agents.go:50`), whose `Run`
(`agents.go:76`) snapshots the selected rows before/after and derives a
per-item `Action` from the DB delta (`deriveAction`, `agents.go:151`). Because
every selected id gets exactly one derived result, the wrapped steps are
**conservation-clean by construction** — the wrapper is precisely the mechanism
that closes the legacy steps' silent-drop gaps for audit purposes.

The dedicated **role agents** (gatekeeper/enricher/disambiguator/
personextractor) each emit one accounted `ItemResult` per selected id in their
own `Run` and additionally run a self-check; they are the stronger,
criterion-gated replacements.

---

## §0. RSS poll / candidate ingest — NOT conservation-covered (pre-agent)

`candidates.Observe` (`internal/kdb/candidates.go:21`) is the ingest funnel. It
filters terms through `hangul.IsCleanKorean` before they ever become a
candidate row (`candidates.go:55`, per design §A.4). A term that fails the
clean-Korean check is **dropped at the door with no row and no audit** — by
design (it never enters the entity table), but it is therefore **outside**
Hermes accounting: Hermes only supervises agents that operate on
`kwave_entities`, and a term filtered here never becomes one.

- **Is this a leak?** Not in the conservation sense (nothing was *selected* and
  then lost). It is an *intake filter*, analogous to a spam pre-filter.
- **Risk:** the only observability is the `kwave_kdb_observations` table
  (surfaced at `/admin/kdb/observations`), which records the raw term sightings
  regardless of whether they became candidates. So an operator *can* see what
  was filtered, but there is no per-term reject reason recorded.
- **Recommendation (documented, not applied — larger change):** when `Observe`
  rejects a term, write the reason into the observation row (a `reject_reason`
  column) so intake filtering is auditable end-to-end. This is an ingest-schema
  change, out of scope for this additive task.

## §1. Candidate gatekeeping — covered by the agent; legacy step is weak

**Legacy `stepReviewCandidates`** auto-rejects `entity_type='term' &&
confidence ≤ 0.40` (design §A.4) — that reject path *is* acted on and recorded,
so the LLM "reject" is not leaking today. But candidates that are never reached
by the batch-limited classify call simply *stay* `candidate` indefinitely with
no disposition; under the wrapped step they are recorded as `noop`
(`deriveAction` default branch, `agents.go:163`) — accounted, but a no-op pile-up
is a *throughput* gap, not a leak.

**`gatekeeper.Agent`** (`internal/kdb/agents/gatekeeper/agent.go`) is the proper
fix and is fully conservation-clean: its `Run` (`gatekeeper/agent.go:196`)
guarantees one accounted `ItemResult` per selected id —
- row vanished at run time → `ActionSkipped` with reason (`agent.go:206-210`);
- deterministic pre-gate junk → `ActionRejected` (`agent.go:224-225`);
- clean name → `ActionKept` (`agent.go:227-229`);
- LLM call/schema failure → `ActionQuarantined` (`agent.go:236-237`) — **the
  key no-silent-drop guarantee: a failed model call quarantines, never drops**;
- gray-band uncertain / low-conf → `ActionQuarantined` (`agent.go:241-242`).

Its self-check additionally asserts no accepted candidate still trips a hard
junk signal (`agent.go:325`). **Verdict: covered.**

## §2. Classify unknown — covered (wrapped step)

`stepClassifyUnknown` (wrapped as `RoleStepClassifyUnknown`,
`agents.go:249`) selects `status='active' AND entity_type='unknown'`
(`selectClassifyUnknown`, `agents.go:185`). The wrapper records `filled` when
`entity_type` changes, else `noop` — every selected id accounted. An item the
LLM leaves `unknown` is a `noop`, not a drop, and remains visible in the
`/admin/entities/unclassified` queue. **Verdict: covered.**

## §3. Promote consensus — covered (wrapped step)

`stepPromoteConsensus` (`RoleStepPromoteConsensus`, `agents.go:251`) selects
multi-source candidates (`selectPromoteConsensus`, `agents.go:192`). Promotion
flips status/confidence → `filled`; a candidate that doesn't meet the bar stays
`candidate` → `noop`. Accounted. **Verdict: covered.**

## §4. Enrich — covered (agent has the strongest guarantee)

`enricher.Agent.Run` (`internal/kdb/agents/enricher/agent.go:194` → `enrichOne`,
`cascade.go:203`) emits exactly one accounted Action per id:
- record not found at run time → `ActionErrored` with reason (`cascade.go:206`);
- no fillable gap (all filled or source-exhausted) → `ActionNoop`
  (`cascade.go:212-213`);
- a gap was closed → `ActionFilled` (`cascade.go:231-233`);
- a gap was attempted but no source produced a value → `ActionSkipped` with
  reason "no source produced a value; attempts advanced" (`cascade.go:236-237`).

**Termination/leak-avoidance:** the per-field ledger
(`kwave_kdb_enrich_attempts`, migration 0062) marks a field `exhausted` after
`maxAttempts` (`cascade.go:251-257`), and `Select` excludes fully-exhausted rows
(`agent.go:167-169`), so the loop *converges to zero fillable gaps* instead of
re-selecting unknowable fields forever — i.e. no item is endlessly churned
(which would be a throughput leak). The no-overwrite invariant is asserted in
the self-check (`criterion.go` self-check `no_overwrite`). **Verdict: covered.**

## §5. Disambiguate / homonyms — the historical leak; now covered by the agent

Design §A.3 identified the real silent-drop: `markHomonymsIfConflict`
(`sweep.go:593-634`) sets `needs_disambig=true` on conflicting same-name persons
and **stops** — "자동 merge/split 은 하지 않음 (보수적)" (`sweep.go:592`). No agent
ever *resolves* the flag, so a flagged row was parked indefinitely with no
follow-up accounting: **a leak-by-design** in the legacy flow.

`disambiguator.Agent.Run` (`internal/kdb/agents/disambiguator/agent.go:1173`)
closes it: it re-clusters the selected ids and records one accounted Action per
member —
- merge → `ActionMerged`, distinct → `ActionSplit`, uncertain →
  `ActionQuarantined` (per-cluster, `processCluster`);
- a selected id whose cluster partner vanished at run time →
  `ActionNoop` with reason "no live cluster partner at run time"
  (`agent.go:1199-1201`) — **explicitly the catch-all that prevents the
  legacy silent park**.

It also wires `aliasmatch.Find` into the cycle (previously only called at
ingest, design §A.3) so typo/near-name clusters are actually detected. The
self-check enforces the evidence gate (never merge conflicting agencies; never
pick a malformed winner). **Verdict: covered — this stage is the headline fix.**

> Note: the *legacy* `markHomonymsIfConflict` path still exists in `sweep.go`
> and is reachable from `stepClassifyUnknown`/`stepPromoteConsensus`. When
> Hermes is OFF it behaves exactly as before (parks `needs_disambig`); the rows
> are still visible at `/admin/entities/homonyms`. When Hermes is ON the
> `disambiguator.Agent` drains that flag with full accounting. The legacy park
> is therefore a *deferred-accountability* gap that the operator can see in the
> admin UI, not an invisible loss.

## §6. Persons sync — covered

`stepSyncPersons` (`RoleStepSyncPersons`, `agents.go:245`, `setWide:true`)
operates set-wide, so its wrapper records a single set-level `noop` audit row
(`agents.go:81-92`) — conservation is trivially satisfied (one synthetic id).
The real per-person accounting is in `personextractor.Agent.Run`
(`internal/kdb/agents/personextractor/agent.go:634`): each selected id →
seed (`ActionFilled`) / role-unknown-deferred (`ActionSkipped` "deferred to
Enricher", `agent.go:688-689`) / reconcile promote (`ActionFilled`) / reconcile
uncertain (`ActionQuarantined`) / junk-legacy (`ActionSkipped`) / row-vanished
(`ActionSkipped`). Notably it **never hard-deletes** a legacy person — junk is
flagged for review (`agent.go:717-722`). **Verdict: covered.**

---

## Conservation coverage summary

| Stage | Legacy step accounted? | Wrapped-step covered? | Dedicated agent covered? |
|---|---|---|---|
| 0 ingest/poll | n/a (intake filter) | n/a (pre-entity) | **n/a — gap, see §0** |
| 1 gatekeep | partial (reject only) | weak (noop pile-up) | **YES** |
| 2 classify | log-only errors | **YES** | (gatekeeper supersedes upstream) |
| 3 promote | log-only errors | **YES** | — |
| 4 enrich | log-only errors | **YES** | **YES (ledger-converging)** |
| 5 disambiguate | **NO (needs_disambig park = leak)** | **YES** | **YES (drains the flag)** |
| 6 persons-sync | inline, no per-item audit | YES (set-wide trivial) | **YES** |

**Stages NOT conservation-checked:** only **§0 ingest/poll**, because it runs
before any entity row exists and thus before Hermes can supervise it. Every
post-ingest stage is conservation-checked once `KDB_HERMES_ENABLED=1` (either
via the step-wrapper's delta-derived results or the dedicated agent's explicit
results).

**Real leak found:** the legacy `markHomonymsIfConflict` `needs_disambig` park
(§5) — a genuine silent-drop in the *legacy* flow. It is **fixed structurally**
by the `disambiguator.Agent` no-live-partner `noop` catch-all
(`disambiguator/agent.go:1197-1202`), which is part of the already-built agent,
so no further code change is required for the leak to be accounted once Hermes
is enabled.

## Fixes applied in this pass

None were required in the role-agent or supervisor code: every post-ingest
stage already emits exactly-one-accounted-result-per-item (verified by the new
offline E2E harness, `internal/kdb/hermes/e2e_test.go`, which feeds the real
`agents.ItemConservation` a deliberately leaky `RunReport` and confirms it is
detected). The one schema correction made was to migration
`0062_enrich_attempts.sql` so its columns
(`entity_id, field, attempts, exhausted, last_source, last_attempt_at`,
PK `(entity_id, field)`) exactly match the enricher's ledger SQL
(`internal/kdb/agents/enricher/cascade.go:247-257`,
`agent.go:167-169`) — without it the §4 convergence/exhaustion accounting would
fail at runtime.

## Remaining recommendation (documented, larger change — NOT applied)

- **§0 ingest auditability:** add a `reject_reason` to `kwave_kdb_observations`
  and have `candidates.Observe` (`candidates.go:55`) record why a term was
  filtered, so intake filtering becomes auditable on `/admin/kdb/observations`.
  This is an ingest-schema migration outside this additive, default-off task.

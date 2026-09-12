# KDB workflow repair — 2026-09-12

## Scope

Repair the running KDB before general-news entity expansion. Preserve IDs,
consumer API contracts, quality gates, source priorities, and runtime models.
This release contains no SQL migration, bulk requeue, entity merge, or TDB import.

## Confirmed baseline (around 01:00 KST)

- 130 new research requests in 24h all had `done`: their recorded terminal
  states were active 32, candidate 59, and gate-held/rejected 39. These are
  initial outcomes, not current readiness; some candidates were later promoted.
- Disambiguator recorded 3,455 split results for 73 distinct entities in 24h.
  `applyDistinct` did not update the timestamp used by selection cooldown.
- One show reported 48 alias fills in 24h but retained no aliases. The following
  alias cleanup had `after.AliasResolved=1` while its top-level action was noop.
  Producer and cleanup applied different acceptance rules.
- 2,040 active records had a foreign-language gap; 1,919 had no record update
  in 7 days. Most CJK gaps were in same-input retry suppression, not queued work.

## Changes

1. Stamp successful distinct reviews, return noop for unchanged labels, preserve
   updated_at/notes on noops, and report failed or locked writes honestly.
2. Use full cluster membership with `HAVING bool_or(review due)` for exact-name,
   English-name, and QID clusters, so a fresh member can reopen an old cluster.
   The existing 14-day review cooldown remains; this is not yet a complete
   evidence-fingerprinted, per-pair identity decision store.
3. Share Korean alias ownership policy between Enricher and cleanup. Prevent
   reinstalling aliases that cleanup would remove. Multiple exact-name owners
   remain ambiguous; this policy never merges entity IDs.
4. Count only persisted new aliases, expose storage failures separately from
   source exhaustion, and report actual cleanup changes instead of synthetic noops.
5. Rename workflow metrics to match what they measure. Add current name-based
   inventory and per-language fill-selection/suppression/policy counts using
   the worker's shared retry predicate. Display inventory query errors explicitly.
6. Replace the dashboard's inferred healthy/official/ready labels with explicit
   counts and units. Display errors separately, keep search and review links,
   label legacy persons separately, and expose the same fill inventory table.
7. Add responsive, keyboard-accessible navigation and opt-in workflow refresh.
   Preserve all existing CRUD routes, authentication and CSRF protection.

## Validation

Use a throwaway PostgreSQL database named **kdb_workflow_test**. Integration
tests refuse other database names and create/drop their own random schemas.

```
KDB_SKIP_LIVE=1 KDB_TEST_DATABASE_URL=... go test ./... -count=1
go build ./...
KDB_SKIP_LIVE=1 KDB_TEST_DATABASE_URL=... go test -race \
  ./internal/kdb/agents/disambiguator ./internal/kdb/agents/enricher \
  ./internal/kdb/autopilot ./internal/kdbadmin -count=1
go test ./internal/kdb/wikidata -run '^TestSearchAndFetch_live$' -count=1
```

Tests cover repeated reviews, unchanged decisions, new cluster partners, locked
and failed writes, alias producer/cleanup convergence, stale snapshots, real
cleanup accounting, input-change/90-day retry eligibility, ambiguity, candidate
vs active, eight-field presence vs job completion, and visible inventory failure.

The initially network-isolated full run failed only the external Wikidata test;
the offline suite and the separately network-enabled live test then passed.

## Rollout and limits

Build `Dockerfile.workflow-fix` from the deployed immutable image. Apply only
reviewed files after verifying the production checkout matches the base. Retain
the prior image and runtime settings for rollback. Restart only kdb-app using
both existing compose files; preserve its static network address. Observe the
next worker runs and the authenticated workflow page before widening changes.

Do not reset all attempt ledgers or disable grounding to make counters fall.
The 90-day fallback revisit and input-hash reopening remain unchanged.

Still required in the next phase:

- Durable requested-locales, resolved entity IDs, per-locale outcomes and
  ready-at events; the legacy queue cannot measure actual readiness SLA.
- Evidence-change-driven identity decisions and audited pair/cluster outcomes.
- Source/identity/policy-specific remediation of existing blocked fields.
- Small evidence-backed fill canaries before any large backlog drain.
- UUID-safe legacy-person migration, normalized multilingual Entity schema,
  TDB mapping and politics/economy/sports source adapters.

An eight-language value count is inventory only, not verified official spelling,
article-level identity confirmation, or a guarantee of translation quality.

## UI verification (same release, second image)

Whole offline suite, build and relevant race tests passed again after UI changes.
Browser tests use synthetic Go-rendered fixtures, never a forged production session.
Playwright 1.48/Chromium passed 390px and 1440px layouts, error/empty/populated
states, mobile menu/Escape/focus, search query, current menu, and opt-in refresh.
Dashboard query was also measured READ ONLY against production: about 75ms.
Screenshots were visually reviewed. Detailed scope: `KDB_ADMIN_UI_PLAN.md`.

The original local image `workflow-20260912-1` predates the UI changes; a new
immutable tag is required before rollout. No dependency/model or source-policy
upgrade is included. The full-platform TODO is in `PRESSLOCALE_EXECUTION_PLAN.md`.

## Deployment outcome

- Deployed `workflow-20260912-2` at 02:04 KST, then the query-only follow-up
  `workflow-20260912-3` at 02:12 KST on 2026-09-12.
- Final digest: `sha256:8f93d91de3d6b54af006cafb92fd1c61f64495e8e80dc88ef3fae4c407f4209a`.
- Original image `ci-20260901-3f8af1b` and runtime configuration backup retained.
- Full DB dump restored successfully in a network-isolated PostgreSQL 16
  instance; 39 tables, 19,452 entities, 5,698 legacy persons, 26,513 research
  requests and 13,583 entity refs in the restored snapshot.
- Normal HTTPS login confirmed the new pages, old detail/list routes, and health.
- Slow repeated name scans were replaced with deduplicated name/entity sets.
  Old/new inventory matched in one SQL snapshot; alias duplicates must not
  duplicate an identity, while two IDs sharing an alias remain ambiguous.
- Workflow response improved from ~7.4s to 1.916–1.954s in three postdeploy samples.
- First Enricher cycle completed normally. Full Disambiguator/alias-cleanup
  convergence and 24h repeat metrics still require observation; do not claim
  long-term convergence from deployment health alone.
- No migration, blanket requeue, manual entity merge or TDB import was performed.

See `KDB_OPERATIONS_HANDOFF_20260912.md` for backup location and rollback.

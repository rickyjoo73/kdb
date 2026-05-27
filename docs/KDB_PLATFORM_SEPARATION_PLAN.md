# KDB Platform Separation Plan

Status: planning approved for phased execution  
Date: 2026-05-26  
Owner: Mediafine operations

## 1. Decision

KDB, the proper-noun/entity database platform, will be separated from the
Mediafine all-in-one app as a shared platform for Mediafine and the other
K-content sites on the same host.

The separation must be incremental:

- Build the new KDB platform in separate containers first.
- Keep the existing Mediafine admin KDB menus until the new platform is proven.
- Keep the current Mediafine database in Phase 1 to avoid risky cross-database
  rewrites.
- Move physical DB isolation only after the API and worker boundaries are
  stable.
- Remove existing Mediafine admin KDB menu items only after parity and a
  production soak period.

The recommended path is:

1. Phase 1: separate containers, shared DB, no UI removal.
2. Phase 2: Mediafine and sibling sites consume KDB through API.
3. Phase 3: schema isolation and authenticated multi-tenant API.
4. Phase 4: central KDB admin and removal of old Mediafine KDB menus.

## 2. Current State

### Runtime

Current KDB logic runs inside `mediafine-app-1`, the unified Go app:

- public render server
- admin server
- workers
- image processing
- KDB RSS poller
- KDB Codex extraction sweeper
- KDB bridge health monitor

`codex-bridge` is already a separate container and exposes
`http://codex-bridge:9002/kdb_extract`.

### KDB Code

Current KDB package:

- `internal/kdb/bridge_health.go`
- `internal/kdb/candidates.go`
- `internal/kdb/cheap_gate.go`
- `internal/kdb/extractor.go`
- `internal/kdb/observations.go`
- `internal/kdb/rss_discover.go`
- `internal/kdb/rss_poller.go`
- `internal/kdb/source_priority.go`
- `internal/kdb/spelling.go`
- `internal/kdb/sweeper.go`
- `internal/kdb/tick.go`

Resolver code that writes KDB tables:

- `internal/worker/resolver/`

Admin code using KDB tables:

- `internal/admin/handlers/content_pages.go`
- `internal/admin/templates/pages/entities.html`
- `internal/admin/templates/pages/entity_*.html`
- `internal/admin/templates/pages/kdb_*.html`
- `internal/admin/templates/pages/entity_whitelist.html`

Translation code reads KDB directly:

- `internal/worker/translate/translate.go`

Supervisor starts KDB ticks:

- `internal/worker/supervisor/supervisor.go`

### DB Snapshot

Estimated row counts from `pg_stat_user_tables` on 2026-05-26:

| Table | Estimated rows | Role |
|---|---:|---|
| `kwave_rss_items_raw` | 9,317 | raw RSS buffer |
| `kwave_entity_resolution_attempts` | 4,833 | resolver/cascade audit |
| `kwave_entity_external_refs` | 2,799 | Wikidata and provider refs |
| `kwave_media_observations` | 2,535 | media spelling observations |
| `kwave_entities` | 1,380 | primary entity registry |
| `kwave_person_research_queue` | 544 | legacy person research queue |
| `kwave_entity_person_details` | 537 | person details for entities |
| `kwave_persons` | 537 | legacy person registry |
| `kwave_kdb_codex_runs` | 329 | KDB LLM audit |
| `kwave_entity_research_queue` | 303 | entity research queue |
| `kwave_news_whitelist` | 98 | KDB RSS source whitelist |
| `kwave_kdb_poll_cycles` | 63 | KDB poll audit |
| `kwave_entity_candidates` | 60 | deprecated candidate table |
| `kwave_entity_relations` | 0 | future relations |

### Current Admin Menus To Preserve Until Cutover

The existing Mediafine admin remains the master UI until Phase 4:

- `/admin/entities`
- `/admin/entities/{id}`
- `/admin/entities/sources`
- `/admin/entities/review`
- `/admin/entities/conflicts`
- `/admin/entities/whitelist`
- `/admin/kdb/candidates`
- `/admin/kdb/codex-runs`
- `/admin/kdb/observations`
- `/admin/persons`

These items must not be removed until the central KDB admin has functional
parity and production soak has passed.

## 3. Target Architecture

Final target:

```text
                 host nginx
                     |
       +-------------+------------------+
       |                                |
global.mediafine.co.kr          kdb.aiinplanet.com
       |                                |
mediafine-app-1                 kdb-admin or kdb-api admin
       |                                |
       +---------- HTTP ---------------+
                     |
                  kdb-api

                  kdb-worker
                      |
               codex-bridge:9002

kdb-api and kdb-worker both own data through kdb-db.
```

Data ownership in the final state:

```text
kdb-api    <--> kdb-db
kdb-worker <--> kdb-db
kdb-worker ---> codex-bridge:9002
site apps   ---> kdb-api
```

During Phase 1, `kdb-db` is still the existing `mediafine-db-1`.

```text
kdb-api    <--> mediafine-db-1
kdb-worker <--> mediafine-db-1
kdb-worker ---> mediafine-codex-bridge-1
```

Same-host sibling sites call KDB through the API instead of reading a DB table:

```text
kstory-app      \
hobbyissue-app   \
kenterhub-app     +--> http://kdb-api:9100
doorbellnews-app /
other sites     /
```

## 4. Container Strategy

Use a separate Compose project for KDB instead of adding all KDB services to
the existing `mediafine` Compose stack.

Recommended Phase 1 services:

- `kdb-api`: HTTP API for entity lookup and admin-backed reads.
- `kdb-worker`: RSS poller, sweeper, observations, candidates, resolver work.

Recommended later services:

- `kdb-db`: physical Postgres isolation, only after schema/API cutover.
- `kdb-admin`: optional separate admin UI if not served by `kdb-api`.

Phase 1 network:

- Join `mediafine_default` or another shared Docker network to reach
  `mediafine-db-1` and `mediafine-codex-bridge-1`.
- Join `dockers_backend` only if host nginx must reverse proxy the service.
- Bind host API port to loopback only:

```yaml
ports:
  - "127.0.0.1:9100:9100"
```

No separate Docker engine or separate server is required at this stage.

## 5. Phase Plan

### Phase 1: Separate Containers, Shared DB

Goal: prove KDB can run outside `mediafine-app-1` without removing existing
admin menus or physically moving tables.

Deliverables:

- `cmd/kdb-api/main.go`
- `cmd/kdb-worker/main.go`
- `internal/kdbapi/` or equivalent HTTP handler package
- `docker-compose.kdb.yml`
- shared Docker network configuration
- health endpoint
- initial read endpoints
- worker mode for KDB poller/sweeper
- feature flag in `mediafine-app-1` to disable embedded KDB ticks after the
  worker is verified

Important constraint:

- Do not immediately remove direct `kwave_entities` reads from translation.
  Translation quality gates are publish-affecting; API dependency must be
  introduced after KDB API reliability is proven.

Phase 1A, shadow API:

- Add `kdb-api`.
- Read from existing Mediafine DB.
- Do not disable existing embedded KDB goroutines.
- Validate lookup parity against existing admin data.

Phase 1B, worker cutover:

- Add `kdb-worker`.
- Disable embedded KDB poller/sweeper/bridge-health in `mediafine-app-1` via an
  environment flag: `KDB_EMBEDDED_TICKS=0`.
- Ensure only one KDB worker writes `kwave_rss_items_raw`,
  `kwave_media_observations`, `kwave_kdb_codex_runs`, and candidate state.

Rollback:

- Stop `kdb-api` and `kdb-worker`.
- Re-enable embedded KDB ticks in `mediafine-app-1` if Phase 1B was cut over.
- No DB rollback required if Phase 1 avoids schema changes.

### Phase 2: Internal API Adoption

Goal: move consumers from direct DB reads to KDB API in controlled order.

Order:

1. Non-publish admin reads.
2. Resolver/backfill admin status pages.
3. Sibling sites read-only usage.
4. Mediafine translation named-entity helper, only after API latency and
   failure behavior are verified.

Rules:

- Direct DB reads can remain as fallback during the transition.
- API failures in publish-affecting paths must degrade safely and visibly.
- Bulk lookup endpoint must exist before translation worker adoption.

Rollback:

- Restore direct DB code path by feature flag.
- Keep API read-only until all callers are stable.

### Phase 3: Schema Isolation And Multi-Tenant Auth

Goal: make KDB a platform, not a Mediafine internal package.

Preferred first isolation step:

- Move KDB tables from `public` to `kdb` schema in the same Postgres instance.

Physical DB isolation comes later:

- Create `kdb-db` only after all Mediafine direct joins are removed or replaced
  with API composition.

Add:

- `kdb_api_keys`
- `kdb_workspaces`
- scoped permissions
- request audit
- per-workspace rate limits

Authentication policy:

- API key is required for both internal and external callers.
- Host-internal callers may receive higher rate limits, but they should not be
  anonymous.
- Phase 1A already supports optional `KDB_API_KEYS` for the read API. If this
  env var is empty, the loopback-only API stays open for local validation. Set
  it before exposing KDB beyond `127.0.0.1`.

Recommended scopes:

- `entity:read`
- `entity:write`
- `observation:write`
- `candidate:review`
- `admin:read`
- `admin:write`

### Phase 4: Central Admin And Old Menu Removal

Goal: move KDB operations out of Mediafine admin.

Recommended UI path:

- Keep current Mediafine admin during Phases 1-3.
- Build central admin at `kdb.aiinplanet.com/admin`.
- Add only a `KDB 관리` link in Mediafine admin.
- Remove old Mediafine KDB menus after parity and soak.

Removal candidates:

- `고유명사 DB`
- `다국어 DB 소스`
- `검토 큐`
- `충돌 / 동명이인`
- `KDB 신규 후보`
- `KDB Codex audit`
- `KDB 매체 관측`
- `K-news Whitelist`

Keep as a link:

- `KDB 관리` -> `https://kdb.aiinplanet.com/admin`

Removal criteria:

- central admin covers all existing actions
- central admin has role-based access
- API callers are authenticated
- Mediafine has no publish-affecting direct dependency on removed handlers
- KDB has run without incident for 7-14 days
- rollback path is documented

## 6. API Surface

Phase 1 read endpoints:

```http
GET /v1/health
GET /v1/entities?q=<query>&type=<type>&limit=<n>
GET /v1/entities/{id}
GET /v1/entities/{id}/spellings?locale=<locale|all>
GET /v1/entities/{id}/external-refs
GET /v1/entities/{id}/relations
GET /v1/persons/{id}
POST /v1/entities/match
POST /v1/lookup
POST /v1/lookup/bulk
```

Phase 1 response shape:

```json
{
  "id": "uuid",
  "entity_type": "group",
  "canonical_ko": "방탄소년단",
  "canonical_en": "BTS",
  "canonical_ja": "BTS",
  "canonical_vi": "BTS",
  "canonical_zh": "BTS",
  "canonical_zh_hant": "BTS",
  "canonical_es": "BTS",
  "canonical_id": "BTS",
  "canonical_pt_br": "BTS",
  "aliases": {
    "ko": ["방탄"],
    "en": ["Bangtan Boys"]
  },
  "confidence": 0.99,
  "operator_locked": true,
  "updated_at": "2026-05-26T12:00:00Z"
}
```

Phase 2 write endpoints:

```http
POST /v1/observations
POST /v1/research-queue
PATCH /v1/entities/{id}
POST /v1/entities/{id}/lock
```

Phase 4 admin endpoints:

```http
GET /v1/admin/candidates
POST /v1/admin/candidates/{id}/promote
POST /v1/admin/candidates/{id}/reject
GET /v1/admin/codex-runs
GET /v1/admin/observations
GET /v1/admin/poll-cycles
GET /v1/admin/sources
```

## 7. Site Integration Pattern

Each sibling site should use the API, not a direct DB connection.

Environment variables:

```text
KDB_API_URL=http://kdb-api:9100
KDB_API_KEY=kdb_live_site_specific_key
KDB_WORKSPACE_ID=kstory
KDB_ENTITY_LOOKUP_API=0
KDB_ENTITY_LOOKUP_DB_FALLBACK=1
```

Go callers in this repo can use the shared client:

```go
import "github.com/rickyjoo73/kdb/pkg/kdbclient"

client := kdbclient.NewFromEnv()
res, err := client.BulkLookup(ctx, kdbclient.BulkLookupRequest{
    Queries: []string{"BTS", "아이브"},
    Type:    "group",
    Limit:   5,
})
if err != nil {
    return err
}
_ = res
```

Mediafine translation can test API-backed entity hints with
`KDB_ENTITY_LOOKUP_API=1`. Keep `KDB_ENTITY_LOOKUP_DB_FALLBACK=1` during the
soak period so publish-affecting translation falls back to direct DB lookup if
the API is unavailable.

Caller behavior:

- Use bulk lookup for article translation or curation.
- Cache positive lookup results for a short TTL where possible.
- Report newly observed spellings through `/v1/observations` only if the site is
  granted `observation:write`.
- Do not mutate canonical fields from sibling sites unless explicitly granted.

## 8. Verification

Phase 1 verification:

- `GET /v1/health` returns healthy.
- API entity counts match DB:

```sql
SELECT count(*) FROM kwave_entities;
```

- Search parity:
  - query `BTS`
  - query `박미선`
  - query `장원영`
  - query `오징어 게임`
- `kdb-worker` runs one poll cycle without duplicate writes.
- `kwave_media_observations` increases after a worker cycle.
- `kwave_kdb_codex_runs` shows no current bridge outage.
- Existing Mediafine admin still works.
- Existing translation publish path still works.

Phase 2 verification:

- API bulk lookup latency stays within the configured publish-path budget.
- API failure path is observable and does not silently lower translation quality.
- Direct DB fallback can be toggled.

Phase 4 verification:

- Central admin can perform every action previously available in Mediafine admin.
- Old Mediafine KDB handlers/templates are no longer needed.
- Existing operators can reach central admin.
- Audit log records operator identity and workspace.

## 9. Rollback

Phase 1 rollback:

```bash
docker compose -p kdb-platform -f docker-compose.kdb.yml down
```

If embedded KDB ticks were disabled:

- restore the old env value
- restart `mediafine-app-1`

No schema rollback should be needed in Phase 1.

Phase 2 rollback:

- switch Mediafine callers back to direct DB path
- keep `kdb-api` running for non-critical callers

Phase 3 rollback:

- schema moves require explicit reversible migration or view compatibility
- keep compatibility views if moving `public.kwave_*` to `kdb.*`

Phase 4 rollback:

- keep old Mediafine KDB admin handlers until central admin soak passes
- old menu removal is the last action, not the first

## 10. Initial Implementation Tasks

Safe first PR/task:

1. Add `cmd/kdb-api`.
2. Add `internal/kdbapi` read handlers.
3. Add `GET /v1/health`.
4. Add `GET /v1/entities`.
5. Add `GET /v1/entities/{id}`.
6. Add `POST /v1/lookup/bulk`.
7. Add `docker-compose.kdb.yml`.
8. Add tests for lookup query mapping.
9. Start container on loopback port `127.0.0.1:9100`.
10. Compare API output with existing admin/DB.

Second task:

1. Add `cmd/kdb-worker`.
2. Move KDB tick entrypoint into worker command.
3. Add env flag `KDB_EMBEDDED_TICKS=0` to disable embedded KDB ticks in
   `mediafine-app-1`.
4. Start the profiled worker with
   `docker compose --profile worker --env-file .env -f docker-compose.kdb.yml up -d kdb-worker`.
5. Run one cycle with embedded ticks disabled.
6. Verify no duplicate RSS/raw/codex writes.

Do not remove any Mediafine admin menu until after Phase 4 criteria pass.

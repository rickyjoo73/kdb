# KDB Phase 2 API Adoption

Use this after the standalone `kdb-api` and `kdb-worker` are healthy.

## Current Phase 2 State

- `kdb-api` owns read endpoints for entity lookup, spellings, relations,
  external refs, person details, and text matching.
- `kdb-api` owns write endpoints for observations, research queue requests,
  entity patch, entity lock, and site-search backfill queueing.
- Mediafine translation can use `kdb-api` for entity hints with direct DB
  fallback enabled.

## Mediafine Translation Canary

Required app env:

```text
KDB_API_URL=http://kdb-api:9100
KDB_WORKSPACE_ID=mediafine
KDB_ENTITY_LOOKUP_API=1
KDB_ENTITY_LOOKUP_DB_FALLBACK=1
```

Rollback:

```text
KDB_ENTITY_LOOKUP_API=0
```

Do not disable fallback until translation jobs have run cleanly through a
soak period.

## Pre-Restart Checks

From `mediafine-app-1`, verify Docker DNS and API health:

```bash
docker exec mediafine-app-1 curl -sS --max-time 3 http://kdb-api:9100/v1/health
```

Only restart `mediafine-app-1` from a clean or intentionally staged working
tree. This production stack bind-mounts the source tree into the app, so a
restart will run any uncommitted host changes.

## Verification

After restart:

```bash
docker exec mediafine-app-1 printenv KDB_ENTITY_LOOKUP_API
docker logs mediafine-app-1 --tail=200 | grep -i 'kdb api entity lookup'
```

Expected:

- `KDB_ENTITY_LOOKUP_API=1`
- no repeated fallback errors
- translation output still records named entity preservation normally

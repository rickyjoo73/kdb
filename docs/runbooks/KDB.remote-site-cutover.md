# KDB Remote Site Cutover

Use this when KDB is installed on a separate server and Mediafine should call
that server instead of the same-host `kdb-api` container.

## Preconditions

The remote endpoint must pass from the Mediafine host before changing app env:

```bash
curl -sS --max-time 5 https://kdb.aiinplanet.com/v1/health
```

Expected response:

```json
{"ok":true,"service":"kdb-api"}
```

Do not cut over if TLS validation fails, if the host serves another app, or if
the response is not the KDB API health JSON.

## Mediafine App Env

Set these in `.env` on the Mediafine host:

```text
KDB_API_URL=https://kdb.aiinplanet.com
KDB_API_KEY=<site-specific-api-key>
KDB_WORKSPACE_ID=mediafine
KDB_API_TIMEOUT_SECONDS=10
KDB_ENTITY_LOOKUP_API=1
KDB_ENTITY_LOOKUP_DB_FALLBACK=1
KDB_EMBEDDED_TICKS=0
KDB_ADMIN_URL=https://kdb.aiinplanet.com/admin
```

Keep `KDB_ENTITY_LOOKUP_DB_FALLBACK=1` during soak. Translation is
publish-affecting, so API failure must degrade to the direct DB path until the
remote KDB server has proven stable.

## Restart

Restart only the app service after env is changed:

```bash
docker compose --env-file .env -f docker-compose.prod.yml up -d --no-deps app
```

## Verify

```bash
docker exec mediafine-app-1 printenv KDB_API_URL
docker exec mediafine-app-1 printenv KDB_ENTITY_LOOKUP_API
docker exec mediafine-app-1 sh -lc 'curl -sS --max-time 5 "$KDB_API_URL/v1/health"'
docker logs mediafine-app-1 --tail=200 | grep -i 'kdb api entity lookup'
```

Expected:

- `KDB_API_URL` is the remote HTTPS URL.
- health returns `service="kdb-api"`.
- no repeated fallback errors in app logs.
- `/admin` sidebar `KDB Platform` points to `KDB_ADMIN_URL` when set.

## Local KDB Stack

Keep the same-host KDB stack running as rollback for the initial soak unless it
would write to the same remote-owned database. After remote KDB owns writes and
the Mediafine app is verified against the remote API:

```bash
docker compose --env-file .env -f docker-compose.kdb.yml stop kdb-worker kdb-api
```

## Rollback

Set the Mediafine app back to the same-host KDB API:

```text
KDB_API_URL=http://kdb-api:9100
KDB_ADMIN_URL=
KDB_ENTITY_LOOKUP_DB_FALLBACK=1
```

Start the local KDB stack if it was stopped:

```bash
docker compose --env-file .env -f docker-compose.kdb.yml up -d kdb-api
docker compose --profile worker --env-file .env -f docker-compose.kdb.yml up -d kdb-worker
docker compose --env-file .env -f docker-compose.prod.yml up -d --no-deps app
```

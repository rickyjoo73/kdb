# kdb

KDB is the K-content proper-noun/entity database platform extracted from the
Mediafine monorepo. The production target is an independent service at
`kdb.aiinplanet.com` with its own Postgres database.

Current production phase:

- `kdb-api` exposes entity lookup and KDB write endpoints.
- `kdb-worker` owns RSS polling, Codex extraction sweeping, observations, and
  candidate promotion.
- `kdb-db` owns the KDB Postgres data.
- Mediafine and sibling sites consume KDB through the API only.
- The old Mediafine KDB tables/admin pages are retained only until API parity
  and a production soak period are complete.

## Services

```bash
docker compose --env-file .env -f docker-compose.kdb.yml up -d kdb-db kdb-api
docker compose --profile worker --env-file .env -f docker-compose.kdb.yml up -d kdb-worker
```

Health check:

```bash
curl -sS http://127.0.0.1:9100/v1/health
```

## Required Environment

Create `.env` from `.env.example` and set at least:

```text
KDB_POSTGRES_PASSWORD=<strong-password>
DATABASE_URL=postgres://kdb:<strong-password>@kdb-db:5432/kdb?sslmode=disable
```

Optional:

```text
KDB_API_KEYS=comma,separated,keys
KDB_CODEX_BRIDGE_URL=http://codex-bridge:9002/kdb_extract
KDB_API_PORT=9100
KDB_API_REQUEST_TIMEOUT_SECONDS=10
```

## Tests

```bash
go test ./cmd/kdb-api ./cmd/kdb-worker ./internal/kdb ./internal/kdbapi ./pkg/kdbclient
```

## Docs

- [Platform separation plan](docs/KDB_PLATFORM_SEPARATION_PLAN.md)
- [Independent DB cutover](docs/runbooks/KDB.independent-db-cutover.md)
- [Worker cutover runbook](docs/runbooks/KDB.platform-cutover.md)
- [Phase 2 API adoption runbook](docs/runbooks/KDB.phase2-api-adoption.md)
- [Entity DB platform spec](docs/ENTITY_DB_PLATFORM.md)

# KDB Independent DB Cutover

Use this when moving KDB from the Mediafine Postgres database into the
standalone `kdb-db` service owned by `kdb.aiinplanet.com`.

## Target

- `kdb-db` owns all `kwave_*` KDB tables, functions, and data.
- `kdb-api` and `kdb-worker` connect to `kdb-db`.
- Mediafine and sibling sites call `kdb-api`; they do not write KDB tables.
- Mediafine direct DB fallback remains enabled only during the soak period.

## Pre-Check

```bash
docker compose --env-file .env -f docker-compose.kdb.yml ps
docker compose --env-file .env -f docker-compose.kdb.yml up -d kdb-db
docker compose --env-file .env -f docker-compose.kdb.yml exec -T kdb-db pg_isready -U "$KDB_POSTGRES_USER" -d "$KDB_POSTGRES_DB"
```

Stop KDB writers before taking a consistent dump:

```bash
docker compose --profile worker --env-file .env -f docker-compose.kdb.yml stop kdb-worker
docker exec mediafine-app-1 printenv KDB_EMBEDDED_TICKS
```

Expected `KDB_EMBEDDED_TICKS=0`.

## Export From Mediafine DB

Dump only KDB-owned objects:

```bash
docker exec mediafine-db-1 pg_dump -U mediafine -d mediafine \
  --clean --if-exists \
  --table=kwave_entities \
  --table=kwave_entity_candidates \
  --table=kwave_entity_external_refs \
  --table=kwave_entity_person_details \
  --table=kwave_entity_relations \
  --table=kwave_entity_research_queue \
  --table=kwave_entity_resolution_attempts \
  --table=kwave_kdb_codex_runs \
  --table=kwave_kdb_poll_cycles \
  --table=kwave_media_observations \
  --table=kwave_news_whitelist \
  --table=kwave_person_research_queue \
  --table=kwave_persons \
  --table=kwave_rss_items_raw \
  > /tmp/kdb-mediafine-dump.sql
```

Function definitions are restored from the KDB migrations after the table dump.

## Import Into kdb-db

```bash
docker compose --env-file .env -f docker-compose.kdb.yml exec -T kdb-db \
  psql -U "$KDB_POSTGRES_USER" -d "$KDB_POSTGRES_DB" \
  < /tmp/kdb-mediafine-dump.sql

docker compose --env-file .env -f docker-compose.kdb.yml exec -T kdb-db \
  psql -U "$KDB_POSTGRES_USER" -d "$KDB_POSTGRES_DB" \
  < migrations/0050_kdb_source_priority_function.sql

docker compose --env-file .env -f docker-compose.kdb.yml exec -T kdb-db \
  psql -U "$KDB_POSTGRES_USER" -d "$KDB_POSTGRES_DB" \
  < migrations/0051_kdb_can_replace_function.sql
```

Verify core row counts:

```bash
docker compose --env-file .env -f docker-compose.kdb.yml exec -T kdb-db \
  psql -U "$KDB_POSTGRES_USER" -d "$KDB_POSTGRES_DB" -c "
SELECT 'kwave_entities' AS table_name, count(*) FROM kwave_entities
UNION ALL SELECT 'kwave_media_observations', count(*) FROM kwave_media_observations
UNION ALL SELECT 'kwave_news_whitelist', count(*) FROM kwave_news_whitelist
UNION ALL SELECT 'kwave_rss_items_raw', count(*) FROM kwave_rss_items_raw;"
```

## Start Services

```bash
docker compose --env-file .env -f docker-compose.kdb.yml up -d kdb-api
curl -sS http://127.0.0.1:9100/v1/health

docker compose --profile worker --env-file .env -f docker-compose.kdb.yml up -d kdb-worker
docker compose --env-file .env -f docker-compose.kdb.yml logs --tail=80 kdb-worker
```

## Mediafine Cutover

Set Mediafine to use KDB API:

```text
KDB_API_URL=https://kdb.aiinplanet.com
KDB_ENTITY_LOOKUP_API=1
KDB_ENTITY_LOOKUP_DB_FALLBACK=1
```

After API parity and soak, remove fallback:

```text
KDB_ENTITY_LOOKUP_DB_FALLBACK=0
```

Do not remove Mediafine KDB tables/admin menus until fallback has been disabled
and production translation/publish workflows have completed cleanly.

## Rollback

1. Stop standalone `kdb-worker`.
2. Point Mediafine back to local/same-host KDB API or direct DB fallback.
3. If required, set `KDB_EMBEDDED_TICKS=1` and restart `mediafine-app-1`.

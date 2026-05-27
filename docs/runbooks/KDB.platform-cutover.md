# KDB Platform Cutover

Use this when moving KDB poller/sweeper/bridge-health work from
`mediafine-app-1` to the standalone `kdb-worker`.

## Signal

The KDB API container is healthy, but KDB background work is still embedded in
`mediafine-app-1`. Cutover must avoid running two writers against
`kwave_rss_items_raw`, `kwave_media_observations`, and `kwave_kdb_codex_runs`.

## Pre-check

```bash
curl -sS http://127.0.0.1:9100/v1/health
docker compose --env-file .env -f docker-compose.kdb.yml ps
docker compose --env-file .env -f docker-compose.kdb.yml --profile worker config --services
```

If `KDB_API_KEYS` is set, data endpoints require either:

```bash
Authorization: Bearer <key>
X-KDB-Key: <key>
```

Confirm current embedded KDB state:

```bash
docker exec mediafine-app-1 printenv KDB_EMBEDDED_TICKS || true
```

Empty or `1` means embedded KDB ticks are enabled.

## Cutover

1. Set `KDB_EMBEDDED_TICKS=0` for `mediafine-app-1`.
2. Recreate/restart only `mediafine-app-1`.
3. Start the standalone worker:

```bash
docker compose --profile worker --env-file .env -f docker-compose.kdb.yml up -d kdb-worker
```

4. Verify worker logs:

```bash
docker compose --env-file .env -f docker-compose.kdb.yml logs --tail=80 kdb-worker
```

5. Verify no duplicate work:

```sql
SELECT codex_status, count(*)
FROM kwave_rss_items_raw
WHERE fetched_at > now() - interval '30 minutes'
GROUP BY codex_status;
```

## Rollback

Stop standalone worker:

```bash
docker compose --profile worker --env-file .env -f docker-compose.kdb.yml stop kdb-worker
```

Set `KDB_EMBEDDED_TICKS=1` or remove the env var from `mediafine-app-1`, then
restart only `mediafine-app-1`.

## Page Human

Page if Codex bridge is healthy but `kwave_media_observations` stays flat for
more than 60 minutes after cutover, or if `kwave_rss_items_raw` shows repeated
retry growth with no successful `codex_status='ok'` rows.

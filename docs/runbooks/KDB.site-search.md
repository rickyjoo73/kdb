# KDB Site Search Backfill

Use this when `/admin/entities/sources` has many empty locale spellings and
RSS polling has not found enough evidence.

## What It Does

`kdb-api` exposes:

```bash
POST /v1/entities/{entity_id}/site-search
```

The endpoint runs inside the standalone KDB container. It searches enabled
`kwave_news_whitelist` domains through Google News RSS with `site:<domain>`,
then inserts matching pages into `kwave_rss_items_raw` with a forced cheap hint
for the target entity. `kdb-worker` performs the existing Codex extraction,
observation, and consensus flow.

It does **not** update `canonical_*` directly.

## Request

```bash
curl -sS -X POST http://127.0.0.1:9100/v1/entities/<uuid>/site-search \
  -H 'Content-Type: application/json' \
  -d '{"locale":"vi","dry_run":true,"limit_domains":3}'
```

If `KDB_API_KEYS` is set, also send `X-KDB-Key: <key>`.

Fields:

- `locale`: required, e.g. `en`, `ja`, `vi`, `es`, `id`, `pt-br`, `zh-hant`
- `query`: optional override; default uses Korean, English, target canonical,
  and aliases
- `domains`: optional whitelist domain subset
- `limit_domains`: default `6`, max `20`
- `max_results_per_domain`: default `3`, max `10`
- `dry_run`: return candidates without inserting raw queue rows

## Safe Flow

1. Confirm `kdb-api` and `kdb-worker` are running.
2. Run with `dry_run:true`.
3. If results look relevant, rerun with `dry_run:false`.
4. Watch `kwave_rss_items_raw.codex_status` and
   `kwave_media_observations` for extraction/consensus.

No result usually means the target spelling is not indexed in Google News RSS,
the whitelist has no enabled domain for that locale, or the entity aliases are
too weak.

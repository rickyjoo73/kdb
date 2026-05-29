# KDB Hermes — Deploy Bundle

Ordered, copy-paste commands for the owner to ship the Hermes multi-agent layer
to the LIVE stack. **Everything here is additive and default-off**: until
`KDB_HERMES_ENABLED=1` is set (and the supervisor branch is wired — see step c),
the autopilot sweeper behaves exactly as today.

## Stack reference (`docker-compose.kdb.yml`)

- **Single Go binary** `kdb-app` (container `kdb-app`) runs the lookup API (9100)
  + admin UI (9101) + the background loops (poller, enrich, autopilot) in one
  process — there is no separate worker/admin/api container in this compose.
- DB: container `kdb-db` (postgres:16-alpine), env `KDB_POSTGRES_USER` (default
  `kdb`), `KDB_POSTGRES_DB` (default `kdb`), `KDB_POSTGRES_PASSWORD` (required).
- The autopilot loop is `runAutopilotLoop` (`cmd/kdb/main.go:125`): first run
  ~30s after boot, then every 30m, via `buildAutopilotRunner`
  (`cmd/kdb/main.go:142`).

> Run every step **sequentially** and read the output before continuing. Take a
> DB snapshot first (step a.0). Set `KDB_POSTGRES_*` to match your `.env`; the
> examples below use the `kdb`/`kdb` defaults.

---

## a. Apply migrations 0061 + 0062 to kdb-db

The two migrations this release needs (0060 homonym is already applied live;
0061/0062 are NOT):

- `0061_hermes_runs.sql` → `kwave_kdb_hermes_runs` (supervisor audit/incident
  table; the admin Hermes page and `Supervisor.record()` both read/write it).
- `0062_enrich_attempts.sql` → `kwave_kdb_enrich_attempts` (per-field enrich
  ledger; lets the Enricher converge instead of re-trying unknowable fields).
  NOTE: 0062 has an FK to `kwave_entities(id)`, which already exists live.

```bash
# a.0  SNAPSHOT FIRST (rollback safety).
docker exec kdb-db pg_dump -U kdb -d kdb -Fc -f /tmp/kdb_pre_hermes.dump
docker cp kdb-db:/tmp/kdb_pre_hermes.dump ./kdb_pre_hermes.$(date +%Y%m%d%H%M).dump

# a.1  Apply the two migrations (idempotent: CREATE TABLE IF NOT EXISTS).
#      Run from the repo root so the relative paths resolve.
docker exec -i kdb-db psql -U kdb -d kdb -v ON_ERROR_STOP=1 < migrations/0061_hermes_runs.sql
docker exec -i kdb-db psql -U kdb -d kdb -v ON_ERROR_STOP=1 < migrations/0062_enrich_attempts.sql

# a.2  Verify the tables exist with the expected columns.
docker exec kdb-db psql -U kdb -d kdb -c '\d kwave_kdb_hermes_runs'
docker exec kdb-db psql -U kdb -d kdb -c '\d kwave_kdb_enrich_attempts'
```

Both files were validated on a throwaway postgres:16: apply + re-apply are
idempotent, and the admin queries, the supervisor's 16-column
`kwave_kdb_hermes_runs` INSERT, and the enricher ledger upsert/select all run
clean against the resulting schema.

## b. Rebuild + redeploy kdb-app

The new code (admin Hermes page + role agents + supervisor) is additive and
compiles into the existing `kdb-app` binary. Rebuild and recreate just that
service (kdb-db is NOT touched):

```bash
# b.1  Build the image.
docker compose -f docker-compose.kdb.yml build kdb-app

# b.2  Recreate kdb-app only (keep kdb-db running).
docker compose -f docker-compose.kdb.yml up -d --no-deps kdb-app

# b.3  Confirm it came up healthy.
docker compose -f docker-compose.kdb.yml ps
docker logs --tail=50 kdb-app
```

At this point the admin **Hermes** page is live (nav: Agents → Hermes) and
renders its empty state; the migrations are in place; but the supervised loop is
still OFF (the autopilot still runs the plain sweep).

## c. Enable Hermes

Hermes is gated by `KDB_HERMES_ENABLED=1`. Add it to `.env`, then recreate
kdb-app so the autopilot loop starts supervising:

```bash
# c.1  Add the flag (one line; avoid echoing secrets around it).
grep -q '^KDB_HERMES_ENABLED=' .env || echo 'KDB_HERMES_ENABLED=1' >> .env

# c.2  Recreate kdb-app to pick it up.
docker compose -f docker-compose.kdb.yml up -d --no-deps kdb-app
docker logs --tail=80 kdb-app
```

> WIRING NOTE (final hookup): `buildAutopilotRunner` (`cmd/kdb/main.go:142`)
> already builds the registry and calls `auto.RegisterSteps(registry)`, but the
> returned closure currently always calls the plain `auto.Run(ctx)`. The flag
> branch is the one remaining line to wire: when
> `os.Getenv("KDB_HERMES_ENABLED") == "1"`, construct `sup := hermes.New(pool)`
> and have the closure call `sup.SuperviseCycle(ctx, registry)` instead of
> `auto.Run(ctx)`. Until that branch is added, setting the env var is a no-op
> (safe). Everything else in this release — the migrations, the agents, the
> supervisor, and the admin page — is shipped and ready.

## d. Verify

```bash
# d.1  Admin Hermes page loads (session auth). Before any cycle it shows the
#      empty-state copy ("no runs yet"). Replace <admin-port> (default 9101).
curl -fsS http://localhost:9101/admin/hermes -c /tmp/j -b /tmp/j | grep -o 'no runs yet' \
  || echo "page reachable (runs present or behind login)"

# d.2  After one or two autopilot cycles, run rows appear, one per role.
docker exec kdb-db psql -U kdb -d kdb -c \
  "SELECT role, status, severity, items_in, items_out, items_dropped, retries, self_check_ok
     FROM kwave_kdb_hermes_runs ORDER BY created_at DESC LIMIT 20;"

# d.3  No SQL errors in the kdb-app log.
docker logs --tail=300 kdb-app | grep -iE 'hermes|error' || echo "no errors"

# d.4  LEAK COUNT — the canary signal. Any row with status='leak' or
#      items_dropped>0 is a silent-drop the supervisor caught. Healthy = zero.
docker exec kdb-db psql -U kdb -d kdb -c \
  "SELECT role, count(*) AS leaks, COALESCE(sum(items_dropped),0) AS dropped
     FROM kwave_kdb_hermes_runs WHERE status='leak' OR items_dropped>0
    GROUP BY role;"
```

The admin Hermes page (`/admin/hermes`) shows the same three views: latest run
per role, open incidents, and detected leaks — watch it directly if you prefer.

## Canary recommendation

1. Enable the flag (step c) and recreate kdb-app.
2. Watch **one or two autopilot cycles** (~30m apart) via `/admin/hermes`.
3. Confirm: every role shows a recent `ok` (or `retried`) run, **zero open
   incidents**, and **zero leaks** (`items_dropped=0`, no `status='leak'`).
4. Only after a clean canary should the loop be trusted to run unattended. If an
   incident or leak appears, roll back (step e) and inspect the offending role's
   `report` jsonb:
   `SELECT report FROM kwave_kdb_hermes_runs WHERE status<>'ok' ORDER BY created_at DESC LIMIT 1;`

## e. Roll back

Two independent levers; **the flag alone is usually enough** (returns the system
to today's behaviour with no schema change):

```bash
# e.1  DISABLE THE FLAG (fastest, fully reverts behaviour).
sed -i '/^KDB_HERMES_ENABLED=/d' .env
docker compose -f docker-compose.kdb.yml up -d --no-deps kdb-app

# e.2  (optional) DOWN migrations — only if you must remove the tables. Both are
#      additive, so leaving them is harmless. DOWN statements are also in each
#      migration's trailing comment block.
docker exec kdb-db psql -U kdb -d kdb -c 'DROP TABLE IF EXISTS kwave_kdb_enrich_attempts;'
docker exec kdb-db psql -U kdb -d kdb -c 'DROP TABLE IF EXISTS kwave_kdb_hermes_runs;'

# e.3  (full rollback) restore the pre-deploy snapshot from step a.0:
#   docker exec -i kdb-db pg_restore -U kdb -d kdb --clean < kdb_pre_hermes.<ts>.dump
```

Because the admin page, the supervisor, and the agents all no-op cleanly when
the table is empty / the flag is off, e.1 returns the stack to its prior state
immediately; e.2/e.3 are only for a clean teardown.

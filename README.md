# bouncewarden

A monitoring command centre for **PgBouncer** — the PostgreSQL connection
pooler. One static Go binary serves a live browser dashboard, an
operations console, and a configuration console for adding poolers to
monitor. It watches the pooler, never the database engine, and it never
sits in the client traffic path.

## What you get

- **Live dashboard** — reachability, active/waiting clients, server
  links, max wait, pool pressure table, throughput charts (counter-delta
  rates), and evidence-based findings. Updates stream over SSE.
- **Operations console** — `PAUSE`, `RESUME`, `SUSPEND`, `RELOAD`,
  `RECONNECT`, `DISABLE`, `ENABLE`, `SHUTDOWN` through PgBouncer's own
  admin console. Every action needs a typed confirmation bound to the
  target, is journaled to `audit.jsonl`, and never goes through a shell.
- **Configuration console** — add/edit/delete monitored machines at
  runtime; test connections (version + capability detection) before
  saving; write-only passwords; finding thresholds.
- **Pools & connections** — `SHOW POOLS` utilisation vs `pool_size`,
  per-database rates, `SHOW CLIENTS` / `SHOW SERVERS` tables.
- **CLI** — `bouncewarden status|monitor|probe` work without the console.

## Run with Docker (the only supported install)

```sh
docker compose up -d --build
# open http://localhost:8080 → Configuration → Add instance
```

The console stores its config (`config.yaml`) and `audit.jsonl` in the
`bouncewarden-data` volume. Set `BOUNCEWARDEN_TOKEN` to require a login
token.

Or plain Docker:

```sh
docker build -t bouncewarden .
docker run -d -p 8080:8080 -v bouncewarden-data:/data \
  -e BOUNCEWARDEN_TOKEN=secret bouncewarden
```

## Connecting a pooler

Add an instance with host/port and an admin-console credential:

- A **stats-only** account (`stats_users` in pgbouncer.ini) is enough
  for the live dashboard.
- An **admin** account (`admin_users`) additionally enables the
  Operations console verbs. Tick "admin rights" on the instance.

Secrets are stored in the config volume, never returned to the browser,
and never appear in audit output.

## Design notes

- One shared collection loop per poll interval (default 2 s); browser
  count never multiplies `SHOW` queries.
- Rates are derived from `SHOW STATS` counter deltas; counter resets
  (pooler restart) invalidate the window instead of producing spikes.
- Unavailable metrics render as `–`, never as fake zeros; stale data is
  labelled when an instance becomes unreachable.
- PgBouncer version capabilities are feature-detected per instance.

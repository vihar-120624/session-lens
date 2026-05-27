# session-lens

A local token-consumption dashboard for Claude Code sessions. A Stop-hook binary records per-session token usage and cost into a SQLite store; a small server exposes JSON aggregates and a dark dashboard so you can see what your month is actually costing.

Go was chosen because the Stop hook benefits from a single static binary with zero runtime install requirement on the host machine.

## Project structure

```
session-lens/
├── cmd/
│   ├── sessionlens-server/main.go   # HTTP server entrypoint (port 7821)
│   └── sessionlens-hook/main.go     # Claude Code Stop-hook entrypoint
├── internal/
│   ├── db/         # sqlite connection, schema bootstrap, UPSERT helpers
│   ├── stats/      # summary / daily / weekly / hourly / by-model / spike queries
│   ├── transcript/ # JSONL parser + per-family pricing table
│   ├── mock/       # deterministic mock dataset + parallel aggregations
│   └── server/     # http.ServeMux routes and JSON handlers
├── web/
│   └── index.html  # vanilla HTML/JS + Chart.js dashboard
└── data/           # runtime SQLite db (gitignored)
```

## Run

```
go mod tidy
go build -o bin/sessionlens-server ./cmd/sessionlens-server && go build -o bin/sessionlens-hook ./cmd/sessionlens-hook
./bin/sessionlens-server
```

Then open <http://127.0.0.1:7821>.

Configuration is via `.env` (see `.env.example`): `PORT`, `DB_PATH`, `PLAN_BUDGET_USD`, `MOCK_MODE` (`1` to boot with the deterministic mock dataset).

## Mock mode

The dashboard ships with a deterministic synthetic dataset useful for screenshots and visual regression. Flip it from the top-right toggle in the UI, or start the server with `MOCK_MODE=1`. The UI persists your last choice to `localStorage`. While mock mode is on a yellow banner is displayed and all aggregate endpoints serve the synthetic data; the `/v1/sessions` write path is unaffected.

## Hook setup

The hook reads the Stop event from stdin, parses the transcript, computes usage + cost, and POSTs to the local server. It always exits 0 — Claude is never blocked.

Add to `~/.claude/settings.json`:

```json
{
  "hooks": {
    "Stop": [
      { "command": "/ABSOLUTE/PATH/TO/session-lens/bin/sessionlens-hook" }
    ]
  }
}
```

The binary is fully static (pure-Go SQLite driver, no CGO) — no runtime needed on the host. Override the server URL via `SESSION_LENS_URL` if you run it elsewhere.

## API reference

- `GET  /healthz`                       — `{ok, version, mock}`
- `GET  /v1/mode`                       — `{mock}` — read current mode
- `POST /v1/mode`                       — `{mock: bool}` — flip mock mode
- `POST /v1/sessions`                   — UPSERT a SessionEvent (201 insert / 200 update)
- `GET  /v1/stats/summary`              — current calendar-month rollup + plan utilisation
- `GET  /v1/stats/daily?days=N`         — daily buckets, last N days (cap 365)
- `GET  /v1/stats/weekly?weeks=N`       — ISO-week buckets
- `GET  /v1/stats/hourly?days=N`        — hourly buckets, last N days (cap 90, default 7)
- `GET  /v1/stats/by-model?days=N`      — per-model totals + per-day stacked breakdown
- `GET  /v1/stats/projects?limit=N`     — top-N projects by cost
- `GET  /v1/stats/spikes`               — recent session and trend-day anomalies
- `GET  /`                              — dashboard UI
- `GET  /static/*`                      — static asset mount

Every `GET /v1/stats/*` endpoint also honours `?mock=1` as a request-scoped override.

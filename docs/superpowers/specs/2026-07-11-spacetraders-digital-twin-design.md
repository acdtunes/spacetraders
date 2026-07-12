# SpaceTraders Digital Twin — Design

**Date:** 2026-07-11
**Status:** Draft — brainstorm complete, written-spec review pending
**Scope:** A new `spacetraders/twin/` Node + TypeScript package that replicates the SpaceTraders v2 API contracts against a simple in-memory world + crude economy, with ship travel compressed 100×. **v1 replicates only the endpoints the captain-bootstrap DATA phase (Slice 1 of `sp-ysgb`) exercises.** Plus one small seam in `gobot/` to let a test daemon point at the twin.

---

## Purpose

We are building the captain-bootstrap coordinator (`sp-ysgb`) — a long-running reconciler that drives a cold agent to the jump gate across DATA → INCOME → GATE phases. Testing and debugging it against the **real** SpaceTraders API is slow (real travel times, rate limits, one shared live agent, no reset) and destructive (mutates the live economy).

The digital twin gives us a **fast, disposable, deterministic** API to drive the already-hardened `spacetraders` CLI and daemon against — so we can exercise the bootstrap reconciler end-to-end, restart it to prove idempotency, and reset the world between runs. Travel is compressed 100× so a real 2-hour flight resolves in ~72s.

**First use case:** the Slice-1 DATA-phase acceptance — from a cold-agent fixture, reach *3 probes scouting all markets*, idempotent across a daemon restart mid-purchase.

## Non-goals (v1)

- **No INCOME/GATE endpoints.** Contracts (`negotiate`/`accept`/`deliver`/`fulfill`) and construction (`GET/POST construction`) are **not** built in v1 — the coordinator code that drives them (Slices 2/3) does not exist yet. The world model and route layer are structured so they slot in later.
- **No economy dynamics in v1.** DATA does no trading; markets and shipyards are seeded with static, plausible data. Crude price/supply response to trades arrives with INCOME.
- **No persistent twin store.** The world is in-memory. Idempotency tests restart the **daemon**, not the twin (which stays up). An optional JSON snapshot can be added later if we ever need to restart the twin itself.
- **Not a full API.** Only the ~14 endpoints below. The other ~19 endpoints the client implements are out of scope until a phase needs them.

---

## Architecture

A single small **Fastify** HTTP server in Node + TypeScript, holding the world in memory, with a **seeded RNG** for reproducible fixtures. Chosen over two alternatives:

- **Persistent (sqlite) world** — rejected: unneeded for v1 (the twin stays up across daemon restarts), more moving parts.
- **Record/replay proxy (VCR-style)** — rejected: cannot model world mutations (buying a probe, moving a ship) or compressed travel; wrong tool for driving a reconciler.

### The base-URL seam (bot-side change)

Today `baseURL` is a hardcoded constant (`gobot/internal/adapters/api/client.go:23`) and every production call site uses the zero-arg `NewSpaceTradersClient()`; `cfg.API.BaseURL` is read only for display and never threaded into the client. So config alone does **not** redirect traffic.

**Chosen fix — env-var fallback in the constructor.** In `NewSpaceTradersClient()`, read `os.Getenv("ST_API_BASE_URL")` and fall back to the `baseURL` constant when unset:

```go
func NewSpaceTradersClient() *SpaceTradersClient {
    bu := baseURL
    if v := os.Getenv("ST_API_BASE_URL"); v != "" {
        bu = v
    }
    return NewSpaceTradersClientWithConfig(bu, defaultMaxRetries, defaultBackoffBase, nil)
}
```

~2 lines, one file. **Production is unaffected** (env unset → real API). The **test daemon sets `ST_API_BASE_URL=http://127.0.0.1:8080/v2`**. One binary serves both. (The client's 2 req/s self-limit still applies against the twin; the test `config.yaml` can raise `api.rate_limit` for faster runs.)

### Serving & admin namespace

- The twin listens on `:8080` (configurable) and mounts all API routes under the **`/v2` prefix** (matching the base URL). `GET /v2/` is the server-status endpoint.
- Twin control lives under a separate **`/_twin/`** namespace (never collides with the API contract): `POST /_twin/reset` (rebuild the cold-start fixture), `GET /_twin/state` (introspect the world for test assertions), `POST /_twin/time-compression` (override the 100× factor at runtime).

---

## Endpoint surface (v1 — 14 endpoints)

Exactly what the DATA phase hits: read treasury → price-check + buy probes → `scout-all-markets` (navigate → dock → read market/shipyard per waypoint), with arrival detected off timestamps.

| Method | Path (under `/v2`) | Purpose in DATA |
|---|---|---|
| GET | `/` | server status |
| POST | `/register` | mint the cold-start agent (fixture seeding via the bot's `player register`) |
| GET | `/my/agent` | treasury (credits) for the capital gate |
| GET | `/my/ships` | fleet snapshot (paginated, limit 20) |
| GET | `/my/ships/{s}` | single-ship state + **arrival detection** |
| POST | `/my/ships` | buy a probe (`PurchaseShip`) |
| POST | `/my/ships/{s}/navigate` | move idle hull to yard; scout-tour hops |
| POST | `/my/ships/{s}/orbit` | orbit before navigate |
| POST | `/my/ships/{s}/dock` | dock before refuel / market read |
| POST | `/my/ships/{s}/refuel` | refuel at market |
| GET | `/systems/{s}/waypoints` and `/systems/{s}/waypoints/{w}` | scout targets + waypoint detail |
| GET | `/systems/{s}/waypoints/{w}/market` · `/shipyard` | scout reads; probe price-check |

**Response fidelity is authoritative from the Go decode targets**, not the domain structs:
- Ship JSON → `gobot/internal/adapters/api/ship_dto.go:18` (`shipDTO`): `registration.role`, `nav.{systemSymbol,waypointSymbol,status,flightMode,route.arrival}`, `fuel`, `cargo.inventory[]`, `cooldown.expiration`, `engine.speed`, `frame`, `reactor`, `crew`, `modules[]`, `mounts[]`.
- All other endpoints → the **inline anonymous response structs** inside each `client.go` method (e.g. `GetMarket` client.go:1062, `GetShipyard` client.go:1134, `GetAgent` client.go:433, `NavigateShip` client.go:213, `PurchaseShip` client.go:1205, `ListWaypoints` client.go:461). The twin's JSON matches these field-for-field.
- **Market goods placement matters:** the client derives EXPORT/IMPORT/EXCHANGE by which array a good appears in (`client.go:1090`). The twin places each good in the correct list.

**Errors** use the SpaceTraders envelope `{"error":{"message","code","data"}}`. The twin returns the specific codes the client branches on: `4214` (ship must be docked), `4244` (ship not docked), `4511` (agent already has a contract); plus correctly-shaped 404/400 elsewhere.

---

## World model

In-memory, seeded deterministically:

- **Agent** — `symbol`, `token`, `headquarters`, `credits`, `startingFaction`. Cold-start fixture mirrors the real `/register` default (≈175,000 credits) for contract fidelity; the reconciler only cares that treasury sits in the cold-start band the bootstrap spec calls "~150k", which this satisfies. HQ in the home system.
- **Ships** — keyed by symbol, shaped per `shipDTO`. Fixture: 1 `COMMAND` frigate + 1 `SATELLITE`/`PROBE`, both at HQ, docked, full fuel.
- **Systems → Waypoints** — a home system with a handful of waypoints (`symbol`, `type`, `x`, `y`, `traits[]`, `orbitals[]`). At least one waypoint has a **shipyard**, several have **markets**, so scouting has real targets.
- **Markets** — per market waypoint: `exports/imports/exchange` + `tradeGoods[]` (`symbol`, `supply`, `activity`, `sellPrice`, `purchasePrice`, `tradeVolume`), static seeded values.
- **Shipyards** — per shipyard waypoint: ship listings including `SHIP_PROBE` priced ≈40k so the probe price-check + staged buy behave as the spec expects.

**Cold-start fixture** is produced by `POST /register` (and rebuildable via `POST /_twin/reset`), so it exactly matches what the bootstrap reconciler expects to observe on tick 1.

## Compressed clock (the crux)

The bot does **not** poll for `status==ARRIVED`. It reads the `nav.route.arrival` ISO-8601 timestamp from the navigate response and arms a local `time.Until(arrival)` timer (`gobot/internal/adapters/grpc/ship_state_scheduler.go:53`). Travel time is therefore governed **entirely by the timestamps the twin returns**.

- On `POST …/navigate`: compute the real ETA from distance/engine speed, then return `departureTime = now` and `arrival = now + realETA / COMPRESSION` (default 100), as valid RFC3339. Keep `arrival ≥ departureTime`.
- `nav.status` is **computed on read**, not stored: `GET /my/ships/{s}` reports `IN_TRANSIT` (location = origin) before the stored `arrival` instant, and flips to `IN_ORBIT` at the **destination** waypoint after it. (The bot's resync confirms `location == destination` before ETA, so the location must move exactly at the arrival instant.)
- **No background tick loop** in v1 — arrival is pure lazy evaluation on read.
- **Cooldowns** (`extract`/`siphon`/`jump` responses) use the same compressed-timestamp treatment; not exercised in DATA but the clock helper is shared.
- `COMPRESSION` is configurable via env (`TWIN_TIME_COMPRESSION`) and `POST /_twin/time-compression`.

---

## Test daemon & isolation

The test daemon is the **same binary**, a separate process, configured via `SPACETRADERS_CONFIG=/…/twin/test-config.yaml`. That config overrides:

- `api.base_url` is moot (seam is env-driven) — the launcher exports `ST_API_BASE_URL=http://127.0.0.1:8080/v2`.
- `database.url` → a separate `spacetraders_test` Postgres (or separate instance). The daemon **auto-migrates** an empty DB on boot, so no manual migration step.
- **The `--force` trap:** production runs with `--force`, which kills whatever PID is in `/tmp/spacetraders-daemon.pid`. The test daemon **must** override `daemon.pid_file` → `/tmp/spacetraders-daemon-test.pid`, `daemon.socket_path` → `…-test.sock`, and `metrics.port` → `9092` (or disable metrics). Without these, a test daemon kills production.
- `captain.player_id` → the seeded test player.

**Fixture seeding reuses the hardened CLI.** Because the twin implements `POST /register`, running the bot's own `player register` (via `ST_ACCOUNT_TOKEN`) against the twin mints the cold-start agent **and** writes the `players` row (agent symbol + JWT) that the daemon reads — no hand-rolled DB seeding. Direct SQL seed is the fallback.

## Observability

A `twin/docker-compose.test.yml` stands up a **parallel** Prometheus + Grafana (+ test Postgres) with distinct container names, named volumes, and **remapped host ports** (Prometheus `9093`, Grafana `3001`, Postgres `5433`) so nothing collides with the production stack (`9091`/`3000`/`5432`). A copied `prometheus.yml` scrapes the **test daemon's** metrics port (`9092`); the existing Grafana dashboards are reused, pointed at test data. Bring-up is manual (`docker compose -f twin/docker-compose.test.yml up`), sequenced **after** the DATA loop works end-to-end against the twin.

---

## Testing strategy

Vitest (`rtk vitest run`), two tiers:

1. **Contract tests (twin-only).** Assert each endpoint's JSON shape/casing against **golden fixtures captured once from the real API** (one real `/my/agent`, market, shipyard, ship, waypoint list). This is what proves "replicate exactly the API contracts." Golden fixtures are checked in under `twin/fixtures/golden/`.
2. **Integration (twin + bot).** Boot the twin, `player register` against it, run the daemon/CLI, and assert the **Slice-1 DATA acceptance**: reaches 3 probes assigned to scout-all-markets; **idempotent across a daemon restart mid-purchase** (restart the daemon, re-observe, no double-buy). `GET /_twin/state` backs the assertions.

---

## Directory layout

```
spacetraders/twin/
  package.json  tsconfig.json  vitest.config.ts
  src/
    server.ts            # Fastify app, /v2 + /_twin route registration
    world/               # in-memory model + seeded cold-start fixture
    clock.ts             # compressed-time helper (arrival/cooldown math)
    routes/              # one module per endpoint group (agent, ships, systems, market, shipyard, register)
    errors.ts            # SpaceTraders error envelope + code constants
  fixtures/golden/       # responses captured from the real API
  test-config.yaml       # daemon config for the test stack
  docker-compose.test.yml
  scripts/               # launch test stack, seed via player register
```

## Delivery slices (the twin itself)

1. **Twin skeleton + read endpoints + cold-start fixture.** Fastify server, world model, `POST /register` + `POST /_twin/reset`, `GET /` `/my/agent` `/my/ships[/{s}]` `/systems/{s}/waypoints[/{w}]` `/market` `/shipyard`. Golden contract tests for each. The bot-side env-var seam.
2. **Mutations + compressed clock.** `POST /my/ships` (buy probe), `navigate`/`orbit`/`dock`/`refuel`, the compressed-clock arrival logic + on-read `nav.status` flip. Contract tests for mutation responses.
3. **Test-daemon integration + observability.** `test-config.yaml`, launch/seed scripts, `docker-compose.test.yml`, and the end-to-end DATA-phase acceptance test (3 probes scouting, restart-idempotent).

## Open questions (deferred to the phase that needs them)

- **INCOME:** contract lifecycle endpoints + crude market price/supply response to trades; how deterministic contract generation should be.
- **GATE:** construction site + supply endpoints; whether construction progress needs a background tick or can stay lazy.
- **Golden fixtures:** whether we can capture from the live agent now, or need a throwaway registration to snapshot pristine `/register` output.

# Staging environment (sp-widl)

A first-class, **provably-isolated** staging stack. "Run a staging game" is one
repeatable command that can never collide with or corrupt prod.

## TL;DR

```bash
deploy/staging/up.sh                              # build (staging binaries) + start staging daemon + routing
deploy/staging/register.sh <CALLSIGN> '<JWT>'     # register a staging agent into the staging DB
deploy/staging/stc.sh player info                 # drive the staging daemon via the CLI
deploy/staging/down.sh                            # stop everything, clean sockets/pids
deploy/staging/down.sh --drop-db                  # ...and drop the spacetraders_staging DB
```

## Why this exists

Before this, staging isolation was hand-hacked at every step and one hardcoded
**live** pid nearly killed the live daemon. The worst hazard: `make build`
overwrote the shared `bin/` the **live** launchd daemon runs, so a live restart
could pick up unvalidated staging code. This directory + the env-aware build
close those holes.

## Isolation contract (every layer disjoint from prod)

| layer    | prod                              | staging                              |
|----------|-----------------------------------|--------------------------------------|
| binary   | `bin/spacetraders-daemon`         | `bin/spacetraders-daemon-staging`    |
| config   | `gobot/config.yaml` (local)       | `gobot/config.staging.yaml` (committed) |
| database | `spacetraders`                    | `spacetraders_staging`               |
| socket   | `/tmp/spacetraders-daemon.sock`   | `/tmp/spacetraders-staging.sock`     |
| pid      | `/tmp/spacetraders-daemon.pid`    | `/tmp/spacetraders-staging.pid`      |
| daemon   | `localhost:50052`                 | `localhost:50054`                    |
| routing  | `localhost:50051`                 | `localhost:50053`                    |
| metrics  | `localhost:9090`                  | `127.0.0.1:9095`                     |
| services | launchd `com.spacetraders.*`      | plain background processes (no launchd label) |

Pinned in CI by `gobot/internal/infrastructure/config/staging_isolation_test.go`
and re-asserted fail-closed at bring-up by `env.sh`.

## The env-aware build (the safety win)

```bash
make build                 # prod: bin/spacetraders-daemon  (BYTE-IDENTICAL to before)
make build ENV=staging     # staging: bin/spacetraders-daemon-staging  (prod binary untouched)
```

`make deploy` / `deploy-daemon` / `restart-daemon` / `install-launchd` **refuse**
any non-prod `ENV` (they manage the live launchd services), so a staging build
can never be deployed to prod by mistake.

## How prod stays untouched

- With `ENV` unset or `ENV=prod`, every Makefile path/label is byte-identical.
- `gobot/config.staging.yaml` sitting next to `config.yaml` is inert for prod:
  config discovery resolves the literal name `config.yaml`, and the prod daemon
  runs with `SPACETRADERS_CONFIG` unset.
- Staging runs as plain background processes — no launchd label is shared with
  `com.spacetraders.*`.
- `env.sh` derives the safety-critical identifiers (socket/pid/DB) **from the
  staging config the daemon actually loads** and refuses, fail-closed, anything
  that equals a prod resource or does not self-identify as staging. Teardown
  signals a process only after confirming its pid is ours *and* its command line
  carries a staging-unique marker.

## Prerequisites

- **Postgres** reachable at `localhost:5432` with the `spacetraders` role (the
  same local server prod uses; staging just gets its own database). `up.sh`
  creates `spacetraders_staging` if absent (via `psql`, or a detected postgres
  docker container).
- **Python** for the routing service (`gobot/services/routing-service/run.sh`
  builds its own venv on first run).
- Ideally a **separate SpaceTraders account/token** for staging, so it never
  spends the live account's 2 req/s budget (`config.staging.yaml` defaults to a
  conservative 1 req/s otherwise).

## Files

| file            | role                                                            |
|-----------------|-----------------------------------------------------------------|
| `env.sh`        | single source of truth + fail-closed prod-safety gate (source only) |
| `up.sh`         | build staging binaries, ensure DB, start routing + daemon       |
| `down.sh`       | stop staging services, clean sockets/pids (`--drop-db` optional)|
| `register.sh`   | register a staging agent into the staging DB (tokens masked)    |
| `stc.sh`        | run the CLI against the staging daemon                          |
| `run/`          | pids + logs (gitignored)                                        |

The committed `gobot/config.staging.yaml` is the staging config. The old ad-hoc
repo-root `staging/` directory (gitignored, hand-assembled) is superseded by
this and can be removed.

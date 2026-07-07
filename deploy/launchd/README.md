# launchd deploy (sp-898q)

The macOS launchd units that run the fleet are **rendered from the committed
`*.plist.template` files here** — never hand-edited in place. This keeps the
load-bearing bits in the repo instead of in a machine-local file that the next
regen silently clobbers.

## Why templates

Two recurring hazards this retires:

- **Dropped `BD_REAL` (sp-sk68).** The captain unit's env must carry
  `BD_REAL=<real bd>`; the `.local/bin` bd-router shim delegates to it, and
  without it *every* watchkeeper `gc`/`bd` call exits 127 — the historical
  whole-fleet wake outage. It used to be hand-added to the installed plist and
  was lost on the next regen. It now lives in the template + `make` default and
  can no longer be dropped.
- **Wrong / stale paths.** The previously committed plists pointed at
  `~/projects/spacetraders` and ran `bin/captain`; the live units run
  `bin/watchkeeper` under `~/IdeaProjects/cities/spacetraders`. Paths are now
  derived from the checkout you deploy from, so they cannot drift.

## Templates → rendered plists

`scripts/render-launchd.sh` (invoked by the `gobot/Makefile`) substitutes:

| placeholder     | value (Makefile default)                        |
|-----------------|-------------------------------------------------|
| `@@GOBOT_DIR@@` | `$(CURDIR)` — the gobot checkout being deployed |
| `@@REPO_ROOT@@` | parent of gobot (contains `dashboard/`)         |
| `@@HOME@@`      | `$(HOME)`                                        |
| `@@BD_REAL@@`   | `$(HOME)/IdeaProjects/bin/bd` (overridable)      |

Rendered output lands in `deploy/launchd/build/` (gitignored). The render fails
loudly if any placeholder is left unsubstituted.

## Orchestrator: apply at deploy

Run from the **production checkout's `gobot/`** (not a worktree — paths are
derived from `$(CURDIR)`):

```bash
# 1. Render + install into ~/Library/LaunchAgents and reload each unit:
make install-launchd

# 2. Or the full path — build stamped binaries, install, restart, and assert
#    the freshly-built commit is the one actually running:
make deploy
```

`make deploy` fails loudly (`assert-live-stamp`) if `captain-supervisor.log` /
`daemon.log` do not show the current commit within 30s — i.e. if a **stale
binary is still running**. The commit comes from the build-stamp banner each
binary now prints at startup (`internal/infrastructure/buildinfo`), also visible
via `spacetraders version` / `spacetraders --version`.

Overrides (e.g. a non-default bd path or a dry run to a scratch dir):

```bash
make install-launchd BD_REAL=/opt/bd AGENTS_DIR=/tmp/agents
make launchd GOBOT_DIR=/path/to/prod/gobot   # render only, inspect build/
```

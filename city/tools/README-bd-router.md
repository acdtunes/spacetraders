# bd-router — prefix-correct beads DB from any directory

## The problem

This repo hosts **two** beads databases:

| prefix | database                 | backend                         |
|--------|--------------------------|---------------------------------|
| `sp-`  | `<repo-root>/.beads`     | embedded Dolt (fleet/rig queue) |
| `st-`  | `<repo-root>/city/.beads`| Dolt **server** :59888 (city)   |

`bd` selects a database by walking **up** from `$PWD` to the nearest `.beads/`.
Agents rarely run from the "right" directory for the prefix they need, so:

- from `city/`: `bd show sp-xxxx` → *"no issue found"*, and `bd ready -l shipwright` → empty queue (it hit the city db);
- from the repo root: `bd show st-xxxx` → *"no issue found"*.

bd has **no** built-in prefix→database routing. Its multi-repo (`repos:`) and
`federation` features *hydrate/merge* issues into one database — that would mix
`sp`/`st` data across two different backends and change `bd ready` semantics, so
they are the wrong tool here.

## The fix

`bd-router` is a thin, transparent `bd` wrapper. It inspects the invocation and
pins `BEADS_DIR` (bd's highest-precedence DB selector) to the database that owns
the addressed prefix, then `exec`s the real bd:

- `sp-<id>` anywhere in the args → repo-root `.beads`
- `st-<id>` anywhere in the args → `city/.beads`
- no bead-id token, but `ready/list … -l <crew-role>` → repo-root `.beads`
  (crew roles are the dirs under `city/agents/`; the city db has **zero** beads
  carrying those labels, so this never shadows city work)
- **anything else** → passed through untouched (identical to plain bd)

Repo locations are discovered by walking up from the script to the directory
containing both `.beads/` and `city/.beads/`, so there are **no hardcoded
machine paths** — a checkout at any path works. If the target `.beads/` has a bd
`redirect` file (e.g. a git worktree wired by `captain.ProvisionWorktree`), the
router follows it, so it **composes with** the worktree-redirect fix rather than
overriding it.

## Activation (machine-local)

```sh
city/tools/install-bd-router.sh          # point ~/.local/bin/bd at the router
city/tools/install-bd-router.sh --uninstall   # restore the original
```

`~/.local/bin/bd` is first on PATH and is inherited by gc-launched agent
sessions, so all of them get routing. The real bd binary is left untouched; the
router finds it at runtime by scanning PATH (override with `BD_REAL` on a
stripped-down PATH). The symlink swap is not committed — **re-run the installer
after any bd reinstall that rewrites `~/.local/bin/bd`.**

## Tradeoffs / residual risk

- **Not bd-native.** A wrapper is required because bd cannot route by prefix
  across separate DBs. If bd later ships first-class prefix routing, drop this.
- **Activation is a machine-local symlink**, so it can be clobbered by a bd
  reinstall. Mitigation: the installer is idempotent and re-runnable; failure
  mode is graceful (plain bd, i.e. the original cwd-dependent behaviour — never
  data loss).
- **Cross-rig ops in one command** (e.g. `bd dep add sp-a st-b`) route by the
  **first** recognised token only; the other prefix would miss. Out of scope
  here (bd's own cross-rig resolution is unrelated to these two local DBs).
- Global `bd` name is shadowed, but the wrapper only ever acts on `sp`/`st`;
  every other prefix and every prefix-less command is a byte-for-byte passthrough,
  so other repos/towns on this machine are unaffected.

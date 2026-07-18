# Project Instructions for AI Agents

This file provides instructions and context for AI coding agents working on this project.

<!-- BEGIN BEADS INTEGRATION v:1 profile:minimal hash:ca08a54f -->
## Beads Issue Tracker

This project uses **bd (beads)** for issue tracking. Run `bd prime` to see full workflow context and commands.

### Quick Reference

```bash
bd ready              # Find available work
bd show <id>          # View issue details
bd update <id> --claim  # Claim work
bd close <id>         # Complete work
```

### Rules

- Use `bd` for ALL task tracking — do NOT use TodoWrite, TaskCreate, or markdown TODO lists
- Run `bd prime` for detailed command reference and session close protocol
- Use `bd remember` for persistent knowledge — do NOT use MEMORY.md files

## Session Completion

**When ending a work session**, you MUST complete ALL steps below. Work is NOT complete until `git push` succeeds.

**MANDATORY WORKFLOW:**

1. **File issues for remaining work** - Create issues for anything that needs follow-up
2. **Run quality gates** (if code changed) - Tests, linters, builds
3. **Update issue status** - Close finished work, update in-progress items
4. **PUSH TO REMOTE** - This is MANDATORY:
   ```bash
   git pull --rebase
   bd dolt push
   git push
   git status  # MUST show "up to date with origin"
   ```
5. **Clean up** - Clear stashes, prune remote branches
6. **Verify** - All changes committed AND pushed
7. **Hand off** - Provide context for next session

**CRITICAL RULES:**
- Work is NOT complete until `git push` succeeds
- NEVER stop before pushing - that leaves work stranded locally
- NEVER say "ready to push when you are" - YOU must push
- If push fails, resolve and retry until it succeeds
<!-- END BEADS INTEGRATION -->


## Required reading (in order, at session start)

1. Your role template (city agents: `city/agents/<role>/prompt.template.md`)
2. `RULINGS.md` — standing Admiral orders; bind every decision
3. `PLAYBOOK.md` — standing rules & strategies (gameplay + process)
4. `CLI-PRIMER.md` — the `spacetraders` CLI capability map + the 3-layer knob system

Engineering reference (shipwright + build agents): `ENGINEERING.md` — gobot code-facts,
worktree/lane hazards, deploy recovery, data-model gotchas, the digital twin. Read by area,
not primed every wake.

Historical context (incidents, numbers, the why) lives under `docs/retrospectives/` —
reference material, not doctrine. Doctrine is the books above plus bd memories.

## Build & Test

```bash
cd gobot
make build            # daemon + CLI + watchkeeper
go test ./...         # full suite (-race for the pre-merge sweep)
make install-cli      # install the spacetraders CLI
make restart-daemon   # deploy daemon changes (shipwright only — batches by content)
```

All code lands via worktree → `captain-gate` → main (RULINGS #13). Never merge by hand.

## Architecture Overview

One `spacetraders` cobra CLI + three processes: the Go daemon (single writer of all game
state; runs every coordinator as a recoverable container), the Python routing service
(tour/VRP solver, gRPC :50051), and the watchkeeper (wakes the captain). Postgres behind
the daemon; Prometheus/Grafana for observability. See `CLI-PRIMER.md` §1 and
`gobot/CLAUDE.md` for depth.

## Conventions & Patterns

- Features ship default-off (byte-identical); arming is part of delivery — see PLAYBOOK §10.
- Money guards fail closed and are never weakened (RULINGS #4).
- `bd` is cwd-resolved: engineering queue (sp-) from the repo root, city db (st-) from `city/`.
- Protected paths — never modified by build agents: `gobot/internal/captain/**`,
  `cmd/captain-gate/**`, `city/agents/**`; `gc` source is off-limits entirely.

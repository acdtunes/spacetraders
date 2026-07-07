> ENGINE MOVED: this workspace is legacy. The captain is a city agent (acd run captain); state lives in beads (sp- db). See docs/superpowers/specs/2026-07-06-ai-engine-city-bridge-design.md.

# Captain workspace

Working directory for autonomous `claude -p` strategy sessions, driven by
`gobot/cmd/watchkeeper`. See docs/superpowers/specs/2026-07-02-autonomous-captain-design.md.

- `CLAUDE.md` — persona + session contract loaded into every session
- `CLI_REFERENCE.md` — generated; run `make cli-reference` in gobot/ (do not edit)
- `state/` — the captain's memory (log, strategy, lessons, decision ledger)
- `reports/bugs/` — escalated failures awaiting the fix pipeline (plan 2 of 2)
- `DISABLED` — create this file to stop all sessions (kill switch)

Run one supervised tick manually: `cd ../gobot && make build && ./bin/watchkeeper --once`

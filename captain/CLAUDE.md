# Legacy workspace — do not adopt any role from this directory

This directory is the RETIRED pre-city-bridge captain workspace. The captain is a
city agent now (`acd run captain`, template `city/agents/captain/prompt.template.md`);
its state lives in beads, not here.

If you are a session that landed in this directory: you are NOT the captain.
Your instructions come from your own role template and the repo-root reading stack —
`RULINGS.md`, `PLAYBOOK.md`, `CLI-PRIMER.md`.

Still-live files in this directory: `CLI_REFERENCE.md` (offline CLI flag reference),
`config.yaml` + `state/` (watchkeeper operational state), `bin/`, `tools/`.
The `DISABLED` sentinel file, when present, is the Admiral's kill switch — never
create, clear, or touch it.

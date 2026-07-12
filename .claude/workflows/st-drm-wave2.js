// st-drm WAVE 2 (bead st-drm.12) — authored 2026-07-12. Referenced by handoff bead st-drm.13.
// Runs ONLY after Wave 1 (st-drm-wave1.js) has merged into feat/twin-digital-twin.
//
// SHAPE: triage -> adaptive fan-out. A single triage agent runs the merged low-level acceptance suite,
// then partitions the twin/gobot fixes needed to green it into PROVABLY DISJOINT file-groups; parallel
// crafters each own one group in an isolated worktree. If the fixes are not separable, triage emits ONE
// group and it degrades to a single serial crafter. Crafters verify IN-PROCESS only; the LIVE-STACK reds
// (ship-actions/cargo) are confirmed by the ORCHESTRATOR after merge (that hand-off IS the entry to Wave 3).
//
// LAUNCH: cwd inside the feat/twin-digital-twin worktree, Wave 1 already merged. Workflow({scriptPath}).
// Pre-check: git merge-base --is-ancestor main HEAD; twin unit suite green; the four Wave-1 tracks merged.
//
// MERGE PROTOCOL (orchestrator, agents CANNOT): verify each crafter diffstat NON-EMPTY (empty worktree
// merges empty); merge crafter branches into feat/twin-digital-twin sequentially with --no-ff, diffstat
// after each; groups are file-disjoint by construction so conflicts should not arise (reconcile additively
// if they do). GATE: cd twin && vitest run --config vitest.unit.config.ts (green); cd gobot && go build ./...
// Then the orchestrator runs the LIVE-STACK low-level acceptance (twin/tests/acceptance/*.e2e.test.ts,
// one file at a time, hard-reset :5434 between runs) to confirm the deferred reds; any still-red becomes a
// focused fix in the orchestrator loop. When ALL low-level acceptance is green, close st-drm.12 -> Wave 3.
export const meta = {
  name: 'st-drm-wave2',
  description: 'Epic st-drm Wave 2 (st-drm.12): triage the merged low-level acceptance suite into disjoint fix-groups, then parallel crafters fix the twin (+gobot read-back) so all low-level acceptance greens. In-process verified; live-stack confirmed by the orchestrator.',
  phases: [
    { title: 'triage', detail: 'partition the failing low-level acceptance into disjoint file-groups', model: 'opus' },
    { title: 'fix', detail: 'one crafter per disjoint group, isolated worktree, in-process verified', model: 'opus' },
  ],
}

const ISO = [
  'ISOLATION: you run in YOUR OWN fresh git worktree branched from the Wave-1-merged feat/twin-digital-twin',
  '(twin/ + gobot/ + bootstrap-harness/ present and current). Work ONLY in your cwd via RELATIVE paths.',
  'NEVER cd into other checkouts and NEVER touch beads (bd). No node_modules: "npm ci" in twin/ first',
  '(one Bash call, timeout 120000; retry once on timeout).',
].join('\n')

const STALL = [
  'ANTI-STALL (non-negotiable): every Bash call timeout <= 120000 ms; NEVER "find /"; NEVER boot a',
  'twin/daemon or run live-stack (no default vitest config, no globalSetup, no *.e2e.test.ts, no',
  'bootstrap-harness e2e) — :8080 / /tmp/spacetraders-daemon-test.sock / Postgres :5434 are GLOBAL',
  'singletons a worktree does NOT isolate. In-process only: ./node_modules/.bin/vitest run --config',
  'vitest.unit.config.ts <file> (from twin/); targeted "go test" on touched packages (timeout 120000,',
  'split packages).',
].join('\n')

const GITP = [
  'GIT: COMMIT ALL your work in YOUR worktree/branch, conventional messages, --no-verify. NEVER stage',
  '.beads/issues.jsonl, issues.jsonl, or package-lock.json (unless from a dep you intentionally added).',
  'An uncommitted worktree merges EMPTY. Final report block: branch / worktree(pwd) / commits / diffstat',
  '(git diff --stat feat/twin-digital-twin...HEAD).',
].join('\n')

const TRIAGE = [
  'You TRIAGE bead st-drm.12 for the orchestrator. You produce a PLAN ONLY — do NOT fix anything, do NOT',
  'edit src, do NOT commit. You run in the Wave-1-merged feat/twin-digital-twin worktree (this cwd).',
  '', STALL,
  '(You are read + run-in-process only; "npm ci" in twin/ if node_modules is absent.)',
  '',
  'INPUTS: read the four Wave-1 agent reports (their final messages are in the run journal / passed to you',
  'by the orchestrator) for: time-mock knobs (st-drm.8), new/838fixed CLI specs (st-drm.11), any twin fix',
  'already made for construction shape (st-drm.9), and harness-audit wave-2 items (st-drm.10). Then:',
  '1. Run the IN-PROCESS low-level suite: cd twin && ./node_modules/.bin/vitest run --config',
  '   vitest.unit.config.ts (207+ plus the Wave-1 additions). List every RED with file + assertion.',
  '2. Enumerate the LIVE-STACK reds you CANNOT run here but know from reports/inventory: at minimum',
  '   twin/tests/acceptance/ship-actions.e2e.test.ts (refuel — LIKELY fixed by the st-drm.8 time-mock,',
  '   confirm from the report; purchase credit-drop — daemon player-info read-back) and any *.e2e the',
  '   st-drm.11 track authored RED.',
  '3. For each red, name the ROOT-CAUSE FILE(S) to change (twin/src/routes/*, twin/src/world/*,',
  '   gobot/internal/... for daemon read-back). Then PARTITION into groups such that NO two groups share',
  '   ANY file (pairwise-disjoint file ownership — this is what makes the fan-out safe). Prefer 2-4 groups;',
  '   if the fixes cannot be separated without file overlap, emit ONE group (serial).',
  '',
  'OUTPUT (StructuredOutput schema): { groups: [ { id, files:[exclusively-owned paths], reds:[the tests',
  'this greens], inProcessVerifiable:boolean, task:"precise fix instruction incl. the observable to satisfy" } ],',
  'liveStackDeferred:[reds only confirmable live — orchestrator verifies post-merge], notes }. Guarantee',
  'the union of files is conflict-free across groups.',
].join('\n')

const TRIAGE_SCHEMA = {
  type: 'object',
  required: ['groups', 'liveStackDeferred'],
  properties: {
    groups: {
      type: 'array',
      items: {
        type: 'object',
        required: ['id', 'files', 'reds', 'inProcessVerifiable', 'task'],
        properties: {
          id: { type: 'string' },
          files: { type: 'array', items: { type: 'string' } },
          reds: { type: 'array', items: { type: 'string' } },
          inProcessVerifiable: { type: 'boolean' },
          task: { type: 'string' },
        },
      },
    },
    liveStackDeferred: { type: 'array', items: { type: 'string' } },
    notes: { type: 'string' },
  },
}

const crafterPrompt = (g) => [
  'You are an nw-software-crafter fixing ONE disjoint slice of bead st-drm.12 so the twin low-level',
  'acceptance greens. The twin conforms to the real API (gobot/api/openapi.json + official docs); NEVER',
  'weaken a test to pass. You OWN EXACTLY these files (do not touch any other file — another crafter may',
  'own it): ' + JSON.stringify(g.files),
  '', ISO, '', STALL, '',
  'YOUR TASK: ' + g.task,
  'REDS TO GREEN: ' + JSON.stringify(g.reds),
  g.inProcessVerifiable
    ? 'VERIFY IN-PROCESS: drive the fix RED->GREEN via ./node_modules/.bin/vitest run --config vitest.unit.config.ts on the relevant file, then run the FULL twin unit suite (same config) — it must stay green. For a gobot change, add/extend a go unit test + go build ./... + targeted go test.'
    : 'This slice targets a LIVE-STACK red the orchestrator will confirm after merge; you CANNOT run it. Implement the fix guided by the real-API contract + the in-process analogue (e.g. an inject() unit test that exercises the same twin logic), and add such an in-process test to lock the behavior. Do NOT run *.e2e.test.ts.',
  'Keep the full twin unit suite green regardless.',
  '', GITP, '',
  'RETURN: files changed; the observable you now satisfy; in-process test result + unit-suite count; if',
  'gobot, go build/test result; then the mandatory report block.',
].join('\n')

phase('triage')
const plan = await agent(TRIAGE, { label: 'st-drm.12 triage', phase: 'triage', model: 'opus', schema: TRIAGE_SCHEMA })
const groups = (plan && Array.isArray(plan.groups)) ? plan.groups.filter((g) => g && Array.isArray(g.files) && g.files.length) : []
log(`triage: ${groups.length} disjoint fix-group(s); ${(plan && plan.liveStackDeferred || []).length} live-stack red(s) deferred to the orchestrator`)
if (!groups.length) return { plan, fixes: [], warning: 'triage produced no actionable groups — orchestrator handles st-drm.12 manually' }

phase('fix')
const fixes = await parallel(groups.map((g) => () =>
  agent(crafterPrompt(g), { label: `st-drm.12 fix:${g.id}`, phase: 'fix', model: 'opus', agentType: 'nw-software-crafter', isolation: 'worktree' })
    .then((r) => ({ group: g.id, files: g.files, result: r }))
))
return { plan, fixes: fixes.filter(Boolean), liveStackDeferred: plan.liveStackDeferred }

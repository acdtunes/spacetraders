package strip

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Adversarial-calibration blockers.
//
// Every case here is a REAL corpus line that the calibration run proved the
// tool CORRUPTS while every existing gate stays green -- the AST hash is blind
// to comments, and the prose-word bag is a bag, so a rewrite can invert who did
// what without losing a single word.
//
// The fix for all of them is a SKIP. None of these lines may change.
// TestBlockerFixturesAreReal re-reads every `in` and every `prev` from disk.
// ---------------------------------------------------------------------------

type blockerCase struct {
	name string
	file string // repo-relative source of `in`
	line int    // 1-based line of `in`
	prev string // the comment line at line-1, "" when `in` opens its group
	in   string
	skip string // the gate that must refuse it
	lits []string
}

// B1. The ';' clause boundary leaves with the id, fusing two independent
// clauses into a run-on that re-attributes the action to the wrong noun.
// No prose word is lost, so nothing the tool measures can see it.
var b1ClauseFusion = []blockerCase{
	{
		name: "B1a_override_moved",
		file: "internal/application/trading/commands/run_opportunity_relocator_test.go", line: 1012,
		prev: "// TestResolveRelocatorTickSeconds_OperatorOverrideStillWins proves the faster default did",
		in:   "// not become a hard-coded cadence. TickSeconds remains the override; sp-fjvlm moved only",
		skip: "S-CLAUSE-BOUNDARY",
	},
	{
		name: "B1b_reserve_floor_rehomed",
		file: "internal/application/bootstrap/commands/run_bootstrap_coordinator.go", line: 55,
		in:   "// common.ImmutableReserveFloor + 100k; sp-zq635 re-homed it into the ONE floor source so the base and",
		skip: "S-CLAUSE-BOUNDARY",
	},
	{
		name: "B1c_construction_knob_retired",
		file: "internal/application/manufacturing/commands/run_construction_coordinator.go", line: 61,
		in:   "// into a logged, retried tick. Was operator-tunable per-launch; sp-sxyx6 retired the knob —",
		skip: "S-CLAUSE-BOUNDARY",
	},
	{
		// Bracket-wide: the ';' clause lives inside the parenthetical, so the
		// FIRST id in the same bracket is protected too. Half-stripping a
		// citation phrase is the worst of both outcomes.
		name: "B1d_bracket_wide_clause",
		file: "internal/adapters/grpc/daemon_server.go", line: 1873,
		in:   "// (sp-86yb gas coordinators; sp-3lj5 extends this to warehouses). No-ops if",
		skip: "S-CLAUSE-BOUNDARY",
	},
}

// B2. The deleted id is the grammatical SUBJECT of the verb that follows it.
// The A7 adjudication added `terminalAfter` to R16 for exactly this and was
// never generalised, so R5c / R9 / R10 / R14 / R17b still ship it.
var b2SubjectVerb = []blockerCase{
	{
		name: "B2a_R5c_was_closed",
		file: "internal/application/probebuy/guarded_probe_buyer.go", line: 148,
		prev: "// DO NOT REACH FOR THAT FILE UNEXAMINED, THOUGH. The two paths do not price the same journey, and",
		in:   "// sp-1ngte was closed rather than \"fixed\" for exactly this reason. LandedProbeCost models",
		skip: "S-SUBJECT-VERB",
	},
	{
		// The previous line DOES end its sentence, so the wrapped-sentence gate
		// cannot be what saves this one -- only the subject-verb gate can.
		name: "B2b_R5c_narrowed_this",
		file: "internal/application/scouting/commands/run_probe_sensing_adoption_test.go", line: 479,
		prev: "// ErrSlotClaimed, having lost its claim to a write that was never about it).",
		in:   "// sp-0eufi NARROWED THIS. The WANTED case moved to",
		skip: "S-SUBJECT-VERB",
	},
	{
		name: "B2c_R14_relative_clause_subject",
		file: "internal/adapters/persistence/chain_pnl_repository.go", line: 81,
		in:   "// netting (SUM(sign(is_buy)·realized_units·realized_unit_price)) that sp-rd21 proved read ~2x",
		skip: "S-SUBJECT-VERB",
	},
	{
		// Directly under an Admiral directive line, which ends in '.', so again
		// the wrapped-sentence gate is not what refuses this.
		name: "B2d_R5c_was_built",
		// provenance retired: Layer 2 rewrote internal/application/parkedsensing/yardqueue.go:16,
		// so this input no longer exists in the corpus. The rule assertion stands.
		file: "", line: 0,
		prev: "// THE SECOND HALF IS AN ADMIRAL DIRECTIVE AND IT OVERRULES THE FIRST'S DESIGN.",
		in:   "// sp-7qhum was built to leave coverage-first as the top-level key, so it could",
		skip: "S-SUBJECT-VERB",
	},
	{
		name: "B2e_R10_removed_the_master_flag",
		// provenance retired: Layer 2 rewrote internal/application/bootstrap/commands/run_bootstrap_coordinator.go:81,
		// so this input no longer exists in the corpus. The rule assertion stands.
		file: "", line: 0,
		in:   "// Death-spiral cure (UNCONDITIONALLY ON, sp-gm7r removed the master flag). It replaces the premature",
		skip: "S-SUBJECT-VERB",
	},
	{
		name: "B2f_R9_removed_the_flag",
		// provenance retired: Layer 2 rewrote internal/application/bootstrap/commands/run_bootstrap_coordinator.go:456,
		// so this input no longer exists in the corpus. The rule assertion stands.
		file: "", line: 0,
		in:   "// sequential). Consulted every tick (sp-gm7r removed the flag); NOT a progress cursor — dropped on",
		skip: "S-SUBJECT-VERB",
	},
	{
		name: "B2g_R9_reconcile_removed_the_flag",
		// provenance retired: Layer 2 rewrote internal/application/bootstrap/commands/run_bootstrap_reconcile.go:360,
		// so this input no longer exists in the corpus. The rule assertion stands.
		file: "", line: 0,
		in:   "// ON (sp-gm7r removed the flag) — consulted every tick, but it only ever touches a STICKY, starved latch.",
		skip: "S-SUBJECT-VERB",
	},
	{
		name: "B2h_R9_unified_gate_removed_the_flag",
		file: "internal/application/manufacturing/commands/run_construction_coordinator_unified_gate_test.go", line: 18,
		in:   "// per-node gates go margin-blind. Unified gate-fill is unconditional (sp-9i4mq removed the flag).",
		skip: "S-SUBJECT-VERB",
	},
	{
		name: "B2i_R9_feeding_policy_override",
		file: "internal/application/manufacturing/services/feeding_policy_test.go", line: 18,
		in:   "// set (sp-sxyx6 removed the per-run override).",
		skip: "S-SUBJECT-VERB",
	},
	{
		// Third-person singular present, not a past participle: the -ed suffix
		// rule alone cannot catch this one.
		name: "B2j_R9_retires_present_tense",
		file: "internal/application/trading/commands/run_trade_route_coordinator.go", line: 635,
		in:   "// handler neither claims nor releases (sp-zewt retires the vjwb orphan-on-death).",
		skip: "S-SUBJECT-VERB",
	},
}

// B3. A line-initial id on a WRAPPED sentence is not a leading tag. The head
// rules delete it and capitalise the next word mid-clause; in the worst cases
// the previous line ends with the governor whose object the id was.
var b3WrappedSentence = []blockerCase{
	{
		name: "B3a_ferry_governor_let",
		// provenance retired: Layer 2 rewrote internal/application/parkedsensing/ferry.go:100,
		// so this input no longer exists in the corpus. The rule assertion stands.
		file: "", line: 0,
		prev: "// measured at a mean of 5,900 credits over 4,235 jumps. That belief is what let",
		in:   "// sp-e46yc happen — an authorised 10.15M probe expansion spent a further 6.44M",
		skip: "S-WRAPPED-SENTENCE",
	},
	{
		name: "B3b_scanner_governor_and",
		file: "internal/application/parkedsensing/scanner.go", line: 99,
		prev: "// IT IS THE ATTEMPT CLOCK, NOT THE FRESHNESS CLAIM — see LastDataAt, and",
		in:   "// sp-zml2u for why the two must be separate. It is fed from",
		skip: "S-WRAPPED-SENTENCE",
	},
	{
		name: "B3c_gategraph_governor_made",
		file: "internal/application/system/gategraph/service.go", line: 741,
		prev: "//     stored adjacency post-sp-yginc), so the sp-qxa4 \"unreadable frontier\" concern that made",
		in:   "//     sp-e059j pick the strict resolver does not apply here.",
		skip: "S-WRAPPED-SENTENCE",
	},
	{
		name: "B3d_yardqueue_governor_under",
		file: "internal/application/parkedsensing/yardqueue_test.go", line: 751,
		prev: "// wrong reason. Under sp-7qhum a dark yard there merely won a tiebreak; under",
		in:   "// sp-0j5hi it would take POSITION ZERO of every tick for as long as the system",
		skip: "S-WRAPPED-SENTENCE",
	},
	{
		name: "B3e_stocker_governor_for",
		file: "internal/application/trading/commands/run_stocker_coordinator_test.go", line: 338,
		prev: "// TestStockerWarehouseAt_ResolvesToNewestOnZombieCollision is the regression pin for",
		in:   "// sp-3lj5 at the stocker's own warehouseAt call site: warehouse-TORWIND-12-bad719ff",
		skip: "S-WRAPPED-SENTENCE",
	},
}

// B4. The id is the antecedent of a sub-label that SURVIVES the deletion,
// leaving a cross-reference that resolves to nothing. Four of these are the
// pointers INTO the 87-line contract table the tool is required to protect.
var b4OrphanSublabel = []blockerCase{
	{
		name: "B4a_contract_table_pointer_semicolon",
		file: "internal/application/trading/commands/run_trade_route_coordinator.go", line: 136,
		in:   "// CompletionOutcome vetoes the runner's success=true (sp-7yej invariant 2);",
		skip: "S-SUBLABEL",
	},
	{
		name: "B4b_contract_table_pointer_and",
		file: "internal/application/trading/commands/run_trade_route_coordinator.go", line: 269,
		in:   "// runner reads it through CompletionOutcome (sp-7yej invariant 2) and",
		skip: "S-SUBLABEL",
	},
	{
		name: "B4c_money_guard_section_ref",
		file: "internal/application/contract/idle_arb.go", line: 873,
		in:   "// WORKING-CAPITAL RESERVE GATE (sp-zq635 §4a): a GLOBAL treasury bound, not a",
		skip: "S-SUBLABEL",
	},
	{
		name: "B4d_money_guard_section_ref_4b",
		file: "internal/application/contract/services/delivery_executor.go", line: 55,
		in:   "// market source-buy (sp-zq635 §4b). Opt-in (WithSourceBuyFloor): OFF leaves the",
		skip: "S-SUBLABEL",
	},
	{
		// Bracket-wide. The comma separated a Go symbol from the section ref;
		// R11 would fuse them into "common.ImmutableReserveFloor §4b".
		name: "B4e_immutable_floor_list_fusion",
		file: "internal/application/contract/services/delivery_executor.go", line: 449,
		in:   "// reserve floor (common.ImmutableReserveFloor, sp-zq635 §4b / sp-05glh) allows, from a",
		skip: "S-SUBLABEL",
	},
	{
		name: "B4f_lead_tag_fix_2",
		file: "internal/adapters/grpc/container_runner.go", line: 730,
		in:   "// sp-6g96 Fix 2: workflow.finished is SCOPED by parentage (workflow.failed",
		skip: "S-SUBLABEL",
	},
	{
		name: "B4g_lead_tag_fix_3",
		file: "internal/infrastructure/database/connection.go", line: 20,
		in:   "// sp-6g96 Fix 3: pgx's default mode (QueryExecModeCacheStatement) caches a",
		skip: "S-SUBLABEL",
	},
	{
		// Bracket-wide again: the surviving "C2c" is exactly what R6/R9 leave.
		name: "B4h_epic_sublabel_C2c",
		file: "cmd/spacetraders-daemon/main.go", line: 674,
		in:   "// Live per-park DEMAND weights (sp-5rakx/sp-bu6ma, epic sp-9le3x C2c): the coordinator",
		skip: "S-SUBLABEL",
	},
	{
		// A definite description whose ONLY identifying content was the id.
		name: "B4i_definite_description_the_invariant",
		file: "cmd/spacetraders-daemon/main.go", line: 1094,
		in:   "// Manning stays in-system only (the sp-qxa4 invariant); repositioning just moves the",
		skip: "S-SUBLABEL",
	},
	{
		name: "B4j_definite_description_the_fix",
		file: "internal/adapters/grpc/container_runner_shipless_completion_test.go", line: 46,
		in:   "// TestSignalCompletion_ShiplessCoordinator_NoCryWolfWarning pins the sp-hehz fix:",
		skip: "S-SUBLABEL",
	},
}

// B5. Hand-aligned ASCII banners. runOffsets fingerprints runs of 3+ SPACES,
// so a dash ruler carries no protection at all and the banner silently loses
// width against its siblings.
var b5Banner = []blockerCase{
	{
		name: "B5a_buyqueue_banner",
		file: "internal/application/parkedsensing/buyqueue_test.go", line: 1577,
		in:   "// --- the silent refusal (sp-l50w1) -------------------------------------------",
		skip: "S-BANNER",
	},
	{
		name: "B5b_scanner_banner",
		file: "internal/application/parkedsensing/scanner_test.go", line: 627,
		in:   "// --- the three outcomes of a turn (sp-zml2u) ---------------------------------",
		skip: "S-BANNER",
	},
	{
		name: "B5c_banner_closing_half",
		file: "internal/adapters/grpc/contract_scaler_ports.go", line: 407,
		in:   "// (sp-urpxy) ---",
		skip: "S-BANNER",
	},
	{
		name: "B5d_trade_fleet_banner",
		file: "internal/infrastructure/config/trade_fleet.go", line: 182,
		in:   "// --- Wider candidate set (sp-jsng, epic sp-fguo build-decomp #5; the #1 fleet-$/hr lever, sp-7q5t) ---",
		skip: "S-BANNER",
	},
}

// B6. R8 leaves a continuation line opening on a bare em-dash. V7 is
// differential and the BEFORE line already opened with '(', so it never fires.
var b6PunctHead = []blockerCase{
	{
		name: "B6a_models_em_dash_head",
		file: "internal/adapters/persistence/models.go", line: 1017,
		prev: "// SensingSlotModel is one probe PLACEMENT in the parked-probe sensing ledger",
		in:   "// (sp-k6v8z) — the durable spine that makes the whole model re-derivable from",
		skip: "S-HEADPUNCT",
	},
}

func allBlockerCases() []blockerCase {
	var out []blockerCase
	for _, g := range [][]blockerCase{
		b1ClauseFusion, b2SubjectVerb, b3WrappedSentence,
		b4OrphanSublabel, b5Banner, b6PunctHead,
	} {
		out = append(out, g...)
	}
	return out
}

func runBlocker(t *testing.T, c blockerCase) {
	t.Helper()
	got := RewriteComment(c.in, litSet(c.lits...), testOpts(), LineCtx{PrevLine: c.prev})
	if got.Text != c.in {
		t.Errorf("line MUST be left alone\n  prev: %q\n  in:   %q\n  got:  %q", c.prev, c.in, got.Text)
	}
	if len(got.Rules) != 0 {
		t.Errorf("no rule may fire, got %v", got.Rules)
	}
	found := false
	for _, s := range got.Skips {
		if s.Reason == c.skip {
			found = true
		}
	}
	if !found {
		t.Errorf("want skip %s, got %+v", c.skip, got.Skips)
	}
}

func TestBlocker1ClauseBoundaryFusion(t *testing.T) {
	for _, c := range b1ClauseFusion {
		t.Run(c.name, func(t *testing.T) { runBlocker(t, c) })
	}
}

func TestBlocker2SubjectVerbDeletion(t *testing.T) {
	for _, c := range b2SubjectVerb {
		t.Run(c.name, func(t *testing.T) { runBlocker(t, c) })
	}
}

func TestBlocker3WrappedSentenceHeadTag(t *testing.T) {
	for _, c := range b3WrappedSentence {
		t.Run(c.name, func(t *testing.T) { runBlocker(t, c) })
	}
}

func TestBlocker4OrphanedSublabel(t *testing.T) {
	for _, c := range b4OrphanSublabel {
		t.Run(c.name, func(t *testing.T) { runBlocker(t, c) })
	}
}

func TestBlocker5AsciiBannerSkipped(t *testing.T) {
	for _, c := range b5Banner {
		t.Run(c.name, func(t *testing.T) { runBlocker(t, c) })
	}
}

func TestBlocker6PunctuationHeadRefused(t *testing.T) {
	for _, c := range b6PunctHead {
		t.Run(c.name, func(t *testing.T) { runBlocker(t, c) })
	}
}

// ---------------------------------------------------------------------------
// Wiring. A guard that reads LineCtx.PrevLine is INERT unless ProcessFile
// actually supplies it, and an inert guard looks exactly like a working one in
// every unit test above. These run the whole-file pipeline on disk.
// ---------------------------------------------------------------------------

func TestBlocker3PrevLineIsWiredThroughProcessFile(t *testing.T) {
	root := repoRoot(t)
	for _, tc := range []struct {
		rel  string
		line int
	}{
		{"internal/application/parkedsensing/ferry.go", 100},
		{"internal/application/parkedsensing/scanner.go", 99},
		{"internal/application/system/gategraph/service.go", 741},
		{"internal/application/parkedsensing/yardqueue_test.go", 751},
		{"internal/application/trading/commands/run_stocker_coordinator_test.go", 338},
		{"internal/application/probebuy/guarded_probe_buyer.go", 148},
	} {
		before, after, _ := processOne(t, root, tc.rel, ScopeHybrid)
		if b, a := lineOf(before, tc.line), lineOf(after, tc.line); a != b {
			t.Errorf("%s:%d changed\n  before: %q\n  after:  %q", tc.rel, tc.line, b, a)
		}
	}
}

// The guard must be REACHABLE, not merely present: with no previous line the
// same text is a genuine leading tag and still strips.
func TestWrappedSentenceGateIsDifferential(t *testing.T) {
	in := "// sp-zml2u for why the two must be separate. It is fed from"
	wrapped := RewriteComment(in, nil, testOpts(), LineCtx{PrevLine: "// ... see LastDataAt, and"})
	if wrapped.Text != in {
		t.Errorf("wrapped context must refuse: %q", wrapped.Text)
	}
	fresh := RewriteComment(in, nil, testOpts(), LineCtx{PrevLine: "// A complete previous sentence."})
	if fresh.Text == in {
		t.Errorf("a sentence boundary above must still allow the leading tag to strip; got %q", fresh.Text)
	}
}

// ---------------------------------------------------------------------------
// Guard liveness: each new gate must REFUSE something and ADMIT something, or
// it is either inert or a blanket veto wearing a rule's name.
// ---------------------------------------------------------------------------

func TestNewGatesAdmitTheBenignShape(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{
			// A ';' that really is a list separator: nothing follows the id, so
			// no clause is fused.
			"clause_boundary_admits_terminal_id",
			"// the reserve floor is consulted every tick (unconditional; sp-1cbxz).",
			"// the reserve floor is consulted every tick (unconditional).",
		},
		{
			// The word after the id is a noun, not a finite verb.
			"subject_verb_admits_noun_head",
			"// Captured so the sp-ywh1 gate-reconcile widening can read backoff markers straight from",
			"// Captured so the gate-reconcile widening can read backoff markers straight from",
		},
		{
			// The survivor is ordinary prose, not an indexed sub-label.
			"sublabel_admits_plain_prose",
			"// sp-uohe money guards (all parametrized, RULINGS #5):",
			"// Money guards (all parametrized, RULINGS #5):",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := RewriteComment(c.in, nil, testOpts(), LineCtx{})
			if got.Text != c.want {
				t.Errorf("gate over-refuses\n  in:   %q\n  got:  %q\n  want: %q", c.in, got.Text, c.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Fixture provenance.
// ---------------------------------------------------------------------------

func TestBlockerFixturesAreReal(t *testing.T) {
	root := repoRoot(t)
	retired := 0
	for _, c := range allBlockerCases() {
		// A fixture with no file is a synthetic shape row, or one whose source
		// line a later layer rewrote. Its rule assertion still runs; only the
		// provenance claim is retired. Counted so silent rot stays visible.
		if c.file == "" {
			retired++
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join(root, c.file))
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(string(b), "\n")
			if c.line < 1 || c.line > len(lines) {
				t.Fatalf("%s has %d lines, fixture claims %d", c.file, len(lines), c.line)
			}
			if got := commentOf(t, c.file, c.line, lines[c.line-1]); got != c.in {
				t.Errorf("fixture drift at %s:%d\n  file: %q\n  test: %q", c.file, c.line, got, c.in)
			}
			if c.prev == "" {
				return
			}
			if c.line < 2 {
				t.Fatalf("%s:%d has no previous line", c.file, c.line)
			}
			if got := commentOf(t, c.file, c.line-1, lines[c.line-2]); got != c.prev {
				t.Errorf("prev-line drift at %s:%d\n  file: %q\n  test: %q", c.file, c.line-1, got, c.prev)
			}
		})
	}
	t.Logf("provenance retired for %d fixture(s) whose source a later layer rewrote", retired)
}

// commentOf strips a fixture line's indentation, so a fixture is the comment
// node text ProcessFile actually hands the rules -- never the raw source line.
func commentOf(t *testing.T, file string, line int, raw string) string {
	t.Helper()
	i := strings.Index(raw, "//")
	if i < 0 {
		t.Fatalf("%s:%d is not a // comment: %q", file, line, raw)
	}
	return raw[i:]
}

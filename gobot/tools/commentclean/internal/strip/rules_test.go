package strip

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureIsReal reports whether want is a comment line of file, now or in git
// history. A real fixture drifts as the corpus is refactored; one that never
// existed is a typo and must still fail.
func fixtureIsReal(root, file, want string) bool {
	if b, err := os.ReadFile(filepath.Join(root, file)); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if i := strings.Index(l, "//"); i >= 0 && l[i:] == want {
				return true
			}
		}
	}
	out, err := exec.Command("git", "-C", root, "log", "--oneline", "-S", want, "--", "./"+file).Output()
	return err == nil && len(bytes.TrimSpace(out)) > 0
}

// litSet is a tiny helper so a table row can declare the literal-cohabiting ids
// it must be protected by without constructing a map inline.
func litSet(ids ...string) map[string]bool {
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

func testOpts() Options { return Options{MaxPasses: 8} }

// ---------------------------------------------------------------------------
// Fixture provenance.
//
// Every `in` string below is a REAL line in the target worktree. TestFixturesAreReal
// re-reads each one from disk and fails if a fixture was retyped rather than
// extracted -- a mistyped fixture returns a plausible WRONG answer forever.
// ---------------------------------------------------------------------------

type ruleCase struct {
	name string
	file string // repo-relative source of `in`, "" for synthetic shape rows
	line int
	in   string
	want string   // expected comment text after rewriting
	lits []string // ids that cohabit with a string literal in scope
	rule string   // expected rule id, "" when the case must not change
	skip string   // expected skip id for an unchanged case, "" if unchecked
}

var positiveCases = []ruleCase{
	{
		name: "T01_R7_paren_sole_colon_follows",
		file: "cmd/spacetraders-daemon/deploy_guard.go", line: 29,
		in:   "// cold-boot path (sp-7pri): captain_events.player_id is an FK onto players.id,",
		want: "// cold-boot path: captain_events.player_id is an FK onto players.id,",
		rule: "R7-PAREN-SOLE",
	},
	{
		name: "T02_R5a_lead_colon_capitalises",
		file: "cmd/spacetraders-daemon/main.go", line: 197,
		in:   "// sp-oszc: cache Get Agent (the #2 API consumer) with a short TTL. Every",
		want: "// Cache Get Agent (the #2 API consumer) with a short TTL. Every",
		rule: "R5a-LEAD-COLON",
	},
	{
		// Two passes with two DIFFERENT rules: R7 removes the parenthetical,
		// which is what turns the remaining "sp-461l:" into a lead-colon tag.
		// Simultaneous span application cannot reach this.
		name: "T04_fixpoint_R7_then_R5a",
		file: "internal/adapters/cli/tour_report_test.go", line: 235,
		in:   "// sp-461l (epic sp-g9td): the graduation gate's tour $/hr now comes from the transactions-cash",
		want: "// The graduation gate's tour $/hr now comes from the transactions-cash",
		rule: "R7-PAREN-SOLE",
	},
	{
		name: "T05_R11_paren_list_back",
		file: "cmd/spacetraders-daemon/main.go", line: 1008,
		in:   "// DATA/INCOME cold-start window (unconditional, sp-1cbxz).",
		want: "// DATA/INCOME cold-start window (unconditional).",
		rule: "R11-PAREN-LIST-BACK",
	},
	{
		name: "T07_R8_lead_paren_no_capitalisation",
		file: "cmd/spacetraders-daemon/main.go", line: 1126,
		in:   "// (sp-iupr) reconciles against, so the watchdog re-mans a fully-manned-but-silent standing",
		want: "// reconciles against, so the watchdog re-mans a fully-manned-but-silent standing",
		rule: "R8-LEAD-PAREN",
	},
	{
		name: "T08_R5b_lead_paren_colon",
		file: "internal/adapters/cli/tour_report_baseline_test.go", line: 16,
		in:   "// (sp-lgnh): with tour-tagged and non-tour trade rows both present, the baseline",
		want: "// With tour-tagged and non-tour trade rows both present, the baseline",
		rule: "R5b-LEAD-PAREN-COLON",
	},
	{
		name: "T09_R14_bare_attributive",
		file: "", line: 0,
		in:   "// Captured so the sp-ywh1 gate-reconcile widening can read backoff markers straight from",
		want: "// Captured so the gate-reconcile widening can read backoff markers straight from",
		rule: "R14-BARE-ATTRIB",
	},
	{
		name: "T10_R5c_exported_field_doc",
		file: "internal/application/contract/types/contract_types.go", line: 175,
		in:   "// sp-uohe money guards (all parametrized, RULINGS #5):",
		want: "// Money guards (all parametrized, RULINGS #5):",
		rule: "R5c-LEAD-SPACE",
	},
	{
		name: "T11_R16_bare_dash_terminal",
		file: "internal/adapters/grpc/orphaned_container_reap.go", line: 3,
		in:   "// orphaned_container_reap.go — sp-h8mbb: the single seam that terminalizes a container whose",
		want: "// orphaned_container_reap.go: the single seam that terminalizes a container whose",
		rule: "R16-BARE-DASH",
	},
	{
		name: "T12_R12_paren_dash_terminal",
		file: "internal/application/bootstrap/commands/run_bootstrap_income.go", line: 76,
		in:   "// probes≥target (the frigate juggles buy-probes-first, then earn — sp-t39j) AND the frigate not",
		want: "// probes≥target (the frigate juggles buy-probes-first, then earn) AND the frigate not",
		rule: "R12-PAREN-DASH",
	},
	{
		name: "T13_R17_bare_list_back",
		file: "internal/adapters/grpc/container_runner_shipless_completion_test.go", line: 73,
		in:   "// before the factory ops were retired, sp-hoj8u — the construction drain is the surviving",
		want: "// before the factory ops were retired — the construction drain is the surviving",
		rule: "R17-BARE-LIST-BACK",
	},
	{
		// A12: the single deliberate override of a recon preserve case. This test
		// exists so the override is VISIBLE and revertible, not incidental.
		name: "T14_R7_st_drm_8_deliberate_override",
		file: "internal/adapters/grpc/ship_state_scheduler.go", line: 21,
		in:   "// INVARIANT (st-drm.8): in any digital-twin stack, the twin's TWIN_MIN_TRAVEL_MS travel floor",
		want: "// INVARIANT: in any digital-twin stack, the twin's TWIN_MIN_TRAVEL_MS travel floor",
		rule: "R7-PAREN-SOLE",
	},
}

var negativeCases = []ruleCase{
	// -----------------------------------------------------------------------
	// Orphaned sub-labels. These three shipped as POSITIVE cases and were the
	// blocker: the surviving "L2", "C2c", "invariant" is a cross-reference
	// whose only antecedent was the citation deleted in front of it. No prose
	// word is lost, so the prose-word bag, the AST hash and the defect regexes
	// all stay green while the reference stops resolving.
	// -----------------------------------------------------------------------
	{
		name: "T03_indexed_sublabel_L2_must_skip",
		file: "internal/application/trading/commands/run_arb_coordinator.go", line: 467,
		in:   "// sp-78ai L2: convert this leg's PLANNED absorption hold into an EXECUTED recovery",
		skip: "S-SUBLABEL",
	},
	{
		name: "T04b_bracket_wide_sublabel_C2c_must_skip",
		file: "cmd/spacetraders-daemon/main.go", line: 674,
		in:   "// Live per-park DEMAND weights (sp-5rakx/sp-bu6ma, epic sp-9le3x C2c): the coordinator",
		skip: "S-SUBLABEL",
	},
	{
		name: "T06_definite_description_must_skip",
		file: "cmd/spacetraders-daemon/main.go", line: 1094,
		in:   "// Manning stays in-system only (the sp-qxa4 invariant); repositioning just moves the",
		skip: "S-SUBLABEL",
	},
	{
		name: "N03_pre_compound",
		file: "internal/adapters/expansion/dark_market_scanner.go", line: 25,
		in:   "// system (byte-identical to pre-sp-gucu).",
		skip: "S-COMPOUND",
	},
	{
		name: "N03b_elided_list_tail_bracket_initial",
		file: "internal/application/manufacturing/commands/run_construction_coordinator_drain_hang_test.go", line: 28,
		in:   "// it), and a normal delivery still advances (sp-9ptm/2me2/yfzi intact).",
		skip: "S-COMPOUND",
	},
	{
		name: "N03c_elided_list_tail_attributive",
		file: "internal/application/manufacturing/services/input_price_ceiling.go", line: 135,
		in:   "// mode this is byte-identical to the sp-a5j7/hzz5 cross-market backstop.",
		skip: "S-COMPOUND",
	},
	{
		name: "N04a_cross_line_open_paren",
		file: "cmd/spacetraders-daemon/main.go", line: 1778,
		in:   "// Wire the tour coordinator's haul-to-storage pre-positioning subsystem (sp-dchv",
		skip: "S-CROSSLINE",
	},
	{
		name: "N04b_cross_line_open_paren_second_instance",
		file: "internal/adapters/grpc/opportunity_relocator_ports.go", line: 152,
		in:   "// ObserveHull re-reads ONE hull's live protection facts for the actuation-time re-check (sp-x2jr6",
		skip: "S-CROSSLINE",
	},
	{
		name: "N05_head_punctuation_continuation",
		file: "cmd/spacetraders-daemon/main.go", line: 1334,
		in:   "// (sp-zvywu). Same instance for every player — see its construction above.",
		skip: "S-HEADPUNCT",
	},
	{
		name: "N06_governed_object",
		file: "cmd/spacetraders-daemon/main.go", line: 1460,
		in:   "// scanner (proven by sp-mtvg). It REUSES the daemon's already-built collaborators (design",
		skip: "S-GOVERNED",
	},
	{
		name: "N07_possessive",
		file: "cmd/spacetraders-daemon/main.go", line: 1140,
		in:   "// `spacetraders tune scoutpost ...` lands on the next tick with no restart. sp-u8jc's two knobs",
		skip: "S-POSSESSIVE",
	},
	{
		name: "N01a_contract_table_header_literal_guard",
		file: "internal/adapters/grpc/command_factory_registry.go", line: 273,
		in:   "// per-type semantics table (sp-7yej invariants 3+4). Every container type the",
		lits: []string{"sp-7yej"},
		skip: "S-TOKEN-LITERAL",
	},
	{
		name: "N09a_cross_file_invariant_vocabulary",
		file: "internal/adapters/grpc/container_runner.go", line: 431,
		in:   "// ITERATION SEMANTICS (sp-7yej invariant 3): the loop below is the RUNNER-LOOP",
		lits: []string{"sp-7yej"},
		skip: "S-TOKEN-LITERAL",
	},
	{
		name: "N09b_cross_file_invariant_vocabulary_second",
		file: "internal/adapters/grpc/container_runner.go", line: 873,
		in:   "// Honest completion (sp-7yej invariant 2): a response that implements",
		lits: []string{"sp-7yej"},
		skip: "S-TOKEN-LITERAL",
	},
	{
		name: "N10_non_test_literal_repo_wide_reach",
		file: "internal/adapters/grpc/bootstrap_ports.go", line: 271,
		in:   "// Contract graduation (sp-difa.1): the durable per-player era-scoped flag that gates the whole",
		lits: []string{"sp-difa.1"},
		skip: "S-TOKEN-LITERAL",
	},
	{
		name: "N02a_emitted_literal_contract",
		file: "internal/adapters/grpc/container_ops_depot_launch.go", line: 702,
		in:   "// Honest reason (sp-gvvph): a command-frigate eviction is RULINGS #7 (the flagship is never a depot",
		lits: []string{"sp-gvvph"},
		skip: "S-TOKEN-LITERAL",
	},
	{
		name: "N02b_emitted_literal_contract_slash_pair",
		file: "", line: 0,
		in:   "// hull), a DIFFERENT cause than the sp-fihvy/sp-fis8y home-reachability eviction — so name it as such",
		lits: []string{"sp-fihvy", "sp-fis8y"},
		skip: "S-TOKEN-LITERAL",
	},
	{
		name: "N11a_citation_preamble_absorbed_not_orphaned",
		file: "internal/adapters/grpc/depot_migration.go", line: 3,
		in:   "// DepotMigrationRunbook is the operator runbook (bead sp-u9xa) for migrating a player",
		lits: []string{"sp-u9xa"},
		skip: "S-TOKEN-LITERAL",
	},
	{
		name: "N11b_citation_preamble_beads_list",
		file: "internal/adapters/cli/depot.go", line: 15,
		in:   "// (beads sp-u9xa, sp-38xc). A contract depot localizes the contract-fulfilment supply",
		lits: []string{"sp-u9xa"},
		skip: "S-TOKEN-LITERAL",
	},
	{
		// T18. Without the TERMINAL restriction on the dash rule this becomes
		// "// netting proved telemetry netting read ~2x inflated" -- a SILENT change
		// of grammatical subject. This is the corruption A7 was adjudicated to stop.
		// The subject-verb gate now refuses this one EARLIER than the dash rule
		// could, which is the generalisation A7 stopped short of. T18b keeps the
		// dash rule itself under test.
		name: "T18_non_terminal_dash_must_skip",
		file: "internal/adapters/cli/tour_report.go", line: 111,
		in:   "// netting — sp-rd21 proved telemetry netting read ~2x inflated (dropped buy legs), so a",
		skip: "S-SUBJECT-VERB",
	},
	{
		// A NON-verb follows the id, so only S-DASH-NONTERMINAL can refuse it.
		// Without this row the A7 adjudication would be untested.
		name: "T18b_dash_rule_still_refuses_alone",
		file: "internal/adapters/grpc/orphaned_container_reap_test.go", line: 3,
		in:   "// orphaned_container_reap_test.go — sp-h8mbb daemon-side cover for ReapOrphanedContainer.",
		skip: "S-DASH-NONTERMINAL",
	},
	{
		// T09 companion: head-noun position. The id is followed by '.', not by a
		// lowercase word, so R14's sandwich test must refuse it.
		name: "T09c_bare_head_noun_not_attributive",
		in:   "// the watchdog reconciles against the sp-iupr.",
		skip: "S-BARE-OTHER",
	},
	{
		// A10: R7 must run BEFORE the governance gate but carry its own governor
		// check, or "proven by (sp-mtvg)." reduces to "proven by ." .
		name: "N06b_orphan_governor_blocks_R7",
		in:   "// the scanner is proven by (sp-mtvg). It REUSES the collaborators",
		skip: "S-ORPHAN-GOVERNOR",
	},
	{
		name: "S_QUOTED_single_quote_left",
		in:   "// the reason string is 'sp-abcd' as persisted",
		skip: "S-QUOTED",
	},
}

func runCase(t *testing.T, c ruleCase) {
	t.Helper()
	got := RewriteComment(c.in, litSet(c.lits...), testOpts(), LineCtx{})
	want := c.want
	if want == "" {
		want = c.in
	}
	if got.Text != want {
		t.Errorf("rewrite mismatch\n  in:   %q\n  got:  %q\n  want: %q", c.in, got.Text, want)
	}
	if c.rule != "" {
		if len(got.Rules) == 0 || got.Rules[0] != c.rule {
			t.Errorf("rule mismatch: got %v, want first=%s", got.Rules, c.rule)
		}
	}
	if c.skip != "" {
		found := false
		for _, s := range got.Skips {
			if s.Reason == c.skip {
				found = true
			}
		}
		if !found {
			t.Errorf("skip mismatch: got %+v, want reason %s", got.Skips, c.skip)
		}
		if len(got.Rules) != 0 {
			t.Errorf("negative case must apply no rule, got %v", got.Rules)
		}
	}
}

func TestPositiveRules(t *testing.T) {
	for _, c := range positiveCases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

func TestNegativeCasesArePassThrough(t *testing.T) {
	for _, c := range negativeCases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// T04's second pass. The fixpoint sequencing is load-bearing: removing the
// parenthetical is what EXPOSES the lead-colon tag, and simultaneous span
// application cannot reach that.
func TestT04NeedsTwoPasses(t *testing.T) {
	in := "// sp-461l (epic sp-g9td): the graduation gate's tour $/hr now comes from the transactions-cash"
	got := RewriteComment(in, nil, testOpts(), LineCtx{})
	if len(got.Rules) != 2 || got.Rules[0] != "R7-PAREN-SOLE" || got.Rules[1] != "R5a-LEAD-COLON" {
		t.Fatalf("want [R7-PAREN-SOLE R5a-LEAD-COLON], got %v", got.Rules)
	}
	one := RewriteComment(in, nil, Options{MaxPasses: 1}, LineCtx{})
	if one.Text == got.Text {
		t.Errorf("a single pass must NOT reach the fixpoint result; got %q", one.Text)
	}
}

// R6-PREAMBLE applies ZERO times on the current corpus: every surviving
// "(epic <id> <x>)" now ends in an indexed sub-label, which the sublabel gate
// refuses first. The rule is still reachable, and this is the shape that
// reaches it -- without this test a later edit could break it invisibly.
func TestR6PreambleIsStillReachable(t *testing.T) {
	in := "// the widening (epic sp-abcd rate limiter) applies at the port boundary"
	got := RewriteComment(in, nil, testOpts(), LineCtx{})
	if want := "// the widening (rate limiter) applies at the port boundary"; got.Text != want {
		t.Fatalf("got %q, want %q", got.Text, want)
	}
	if len(got.Rules) != 1 || got.Rules[0] != "R6-PREAMBLE" {
		t.Errorf("want [R6-PREAMBLE], got %v", got.Rules)
	}
}

// ---------------------------------------------------------------------------
// T15 -- word-boundary regression. The 1,346-occurrence / 277-pseudo-id false
// positive class that a missing leading \b admits.
// ---------------------------------------------------------------------------

func TestT15WordBoundaryRejectsEnglishCompounds(t *testing.T) {
	compounds := []string{
		"lowest-effort", "latest-write-wins", "worst-case", "last-resort",
		"cost-first", "highest-completion", "fastest-resort", "test-only",
	}
	for _, w := range compounds {
		t.Run(w, func(t *testing.T) {
			line := "// the " + w + " path is chosen here"
			if m := IDPattern.FindAllString(line, -1); len(m) != 0 {
				t.Errorf("%q must not match inside %q, got %v", IDPattern.String(), w, m)
			}
			if got := RewriteComment(line, nil, testOpts(), LineCtx{}); got.Text != line {
				t.Errorf("line changed: %q -> %q", line, got.Text)
			}
		})
	}
}

func TestT15UppercaseIdsNeverMatch(t *testing.T) {
	for _, w := range []string{"SP-ABCD", "ST-DRM", "Sp-abcd"} {
		if m := IDPattern.FindAllString("// see "+w+" here", -1); len(m) != 0 {
			t.Errorf("case-sensitive pattern matched %q: %v", w, m)
		}
	}
}

// ---------------------------------------------------------------------------
// T16 -- veto liveness. A veto clause that always matches is silently INERT;
// this is the only test that catches it. (A8: "^//\s*[^\w\t]" matches every
// normal comment because \s* takes zero chars and [^\w\t] eats the space.)
// ---------------------------------------------------------------------------

func TestT16VetoV7IsLive(t *testing.T) {
	if V7HeadPunct.MatchString("// ordinary comment text") {
		t.Error("V7 must NOT match an ordinary comment -- an always-matching clause disables the differential veto entirely")
	}
	if !V7HeadPunct.MatchString("//. stranded") {
		t.Error("V7 must match a stranded leading punctuation body")
	}
	if !V7HeadPunct.MatchString("// : dangling colon") {
		t.Error("V7 must match a dangling leading colon")
	}
}

// R5a runs BEFORE the head-punctuation gate -- it has to, since "id:" IS head
// punctuation -- so it is the one rule that can still hand V7 a candidate. This
// is a real corpus line: the survivor would open on a backtick.
func TestT16VetoV7FiresOnRealCandidate(t *testing.T) {
	in := "// sp-86yb: `operations stop` (e.g. a gas coordinator `--gas` stop) killed the"
	got := RewriteComment(in, nil, testOpts(), LineCtx{})
	if got.Text != in {
		t.Errorf("expected V7 to veto, got %q", got.Text)
	}
	if len(got.Vetoes) == 0 || got.Vetoes[0].ID != "V7" {
		t.Errorf("expected a V7 veto, got %+v", got.Vetoes)
	}
}

// The shape V7 used to catch after the fact is now refused before any rule
// runs, so the line never enters the veto path at all. Both mechanisms must
// stay live: this one is cheaper and names the reason.
func TestPunctuationHeadIsRefusedBeforeTheVeto(t *testing.T) {
	in := "// sp-abcd - the rest of the sentence"
	got := RewriteComment(in, nil, testOpts(), LineCtx{})
	if got.Text != in {
		t.Errorf("line must stand, got %q", got.Text)
	}
	if len(got.Vetoes) != 0 {
		t.Errorf("the gate must pre-empt the veto, got %+v", got.Vetoes)
	}
	if len(got.Skips) == 0 || got.Skips[0].Reason != "S-HEADPUNCT" {
		t.Errorf("want S-HEADPUNCT, got %+v", got.Skips)
	}
}

// ---------------------------------------------------------------------------
// T17 -- derivative-token integrity: matched as ONE whole token, then SKIPPED.
// Never partially matched into "-prevention".
// ---------------------------------------------------------------------------

func TestT17DerivativeTokensWholeThenSkipped(t *testing.T) {
	for _, id := range []string{"sp-lybx-prevention", "sp-ratelimit-prio", "sp-86vb-style", "st-wisp-2h6r5"} {
		t.Run(id, func(t *testing.T) {
			line := "// the " + id + " thing"
			m := IDPattern.FindAllString(line, -1)
			if len(m) != 1 || m[0] != id {
				t.Fatalf("want one whole-token match %q, got %v", id, m)
			}
			got := RewriteComment(line, nil, testOpts(), LineCtx{})
			if got.Text != line {
				t.Errorf("derivative token must be skipped: %q -> %q", line, got.Text)
			}
			if len(got.Skips) == 0 || got.Skips[0].Reason != "S-TOKEN-DERIV" {
				t.Errorf("want S-TOKEN-DERIV, got %+v", got.Skips)
			}
		})
	}
}

func TestIDListIsOneUnit(t *testing.T) {
	line := "// (beads sp-u9xa, sp-38xc). rest"
	m := IDListPattern.FindAllString(line, -1)
	if len(m) != 1 || m[0] != "sp-u9xa, sp-38xc" {
		t.Fatalf("id-list must be one unit, got %v", m)
	}
}

// ---------------------------------------------------------------------------
// cap1 -- the only non-deletion operation.
// ---------------------------------------------------------------------------

func TestCap1Refusals(t *testing.T) {
	cases := []struct{ in, want string }{
		{"cache the thing", "Cache the thing"},
		{"L2: convert", "L2: convert"},               // not [a-z] first
		{"maybeBuy the thing", "maybeBuy the thing"}, // interior uppercase => identifier
		{"snake_case value", "snake_case value"},     // contains '_'
		{"doThing() runs", "doThing() runs"},         // call
		{"foo(x) runs", "foo(x) runs"},               // call
		{"pkg.Sym is used", "pkg.Sym is used"},       // selector
		{"", ""},
		{"123 things", "123 things"},
	}
	for _, c := range cases {
		if got := cap1(c.in); got != c.want {
			t.Errorf("cap1(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Structural clamps (6.5).
// ---------------------------------------------------------------------------

func TestBodyPrefixIsUnreachable(t *testing.T) {
	for _, c := range append(append([]ruleCase{}, positiveCases...), negativeCases...) {
		got := RewriteComment(c.in, litSet(c.lits...), testOpts(), LineCtx{})
		bs := bodyStart(c.in)
		if !strings.HasPrefix(got.Text, c.in[:bs]) {
			t.Errorf("%s: body prefix %q destroyed: %q", c.name, c.in[:bs], got.Text)
		}
	}
}

func TestNoByteIsEverInserted(t *testing.T) {
	for _, c := range positiveCases {
		got := RewriteComment(c.in, litSet(c.lits...), testOpts(), LineCtx{})
		if len(got.Text) >= len(c.in) {
			t.Errorf("%s: result must be strictly shorter (pure deletion): %d -> %d", c.name, len(c.in), len(got.Text))
		}
	}
}

func TestNoDefectRegexIntroduced(t *testing.T) {
	for _, c := range append(append([]ruleCase{}, positiveCases...), negativeCases...) {
		got := RewriteComment(c.in, litSet(c.lits...), testOpts(), LineCtx{})
		for _, d := range DefectChecks {
			if d.Re.MatchString(got.Text) && !d.Re.MatchString(c.in) {
				t.Errorf("%s: introduced defect %s (%s) in %q", c.name, d.ID, d.Re.String(), got.Text)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Comment kinds skipped entirely (3).
// ---------------------------------------------------------------------------

func TestG1BlockCommentsSkipped(t *testing.T) {
	in := "/* sp-abcd: a block comment */"
	if r := ClassifyComment(in); r != "S-BLOCK" {
		t.Errorf("want S-BLOCK, got %q", r)
	}
}

func TestG2DirectivesSkipped(t *testing.T) {
	for _, in := range []string{
		"//go:generate sp-abcd",
		"//nolint:all // sp-abcd",
		"//line foo.go:1",
		"//export Foo",
		"//+build linux",
	} {
		if r := ClassifyComment(in); r != "S-DIRECTIVE" {
			t.Errorf("%q: want S-DIRECTIVE, got %q", in, r)
		}
	}
}

func TestG3FrozenTableSkipped(t *testing.T) {
	frozen := "//\t                                                            (sp-perx); -1 uses 2q2o backoff"
	if r := ClassifyComment(frozen); r != "S-FROZEN-TABLE" {
		t.Errorf("want S-FROZEN-TABLE, got %q", r)
	}
	// An ordinary tab-indented comment with no column run is NOT frozen.
	if r := ClassifyComment("//\tordinary continuation"); r == "S-FROZEN-TABLE" {
		t.Error("plain tab comment must not be treated as a frozen table")
	}
}

// The 8 prefix-dropped tags in the contract table must stay invisible to the
// pattern. Widening to bare 4-char alphanumerics would shred prose tree-wide.
func TestPrefixDroppedTagsNeverMatch(t *testing.T) {
	for _, tag := range []string{"(cxpq)", "(s232)", "(f5pr)", "2q2o", "(dchv)", "(tgp5)", "(perx)"} {
		if m := IDPattern.FindAllString("// backoff "+tag+" here", -1); len(m) != 0 {
			t.Errorf("prefix-dropped tag %q must not match: %v", tag, m)
		}
	}
}

// ---------------------------------------------------------------------------
// Fixture provenance guard.
// ---------------------------------------------------------------------------

func repoRoot(t *testing.T) string {
	t.Helper()
	d, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
		p := filepath.Dir(d)
		if p == d {
			t.Fatal("go.mod not found walking up from cwd")
		}
		d = p
	}
}

func TestFixturesAreReal(t *testing.T) {
	root := repoRoot(t)
	for _, c := range append(append([]ruleCase{}, positiveCases...), negativeCases...) {
		if c.file == "" {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			if !fixtureIsReal(root, c.file, c.in) &&
				!(c.want != "" && fixtureIsReal(root, c.file, c.want)) {
				t.Errorf("fixture was never a line of %s\n  pre : %q\n  post: %q",
					c.file, c.in, c.want)
			}
		})
	}
}

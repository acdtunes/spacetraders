package commands

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ---- depth-policy fakes + helpers ------------------------------------------

// fakeObjective is the deep-resource (heavy-yard) objective signal the depth bias reads
// (sp-rjgr §4). Injected at the port boundary so the POLICY's response to the signal —
// shift toward depth when unmet, relax when met — is tested without the shipyard-inventory
// or autosizer adapters behind it.
type fakeObjective struct {
	shortfall int
	yardKnown bool
	readable  bool
	err       error
	calls     int
}

func (f *fakeObjective) HeavyYardObjective(_ context.Context, _ int) (int, bool, bool, error) {
	f.calls++
	return f.shortfall, f.yardKnown, f.readable, f.err
}

// declaredSystems reads the systems declared through the fakePostRepo (the driven-port
// boundary the depth policy is observed at).
func declaredSystems(pr *fakePostRepo) []string {
	out := make([]string, 0, len(pr.upserts))
	for _, p := range pr.upserts {
		out = append(out, p.SystemSymbol)
	}
	return out
}

func containsSystem(systems []string, want string) bool {
	for _, s := range systems {
		if s == want {
			return true
		}
	}
	return false
}

func intersectSystems(got, universe []string) []string {
	out := []string{}
	for _, u := range universe {
		if containsSystem(got, u) {
			out = append(out, u)
		}
	}
	return out
}

// ---- depth drive -----------------------------------------------------------

// Depth-drive proof (sp-rjgr test 1): with a depth split > 0, at least one pathfinder
// targets the DEEPEST-reachable virgin (a hop-2 edge target) EVEN WHILE hop-1 virgins
// remain — the outward drive pure BFS never has (today BFS scores hop-1 above hop-2 every
// time, so a hop-2 is declared ~never). Breadth still declares its near-ring head; depth
// additionally punches to the deep virgin.
func TestFrontier_Depth_TargetsDeepestReachableVirgin(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	pr := &fakePostRepo{}
	fr := &fakeFleetRepo{idle: []*navigation.Ship{newProbe(t, "P1", "X1-HOME-A1")}} // supply covers → isolate declaration
	lr := &fakeLedgerRepo{}
	h := newHandler(pr, fr, lr, clock)
	h.SetExpansionScanner(&fakeScanner{candidates: []ExpansionCandidate{
		{SystemSymbol: "X1-N1", Hops: 1, KnownMarkets: 0, Charted: false, BranchRoot: "X1-N1"},  // near-ring virgin
		{SystemSymbol: "X1-N2", Hops: 1, KnownMarkets: 0, Charted: false, BranchRoot: "X1-N2"},  // near-ring virgin
		{SystemSymbol: "X1-P", Hops: 1, KnownMarkets: 2, Charted: true, BranchRoot: "X1-P"},     // charted parent (breadth head)
		{SystemSymbol: "X1-DEEP", Hops: 2, KnownMarkets: 0, Charted: false, BranchRoot: "X1-P"}, // the deep edge BFS never picks
	}})

	require.NoError(t, h.ReconcileOnce(context.Background(), testCmd()))

	declared := declaredSystems(pr)
	require.True(t, containsSystem(declared, "X1-DEEP"),
		"the depth slice declares the deepest-reachable virgin even while hop-1 virgins remain — the outward drive pure BFS lacks")
	require.True(t, containsSystem(declared, "X1-P"),
		"the breadth slice still declares its near-ring head — market coverage continues (no regression)")
}

// Mutation guard (sp-rjgr): a 0% depth split (breadth 100%) is PURE BFS — no pathfinder is
// dispatched, so the deepest virgin is never targeted. Byte-identical scenario to the
// depth-drive test above; only the split flips. Proves the split is load-bearing.
func TestFrontier_Depth_ZeroDepthSplitIsPureBFS(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	pr := &fakePostRepo{}
	fr := &fakeFleetRepo{idle: []*navigation.Ship{newProbe(t, "P1", "X1-HOME-A1")}}
	lr := &fakeLedgerRepo{}
	h := newHandler(pr, fr, lr, clock)
	h.SetExpansionScanner(&fakeScanner{candidates: []ExpansionCandidate{
		{SystemSymbol: "X1-P", Hops: 1, KnownMarkets: 2, Charted: true, BranchRoot: "X1-P"},
		{SystemSymbol: "X1-DEEP", Hops: 2, KnownMarkets: 0, Charted: false, BranchRoot: "X1-P"},
	}})

	cmd := testCmd()
	cmd.BreadthFractionPercent = 100 // 0% depth — pure BFS. No objective reader wired → nothing resurrects depth.

	require.NoError(t, h.ReconcileOnce(context.Background(), cmd))

	declared := declaredSystems(pr)
	require.False(t, containsSystem(declared, "X1-DEEP"),
		"0% depth split ⇒ pure BFS ⇒ the deepest virgin is never targeted (depth drive is load-bearing)")
	require.True(t, containsSystem(declared, "X1-P"),
		"breadth (pure BFS) still declares its scored head")
}

// ---- distinct bearings -----------------------------------------------------

// Distinct-branch fan-out (sp-rjgr test 2): multiple pathfinders take DISTINCT branches —
// never two down the same corridor (a heavy yard could be any direction). Branch A holds two
// deep virgins, branch B one; with two pathfinders the depth slice spreads across A and B,
// not both down A.
func TestFrontier_Depth_PathfindersFanAcrossDistinctBranches(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	pr := &fakePostRepo{}
	fr := &fakeFleetRepo{idle: []*navigation.Ship{newProbe(t, "P1", "X1-HOME-A1")}}
	lr := &fakeLedgerRepo{}
	h := newHandler(pr, fr, lr, clock)
	h.SetExpansionScanner(&fakeScanner{candidates: []ExpansionCandidate{
		// near-ring charted parents (breadth fodder — clearly outscore the deep virgins so breadth
		// takes a NEAR system, leaving the deep edge to the depth slice)
		{SystemSymbol: "X1-BR-A", Hops: 1, KnownMarkets: 5, Charted: true, BranchRoot: "X1-BR-A"},
		{SystemSymbol: "X1-BR-B", Hops: 1, KnownMarkets: 5, Charted: true, BranchRoot: "X1-BR-B"},
		// branch A has TWO deep virgins; branch B has ONE
		{SystemSymbol: "X1-A-DEEP1", Hops: 2, Charted: false, BranchRoot: "X1-BR-A"},
		{SystemSymbol: "X1-A-DEEP2", Hops: 2, Charted: false, BranchRoot: "X1-BR-A"},
		{SystemSymbol: "X1-B-DEEP", Hops: 2, Charted: false, BranchRoot: "X1-BR-B"},
	}})

	cmd := testCmd()
	cmd.MaxDepthPathfinders = 2 // two concurrent pathfinders

	require.NoError(t, h.ReconcileOnce(context.Background(), cmd))

	declared := declaredSystems(pr)
	depthPicks := intersectSystems(declared, []string{"X1-A-DEEP1", "X1-A-DEEP2", "X1-B-DEEP"})
	require.Len(t, depthPicks, 2, "two depth pathfinders are dispatched")
	require.True(t, containsSystem(depthPicks, "X1-B-DEEP"),
		"branch B is covered — the depth slice does not bet everything on one corridor")
	require.False(t, containsSystem(depthPicks, "X1-A-DEEP1") && containsSystem(depthPicks, "X1-A-DEEP2"),
		"the two pathfinders take DISTINCT branches, never two down the same corridor")
}

// ---- objective-aware bias --------------------------------------------------

// Objective bias (sp-rjgr test 3): when the deep-resource objective is UNMET — heavy
// shortfall > 0 AND no heavy yard known — the split auto-shifts toward depth (punch outward
// to find the yard); once a yard is known (or there is no shortfall) it relaxes back to the
// baseline. Baseline is set to 0% depth (breadth 100) so the shift is the ONLY thing that can
// declare the deep virgin — a clean observable.
func TestFrontier_Depth_ObjectiveBiasShiftsTowardDepth(t *testing.T) {
	run := func(shortfall int, yardKnown bool) []string {
		clock := &shared.MockClock{CurrentTime: time.Now()}
		pr := &fakePostRepo{}
		fr := &fakeFleetRepo{idle: []*navigation.Ship{newProbe(t, "P1", "X1-HOME-A1")}}
		lr := &fakeLedgerRepo{}
		h := newHandler(pr, fr, lr, clock)
		h.SetExpansionScanner(&fakeScanner{candidates: []ExpansionCandidate{
			{SystemSymbol: "X1-NEAR", Hops: 1, KnownMarkets: 3, Charted: true, BranchRoot: "X1-NEAR"},
			{SystemSymbol: "X1-DEEP", Hops: 2, Charted: false, BranchRoot: "X1-NEAR"},
		}})
		h.SetDepthObjectiveReader(&fakeObjective{shortfall: shortfall, yardKnown: yardKnown, readable: true})
		cmd := testCmd()
		cmd.BreadthFractionPercent = 100 // baseline depth 0% → pure BFS unless the objective biases
		cmd.ObjectiveBiasPercent = 50    // unmet → depth 50%
		require.NoError(t, h.ReconcileOnce(context.Background(), cmd))
		return declaredSystems(pr)
	}

	require.True(t, containsSystem(run(2, false), "X1-DEEP"),
		"an unmet heavy-yard objective (shortfall>0, no yard) biases the split toward depth until a yard is found")
	require.False(t, containsSystem(run(2, true), "X1-DEEP"),
		"once a heavy yard is known the split relaxes back toward breadth")
	require.False(t, containsSystem(run(0, false), "X1-DEEP"),
		"no heavy shortfall ⇒ no depth bias (the objective is not unmet)")
}

// ---- max-depth cap ---------------------------------------------------------

// Max-depth cap (sp-rjgr test 4): the pathfinder never targets a virgin beyond the cap. With
// the cap at 3 the hop-5 virgin is out of bounds and the deepest WITHIN the cap is chosen;
// raise the cap and the hop-5 virgin becomes the deepest reachable and IS chosen — the cap is
// load-bearing.
func TestFrontier_Depth_MaxDepthCapBoundsPathfinder(t *testing.T) {
	run := func(maxDepthHops int) []string {
		clock := &shared.MockClock{CurrentTime: time.Now()}
		pr := &fakePostRepo{}
		fr := &fakeFleetRepo{idle: []*navigation.Ship{newProbe(t, "P1", "X1-HOME-A1")}}
		lr := &fakeLedgerRepo{}
		h := newHandler(pr, fr, lr, clock)
		h.SetExpansionScanner(&fakeScanner{candidates: []ExpansionCandidate{
			{SystemSymbol: "X1-NEAR", Hops: 1, KnownMarkets: 3, Charted: true, BranchRoot: "X1-NEAR"},
			{SystemSymbol: "X1-D2", Hops: 2, Charted: false, BranchRoot: "X1-NEAR"},
			{SystemSymbol: "X1-TOODEEP", Hops: 5, Charted: false, BranchRoot: "X1-NEAR"},
		}})
		cmd := testCmd()
		cmd.MaxDepthPathfinders = 1 // one pathfinder → it takes the single deepest-within-cap virgin
		cmd.MaxDepthHops = maxDepthHops
		require.NoError(t, h.ReconcileOnce(context.Background(), cmd))
		return declaredSystems(pr)
	}

	capped := run(3)
	require.False(t, containsSystem(capped, "X1-TOODEEP"),
		"the max-depth cap bounds the pathfinder — a virgin beyond the cap is never targeted")
	require.True(t, containsSystem(capped, "X1-D2"),
		"the deepest virgin WITHIN the cap is targeted")

	raised := run(8)
	require.True(t, containsSystem(raised, "X1-TOODEEP"),
		"raise the cap and the deeper virgin becomes reachable and is targeted — the cap is load-bearing")
}

// ---- tunables resolve live > launch > default ------------------------------

// Tunables (sp-rjgr test 6): the depth knobs resolve live-config over launch over documented
// default, the established sp-vwek/sp-0z7f precedence.
func TestFrontier_Depth_TunablesResolveLiveOverLaunchOverDefault(t *testing.T) {
	cmd := testCmd()
	cmd.BreadthFractionPercent = 70
	cmd.MaxDepthPathfinders = 2
	cmd.MaxDepthHops = 4
	cmd.ObjectiveBiasPercent = 20

	// No live snapshot → launch values apply.
	launch := resolveConfig(cmd, nil)
	require.Equal(t, 70, launch.BreadthFractionPercent, "launch value applies with no live snapshot")
	require.Equal(t, 2, launch.MaxDepthPathfinders)
	require.Equal(t, 4, launch.MaxDepthHops)
	require.Equal(t, 20, launch.ObjectiveBiasPercent)

	// A live snapshot overrides the launch values.
	live := resolveConfig(cmd, liveconfig.Snapshot{
		"breadth_fraction_percent": 80,
		"max_depth_pathfinders":    5,
		"max_depth_hops":           9,
		"objective_bias_percent":   35,
	})
	require.Equal(t, 80, live.BreadthFractionPercent, "live snapshot overrides launch")
	require.Equal(t, 5, live.MaxDepthPathfinders)
	require.Equal(t, 9, live.MaxDepthHops)
	require.Equal(t, 35, live.ObjectiveBiasPercent)

	// An empty snapshot → every knob falls to its documented default.
	def := resolveConfig(testCmd(), liveconfig.Snapshot{})
	require.Equal(t, defaultBreadthFractionPercent, def.BreadthFractionPercent, "empty snapshot → default")
	require.Equal(t, defaultMaxDepthPathfinders, def.MaxDepthPathfinders)
	require.Equal(t, defaultMaxDepthHops, def.MaxDepthHops)
	require.Equal(t, defaultObjectiveBiasPercent, def.ObjectiveBiasPercent)
}

package commands

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/system/gategraph"
	tradingsvc "github.com/andrescamacho/spacetraders-go/internal/application/trading/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// Far-sink capture. The tour horizon is built by gate-NEIGHBOUR expansion around the hull,
// so a rich sink that happens to sit further out is ABSENT from the snapshot rather than
// rejected — no pricing or hop-depth change can capture it, because the solver never sees
// it. These tests drive the admission producer (and the planForState seam it rides) and
// assert the exact set/rows/distances that reach the planner.
//
// The geometry is chosen so ADMISSION IS THE ONLY FREE VARIABLE: X1-FAR sits THREE gate
// hops from home, beyond the depth-2 candidate walk that produces the base set, and MID
// (the intermediate) carries no market so the profitable-edge shortlist cannot pull it in
// either. A plan containing X1-FAR therefore cannot have got there by any pre-existing path.

const (
	farHome = "X1-HOME"
	farN1   = "X1-N1"
	farMid  = "X1-MID"
	farSys  = "X1-FAR"
)

// farSinkChain wires HOME—N1—MID—FAR as a bidirectional gate chain, then appends any extra
// adjacency. FAR is 3 hops from HOME, 2 from N1 — outside the depth-2 candidate walk,
// inside the executor's 5-jump strict flight bound.
func farSinkChain(extra ...[2]string) *fakeGateGraph {
	edges := map[string][]system.GateEdge{}
	link := func(a, b string) {
		edges[a] = append(edges[a], system.GateEdge{ConnectedSystem: b})
		edges[b] = append(edges[b], system.GateEdge{ConnectedSystem: a})
	}
	link(farHome, farN1)
	link(farN1, farMid)
	link(farMid, farSys)
	for _, e := range extra {
		link(e[0], e[1])
	}
	return &fakeGateGraph{edges: edges}
}

// farSinkWorld is the market world: home exports GX cheaply, X1-FAR imports it at 30x.
// N1 and MID carry no market at all, so the base tour graph is exactly [HOME, N1] and the
// only value in the world is the one the horizon hides.
func farSinkWorld() *tourFixture {
	return &tourFixture{
		cargo: map[string]int{}, location: farHome + "-M", cargoCap: 100,
		markets: map[string][]string{
			farHome: {farHome + "-M"},
			farSys:  {farSys + "-W"},
		},
		ask: map[string]map[string]int{
			farHome + "-M": {"GX": 1000},
			farSys + "-W":  {"GX": 30500},
		},
		bid: map[string]map[string]int{
			farHome + "-M": {"GX": 900},
			farSys + "-W":  {"GX": 30000},
		},
		tv: map[string]map[string]int{
			farHome + "-M": {"GX": 100},
			farSys + "-W":  {"GX": 100},
		},
		tradeType:          map[string]map[string]string{farSys + "-W": {"GX": "IMPORT"}},
		activityByWaypoint: map[string]string{farSys + "-W": "WEAK"},
		neighbors:          map[string][]string{farHome: {farN1}},
	}
}

// gxSinkAt is the global best-sink scan's verdict: GX sells best in the named far system.
func gxSinkAt(waypoint, system string, bid int) *stubScanner {
	return &stubScanner{sinks: map[string]market.GlobalSinkResult{
		"GX": {WaypointSymbol: waypoint, SystemSymbol: system, Bid: bid},
	}}
}

// widenedFarCmd is the live shape: the per-pair hop matrix armed (MaxTourSystems > 2) and
// the candidate walk at depth 2 — the configuration under which X1-FAR is still invisible.
func widenedFarCmd() *RunTourCoordinatorCommand {
	return &RunTourCoordinatorCommand{PlayerID: 1, CandidateHopDepth: 2, MaxTourSystems: 5}
}

func farSinkHandler(t *testing.T, fx *tourFixture, graph *fakeGateGraph, scanner *stubScanner) *RunTourCoordinatorHandler {
	t.Helper()
	h := newCandidatesHandler(t, fx)
	h.SetGateGraph(graph)
	h.SetOutOfHorizonSinkScanner(scanner)
	return h
}

// baseSnapshotOver builds the in-scope snapshot exactly as planForState does, so the
// admission producer is fed the real thing rather than a hand-written stand-in.
func baseSnapshotOver(t *testing.T, h *RunTourCoordinatorHandler, allowed []string) []routing.TourGoodSnapshot {
	t.Helper()
	snap, _, err := tradingsvc.BuildTourSnapshot(context.Background(), h.marketRepo, h.waypointRepo,
		allowed, 1, h.clock.Now(), h.rankerAgeCaps)
	if err != nil {
		t.Fatalf("build base snapshot: %v", err)
	}
	return snap
}

func hasSinkRow(rows []routing.TourGoodSnapshot, system, good string) bool {
	for _, r := range rows {
		if r.System == system && r.Good == good && r.Bid > 0 {
			return true
		}
	}
	return false
}

// RED #1 — THE CAPTURE. A sink beyond the candidate horizon, previously absent from the
// snapshot, reaches the planner: X1-FAR is in the allowed set AND its 30,000-bid GX row is
// in the good universe the solver plans over. The control in the same test is the proof it
// was genuinely hidden: with the far-sink path unable to run (no scanner wired), the very
// same world produces a plan that has never heard of X1-FAR.
func TestPlanForState_AdmitsFarSinkBeyondCandidateHorizon(t *testing.T) {
	world := farSinkWorld()
	fake := &tourFakeRoutingClient{plans: []*routing.TourPlan{{Feasible: true}}}
	h := newTourHandler(t, world, fake, nil)
	h.SetGateGraph(farSinkChain())
	ship := routing.TourShipState{ShipSymbol: "F-1", CurrentWaypoint: farHome + "-M", CurrentSystem: farHome, HoldCapacity: 40}

	// CONTROL: no global sink scan wired → the horizon is exactly what it always was.
	if _, _, _, err := h.planForState(context.Background(), ship,
		h.tourSystemsFrom(context.Background(), farHome, widenedFarCmd()), widenedFarCmd(), tourPlanBudget{maxHops: 6, maxSpend: 1_000_000}); err != nil {
		t.Fatalf("planForState (control): %v", err)
	}
	if candidateSetHas(fake.allowedSystems[0], farSys) {
		t.Fatalf("control must not see %s (it is 3 gate hops out, beyond the depth-2 walk); got %v", farSys, fake.allowedSystems[0])
	}
	if hasSinkRow(fake.snapshots[0], farSys, "GX") {
		t.Fatalf("control snapshot must not carry the far GX sink; got %+v", fake.snapshots[0])
	}

	// CAPTURE: the same world, with the scan that already knows where the value is.
	h.SetOutOfHorizonSinkScanner(gxSinkAt(farSys+"-W", farSys, 30000))
	if _, _, _, err := h.planForState(context.Background(), ship,
		h.tourSystemsFrom(context.Background(), farHome, widenedFarCmd()), widenedFarCmd(), tourPlanBudget{maxHops: 6, maxSpend: 1_000_000}); err != nil {
		t.Fatalf("planForState (capture): %v", err)
	}
	if !candidateSetHas(fake.allowedSystems[1], farSys) {
		t.Fatalf("far sink %s must reach the planner's allowed set; got %v", farSys, fake.allowedSystems[1])
	}
	if !hasSinkRow(fake.snapshots[1], farSys, "GX") {
		t.Fatalf("the far GX sink row (bid 30000) must reach the planner's snapshot; got %+v", fake.snapshots[1])
	}
}

// RED #2 — THE INVARIANT THAT MATTERS. A sink the global scan ranks first is REFUSED at
// admission when it sits further from an already-admitted system than the bound the
// executor's own leg resolver flies (gategraph.MaxJumpPath). Admission and execution agree
// by construction: this is what a hull holding undeliverable cargo costs when they do not.
//
// The two cases differ ONLY in chain length, so distance — not price, freshness or wiring —
// is provably what refuses the sink.
func TestAdmitFarSinks_RejectsSinkBeyondExecutorFlightHorizon(t *testing.T) {
	for _, c := range []struct {
		name      string
		spacers   [][2]string
		hopsHome  int
		wantAdmit bool
	}{
		// HOME—N1—MID—FAR: FAR is 3 hops from home, well inside the 5-jump flight bound.
		{name: "within flight bound", hopsHome: 3, wantAdmit: true},
		// HOME—N1—MID—S1—S2—S3—FAR: FAR is 6 hops from home. The executor's strict resolver
		// refuses that leg, so admitting it would buy cargo it cannot deliver.
		{name: "beyond flight bound", hopsHome: 6,
			spacers: [][2]string{{farMid, "X1-S1"}, {"X1-S1", "X1-S2"}, {"X1-S2", "X1-S3"}, {"X1-S3", farSys}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			world := farSinkWorld()
			graph := farSinkChain(c.spacers...)
			if len(c.spacers) > 0 {
				// Break the short MID—FAR link so the only route is the long chain.
				graph.edges[farMid] = []system.GateEdge{{ConnectedSystem: farN1}, {ConnectedSystem: "X1-S1"}}
				graph.edges[farSys] = []system.GateEdge{{ConnectedSystem: "X1-S3"}}
			}
			h := farSinkHandler(t, world, graph, gxSinkAt(farSys+"-W", farSys, 30000))
			allowed := []string{farHome, farN1}

			got := h.admitFarSinks(context.Background(), widenedFarCmd(), allowed,
				baseSnapshotOver(t, h, allowed))

			if c.wantAdmit && !candidateSetHas(got.systems, farSys) {
				t.Fatalf("%s: a sink %d hops out is inside the executor's flight bound and must be admitted; got %v",
					c.name, c.hopsHome, got.systems)
			}
			if !c.wantAdmit && candidateSetHas(got.systems, farSys) {
				t.Fatalf("%s: a sink %d hops out is UNROUTABLE for the executor and must be refused at admission (the stranded-hull class); got %v",
					c.name, c.hopsHome, got.systems)
			}
		})
	}
}

// RED #3 — PER-ACTIVITY, NOT GLOBAL. Two sinks in the same far system, the same age and
// the same 90-minute haul away: the STRONG one (30-min fitted cap) is dead on arrival and
// excluded, the WEAK one (8-hour cap) is kept. A single global freshness horizon must
// either forfeit the WEAK majority or buy cargo into an expired STRONG sink; measuring
// each row against ITS OWN cap is what removes that choice.
func TestAdmitFarSinks_ExcludesRowsThatExpireInFlight(t *testing.T) {
	world := farSinkWorld()
	world.markets[farSys] = []string{farSys + "-W", farSys + "-S"}
	world.ask[farSys+"-S"] = map[string]int{"GS": 40500}
	world.bid[farSys+"-S"] = map[string]int{"GS": 40000}
	world.tv[farSys+"-S"] = map[string]int{"GS": 100}
	world.tradeType[farSys+"-S"] = map[string]string{"GS": "IMPORT"}
	world.activityByWaypoint[farSys+"-S"] = "STRONG"
	// Home also exports GS, so BOTH goods have a cheap in-scope source and both far sinks
	// are genuine hidden lanes — the exclusion cannot be an artefact of one being unsourced.
	world.ask[farHome+"-M"]["GS"] = 1000
	world.bid[farHome+"-M"]["GS"] = 900
	world.tv[farHome+"-M"]["GS"] = 100
	// Both far markets read at the SAME instant: age is held constant, activity is the variable.
	world.ageByWaypoint = map[string]time.Duration{farSys + "-W": 0, farSys + "-S": 0}

	scanner := &stubScanner{sinks: map[string]market.GlobalSinkResult{
		"GX": {WaypointSymbol: farSys + "-W", SystemSymbol: farSys, Bid: 30000},
		"GS": {WaypointSymbol: farSys + "-S", SystemSymbol: farSys, Bid: 40000},
	}}
	h := farSinkHandler(t, world, farSinkChain(), scanner)
	allowed := []string{farHome, farN1}

	got := h.admitFarSinks(context.Background(), widenedFarCmd(), allowed, baseSnapshotOver(t, h, allowed))

	// The RICHEST lane into this system (GS, spread 39,000) is the one that expires. The
	// system must still be reached for the surviving GX lane — an expired lane is a verdict
	// on that lane, never on the system.
	if !candidateSetHas(got.systems, farSys) {
		t.Fatalf("a system whose richest lane expires in flight must still be admitted for a lane that survives; got %v", got.systems)
	}
	if !hasSinkRow(got.rows, farSys, "GX") {
		t.Fatalf("the WEAK sink survives a 90-minute haul (8h cap) and must be kept; got %+v", got.rows)
	}
	if hasSinkRow(got.rows, farSys, "GS") {
		t.Fatalf("the STRONG sink expires 60 minutes before arrival (30min cap) and must be excluded even though it is the RICHER lane; got %+v", got.rows)
	}
	// An expired market must not survive as a bare routable NODE either: coordinates with no
	// tradeable row are a stop the solver can route to for nothing.
	if waypointOffered(got.waypoints, farSys+"-S") {
		t.Fatalf("the expired STRONG market must not be offered to the planner as a routable waypoint; got %+v", got.waypoints)
	}
	if !waypointOffered(got.waypoints, farSys+"-W") {
		t.Fatalf("the surviving WEAK market must keep its coordinates or the planner cannot route to it; got %+v", got.waypoints)
	}
}

func waypointOffered(waypoints []routing.TourWaypoint, symbol string) bool {
	for _, wp := range waypoints {
		if wp.Symbol == symbol {
			return true
		}
	}
	return false
}

// RED #4 — FAIL CLOSED. Every unreadable input excludes the sink rather than admitting it
// optimistically (RULINGS #4): no way to verify reach, no way to read the global scan, no
// activity label to measure freshness against, and no armed per-pair pricing to charge the
// crossing honestly. Each case starts from the world that DOES admit in RED #1, so a green
// here is the guard firing, not an inert fixture.
func TestAdmitFarSinks_FailsClosedOnUnreadableInputs(t *testing.T) {
	for _, c := range []struct {
		name    string
		mutate  func(fx *tourFixture, h *RunTourCoordinatorHandler)
		command *RunTourCoordinatorCommand
	}{
		// Without the durable graph the only distance reading available is a LIVE 1-hop scan,
		// which would report the far system as adjacent to the whole tour graph — and the
		// per-pair hop matrix is off in the same breath, so the crossing would also be charged
		// as one hop. The fixture hands over exactly that misleading reading.
		{name: "no durable gate graph — reach unverifiable",
			mutate: func(fx *tourFixture, h *RunTourCoordinatorHandler) {
				fx.neighbors[farSys] = []string{farHome, farN1}
				h.SetGateGraph(nil)
			}},
		// A PARTIAL scan — rows returned alongside the error — is the case an emptiness check
		// alone would wave through: the sinks look usable but the read is not trustworthy.
		{name: "global sink scan unreadable",
			mutate: func(_ *tourFixture, h *RunTourCoordinatorHandler) {
				partial := gxSinkAt(farSys+"-W", farSys, 30000)
				partial.err = errors.New("market scan failed")
				h.SetOutOfHorizonSinkScanner(partial)
			}},
		{name: "sink activity unlabelled — freshness unmeasurable",
			mutate: func(fx *tourFixture, _ *RunTourCoordinatorHandler) {
				fx.activityByWaypoint[farSys+"-W"] = ""
			}},
		{name: "per-pair hop pricing unarmed — a far crossing would price as one hop",
			command: &RunTourCoordinatorCommand{PlayerID: 1, CandidateHopDepth: 2, MaxTourSystems: 2}},
	} {
		t.Run(c.name, func(t *testing.T) {
			world := farSinkWorld()
			h := farSinkHandler(t, world, farSinkChain(), gxSinkAt(farSys+"-W", farSys, 30000))
			allowed := []string{farHome, farN1}
			snapshot := baseSnapshotOver(t, h, allowed)
			if c.mutate != nil {
				c.mutate(world, h)
			}
			cmd := widenedFarCmd()
			if c.command != nil {
				cmd = c.command
			}

			if got := h.admitFarSinks(context.Background(), cmd, allowed, snapshot); len(got.systems) != 0 {
				t.Fatalf("%s must admit nothing; got %v", c.name, got.systems)
			}
		})
	}
}

// RED #5 — THE BOUND HOLDS. Three equally reachable, equally fresh far sinks: only the
// richest farSinkMaxSystems are admitted and the rest stay out. Without a cap this is the
// global scan wired raw, which is the unbounded reach that stranded hulls in the first place.
func TestAdmitFarSinks_AdmitsOnlyTheRichestWithinTheBound(t *testing.T) {
	world := farSinkWorld()
	world.markets = map[string][]string{farHome: {farHome + "-M"}}
	world.ask[farHome+"-M"] = map[string]int{"GA": 1000, "GB": 1000, "GC": 1000}
	world.bid[farHome+"-M"] = map[string]int{"GA": 900, "GB": 900, "GC": 900}
	world.tv[farHome+"-M"] = map[string]int{"GA": 100, "GB": 100, "GC": 100}

	sinks := map[string]market.GlobalSinkResult{}
	extra := [][2]string{}
	for _, s := range []struct {
		system string
		good   string
		bid    int
	}{{"X1-F1", "GA", 40000}, {"X1-F2", "GB", 30000}, {"X1-F3", "GC", 20000}} {
		wp := s.system + "-M"
		world.markets[s.system] = []string{wp}
		world.ask[wp] = map[string]int{s.good: s.bid + 500}
		world.bid[wp] = map[string]int{s.good: s.bid}
		world.tv[wp] = map[string]int{s.good: 100}
		world.tradeType[wp] = map[string]string{s.good: "IMPORT"}
		world.activityByWaypoint[wp] = "WEAK"
		sinks[s.good] = market.GlobalSinkResult{WaypointSymbol: wp, SystemSymbol: s.system, Bid: s.bid}
		extra = append(extra, [2]string{farMid, s.system})
	}
	h := farSinkHandler(t, world, farSinkChain(extra...), &stubScanner{sinks: sinks})
	allowed := []string{farHome, farN1}

	got := h.admitFarSinks(context.Background(), widenedFarCmd(), allowed, baseSnapshotOver(t, h, allowed))

	if len(got.systems) != farSinkMaxSystems {
		t.Fatalf("admission must stop at the bound (%d); got %v", farSinkMaxSystems, got.systems)
	}
	if !candidateSetHas(got.systems, "X1-F1") || !candidateSetHas(got.systems, "X1-F2") {
		t.Fatalf("the two RICHEST hidden lanes must be the ones admitted; got %v", got.systems)
	}
	if candidateSetHas(got.systems, "X1-F3") {
		t.Fatalf("the third-richest sink is beyond the bound and must not be admitted; got %v", got.systems)
	}
}

// RED #6 — PRICED ON THE HOPS ADMISSION PASSED. The distances the reachability check
// resolved ride onto the solver's per-pair travel matrix, so the crossing the planner
// charges for is the crossing the guard verified. A far system admitted at 3 hops but
// priced at the solver's absent-pair default of 1 would be ranked three times too well —
// the mis-pricing the narrow horizon existed to prevent.
func TestPlanForState_PricesAdmittedFarSinkAtItsProvenDistance(t *testing.T) {
	world := farSinkWorld()
	fake := &tourFakeRoutingClient{plans: []*routing.TourPlan{{Feasible: true}}}
	h := newTourHandler(t, world, fake, nil)
	h.SetGateGraph(farSinkChain())
	h.SetOutOfHorizonSinkScanner(gxSinkAt(farSys+"-W", farSys, 30000))
	ship := routing.TourShipState{ShipSymbol: "F-1", CurrentWaypoint: farHome + "-M", CurrentSystem: farHome, HoldCapacity: 40}

	if _, _, _, err := h.planForState(context.Background(), ship,
		h.tourSystemsFrom(context.Background(), farHome, widenedFarCmd()), widenedFarCmd(), tourPlanBudget{maxHops: 6, maxSpend: 1_000_000}); err != nil {
		t.Fatalf("planForState: %v", err)
	}
	if d, ok := hopBetween(fake.interSystemHops[0], farHome, farSys); !ok || d != 3 {
		t.Fatalf("(%s,%s) must reach the planner priced at its proven 3 gate hops; got %v (present=%v) in %+v",
			farHome, farSys, d, ok, fake.interSystemHops[0])
	}
	if d, ok := hopBetween(fake.interSystemHops[0], farN1, farSys); !ok || d != 2 {
		t.Fatalf("(%s,%s) must reach the planner priced at its proven 2 gate hops; got %v (present=%v) in %+v",
			farN1, farSys, d, ok, fake.interSystemHops[0])
	}
}

// RED #9 — a reach proof expires with the graph it was proven against. Admitting a system
// changes what "reachable from the whole tour graph" means, so a verdict reached before that
// admission may no longer hold. X1-Y is flyable from the base graph but SIX hops from X1-X;
// once X1-X joins, a second lane into X1-Y must be re-judged, not waved through on the
// earlier answer. Reusing the stale verdict is how a tour ends up holding a leg nobody can fly.
func TestAdmitFarSinks_ReProvesReachAfterTheGraphGrows(t *testing.T) {
	const (
		sinkX = "X1-X" // 3 hops from home via N1—MID
		sinkY = "X1-Y" // 3 hops from home via P1—P2, but SIX hops from sinkX
	)
	world := &tourFixture{
		cargo: map[string]int{}, location: farHome + "-M", cargoCap: 100,
		markets: map[string][]string{
			farHome: {farHome + "-M"},
			sinkX:   {sinkX + "-M"},
			sinkY:   {sinkY + "-S", sinkY + "-W"},
		},
		ask: map[string]map[string]int{
			farHome + "-M": {"GX": 1000, "GY1": 1000, "GY2": 1000},
			sinkX + "-M":   {"GX": 40500},
			sinkY + "-S":   {"GY1": 50500},
			sinkY + "-W":   {"GY2": 30500},
		},
		bid: map[string]map[string]int{
			farHome + "-M": {"GX": 900, "GY1": 900, "GY2": 900},
			sinkX + "-M":   {"GX": 40000},
			sinkY + "-S":   {"GY1": 50000},
			sinkY + "-W":   {"GY2": 30000},
		},
		tv: map[string]map[string]int{
			farHome + "-M": {"GX": 100, "GY1": 100, "GY2": 100},
			sinkX + "-M":   {"GX": 100}, sinkY + "-S": {"GY1": 100}, sinkY + "-W": {"GY2": 100},
		},
		tradeType: map[string]map[string]string{
			sinkX + "-M": {"GX": "IMPORT"}, sinkY + "-S": {"GY1": "IMPORT"}, sinkY + "-W": {"GY2": "IMPORT"},
		},
		// The RICHEST lane (GY1, spread 49,000) expires in flight, so X1-Y is probed and
		// passed over BEFORE X1-X is admitted — which is what puts a stale verdict in hand.
		activityByWaypoint: map[string]string{sinkX + "-M": "WEAK", sinkY + "-S": "STRONG", sinkY + "-W": "WEAK"},
		neighbors:          map[string][]string{farHome: {farN1}},
	}
	graph := farSinkChain([2]string{farMid, sinkX}, [2]string{farHome, "X1-P1"},
		[2]string{"X1-P1", "X1-P2"}, [2]string{"X1-P2", sinkY})
	scanner := &stubScanner{sinks: map[string]market.GlobalSinkResult{
		"GY1": {WaypointSymbol: sinkY + "-S", SystemSymbol: sinkY, Bid: 50000},
		"GX":  {WaypointSymbol: sinkX + "-M", SystemSymbol: sinkX, Bid: 40000},
		"GY2": {WaypointSymbol: sinkY + "-W", SystemSymbol: sinkY, Bid: 30000},
	}}
	h := farSinkHandler(t, world, graph, scanner)
	allowed := []string{farHome, farN1}

	got := h.admitFarSinks(context.Background(), widenedFarCmd(), allowed, baseSnapshotOver(t, h, allowed))

	if !candidateSetHas(got.systems, sinkX) {
		t.Fatalf("%s is flyable from the whole graph and must be admitted; got %v", sinkX, got.systems)
	}
	if candidateSetHas(got.systems, sinkY) {
		t.Fatalf("%s sits 6 hops from the just-admitted %s — its earlier verdict is stale and it must be re-judged and refused; got %v",
			sinkY, sinkX, got.systems)
	}
}

// RED #7 — the pricing agreement is guaranteed independently of the matrix walk. The walk
// is breadth-bounded (a dense gate cluster truncates it), so a far pair it fails to re-find
// must still be priced on the distance admission PROVED. Proven distances win outright: a
// crossing may not be charged one number by the guard and another by the solver.
func TestMergeInterSystemHops_ProvenDistancesFillAndOverrideTheMatrix(t *testing.T) {
	base := []routing.InterSystemHopDistance{
		{FromSystem: farHome, ToSystem: farN1, GateHops: 2},
		{FromSystem: farHome, ToSystem: farSys, GateHops: 9}, // a walk that resolved a longer detour
	}
	proven := []routing.InterSystemHopDistance{
		{FromSystem: farHome, ToSystem: farSys, GateHops: 3},
		{FromSystem: farN1, ToSystem: farSys, GateHops: 2}, // absent from the matrix entirely
	}

	got := mergeInterSystemHops(base, proven)

	if d, ok := hopBetween(got, farN1, farSys); !ok || d != 2 {
		t.Fatalf("a pair the matrix walk never found must still be priced at its proven distance; got %v (present=%v) in %+v", d, ok, got)
	}
	if d, ok := hopBetween(got, farHome, farSys); !ok || d != 3 {
		t.Fatalf("the PROVEN distance must win — admission and pricing cannot disagree; got %v in %+v", d, got)
	}
	if d, ok := hopBetween(got, farHome, farN1); !ok || d != 2 {
		t.Fatalf("matrix pairs with no proven counterpart must survive untouched; got %v (present=%v) in %+v", d, ok, got)
	}
}

// RED #8 — the matrix walk reaches as far as the executor flies. An admitted far sink can
// sit up to the strict flight bound from another allowed system, and a pair the walk cannot
// resolve defaults to ONE hop in the solver — pricing a five-hop crossing as one, which is
// exactly the underpricing the narrow horizon existed to prevent.
func TestTourInterSystemHops_SpansTheExecutorFlightBound(t *testing.T) {
	chain := []string{"X1-H0", "X1-H1", "X1-H2", "X1-H3", "X1-H4", "X1-H5"}
	edges := map[string][]system.GateEdge{}
	for i := 0; i+1 < len(chain); i++ {
		edges[chain[i]] = append(edges[chain[i]], system.GateEdge{ConnectedSystem: chain[i+1]})
		edges[chain[i+1]] = append(edges[chain[i+1]], system.GateEdge{ConnectedSystem: chain[i]})
	}
	h := newCandidatesHandler(t, defaultSafetyFixture())
	h.SetGateGraph(&fakeGateGraph{edges: edges})

	got := h.tourInterSystemHops(context.Background(), []string{chain[0], chain[5]}, widenedFarCmd())

	if d, ok := hopBetween(got, chain[0], chain[5]); !ok || d != 5 {
		t.Fatalf("a pair at the executor's flight bound must be priced at its real 5 hops, not defaulted to 1; got %v (present=%v) in %+v", d, ok, got)
	}
}

// sp-dct0r — the ARRIVAL BOUND the staleness ager predicts with.
//
// The retired constant aged 1800s per gate hop on the stated ground that the ager must
// mirror the solver's per-hop charge. sp-smbgd retired that charge (the solver now prices an
// affine base + per_hop*hops), and the mirror was wrong anyway: the price is an
// expected-value MEDIAN, while this feeds survivesArrival, which asks whether a quote is
// still fresh ON ARRIVAL. These pin both halves of the replacement — looser than the retired
// flat ager everywhere the flight bound reaches (the suppression sp-smbgd's win was gated
// behind), and still conservative against the solver's median charge (never optimistic).

// retiredFlatAgerPerHop is the constant this replaces, quoted rather than referenced: it no
// longer exists in the code, and the test's job is to prove we moved off it.
const retiredFlatAgerPerHop = 30 * time.Minute

// solverMedianCrossingSeconds is the solver's OWN published charge by gate-hop depth
// (750 + 650*hops) — the median the ager must never drop to, quoted so the assertion does not
// recompute the model it is checking.
var solverMedianCrossingSeconds = map[int]float64{1: 1400, 2: 2050, 3: 2700, 4: 3350, 5: 4000}

func TestFarSinkReach_AgesEveryReachableDepthMoreCheaplyThanTheRetiredFlatAger(t *testing.T) {
	for hops := 1; hops <= gategraph.MaxJumpPath; hops++ {
		got := farSinkReach{maxHops: hops}.travel()
		retired := time.Duration(hops) * retiredFlatAgerPerHop
		if got >= retired {
			t.Fatalf("a %d-hop sink is aged %s, but the RETIRED flat ager charged %s — the fix must "+
				"admit more far sinks at every reachable depth, not fewer", hops, got, retired)
		}
	}
}

func TestFarSinkReach_NeverAgesASinkAsCheaplyAsTheSolverPricesIt(t *testing.T) {
	for hops, medianSeconds := range solverMedianCrossingSeconds {
		got := farSinkReach{maxHops: hops}.travel()
		median := time.Duration(medianSeconds) * time.Second
		if got <= median {
			t.Fatalf("a %d-hop sink is aged %s against a solver charge of %s — a freshness guard "+
				"predicting arrival at the MEDIAN is wrong half the time and cannot hold a p90-fitted cap",
				hops, got, median)
		}
	}
}

// A same-system "crossing" must still age by nothing. farSinkReachAt already refuses hops<=0
// upstream, so this pins the model's behaviour at the boundary rather than a live path.
func TestFarSinkReach_AgesASameSystemSinkByNothing(t *testing.T) {
	for _, hops := range []int{0, -1} {
		if got := (farSinkReach{maxHops: hops}).travel(); got != 0 {
			t.Fatalf("a %d-hop (same-system) reach aged by %s, want 0", hops, got)
		}
	}
}

// THE PAYOFF, AS A GUARD DECISION RATHER THAN A NUMBER. A RESTRICTED row (180-minute fitted
// cap) observed 60 minutes ago at the DEEPEST reach the flight bound admits — five gate hops,
// where the retired ager diverged furthest: flat ageing put arrival at 60+150 = 210 minutes and
// discarded the lane, while the arrival bound puts it at 60+~81 = ~141, inside the cap, so it
// survives. Both verdicts are asserted — the retired one AND the new one — so a regression to
// flat ageing fails here rather than silently forfeiting the lane again.
func TestKeepRowsSurvivingArrival_KeepsADeepLaneTheRetiredFlatAgerWouldHaveDiscarded(t *testing.T) {
	now := time.Now()
	const hops = gategraph.MaxJumpPath
	caps := trading.DefaultRankerAgeCaps()
	row := routing.TourGoodSnapshot{
		Waypoint: farSys + "-W", System: farSys, Good: "GX",
		Activity: string(shared.ActivityLevelRestricted), Bid: 30000,
		ObservedAt: now.Add(-60 * time.Minute),
	}

	reach := farSinkReach{maxHops: hops}
	retired := time.Duration(hops) * retiredFlatAgerPerHop
	if survivesArrival(row, retired, caps, now) {
		t.Fatalf("fixture is inert: the retired flat ager (%s) must have DISCARDED this row, or the "+
			"test proves nothing about the change", retired)
	}

	kept, expired := keepRowsSurvivingArrival([]routing.TourGoodSnapshot{row}, reach.travel(), caps, now)
	if expired != 0 || len(kept) != 1 {
		t.Fatalf("a GROWING row 35min old, %d hops out (arrival bound %s, cap %s) must survive; got %d kept, %d expired",
			hops, reach.travel(), caps.For(row.Activity), len(kept), expired)
	}
}

// The loosened bound is still a GUARD: a row that genuinely ages out over the same haul is
// still dropped. Without this, the fix above is indistinguishable from deleting the check.
func TestKeepRowsSurvivingArrival_StillDiscardsARowThatAgesOutOverTheHaul(t *testing.T) {
	now := time.Now()
	const hops = gategraph.MaxJumpPath
	caps := trading.DefaultRankerAgeCaps()
	row := routing.TourGoodSnapshot{
		Waypoint: farSys + "-S", System: farSys, Good: "GS",
		Activity: string(shared.ActivityLevelRestricted), Bid: 40000,
		ObservedAt: now.Add(-120 * time.Minute), // 120min + ~81min arrival > the 180min RESTRICTED cap
	}

	kept, expired := keepRowsSurvivingArrival([]routing.TourGoodSnapshot{row},
		farSinkReach{maxHops: hops}.travel(), caps, now)
	if expired != 1 || len(kept) != 0 {
		t.Fatalf("a GROWING row 50min old cannot survive a %d-hop haul against a %s cap; got %d kept, %d expired",
			hops, caps.For(row.Activity), len(kept), expired)
	}
}

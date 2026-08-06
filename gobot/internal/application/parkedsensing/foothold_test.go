package parkedsensing

import (
	"context"
	"errors"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// foothold_test.go covers the path that breaks the buying deadlock: a SPARE
// placement in a system with no counter of ours is filled by flying a surplus
// hull across a gate.
//
// THE FIXTURE MIRRORS THE LIVE FLEET, deliberately and in detail. The defect
// this area keeps producing is a fake that cannot express the condition under
// test, so the home system carries THIRTEEN parked market hulls with the goods
// sets and depths the production ledger actually holds, the frontier carries
// NINE spare rows across four systems, and there is not one free spare anywhere.
// A one-hull fixture would let a broken surplus rule pass by accident.

// --- fakes -------------------------------------------------------------------

// The gate store fake is expansion_test.go's fakeGates, reused deliberately: it
// is the adversarial one, returning a populated frontier ALONGSIDE its error, so
// a reader here that ignored the error would walk a live-looking topology.

type fakeManned struct {
	hulls map[string]bool
	err   error
}

func (f *fakeManned) MannedHulls(_ context.Context, _ int) (map[string]bool, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.hulls, nil
}

// flyingWorld is a ship reader and a mover sharing ONE position map, so a hull
// commanded to move actually arrives.
//
// That coupling is the point. A mover that only records calls cannot show a hull
// LEAVING a system and ARRIVING at a cross-gate placement, which is the whole
// claim this file has to make good on — the drain writes a claim, and the
// placement machine flies it over the ticks that follow. RouteAcross therefore
// puts the hull in orbit at its destination, exactly as a completed crossing
// leaves it, and Dock berths it.
type flyingWorld struct {
	positions map[string]ShipPos
	docked    map[string]string
	routes    []moveCall
	docks     []string
}

func newFlyingWorld() *flyingWorld {
	return &flyingWorld{positions: map[string]ShipPos{}, docked: map[string]string{}}
}

func (w *flyingWorld) park(ship, waypoint string) {
	w.positions[ship] = ShipPos{Waypoint: waypoint, NavStatus: navigation.NavStatusDocked, Found: true}
	w.docked[waypoint] = ship
}

func (w *flyingWorld) DockedProbeAt(_ context.Context, _ int, waypoint string) (string, bool, error) {
	s, ok := w.docked[waypoint]
	return s, ok, nil
}

// No borrowed hull stands anywhere in this world, and no hull is lendable: these
// tests are about the foothold's own carrier selection, and a borrow answering here
// would fund placements the surplus pool was supposed to be the only source for.
func (w *flyingWorld) DockedBuyerAt(_ context.Context, _ int, _ string) (string, bool, error) {
	return "", false, nil
}

func (w *flyingWorld) LendableHulls(_ context.Context, _ int, _ int) ([]LendableHull, error) {
	return nil, nil
}

func (w *flyingWorld) ShipAt(_ context.Context, _ int, ship string) (ShipPos, error) {
	return w.positions[ship], nil
}

func (w *flyingWorld) NavigateWithin(_ context.Context, _ int, ship, destination string) error {
	w.positions[ship] = ShipPos{Waypoint: destination, NavStatus: navigation.NavStatusInOrbit, Found: true}
	return nil
}

func (w *flyingWorld) RouteAcross(_ context.Context, _ int, ship, from, destination string) error {
	w.routes = append(w.routes, moveCall{ship, destination})
	delete(w.docked, from)
	w.positions[ship] = ShipPos{Waypoint: destination, NavStatus: navigation.NavStatusInOrbit, Found: true}
	return nil
}

func (w *flyingWorld) Dock(_ context.Context, _ int, ship string) error {
	w.docks = append(w.docks, ship)
	pos := w.positions[ship]
	pos.NavStatus = navigation.NavStatusDocked
	w.positions[ship] = pos
	w.docked[pos.Waypoint] = ship
	return nil
}

// --- the live-shaped fixture -------------------------------------------------

// homeMarkets is X1-KP23 exactly as the production ledger holds it: thirteen
// parked market probes, with the goods each was recorded watching and the depth
// measured at it.
//
// The goods sets are what make this fixture worth having. Six of the thirteen
// are genuinely redundant (every good they watch is watched elsewhere in the
// system), one — C38 — is the system's ONLY observer of LAB_INSTRUMENTS and must
// never be taken, and the rest are protected by scout posts. A rule that got any
// of that wrong would pass against a smaller fixture.
func homeMarkets() []QueuedSlot {
	rows := []struct {
		waypoint string
		hull     string
		depth    int64
		goods    []string
	}{
		{"X1-KP23-A1", "TORWIND-2", 639340, []string{"CLOTHING", "EQUIPMENT", "FOOD", "MEDICINE"}},
		{"X1-KP23-J56", "TORWIND-16", 628480, []string{"CLOTHING", "EQUIPMENT", "FOOD", "MEDICINE"}},
		{"X1-KP23-D40", "TORWIND-F", 481760, []string{"ELECTRONICS", "EQUIPMENT", "FABRICS", "MEDICINE"}},
		{"X1-KP23-K86", "TORWIND-17", 437898, []string{"CLOTHING", "EQUIPMENT", "FABRICS", "FOOD"}},
		{"X1-KP23-D41", "TORWIND-14", 381476, []string{"ADVANCED_CIRCUITRY", "ELECTRONICS", "MACHINERY", "MICROPROCESSORS", "SHIP_PLATING"}},
		{"X1-KP23-C38", "TORWIND-11", 311814, []string{"ELECTRONICS", "EQUIPMENT", "LAB_INSTRUMENTS", "SHIP_PLATING"}},
		{"X1-KP23-A4", "TORWIND-12", 201780, []string{"ADVANCED_CIRCUITRY"}},
		{"X1-KP23-E43", "TORWIND-3", 131220, []string{"FABRICS", "MACHINERY"}},
		{"X1-KP23-F46", "TORWIND-13", 94815, []string{"ELECTRONICS"}},
		{"X1-KP23-A3", "TORWIND-D", 77959, []string{"MICROPROCESSORS"}},
		{"X1-KP23-A2", "TORWIND-E", 70314, []string{"SHIP_PLATING"}},
		{"X1-KP23-H50", "TORWIND-4", 68646, []string{"SHIP_PLATING"}},
		{"X1-KP23-G48", "TORWIND-15", 30280, []string{"EQUIPMENT"}},
	}
	out := make([]QueuedSlot, 0, len(rows))
	for _, r := range rows {
		out = append(out, QueuedSlot{
			Waypoint: r.waypoint, System: "X1-KP23", Kind: SlotKindMarket, State: SlotStateParked,
			AssignedShip: r.hull, DepthCredits: r.depth, WhitelistGoods: r.goods,
		})
	}
	return out
}

// frontierSpares is the nine spare rows the live ledger holds: eight unfunded
// wants at yards across four systems we have judged but never occupied, plus the
// one in the home system that is already being flown. None of the eight can be
// bought — that is the deadlock.
func frontierSpares() []QueuedSlot {
	spares := []QueuedSlot{
		{Waypoint: "X1-KP23-C38", System: "X1-KP23", Kind: SlotKindSpare, State: SlotStateInTransit, AssignedShip: "TORWIND-1B"},
	}
	for _, s := range []struct{ waypoint, system string }{
		{"X1-GF41-Y1", "X1-GF41"}, {"X1-GF41-Y2", "X1-GF41"}, {"X1-GF41-Y3", "X1-GF41"},
		{"X1-BT49-Y1", "X1-BT49"}, {"X1-BT49-Y2", "X1-BT49"},
		{"X1-MY3-Y1", "X1-MY3"}, {"X1-MY3-Y2", "X1-MY3"},
		{"X1-UV2-Y1", "X1-UV2"},
	} {
		spares = append(spares, QueuedSlot{
			Waypoint: s.waypoint, System: s.system, Kind: SlotKindSpare, State: SlotStateWanted,
		})
	}
	return spares
}

// liveGates is the gate adjacency around the home system, as stored.
func liveGates() *fakeGates {
	return &fakeGates{adjacency: map[string][]string{
		"X1-KP23": {"X1-AJ10", "X1-GF41", "X1-MY3", "X1-QG29", "X1-XD91"},
		"X1-GF41": {"X1-AJ10", "X1-KP23", "X1-QG29", "X1-UV2"},
		"X1-MY3":  {"X1-AJ10", "X1-BT49", "X1-KP23", "X1-XD91"},
		"X1-BT49": {"X1-AJ10", "X1-MY3"},
		"X1-UV2":  {"X1-AJ10", "X1-GF41"},
		"X1-AJ10": {"X1-BT49", "X1-GF41", "X1-KP23", "X1-MY3", "X1-UV2"},
	}}
}

// footholdPorts wires the live-shaped world. mannedHulls names the hulls a scout
// post claims; the live fleet has seven of the thirteen so manned.
func footholdPorts(manned map[string]bool) (BuyPorts, *fakeBuyLedger, *flyingWorld) {
	led := &fakeBuyLedger{
		slots:   append(homeMarkets(), frontierSpares()...),
		systems: []ScreenedSystem{{System: "X1-KP23", DepthCredits: 3_000_000}},
	}
	world := newFlyingWorld()
	for _, row := range homeMarkets() {
		world.park(row.AssignedShip, row.Waypoint)
	}
	return BuyPorts{
		Treasury:    &fakeTreasury{credits: 5_000_000},
		CargoSpend:  &fakeCargoSpend{},
		Purchaser:   &fakePurchaser{price: 100_000},
		Ledger:      led,
		Yards:       &fakeYards{yards: map[string][]string{}},
		Ships:       world,
		Fleet:       &fakeFleet{},
		Gates:       liveGates(),
		MannedHulls: &fakeManned{hulls: manned},
	}, led, world
}

// liveManned is the seven home-system hulls that are also manning a scout post.
func liveManned() map[string]bool {
	return map[string]bool{
		"TORWIND-2": true, "TORWIND-16": true, "TORWIND-14": true,
		"TORWIND-3": true, "TORWIND-13": true, "TORWIND-E": true, "TORWIND-4": true,
	}
}

func drainOnce(t *testing.T, p BuyPorts) BuyReport {
	t.Helper()
	rep, err := DrainBuyQueue(context.Background(), p, testPlayerID, BuyKnobs{SpendEnabled: true, ProbeCap: 40}, fixedClock{})
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	return rep
}

func slotAt(t *testing.T, led *fakeBuyLedger, waypoint, kind string) QueuedSlot {
	t.Helper()
	for _, s := range led.slots {
		if s.Waypoint == waypoint && s.Kind == kind {
			return s
		}
	}
	t.Fatalf("no %s slot at %s", kind, waypoint)
	return QueuedSlot{}
}

// --- the headline: a hull leaves one system and arrives in another ------------

// A single tick cannot show this. The drain only writes the claim; the crossing
// is the placement machine's, one step per tick, so the proof that a foothold is
// actually established has to run both engines against the SAME ledger over
// consecutive ticks and watch the hull move.
func TestFoothold_SurplusHullLeavesHomeAndParksAtCrossGateYardOverTicks(t *testing.T) {
	ports, led, world := footholdPorts(liveManned())

	// TICK 1 — the drain. No yard in X1-GF41 has a hull of ours, so the spare
	// want there cannot be bought; the foothold path claims it instead.
	rep := drainOnce(t, ports)
	if rep.Footholds == 0 {
		t.Fatalf("no foothold established; report %+v", rep)
	}
	if rep.Bought != 0 {
		t.Fatalf("foothold must spend nothing, bought %d", rep.Bought)
	}

	target := slotAt(t, led, "X1-GF41-Y1", SlotKindSpare)
	if target.State != SlotStateInTransit || target.AssignedShip == "" {
		t.Fatalf("target not claimed: %+v", target)
	}
	hull := target.AssignedShip
	if hull != "TORWIND-15" {
		t.Fatalf("expected the cheapest redundant unmanned hull TORWIND-15, got %s", hull)
	}

	// The market it came off is released — still WANTED, so it will be re-bought,
	// but no longer naming a hull that has left.
	source := slotAt(t, led, "X1-KP23-G48", SlotKindMarket)
	if source.State != SlotStateWanted || source.AssignedShip != "" {
		t.Fatalf("source market not released: %+v", source)
	}

	// The hull has NOT moved yet — the drain wrote a row and nothing else.
	if pos := world.positions[hull]; pos.Waypoint != "X1-KP23-G48" {
		t.Fatalf("drain moved a hull inside the tick; hull at %s", pos.Waypoint)
	}

	// TICKS 2..n — the placement machine flies it. Bounded so a machine that
	// never advances fails here rather than hanging.
	placement := PlacementPorts{Ledger: led, Ships: world, Mover: world, Fleet: &fakeFleet{}}
	for tick := 0; tick < 5; tick++ {
		if _, err := AdvancePlacements(context.Background(), placement, testPlayerID, 0); err != nil {
			t.Fatalf("placement tick %d: %v", tick, err)
		}
	}

	// It crossed the gate...
	if len(world.routes) == 0 {
		t.Fatalf("hull was never routed across a gate")
	}
	if world.routes[0] != (moveCall{hull, "X1-GF41-Y1"}) {
		t.Fatalf("unexpected crossing: %+v", world.routes[0])
	}
	// ...and is now parked at the yard, which is the whole objective: a hull
	// standing at a counter in X1-GF41 is what makes that system's yards buyable.
	arrived := slotAt(t, led, "X1-GF41-Y1", SlotKindSpare)
	if arrived.State != SlotStateParked || arrived.AssignedShip != hull {
		t.Fatalf("hull never took up its foothold: %+v", arrived)
	}
	if pos := world.positions[hull]; pos.Waypoint != "X1-GF41-Y1" || pos.NavStatus != navigation.NavStatusDocked {
		t.Fatalf("hull not docked at the foothold: %+v", pos)
	}
	// And the ledger now answers buyerAt for that yard — the deadlock is broken.
	if ship, found, _ := world.DockedProbeAt(context.Background(), testPlayerID, "X1-GF41-Y1"); !found || ship != hull {
		t.Fatalf("yard still has no purchasing hull: %q found=%v", ship, found)
	}
}

// --- the surplus predicate ----------------------------------------------------

// C38 is the home system's only observer of LAB_INSTRUMENTS. It is unmanned and
// it is not the last probe, so nothing but the goods-coverage rule protects it.
func TestFoothold_NeverTakesTheOnlyObserverOfAGood(t *testing.T) {
	// Every hull manned EXCEPT C38's, so if C38 is takeable it will be taken.
	manned := map[string]bool{}
	for _, row := range homeMarkets() {
		manned[row.AssignedShip] = true
	}
	delete(manned, "TORWIND-11") // C38 — sole LAB_INSTRUMENTS observer

	ports, led, _ := footholdPorts(manned)
	rep := drainOnce(t, ports)

	if rep.Footholds != 0 {
		t.Fatalf("took a hull that was the only observer of a good; report %+v", rep)
	}
	c38 := slotAt(t, led, "X1-KP23-C38", SlotKindMarket)
	if c38.State != SlotStateParked || c38.AssignedShip != "TORWIND-11" {
		t.Fatalf("LAB_INSTRUMENTS observer was released: %+v", c38)
	}
}

// Redundancy is a property of a SET, so it cannot be measured once and spent
// twice. A3 and A4 both watch goods that D41 also watches; D41 is manned. Once
// A3 goes, MICROPROCESSORS is watched only by D41 — so a second take is still
// legal — but if the two hulls covered ONLY each other, the second must not be.
func TestFoothold_ReDecidesRedundancyAfterEachTake(t *testing.T) {
	// A pair that covers only each other, in a system of their own, plus one
	// spare want next door for each of them to be drawn toward.
	led := &fakeBuyLedger{
		slots: []QueuedSlot{
			{Waypoint: "X1-HOME-M1", System: "X1-HOME", Kind: SlotKindMarket, State: SlotStateParked,
				AssignedShip: "PROBE-A", DepthCredits: 10, WhitelistGoods: []string{"FUEL"}},
			{Waypoint: "X1-HOME-M2", System: "X1-HOME", Kind: SlotKindMarket, State: SlotStateParked,
				AssignedShip: "PROBE-B", DepthCredits: 20, WhitelistGoods: []string{"FUEL"}},
			{Waypoint: "X1-EDGE-Y1", System: "X1-EDGE", Kind: SlotKindSpare, State: SlotStateWanted},
			{Waypoint: "X1-EDGE-Y2", System: "X1-EDGE", Kind: SlotKindSpare, State: SlotStateWanted},
		},
		systems: []ScreenedSystem{{System: "X1-HOME", DepthCredits: 100}},
	}
	world := newFlyingWorld()
	world.park("PROBE-A", "X1-HOME-M1")
	world.park("PROBE-B", "X1-HOME-M2")

	ports := BuyPorts{
		Treasury: &fakeTreasury{credits: 5_000_000}, CargoSpend: &fakeCargoSpend{},
		Purchaser: &fakePurchaser{price: 100_000}, Ledger: led,
		Yards: &fakeYards{yards: map[string][]string{}}, Ships: world, Fleet: &fakeFleet{},
		Gates: &fakeGates{adjacency: map[string][]string{
			"X1-EDGE": {"X1-HOME"}, "X1-HOME": {"X1-EDGE"},
		}},
		MannedHulls: &fakeManned{hulls: map[string]bool{}},
	}

	rep := drainOnce(t, ports)

	// Exactly ONE may go. The snapshot says both are redundant — each is covered
	// by the other — and taking both would leave FUEL unwatched.
	if rep.Footholds != 1 {
		t.Fatalf("expected exactly one take, got %d", rep.Footholds)
	}
	remaining := 0
	for _, s := range led.slots {
		if s.Kind == SlotKindMarket && s.State == SlotStateParked && s.AssignedShip != "" {
			remaining++
		}
	}
	if remaining != 1 {
		t.Fatalf("FUEL coverage collapsed: %d parked market hulls left, want 1", remaining)
	}
}

func TestFoothold_NeverTakesAMannedScoutPostHull(t *testing.T) {
	// G48/TORWIND-15 is the cheapest fully-redundant hull and would be taken
	// first; manning it must move the choice on to the next eligible one.
	manned := liveManned()
	manned["TORWIND-15"] = true

	ports, led, _ := footholdPorts(manned)
	rep := drainOnce(t, ports)

	if rep.Footholds == 0 {
		t.Fatalf("expected a foothold from a different hull; report %+v", rep)
	}
	g48 := slotAt(t, led, "X1-KP23-G48", SlotKindMarket)
	if g48.State != SlotStateParked || g48.AssignedShip != "TORWIND-15" {
		t.Fatalf("a manned scout-post hull was taken: %+v", g48)
	}
	// The next-cheapest unmanned redundant hull is A3/TORWIND-D.
	if got := slotAt(t, led, "X1-GF41-Y1", SlotKindSpare).AssignedShip; got != "TORWIND-D" {
		t.Fatalf("expected TORWIND-D, got %q", got)
	}
}

// --- targeting ----------------------------------------------------------------

// A MARKET want is worth one probe and never justifies stripping a working
// market. Only a SPARE want — expansion's request for a buying foothold, always
// written at a probe yard — does.
func TestFoothold_FillsOnlySparePlacementsNotMarketWants(t *testing.T) {
	ports, led, _ := footholdPorts(liveManned())
	// Replace the frontier's spare wants with market wants in an IN_SCOPE system
	// that has no counter of ours, so the ONLY thing distinguishing them is kind.
	var kept []QueuedSlot
	for _, s := range led.slots {
		if s.Kind == SlotKindSpare && s.State == SlotStateWanted {
			s.Kind = SlotKindMarket
		}
		kept = append(kept, s)
	}
	led.slots = kept
	led.systems = append(led.systems, ScreenedSystem{System: "X1-GF41", DepthCredits: 900_000})

	rep := drainOnce(t, ports)

	if rep.Footholds != 0 {
		t.Fatalf("a market want pulled a hull off a working market; report %+v", rep)
	}
	if rep.SkippedNoYard == 0 {
		t.Fatalf("market wants should have been skipped for want of a yard; report %+v", rep)
	}
}

// The reach is the WALK's reach. A claim written for a hull further out than
// MaxWalkRings stalls silently — nextHopToward names no next system, the row
// sits IN_TRANSIT naming a hull that never arrives — so a distant system must
// not be drawn from at all.
func TestFoothold_DrawsNoHullFromBeyondTheWalksReach(t *testing.T) {
	ports, led, world := footholdPorts(liveManned())
	// A chain three hops long: the target's system reaches HOP1 and HOP2, but
	// the surplus sits in FAR, one hop further than the walk can fly.
	ports.Gates = &fakeGates{adjacency: map[string][]string{
		"X1-GF41": {"X1-HOP1"},
		"X1-HOP1": {"X1-GF41", "X1-HOP2"},
		"X1-HOP2": {"X1-HOP1", "X1-FAR"},
		"X1-FAR":  {"X1-HOP2"},
	}}
	led.slots = append([]QueuedSlot{
		{Waypoint: "X1-GF41-Y1", System: "X1-GF41", Kind: SlotKindSpare, State: SlotStateWanted},
	}, farSurplus()...)
	// THE DISTANT HULLS MUST EXIST IN THE SHIPS TABLE. Without this the test
	// passes for the wrong reason: ShipAt answers Found=false, the hulls are
	// skipped as unlocatable, and the assertion below holds even with the reach
	// bound removed entirely. A mutation probe on MaxWalkRings survived exactly
	// that gap. Parked and docked, they are takeable in every respect BUT range,
	// so distance is the only thing left that can hold them.
	world.park("FAR-A", "X1-FAR-M1")
	world.park("FAR-B", "X1-FAR-M2")

	rep := drainOnce(t, ports)

	if rep.Footholds != 0 {
		t.Fatalf("drew a hull from beyond MaxWalkRings=%d; report %+v", MaxWalkRings, rep)
	}
	if got := slotAt(t, led, "X1-FAR-M1", SlotKindMarket); got.State != SlotStateParked {
		t.Fatalf("distant market was released: %+v", got)
	}
}

// The companion to the test above, and the reason it can be trusted: the SAME
// fixture with the surplus moved one hop closer DOES yield a foothold. Without
// this, a rule that drew from nowhere at all would satisfy the range test.
func TestFoothold_DrawsAHullFromTheEdgeOfTheWalksReach(t *testing.T) {
	ports, led, world := footholdPorts(liveManned())
	ports.Gates = &fakeGates{adjacency: map[string][]string{
		"X1-GF41": {"X1-HOP1"},
		"X1-HOP1": {"X1-GF41", "X1-FAR"},
		"X1-FAR":  {"X1-HOP1"},
	}}
	led.slots = append([]QueuedSlot{
		{Waypoint: "X1-GF41-Y1", System: "X1-GF41", Kind: SlotKindSpare, State: SlotStateWanted},
	}, farSurplus()...)
	world.park("FAR-A", "X1-FAR-M1")
	world.park("FAR-B", "X1-FAR-M2")

	rep := drainOnce(t, ports)

	if rep.Footholds != 1 {
		t.Fatalf("a surplus hull exactly MaxWalkRings=%d hops away should be reachable; report %+v", MaxWalkRings, rep)
	}
	if got := slotAt(t, led, "X1-GF41-Y1", SlotKindSpare); got.AssignedShip != "FAR-A" {
		t.Fatalf("expected the cheapest distant hull FAR-A, got %+v", got)
	}
}

// farSurplus is two fully-redundant unmanned hulls, parked three gate hops from
// the placement that wants them.
func farSurplus() []QueuedSlot {
	return []QueuedSlot{
		{Waypoint: "X1-FAR-M1", System: "X1-FAR", Kind: SlotKindMarket, State: SlotStateParked,
			AssignedShip: "FAR-A", DepthCredits: 10, WhitelistGoods: []string{"FUEL"}},
		{Waypoint: "X1-FAR-M2", System: "X1-FAR", Kind: SlotKindMarket, State: SlotStateParked,
			AssignedShip: "FAR-B", DepthCredits: 20, WhitelistGoods: []string{"FUEL"}},
	}
}

// --- guards -------------------------------------------------------------------

// The money guard: the target is claimed BEFORE the source is released, so a
// crash between the two over-counts the hull (buying fewer probes) rather than
// leaving it named by neither row and authorising a replacement purchase for a
// probe we already own (RULINGS #4).
func TestFoothold_ClaimsTargetBeforeReleasingSource(t *testing.T) {
	ports, led, _ := footholdPorts(liveManned())
	drainOnce(t, ports)

	claim, release := -1, -1
	for i, tr := range led.transitions {
		if tr.waypoint == "X1-GF41-Y1" && tr.to == SlotStateInTransit {
			claim = i
		}
		if tr.waypoint == "X1-KP23-G48" && tr.to == SlotStateWanted {
			release = i
		}
	}
	if claim < 0 || release < 0 {
		t.Fatalf("expected both writes, got claim=%d release=%d", claim, release)
	}
	if claim > release {
		t.Fatalf("source released before the target was claimed: a crash between them would orphan the hull")
	}
}

// An unreadable post list read permissively is an empty one, which says "no hull
// is manned" and would hand the scouting fleet's hulls away. It must fail closed
// — and must not take the rest of the drain down with it.
func TestFoothold_TakesNoHullWhenThePostListIsUnreadable(t *testing.T) {
	ports, led, _ := footholdPorts(liveManned())
	ports.MannedHulls = &fakeManned{err: errors.New("post store down")}

	rep := drainOnce(t, ports)

	if rep.Footholds != 0 {
		t.Fatalf("took a hull without being able to check the post list; report %+v", rep)
	}
	for _, row := range homeMarkets() {
		if got := slotAt(t, led, row.Waypoint, SlotKindMarket); got.State != SlotStateParked {
			t.Fatalf("released %s despite an unreadable post list: %+v", row.Waypoint, got)
		}
	}
}

// The ledger says PARKED; the ships table is what says the hull is standing
// still. A hull already flying is steered by another machine and is not takeable.
func TestFoothold_SkipsAHullTheShipsTableSaysIsFlying(t *testing.T) {
	ports, led, world := footholdPorts(liveManned())
	// The cheapest eligible hull is in the air.
	world.positions["TORWIND-15"] = ShipPos{
		Waypoint: "X1-KP23-G48", NavStatus: navigation.NavStatusInTransit, Found: true,
	}

	rep := drainOnce(t, ports)

	if rep.Footholds == 0 {
		t.Fatalf("expected the next eligible hull to be used; report %+v", rep)
	}
	if got := slotAt(t, led, "X1-KP23-G48", SlotKindMarket); got.AssignedShip != "TORWIND-15" {
		t.Fatalf("re-tasked a hull that was mid-flight: %+v", got)
	}
}

// The per-tick bound holds even when many placements are begging for a hull.
func TestFoothold_StopsAtThePerTickBound(t *testing.T) {
	ports, _, _ := footholdPorts(liveManned())
	rep := drainOnce(t, ports)

	if rep.Footholds > maxFootholdRetasks {
		t.Fatalf("sent %d hulls in one tick, bound is %d", rep.Footholds, maxFootholdRetasks)
	}
	if rep.Footholds != maxFootholdRetasks {
		t.Fatalf("eight unfunded spare wants and five eligible hulls should saturate the bound, got %d", rep.Footholds)
	}
}

// Unwired guard ports mean no foothold at all, never a partly-guarded one.
func TestFoothold_DoesNothingWhenTheGuardPortsAreUnwired(t *testing.T) {
	ports, led, _ := footholdPorts(liveManned())
	ports.Gates, ports.MannedHulls = nil, nil

	rep := drainOnce(t, ports)

	if rep.Footholds != 0 {
		t.Fatalf("established a foothold with no guards wired; report %+v", rep)
	}
	for _, row := range homeMarkets() {
		if got := slotAt(t, led, row.Waypoint, SlotKindMarket); got.AssignedShip != row.AssignedShip {
			t.Fatalf("%s was re-tasked with no guards wired: %+v", row.Waypoint, got)
		}
	}
}

// --- gate direction: the reach search must follow the edges the hull will fly ---

// asymGates is a DELIBERATELY ONE-WAY neighbourhood around the target, and the
// asymmetry is the entire test.
//
// Every other fixture in this file is symmetric, so a forward walk from the target
// and a reverse walk into it return the SAME set and neither can tell the two
// apart. Live, 624 of 5,488 gate edges (11.4%) have no reverse row, so that
// symmetry is a property of the fixtures, not of the map.
//
//	X1-AAA  ->  X1-GF41      (and NOT back: a hull in AAA CAN arrive)
//	X1-GF41 ->  X1-BBB       (and NOT back: a hull in BBB can NEVER arrive)
//
// A search that walks FORWARD FROM THE TARGET finds BBB and offers it as a source,
// which dispatches a hull that can never arrive — it holds probe-cap headroom and
// does no work. A search that asks "who can reach the target" finds AAA.
func asymGates() *fakeGates {
	return &fakeGates{adjacency: map[string][]string{
		"X1-AAA":  {"X1-GF41"},
		"X1-GF41": {"X1-BBB"},
		"X1-BBB":  {},
	}}
}

// asymSurplus puts a redundant, takeable pair in BOTH one-way systems.
//
// The BBB pair is deliberately CHEAPER (depth 1/2 against AAA's 10/20), so a rule
// that ranked candidates by sacrifice cost rather than by reachability would pick
// the stranding source. Reachability has to be the only thing that decides.
func asymSurplus() []QueuedSlot {
	return []QueuedSlot{
		{Waypoint: "X1-AAA-M1", System: "X1-AAA", Kind: SlotKindMarket, State: SlotStateParked,
			AssignedShip: "AAA-A", DepthCredits: 10, WhitelistGoods: []string{"FUEL"}},
		{Waypoint: "X1-AAA-M2", System: "X1-AAA", Kind: SlotKindMarket, State: SlotStateParked,
			AssignedShip: "AAA-B", DepthCredits: 20, WhitelistGoods: []string{"FUEL"}},
		{Waypoint: "X1-BBB-M1", System: "X1-BBB", Kind: SlotKindMarket, State: SlotStateParked,
			AssignedShip: "BBB-A", DepthCredits: 1, WhitelistGoods: []string{"FUEL"}},
		{Waypoint: "X1-BBB-M2", System: "X1-BBB", Kind: SlotKindMarket, State: SlotStateParked,
			AssignedShip: "BBB-B", DepthCredits: 2, WhitelistGoods: []string{"FUEL"}},
	}
}

func asymPorts(t *testing.T) (BuyPorts, *fakeBuyLedger) {
	t.Helper()
	ports, led, world := footholdPorts(liveManned())
	ports.Gates = asymGates()
	led.slots = append([]QueuedSlot{
		{Waypoint: "X1-GF41-Y1", System: "X1-GF41", Kind: SlotKindSpare, State: SlotStateWanted},
	}, asymSurplus()...)
	// Every candidate hull is parked and docked, so range and direction are the
	// only things left that can hold any of them — the same trap
	// TestFoothold_DrawsNoHullFromBeyondTheWalksReach documents.
	for _, row := range asymSurplus() {
		world.park(row.AssignedShip, row.Waypoint)
	}
	return ports, led
}

func TestFoothold_DrawsFromASystemThatCanReachTheTargetOverAOneWayEdge(t *testing.T) {
	ports, led := asymPorts(t)

	rep := drainOnce(t, ports)

	if rep.Footholds != 1 {
		t.Fatalf("Footholds = %d, want 1 — X1-AAA reaches the target over a one-way edge; report %+v", rep.Footholds, rep)
	}
	if got := slotAt(t, led, "X1-GF41-Y1", SlotKindSpare); got.AssignedShip != "AAA-A" {
		t.Fatalf("foothold hull = %q, want AAA-A from the system that can actually arrive", got.AssignedShip)
	}
	// The converse, and the half that catches the live defect: X1-BBB is reachable
	// FROM the target and so appears in a forward walk, but a hull there can never
	// arrive. Neither of its markets may be released.
	for _, waypoint := range []string{"X1-BBB-M1", "X1-BBB-M2"} {
		if got := slotAt(t, led, waypoint, SlotKindMarket); got.State != SlotStateParked {
			t.Fatalf("%s was released to fly an unreachable route (state %s) — that hull would strand", waypoint, got.State)
		}
	}
}

func TestFoothold_SendsNothingWhenOnlyAnUnreachableSystemHasSurplus(t *testing.T) {
	// The pure negative. Without it, a rule that simply drew from nowhere would
	// satisfy the test above — and a stranding dispatch is worse than no foothold,
	// because the hull keeps holding probe-cap headroom while doing no work.
	ports, led, world := footholdPorts(liveManned())
	ports.Gates = asymGates()
	onlyUnreachable := asymSurplus()[2:] // the X1-BBB pair alone
	led.slots = append([]QueuedSlot{
		{Waypoint: "X1-GF41-Y1", System: "X1-GF41", Kind: SlotKindSpare, State: SlotStateWanted},
	}, onlyUnreachable...)
	for _, row := range onlyUnreachable {
		world.park(row.AssignedShip, row.Waypoint)
	}

	rep := drainOnce(t, ports)

	if rep.Footholds != 0 {
		t.Fatalf("Footholds = %d, want 0 — the only surplus sits where no hull can reach the target", rep.Footholds)
	}
	if got := slotAt(t, led, "X1-GF41-Y1", SlotKindSpare); got.State != SlotStateWanted {
		t.Fatalf("target claimed for an unreachable source: %+v", got)
	}
}

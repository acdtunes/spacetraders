package parkedsensing_test

// Cross-gate placement walk tests.
//
// WHAT THIS PINS, and why a single-tick test cannot. The walk's whole claim is
// that a hull crosses a gate ACROSS SEVERAL TICKS without any one of them
// waiting. Both halves of that are properties OF A SEQUENCE:
//
//   - "it crosses" needs consecutive ticks reading state the previous tick
//     wrote, or nothing is ever shown to advance;
//   - "it never blocks" needs every one of those ticks to finish, not just the
//     first, because the step that blocks is the SECOND one (the jump), and a
//     test that only ever runs step one would pass over the defect.
//
// So the headline test runs real ticks against a world that PERSISTS between
// them — a mutable ships table and a mutable placement ledger, which are exactly
// the two durable rows the production walk reads. Nothing is carried in memory
// between ticks by the code under test; if it were, the world below would not be
// enough to drive it.
//
// The mediator models the real failure faithfully: JumpShipCommand sent from OFF
// the gate makes the real handler navigate the hull there and wait out that
// flight, so here it parks. That is what turns "jumped from the wrong place"
// from a style complaint into a hung tick.

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	adapterSensing "github.com/andrescamacho/spacetraders-go/internal/adapters/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/application/mediator"
	appSensing "github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipQueries "github.com/andrescamacho/spacetraders-go/internal/application/ship/queries"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ---- the persistent world ---------------------------------------------------

// stubGateNeighbours is the stored gate adjacency, in memory. A system absent
// from the map answers with nothing, which is how the real port reports a
// topology it is unsure of.
type stubGateNeighbours struct {
	edges map[string][]string
	err   error
}

func (s stubGateNeighbours) Neighbours(_ context.Context, system string) ([]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.edges[system], nil
}

// Mapped reports every system as already mapped — neutral for the gate-priority tier.
func (s stubGateNeighbours) Mapped(_ context.Context, _ string) (bool, error) { return true, nil }

// walkWorld stands in for the two durable rows the walk resumes from: where the
// ships table says each hull is, and what the ledger says each placement is
// doing. It is MUTABLE and shared across ticks on purpose — a tick that cannot
// see the previous tick's writes cannot demonstrate progress.
type walkWorld struct {
	mu sync.Mutex

	shipAt    map[string]string
	navStatus map[string]navigation.NavStatus

	// gateOf names each system's jump gate, so the walk's "am I standing on the
	// gate" question has a real answer that changes as the hull moves.
	gateOf map[string]string

	sent []string
	// jumpedTo records the DestinationSystem of every jump, which is how the
	// next-hop choice is inspected.
	jumpedTo []string

	// release is never closed while a test body runs: a command that waits out a
	// journey parks on it exactly as WaitForShipArrival parks on the arrival.
	release chan struct{}
}

// PassableGraph mirrors this fake's own adjacency as a single snapshot. Mapped enumerates exactly
// the systems this fake HOLDS: a bulk snapshot cannot express the per-system Mapped's "true for
// anything you ask", and enumerating what is actually stored is the truthful reading — a system
// with no entry here has not been read, which is what the reachability filter treats as unknown
// rather than as a dead end.
func (s stubGateNeighbours) PassableGraph(_ context.Context) (appSensing.GateGraph, error) {
	if s.err != nil {
		return appSensing.GateGraph{}, s.err
	}
	graph := appSensing.GateGraph{Passable: map[string][]string{}, Mapped: map[string]bool{}}
	for system, neighbours := range s.edges {
		graph.Mapped[system] = true
		graph.Passable[system] = append([]string(nil), neighbours...)
	}
	return graph, nil
}

func newWalkWorld() *walkWorld {
	return &walkWorld{
		shipAt:    map[string]string{},
		navStatus: map[string]navigation.NavStatus{},
		gateOf:    map[string]string{},
		release:   make(chan struct{}),
	}
}

func (w *walkWorld) Send(ctx context.Context, request mediator.Request) (mediator.Response, error) {
	w.mu.Lock()
	w.sent = append(w.sent, reflect.TypeOf(request).String())
	w.mu.Unlock()

	waitOutTheFlight := func() {
		select {
		case <-w.release:
		case <-ctx.Done():
		}
	}

	switch cmd := request.(type) {
	case *shipQueries.FindNearestJumpGateQuery:
		w.mu.Lock()
		defer w.mu.Unlock()
		system := shared.ExtractSystemSymbol(w.shipAt[cmd.ShipSymbol])
		symbol, ok := w.gateOf[system]
		if !ok {
			return &shipQueries.FindNearestJumpGateResponse{}, nil
		}
		gate, err := shared.NewWaypoint(symbol, 0, 0)
		if err != nil {
			return nil, err
		}
		gate.Type = "JUMP_GATE"
		return &shipQueries.FindNearestJumpGateResponse{JumpGate: gate}, nil

	case *shipTypes.NavigateDirectCommand:
		// Dispatch only. The API has accepted the move; the arrival scheduler
		// writes the landing back, which the NEXT tick reads.
		w.mu.Lock()
		defer w.mu.Unlock()
		w.shipAt[cmd.ShipSymbol] = cmd.Destination
		w.navStatus[cmd.ShipSymbol] = navigation.NavStatusInOrbit
		return &shipTypes.NavigateDirectResponse{Status: "navigating"}, nil

	case *shipNav.JumpShipCommand:
		// Faithful to the real handler: a hull already ON the gate jumps
		// instantly, while a hull that is NOT makes the handler fly it there
		// first — through NavigateRouteCommand, which waits out the flight. So
		// reaching this command off-gate IS the defect, and it hangs here.
		w.mu.Lock()
		onGate := w.shipAt[cmd.ShipSymbol] == w.gateOf[shared.ExtractSystemSymbol(w.shipAt[cmd.ShipSymbol])]
		w.mu.Unlock()
		if !onGate {
			waitOutTheFlight()
			return &shipNav.JumpShipResponse{Success: true}, nil
		}
		w.mu.Lock()
		defer w.mu.Unlock()
		w.jumpedTo = append(w.jumpedTo, cmd.DestinationSystem)
		// Landing: a jump puts the hull on the destination system's gate.
		if gate, ok := w.gateOf[cmd.DestinationSystem]; ok {
			w.shipAt[cmd.ShipSymbol] = gate
			w.navStatus[cmd.ShipSymbol] = navigation.NavStatusInOrbit
		}
		return &shipNav.JumpShipResponse{Success: true}, nil

	case *shipNav.NavigateRouteCommand, *shipNav.RouteShipCommand:
		waitOutTheFlight()
		return &shipNav.NavigateRouteResponse{Status: "completed"}, nil
	}
	return nil, nil
}

func (w *walkWorld) Register(reflect.Type, mediator.RequestHandler) error { return nil }
func (w *walkWorld) RegisterMiddleware(mediator.Middleware)               {}

func (w *walkWorld) commands() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.sent...)
}

func (w *walkWorld) sentAny(needle string) bool {
	for _, name := range w.commands() {
		if strings.Contains(name, needle) {
			return true
		}
	}
	return false
}

func (w *walkWorld) positionOf(ship string) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.shipAt[ship]
}

func (w *walkWorld) jumps() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.jumpedTo...)
}

// ShipAt makes the world serve as the placement machine's ships-table reader, so
// every tick sees exactly what the previous tick's commands wrote.
func (w *walkWorld) ShipAt(_ context.Context, _ int, shipSymbol string) (appSensing.ShipPos, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	at, ok := w.shipAt[shipSymbol]
	return appSensing.ShipPos{Waypoint: at, NavStatus: w.navStatus[shipSymbol], Found: ok}, nil
}

func (w *walkWorld) DockedProbeAt(context.Context, int, string) (string, bool, error) {
	return "", false, nil
}

// The borrow path is not exercised here — this world is about the gate walk — so
// both reads answer "nobody", which is the neutral value: it leaves the walk under
// test deciding exactly what it decided before the escape existed.
func (w *walkWorld) DockedBuyerAt(context.Context, int, string) (string, bool, error) {
	return "", false, nil
}

func (w *walkWorld) LendableHulls(context.Context, int, int) ([]appSensing.LendableHull, error) {
	return nil, nil
}

// walkLedger is the placement ledger, and it PERSISTS: a transition really flips
// the stored state, so the next tick reads the state this one wrote. That is the
// property the headline test turns on — a ledger that merely recorded the
// attempt would let a walk that never advances still look like it did.
type walkLedger struct {
	mu          sync.Mutex
	slots       map[string]*appSensing.QueuedSlot
	transitions []string
	attempts    []string
}

func newWalkLedger(slot appSensing.QueuedSlot) *walkLedger {
	return &walkLedger{slots: map[string]*appSensing.QueuedSlot{slot.Waypoint: &slot}}
}

func (l *walkLedger) SlotsByState(_ context.Context, _ int, states ...string) ([]appSensing.QueuedSlot, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []appSensing.QueuedSlot
	for _, slot := range l.slots {
		for _, want := range states {
			if slot.State == want {
				out = append(out, *slot)
				break
			}
		}
	}
	return out, nil
}

// PlacementWorklist is the placement machine's order-carrying read. The walk
// tests drive a single slot, so insertion order is the whole order; the rotation
// is covered in the repository, where it is implemented.
func (l *walkLedger) PlacementWorklist(ctx context.Context, playerID int, states ...string) ([]appSensing.QueuedSlot, error) {
	return l.SlotsByState(ctx, playerID, states...)
}

// MarkPlacementAttempt records the turn. It PERSISTS like the transitions above,
// so a walk that burns an attempt per tick is visible to the test as such.
func (l *walkLedger) MarkPlacementAttempt(_ context.Context, _ int, waypoint, kind string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.attempts = append(l.attempts, waypoint+"/"+kind)
	return nil
}

func (l *walkLedger) TransitionSlot(_ context.Context, _ int, tr appSensing.SlotTransition, _ appSensing.SlotFields) error {
	waypoint, from, to := tr.Waypoint, tr.From, tr.To
	l.mu.Lock()
	defer l.mu.Unlock()
	slot, ok := l.slots[waypoint]
	if !ok || slot.State != from {
		return appSensing.ErrSlotClaimed
	}
	slot.State = to
	l.transitions = append(l.transitions, waypoint+":"+from+"->"+to)
	return nil
}

func (l *walkLedger) stateOf(waypoint string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.slots[waypoint].State
}

// ---- the headline test ------------------------------------------------------

// TestPlacementWalk_HullCrossesAGateOverConsecutiveTicks is the test that
// matters, and it is the live fleet's exact shape.
//
// Every sensing probe sat in X1-KP23 because the cross-gate verb refused, so the
// only yards we could ever buy through were the ones in the system we already
// occupied — and with both of KP23's probe yards already carrying placements,
// no staging yard was free, no new charting seed could be requested, and
// expansion terminated. Landing a hull in a bordering system is what breaks
// that, and it is what this drives: a placement in X1-GF41 served by a hull
// standing in X1-KP23.
//
// The assertions are deliberately about the SEQUENCE, not about any one call:
//
//  1. every tick RETURNS (the tick is never held open by a flight or a cooldown);
//  2. the hull's system actually CHANGES (it crossed, rather than merely not
//     blocking — a refusal would also satisfy 1);
//  3. the ledger state the walk resumes from is what the previous tick wrote;
//  4. no whole-journey command is ever issued.
func TestPlacementWalk_HullCrossesAGateOverConsecutiveTicks(t *testing.T) {
	// Real symbols off the live map: X1-KP23-I53 and X1-GF41-I56 are the two
	// systems' actual jump gates, and X1-KP23-D40 is where the live TORWIND-F
	// was parked when this was measured.
	const (
		hull     = "TORWIND-F"
		slotWP   = "X1-GF41-A2"
		kp23Gate = "X1-KP23-I53"
		gf41Gate = "X1-GF41-I56"
	)

	world := newWalkWorld()
	world.gateOf["X1-KP23"] = kp23Gate
	world.gateOf["X1-GF41"] = gf41Gate
	// Parked at a market waypoint in KP23 — not on the gate, and a whole gate
	// away from the placement it has been bought for.
	world.shipAt[hull] = "X1-KP23-D40"
	world.navStatus[hull] = navigation.NavStatusInOrbit

	ledger := newWalkLedger(appSensing.QueuedSlot{
		Waypoint: slotWP, System: "X1-GF41", Kind: appSensing.SlotKindMarket,
		State: appSensing.SlotStateBought, AssignedShip: hull,
	})

	ports := appSensing.PlacementPorts{
		Ledger: ledger,
		Ships:  world,
		Mover: adapterSensing.NewMoverPort(world, stubGateNeighbours{edges: map[string][]string{
			"X1-KP23": {"X1-AJ10", "X1-GF41", "X1-MY3", "X1-QG29", "X1-XD91"},
			"X1-GF41": {"X1-KC84", "X1-KP23", "X1-RX9", "X1-UV2"},
		}}),
		Fleet: stubFleetTagger{},
	}

	startSystem := shared.ExtractSystemSymbol(world.positionOf(hull))
	require.Equal(t, "X1-KP23", startSystem, "test setup: the hull must start a gate away from its slot")

	// Four consecutive ticks of the SAME ledger and the SAME ships table. Each
	// one must finish on its own; the walk advances one step per tick.
	const ticks = 4
	for tick := 1; tick <= ticks; tick++ {
		runPlacementTick(t, tick, ports, world)
	}

	// (2) It CROSSED. This is what a refusal could never produce, and it is the
	// unlock: a hull standing in GF41 makes GF41's shipyards buyable.
	require.Equal(t, "X1-GF41", shared.ExtractSystemSymbol(world.positionOf(hull)),
		"the hull never left %s after %d ticks, so no new system was ever converted. Commands: %v",
		startSystem, ticks, world.commands())

	// It jumped, and it jumped exactly once — toward the bordering system, not
	// round in circles.
	require.Equal(t, []string{"X1-GF41"}, world.jumps(),
		"the walk did not make exactly one jump to the bordering system: %v", world.commands())

	// And it finished the job: the last leg is an in-system hop onto the slot.
	require.Equal(t, slotWP, world.positionOf(hull),
		"the hull crossed the gate but never reached its slot: %v", world.commands())

	// (3) The ledger carried the walk between ticks. The slot was advanced out of
	// BOUGHT by the first tick and every later tick resumed from that.
	require.Equal(t, appSensing.SlotStateInTransit, ledger.stateOf(slotWP),
		"the placement did not persist its progress across ticks: %v", ledger.transitions)

	// (4) Nothing that waits out a journey was ever sent.
	require.False(t, world.sentAny("NavigateRouteCommand"),
		"the walk issued the whole-journey navigate, which blocks until arrival: %v", world.commands())
	require.False(t, world.sentAny("RouteShipCommand"),
		"the walk issued the multi-jump route, which waits out every leg and cooldown: %v", world.commands())
}

// runPlacementTick runs one tick and fails the test BY TICK NUMBER if it does
// not return. Naming the tick is what distinguishes "never started" from "hung
// on the jump", which are different defects with the same symptom.
func runPlacementTick(t *testing.T, tick int, ports appSensing.PlacementPorts, world *walkWorld) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := appSensing.AdvancePlacements(ctx, ports, testPlayerID, appSensing.DefaultMaxPlacementActions)
		done <- err
	}()

	select {
	case err := <-done:
		require.NoError(t, err, "tick %d failed", tick)
	case <-time.After(tickDeadline):
		stalled := world.commands() // captured BEFORE the unblock, so it shows the stall
		cancel()
		<-done
		t.Fatalf("tick %d was still running after %v: the cross-gate walk is waiting on a "+
			"flight or a jump cooldown inside the tick, which starves every placement behind "+
			"it. Commands issued by then: %v", tick, tickDeadline, stalled)
	}
}

// ---- the seam itself --------------------------------------------------------

// TestMoverPort_RouteAcross_UsesPositionNotDistance pins the discriminator on
// the placement walk, the same trap the seed's hop documents.
//
// Orbitals share the coordinates of the body they orbit — X1-KP23-A2, A3 and A4
// all sit exactly on X1-KP23-A1 — so a hull can read ZERO distance from a
// waypoint it is not standing on. FindNearestJumpGateResponse carries a Distance
// field, and it is zero here, so an implementation that trusted it instead of
// comparing waypoint SYMBOLS would decide the hull was already on the gate and
// send the jump. The real handler would then fly the hull to the gate first and
// wait out that flight: the blocking defect, reintroduced through a plausible
// shortcut.
func TestMoverPort_RouteAcross_UsesPositionNotDistance(t *testing.T) {
	const orbital = "X1-KP23-A2" // a real orbital, and not the gate

	world := newWalkWorld()
	world.gateOf["X1-KP23"] = "X1-KP23-I53"
	world.shipAt["PROBE-A"] = orbital
	world.navStatus["PROBE-A"] = navigation.NavStatusInOrbit

	mover := adapterSensing.NewMoverPort(world, stubGateNeighbours{edges: map[string][]string{
		"X1-KP23": {"X1-GF41"},
	}})

	done := make(chan error, 1)
	go func() {
		done <- mover.RouteAcross(context.Background(), testPlayerID, "PROBE-A", orbital, "X1-GF41-A2")
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(tickDeadline):
		close(world.release)
		<-done
		t.Fatalf("a hull co-located with its gate but not on it blocked the tick after %v: %v",
			tickDeadline, world.commands())
	}

	require.True(t, world.sentAny("NavigateDirectCommand"),
		"a hull beside the gate was not moved onto it: %v", world.commands())
	require.False(t, world.sentAny("JumpShipCommand"),
		"jumped from beside the gate rather than on it: %v", world.commands())
}

// TestMoverPort_RouteAcross_JumpsToTheNextHopNotTheFinalSystem pins the routing
// choice for a destination that is not a neighbour.
//
// A jump gate reaches only the systems it is connected to. Naming the FINAL
// system on a two-hop destination is a jump the API rejects, so the hull would
// sit on the gate re-issuing a rejected command forever — a silent stall that
// looks exactly like a cooldown. The walk must name the INTERMEDIATE system it
// can actually reach, and re-derive the next one after it lands.
func TestMoverPort_RouteAcross_JumpsToTheNextHopNotTheFinalSystem(t *testing.T) {
	world := newWalkWorld()
	world.gateOf["X1-KP23"] = "X1-KP23-I53"
	world.gateOf["X1-MY3"] = "X1-MY3-C20F"
	// Standing ON the gate, so the step under test is the jump itself.
	world.shipAt["PROBE-A"] = "X1-KP23-I53"
	world.navStatus["PROBE-A"] = navigation.NavStatusInOrbit

	// X1-BT49 is two hops out: KP23 -> MY3 -> BT49, exactly as the live gate
	// graph has it.
	mover := adapterSensing.NewMoverPort(world, stubGateNeighbours{edges: map[string][]string{
		"X1-KP23": {"X1-MY3", "X1-QG29"},
		"X1-MY3":  {"X1-BT49", "X1-KP23", "X1-MC90"},
		"X1-QG29": {"X1-KP23"},
	}})

	done := make(chan error, 1)
	go func() {
		done <- mover.RouteAcross(context.Background(), testPlayerID, "PROBE-A", "X1-KP23-I53", "X1-BT49-A1")
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(tickDeadline):
		close(world.release)
		<-done
		t.Fatalf("the two-hop walk blocked after %v: %v", tickDeadline, world.commands())
	}

	require.Equal(t, []string{"X1-MY3"}, world.jumps(),
		"the walk jumped somewhere other than the reachable intermediate system — naming the "+
			"final system is a jump the API rejects: %v", world.commands())
}

// TestMoverPort_RouteAcross_FailsClosedWithoutMovingWhenUnroutable keeps the
// money-safe property the old refusal had.
//
// When no gate route can be named, the walk must move NOTHING: a flight to a
// gate it cannot leave through is spent fuel and a hull further from anywhere
// useful. Holding the placement exactly as it was leaves the hull still named by
// the row — so the probe cap still counts it, the over-count direction the money
// guard requires — and the next tick retries for free.
func TestMoverPort_RouteAcross_FailsClosedWithoutMovingWhenUnroutable(t *testing.T) {
	world := newWalkWorld()
	world.gateOf["X1-KP23"] = "X1-KP23-I53"
	world.shipAt["PROBE-A"] = "X1-KP23-D40"
	world.navStatus["PROBE-A"] = navigation.NavStatusInOrbit

	// The stored graph knows KP23's neighbours, and X1-ZZ99 is not among them
	// nor reachable through them.
	mover := adapterSensing.NewMoverPort(world, stubGateNeighbours{edges: map[string][]string{
		"X1-KP23": {"X1-GF41"},
		"X1-GF41": {"X1-KP23"},
	}})

	done := make(chan error, 1)
	go func() {
		done <- mover.RouteAcross(context.Background(), testPlayerID, "PROBE-A", "X1-KP23-D40", "X1-ZZ99-M1")
	}()

	select {
	case err := <-done:
		require.Error(t, err, "an unroutable destination reported success without moving anything")
	case <-time.After(tickDeadline):
		close(world.release)
		<-done
		t.Fatalf("the unroutable walk blocked after %v: %v", tickDeadline, world.commands())
	}

	require.False(t, world.sentAny("NavigateDirectCommand"),
		"the hull was flown toward a gate it has no route out of: %v", world.commands())
	require.False(t, world.sentAny("JumpShipCommand"),
		"a jump was attempted with no next system named: %v", world.commands())
	require.Equal(t, "X1-KP23-D40", world.positionOf("PROBE-A"),
		"the hull moved despite the walk failing closed: %v", world.commands())
}

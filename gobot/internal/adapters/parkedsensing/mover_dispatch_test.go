package parkedsensing_test

// Non-blocking dispatch tests for the sensing movement adapters.
//
// WHAT THESE PIN, and why it is not "navigation was called". The sensing engines
// are written as tick machines: one action per errand per tick, nothing held in
// memory between ticks, and the NEXT tick reads the ships table to see what
// happened. Both port contracts say so in as many words — ShipMover's placement
// machine documents that "a hull dispatched by this tick is not also examined for
// arrival by it", and SeedCommander opens with "no retry and no waiting: the tick
// issues one and returns".
//
// A movement command that runs the whole journey therefore does not merely make
// one placement slow: it holds the tick open for the length of a flight, and
// EVERYTHING BEHIND IT IN THE TICK STARVES — the hulls that have already landed
// and are waiting to be docked and stood down, and every later stage, including
// the expansion pass where charting seeds launch. That starvation is the defect,
// so it is what the first test asserts, with a mediator that blocks exactly where
// the real one blocks.
//
// The fake mediator is the load-bearing part: it blocks on the whole-journey
// commands (NavigateRouteCommand, RouteShipCommand — both end in
// WaitForShipArrival) and returns immediately on the dispatch-only ones. Wire the
// REAL adapter to it and a blocking command really does stall the real placement
// loop, which is what makes this a reproduction rather than a restatement.

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

// tickDeadline bounds how long one placement tick may take with a hull in
// flight. Generous by three orders of magnitude against the work actually done
// (a handful of in-memory stub calls), so a failure means the tick is genuinely
// waiting on something rather than merely being slow.
const tickDeadline = 3 * time.Second

// ---- fakes ------------------------------------------------------------------

// journeyMediator answers the navigation commands the way the real mediator
// does: the whole-journey verbs block until the hull lands, the dispatch verbs
// return as soon as the API has accepted the command.
type journeyMediator struct {
	mu   sync.Mutex
	sent []string

	// gate is the jump gate FindNearestJumpGateQuery reports, and shipAt is
	// where the hull actually stands. They are separate because the ONLY
	// question the gate-hop seam turns on is whether those two are the same
	// waypoint.
	gate   string
	shipAt string

	// release is never closed while a test body runs. A journey command parks on
	// it exactly as WaitForShipArrival parks on the arrival event.
	release chan struct{}
}

func newJourneyMediator() *journeyMediator {
	return &journeyMediator{release: make(chan struct{})}
}

func (m *journeyMediator) Send(ctx context.Context, request mediator.Request) (mediator.Response, error) {
	m.mu.Lock()
	m.sent = append(m.sent, reflect.TypeOf(request).String())
	m.mu.Unlock()

	// The whole journey: plan the route, fly it, and WAIT for the arrival before
	// returning. This is the real blocking behaviour, reproduced.
	waitOutTheFlight := func() {
		select {
		case <-m.release:
		case <-ctx.Done():
		}
	}

	if _, ok := request.(*shipQueries.FindNearestJumpGateQuery); ok {
		gate, err := shared.NewWaypoint(m.gate, 0, 0)
		if err != nil {
			return nil, err
		}
		gate.Type = "JUMP_GATE"
		return &shipQueries.FindNearestJumpGateResponse{JumpGate: gate}, nil
	}

	switch request.(type) {
	case *shipNav.JumpShipCommand:
		// Models the real handler faithfully. A hull already standing on the
		// gate jumps immediately — the API jump is instantaneous. A hull that is
		// NOT on the gate makes the handler navigate it there first, through
		// NavigateRouteCommand, which waits out that whole flight. So reaching
		// this command off-gate is itself the defect.
		if m.shipAt != m.gate {
			waitOutTheFlight()
		}
		return &shipNav.JumpShipResponse{Success: true, JumpGateSymbol: m.gate}, nil
	case *shipNav.NavigateRouteCommand, *shipNav.RouteShipCommand:
		waitOutTheFlight()
		return &shipNav.NavigateRouteResponse{Status: "completed"}, nil
	case *shipTypes.NavigateDirectCommand:
		// Dispatch only: the API has accepted the move and the hull is now
		// IN_TRANSIT with an arrival time. Nothing waits.
		return &shipTypes.NavigateDirectResponse{Status: "navigating"}, nil
	}
	return nil, nil
}

func (m *journeyMediator) Register(reflect.Type, mediator.RequestHandler) error { return nil }
func (m *journeyMediator) RegisterMiddleware(mediator.Middleware)               {}

// commands returns the command type names sent so far, in order.
func (m *journeyMediator) commands() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.sent...)
}

// sentAny reports whether any command whose type name contains needle was sent.
func (m *journeyMediator) sentAny(needle string) bool {
	for _, name := range m.commands() {
		if strings.Contains(name, needle) {
			return true
		}
	}
	return false
}

// stubPlacementLedger is the placement machine's ledger slice, in memory.
type stubPlacementLedger struct {
	mu          sync.Mutex
	slots       []appSensing.QueuedSlot
	transitions []string
	attempts    []string
}

func (l *stubPlacementLedger) SlotsByState(context.Context, int, ...string) ([]appSensing.QueuedSlot, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]appSensing.QueuedSlot(nil), l.slots...), nil
}

// PlacementWorklist is the order-carrying read the placement machine uses. This
// stub returns insertion order: these tests assert the ADAPTER's movement
// behaviour, and the rotation itself is covered where it lives, in the repository.
func (l *stubPlacementLedger) PlacementWorklist(context.Context, int, ...string) ([]appSensing.QueuedSlot, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]appSensing.QueuedSlot(nil), l.slots...), nil
}

func (l *stubPlacementLedger) MarkPlacementAttempt(_ context.Context, _ int, waypoint, kind string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.attempts = append(l.attempts, waypoint+"/"+kind)
	return nil
}

func (l *stubPlacementLedger) TransitionSlot(_ context.Context, _ int, waypoint, _, from, to string, _ appSensing.SlotFields) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.transitions = append(l.transitions, waypoint+":"+from+"->"+to)
	return nil
}

// stubShipReader answers ship positions from a fixed map.
type stubShipReader struct{ positions map[string]appSensing.ShipPos }

func (stubShipReader) DockedProbeAt(context.Context, int, string) (string, bool, error) {
	return "", false, nil
}

func (s stubShipReader) ShipAt(_ context.Context, _ int, shipSymbol string) (appSensing.ShipPos, error) {
	return s.positions[shipSymbol], nil
}

type stubFleetTagger struct{}

func (stubFleetTagger) AssignFleet(context.Context, int, string, string) error { return nil }

// ---- the test that matters --------------------------------------------------

// TestPlacementTick_HullInFlightDoesNotStarveTheHullsBehindIt reproduces the live
// fleet's exact shape and asserts the tick still finishes its work.
//
// On the live fleet six placements were IN_TRANSIT. ONE of them (TORWIND-F, sat
// in orbit at X1-KP23-A2, nowhere near its slot at D40) needed flying, and it is
// first in the ledger read. The other four had ALREADY ARRIVED — the ships table
// said IN_ORBIT at their own slot waypoints — and needed nothing but a dock
// command to be stood down and start scanning. They never got one, because the
// tick was parked inside the first hull's flight.
//
// So the assertion is not that a navigate was issued. It is that the four hulls
// BEHIND the flight were still served in the same tick.
func TestPlacementTick_HullInFlightDoesNotStarveTheHullsBehindIt(t *testing.T) {
	med := newJourneyMediator()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	arrived := []struct{ hull, waypoint string }{
		{"TORWIND-14", "X1-KP23-D41"},
		{"TORWIND-13", "X1-KP23-F46"},
		{"TORWIND-15", "X1-KP23-G48"},
		{"TORWIND-4", "X1-KP23-H50"},
	}

	// The hull that must be flown goes FIRST, exactly as the live ledger read
	// ordered it: everything after it is what starves.
	slots := []appSensing.QueuedSlot{{
		Waypoint: "X1-KP23-D40", System: "X1-KP23", Kind: appSensing.SlotKindMarket,
		State: appSensing.SlotStateInTransit, AssignedShip: "TORWIND-F",
	}}
	positions := map[string]appSensing.ShipPos{
		// Sitting still at A2, which is NOT its slot: the placement machine's
		// claim branch has to fly it.
		"TORWIND-F": {Waypoint: "X1-KP23-A2", NavStatus: navigation.NavStatusInOrbit, Found: true},
	}
	for _, a := range arrived {
		slots = append(slots, appSensing.QueuedSlot{
			Waypoint: a.waypoint, System: "X1-KP23", Kind: appSensing.SlotKindMarket,
			State: appSensing.SlotStateInTransit, AssignedShip: a.hull,
		})
		// Landed and in orbit ON its slot: one dock command away from parking.
		positions[a.hull] = appSensing.ShipPos{
			Waypoint: a.waypoint, NavStatus: navigation.NavStatusInOrbit, Found: true,
		}
	}

	ledger := &stubPlacementLedger{slots: slots}
	ports := appSensing.PlacementPorts{
		Ledger: ledger,
		Ships:  stubShipReader{positions: positions},
		Mover:  adapterSensing.NewMoverPort(med, stubGateNeighbours{}),
		Fleet:  stubFleetTagger{},
	}

	type outcome struct {
		rep appSensing.PlacementReport
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		rep, err := appSensing.AdvancePlacements(ctx, ports, testPlayerID, appSensing.DefaultMaxPlacementActions)
		done <- outcome{rep, err}
	}()

	var got outcome
	select {
	case got = <-done:
	case <-time.After(tickDeadline):
		stalled := med.commands() // captured BEFORE the unblock, so it shows the stall
		cancel()                  // unblock the parked command so the goroutine can unwind
		<-done
		t.Fatalf("the placement tick was still running after %v with one hull in flight: "+
			"it is waiting out the flight, so the %d hulls behind it that had already "+
			"arrived were never docked. Commands issued by then: %v",
			tickDeadline, len(arrived), stalled)
	}

	require.NoError(t, got.err)

	// The flight WAS dispatched — this test must not pass by skipping the move.
	require.Equal(t, 1, got.rep.Dispatched,
		"the hull that needed flying was not dispatched at all: %v", med.commands())

	// And every hull behind it was served in the SAME tick. This is the defect.
	require.Equal(t, len(arrived), got.rep.Docking,
		"hulls that had already arrived were starved by the hull in flight: %v", med.commands())

	// Nothing in the tick may send a command that waits out a journey.
	require.False(t, med.sentAny("NavigateRouteCommand"),
		"the placement tick sent the whole-journey navigate, which blocks until arrival: %v", med.commands())
}

// ---- the seam itself --------------------------------------------------------

// TestMoverPort_NavigateWithin_DispatchesAndReturns pins the command choice that
// makes the tick above possible. NavigateRouteCommand plans and flies the whole
// route and ends in WaitForShipArrival; NavigateDirectCommand hands the move to
// the API and returns with the arrival time, leaving the ships table and the
// arrival scheduler to record the landing for a later tick to read.
func TestMoverPort_NavigateWithin_DispatchesAndReturns(t *testing.T) {
	med := newJourneyMediator()
	mover := adapterSensing.NewMoverPort(med, stubGateNeighbours{})

	done := make(chan error, 1)
	go func() {
		done <- mover.NavigateWithin(context.Background(), testPlayerID, "PROBE-A", "X1-AA-M1")
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(tickDeadline):
		close(med.release)
		<-done
		t.Fatalf("NavigateWithin was still waiting after %v — it sent a command that "+
			"waits out the flight: %v", tickDeadline, med.commands())
	}

	require.True(t, med.sentAny("NavigateDirectCommand"),
		"NavigateWithin did not issue the dispatch-only navigate: %v", med.commands())
	require.False(t, med.sentAny("NavigateRouteCommand"),
		"NavigateWithin issued the whole-journey navigate, which blocks until arrival: %v", med.commands())
}

// The cross-gate verb's contract lives in gate_walk_test.go.
//
// RouteAcross used to REFUSE every crossing, and a test here pinned that refusal.
// The refusal was safe but it was a wall: a probe could never leave the system it
// was bought in, so only that system's yards were ever usable and the frontier
// could not widen. It now WALKS the crossing a step per tick instead.
//
// The property that test was really protecting — the tick never waits on a
// flight or a cooldown — did not go away with it; it moved next door and got
// stronger, because proving it of a WALK takes consecutive ticks rather than one
// call. See TestPlacementWalk_HullCrossesAGateOverConsecutiveTicks, and
// TestMoverPort_RouteAcross_FailsClosedWithoutMovingWhenUnroutable for the
// money-safe hold this refusal used to provide.

// TestSeedCommandPort_JumpTo_WalksTheGateHopOneStepPerTick is the seed's version
// of the headline case, and it is on the critical path rather than latent: seeds
// launching is the whole point of unblocking the tick, and 13 systems have never
// had one.
//
// A gate hop is TWO physical moves — fly to the gate, then jump off it — and only
// the first is a flight. JumpShipCommand does both, so it waits out that flight
// inside the tick. The fix is to do the flight leg here, dispatch-only, and send
// the jump command only once the hull is standing on the gate, where its navigate
// branch is unreachable.
//
// The two subtests are consecutive ticks of one errand, so together they prove the
// seed actually CROSSES rather than merely failing to block.
func TestSeedCommandPort_JumpTo_WalksTheGateHopOneStepPerTick(t *testing.T) {
	const gate = "X1-AA-J1"

	t.Run("tick 1: off the gate, dispatches the hop and returns", func(t *testing.T) {
		med := newJourneyMediator()
		med.gate, med.shipAt = gate, "X1-AA-M1" // parked at its old slot, not the gate
		seed := adapterSensing.NewSeedCommandPort(med, nil, nil, nil, nil, seedHopGates)

		done := make(chan error, 1)
		go func() {
			done <- seed.JumpTo(context.Background(), testPlayerID, "PROBE-A", med.shipAt, "X1-BB")
		}()

		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(tickDeadline):
			close(med.release)
			<-done
			t.Fatalf("the seed's gate hop was still waiting after %v — the tick is parked "+
				"inside the flight to the gate, so expansion never finishes and no seed "+
				"ever charts: %v", tickDeadline, med.commands())
		}

		require.True(t, med.sentAny("NavigateDirectCommand"),
			"the hop to the gate was not dispatched: %v", med.commands())
		require.False(t, med.sentAny("JumpShipCommand"),
			"sent the jump from off the gate, which makes the handler fly the hull there "+
				"and wait: %v", med.commands())
		require.False(t, med.sentAny("NavigateRouteCommand"),
			"used the whole-journey navigate for the hop to the gate: %v", med.commands())
	})

	t.Run("tick 2: standing on the gate, jumps", func(t *testing.T) {
		med := newJourneyMediator()
		med.gate, med.shipAt = gate, gate // the hop above has landed
		seed := adapterSensing.NewSeedCommandPort(med, nil, nil, nil, nil, seedHopGates)

		done := make(chan error, 1)
		go func() {
			done <- seed.JumpTo(context.Background(), testPlayerID, "PROBE-A", gate, "X1-BB")
		}()

		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(tickDeadline):
			close(med.release)
			<-done
			t.Fatalf("the jump was still waiting after %v: %v", tickDeadline, med.commands())
		}

		require.True(t, med.sentAny("JumpShipCommand"),
			"a hull standing on the gate did not jump: %v", med.commands())
		require.False(t, med.sentAny("NavigateDirectCommand"),
			"moved a hull that was already on its gate: %v", med.commands())
	})
}

// TestSeedCommandPort_JumpTo_UsesPositionNotDistance pins the discriminator.
//
// Orbitals share coordinates with the body they orbit, so a hull can be ZERO
// distance from a gate it is not standing on. An implementation that read the
// query's Distance instead of comparing waypoints would send the jump from
// off-gate, and the handler would fly the hull there and wait — the exact defect,
// reintroduced through a plausible shortcut.
func TestSeedCommandPort_JumpTo_UsesPositionNotDistance(t *testing.T) {
	med := newJourneyMediator()
	// Co-located with the gate (an orbital of the same body) but NOT on it.
	//
	// A REAL THREE-SEGMENT SYMBOL, as X1-KP23-A2 is in the placement walk's
	// version of this test. It used to read "X1-AA-J1-MOON", which expressed
	// co-location by suffix and cannot occur: every one of the 25,208 waypoints
	// in the live cache has exactly three segments, and ExtractSystemSymbol —
	// which strips everything after the LAST hyphen — reads that shape as the
	// system "X1-AA-J1". Harmless while JumpTo never asked what system the hull
	// was in; the moment the crossing resolved a route it was asking about a
	// system that does not exist. The discriminator under test is untouched:
	// gate != fromWaypoint still separates "on the gate" from "beside it", and
	// the mediator still hangs if a jump is sent from off-gate.
	med.gate, med.shipAt = "X1-AA-J1", "X1-AA-J2"
	seed := adapterSensing.NewSeedCommandPort(med, nil, nil, nil, nil, seedHopGates)

	done := make(chan error, 1)
	go func() {
		done <- seed.JumpTo(context.Background(), testPlayerID, "PROBE-A", med.shipAt, "X1-BB")
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(tickDeadline):
		close(med.release)
		<-done
		t.Fatalf("a hull co-located with its gate but not on it blocked the tick after %v: %v",
			tickDeadline, med.commands())
	}

	require.True(t, med.sentAny("NavigateDirectCommand"),
		"a hull beside the gate was not moved onto it: %v", med.commands())
	require.False(t, med.sentAny("JumpShipCommand"),
		"jumped from beside the gate rather than on it: %v", med.commands())
}

// TestSeedCommandPort_NavigateTo_DispatchesAndReturns pins the same seam on the
// charting seed's tour hop. SeedCommander's contract is explicit — "no retry and
// no waiting: the tick issues one and returns" — and the expansion pass that
// drives it already skips a hull the ships table reports IN_TRANSIT, so a waiting
// hop buys nothing and holds the tick open for a whole leg of the tour.
func TestSeedCommandPort_NavigateTo_DispatchesAndReturns(t *testing.T) {
	med := newJourneyMediator()
	seed := adapterSensing.NewSeedCommandPort(med, nil, nil, nil, nil, seedHopGates)

	done := make(chan error, 1)
	go func() {
		done <- seed.NavigateTo(context.Background(), testPlayerID, "PROBE-A", "X1-AA-M1")
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(tickDeadline):
		close(med.release)
		<-done
		t.Fatalf("the seed's NavigateTo was still waiting after %v — it sent a command "+
			"that waits out the flight: %v", tickDeadline, med.commands())
	}

	require.True(t, med.sentAny("NavigateDirectCommand"),
		"the seed's NavigateTo did not issue the dispatch-only navigate: %v", med.commands())
	require.False(t, med.sentAny("NavigateRouteCommand"),
		"the seed's NavigateTo issued the whole-journey navigate, which blocks until arrival: %v", med.commands())
}

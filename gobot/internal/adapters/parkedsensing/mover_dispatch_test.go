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
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
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

	switch request.(type) {
	case *shipNav.NavigateRouteCommand, *shipNav.RouteShipCommand:
		// The whole journey: plan the route, fly it, and WAIT for the arrival
		// before returning. This is the real blocking behaviour, reproduced.
		select {
		case <-m.release:
		case <-ctx.Done():
		}
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
}

func (l *stubPlacementLedger) SlotsByState(context.Context, int, ...string) ([]appSensing.QueuedSlot, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]appSensing.QueuedSlot(nil), l.slots...), nil
}

func (l *stubPlacementLedger) TransitionSlot(_ context.Context, _ int, waypoint, from, to string, _ appSensing.SlotFields) error {
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
		Mover:  adapterSensing.NewMoverPort(med),
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
	mover := adapterSensing.NewMoverPort(med)

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

// TestMoverPort_RouteAcross_RefusesRatherThanBlocks pins the cross-gate verb.
//
// The only gate-crossing machinery available resolves a whole multi-jump path and
// flies it, waiting out every leg and every jump cooldown — so sending it from a
// tick is the same defect as the in-system case and worse, since one hull could
// hold the sensing engine shut for hours. It refuses instead, which returns the
// placement to its ordinary free retry with the hull still named by the row (an
// over-count, the money-safe direction) and the tick still running.
func TestMoverPort_RouteAcross_RefusesRatherThanBlocks(t *testing.T) {
	med := newJourneyMediator()
	mover := adapterSensing.NewMoverPort(med)

	done := make(chan error, 1)
	go func() {
		done <- mover.RouteAcross(context.Background(), testPlayerID, "PROBE-A", "X1-ZZ-M1")
	}()

	select {
	case err := <-done:
		require.Error(t, err, "RouteAcross reported success without moving anything")
	case <-time.After(tickDeadline):
		close(med.release)
		<-done
		t.Fatalf("RouteAcross was still waiting after %v — it sent a command that flies "+
			"the whole gate path: %v", tickDeadline, med.commands())
	}

	require.False(t, med.sentAny("RouteShipCommand"),
		"RouteAcross issued the multi-jump route, which waits out every leg: %v", med.commands())
}

// TestSeedCommandPort_NavigateTo_DispatchesAndReturns pins the same seam on the
// charting seed's tour hop. SeedCommander's contract is explicit — "no retry and
// no waiting: the tick issues one and returns" — and the expansion pass that
// drives it already skips a hull the ships table reports IN_TRANSIT, so a waiting
// hop buys nothing and holds the tick open for a whole leg of the tour.
func TestSeedCommandPort_NavigateTo_DispatchesAndReturns(t *testing.T) {
	med := newJourneyMediator()
	seed := adapterSensing.NewSeedCommandPort(med, nil, nil, nil, nil)

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

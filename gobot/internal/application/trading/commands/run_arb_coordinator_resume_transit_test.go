package commands

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	navCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// --- sp-8l3o: the resume path must WAIT out a hull re-adopted mid-transit before any
// movement, not attempt the jump/navigate now (API 4214 'in-transit' → iteration error
// → the container burns its whole restart budget riding out a routine arrival). ---

// readyArrivalSubscriber is a controllable ShipEventSubscriber whose SubscribeArrived
// hands back a channel already carrying one ARRIVED event, so WaitForShipArrival takes
// its event fast-path and returns immediately (no real clock wait) — the same fake shape
// arrival_wait_test.go uses. subscribed counts subscriptions so a test can prove the
// in-transit wait actually engaged.
type readyArrivalSubscriber struct {
	navigation.ShipEventSubscriber // embeds the wide interface; only the arrival pair is exercised
	ch                             chan navigation.ShipArrivedEvent
	subscribed                     int
}

func newReadyArrivalSubscriber(shipSymbol, location string) *readyArrivalSubscriber {
	ch := make(chan navigation.ShipArrivedEvent, 1)
	ch <- navigation.ShipArrivedEvent{
		ShipSymbol: shipSymbol,
		PlayerID:   shared.MustNewPlayerID(1),
		Location:   location,
		Status:     navigation.NavStatusInOrbit,
	}
	return &readyArrivalSubscriber{ch: ch}
}

func (s *readyArrivalSubscriber) SubscribeArrived(string) <-chan navigation.ShipArrivedEvent {
	s.subscribed++
	return s.ch
}
func (s *readyArrivalSubscriber) UnsubscribeArrived(string, <-chan navigation.ShipArrivedEvent) {}

// transitAwareMediator answers movement (JumpShipCommand / NavigateRouteCommand) by
// faithfully modelling the live API's 4214 rejection: a movement dispatched while the
// hull is still IN_TRANSIT fails (and is recorded), exactly as the incident's jump did;
// once the hull has arrived (IN_ORBIT) the movement succeeds. It holds the SAME ship
// pointer travel() operates on (the single-pointer repo returns it), so its nav-status
// check reflects the hull's real state at movement time. Buy/sell are recorded so a
// resume test can assert the held tranche is delivered once.
type transitAwareMediator struct {
	ship                   *navigation.Ship
	jumps                  int
	navigates              int
	purchases              []*shipCargo.PurchaseCargoCommand
	sells                  []*shipCargo.SellCargoCommand
	movementWhileInTransit bool
	jumpResp               *navCmd.JumpShipResponse
	navErr                 error // an injected NON-transit movement failure (existing-semantics test)
}

func (m *transitAwareMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	switch cmd := request.(type) {
	case *navCmd.JumpShipCommand:
		m.jumps++
		if m.ship.NavStatus() == navigation.NavStatusInTransit {
			m.movementWhileInTransit = true
			return nil, fmt.Errorf("API 4214: ship %s is currently in-transit and must arrive before it can jump", cmd.ShipSymbol)
		}
		return m.jumpResp, nil
	case *navCmd.NavigateRouteCommand:
		m.navigates++
		if m.ship.NavStatus() == navigation.NavStatusInTransit {
			m.movementWhileInTransit = true
			return nil, fmt.Errorf("API 4214: ship %s is currently in-transit and must arrive before it can navigate", cmd.ShipSymbol)
		}
		if m.navErr != nil {
			return nil, m.navErr
		}
		return nil, nil
	case *shipCargo.PurchaseCargoCommand:
		m.purchases = append(m.purchases, cmd)
		return &shipCargo.PurchaseCargoResponse{TotalCost: cmd.Units * trSourceAsk, UnitsAdded: cmd.Units, TransactionCount: 1}, nil
	case *shipCargo.SellCargoCommand:
		m.sells = append(m.sells, cmd)
		return &shipCargo.SellCargoResponse{TotalRevenue: cmd.Units * trSellRevenue, UnitsSold: cmd.Units, TransactionCount: 1}, nil
	default:
		return nil, nil // dock, etc. succeed silently
	}
}

func (m *transitAwareMediator) Register(reflect.Type, common.RequestHandler) error { return nil }
func (m *transitAwareMediator) RegisterMiddleware(common.Middleware)               {}

// newTransitHaulerHolding builds a hull IN_TRANSIT holding `held` units of `good` — the
// physical state a prior arb attempt leaves when the container is re-adopted mid-hop
// (bought, then killed mid in-system flight): the sp-5nqx cargo-aboard resume detector
// fires (skip the buy), and the hull is still IN_TRANSIT (the sp-8l3o wait must fire).
func newTransitHaulerHolding(t *testing.T, symbol, waypoint, good string, held int) *navigation.Ship {
	t.Helper()
	cargo, err := shared.NewCargo(40, 0, nil)
	if err != nil {
		t.Fatalf("cargo: %v", err)
	}
	fuel, err := shared.NewFuel(100, 100)
	if err != nil {
		t.Fatalf("fuel: %v", err)
	}
	wp, err := shared.NewWaypoint(waypoint, 0, 0)
	if err != nil {
		t.Fatalf("waypoint: %v", err)
	}
	ship, err := navigation.NewShip(
		symbol, shared.MustNewPlayerID(1), wp, fuel, 100, 40, cargo, 30,
		"FRAME_LIGHT_FREIGHTER", "HAULER", nil, navigation.NavStatusInTransit,
	)
	if err != nil {
		t.Fatalf("ship: %v", err)
	}
	if held > 0 {
		if err := ship.ReceiveCargo(&shared.CargoItem{Symbol: good, Units: held}); err != nil {
			t.Fatalf("preload cargo: %v", err)
		}
	}
	return ship
}

// newTransitShipAtGate builds a hull IN_TRANSIT sitting on a JUMP_GATE waypoint — the
// exact incident state: arb-run-TORWIND-21 was re-adopted mid the in-system hop toward
// its source gate, so its cached location was the gate and IsJumpGate() was true, which
// is why travel() went straight to the jump and 4214'd. cargo held so the resume detector
// treats the buy as done.
func newTransitShipAtGate(t *testing.T, symbol, gateSymbol, good string, held int) *navigation.Ship {
	t.Helper()
	cargo, err := shared.NewCargo(40, 0, nil)
	if err != nil {
		t.Fatalf("cargo: %v", err)
	}
	fuel, err := shared.NewFuel(100, 100)
	if err != nil {
		t.Fatalf("fuel: %v", err)
	}
	wp, err := shared.NewWaypoint(gateSymbol, 0, 0)
	if err != nil {
		t.Fatalf("waypoint: %v", err)
	}
	wp.Type = "JUMP_GATE"
	ship, err := navigation.NewShip(
		symbol, shared.MustNewPlayerID(1), wp, fuel, 100, 40, cargo, 30,
		"FRAME_LIGHT_FREIGHTER", "HAULER", nil, navigation.NavStatusInTransit,
	)
	if err != nil {
		t.Fatalf("ship: %v", err)
	}
	if held > 0 {
		if err := ship.ReceiveCargo(&shared.CargoItem{Symbol: good, Units: held}); err != nil {
			t.Fatalf("preload cargo: %v", err)
		}
	}
	return ship
}

// THE incident, in isolation on the shared travel primitive: a cross-system leg entered
// with the hull still IN_TRANSIT on its source gate must wait out the arrival FIRST, then
// jump — never jump while in-transit. With the arrival wait wired, the jump the mediator
// would 4214 on an in-transit hull instead lands on an arrived one and succeeds, so
// travel() returns no error and never records a movement dispatched while in-transit.
func TestTravel_ResumeInTransit_WaitsBeforeJump_NoTransitError(t *testing.T) {
	ship := newTransitShipAtGate(t, "ARB-8L3O", "X1-AAA-GATE", trGood, 20)
	mediator := &transitAwareMediator{
		ship:     ship,
		jumpResp: &navCmd.JumpShipResponse{Success: true, DestinationSystem: "X1-BBB", CooldownSeconds: 60},
	}
	sub := newReadyArrivalSubscriber(ship.ShipSymbol(), "X1-AAA-GATE")
	// single-pointer repo: the post-wait AND post-jump reloads both return this hull, so
	// the mediator's nav-status check sees exactly what the wait did to it.
	shipRepo := &trFakeShipRepo{ship: ship}
	handler := NewRunTradeRouteCoordinatorHandler(mediator, shipRepo, nil, nil, &trFakeClock{}, nil)
	handler.SetEventSubscriber(sub)

	_, err := handler.travel(context.Background(), ship, "X1-BBB-MARKET", 1)
	if err != nil {
		t.Fatalf("travel must wait out the in-transit arrival then jump cleanly, got error: %v", err)
	}
	if sub.subscribed == 0 {
		t.Fatal("the in-transit hull must subscribe for its arrival event before moving (sp-8l3o wait did not engage)")
	}
	if mediator.movementWhileInTransit {
		t.Fatal("a jump/navigate was dispatched while the hull was still IN_TRANSIT — the pre-movement arrival wait did not run (sp-8l3o)")
	}
	if mediator.jumps != 1 {
		t.Fatalf("expected exactly one jump (after arrival), got %d", mediator.jumps)
	}
}

// The control that proves the wait is load-bearing: with NO subscriber wired (the
// pre-fix state), travel() attempts the jump while the hull is still IN_TRANSIT and the
// API rejects it 4214 — the exact iteration error the incident's restart backoff had to
// ride out. This is what SetEventSubscriber converts from an error into a wait.
func TestTravel_ResumeInTransit_NoSubscriber_Jumps4214_PreFixBehavior(t *testing.T) {
	ship := newTransitShipAtGate(t, "ARB-8L3O-CTRL", "X1-AAA-GATE", trGood, 20)
	mediator := &transitAwareMediator{
		ship:     ship,
		jumpResp: &navCmd.JumpShipResponse{Success: true, DestinationSystem: "X1-BBB", CooldownSeconds: 60},
	}
	shipRepo := &trFakeShipRepo{ship: ship}
	handler := NewRunTradeRouteCoordinatorHandler(mediator, shipRepo, nil, nil, &trFakeClock{}, nil)
	// no SetEventSubscriber → the pre-movement wait is skipped (fail-open)

	_, err := handler.travel(context.Background(), ship, "X1-BBB-MARKET", 1)
	if err == nil {
		t.Fatal("without the arrival wait, jumping an in-transit hull must surface the 4214 error (the pre-fix incident)")
	}
	if !mediator.movementWhileInTransit {
		t.Fatal("expected the jump to have been attempted while IN_TRANSIT (the defect this bead fixes)")
	}
}

// End-to-end (the arb Handle resume path): a run re-adopted holding its tranche AND still
// IN_TRANSIT must ride out the arrival, then travel→dock→sell and complete with ZERO
// iteration errors — never the 4214 that consumed the whole restart budget. Same-system
// lane, so the movement leg is a navigate (the arb resume path's travel primitive is
// shared; the wait fires before EITHER movement verb).
func TestArbCoordinator_ResumeMidTransit_RidesOutArrival_CompletesNoError(t *testing.T) {
	ship := newTransitHaulerHolding(t, "ARB-RESUME-TRANSIT", trSource, trGood, 12)
	mediator := &transitAwareMediator{ship: ship}
	sub := newReadyArrivalSubscriber(ship.ShipSymbol(), trDest)
	marketRepo := &trFakeMarketRepo{fixture: &trFixture{}}
	shipRepo := &trFakeShipRepo{ship: ship}
	h := NewRunArbCoordinatorHandler(mediator, shipRepo, marketRepo, nil, nil, nil)
	h.SetEventSubscriber(sub)

	resp, err := h.Handle(context.Background(), &RunArbCoordinatorCommand{
		ShipSymbol: ship.ShipSymbol(),
		Good:       trGood,
		BuyAt:      trSource,
		SellAt:     trDest,
		PlayerID:   1,
	})
	if err != nil {
		t.Fatalf("a resumed-in-transit run must ride out the arrival and complete, got error: %v", err)
	}
	arb := arbResponse(t, resp)
	if !arb.Completed || arb.Aborted {
		t.Fatalf("expected a completed delivery, got %+v", arb)
	}
	if mediator.movementWhileInTransit {
		t.Fatal("movement was dispatched while the hull was still IN_TRANSIT — the pre-movement arrival wait did not run (sp-8l3o)")
	}
	if sub.subscribed == 0 {
		t.Fatal("the in-transit resume must subscribe for the arrival event")
	}
	// The held tranche is delivered once in full, and NEVER re-bought (sp-5nqx still holds).
	if len(mediator.purchases) != 0 {
		t.Fatalf("a resumed run must never re-buy, got %d purchases", len(mediator.purchases))
	}
	if len(mediator.sells) != 1 || arb.UnitsTraded != 12 {
		t.Fatalf("the resumed tranche must be delivered once in full: got %d sells, %d units", len(mediator.sells), arb.UnitsTraded)
	}
}

// Existing failure semantics are preserved: a resume whose hull is NOT in transit and
// whose movement leg fails for a genuine (non-4214) reason must still surface that error
// — the pre-movement wait only rides out an IN_TRANSIT hull, it never swallows a real
// movement failure.
func TestArbCoordinator_ResumeNotInTransit_MovementError_StillFails(t *testing.T) {
	ship := newTradeHauler(t, "ARB-RESUME-FAIL") // NavStatusDocked, not in transit
	if err := ship.ReceiveCargo(&shared.CargoItem{Symbol: trGood, Units: 12}); err != nil {
		t.Fatalf("preload cargo: %v", err)
	}
	mediator := &transitAwareMediator{
		ship:   ship,
		navErr: fmt.Errorf("injected genuine navigate failure (insufficient fuel) - not an in-transit wait"),
	}
	sub := newReadyArrivalSubscriber(ship.ShipSymbol(), trDest)
	marketRepo := &trFakeMarketRepo{fixture: &trFixture{}}
	shipRepo := &trFakeShipRepo{ship: ship}
	h := NewRunArbCoordinatorHandler(mediator, shipRepo, marketRepo, nil, nil, nil)
	h.SetEventSubscriber(sub)

	_, err := h.Handle(context.Background(), &RunArbCoordinatorCommand{
		ShipSymbol: ship.ShipSymbol(),
		Good:       trGood,
		BuyAt:      trSource,
		SellAt:     trDest,
		PlayerID:   1,
	})
	if err == nil {
		t.Fatal("a genuine movement failure on a non-transit resume must still fail the run (existing semantics unchanged)")
	}
	if sub.subscribed != 0 {
		t.Fatal("a hull that is NOT in transit must not engage the arrival wait at all")
	}
}

package commands

import (
	"context"
	"testing"

	navCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
)

// sp-trnp — the leg-1 crash the leg-0 pin missed. travel()'s departure hop navigates a
// driveless hull from a market to the source jump gate, then jumps. But the navigate can
// report completion on a STALE "left transit" resync (arrival_wait.go's pre-ETA safety
// poll reads a not-yet-propagated pre-departure nav_status — the sp-n7yp/sp-ynuf nav-cache
// race) BEFORE the hull actually reaches the gate. The hull is left OFF the gate, and
// jump_ship.go then hard-rejects the driveless jump "cannot jump: not at a jump gate" — its
// auto-navigate rescues only DRIVE-equipped hulls — crashing the tour UNRECOVERABLY (the
// live incident: TORWIND-37 sold at X1-DP51-X11A on leg 0, its leg-1 departure hop toward
// gate X1-DP51-B26F "completed" via a 30s-in false-positive resync while still 2m in
// transit, and the jump crashed).
//
// travel() must RE-CONFIRM the hull is authoritatively on the source gate after the
// departure-hop navigate and must NOT fire the jump when it isn't — the driveless off-gate
// jump has no recovery, so a doomed jump must never be dispatched.
func TestTravel_CrossSystem_DepartureNavigateStalledOffGate_ResyncsAndDoesNotJump(t *testing.T) {
	// Market origin → the departure hop runs. The navigate "completes", but the
	// AUTHORITATIVE resync still shows the hull at the market, not the gate: it never
	// truly arrived (the false-positive). travel must catch this, not blind-jump.
	ship := newTravelShipAt(t, "HAULER-1", "X1-AAA-DOCK")
	stillOffGate := newTravelShipAt(t, "HAULER-1", "X1-AAA-DOCK")
	mediator := &travelMediator{
		gateResp: gateResponseAt(t, "X1-AAA-GATE"),
		jumpResp: &navCmd.JumpShipResponse{Success: true, DestinationSystem: "X1-BBB", CooldownSeconds: 60},
	}
	clock := &travelFakeClock{}
	shipRepo := &travelShipRepo{ship: stillOffGate, syncedShip: stillOffGate}
	handler := NewRunTradeRouteCoordinatorHandler(mediator, shipRepo, nil, nil, clock, nil)

	_, err := handler.travel(context.Background(), ship, "X1-BBB-MARKET", 1)

	if err == nil {
		t.Fatal("expected a retryable error when the departure-hop navigate left the hull off the source gate, got nil")
	}
	if shipRepo.syncCalls == 0 {
		t.Fatal("travel must RE-CONFIRM the hull's position with an authoritative resync after the departure-hop navigate (the persisted 'arrived' can be a stale-cache false-positive)")
	}
	if len(mediator.jumps) != 0 {
		t.Fatalf("travel must NOT fire the jump when the hull is not confirmed on the gate — a driveless off-gate jump hard-crashes with no recovery; got %d jump(s)", len(mediator.jumps))
	}
}

// sp-trnp recovery: when the departure-hop navigate's completion signal was a stale-cache
// false-positive but the hull HAS in fact reached the gate, the authoritative resync
// confirms it and travel proceeds to the jump normally — the re-confirmation is a guard on
// the anomaly, never a tax that blocks the healthy path.
func TestTravel_CrossSystem_DepartureNavigateResyncConfirmsGate_Jumps(t *testing.T) {
	ship := newTravelShipAt(t, "HAULER-1", "X1-AAA-DOCK")
	onGate := newTravelShipAtGate(t, "HAULER-1", "X1-AAA-GATE") // resync shows the hull truly on the source gate
	reloaded := newTravelShipAt(t, "HAULER-1", "X1-BBB-GATE")   // post-jump reload (destination system's gate)
	mediator := &travelMediator{
		gateResp: gateResponseAt(t, "X1-AAA-GATE"),
		jumpResp: &navCmd.JumpShipResponse{Success: true, DestinationSystem: "X1-BBB", CooldownSeconds: 60},
	}
	clock := &travelFakeClock{}
	shipRepo := &travelShipRepo{ship: reloaded, syncedShip: onGate}
	handler := NewRunTradeRouteCoordinatorHandler(mediator, shipRepo, nil, nil, clock, nil)

	got, err := handler.travel(context.Background(), ship, "X1-BBB-MARKET", 1)

	if err != nil {
		t.Fatalf("expected travel to proceed once the resync confirms the hull on the gate, got: %v", err)
	}
	if shipRepo.syncCalls == 0 {
		t.Fatal("expected an authoritative resync after the departure-hop navigate")
	}
	if len(mediator.jumps) != 1 {
		t.Fatalf("expected exactly one jump once the source gate was confirmed, got %d", len(mediator.jumps))
	}
	if got != reloaded {
		t.Fatal("expected the post-jump reloaded ship back")
	}
}

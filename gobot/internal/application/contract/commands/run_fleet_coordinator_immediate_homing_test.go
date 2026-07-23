package commands

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	appContract "github.com/andrescamacho/spacetraders-go/internal/application/contract"
	contractServices "github.com/andrescamacho/spacetraders-go/internal/application/contract/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// These tests cover sp-x80j3: the contract coordinator must home a just-delivered
// hull to a standby sink THE MOMENT its contract-work worker completes, instead of
// leaving it loitering at the delivery waypoint until the next between-legs
// selection change or the ~90s idle-arb sweep. Homing reuses the coordinator's ONE
// homing path (ResolveStandbyForHoming + the async HomeShipCommand) the between-legs
// hook and the idle-arb re-home already use (RULINGS #7). Assertions are on the
// observable outcome at the driven homing port (the dispatched HomeShipCommand),
// never on internal calls.

// newImmediateHomingHandler wires a coordinator handler with only the collaborators
// the completion-point homing touches: the ship repo (load the completed hull +
// fleet peers), the mediator the async HomeShipCommand is dispatched through, and
// the demand provider that ranks / auto-resolves the standby set. standbyProvider
// is left nil so ResolveStandbyStations falls back to the (empty) launch set — the
// live empty-hub case where the role demand parks auto-drive homing.
func newImmediateHomingHandler(med common.Mediator, shipRepo navigation.ShipRepository, demand appContract.StandbyDemandProvider) *RunFleetCoordinatorHandler {
	return &RunFleetCoordinatorHandler{
		shipRepo:              shipRepo,
		fleetPoolManager:      contractServices.NewFleetPoolManager(med),
		standbyDemandProvider: demand,
	}
}

// A hull that just delivered its contract sits OFF-station at the delivery waypoint
// (X1-UM5-Z). The moment its worker completes, the coordinator must dispatch it to a
// demand-ranked standby sink IN THE SAME HANDLING — not wait for the next
// between-legs selection change or the ~90s idle-arb sweep (sp-x80j3). With no
// `fleet hub` pinned the role central parks auto-drive the set, and the dispatched
// command must carry those parks and their demand weights.
func TestCoordinator_ImmediateHomingOnCompletion_DispatchesCompletedHullToStandby(t *testing.T) {
	completed := newHomeTestShip(t, "TORWIND-5", "X1-UM5-Z", 0, 0)
	shipRepo := &homeStubShipRepo{ship: completed, fleet: []*navigation.Ship{completed}}
	got := make(chan *HomeShipCommand, 1)
	med := &recordingHomeMediator{got: got}
	provider := &stubDemandProvider{demand: map[string]float64{"X1-UM5-G49": 340, "X1-UM5-K83": 260}}

	handler := newImmediateHomingHandler(med, shipRepo, provider)
	cmd := &RunFleetCoordinatorCommand{PlayerID: shared.MustNewPlayerID(1), ContainerID: "coord-1"}

	handler.homeCompletedHullToStandby(context.Background(), cmd, "TORWIND-5")

	select {
	case dispatched := <-got:
		if dispatched.ShipSymbol != "TORWIND-5" {
			t.Fatalf("expected immediate home for the completed hull TORWIND-5, got %s", dispatched.ShipSymbol)
		}
		wantStations := []string{"X1-UM5-G49", "X1-UM5-K83"} // empty hub set → sorted role parks auto-drive
		if !reflect.DeepEqual(dispatched.StandbyStations, wantStations) {
			t.Fatalf("immediate home must carry the demand-resolved standby set %v, got %v", wantStations, dispatched.StandbyStations)
		}
		if !reflect.DeepEqual(dispatched.StandbyDemand, provider.demand) {
			t.Fatalf("immediate home must carry the per-park demand weights, got %v", dispatched.StandbyDemand)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("completed hull was NOT homed in the same handling — it will loiter at the delivery waypoint until the next pass")
	}
}

// No-thrash: a hull whose contract destination IS a standby sink already delivered
// AT home — the immediate re-home must skip the redundant dispatch (home_ship
// short-circuits too, this just avoids the wasteful command). The completed hull
// sits at X1-UM5-G49, one of the auto-resolved role parks.
func TestCoordinator_ImmediateHomingOnCompletion_SkipsHullAlreadyAtStandbySink(t *testing.T) {
	completed := newHomeTestShip(t, "TORWIND-6", "X1-UM5-G49", 54, -33)
	shipRepo := &homeStubShipRepo{ship: completed, fleet: []*navigation.Ship{completed}}
	got := make(chan *HomeShipCommand, 1)
	med := &recordingHomeMediator{got: got}
	provider := &stubDemandProvider{demand: map[string]float64{"X1-UM5-G49": 340, "X1-UM5-K83": 260}}

	handler := newImmediateHomingHandler(med, shipRepo, provider)
	cmd := &RunFleetCoordinatorCommand{PlayerID: shared.MustNewPlayerID(1), ContainerID: "coord-1"}

	handler.homeCompletedHullToStandby(context.Background(), cmd, "TORWIND-6")

	select {
	case dispatched := <-got:
		t.Fatalf("a hull already AT a standby sink must NOT be re-dispatched, got home for %s", dispatched.ShipSymbol)
	case <-time.After(500 * time.Millisecond):
		// expected: no dispatch
	}
}

// The command frigate homes on its own last-resort rules, never parked as a standby
// drifter (RULINGS #7) — a completed frigate is excluded from immediate homing even
// though it sits OFF-station at the delivery waypoint (so only the frigate guard,
// not the no-thrash guard, can suppress the dispatch here).
func TestCoordinator_ImmediateHomingOnCompletion_ExcludesCommandFrigate(t *testing.T) {
	frigate := newHomeTestShip(t, "TORWIND-1", "X1-UM5-Z", 0, 0) // "-1" suffix ⇒ command hull
	shipRepo := &homeStubShipRepo{ship: frigate, fleet: []*navigation.Ship{frigate}}
	got := make(chan *HomeShipCommand, 1)
	med := &recordingHomeMediator{got: got}
	provider := &stubDemandProvider{demand: map[string]float64{"X1-UM5-G49": 340, "X1-UM5-K83": 260}}

	handler := newImmediateHomingHandler(med, shipRepo, provider)
	cmd := &RunFleetCoordinatorCommand{PlayerID: shared.MustNewPlayerID(1), ContainerID: "coord-1"}

	handler.homeCompletedHullToStandby(context.Background(), cmd, "TORWIND-1")

	select {
	case dispatched := <-got:
		t.Fatalf("the command frigate must be excluded from immediate homing, got home for %s", dispatched.ShipSymbol)
	case <-time.After(500 * time.Millisecond):
		// expected: no dispatch
	}
}

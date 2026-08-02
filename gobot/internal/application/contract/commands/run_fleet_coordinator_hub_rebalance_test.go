package commands

import (
	"context"
	"testing"

	appContract "github.com/andrescamacho/spacetraders-go/internal/application/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// These tests cover PART 2 of the operation-level live hub model, under fixed placement
// (sp-mtgje): after a `fleet hub add|remove` mutates the coordinator's LIVE standby set, the
// coordinator's between-legs homing must re-home idle dedicated hulls to their assigned slot in the
// CURRENT set — a changed set redirects the fixed placement — while NEVER interrupting a hull
// mid-contract-leg (RULINGS #7) and disabling homing entirely on an empty set.
//
// They mirror the composition the coordinator loop performs: resolve the
// LIVE set (ResolveStandbyStations over the provider the `fleet hub` RPC writes),
// then dispatch the EXISTING balanced HomeShipCommand with it. Assertions are on
// the observable navigation outcome (where the hull is sent, or that it is not
// moved), never on internal calls.

// hubRebalanceProvider serves a fixed live standby set, standing in for the
// container-config-backed provider the daemon mutates via `fleet hub`.
type hubRebalanceProvider struct {
	live []string
}

func (p *hubRebalanceProvider) StandbyStations(_ context.Context, _ string, _ int) ([]string, error) {
	return p.live, nil
}

var _ appContract.StandbyStationProvider = (*hubRebalanceProvider)(nil)

// homeToLiveSet mirrors the coordinator's between-legs homing hook: resolve the
// LIVE standby set, then run the balanced HomeShipCommand with it. Returns the
// homing response and the mediator so a test can assert where (or whether) the
// hull was navigated.
func homeToLiveSet(t *testing.T, ship *navigation.Ship, graph *homeStubGraphProvider, provider appContract.StandbyStationProvider, launchList []string) (*HomeShipResponse, *homeFakeMediator) {
	t.Helper()
	logger := &completionCapturingLogger{}
	live := appContract.ResolveStandbyStations(context.Background(), logger, provider, "cc-1", 1, launchList)

	shipRepo := &homeStubShipRepo{ship: ship}
	mediator := &homeFakeMediator{}
	handler := NewHomeShipHandler(mediator, shipRepo, graph)

	resp, err := handler.Handle(context.Background(), &HomeShipCommand{
		ShipSymbol:      ship.ShipSymbol(),
		PlayerID:        shared.MustNewPlayerID(1),
		StandbyStations: live,
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	homeResp, ok := resp.(*HomeShipResponse)
	if !ok {
		t.Fatalf("unexpected response type %T", resp)
	}
	return homeResp, mediator
}

// TestRebalance_IdleHullHomesToItsSlotInLiveSet: a `fleet hub` change (launch {OLD}, live {NEW}) must
// drive homing off the LIVE set — an idle dedicated hull homes to its assigned slot in the CURRENT set
// (NEW), never the stale launch snapshot (OLD). Under fixed placement a lone hull owns the first live
// slot; homing to NEW (not OLD) proves the live set, not the launch list, drives placement.
func TestRebalance_IdleHullHomesToItsSlotInLiveSet(t *testing.T) {
	ship := newHomeTestShip(t, "TORWIND-4", "X1-TEST-A1", 0, 0)
	old := homeTestWaypoint(t, "X1-TEST-OLD", 100, 0)
	newHub := homeTestWaypoint(t, "X1-TEST-NEW", 10, 0)
	graph := &homeStubGraphProvider{graph: homeTestGraph(old, newHub)}

	launchList := []string{"X1-TEST-OLD"}                            // the hub at launch
	provider := &hubRebalanceProvider{live: []string{"X1-TEST-NEW"}} // changed live to NEW

	resp, mediator := homeToLiveSet(t, ship, graph, provider, launchList)

	if !resp.Navigated {
		t.Fatalf("an idle dedicated hull must be homed to its slot in the live hub set, got Navigated=false")
	}
	if resp.TargetStation != "X1-TEST-NEW" {
		t.Fatalf("hull must home to its slot in the CURRENT live set (NEW), not the stale launch OLD, got %q", resp.TargetStation)
	}
	if len(mediator.navigateCalls) != 1 || mediator.navigateCalls[0].Destination != "X1-TEST-NEW" {
		t.Fatalf("expected exactly one navigate to X1-TEST-NEW, got %+v", mediator.navigateCalls)
	}
}

// TestRebalance_NoMidContractPoach: a hull on an active contract leg (in transit)
// is NEVER re-homed for a rebalance, even though the live set is non-empty — the
// clean-hand-off invariant (RULINGS #7). Only idle hulls rebalance.
func TestRebalance_NoMidContractPoach(t *testing.T) {
	// A hull underway to its contract destination: CurrentLocation is the
	// destination once transit starts, and IsInTransit() gates re-homing.
	ship := newHomeTestShipWithStatus(t, "TORWIND-4", "X1-TEST-DEST", 50, 0, navigation.NavStatusInTransit)
	near := homeTestWaypoint(t, "X1-TEST-NEAR", 10, 0)
	graph := &homeStubGraphProvider{graph: homeTestGraph(near)}

	provider := &hubRebalanceProvider{live: []string{"X1-TEST-NEAR"}}

	resp, mediator := homeToLiveSet(t, ship, graph, provider, nil)

	if resp.Navigated {
		t.Fatalf("a hull mid-contract-leg must NOT be re-homed for a rebalance (RULINGS #7), got Navigated=true")
	}
	if len(mediator.navigateCalls) != 0 {
		t.Fatalf("a mid-contract hull must never be navigated by a rebalance, got %+v", mediator.navigateCalls)
	}
}

// TestEmptyStandbySet_HomingDisabled: with every hub removed live, the LIVE set is
// empty and homing is disabled — an idle dedicated hull is left where it is, no
// navigation, preserving the "empty set disables homing" contract across the live
// path.
func TestEmptyStandbySet_HomingDisabled(t *testing.T) {
	ship := newHomeTestShip(t, "TORWIND-4", "X1-TEST-A1", 0, 0)
	near := homeTestWaypoint(t, "X1-TEST-NEAR", 10, 0)
	graph := &homeStubGraphProvider{graph: homeTestGraph(near)}

	// Launch had a hub, but the operator `fleet hub remove`d it — live set empty.
	launchList := []string{"X1-TEST-NEAR"}
	provider := &hubRebalanceProvider{live: []string{}}

	resp, mediator := homeToLiveSet(t, ship, graph, provider, launchList)

	if resp.Navigated {
		t.Fatalf("an empty live hub set must disable homing, got Navigated=true")
	}
	if len(mediator.navigateCalls) != 0 {
		t.Fatalf("empty live hub set must produce no navigation, got %+v", mediator.navigateCalls)
	}
}

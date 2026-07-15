package ship

import (
	"context"
	"reflect"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// --- Charter fan-out doubles ----------------------------------------------

type spyGateCharter struct {
	systems []string
}

func (s *spyGateCharter) Connections(_ context.Context, systemSymbol string, _ int) ([]system.GateEdge, error) {
	s.systems = append(s.systems, systemSymbol)
	return nil, nil
}

// fakeWaypointSource returns a fixed waypoint set for the charted system and
// records which system it was asked to chart, standing in for the production
// graph-provider-backed source (which fetches+persists the system's waypoints).
type fakeWaypointSource struct {
	waypoints []*shared.Waypoint
	systems   []string
}

func (f *fakeWaypointSource) ChartWaypoints(_ context.Context, systemSymbol string, _ shared.PlayerID) ([]*shared.Waypoint, error) {
	f.systems = append(f.systems, systemSymbol)
	return f.waypoints, nil
}

type spyMarketScanner struct {
	scanned []string
}

func (s *spyMarketScanner) ScanAndSaveMarket(_ context.Context, _ uint, waypointSymbol string) error {
	s.scanned = append(s.scanned, waypointSymbol)
	return nil
}

type spyShipyardScanner struct {
	scanned []string
}

func (s *spyShipyardScanner) ScanAndSaveShipyard(_ context.Context, _ uint, waypointSymbol string) error {
	s.scanned = append(s.scanned, waypointSymbol)
	return nil
}

// TestWarpSystemCharter_ChartsGateEdgesWaypointsMarketsShipyards pins scenario 4's
// persistence fan-out: charting a freshly warped-to system routes each deliverable
// to its store. Gate edges and waypoints are charted for the arrival system; the
// market scan targets only the marketplace waypoint; the shipyard scan runs across
// every waypoint (its own trait gate no-ops the non-shipyards). The fixture has one
// marketplace waypoint and one plain waypoint so market vs. shipyard routing is
// distinguishable, not just counted.
func TestWarpSystemCharter_ChartsGateEdgesWaypointsMarketsShipyards(t *testing.T) {
	marketplace := mustWaypoint(t, "X1-SYSB-B1", 0, 0)
	marketplace.Traits = []string{"MARKETPLACE"}
	plain := mustWaypoint(t, "X1-SYSB-B2", 5, 0) // not a marketplace

	gate := &spyGateCharter{}
	waypoints := &fakeWaypointSource{waypoints: []*shared.Waypoint{marketplace, plain}}
	market := &spyMarketScanner{}
	shipyard := &spyShipyardScanner{}

	charter := NewWarpSystemCharter(gate, waypoints, market, shipyard)

	err := charter.ChartSystem(context.Background(), "X1-SYSB", shared.MustNewPlayerID(1))
	if err != nil {
		t.Fatalf("ChartSystem should be best-effort and return nil, got: %v", err)
	}

	if !reflect.DeepEqual(gate.systems, []string{"X1-SYSB"}) {
		t.Fatalf("expected gate edges charted for X1-SYSB, got %v", gate.systems)
	}
	if !reflect.DeepEqual(waypoints.systems, []string{"X1-SYSB"}) {
		t.Fatalf("expected waypoints charted for X1-SYSB, got %v", waypoints.systems)
	}
	if !reflect.DeepEqual(market.scanned, []string{marketplace.Symbol}) {
		t.Fatalf("expected market scanned ONLY at the marketplace waypoint %s, got %v", marketplace.Symbol, market.scanned)
	}
	if !reflect.DeepEqual(shipyard.scanned, []string{marketplace.Symbol, plain.Symbol}) {
		t.Fatalf("expected shipyard scan across every waypoint, got %v", shipyard.scanned)
	}
}

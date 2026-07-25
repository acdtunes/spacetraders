package ship

import (
	"context"
	"errors"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// --- Escape-reader test doubles -------------------------------------------

// stubWaypointSource is the double at the fetch-through waypoint port. It answers
// with a canned waypoint set per system, or fails - the UNREADABLE case the reader
// must surface rather than silently answer "no fuel".
type stubWaypointSource struct {
	waypoints map[string][]*shared.Waypoint
	err       error
}

func (s *stubWaypointSource) ChartWaypoints(_ context.Context, systemSymbol string, _ shared.PlayerID) ([]*shared.Waypoint, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.waypoints[systemSymbol], nil
}

// stubGateAdjacency is the double at the gate-graph store. Its map is keyed by the
// SOURCE system, and each edge's UnderConstruction describes the CONNECTED system's
// own gate - the reverse-lookup shape the real store uses.
type stubGateAdjacency struct {
	adjacency map[string][]system.GateEdge
	err       error
}

func (s *stubGateAdjacency) Adjacency(_ context.Context) (map[string][]system.GateEdge, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.adjacency, nil
}

func fuelWaypoint(t *testing.T, symbol string) *shared.Waypoint {
	t.Helper()
	waypoint := mustWaypoint(t, symbol, 0, 0)
	waypoint.HasFuel = true
	return waypoint
}

// --- Tests ----------------------------------------------------------------

// TestSystemEscapeReader_ReportsBothWaysOutOfASystem pins the two independent
// escapes the reader resolves for a system a warp would land in: a waypoint that
// sells fuel, and a jump gate of ITS OWN that a neighbour recorded as BUILT. The
// facts come from different stores, so both are asserted from one read.
func TestSystemEscapeReader_ReportsBothWaysOutOfASystem(t *testing.T) {
	waypoints := &stubWaypointSource{waypoints: map[string][]*shared.Waypoint{
		"X1-TY66": {mustWaypoint(t, "X1-TY66-A1", 0, 0), fuelWaypoint(t, "X1-TY66-M2")},
	}}
	gates := &stubGateAdjacency{adjacency: map[string][]system.GateEdge{
		"X1-YY85": {{ConnectedSystem: "X1-TY66", GateWaypoint: "X1-TY66-I58", UnderConstruction: false}},
	}}

	reader := NewSystemEscapeReader(waypoints, gates)

	escape, err := reader.EscapeOptions(context.Background(), "X1-TY66", shared.MustNewPlayerID(1))
	if err != nil {
		t.Fatalf("expected a readable destination, got error: %v", err)
	}
	if !escape.SellsFuel {
		t.Fatalf("expected SellsFuel from the fuel-bearing waypoint X1-TY66-M2, got %+v", escape)
	}
	if !escape.HasBuiltGate {
		t.Fatalf("expected HasBuiltGate from the neighbour edge recording X1-TY66-I58 as built, got %+v", escape)
	}
	if escape.IsDeadEnd() {
		t.Fatalf("a system with fuel and a built gate is not a dead end, got %+v", escape)
	}
}

// TestSystemEscapeReader_RefusesToCallAnUnbuiltOrUnknownGateAnExit is the live
// near-miss, generalised. Adjacency alone does NOT mean traversable: X1-TY66 read
// as gate-connected while its own gate X1-TY66-I58 was still under construction,
// and warping a 722,511-credit hull there would have stranded it. A STALE row is
// treated the same way - its build state is unverified, and an unverified claim is
// not an escape. A system no neighbour has recorded has no known gate at all.
func TestSystemEscapeReader_RefusesToCallAnUnbuiltOrUnknownGateAnExit(t *testing.T) {
	cases := []struct {
		name      string
		adjacency map[string][]system.GateEdge
	}{
		{
			name: "the destination's own gate is under construction",
			adjacency: map[string][]system.GateEdge{
				"X1-YY85": {{ConnectedSystem: "X1-TY66", GateWaypoint: "X1-TY66-I58", UnderConstruction: true}},
			},
		},
		{
			name: "the only edge recording it is stale, so its build state is unverified",
			adjacency: map[string][]system.GateEdge{
				"X1-YY85": {{ConnectedSystem: "X1-TY66", GateWaypoint: "X1-TY66-I58", UnderConstruction: false, Stale: true}},
			},
		},
		{
			name: "no neighbour has recorded a gate into it",
			adjacency: map[string][]system.GateEdge{
				"X1-YY85": {{ConnectedSystem: "X1-FD3", GateWaypoint: "X1-FD3-I44", UnderConstruction: false}},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			waypoints := &stubWaypointSource{waypoints: map[string][]*shared.Waypoint{
				"X1-TY66": {mustWaypoint(t, "X1-TY66-A1", 0, 0)}, // zero scanned markets: no fuel
			}}

			reader := NewSystemEscapeReader(waypoints, &stubGateAdjacency{adjacency: tc.adjacency})

			escape, err := reader.EscapeOptions(context.Background(), "X1-TY66", shared.MustNewPlayerID(1))
			if err != nil {
				t.Fatalf("expected a readable destination, got error: %v", err)
			}
			if escape.HasBuiltGate {
				t.Fatalf("expected no traversable exit gate, got %+v", escape)
			}
			if !escape.IsDeadEnd() {
				t.Fatalf("no fuel and no traversable exit is a dead end, got %+v", escape)
			}
		})
	}
}

// TestSystemEscapeReader_FailsClosedWhenTheDestinationCannotBeRead pins the
// fail-CLOSED posture of the guard's inputs. An unreadable waypoint set, an
// unreadable gate store, and a half-wired reader must every one surface as an
// ERROR - which the executor turns into a refusal - never as a quietly optimistic
// "no obstacles found".
func TestSystemEscapeReader_FailsClosedWhenTheDestinationCannotBeRead(t *testing.T) {
	waypoints := &stubWaypointSource{waypoints: map[string][]*shared.Waypoint{
		"X1-TY66": {fuelWaypoint(t, "X1-TY66-M2")},
	}}
	gates := &stubGateAdjacency{adjacency: map[string][]system.GateEdge{}}

	cases := []struct {
		name   string
		reader *SystemEscapeReader
	}{
		{name: "waypoints unreadable", reader: NewSystemEscapeReader(&stubWaypointSource{err: errors.New("system graph fetch failed")}, gates)},
		{name: "gate store unreadable", reader: NewSystemEscapeReader(waypoints, &stubGateAdjacency{err: errors.New("adjacency query failed")})},
		{name: "no waypoint source wired", reader: NewSystemEscapeReader(nil, gates)},
		{name: "no gate store wired", reader: NewSystemEscapeReader(waypoints, nil)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			escape, err := tc.reader.EscapeOptions(context.Background(), "X1-TY66", shared.MustNewPlayerID(1))
			if err == nil {
				t.Fatalf("expected an unreadable destination to surface as an error, got escape %+v", escape)
			}
		})
	}
}

package queries

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// stubGraphProvider is a minimal ISystemGraphProvider that returns a fixed
// graph and records the arguments it was called with. It lets the handler
// tests assert filtering/sorting behaviour without any API or database.
type stubGraphProvider struct {
	graph            *system.NavigationGraph
	getGraphCalled   int
	lastSystemSymbol string
	lastPlayerID     int
}

func (s *stubGraphProvider) GetGraph(_ context.Context, systemSymbol string, _ bool, playerID int) (*system.GraphLoadResult, error) {
	s.getGraphCalled++
	s.lastSystemSymbol = systemSymbol
	s.lastPlayerID = playerID
	return &system.GraphLoadResult{Graph: s.graph, Source: "database"}, nil
}

func waypointWith(t *testing.T, symbol, wpType string, traits []string) *shared.Waypoint {
	t.Helper()
	wp, err := shared.NewWaypoint(symbol, 0, 0)
	if err != nil {
		t.Fatalf("NewWaypoint(%s): %v", symbol, err)
	}
	wp.Type = wpType
	wp.Traits = traits
	return wp
}

func systemWithMixedWaypoints(t *testing.T) *system.NavigationGraph {
	t.Helper()
	graph := system.NewNavigationGraph("X1-PZ28")
	graph.AddWaypoint(waypointWith(t, "X1-PZ28-I55", "JUMP_GATE", nil))
	graph.AddWaypoint(waypointWith(t, "X1-PZ28-A1", "ORBITAL_STATION", []string{"SHIPYARD", "MARKETPLACE"}))
	graph.AddWaypoint(waypointWith(t, "X1-PZ28-B2", "PLANET", []string{"MARKETPLACE"}))
	graph.AddWaypoint(waypointWith(t, "X1-PZ28-C3", "MOON", nil))
	return graph
}

// The jump gate is invisible via the market cache because it is not a
// MARKETPLACE. Listing the system's waypoints must surface it (and every other
// waypoint) from the daemon's waypoint graph, auto-syncing from the API.
func TestListWaypoints_ReturnsAllWaypointsSortedBySymbol(t *testing.T) {
	provider := &stubGraphProvider{graph: systemWithMixedWaypoints(t)}
	handler := NewListWaypointsHandler(provider, nil)

	pid := 1
	resp, err := handler.Handle(context.Background(), &ListWaypointsQuery{
		SystemSymbol: "X1-PZ28",
		PlayerID:     &pid,
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	listResp, ok := resp.(*ListWaypointsResponse)
	if !ok {
		t.Fatalf("unexpected response type: %T", resp)
	}
	if provider.getGraphCalled != 1 {
		t.Fatalf("expected exactly one graph sync, got %d", provider.getGraphCalled)
	}
	if provider.lastSystemSymbol != "X1-PZ28" {
		t.Fatalf("expected system X1-PZ28, got %s", provider.lastSystemSymbol)
	}

	got := symbolsOf(listResp.Waypoints)
	want := []string{"X1-PZ28-A1", "X1-PZ28-B2", "X1-PZ28-C3", "X1-PZ28-I55"}
	if !equalStrings(got, want) {
		t.Fatalf("expected sorted %v, got %v", want, got)
	}
}

// Acceptance: the captain can enumerate the JUMP_GATE waypoint symbol by type
// without physically visiting anything.
func TestListWaypoints_FiltersByType(t *testing.T) {
	provider := &stubGraphProvider{graph: systemWithMixedWaypoints(t)}
	handler := NewListWaypointsHandler(provider, nil)

	pid := 1
	resp, err := handler.Handle(context.Background(), &ListWaypointsQuery{
		SystemSymbol: "X1-PZ28",
		Type:         "JUMP_GATE",
		PlayerID:     &pid,
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	got := symbolsOf(resp.(*ListWaypointsResponse).Waypoints)
	want := []string{"X1-PZ28-I55"}
	if !equalStrings(got, want) {
		t.Fatalf("expected only the jump gate %v, got %v", want, got)
	}
}

// Acceptance: the captain can enumerate all SHIPYARD waypoints by trait.
func TestListWaypoints_FiltersByTrait(t *testing.T) {
	provider := &stubGraphProvider{graph: systemWithMixedWaypoints(t)}
	handler := NewListWaypointsHandler(provider, nil)

	pid := 1
	resp, err := handler.Handle(context.Background(), &ListWaypointsQuery{
		SystemSymbol: "X1-PZ28",
		Trait:        "SHIPYARD",
		PlayerID:     &pid,
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	got := symbolsOf(resp.(*ListWaypointsResponse).Waypoints)
	want := []string{"X1-PZ28-A1"}
	if !equalStrings(got, want) {
		t.Fatalf("expected only the shipyard %v, got %v", want, got)
	}
}

func TestListWaypoints_RequiresSystemSymbol(t *testing.T) {
	provider := &stubGraphProvider{graph: systemWithMixedWaypoints(t)}
	handler := NewListWaypointsHandler(provider, nil)

	pid := 1
	_, err := handler.Handle(context.Background(), &ListWaypointsQuery{PlayerID: &pid})
	if err == nil {
		t.Fatalf("expected error for missing system symbol")
	}
	if provider.getGraphCalled != 0 {
		t.Fatalf("expected no graph sync when validation fails, got %d", provider.getGraphCalled)
	}
}

func symbolsOf(waypoints []*shared.Waypoint) []string {
	out := make([]string, 0, len(waypoints))
	for _, wp := range waypoints {
		out = append(out, wp.Symbol)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

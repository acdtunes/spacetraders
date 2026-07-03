package queries

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// stubWaypointProvider is a minimal IWaypointProvider that returns a fixed
// waypoint and records the arguments it was called with.
type stubWaypointProvider struct {
	waypoint           *shared.Waypoint
	getWaypointCalled  int
	lastWaypointSymbol string
	lastSystemSymbol   string
}

func (s *stubWaypointProvider) GetWaypoint(_ context.Context, waypointSymbol, systemSymbol string, _ int) (*shared.Waypoint, error) {
	s.getWaypointCalled++
	s.lastWaypointSymbol = waypointSymbol
	s.lastSystemSymbol = systemSymbol
	return s.waypoint, nil
}

// The captain needs the jump gate's detail (type + traits) without visiting it.
// GetWaypoint must derive the system symbol from the waypoint symbol and
// auto-fetch through the provider.
func TestGetWaypoint_ReturnsDetailAndDerivesSystem(t *testing.T) {
	jumpGate := waypointWith(t, "X1-PZ28-I55", "JUMP_GATE", nil)
	provider := &stubWaypointProvider{waypoint: jumpGate}
	handler := NewGetWaypointHandler(provider, nil)

	pid := 1
	resp, err := handler.Handle(context.Background(), &GetWaypointQuery{
		WaypointSymbol: "X1-PZ28-I55",
		PlayerID:       &pid,
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	getResp, ok := resp.(*GetWaypointResponse)
	if !ok {
		t.Fatalf("unexpected response type: %T", resp)
	}
	if provider.getWaypointCalled != 1 {
		t.Fatalf("expected exactly one provider fetch, got %d", provider.getWaypointCalled)
	}
	if provider.lastSystemSymbol != "X1-PZ28" {
		t.Fatalf("expected derived system X1-PZ28, got %s", provider.lastSystemSymbol)
	}
	if getResp.Waypoint.Symbol != "X1-PZ28-I55" {
		t.Fatalf("expected waypoint X1-PZ28-I55, got %s", getResp.Waypoint.Symbol)
	}
	if getResp.Waypoint.Type != "JUMP_GATE" {
		t.Fatalf("expected type JUMP_GATE, got %s", getResp.Waypoint.Type)
	}
}

func TestGetWaypoint_RequiresWaypointSymbol(t *testing.T) {
	provider := &stubWaypointProvider{}
	handler := NewGetWaypointHandler(provider, nil)

	pid := 1
	_, err := handler.Handle(context.Background(), &GetWaypointQuery{PlayerID: &pid})
	if err == nil {
		t.Fatalf("expected error for missing waypoint symbol")
	}
	if provider.getWaypointCalled != 0 {
		t.Fatalf("expected no provider fetch when validation fails, got %d", provider.getWaypointCalled)
	}
}

package queries

import (
	"context"
	"fmt"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// stubConnectionsGraphProvider embeds the domain interface so only GetGraph
// needs a concrete implementation; any unexpected call panics on a
// nil-method deref.
type stubConnectionsGraphProvider struct {
	system.ISystemGraphProvider

	result *system.GraphLoadResult
	err    error

	calledWithSystem string
}

func (s *stubConnectionsGraphProvider) GetGraph(_ context.Context, systemSymbol string, _ bool, _ int) (*system.GraphLoadResult, error) {
	s.calledWithSystem = systemSymbol
	return s.result, s.err
}

// stubConnectionsAPIClient embeds the domain interface so only GetJumpGate
// needs a concrete implementation.
type stubConnectionsAPIClient struct {
	ports.APIClient

	gateData *ports.JumpGateData
	err      error

	calledWithWaypoint string
}

func (s *stubConnectionsAPIClient) GetJumpGate(_ context.Context, _, waypointSymbol, _ string) (*ports.JumpGateData, error) {
	s.calledWithWaypoint = waypointSymbol
	return s.gateData, s.err
}

// stubConnectionsPlayerRepo embeds the domain interface so only FindByID
// needs a concrete implementation.
type stubConnectionsPlayerRepo struct {
	player.PlayerRepository

	playerEntity *player.Player
}

func (s *stubConnectionsPlayerRepo) FindByID(_ context.Context, _ shared.PlayerID) (*player.Player, error) {
	return s.playerEntity, nil
}

func newConnectionsGateWaypoint(t *testing.T, symbol string) *shared.Waypoint {
	t.Helper()
	wp, err := shared.NewWaypoint(symbol, 0, 0)
	if err != nil {
		t.Fatalf("NewWaypoint: %v", err)
	}
	wp.Type = "JUMP_GATE"
	return wp
}

func newConnectionsNonGateWaypoint(t *testing.T, symbol string) *shared.Waypoint {
	t.Helper()
	wp, err := shared.NewWaypoint(symbol, 0, 0)
	if err != nil {
		t.Fatalf("NewWaypoint: %v", err)
	}
	wp.Type = "PLANET"
	return wp
}

// GetJumpGateConnectionsQuery is SystemSymbol-based (not ship-relative, sp-wlev
// multi-system trade-route lane scanning) - given a system with a jump gate,
// it must resolve the gate's live connections and report the set of
// one-hop-reachable system symbols, extracted from the connection waypoints.
func TestGetJumpGateConnections_SystemHasGate_ReturnsConnectedSystems(t *testing.T) {
	gate := newConnectionsGateWaypoint(t, "X1-PA3-B10D")
	rock := newConnectionsNonGateWaypoint(t, "X1-PA3-A1")

	graphProvider := &stubConnectionsGraphProvider{
		result: &system.GraphLoadResult{
			Graph: &system.NavigationGraph{
				Waypoints: map[string]*shared.Waypoint{rock.Symbol: rock, gate.Symbol: gate},
			},
		},
	}
	apiClient := &stubConnectionsAPIClient{
		gateData: &ports.JumpGateData{
			Symbol:      "X1-PA3-B10D",
			Connections: []string{"X1-ZC66-A40D", "X1-GQ92-I51", "X1-UQ16-D16D"},
		},
	}
	playerRepo := &stubConnectionsPlayerRepo{playerEntity: player.NewPlayer(shared.MustNewPlayerID(1), "AGENT", "test-token")}

	handler := NewGetJumpGateConnectionsHandler(graphProvider, apiClient, playerRepo)

	playerIDInt := 1
	query := &GetJumpGateConnectionsQuery{
		SystemSymbol: "X1-PA3",
		PlayerID:     &playerIDInt,
	}

	resp, err := handler.Handle(context.Background(), query)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	connResp, ok := resp.(*GetJumpGateConnectionsResponse)
	if !ok {
		t.Fatalf("unexpected response type: %T", resp)
	}

	if connResp.JumpGate == nil || connResp.JumpGate.Symbol != "X1-PA3-B10D" {
		t.Fatalf("expected jump gate X1-PA3-B10D, got %+v", connResp.JumpGate)
	}

	wantSystems := map[string]bool{"X1-ZC66": true, "X1-GQ92": true, "X1-UQ16": true}
	if len(connResp.ConnectedSystems) != len(wantSystems) {
		t.Fatalf("expected %d connected systems, got %d: %v", len(wantSystems), len(connResp.ConnectedSystems), connResp.ConnectedSystems)
	}
	for _, s := range connResp.ConnectedSystems {
		if !wantSystems[s] {
			t.Fatalf("unexpected connected system %q in %v", s, connResp.ConnectedSystems)
		}
	}

	// Must resolve the gate WAYPOINT (not the bare system symbol) when
	// calling GetJumpGate, mirroring jump_ship's own resolution requirement.
	if apiClient.calledWithWaypoint != "X1-PA3-B10D" {
		t.Fatalf("expected GetJumpGate called with gate waypoint X1-PA3-B10D, got %q", apiClient.calledWithWaypoint)
	}
}

// A system with no jump-gate-typed waypoint in its graph cannot have its
// connections discovered - the handler must reject with a clear error
// instead of calling the live API with a zero-value waypoint.
func TestGetJumpGateConnections_NoGateInSystem_ReturnsClearError(t *testing.T) {
	rock := newConnectionsNonGateWaypoint(t, "X1-PA3-A1")

	graphProvider := &stubConnectionsGraphProvider{
		result: &system.GraphLoadResult{
			Graph: &system.NavigationGraph{
				Waypoints: map[string]*shared.Waypoint{rock.Symbol: rock},
			},
		},
	}
	apiClient := &stubConnectionsAPIClient{}
	playerRepo := &stubConnectionsPlayerRepo{playerEntity: player.NewPlayer(shared.MustNewPlayerID(1), "AGENT", "test-token")}

	handler := NewGetJumpGateConnectionsHandler(graphProvider, apiClient, playerRepo)

	playerIDInt := 1
	query := &GetJumpGateConnectionsQuery{
		SystemSymbol: "X1-PA3",
		PlayerID:     &playerIDInt,
	}

	_, err := handler.Handle(context.Background(), query)
	if err == nil {
		t.Fatalf("expected an error when the system has no jump gate")
	}
	if apiClient.calledWithWaypoint != "" {
		t.Fatalf("expected GetJumpGate not to be called when no gate exists, got waypoint arg %q", apiClient.calledWithWaypoint)
	}
}

// A failure resolving the gate's connections via the live API must surface
// as a wrapped error, not a panic or a silently empty result.
func TestGetJumpGateConnections_APIError_ReturnsWrappedError(t *testing.T) {
	gate := newConnectionsGateWaypoint(t, "X1-PA3-B10D")

	graphProvider := &stubConnectionsGraphProvider{
		result: &system.GraphLoadResult{
			Graph: &system.NavigationGraph{
				Waypoints: map[string]*shared.Waypoint{gate.Symbol: gate},
			},
		},
	}
	apiClient := &stubConnectionsAPIClient{err: fmt.Errorf("api unavailable")}
	playerRepo := &stubConnectionsPlayerRepo{playerEntity: player.NewPlayer(shared.MustNewPlayerID(1), "AGENT", "test-token")}

	handler := NewGetJumpGateConnectionsHandler(graphProvider, apiClient, playerRepo)

	playerIDInt := 1
	query := &GetJumpGateConnectionsQuery{
		SystemSymbol: "X1-PA3",
		PlayerID:     &playerIDInt,
	}

	_, err := handler.Handle(context.Background(), query)
	if err == nil {
		t.Fatalf("expected an error when the live API call fails")
	}
}

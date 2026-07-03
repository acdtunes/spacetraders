package navigation

import (
	"context"
	"fmt"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// stubShipRepo embeds the domain interface so we only implement the methods the
// handler exercises; any unexpected call will panic with a nil-method deref.
type stubShipRepo struct {
	domainNavigation.ShipRepository

	navigateErr     error
	syncedShip      *domainNavigation.Ship
	syncCalledCount int
}

func (s *stubShipRepo) Navigate(_ context.Context, _ *domainNavigation.Ship, _ *shared.Waypoint, _ shared.PlayerID) (*domainNavigation.Result, error) {
	return nil, s.navigateErr
}

func (s *stubShipRepo) SyncShipFromAPI(_ context.Context, _ string, _ shared.PlayerID) (*domainNavigation.Ship, error) {
	s.syncCalledCount++
	return s.syncedShip, nil
}

func newTestShip(t *testing.T, symbol string, location *shared.Waypoint) *domainNavigation.Ship {
	t.Helper()
	fuel, err := shared.NewFuel(0, 0)
	if err != nil {
		t.Fatalf("NewFuel: %v", err)
	}
	cargo, err := shared.NewCargo(0, 0, nil)
	if err != nil {
		t.Fatalf("NewCargo: %v", err)
	}
	ship, err := domainNavigation.NewShip(
		symbol,
		shared.MustNewPlayerID(1),
		location,
		fuel,
		0,
		0,
		cargo,
		9,
		"FRAME_PROBE",
		"SATELLITE",
		nil,
		domainNavigation.NavStatusInOrbit,
	)
	if err != nil {
		t.Fatalf("NewShip: %v", err)
	}
	return ship
}

// Reproduces the scout-tour crash-loop: the daemon's cached position lags the
// server by one waypoint, so navigate returns API 4204 ("ship is currently
// located at the destination"). The handler must treat that as a no-op success
// (the ship IS at the target) and reconcile state, not surface a hard error.
func TestNavigateDirect_AlreadyAtDestination4204(t *testing.T) {
	stale, _ := shared.NewWaypoint("X1-PZ28-H64", 0, 0)
	destination, _ := shared.NewWaypoint("X1-PZ28-H65", 1, 1)

	ship := newTestShip(t, "TORWIND-2", stale)
	reconciled := newTestShip(t, "TORWIND-2", destination)

	repo := &stubShipRepo{
		navigateErr: fmt.Errorf("failed to navigate ship: max retries exceeded: " +
			`API error (status 400): {"error":{"code":4204,"message":"Navigate request failed. Ship TORWIND-2 is currently located at the destination."}}`),
		syncedShip: reconciled,
	}

	handler := NewNavigateDirectHandler(repo, nil)

	cmd := &types.NavigateDirectCommand{
		Ship:                ship,
		Destination:         destination.Symbol,
		DestinationWaypoint: destination,
		PlayerID:            shared.MustNewPlayerID(1),
	}

	resp, err := handler.Handle(context.Background(), cmd)
	if err != nil {
		t.Fatalf("expected no error on 4204 already-at-destination, got: %v", err)
	}

	navResp, ok := resp.(*types.NavigateDirectResponse)
	if !ok {
		t.Fatalf("unexpected response type: %T", resp)
	}
	if navResp.Status != "already_at_destination" {
		t.Fatalf("expected status already_at_destination, got %q", navResp.Status)
	}
	if repo.syncCalledCount != 1 {
		t.Fatalf("expected ship state to be reconciled once from API, got %d syncs", repo.syncCalledCount)
	}
	if ship.CurrentLocation().Symbol != destination.Symbol {
		t.Fatalf("expected ship position reconciled to %s, got %s", destination.Symbol, ship.CurrentLocation().Symbol)
	}
}

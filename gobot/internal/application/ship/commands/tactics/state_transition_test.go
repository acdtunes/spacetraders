package tactics

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

type recordingShipRepo struct {
	domainNavigation.ShipRepository

	orbitCalls int
	dockCalls  int
}

func (r *recordingShipRepo) Orbit(_ context.Context, _ *domainNavigation.Ship, _ shared.PlayerID) error {
	r.orbitCalls++
	return nil
}

func (r *recordingShipRepo) Dock(_ context.Context, _ *domainNavigation.Ship, _ shared.PlayerID) error {
	r.dockCalls++
	return nil
}

func newShipInState(t *testing.T, status domainNavigation.NavStatus) *domainNavigation.Ship {
	t.Helper()
	location, err := shared.NewWaypoint("X1-AA-1", 0, 0)
	if err != nil {
		t.Fatalf("NewWaypoint: %v", err)
	}
	fuel, err := shared.NewFuel(0, 0)
	if err != nil {
		t.Fatalf("NewFuel: %v", err)
	}
	cargo, err := shared.NewCargo(0, 0, nil)
	if err != nil {
		t.Fatalf("NewCargo: %v", err)
	}
	ship, err := domainNavigation.NewShip(
		"SHIP-1",
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
		status,
	)
	if err != nil {
		t.Fatalf("NewShip: %v", err)
	}
	return ship
}

func TestStateTransition_AlreadyInStateSkipsAPI(t *testing.T) {
	tests := []struct {
		name           string
		initialStatus  domainNavigation.NavStatus
		handle         func(context.Context, *recordingShipRepo, *domainNavigation.Ship) (interface{}, error)
		expectStatus   string
		expectAPICalls func(*recordingShipRepo) int
	}{
		{
			name:          "orbit while already in orbit",
			initialStatus: domainNavigation.NavStatusInOrbit,
			handle: func(ctx context.Context, repo *recordingShipRepo, ship *domainNavigation.Ship) (interface{}, error) {
				resp, err := NewOrbitShipHandler(repo).Handle(ctx, &types.OrbitShipCommand{Ship: ship, PlayerID: shared.MustNewPlayerID(1)})
				if err != nil {
					return nil, err
				}
				return resp.(*types.OrbitShipResponse).Status, nil
			},
			expectStatus:   "already_in_orbit",
			expectAPICalls: func(r *recordingShipRepo) int { return r.orbitCalls },
		},
		{
			name:          "orbit while docked triggers API",
			initialStatus: domainNavigation.NavStatusDocked,
			handle: func(ctx context.Context, repo *recordingShipRepo, ship *domainNavigation.Ship) (interface{}, error) {
				resp, err := NewOrbitShipHandler(repo).Handle(ctx, &types.OrbitShipCommand{Ship: ship, PlayerID: shared.MustNewPlayerID(1)})
				if err != nil {
					return nil, err
				}
				return resp.(*types.OrbitShipResponse).Status, nil
			},
			expectStatus:   "in_orbit",
			expectAPICalls: func(r *recordingShipRepo) int { return r.orbitCalls },
		},
		{
			name:          "dock while already docked",
			initialStatus: domainNavigation.NavStatusDocked,
			handle: func(ctx context.Context, repo *recordingShipRepo, ship *domainNavigation.Ship) (interface{}, error) {
				resp, err := NewDockShipHandler(repo).Handle(ctx, &types.DockShipCommand{Ship: ship, PlayerID: shared.MustNewPlayerID(1)})
				if err != nil {
					return nil, err
				}
				return resp.(*types.DockShipResponse).Status, nil
			},
			expectStatus:   "already_docked",
			expectAPICalls: func(r *recordingShipRepo) int { return r.dockCalls },
		},
		{
			name:          "dock while in orbit triggers API",
			initialStatus: domainNavigation.NavStatusInOrbit,
			handle: func(ctx context.Context, repo *recordingShipRepo, ship *domainNavigation.Ship) (interface{}, error) {
				resp, err := NewDockShipHandler(repo).Handle(ctx, &types.DockShipCommand{Ship: ship, PlayerID: shared.MustNewPlayerID(1)})
				if err != nil {
					return nil, err
				}
				return resp.(*types.DockShipResponse).Status, nil
			},
			expectStatus:   "docked",
			expectAPICalls: func(r *recordingShipRepo) int { return r.dockCalls },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := &recordingShipRepo{}
			ship := newShipInState(t, tc.initialStatus)

			status, err := tc.handle(context.Background(), repo, ship)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if status != tc.expectStatus {
				t.Fatalf("expected status %q, got %q", tc.expectStatus, status)
			}

			alreadyInState := tc.expectStatus == "already_in_orbit" || tc.expectStatus == "already_docked"
			calls := tc.expectAPICalls(repo)
			if alreadyInState && calls != 0 {
				t.Fatalf("expected no API call when already in state, got %d", calls)
			}
			if !alreadyInState && calls != 1 {
				t.Fatalf("expected exactly one API call on state change, got %d", calls)
			}
		})
	}
}

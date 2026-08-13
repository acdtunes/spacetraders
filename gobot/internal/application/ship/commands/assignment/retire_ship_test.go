package assignment

import (
	"context"
	"errors"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// retireStubShipRepo embeds the domain interface so only the two methods the handler
// touches need implementations; any unexpected call panics on a nil-method deref.
type retireStubShipRepo struct {
	navigation.ShipRepository

	ship    *navigation.Ship
	findErr error

	retireErr     error
	retiredSymbol string
	retiredFlag   bool
	retiredPlayer shared.PlayerID
	retireCalled  int
}

func (s *retireStubShipRepo) FindBySymbol(_ context.Context, _ string, _ shared.PlayerID) (*navigation.Ship, error) {
	if s.findErr != nil {
		return nil, s.findErr
	}
	return s.ship, nil
}

func (s *retireStubShipRepo) SetShipRetiring(_ context.Context, shipSymbol string, retiring bool, playerID shared.PlayerID) error {
	s.retireCalled++
	s.retiredSymbol = shipSymbol
	s.retiredFlag = retiring
	s.retiredPlayer = playerID
	return s.retireErr
}

// newRetireTestShip builds a hull holding `units` of cargo — the only ship state the
// retire handler reads, since the drain verdict is the live hold.
func newRetireTestShip(t *testing.T, symbol string, units int) *navigation.Ship {
	t.Helper()
	var inv []*shared.CargoItem
	if units > 0 {
		item, err := shared.NewCargoItem("G1", "G1", "", units)
		if err != nil {
			t.Fatalf("build cargo item: %v", err)
		}
		inv = []*shared.CargoItem{item}
	}
	cargo, err := shared.NewCargo(40, units, inv)
	if err != nil {
		t.Fatalf("build cargo: %v", err)
	}
	fuel, err := shared.NewFuel(100, 100)
	if err != nil {
		t.Fatalf("build fuel: %v", err)
	}
	wp, err := shared.NewWaypoint("X1-TW-A2", 0, 0)
	if err != nil {
		t.Fatalf("build waypoint: %v", err)
	}
	ship, err := navigation.NewShip(symbol, shared.MustNewPlayerID(2), wp, fuel, 100, 40, cargo,
		30, "FRAME_HEAVY_FREIGHTER", "HAULER", nil, navigation.NavStatusDocked)
	if err != nil {
		t.Fatalf("build ship: %v", err)
	}
	return ship
}

// The verb writes through the single retirement write path and reports the hull's drain
// state, so an operator learns at once whether the hull is already scrap-ready or still
// has a load to sell.
func TestRetireShip_MarksAndReportsDrainState(t *testing.T) {
	cases := []struct {
		name        string
		cancel      bool
		cargoUnits  int
		wantFlag    bool
		wantDrained bool
	}{
		{name: "retire a laden hull", cargoUnits: 20, wantFlag: true, wantDrained: false},
		{name: "retire an empty hull", cargoUnits: 0, wantFlag: true, wantDrained: true},
		{name: "cancel a retirement", cancel: true, cargoUnits: 0, wantFlag: false, wantDrained: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &retireStubShipRepo{ship: newRetireTestShip(t, "TORWIND-1E", tc.cargoUnits)}
			handler := NewRetireShipHandler(repo, nil)

			pid := 7
			resp, err := handler.Handle(context.Background(), &RetireShipCommand{
				ShipSymbol: "TORWIND-1E", Cancel: tc.cancel, PlayerID: &pid,
			})
			if err != nil {
				t.Fatalf("expected no error, got: %v", err)
			}

			retireResp, ok := resp.(*RetireShipResponse)
			if !ok {
				t.Fatalf("unexpected response type: %T", resp)
			}
			if repo.retireCalled != 1 {
				t.Fatalf("expected exactly one SetShipRetiring call, got %d", repo.retireCalled)
			}
			if repo.retiredSymbol != "TORWIND-1E" || repo.retiredFlag != tc.wantFlag {
				t.Fatalf("expected TORWIND-1E retiring=%t, got %q retiring=%t", tc.wantFlag, repo.retiredSymbol, repo.retiredFlag)
			}
			if repo.retiredPlayer.Value() != 7 {
				t.Fatalf("expected player 7, got %d", repo.retiredPlayer.Value())
			}
			if retireResp.Retiring != tc.wantFlag || retireResp.Drained != tc.wantDrained {
				t.Fatalf("expected retiring=%t drained=%t, got retiring=%t drained=%t",
					tc.wantFlag, tc.wantDrained, retireResp.Retiring, retireResp.Drained)
			}
			if retireResp.CargoUnits != tc.cargoUnits {
				t.Fatalf("expected cargo %d, got %d", tc.cargoUnits, retireResp.CargoUnits)
			}
		})
	}
}

// Fail closed: an unreadable hull writes no mark. An operator must never be told a hull is
// retiring when the daemon could not even read it.
func TestRetireShip_UnreadableHullWritesNothing(t *testing.T) {
	repo := &retireStubShipRepo{findErr: errors.New("db down")}
	handler := NewRetireShipHandler(repo, nil)

	pid := 7
	if _, err := handler.Handle(context.Background(), &RetireShipCommand{ShipSymbol: "TORWIND-1E", PlayerID: &pid}); err == nil {
		t.Fatal("expected an error when the hull cannot be read")
	}
	if repo.retireCalled != 0 {
		t.Fatalf("expected no retirement write, got %d", repo.retireCalled)
	}
}

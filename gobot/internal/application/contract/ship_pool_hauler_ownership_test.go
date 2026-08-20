package contract

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// OwnsHaulerHull is the OWNERSHIP half of the command-frigate cargo baseline: the baseline is a
// comparison ("dispatch a light hauler instead"), so it may only apply when a hauler exists to
// dispatch. Every case here is one clause of that predicate — a hull the pool could never treat
// as a hauler must not silently keep the baseline armed, and a hull that is merely BUSY must.
func TestOwnsHaulerHull(t *testing.T) {
	inTransit := func(ship *navigation.Ship) *navigation.Ship {
		ship.SetNavStatus(navigation.NavStatusInTransit)
		return ship
	}
	dedicated := func(ship *navigation.Ship, fleet string) *navigation.Ship {
		ship.SetDedicatedFleet(fleet)
		return ship
	}

	tests := []struct {
		name  string
		ships []*navigation.Ship
		want  bool
	}{
		{
			name:  "empty fleet owns no hauler",
			ships: nil,
			want:  false,
		},
		{
			name:  "COLDSTART: the command frigate alone is not a hauler to prefer over itself",
			ships: []*navigation.Ship{newCandidateShip(t, "TORWIND-1", "COMMAND", 40, 0, 0)},
			want:  false,
		},
		{
			name:  "a flagship mis-registered HAULER still never counts itself (\"*-1\" symbol)",
			ships: []*navigation.Ship{newCandidateShip(t, "TORWIND-1", "HAULER", 40, 0, 0)},
			want:  false,
		},
		{
			name:  "a 0-cargo hull tagged HAULER single-trips nothing and is no hauler",
			ships: []*navigation.Ship{newCandidateShip(t, "TORWIND-5", "HAULER", 0, 0, 0)},
			want:  false,
		},
		{
			name:  "non-haul roles are not haulers",
			ships: []*navigation.Ship{newCandidateShip(t, "TORWIND-3", "EXCAVATOR", 30, 0, 0), newCandidateShip(t, "TORWIND-4", "SATELLITE", 0, 0, 0)},
			want:  false,
		},
		{
			name:  "an idle hauler is owned",
			ships: []*navigation.Ship{newCandidateShip(t, "TORWIND-7", "HAULER", 80, 0, 0)},
			want:  true,
		},
		{
			name:  "OWNERSHIP not idleness: a hauler in transit is still worth waiting for",
			ships: []*navigation.Ship{inTransit(newCandidateShip(t, "TORWIND-7", "HAULER", 80, 0, 0))},
			want:  true,
		},
		{
			name:  "a hauler pinned to another coordinator's fleet is still owned",
			ships: []*navigation.Ship{dedicated(newCandidateShip(t, "TORWIND-7", "HAULER", 80, 0, 0), "mining")},
			want:  true,
		},
		{
			name:  "the frigate alongside a real hauler",
			ships: []*navigation.Ship{newCandidateShip(t, "TORWIND-1", "COMMAND", 40, 0, 0), newCandidateShip(t, "TORWIND-7", "HAULER", 80, 0, 0)},
			want:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := OwnsHaulerHull(context.Background(), shared.MustNewPlayerID(1), &stubShipRepo{ships: tt.ships})
			if err != nil {
				t.Fatalf("OwnsHaulerHull: %v", err)
			}
			if got != tt.want {
				t.Fatalf("OwnsHaulerHull() = %v, want %v", got, tt.want)
			}
		})
	}
}

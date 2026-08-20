package contract

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// HaulerAlternativeAvailable is the COMPARISON half of the command-frigate cargo baseline: the
// baseline says "dispatch a light hauler instead", so it may only apply when a hauler the
// coordinator could actually dispatch instead EXISTS. Every case here is one clause of that
// predicate — a hull the pool could never treat as a hauler must not silently keep the baseline
// armed, and neither must one that is not dispatchable at all this tick.
//
// sp-6378a corrects sp-00y49's ownership-only reading of that predicate. sp-00y49 counted a hauler
// in ANY state, reasoning that waiting for it beats a 40-cargo double-trip. Live on TORWINDSTG4
// that reasoning broke down: sp-5kn8v only releases the frigate to contract work once hauler #1
// exists, so ownership always read true, the baseline re-armed on every pass, and the released
// frigate sat fully idle instead of waiting productively. Availability, not ownership, is what the
// comparison needs — with the pin case (below) kept as-is.
func TestHaulerAlternativeAvailable(t *testing.T) {
	inTransit := func(ship *navigation.Ship) *navigation.Ship {
		ship.SetNavStatus(navigation.NavStatusInTransit)
		return ship
	}
	claimed := func(t *testing.T, ship *navigation.Ship) *navigation.Ship {
		t.Helper()
		if err := ship.AssignToContainer("contract-worker-"+ship.ShipSymbol(), shared.NewRealClock()); err != nil {
			t.Fatalf("AssignToContainer: %v", err)
		}
		return ship
	}
	dedicated := func(ship *navigation.Ship, fleet string) *navigation.Ship {
		ship.SetDedicatedFleet(fleet)
		return ship
	}

	tests := []struct {
		name  string
		ships func(t *testing.T) []*navigation.Ship
		want  bool
	}{
		{
			name:  "empty fleet has no alternative",
			ships: func(*testing.T) []*navigation.Ship { return nil },
			want:  false,
		},
		{
			name: "COLDSTART: the command frigate alone is not an alternative to itself",
			ships: func(t *testing.T) []*navigation.Ship {
				return []*navigation.Ship{newCandidateShip(t, "TORWIND-1", "COMMAND", 40, 0, 0)}
			},
			want: false,
		},
		{
			name: "a flagship mis-registered HAULER still never counts itself (\"*-1\" symbol)",
			ships: func(t *testing.T) []*navigation.Ship {
				return []*navigation.Ship{newCandidateShip(t, "TORWIND-1", "HAULER", 40, 0, 0)}
			},
			want: false,
		},
		{
			name: "a 0-cargo hull tagged HAULER single-trips nothing and is no hauler",
			ships: func(t *testing.T) []*navigation.Ship {
				return []*navigation.Ship{newCandidateShip(t, "TORWIND-5", "HAULER", 0, 0, 0)}
			},
			want: false,
		},
		{
			name: "non-haul roles are not haulers",
			ships: func(t *testing.T) []*navigation.Ship {
				return []*navigation.Ship{newCandidateShip(t, "TORWIND-3", "EXCAVATOR", 30, 0, 0), newCandidateShip(t, "TORWIND-4", "SATELLITE", 0, 0, 0)}
			},
			want: false,
		},
		{
			name: "an idle hauler is a genuine alternative",
			ships: func(t *testing.T) []*navigation.Ship {
				return []*navigation.Ship{newCandidateShip(t, "TORWIND-7", "HAULER", 80, 0, 0)}
			},
			want: true,
		},
		{
			// sp-6378a INVERTS sp-00y49's "OWNERSHIP not idleness: a hauler in transit is still
			// worth waiting for". It is not dispatchable on THIS pass, so it is not something the
			// coordinator can send instead of the frigate — and benching the frigate meanwhile buys
			// nothing, since its alternative is idleness, not trade.
			name: "a hauler in transit cannot be dispatched instead of the frigate this tick",
			ships: func(t *testing.T) []*navigation.Ship {
				return []*navigation.Ship{inTransit(newCandidateShip(t, "TORWIND-7", "HAULER", 80, 0, 0))}
			},
			want: false,
		},
		{
			// The live TORWINDSTG4 shape: the fleet's one hauler was mid-contract, not in transit.
			name: "a hauler already claimed by a container is likewise no alternative",
			ships: func(t *testing.T) []*navigation.Ship {
				return []*navigation.Ship{claimed(t, newCandidateShip(t, "TORWIND-7", "HAULER", 80, 0, 0))}
			},
			want: false,
		},
		{
			// RULINGS #7: a pin is an operator decision about where that hull works, so its own
			// nav state is not this coordinator's to read around — unavailable, and it stays that
			// way. Unchanged from sp-00y49.
			name: "a hauler pinned to another coordinator's fleet counts while idle",
			ships: func(t *testing.T) []*navigation.Ship {
				return []*navigation.Ship{dedicated(newCandidateShip(t, "TORWIND-7", "HAULER", 80, 0, 0), "mining")}
			},
			want: true,
		},
		{
			name: "a hauler pinned to another coordinator's fleet counts in transit too",
			ships: func(t *testing.T) []*navigation.Ship {
				return []*navigation.Ship{inTransit(dedicated(newCandidateShip(t, "TORWIND-7", "HAULER", 80, 0, 0), "mining"))}
			},
			want: true,
		},
		{
			name: "the frigate alongside an idle hauler",
			ships: func(t *testing.T) []*navigation.Ship {
				return []*navigation.Ship{newCandidateShip(t, "TORWIND-1", "COMMAND", 40, 0, 0), newCandidateShip(t, "TORWIND-7", "HAULER", 80, 0, 0)}
			},
			want: true,
		},
		{
			// The live regression, end to end: one hauler, busy, and a below-baseline frigate.
			name: "the frigate alongside the fleet's only, busy hauler",
			ships: func(t *testing.T) []*navigation.Ship {
				return []*navigation.Ship{newCandidateShip(t, "TORWIND-1", "COMMAND", 40, 0, 0), claimed(t, newCandidateShip(t, "TORWIND-7", "HAULER", 80, 0, 0))}
			},
			want: false,
		},
		{
			name: "one free hauler among busy ones is enough",
			ships: func(t *testing.T) []*navigation.Ship {
				return []*navigation.Ship{
					inTransit(newCandidateShip(t, "TORWIND-6", "HAULER", 80, 0, 0)),
					newCandidateShip(t, "TORWIND-7", "HAULER", 80, 0, 0),
				}
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := HaulerAlternativeAvailable(context.Background(), shared.MustNewPlayerID(1), &stubShipRepo{ships: tt.ships(t)})
			if err != nil {
				t.Fatalf("HaulerAlternativeAvailable: %v", err)
			}
			if got != tt.want {
				t.Fatalf("HaulerAlternativeAvailable() = %v, want %v", got, tt.want)
			}
		})
	}
}

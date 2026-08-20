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
// The predicate is exactly "is a hauler in THIS coordinator's candidate pool free right now":
// availableNow AND drawable here. Dedication only ever EXCLUDES — the claim-filter drops every
// dedicated hull from the general pool and FindIdleShipsByFleet picks up only the coordinator's
// own tag, so a foreign pin is no candidate in any state, idle included.
const (
	ownFleet = "contract"
	// "trade" is the live foreign pin; stocker/warehouse/depot-delivery are hauler-capable
	// fleets that read identically here — the rule is by-name inequality, not an enumeration.
	otherFleet = "trade"
)

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
			// THE case that separates ownership from pool-membership: this hull is perfectly
			// idle, yet the claim-filter makes it invisible to contract selection — the
			// coordinator cannot dispatch it, so it is no alternative and must not bench the
			// frigate. Inverted by sp-u7n3m; sp-00y49/sp-6378a both counted it.
			name: "an IDLE hauler pinned to a foreign fleet was never a contract candidate",
			ships: func(t *testing.T) []*navigation.Ship {
				return []*navigation.Ship{dedicated(newCandidateShip(t, "TORWIND-7", "HAULER", 80, 0, 0), otherFleet)}
			},
			want: false,
		},
		{
			name: "a foreign-pinned hauler in transit is no alternative either",
			ships: func(t *testing.T) []*navigation.Ship {
				return []*navigation.Ship{inTransit(dedicated(newCandidateShip(t, "TORWIND-7", "HAULER", 80, 0, 0), otherFleet))}
			},
			want: false,
		},
		{
			name: "a BUSY foreign-pinned hauler is no alternative either",
			ships: func(t *testing.T) []*navigation.Ship {
				return []*navigation.Ship{claimed(t, dedicated(newCandidateShip(t, "TORWIND-7", "HAULER", 80, 0, 0), otherFleet))}
			},
			want: false,
		},
		{
			// The one genuine "a better hull is free right now" case, and the reason dedication
			// alone cannot decide this: EXCLUSIVE MODE routes this hull through dedicatedIdleShips,
			// so the coordinator really can send it instead. availableNow's role is untouched.
			name: "an IDLE hauler pinned to this coordinator's own fleet IS a genuine alternative",
			ships: func(t *testing.T) []*navigation.Ship {
				return []*navigation.Ship{dedicated(newCandidateShip(t, "TORWIND-7", "HAULER", 80, 0, 0), ownFleet)}
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
			// sp-u7n3m AC1, the live TORWINDSTG4 shape sp-6378a still failed on: the fleet's one
			// hauler is busy AND carries THIS coordinator's own "contract" pin. That pin does not
			// wall it off from contract work — it IS contract work, mid-leg — so it is no
			// alternative, and the baseline must not re-arm against the released frigate.
			// dedicatedIdleShips is empty on this tick, which is precisely when RULINGS #7's
			// last-resort admission is meant to seat the frigate.
			name: "the fleet's only hauler is busy AND pinned to this coordinator's own fleet",
			ships: func(t *testing.T) []*navigation.Ship {
				return []*navigation.Ship{
					newCandidateShip(t, "TORWINDSTG4-1", "COMMAND", 40, 0, 0),
					claimed(t, dedicated(newCandidateShip(t, "TORWINDSTG4-5", "HAULER", 80, 0, 0), ownFleet)),
				}
			},
			want: false,
		},
		{
			name: "an own-fleet-pinned hauler in transit is likewise no alternative",
			ships: func(t *testing.T) []*navigation.Ship {
				return []*navigation.Ship{inTransit(dedicated(newCandidateShip(t, "TORWINDSTG4-5", "HAULER", 80, 0, 0), ownFleet))}
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
			got, err := HaulerAlternativeAvailable(context.Background(), shared.MustNewPlayerID(1), &stubShipRepo{ships: tt.ships(t)}, ownFleet)
			if err != nil {
				t.Fatalf("HaulerAlternativeAvailable: %v", err)
			}
			if got != tt.want {
				t.Fatalf("HaulerAlternativeAvailable() = %v, want %v", got, tt.want)
			}
		})
	}
}

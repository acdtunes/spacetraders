package contract

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// stubShipRepo serves a fixed fleet from FindAllByPlayer and leaves every other
// ShipRepository method embedded (nil), so a test panics loudly if candidate
// discovery ever reaches for something other than the full fleet snapshot.
type stubShipRepo struct {
	navigation.ShipRepository
	ships []*navigation.Ship
}

func (r *stubShipRepo) FindAllByPlayer(ctx context.Context, playerID shared.PlayerID) ([]*navigation.Ship, error) {
	return r.ships, nil
}

// newCandidateShip builds an idle, docked ship at (x,y) with the given symbol,
// role and cargo capacity - the minimum surface a coordinator inspects when
// deciding whether a hull is a haul candidate.
func newCandidateShip(t *testing.T, symbol, role string, cargoCap int, x, y float64) *navigation.Ship {
	t.Helper()
	cargo, err := shared.NewCargo(cargoCap, 0, nil)
	if err != nil {
		t.Fatalf("build cargo: %v", err)
	}
	fuel, err := shared.NewFuel(100, 100)
	if err != nil {
		t.Fatalf("build fuel: %v", err)
	}
	wp, err := shared.NewWaypoint("X1-TW-A2", x, y)
	if err != nil {
		t.Fatalf("build waypoint: %v", err)
	}
	ship, err := navigation.NewShip(
		symbol,
		shared.MustNewPlayerID(1),
		wp,
		fuel,
		100,
		cargoCap,
		cargo,
		30,
		"FRAME_FRIGATE",
		role,
		nil,
		navigation.NavStatusDocked,
	)
	if err != nil {
		t.Fatalf("build ship: %v", err)
	}
	return ship
}

func containsSymbol(symbols []string, want string) bool {
	for _, s := range symbols {
		if s == want {
			return true
		}
	}
	return false
}

// The contract coordinator must treat the command ship as a first-class haul
// candidate, not a zero-hauler fallback. Before sp-4a4e a 40-cargo COMMAND
// frigate sat benched for hours whenever any hauler existed, because it only
// entered the pool when NO haulers existed at all - so a free, fast hull
// contributed nothing while a light hauler flew oversized legs.
func TestFindIdleLightHaulers_IncludesIdleCommandShipAlongsideHaulers(t *testing.T) {
	hauler := newCandidateShip(t, "TORWIND-3", "HAULER", 30, 700, 0)  // idle, far
	command := newCandidateShip(t, "TORWIND-1", "COMMAND", 40, 50, 0) // idle, close, command
	repo := &stubShipRepo{ships: []*navigation.Ship{hauler, command}}

	_, symbols, err := FindIdleLightHaulers(context.Background(), shared.MustNewPlayerID(1), repo, IncludeCommandShip)
	if err != nil {
		t.Fatalf("FindIdleLightHaulers: %v", err)
	}

	if !containsSymbol(symbols, "TORWIND-1") {
		t.Fatalf("command ship TORWIND-1 excluded from candidate pool %v - it is idle and must be a first-class haul candidate, not a benched fallback", symbols)
	}
	if !containsSymbol(symbols, "TORWIND-3") {
		t.Fatalf("hauler TORWIND-3 missing from candidate pool %v", symbols)
	}
}

// Acceptance (sp-4a4e): with the only hauler busy and the command ship idle, the
// coordinator must be able to dispatch the command ship - not fall through to an
// empty pool and wait 5h+ while a 40-cargo hull sits docked. The fallback-only
// design returned nothing here because a (busy) hauler existed.
func TestFindIdleLightHaulers_BusyHauler_IdleCommandShip_CommandIsCandidate(t *testing.T) {
	hauler := newCandidateShip(t, "TORWIND-3", "HAULER", 30, 700, 0)
	if err := hauler.AssignToContainer("contract-worker-TORWIND-3", shared.NewRealClock()); err != nil {
		t.Fatalf("assign hauler busy: %v", err)
	}
	command := newCandidateShip(t, "TORWIND-1", "COMMAND", 40, 50, 0)
	repo := &stubShipRepo{ships: []*navigation.Ship{hauler, command}}

	_, symbols, err := FindIdleLightHaulers(context.Background(), shared.MustNewPlayerID(1), repo, IncludeCommandShip)
	if err != nil {
		t.Fatalf("FindIdleLightHaulers: %v", err)
	}

	if len(symbols) != 1 || symbols[0] != "TORWIND-1" {
		t.Fatalf("expected only the idle command ship [TORWIND-1] as candidate while the hauler is busy, got %v", symbols)
	}
}

// Scope guard: manufacturing/factory coordinators call FindIdleLightHaulers
// without opting in (ExcludeCommandShip default), and must never draft the
// command ship - it stays reserved for contracts and manual operations. Only
// haulers return.
func TestFindIdleLightHaulers_ExcludesCommandShip_WhenNotOptedIn(t *testing.T) {
	hauler := newCandidateShip(t, "TORWIND-3", "HAULER", 30, 700, 0)
	command := newCandidateShip(t, "TORWIND-1", "COMMAND", 40, 50, 0)
	repo := &stubShipRepo{ships: []*navigation.Ship{hauler, command}}

	_, symbols, err := FindIdleLightHaulers(context.Background(), shared.MustNewPlayerID(1), repo)
	if err != nil {
		t.Fatalf("FindIdleLightHaulers: %v", err)
	}

	if containsSymbol(symbols, "TORWIND-1") {
		t.Fatalf("command ship TORWIND-1 must stay out of the manufacturing pool %v", symbols)
	}
	if !containsSymbol(symbols, "TORWIND-3") {
		t.Fatalf("hauler TORWIND-3 missing from manufacturing pool %v", symbols)
	}
}

// Claim-filter (sp-snmb): a ship marked DedicatedFleet is reserved exclusively
// for the contract coordinator's own direct lookup (FindIdleDedicatedShips) -
// every other coordinator (manufacturing, factory, gas, balance-handler) shares
// this same discovery function, so excluding dedicated ships here, unconditionally,
// is what makes them invisible fleet-wide "for free" without touching every
// caller individually.
func TestFindIdleLightHaulers_ExcludesDedicatedShips(t *testing.T) {
	dedicated := newCandidateShip(t, "TORWIND-4", "HAULER", 30, 10, 0)
	dedicated.SetDedicatedFleet("contract")
	general := newCandidateShip(t, "TORWIND-3", "HAULER", 30, 700, 0)
	repo := &stubShipRepo{ships: []*navigation.Ship{dedicated, general}}

	_, symbols, err := FindIdleLightHaulers(context.Background(), shared.MustNewPlayerID(1), repo, IncludeCommandShip)
	if err != nil {
		t.Fatalf("FindIdleLightHaulers: %v", err)
	}

	if containsSymbol(symbols, "TORWIND-4") {
		t.Fatalf("dedicated ship TORWIND-4 must be excluded from the general pool %v - it is reserved for the contract coordinator's own dedicated lookup", symbols)
	}
	if !containsSymbol(symbols, "TORWIND-3") {
		t.Fatalf("non-dedicated hauler TORWIND-3 missing from candidate pool %v", symbols)
	}
}

// FindIdleDedicatedShips is the contract coordinator's direct lookup for its
// reserved fleet (sp-snmb): given the operator-supplied --dedicated-ships list,
// it returns only the ones that are currently idle - busy ships and unknown
// symbols are silently skipped rather than erroring, since the fleet composition
// legitimately varies (a dedicated ship might be mid-delivery, or not yet owned).
func TestFindIdleDedicatedShips_ReturnsOnlyIdleListedShips(t *testing.T) {
	idle := newCandidateShip(t, "TORWIND-4", "HAULER", 30, 10, 0)
	idle.SetDedicatedFleet("contract")
	busy := newCandidateShip(t, "TORWIND-5", "HAULER", 30, 10, 0)
	busy.SetDedicatedFleet("contract")
	if err := busy.AssignToContainer("contract-worker-TORWIND-5", shared.NewRealClock()); err != nil {
		t.Fatalf("assign busy dedicated ship: %v", err)
	}
	unrelated := newCandidateShip(t, "TORWIND-3", "HAULER", 30, 700, 0)
	repo := &stubShipRepo{ships: []*navigation.Ship{idle, busy, unrelated}}

	_, symbols, err := FindIdleDedicatedShips(context.Background(), shared.MustNewPlayerID(1), repo, []string{"TORWIND-4", "TORWIND-5", "TORWIND-9"})
	if err != nil {
		t.Fatalf("FindIdleDedicatedShips: %v", err)
	}

	if len(symbols) != 1 || symbols[0] != "TORWIND-4" {
		t.Fatalf("expected only the idle dedicated ship [TORWIND-4], got %v", symbols)
	}
}

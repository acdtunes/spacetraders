package commands

import (
	"context"
	"fmt"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// reconcileStubShipRepo serves canned ships by symbol and records every Save
// call, so tests can assert exactly which ships were reconciled and with what
// DedicatedFleet() value - without needing a real database.
type reconcileStubShipRepo struct {
	navigation.ShipRepository
	ships     map[string]*navigation.Ship
	saveErr   map[string]error // symbol -> error to return from Save, if any
	saveCalls []string         // ship symbols passed to Save, in order
}

func (s *reconcileStubShipRepo) FindBySymbol(_ context.Context, symbol string, _ shared.PlayerID) (*navigation.Ship, error) {
	ship, ok := s.ships[symbol]
	if !ok {
		return nil, fmt.Errorf("ship %s not found", symbol)
	}
	return ship, nil
}

func (s *reconcileStubShipRepo) Save(_ context.Context, ship *navigation.Ship) error {
	s.saveCalls = append(s.saveCalls, ship.ShipSymbol())
	if err, ok := s.saveErr[ship.ShipSymbol()]; ok {
		return err
	}
	return nil
}

// Every symbol on the operator's --dedicated-ships list must be marked into
// the named fleet and persisted, so the claim-filter in FindIdleLightHaulers
// actually takes effect (sp-snmb).
func TestReconcileDedicatedFleet_MarksConfiguredShipsAsDedicated(t *testing.T) {
	shipA := newHomeTestShip(t, "TORWIND-4", "X1-TEST-A1", 0, 0)
	shipB := newHomeTestShip(t, "TORWIND-5", "X1-TEST-A1", 0, 0)
	repo := &reconcileStubShipRepo{ships: map[string]*navigation.Ship{
		"TORWIND-4": shipA,
		"TORWIND-5": shipB,
	}}
	logger := &completionCapturingLogger{}

	reconcileDedicatedFleet(context.Background(), logger, repo, shared.MustNewPlayerID(1), []string{"TORWIND-4", "TORWIND-5"}, "contract")

	if shipA.DedicatedFleet() != "contract" {
		t.Fatalf("expected TORWIND-4 marked into the contract fleet, got %q", shipA.DedicatedFleet())
	}
	if shipB.DedicatedFleet() != "contract" {
		t.Fatalf("expected TORWIND-5 marked into the contract fleet, got %q", shipB.DedicatedFleet())
	}
	if len(repo.saveCalls) != 2 {
		t.Fatalf("expected exactly 2 Save calls, got %d: %v", len(repo.saveCalls), repo.saveCalls)
	}
}

// A ship already reconciled into the target fleet on a prior pass must not be
// saved again - reconciliation runs on every coordinator startup, and a fleet
// of already-dedicated ships must not generate redundant DB writes each time.
func TestReconcileDedicatedFleet_AlreadyReconciled_SkipsSave(t *testing.T) {
	ship := newHomeTestShip(t, "TORWIND-4", "X1-TEST-A1", 0, 0)
	ship.SetDedicatedFleet("contract")
	repo := &reconcileStubShipRepo{ships: map[string]*navigation.Ship{"TORWIND-4": ship}}
	logger := &completionCapturingLogger{}

	reconcileDedicatedFleet(context.Background(), logger, repo, shared.MustNewPlayerID(1), []string{"TORWIND-4"}, "contract")

	if len(repo.saveCalls) != 0 {
		t.Fatalf("expected no Save call for an already-reconciled ship, got %d: %v", len(repo.saveCalls), repo.saveCalls)
	}
}

// An unknown ship symbol (e.g. sold or renamed since the operator's
// --dedicated-ships flag was last updated) must log a warning and continue
// reconciling the remaining ships, not abort the whole pass.
func TestReconcileDedicatedFleet_UnknownShipSymbol_LogsWarningAndContinues(t *testing.T) {
	knownShip := newHomeTestShip(t, "TORWIND-5", "X1-TEST-A1", 0, 0)
	repo := &reconcileStubShipRepo{ships: map[string]*navigation.Ship{"TORWIND-5": knownShip}}
	logger := &completionCapturingLogger{}

	reconcileDedicatedFleet(context.Background(), logger, repo, shared.MustNewPlayerID(1), []string{"TORWIND-GONE", "TORWIND-5"}, "contract")

	if knownShip.DedicatedFleet() != "contract" {
		t.Fatalf("expected the known ship to still be reconciled despite the unknown symbol, got %q", knownShip.DedicatedFleet())
	}
	if len(repo.saveCalls) != 1 || repo.saveCalls[0] != "TORWIND-5" {
		t.Fatalf("expected exactly one Save call for the known ship, got %v", repo.saveCalls)
	}
	foundWarning := false
	for _, entry := range logger.entries {
		if entry.level == "WARNING" {
			foundWarning = true
		}
	}
	if !foundWarning {
		t.Fatalf("expected a WARNING log for the unknown ship symbol, got entries: %+v", logger.entries)
	}
}

// An empty --dedicated-ships list (the default, no dedicated fleet
// configured) must not touch the repository at all.
func TestReconcileDedicatedFleet_EmptyList_NoOp(t *testing.T) {
	repo := &reconcileStubShipRepo{ships: map[string]*navigation.Ship{}}
	logger := &completionCapturingLogger{}

	reconcileDedicatedFleet(context.Background(), logger, repo, shared.MustNewPlayerID(1), nil, "contract")

	if len(repo.saveCalls) != 0 {
		t.Fatalf("expected no Save calls for an empty dedicated-ships list, got %v", repo.saveCalls)
	}
	if len(logger.entries) != 0 {
		t.Fatalf("expected no log entries for an empty dedicated-ships list, got %+v", logger.entries)
	}
}

// A symbol present on the operator's --dedicated-ships list is dedicated -
// this decides whether the "previous ship" hook homes a ship instead of
// balancing it to a market (sp-snmb).
func TestIsDedicatedShip_SymbolInList_ReturnsTrue(t *testing.T) {
	if !isDedicatedShip("TORWIND-4", []string{"TORWIND-4", "TORWIND-5"}) {
		t.Fatalf("expected TORWIND-4 to be reported as dedicated")
	}
}

// A symbol absent from the list is not dedicated - it must get the normal
// market-balancing treatment.
func TestIsDedicatedShip_SymbolNotInList_ReturnsFalse(t *testing.T) {
	if isDedicatedShip("TORWIND-9", []string{"TORWIND-4", "TORWIND-5"}) {
		t.Fatalf("expected TORWIND-9 to be reported as not dedicated")
	}
}

// No configured dedicated ships at all means every ship gets the normal
// market-balancing treatment.
func TestIsDedicatedShip_EmptyList_ReturnsFalse(t *testing.T) {
	if isDedicatedShip("TORWIND-4", nil) {
		t.Fatalf("expected no ship to be reported as dedicated with an empty list")
	}
}

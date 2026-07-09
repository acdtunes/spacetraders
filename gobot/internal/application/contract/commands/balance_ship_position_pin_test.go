package commands

import (
	"context"
	"testing"

	domainContainer "github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// --- test doubles (port boundaries only) -------------------------------------

// balanceFakeContainerRepo satisfies the balancing handler's ContainerRepository
// port. The temporary balancing-container record is best-effort bookkeeping and
// irrelevant to the claim path under test, so Add/Remove are inert.
type balanceFakeContainerRepo struct{}

func (r *balanceFakeContainerRepo) Add(_ context.Context, _ *domainContainer.Container, _ string) error {
	return nil
}

func (r *balanceFakeContainerRepo) Remove(_ context.Context, _ string, _ int) error {
	return nil
}

// balanceFakeMarketRepo returns a canned market set. An empty set makes Handle
// return early (Navigated:false) right after the claim — the exact cutoff these
// tests want, so the heavier navigation/graph collaborators are never reached.
type balanceFakeMarketRepo struct {
	markets []string
}

func (r *balanceFakeMarketRepo) FindAllMarketsInSystem(_ context.Context, _ string, _ int) ([]string, error) {
	return r.markets, nil
}

func newBalanceTestHandler(shipRepo navigation.ShipRepository, marketRepo MarketRepository) *BalanceShipPositionHandler {
	// mediator + graphProvider are nil on purpose: both tests stop at the claim
	// (rejection) or at the empty-market cutoff, before any navigation or graph
	// lookup, so those collaborators are never dereferenced.
	return NewBalanceShipPositionHandler(nil, shipRepo, &balanceFakeContainerRepo{}, nil, marketRepo, shared.NewRealClock())
}

func balanceCommand() *BalanceShipPositionCommand {
	return &BalanceShipPositionCommand{
		ShipSymbol: "TORWIND-3",
		PlayerID:   shared.MustNewPlayerID(1),
	}
}

// --- balance ship-position claim path (sp-lprs, l7h2 Phase 2.5) ---------------

// The balancing reservation is now the atomic operation-checked ClaimShip under
// the contract fleet identity ("contract"), replacing the old non-atomic
// AssignToContainer+Save. A claimable (contract-pinned or unpinned) hull is
// claimed for the ship-balancing container, and the claim is released on exit.
func TestBalanceShipPosition_ClaimsUnderContractOperation(t *testing.T) {
	ship := newNegotiateTestShip(t, navigation.NavStatusInOrbit)
	repo := &spawnContractFakeShipRepo{ship: ship}
	// No markets in the system -> Handle returns right after the claim.
	handler := newBalanceTestHandler(repo, &balanceFakeMarketRepo{})

	resp, err := handler.Handle(context.Background(), balanceCommand())
	if err != nil {
		t.Fatalf("expected best-effort balance to succeed, got error: %v", err)
	}
	if balResp, ok := resp.(*BalanceShipPositionResponse); !ok || balResp.Navigated {
		t.Fatalf("expected no navigation with an empty market set, got %+v", resp)
	}
	if claim := repo.lastClaim(t); claim.symbol != "TORWIND-3" || claim.containerID != "ship-balancing-TORWIND-3" || claim.operation != "contract" {
		t.Fatalf("expected atomic claim of TORWIND-3 by the balancing container under operation contract, got %+v", claim)
	}
	// The committed claim is released on exit (the ship returns to the pool).
	if snap := repo.lastSave(t); snap.assigned {
		t.Fatalf("expected the balancing claim released on exit, got still-assigned %+v", snap)
	}
}

// sp-lprs: a hull the captain pinned to another fleet — the command frigate's
// "command" pin is the poach vector — is rejected inside ClaimShip's locked
// transaction. Balancing is best-effort repositioning, so it SKIPS the ship
// (no navigation) rather than poaching it. Critically, because the release
// defer is armed only after a successful claim, the foreign-pinned hull is
// never written to: no force-release stomps its real owner's assignment.
func TestBalanceShipPosition_CommandPinnedFrigate_SkippedNotPoached(t *testing.T) {
	ship := newNegotiateTestShip(t, navigation.NavStatusInOrbit)
	repo := &spawnContractFakeShipRepo{
		ship:     ship,
		claimErr: shared.NewShipDedicatedToOtherFleetError("TORWIND-3", "command", "contract"),
	}
	handler := newBalanceTestHandler(repo, &balanceFakeMarketRepo{markets: []string{"X1-TEST-MKT"}})

	resp, err := handler.Handle(context.Background(), balanceCommand())
	if err != nil {
		t.Fatalf("expected a rejected claim to skip balancing (best-effort), got error: %v", err)
	}
	if balResp, ok := resp.(*BalanceShipPositionResponse); !ok || balResp.Navigated {
		t.Fatalf("expected the foreign-pinned hull NOT navigated, got %+v", resp)
	}
	// The foreign-pinned hull is never written: no successful claim, and no
	// release Save (the release defer is armed only after a committed claim).
	if len(repo.claims) != 0 {
		t.Fatalf("expected no successful claim of a command-pinned hull, got %v", repo.claims)
	}
	if len(repo.saves) != 0 {
		t.Fatalf("expected the command-pinned hull untouched (no force-release stomp), got %v", repo.saves)
	}
}

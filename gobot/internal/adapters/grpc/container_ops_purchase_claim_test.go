package grpc

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// purchaseClaimShipRepo is a fake at the ship-repository PORT boundary for the
// purchase-claim tests. Its ClaimShip mirrors the two guards the real repository
// applies inside its row-locked transaction (ship_repository.go ClaimShip):
// a hull already held by a different container is refused with the standing
// already-assigned error, and a hull whose DedicatedFleet names a fleet other
// than the claiming operation is refused with the standing dedication error.
// Modelling the port contract here (rather than injecting a canned error) is what
// lets a test observe whether the purchase op declares the RIGHT identity instead
// of merely "some" identity — a claim that declared a wildcard, or the hull's own
// tag, would sail past an injected-error fake.
type purchaseClaimShipRepo struct {
	navigation.ShipRepository
	mu     sync.Mutex
	ships  map[string]*navigation.Ship
	claims []tradeShipClaim
}

func (r *purchaseClaimShipRepo) ClaimShip(_ context.Context, symbol, containerID string, _ shared.PlayerID, operation string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	ship, ok := r.ships[symbol]
	if !ok {
		return shared.NewShipAssignmentError("ship not found", symbol, "")
	}
	if ship.IsAssigned() && ship.ContainerID() != containerID {
		return shared.NewShipAlreadyAssignedError(symbol, ship.ContainerID())
	}
	if ship.DedicatedFleet() != "" && ship.DedicatedFleet() != operation {
		return shared.NewShipDedicatedToOtherFleetError(symbol, ship.DedicatedFleet(), operation)
	}

	r.claims = append(r.claims, tradeShipClaim{symbol: symbol, containerID: containerID, operation: operation})
	if !ship.IsAssigned() {
		_ = ship.AssignToContainer(containerID, shared.NewRealClock())
	}
	return nil
}

func (r *purchaseClaimShipRepo) FindBySymbol(_ context.Context, symbol string, _ shared.PlayerID) (*navigation.Ship, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ships[symbol], nil
}

func (r *purchaseClaimShipRepo) Save(_ context.Context, ship *navigation.Ship) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ships[ship.ShipSymbol()] = ship
	return nil
}

func (r *purchaseClaimShipRepo) SaveWithRetry(ctx context.Context, symbol string, playerID shared.PlayerID, mutate navigation.ShipMutation) (*navigation.Ship, bool, error) {
	ship, err := r.FindBySymbol(ctx, symbol, playerID)
	if err != nil {
		return nil, false, err
	}
	changed, err := mutate(ship)
	if err != nil {
		return ship, false, err
	}
	if !changed {
		return ship, false, nil
	}
	return ship, true, r.Save(ctx, ship)
}

func (r *purchaseClaimShipRepo) FindByContainer(_ context.Context, containerID string, _ shared.PlayerID) ([]*navigation.Ship, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*navigation.Ship
	for _, s := range r.ships {
		if s.IsAssigned() && s.ContainerID() == containerID {
			out = append(out, s)
		}
	}
	return out, nil
}

func (r *purchaseClaimShipRepo) recordedClaims() []tradeShipClaim {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]tradeShipClaim(nil), r.claims...)
}

// purchaseLaunchers are the two production entry points that create a PURCHASE
// container. Both must declare the same buying identity — a fix applied to only
// one of them leaves the other dying on the live fleet.
var purchaseLaunchers = []struct {
	name   string
	launch func(s *DaemonServer, purchaser string, pid int) (string, error)
}{
	{"purchase_ship", func(s *DaemonServer, purchaser string, pid int) (string, error) {
		id, _, _, _, _, err := s.PurchaseShip(context.Background(), purchaser, "SHIP_PROBE", pid, nil)
		return id, err
	}},
	{"batch_purchase_ships", func(s *DaemonServer, purchaser string, pid int) (string, error) {
		id, _, _, _, _, err := s.BatchPurchaseShips(context.Background(), purchaser, "SHIP_PROBE", 1, 0, pid, nil, nil, "")
		return id, err
	}},
}

func newPurchasingFrigate(t *testing.T, symbol string, playerID int) *navigation.Ship {
	t.Helper()
	frigate := newIdleTradeShip(t, symbol, playerID)
	frigate.SetDedicatedFleet(navigation.PurchasingFleet)
	return frigate
}

// PRODUCTION REPRODUCTION (agent TORWIND, 7 claim_failed containers). The command
// frigate TORWIND-1 is retired from contracts at the first-hauler pivot and tagged
// dedicated_fleet="purchasing" — the EXCLUSIVE, protected buy ship every subsequent
// purchase is routed through (navigation.PurchasingFleet; bootstrapAcquirer.buyWith
// PREFERS it). The buy is then executed by a PURCHASE container that names the
// frigate as its purchaser.
//
// That container carried no fleet identity at all, so the claim fell to the legacy
// path and the standing dedication guard refused the flagship to "an undeclared
// operation" — the dedication created to reserve the frigate FOR buying was the very
// thing that blocked it from buying. The purchase op must declare the identity the
// dedication names, so the exclusive purchaser can actually purchase.
//
// This is not a relaxation of the ownership model (RULINGS #7): the hull is claimed
// by the fleet it is dedicated to, which is precisely what dedication grants.
func TestPurchaseContainerClaimsThePurchasingDedicatedCommandFrigate(t *testing.T) {
	for _, launcher := range purchaseLaunchers {
		t.Run(launcher.name, func(t *testing.T) {
			s, db, playerID := newRecoveryTestServer(t)

			frigate := newPurchasingFrigate(t, "TORWIND-1", playerID)
			repo := &purchaseClaimShipRepo{ships: map[string]*navigation.Ship{"TORWIND-1": frigate}}
			s.shipRepo = repo

			containerID, err := launcher.launch(s, "TORWIND-1", playerID)
			require.NoError(t, err)
			if r := s.registeredRunner(containerID); r != nil {
				defer r.cancelFunc()
			}

			// The exclusive purchaser must actually be holding the buy.
			requireContainerState(t, db, containerID, "RUNNING", "")
			require.True(t, frigate.IsAssigned(), "the purchasing-dedicated frigate must be claimable by the purchase it exists to run")
			require.Equal(t, containerID, frigate.ContainerID())

			// It must be claimed BY NAME as the purchasing fleet — the identity the
			// dedication names — not smuggled through as an undeclared operation.
			claims := repo.recordedClaims()
			require.Len(t, claims, 1, "the purchase must claim through the atomic operation-checked ClaimShip")
			require.Equal(t, navigation.PurchasingFleet, claims[0].operation,
				"a purchase must declare the purchasing fleet identity")
			require.Equal(t, containerID, claims[0].containerID)
		})
	}
}

// The ownership model must not be widened while fixing the above (RULINGS #7:
// "Pinned/dedicated hulls are never poached ... Do not code around fleet pins").
// A purchase op declares the PURCHASING fleet and nothing else, so a hull pinned to
// a different operation — here a contract hauler — is still refused, the container
// still terminalizes on the standing rejection, and the pinned hull is left
// untouched. This is what fails if the identity is ever widened into a wildcard, or
// derived from the hull's own tag (which would let a purchase claim anything).
func TestPurchaseContainerStillRefusedAHullPinnedToAnotherFleet(t *testing.T) {
	for _, launcher := range purchaseLaunchers {
		t.Run(launcher.name, func(t *testing.T) {
			s, db, playerID := newRecoveryTestServer(t)

			hauler := newIdleTradeShip(t, "TORWIND-7", playerID)
			hauler.SetDedicatedFleet("contract")
			repo := &purchaseClaimShipRepo{ships: map[string]*navigation.Ship{"TORWIND-7": hauler}}
			s.shipRepo = repo

			containerID, err := launcher.launch(s, "TORWIND-7", playerID)
			require.NoError(t, err)
			if r := s.registeredRunner(containerID); r != nil {
				defer r.cancelFunc()
			}

			requireContainerState(t, db, containerID, "FAILED", "claim_failed")
			require.False(t, hauler.IsAssigned(), "a contract-pinned hull must never be poached to run a purchase")
			require.Empty(t, repo.recordedClaims(), "the rejected claim must leave no assignment behind")
		})
	}
}

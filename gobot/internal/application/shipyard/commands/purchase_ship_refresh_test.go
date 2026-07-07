package commands

import (
	"context"
	"errors"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// purchaseStubShipRepo embeds the domain interface so only the method the
// post-purchase refresh exercises needs a concrete implementation; any other
// call nil-panics, proving the refresh path touches nothing but SyncShipFromAPI.
type purchaseStubShipRepo struct {
	navigation.ShipRepository

	syncedSymbol string
	syncCalled   int
	syncErr      error
}

func (s *purchaseStubShipRepo) SyncShipFromAPI(_ context.Context, symbol string, _ shared.PlayerID) (*navigation.Ship, error) {
	s.syncCalled++
	s.syncedSymbol = symbol
	if s.syncErr != nil {
		return nil, s.syncErr
	}
	return nil, nil
}

// A freshly purchased ship can land in the cache with an EMPTY Role (invisible
// to role-based coordinators) or phantom cargo/nav state. The daemon must force
// exactly one authoritative GET /my/ships for the new ship the moment it is
// persisted, so the cache never lingers desynced (cluster lesson L50).
func TestPurchaseShip_RefreshesNewShipStateAfterPurchase(t *testing.T) {
	shipRepo := &purchaseStubShipRepo{}
	handler := &PurchaseShipHandler{shipRepo: shipRepo}

	handler.refreshPurchasedShip(context.Background(), "TORWIND-4", shared.MustNewPlayerID(1))

	if shipRepo.syncCalled != 1 {
		t.Fatalf("expected exactly one post-purchase ship refresh, got %d", shipRepo.syncCalled)
	}
	if shipRepo.syncedSymbol != "TORWIND-4" {
		t.Fatalf("expected refresh of the newly purchased ship TORWIND-4, got %q", shipRepo.syncedSymbol)
	}
}

// The ship is already bought and persisted before the refresh, so a refresh
// failure must never fail the purchase — it is best-effort and self-heals on
// the next pool sync. The helper swallows the error (returns void).
func TestPurchaseShip_RefreshFailureIsSwallowed(t *testing.T) {
	shipRepo := &purchaseStubShipRepo{syncErr: errors.New("api 503 service unavailable")}
	handler := &PurchaseShipHandler{shipRepo: shipRepo}

	handler.refreshPurchasedShip(context.Background(), "TORWIND-4", shared.MustNewPlayerID(1))

	if shipRepo.syncCalled != 1 {
		t.Fatalf("expected one refresh attempt even on error, got %d", shipRepo.syncCalled)
	}
}

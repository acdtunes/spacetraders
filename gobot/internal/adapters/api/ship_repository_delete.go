package api

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// DeleteShip removes one hull's row after the game has destroyed it, so the fleet
// view stops reporting a hull that no longer exists.
//
// Unlike the sync reconcile this takes no live fleet read as its warrant, so it is
// scoped to the exact (ship_symbol, player_id) the caller names and nothing wider.
func (r *ShipRepository) DeleteShip(ctx context.Context, symbol string, playerID shared.PlayerID) error {
	if symbol == "" {
		return fmt.Errorf("DeleteShip requires a ship symbol")
	}

	err := r.db.WithContext(ctx).
		Where("ship_symbol = ? AND player_id = ?", symbol, playerID.Value()).
		Delete(&persistence.ShipModel{}).Error
	if err != nil {
		return fmt.Errorf("failed to delete ship %s: %w", symbol, err)
	}

	r.shipListCache.Delete(playerID.Value())
	return nil
}

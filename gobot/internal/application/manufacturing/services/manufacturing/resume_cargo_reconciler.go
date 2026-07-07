package manufacturing

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// reconcileResumeCargo guards the idempotent "already have cargo, resume mid-task"
// branch shared by every manufacturing executor (COLLECT_SELL, ACQUIRE_DELIVER,
// STORAGE_ACQUIRE_DELIVER, DELIVER_TO_CONSTRUCTION) against a PHANTOM cached hold.
//
// A cached non-empty hold can be a cache/server desync (cluster lesson L47): the
// server hold is actually empty because a prior supply/delivery/contract path
// removed the cargo without decrementing the local cache. Trusting it routes the
// task into a sell/deliver resume branch it can never complete - the ship flies
// empty and the server rejects the sell/supply with a 4219 "cargo does not contain
// N units". This is the same cache/server desync family as the ff22324 negotiate
// fix and the sp-os5s foreign-cargo/post-purchase auto-heals.
//
// When the cached ship shows the task good present, this forces a GET /my/ships to
// reconcile the ship BEFORE the executor chooses its phase, logging "cache refreshed,
// cargo N/M" so the resume decision is auditable in the container log stream (the
// message string carries the counts because the log renderer drops structured map
// fields). A cache that already reads empty needs no round-trip and is returned
// unchanged. Best-effort by design: a refresh failure falls back to the cached ship
// so a transient API hiccup never strands a genuinely loaded ship.
func reconcileResumeCargo(
	ctx context.Context,
	navigator Navigator,
	cachedShip *navigation.Ship,
	good string,
	shipSymbol string,
	playerID shared.PlayerID,
) *navigation.Ship {
	// Only a cache that claims cargo can trigger a phantom resume; an already-empty
	// cache will take the acquire branch anyway, so skip the server round-trip.
	if !cachedShip.Cargo().HasItem(good, 1) {
		return cachedShip
	}

	logger := common.LoggerFromContext(ctx)

	fresh, err := navigator.ReloadShipFromAPI(ctx, shipSymbol, playerID)
	if err != nil {
		logger.Log("WARN", "Resume cargo refresh failed; trusting cache", map[string]interface{}{
			"ship":    shipSymbol,
			"good":    good,
			"trigger": "resume_cargo_phantom",
			"error":   err.Error(),
		})
		return cachedShip
	}

	logger.Log("INFO", fmt.Sprintf("Resume cache refreshed, cargo %d/%d", fresh.CargoUnits(), fresh.CargoCapacity()), map[string]interface{}{
		"ship":         shipSymbol,
		"good":         good,
		"trigger":      "resume_cargo_phantom",
		"cached_units": cachedShip.Cargo().GetItemUnits(good),
		"api_units":    fresh.Cargo().GetItemUnits(good),
	})
	return fresh
}

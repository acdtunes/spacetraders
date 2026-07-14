package contract

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/depot"
)

// DepotRegistryProvider is the narrow driven port the contract coordinator consults
// each pass to obtain the LIVE contract-depot routing registry (bead sp-u9xa, the
// final seam). The durable store owns no in-memory authority, so every call re-derives
// the registry from the persisted rows — which is what makes a `depot add|remove` on a
// running daemon honored on the next pass with no restart, exactly as the boot-time
// rebuild sees it. The daemon server satisfies this via its existing per-player
// LoadDepotRegistry method (mirroring how it already satisfies IdleArbLauncher).
type DepotRegistryProvider interface {
	LoadDepotRegistry(ctx context.Context, playerID int) (*depot.Registry, error)
}

// ResolveDepotRegistry loads the live depot routing registry for a player, fail-safe
// for the dominant-income contract engine. It mirrors ResolveStandbyStations' optional-
// port idiom: a nil provider (feature unwired) resolves to nil, and any durable-store read
// error resolves to nil after a WARNING — never propagating the failure into the
// coordinator loop. In both cases routeContractViaDepot(nil, ...) degrades to the
// pre-existing default long-haul path, so an empty/unavailable registry == today's
// behavior (the natural off-switch, no config flag).
func ResolveDepotRegistry(
	ctx context.Context,
	logger common.ContainerLogger,
	provider DepotRegistryProvider,
	playerID int,
) *depot.Registry {
	if provider == nil {
		return nil
	}
	reg, err := provider.LoadDepotRegistry(ctx, playerID)
	if err != nil {
		if logger != nil {
			logger.Log("WARNING", fmt.Sprintf(
				"failed to load live contract-depot registry for player %d (falling back to default long-haul contract routing): %v",
				playerID, err), nil)
		}
		return nil
	}
	return reg
}

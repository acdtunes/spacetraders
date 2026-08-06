package expansion

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/hullbuy"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// explorerDedicatedFleet is the tag dedicate-at-purchase stamps on a bought explorer. Read from the
// SHARED buy policy rather than copied as a literal: a drift between the two would make this finder
// silently see no explorer, which fails safe (no warp) but would present as a fleet that bought a
// 769k hull and then never used it.
var explorerDedicatedFleet = hullbuy.DedicatedFleet(hullbuy.HullClassExplorer)

// idle_explorer_port.go finds the bought+dedicated explorer waiting to be warped.
//
// It is the retired frontier coordinator's idleDedicatedExplorer, lifted out of that handler
// verbatim in behaviour so the live sensing driver can ask the same question without depending on a
// coordinator that refuses to start. Both predicates it applies are load-bearing:
//
//   - IDLE ONLY. A hull in transit is not idle, and that is the whole of the dispatch's
//     idempotence: an explorer mid-warp is invisible here, so no tick can launch a second warp on
//     top of the one already flying. It needs no cross-tick state to achieve that (RULINGS #2) —
//     the ships table is the state.
//   - WARP DRIVE REQUIRED. A mis-tagged hull without one would be handed to the warp executor and
//     refused (ErrShipHasNoWarpDrive) after the dispatch had already been decided. Checking here
//     means the wrong hull is never selected in the first place.

// idleShipReader lists a player's idle hulls. navigation.ShipRepository satisfies it.
type idleShipReader interface {
	FindIdleByPlayer(ctx context.Context, playerID shared.PlayerID) ([]*navigation.Ship, error)
}

// IdleExplorerPort reports the idle, warp-capable, explorer-dedicated hull the off-gate dispatch
// should warp.
type IdleExplorerPort struct{ ships idleShipReader }

// NewIdleExplorerPort wires the finder over the ship repository.
func NewIdleExplorerPort(ships idleShipReader) *IdleExplorerPort {
	return &IdleExplorerPort{ships: ships}
}

// IdleExplorer returns the first idle explorer-dedicated hull carrying a warp drive.
//
// found=false is the ORDINARY state, not an error: it is what the fleet looks like before the
// autosizer has bought an explorer, and while the one we own is mid-warp. A read failure is
// propagated rather than reported as "none", because the caller must not read a database hiccup as
// a reason to do nothing quietly — the tag is what tells an explorer apart from every other hull,
// and guessing at it is how a fleet warps the wrong thing.
func (p *IdleExplorerPort) IdleExplorer(ctx context.Context, playerID int) (string, bool, error) {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return "", false, err
	}
	ships, err := p.ships.FindIdleByPlayer(ctx, pid)
	if err != nil {
		return "", false, fmt.Errorf("failed to list idle hulls looking for an explorer: %w", err)
	}
	for _, ship := range ships {
		if ship == nil {
			continue
		}
		if ship.DedicatedFleet() == explorerDedicatedFleet && ship.HasWarpDrive() {
			return ship.ShipSymbol(), true, nil
		}
	}
	return "", false, nil
}

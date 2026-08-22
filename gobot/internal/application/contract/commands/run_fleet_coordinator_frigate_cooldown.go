package commands

import (
	"time"

	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// commandFrigateLastResortSettleWindow holds the undedicated command hull back from redraft
// after a leg it was drafted for only because no regular hauler existed: last-resort
// admission has no memory of prior drafts, so without a pause it can be reclaimed the same
// tick, indefinitely. Sized to outlast one bootstrap reconcile tick. Scoped to a fresh
// general-pool draft only (generalPoolCommandDraft) — holders, resumes, dedicated hulls and
// regular haulers are unaffected.
const commandFrigateLastResortSettleWindow = 90 * time.Second

// generalPoolCommandDraft reports whether selectedShip is the undedicated command hull
// discovered in this pass's general pool — a depot-routed hull or cargo holder is never a
// member of it, so this alone scopes the settle window to that one case.
func generalPoolCommandDraft(entities []*navigation.Ship, selectedShip string) bool {
	for _, ship := range entities {
		if ship.ShipSymbol() != selectedShip {
			continue
		}
		return domainContract.IsCommandHull(ship) && ship.DedicatedFleet() == ""
	}
	return false
}

// commandFrigateCooldownRemaining reports how much of the settle window is left for
// shipSymbol, or 0 when clear to redraft. cooldowns is in-memory for this coordinator's
// lifetime only, like the spawn governor and liquidation cooldown elsewhere in this package.
func commandFrigateCooldownRemaining(cooldowns map[string]time.Time, shipSymbol string, now time.Time) time.Duration {
	until, ok := cooldowns[shipSymbol]
	if !ok {
		return 0
	}
	remaining := until.Sub(now)
	if remaining <= 0 {
		return 0
	}
	return remaining
}

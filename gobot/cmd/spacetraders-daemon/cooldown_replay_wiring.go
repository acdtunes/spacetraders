package main

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/trading/cooldownreplay"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	domainTrading "github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// cooldownReplayWindow bounds the boot replay. Debt decays as exp(-dt/tau), so past ~3 tau a row
// contributes under 5% of what it accrued — replaying further back costs query time to restore
// nothing. Derived from the configured tau rather than pinned, so a refit moves both together.
func cooldownReplayWindow(tau time.Duration) time.Duration { return 3 * tau }

// replayLaneCooldown restores the shared compression ledger's source-drain memory at boot and
// reports how many purchase rows it applied.
//
// IT CANNOT FAIL THE BOOT, AND THAT IS THE POINT. A guard's repair path must never be the reason
// the daemon does not start: every outcome here — no player configured, an unreadable history, a
// market it cannot size — logs and returns, leaving the ledger exactly as empty as it was before
// this existed.
//
// THE PLAYER ID IS OPTIONAL, NOT DEFERRED. cfg.Captain.PlayerID is a config value that is never
// assigned at runtime, so it is zero for the whole process whenever no captain player is
// configured — there is no later point in boot at which it becomes positive. It is read through
// the error-returning constructor for that reason; the Must- form turns an ordinary unconfigured
// deployment into a panic before the daemon ever listens. The sibling boot paths treat the same
// value the same way (the deploy signal guards on the player existing, the watchkeeper checks it
// against zero).
func replayLaneCooldown(
	ctx context.Context,
	ledger *domainTrading.LaneCooldownLedger,
	history cooldownreplay.PurchaseHistory,
	markets cooldownreplay.MarketDepth,
	configuredPlayerID int,
	tau time.Duration,
	now time.Time,
) int {
	playerID, err := shared.NewPlayerID(configuredPlayerID)
	if err != nil {
		fmt.Printf("Lane cooldown replay: no player configured, starting with no compression memory\n")
		return 0
	}
	replayed, rerr := cooldownreplay.Rebuild(ctx, ledger, history, markets, playerID, cooldownReplayWindow(tau), now)
	if rerr != nil {
		fmt.Printf("Lane cooldown replay failed, starting with no compression memory: %v\n", rerr)
		return 0
	}
	if replayed > 0 {
		fmt.Printf("Lane cooldown replay: restored %d recent purchase(s) of source drain\n", replayed)
	}
	return replayed
}

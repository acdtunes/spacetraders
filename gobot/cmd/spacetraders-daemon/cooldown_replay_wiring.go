package main

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/trading/cooldownreplay"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
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
// THE PLAYER ID IS OPTIONAL, NOT DEFERRED, and that is why it is resolved rather than asserted.
// cfg.Captain.PlayerID is a config value never assigned at runtime, so it is zero for the whole
// process on any deployment without a captain player — there is no later point in boot at which it
// becomes positive, and the Must- form turns that ordinary case into a panic before the daemon
// listens. Reading it through the error-returning constructor stops the panic but would leave the
// replay INERT exactly where it is needed, so the stored players are the fallback.
func replayLaneCooldown(
	ctx context.Context,
	ledger *domainTrading.LaneCooldownLedger,
	history cooldownreplay.PurchaseHistory,
	markets cooldownreplay.MarketDepth,
	players PlayerDirectory,
	configuredPlayerID int,
	tau time.Duration,
	now time.Time,
) int {
	playerID, ok := resolveReplayPlayer(ctx, configuredPlayerID, players)
	if !ok {
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

// PlayerDirectory lists the players this daemon holds state for. *persistence.GormPlayerRepository
// satisfies it.
type PlayerDirectory interface {
	ListAll(ctx context.Context) ([]*player.Player, error)
}

// resolveReplayPlayer decides whose drain history to replay: the configured captain player when one
// is set, otherwise the single stored player.
//
// The fallback exists because the configured id is absent on any captain-less deployment, and a
// replay that silently does nothing there is not a repair — it is the amnesia with a log line.
//
// It refuses to guess. The compression ledger's key carries no player dimension, so replaying two
// players into it would blend their drains into one market's history; more than one stored player
// therefore skips rather than picks. Zero players, or a directory that cannot be read, skip too.
// Every one of those is a log and a return, never a boot failure.
func resolveReplayPlayer(ctx context.Context, configuredPlayerID int, players PlayerDirectory) (shared.PlayerID, bool) {
	if playerID, err := shared.NewPlayerID(configuredPlayerID); err == nil {
		return playerID, true
	}
	if players == nil {
		fmt.Printf("Lane cooldown replay: no player configured and no player directory wired - starting with no compression memory\n")
		return shared.PlayerID{}, false
	}
	stored, err := players.ListAll(ctx)
	if err != nil {
		fmt.Printf("Lane cooldown replay: could not read the stored players - starting with no compression memory: %v\n", err)
		return shared.PlayerID{}, false
	}
	if len(stored) != 1 || stored[0] == nil {
		fmt.Printf("Lane cooldown replay: no player configured and %d stored - cannot tell whose drain to replay, starting with no compression memory\n", len(stored))
		return shared.PlayerID{}, false
	}
	return stored[0].ID, true
}

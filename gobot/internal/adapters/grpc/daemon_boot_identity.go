package grpc

// daemon_boot_identity.go re-asserts the player's durable agent identity into players.metadata at
// every daemon boot (sp-0eufi).
//
// THE OUTAGE THIS ENDS: players.metadata.headquarters had three readers and no reachable writer.
// The only code that set it — SyncPlayerHandler — was never constructed or dispatched anywhere,
// so every players row carried {"starting_faction": ...} and nothing else. The parked-sensing
// HomeSystemPort read the absent key and failed; because the sensing CUTOVER runs BEFORE
// expansion in the tick, the whole reconcile aborted. Screen, reaper, adoption, drain, placements
// and expansion were all dead, every 30 seconds, with an empty sensing_systems table and not one
// probe ever placed. The legacy frontier coordinator failed on the same dependency.
//
// WHY BOOT, AND WHY HERE: registration seeds the key going forward, but a row created by any
// earlier path — or by a registration route that never learned the value — would stay broken
// forever with no way back except a hand-written UPDATE. Re-asserting at boot makes the key
// self-healing: whatever created the row, the daemon that runs the fleet repairs it from the
// player's OWN /my/agent, which is the only authoritative source (headquarters differs per agent
// and must never be guessed). It is idempotent, so a correct row is not rewritten.

import (
	"context"
	"fmt"
	"time"

	playerCmd "github.com/andrescamacho/spacetraders-go/internal/application/player/commands"
)

// bootIdentitySyncTimeout caps the single /my/agent read this repair makes. Short on purpose: the
// call is one cheap request against a healthy API, and every second beyond that is a second the
// daemon spends not running the fleet. Exceeding it is not an error worth failing boot over — the
// row is simply repaired on the next boot.
const bootIdentitySyncTimeout = 5 * time.Second

// syncAgentIdentityAtBoot repairs/refreshes players.metadata for one player from /my/agent.
//
// Dispatched through the mediator rather than calling the repository directly, for two reasons:
// PlayerTokenMiddleware resolves the player's token into the context (the handler needs it, and
// this is the same path every other command uses), and it means the identity merge has exactly
// ONE implementation instead of a boot-time copy that could drift from the command's.
//
// FAIL-OPEN, deliberately, and the asymmetry is the point: this is a repair, not a spender. A
// daemon that cannot reach /my/agent at boot must still start and run the fleet — refusing to
// boot over a metadata refresh would convert a degraded frontier into a total outage. The write
// itself is fail-CLOSED inside the handler (an unreadable agent writes nothing rather than
// clobbering a good row), so nothing is corrupted by the failure; the next boot retries.
//
// The warning is loud and names the consequence, because a silent failure here is what the
// missing key looked like for as long as it went unnoticed.
func (s *DaemonServer) syncAgentIdentityAtBoot(ctx context.Context, playerID int) {
	if playerID <= 0 {
		return // no player yet (fresh install, pre-registration) — nothing to sync
	}

	// Its OWN short budget, not the boot phase's. This is one live /my/agent call on a path that
	// also launches every standing coordinator, and an unbounded read here would let a degraded API
	// hold the boot sequence open for the whole shared 30s window. A repair must never be able to
	// cost more than the thing it repairs.
	syncCtx, cancel := context.WithTimeout(ctx, bootIdentitySyncTimeout)
	defer cancel()

	resp, err := s.mediator.Send(syncCtx, &playerCmd.SyncPlayerCommand{PlayerID: playerID})
	if err != nil {
		fmt.Printf("Warning: failed to sync agent identity for player %d: %v\n"+
			"  players.metadata.headquarters may be missing; parked-sensing cutover and frontier "+
			"expansion refuse without it. Retried on next daemon boot.\n", playerID, err)
		return
	}

	// Only announce a real change: a correct row is the steady state and must not add a line to
	// every boot log.
	if synced, ok := resp.(*playerCmd.SyncPlayerResponse); ok && synced != nil && synced.Updated {
		hq := "(unset)"
		if synced.Player != nil {
			if v, found := synced.Player.Metadata["headquarters"].(string); found && v != "" {
				hq = v
			}
		}
		fmt.Printf("Synced agent identity for player %d from /my/agent (headquarters=%s)\n", playerID, hq)
	}
}

// Package cooldownreplay rebuilds the lane-compression ledger's source-drain memory at boot.
//
// The ledger is in-memory, so a restart forgets how much the fleet has recently taken out of each
// market. That is harmless for a ranker — a mis-ranked lane self-corrects — and not harmless for a
// spend guard, which forgets in the permissive direction and lets a just-drained source be drained
// again. RULINGS #2: operational state, cooldown clocks named explicitly, survives a restart.
//
// It reconstructs rather than persists, because the durable record already exists: every purchase
// wrote a transaction row carrying the market, the good, the units and the time. Nothing new is
// stored, so there is no second copy of the truth to drift.
package cooldownreplay

import (
	"context"
	"sort"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// PurchaseHistory reads recorded purchases. *persistence.TransactionRepositoryGORM satisfies it.
type PurchaseHistory interface {
	FindByPlayer(ctx context.Context, playerID shared.PlayerID, opts ledger.QueryOptions) ([]*ledger.Transaction, error)
}

// MarketDepth reads a market's current listing, for its per-transaction trade volume.
type MarketDepth interface {
	GetMarketData(ctx context.Context, waypoint string, playerID int) (*market.Market, error)
}

// replayedOperation is the operation tag whose purchases the pacing consult accrues from. Replaying
// a wider set would rebuild a debt the running fleet never accrues, so the replay would disagree
// with live behaviour from the first tick.
const replayedOperation = "construction_supply"

// Rebuild replays recent purchases into the ledger's source-drain keys and reports how many rows it
// applied. Errors reading the history are returned; a market whose depth cannot be read is skipped,
// never guessed.
//
// TRADE VOLUME IS THE ONE THING THE ROWS DO NOT CARRY, and the market table keeps no history — it
// holds one row per market and good, overwritten. Debt is units/tradeVolume, so a replay using
// today's volume mis-scales every row, and where the volume has FALLEN since it would compute debt
// larger than what really accrued — arming the guard harder than reality.
//
// The rows bound it themselves. One purchase row is one API call, and a call cannot exceed the
// market's per-transaction volume at that moment, so the largest single purchase in the window is a
// LOWER BOUND on the volume that was in force. Taking the larger of that and today's volume can only
// OVERSTATE the volume, which can only UNDERSTATE the debt. The replay therefore fails in the same
// permissive direction as the amnesia it repairs, and never in the direction that over-arms.
//
// IT NEVER ADDS TO A KEY THAT ALREADY CARRIES DEBT. That makes it idempotent and structurally unable
// to double-count a purchase that also accrued in memory — the failure that would arrive through the
// fix rather than the bug. Ordering it before the coordinators start is still right; this is what
// makes the correctness independent of that ordering.
func Rebuild(
	ctx context.Context,
	cooldown *trading.LaneCooldownLedger,
	history PurchaseHistory,
	markets MarketDepth,
	playerID shared.PlayerID,
	window time.Duration,
	now time.Time,
) (int, error) {
	if cooldown == nil || history == nil || markets == nil || window <= 0 {
		return 0, nil
	}
	since := now.Add(-window)
	purchaseType := ledger.TransactionType("PURCHASE_CARGO")
	rows, err := history.FindByPlayer(ctx, playerID, ledger.QueryOptions{
		StartDate:       &since,
		TransactionType: &purchaseType,
	})
	if err != nil {
		return 0, err
	}

	type drain struct {
		waypoint, good string
		units          int
		at             time.Time
	}
	drains := make([]drain, 0, len(rows))
	// The largest single call seen per market and good — the lower bound on the volume in force.
	observedFloor := make(map[trading.LaneKey]int)
	for _, row := range rows {
		if row == nil || row.OperationType() != replayedOperation {
			continue
		}
		waypoint, good, units, ok := purchaseFields(row)
		if !ok {
			continue
		}
		key := trading.SourceDrainKey(waypoint, good)
		if units > observedFloor[key] {
			observedFloor[key] = units
		}
		drains = append(drains, drain{waypoint: waypoint, good: good, units: units, at: row.Timestamp()})
	}

	// Replay oldest first so each Accrue decays the debt standing before it, exactly as the live
	// sequence did. A window's worth of rows is small and the repository's order is not guaranteed.
	sort.SliceStable(drains, func(i, j int) bool { return drains[i].at.Before(drains[j].at) })

	volumes := make(map[trading.LaneKey]int)
	skipped := make(map[trading.LaneKey]bool)
	applied := 0
	for _, d := range drains {
		key := trading.SourceDrainKey(d.waypoint, d.good)
		if skipped[key] {
			continue
		}
		volume, resolved := volumes[key]
		if !resolved {
			// Refuse a key the running fleet has already touched: this replay is a boot-time
			// reconstruction, not a correction, and adding to live debt would inflate it.
			if cooldown.Debt(key, now) > 0 {
				skipped[key] = true
				continue
			}
			volume = resolveVolume(ctx, markets, d.waypoint, d.good, playerID, observedFloor[key])
			if volume <= 0 {
				skipped[key] = true
				continue
			}
			volumes[key] = volume
		}
		cooldown.Accrue(key, d.units, volume, d.at)
		applied++
	}
	return applied, nil
}

// purchaseFields pulls the market, good and units off a purchase row's metadata. A row missing any
// of them is not a drain this replay can size, so it is skipped rather than guessed at.
func purchaseFields(row *ledger.Transaction) (waypoint, good string, units int, ok bool) {
	meta := row.Metadata()
	if meta == nil {
		return "", "", 0, false
	}
	waypoint, _ = meta["waypoint"].(string)
	good, _ = meta["good_symbol"].(string)
	units = intFrom(meta["units"])
	if waypoint == "" || good == "" || units <= 0 {
		return "", "", 0, false
	}
	return waypoint, good, units, true
}

// intFrom reads a metadata number, which round-trips through JSON as a float64 but arrives as an int
// from an in-process writer.
func intFrom(raw interface{}) int {
	switch v := raw.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return 0
}

// resolveVolume is the larger of the market's current per-transaction volume and the largest single
// purchase observed in the window — see Rebuild for why overstating it is the safe direction.
func resolveVolume(ctx context.Context, markets MarketDepth, waypoint, good string, playerID shared.PlayerID, observedFloor int) int {
	volume := observedFloor
	mkt, err := markets.GetMarketData(ctx, waypoint, playerID.Value())
	if err == nil && mkt != nil {
		if g := mkt.FindGood(good); g != nil && g.TradeVolume() > volume {
			volume = g.TradeVolume()
		}
	}
	return volume
}

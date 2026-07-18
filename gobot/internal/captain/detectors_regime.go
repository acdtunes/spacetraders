// detectors_regime.go — captain-declared price-tripwire regime detector (sp-zlfv):
// good-class matching, threshold resolution, and detectRegimeShift. Split out of
// detectors.go for navigability; behavior unchanged.
package watchkeeper

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

// gasSymbols is the fixed set of SpaceTraders goods the captain's "GAS"
// price class covers (extracted via gas-siphon operations, not mining).
// There is no exported domain classification to reuse — internal/domain/goods
// keeps no ore/gas grouping — so sp-zlfv defines its own minimal, local
// classifier rather than reach into an unrelated package for three strings.
var gasSymbols = map[string]bool{
	"HYDROCARBON":     true,
	"LIQUID_HYDROGEN": true,
	"LIQUID_NITROGEN": true,
}

// matchesGoodClass reports whether goodSymbol belongs to a tripwire's
// configured good scope: the class keyword "ORE" (any *_ORE symbol), the
// class keyword "GAS" (gasSymbols), or a literal comma-separated symbol
// allowlist (exact match, case-insensitive) for anything else.
func matchesGoodClass(goodSymbol, class string) bool {
	switch strings.ToUpper(strings.TrimSpace(class)) {
	case "ORE":
		return strings.HasSuffix(goodSymbol, "_ORE")
	case "GAS":
		return gasSymbols[goodSymbol]
	default:
		for _, sym := range strings.Split(class, ",") {
			if strings.EqualFold(strings.TrimSpace(sym), goodSymbol) {
				return true
			}
		}
		return false
	}
}

// resolveRegimeThreshold returns the effective price threshold to compare
// against, plus the baseline it was derived from. Absolute mode (Threshold
// set) needs no lookup: baseline reports as 0. Multiplier mode looks up the
// OLDEST recorded price-history sample within tw.Window as the baseline and
// scales it; ok=false when a multiplier tripwire has no baseline recorded
// yet within the window (nothing to compare the current price against).
func resolveRegimeThreshold(ctx context.Context, db *gorm.DB, playerID int, waypoint, good string, tw RegimeTripwire, now time.Time) (threshold int, baseline int, ok bool, err error) {
	if tw.Threshold != nil {
		return *tw.Threshold, 0, true, nil
	}
	if tw.Multiplier == nil {
		return 0, 0, false, nil
	}
	var oldest persistence.MarketPriceHistoryModel
	err = db.WithContext(ctx).
		Where("player_id = ? AND waypoint_symbol = ? AND good_symbol = ? AND recorded_at >= ?",
			playerID, waypoint, good, now.Add(-tw.Window)).
		Order("recorded_at ASC").
		Limit(1).
		Find(&oldest).Error
	if err != nil {
		return 0, 0, false, err
	}
	if oldest.WaypointSymbol == "" {
		return 0, 0, false, nil
	}
	baseline = oldest.SellPrice
	return int(*tw.Multiplier * float64(baseline)), baseline, true, nil
}

// regimeDedupKey scopes the edge-trigger cooldown to (good, market,
// direction): the natural identity of a single crossing, not the tripwire
// config that detected it. Two tripwires that happen to overlap on the same
// good+market+direction are a degenerate config the captain would not
// realistically declare (there is no reason to set two tripwires for the
// same good, same direction, different thresholds instead of just one).
func regimeDedupKey(good, waypoint, direction string) string {
	return good + "@" + waypoint + ":" + direction
}

// detectRegimeShift scans MarketData for prices crossing a captain-declared
// tripwire (sp-zlfv): mechanizes the per-wake price sweep the captain used to
// hand-roll ("any ore bid >=200 or gas bid >=150 (~3x baseline) triggers an
// immediate extraction re-consult"). Tripwires are ONE-SHOT (sp-a6e0): the
// supervisor CONSUMES a fired tripwire from the persisted RegimePolicy on the
// delivered wake, so a crossing cannot recur without the captain re-declaring —
// there is no Window-based re-fire cooldown here. This scan only avoids piling
// a DUPLICATE while an identical crossing is still awaiting delivery, via the
// HasUnprocessed idiom detectCreditsCrossing uses. Window's sole surviving role
// is the multiplier-mode baseline lookback (resolveRegimeThreshold). No
// tripwires configured means no query at all (zero overhead when unset).
func detectRegimeShift(ctx context.Context, db *gorm.DB, store captain.EventStore, cfg DetectorConfig, now time.Time) error {
	if len(cfg.RegimeTripwires) == 0 {
		return nil
	}
	var markets []persistence.MarketData
	if err := db.WithContext(ctx).Where("player_id = ?", cfg.PlayerID).Find(&markets).Error; err != nil {
		return err
	}
	for _, tw := range cfg.RegimeTripwires {
		for _, m := range markets {
			if !matchesGoodClass(m.GoodSymbol, tw.Good) {
				continue
			}
			threshold, baseline, ok, err := resolveRegimeThreshold(ctx, db, cfg.PlayerID, m.WaypointSymbol, m.GoodSymbol, tw, now)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			price := m.SellPrice
			var crossed bool
			switch tw.Direction {
			case "bid-above":
				crossed = price >= threshold
			case "bid-below":
				crossed = price <= threshold
			}
			if !crossed {
				continue
			}
			key := regimeDedupKey(m.GoodSymbol, m.WaypointSymbol, tw.Direction)
			// One-shot (sp-a6e0): suppress only a DUPLICATE while an identical
			// crossing is still unprocessed (awaiting delivery). No Window cooldown —
			// re-firing is prevented by the supervisor consuming the tripwire.
			dup, err := store.HasUnprocessed(ctx, cfg.PlayerID, captain.EventMarketRegimeShift, key)
			if err != nil {
				return err
			}
			if dup {
				continue
			}
			// direction is recorded so the supervisor can map this fired crossing
			// back to the tripwire that produced it when consuming the one-shot.
			_ = store.Record(ctx, &captain.Event{
				Type: captain.EventMarketRegimeShift, Ship: key, PlayerID: cfg.PlayerID,
				Payload: fmt.Sprintf(`{"good":%q,"market":%q,"direction":%q,"price":%d,"baseline":%d,"threshold":%d}`,
					m.GoodSymbol, m.WaypointSymbol, tw.Direction, price, baseline, threshold),
			})
		}
	}
	return nil
}

package cargo

import (
	"context"
	"fmt"
	"hash/fnv"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	shipPkg "github.com/andrescamacho/spacetraders-go/internal/application/ship"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// MarketRefresher defines the interface for refreshing market data after transactions.
// This interface allows the CargoTransactionHandler to refresh prices without
// creating import cycles with scouting/commands.
type MarketRefresher interface {
	ScanAndSaveMarket(ctx context.Context, playerID uint, waypointSymbol string) error
}

// refreshMarketData triggers the deliberate post-trade market scan (the "after" half
// of the scan→buy→scan impact pair the sp-tl68 model is fitted from). It is non-blocking
// — errors are logged but never fail the transaction.
//
// OPTIMIZATION: Skip refresh when called from manufacturing operations (context flag).
// Manufacturing scans markets separately and doesn't need immediate refresh after each sell.
//
// This scan is the top API consumer, so under a trade coordinator's ScanPolicy
// it is SAMPLED — the full paired scan fires on only a config fraction of trades (enough
// to refit the model per era), and every other trade falls back to the recent-scan
// freshness gate (reuse a cache scanned within MaxScanAge, scan a stale/never-scanned
// market). A caller that stamps no policy (manufacturing, contract delivery, refuel, CLI)
// is byte-for-byte unchanged: the scan always fires.
func (h *CargoTransactionHandler) refreshMarketData(ctx context.Context, cmd *CargoTransactionCommand, waypointSymbol string) {
	// Skip if no market refresher is configured
	if h.marketRefresher == nil {
		return
	}

	// OPTIMIZATION: Skip market refresh for manufacturing operations
	// They scan markets independently and don't need post-transaction refresh
	if shared.SkipMarketRefreshFromContext(ctx) {
		return
	}

	logger := logging.LoggerFromContext(ctx)

	// Sampling + recent-scan freshness gate: a non-sampled trade at a
	// freshly-scanned market reuses the cache instead of re-scanning.
	wanted, paired := h.impactScanWanted(ctx, cmd, waypointSymbol)
	if !wanted {
		logger.Log("DEBUG", "Post-trade impact scan sampled out (cache fresh) - skipping", map[string]interface{}{
			"action": "impact_scan_skipped", "waypoint": waypointSymbol, "ship": cmd.ShipSymbol,
		})
		return
	}

	// A SAMPLED trade's scan is the "after" half of the impact pair, so it
	// is exempt from the market-scan budget's freshness veto — arriving right after
	// the "before" observation is the whole point of it. It still draws
	// a token and still faces the value bar. The stale-cache branch is left
	// unstamped and is paced as an ordinary discretionary decision scan.
	if paired {
		ctx = shared.WithPairedScan(ctx)
	}

	err := h.marketRefresher.ScanAndSaveMarket(ctx, uint(cmd.PlayerID.Value()), waypointSymbol)
	if err != nil {
		// Log error but don't fail the transaction - market refresh is non-critical
		logger.Log("WARN", "Failed to refresh market data after transaction", map[string]interface{}{
			"waypoint": waypointSymbol,
			"error":    err.Error(),
		})
	} else {
		logger.Log("DEBUG", "Market data refreshed after transaction", map[string]interface{}{
			"waypoint": waypointSymbol,
		})
	}
}

// impactScanWanted decides whether this trade's deliberate post-trade impact scan should
// fire, under the ctx ScanPolicy:
//   - no policy stamped (every non-tour caller) → always scan;
//   - the trade is SAMPLED (per ImpactSampleRate) → scan, to record the impact pair the
//     analyst refits the model from;
//   - otherwise the recent-scan freshness gate governs: a cache scanned within MaxScanAge
//     is reused (skip the scan), a stale or never-scanned market is still scanned so the
//     next decision has fresh-enough prices.
func (h *CargoTransactionHandler) impactScanWanted(ctx context.Context, cmd *CargoTransactionCommand, waypointSymbol string) (wanted, paired bool) {
	policy, ok := shared.ScanPolicyFromContext(ctx)
	if !ok {
		// No policy stamped (manufacturing, contract delivery, refuel, CLI): the scan
		// fires, but it is not a MEASUREMENT pair — there is no sampling decision
		// behind it — so it stays budgeted like any other decision scan.
		return true, false
	}
	nonce := h.impactNonce.Add(1)
	key := fmt.Sprintf("%s|%s|%s|%d", cmd.ShipSymbol, cmd.GoodSymbol, waypointSymbol, nonce)
	if sampleImpact(key, policy.ImpactSampleRate) {
		// The sampled branch, and the ONLY paired one: this scan exists to record
		// dP/P against the cached "before" row, so its freshness is the precondition
		// rather than a reason to skip.
		return true, true
	}
	return !h.marketFreshWithin(ctx, cmd.PlayerID, waypointSymbol, policy.MaxScanAge), false
}

// marketFreshWithin reports whether the cached market at waypoint was scanned within
// maxAge (the recent-scan gate). maxAge<=0 disables the gate (never "fresh" → always
// scans), and an unreadable/missing cache is NOT fresh (scan for safety).
func (h *CargoTransactionHandler) marketFreshWithin(ctx context.Context, playerID shared.PlayerID, waypoint string, maxAge time.Duration) bool {
	if maxAge <= 0 {
		return false
	}
	mkt, err := h.marketRepo.GetMarketData(ctx, waypoint, playerID.Value())
	if err != nil {
		return false
	}
	return shipPkg.MarketFreshWithin(mkt, maxAge, time.Now())
}

// sampleImpact deterministically decides whether a trade identified by key is
// instrumented for price impact: an FNV hash of the key yields a uniform draw in
// [0,1), sampled iff draw < rate. rate<=0 never samples, rate>=1 always samples. The
// hash spreads evenly, so keys that vary only by the per-trade nonce (same ship/good/
// market) are ~rate sampled — no lane is ever permanently in or out.
func sampleImpact(key string, rate float64) bool {
	if rate <= 0 {
		return false
	}
	if rate >= 1 {
		return true
	}
	hasher := fnv.New64a()
	_, _ = hasher.Write([]byte(key))
	// FNV-1a has weak avalanche on keys sharing a long prefix (a hull re-trading one
	// lane differs only in the trailing nonce), so finalize with a splitmix64 mixer for a
	// uniform draw — otherwise sequential nonces skew the sampled fraction well off rate.
	mixed := splitmix64Finalize(hasher.Sum64())
	// Top 53 bits → a uniform double in [0,1); < rate samples this trade.
	draw := float64(mixed>>11) / float64(uint64(1)<<53)
	return draw < rate
}

// splitmix64Finalize is the splitmix64 finalizing mix — a bijective avalanche that turns
// a low-entropy 64-bit value into uniformly distributed bits, so sampleImpact's draw is
// unbiased regardless of the underlying hash's mixing quality.
func splitmix64Finalize(x uint64) uint64 {
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	x ^= x >> 31
	return x
}

package commands

// run_trade_route_coordinator_scandedup.go — the scan-dedup A/B test: lets
// staleAskAborts reuse this visit's own arrival scan instead of a second live
// GetMarket call on the same waypoint, only when a live allowlist arms the
// ship and marketscan.DedupEligible proves the reuse safe. Default OFF: every
// existing caller and test is byte-for-byte unchanged.

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/marketscan"
)

// scanDedupGuardStaleAsk labels the stale-ask guard for the saved-calls counter.
const scanDedupGuardStaleAsk = "stale_ask"

// ScanDedupAllowlist reports whether shipSymbol is armed for the scan-dedup A/B
// test: default off, live-settable with no restart. Optional; nil disables
// dedup entirely, matching marketRefresher/gateGraph's optional-port contract.
type ScanDedupAllowlist interface {
	IsArmed(ctx context.Context, playerID int, shipSymbol string) (bool, error)
}

// SetScanDedupAllowlist wires the live allowlist. Unset (nil) leaves every
// visit's bracket disabled, byte-identical to today.
func (h *RunTradeRouteCoordinatorHandler) SetScanDedupAllowlist(a ScanDedupAllowlist) {
	h.scanDedupAllowlist = a
}

// scanDedupBracket is one visit's arrival-scan reuse window: the two clock
// readings DedupEligible checks a candidate row against. The zero value
// (BeforeTravel unset) is disabled — every visit on an unarmed ship.
type scanDedupBracket struct {
	// BeforeTravel: taken before this visit's own travel() call — the proof
	// floor that a fresh row is from THIS attempt, not a stale leftover.
	BeforeTravel time.Time
	// AfterArrival: taken once travel()+dock() confirm complete — the elapsed
	// budget's reference point, so transit time is never counted against it.
	AfterArrival time.Time
}

func (b scanDedupBracket) armed() bool {
	return !b.BeforeTravel.IsZero()
}

// startScanDedupBracket checks the allowlist and, if armed, captures
// BeforeTravel. Call immediately before travel(). Any failure (nil port, port
// error, unarmed ship) returns the disabled bracket.
func (h *RunTradeRouteCoordinatorHandler) startScanDedupBracket(ctx context.Context, shipSymbol string, playerID int) scanDedupBracket {
	if h.scanDedupAllowlist == nil {
		return scanDedupBracket{}
	}
	isArmed, err := h.scanDedupAllowlist.IsArmed(ctx, playerID, shipSymbol)
	if err != nil || !isArmed {
		return scanDedupBracket{}
	}
	return scanDedupBracket{BeforeTravel: h.clock.Now()}
}

// confirmScanDedupArrival captures AfterArrival once travel()+dock() succeed.
func (h *RunTradeRouteCoordinatorHandler) confirmScanDedupArrival(b scanDedupBracket) scanDedupBracket {
	if !b.armed() {
		return b
	}
	b.AfterArrival = h.clock.Now()
	return b
}

// reuseScanDedup is the eligibility+reuse check an earning-class guard applies
// before it would otherwise spend a live scan for waypoint. Any doubt reports
// false — the caller's cue to scan live exactly as before (fail closed, never
// weakened).
func (h *RunTradeRouteCoordinatorHandler) reuseScanDedup(
	ctx context.Context,
	b scanDedupBracket,
	shipSymbol, waypoint string,
	playerID int,
	guard string,
	logger common.ContainerLogger,
) bool {
	if !b.armed() {
		return false
	}
	row, err := h.marketRepo.GetMarketData(ctx, waypoint, playerID)
	if err != nil || row == nil {
		return false
	}
	if !marketscan.DedupEligible(h.clock.Now(), b.BeforeTravel, b.AfterArrival, row.LastUpdated(), marketscan.DedupMaxElapsedSinceArrival) {
		return false
	}
	metrics.RecordScanDedupSaved(playerID, shipSymbol, guard)
	logger.Log("DEBUG", fmt.Sprintf("Scan-dedup: reusing the arrival scan of %s for the %s guard", waypoint, guard), map[string]interface{}{
		"action": "scan_dedup_reused", "waypoint": waypoint, "ship_symbol": shipSymbol, "guard": guard,
	})
	return true
}

package commands

import (
	"context"
	"fmt"
	"hash/fnv"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

const (
	// defaultDirectScanInterval is the probe market-scan cadence applied when a
	// scout_tour is launched with no ScanInterval supplied (sp-zixw) — the
	// CLI/legacy direct-launch path (workflow scout-markets and the daemon
	// ScoutTour RPC). Replaces the old hardcoded 5-minute wait, which wasted API
	// budget at 54 hulls; the captain's ask was a 15-30m cap, so direct launches
	// default to 15m. Coordinator-spawned tours instead derive their own interval
	// from the post's freshness target (deriveScanInterval,
	// run_scout_post_coordinator.go) — both paths funnel through
	// effectiveScanInterval below, so the same floor/cap always applies.
	defaultDirectScanInterval = 15 * time.Minute

	// scanIntervalFloor and scanIntervalCap bound every resolved scan interval,
	// coordinator-derived or direct (RULINGS #5: parametrized, not hardcoded at the
	// call site). Below the floor the API cost outweighs the freshness gained at
	// 54+ hulls; above the cap a post drifts stale regardless of how loose its
	// freshness target is.
	scanIntervalFloor = 5 * time.Minute
	scanIntervalCap   = 30 * time.Minute

	// defaultTourStartJitterMax is the phase-jitter ceiling applied to a scout
	// tour's start when tour_start_jitter_max_seconds is 0/absent in config.yaml
	// (sp-x8i5, RULINGS #5). ~45 scouts booting with similar scan intervals were
	// waking in near-lockstep: the burst-window log showed 1080 scout_tour + 725
	// scout_post_coordinator lines (43% of all activity) inside one 6-minute
	// window, p99 limiter wait 4s, while the 15m-average utilization read a
	// nowhere-near-saturated 53% — the average smoothed right over the spike.
	// 120s spreads 45 tour starts roughly 2.7s apart on average across the
	// ceiling: wide enough to decohere the wave, short enough next to the 5m
	// scan floor (scanIntervalFloor) that it never meaningfully delays a fresh
	// post's first scan.
	defaultTourStartJitterMax = 120 * time.Second
)

// stableJitter derives a deterministic pseudo-random duration in [0, ceiling)
// from id via FNV-1a — NOT math/rand (sp-x8i5). The offset must be the SAME on
// every build (fresh launch or restart recovery) for a given id, or a daemon
// restart would silently reshuffle every scout's phase and could re-cohere the
// very wave this fix decoheres. ceiling<=0 returns 0 (no jitter).
func stableJitter(id string, ceiling time.Duration) time.Duration {
	if ceiling <= 0 {
		return 0
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(id))
	return time.Duration(h.Sum64() % uint64(ceiling))
}

// clampScanInterval bounds d to [scanIntervalFloor, scanIntervalCap] (sp-zixw).
// Shared by effectiveScanInterval (direct launches) and deriveScanInterval
// (run_scout_post_coordinator.go) so both paths enforce one budget.
func clampScanInterval(d time.Duration) time.Duration {
	if d < scanIntervalFloor {
		return scanIntervalFloor
	}
	if d > scanIntervalCap {
		return scanIntervalCap
	}
	return d
}

// circuitPaceInterval is the END-OF-CIRCUIT wait for a multi-market tour (sp-enry):
// it paces the circuit PERIOD to target — waiting only the remainder after the
// circuit's own travel time, never a full extra interval and never a negative
// duration. Two properties fall out of this one formula:
//
//   - The API-budget invariant holds. Each partition is re-scanned about once per
//     target regardless of how many probes N split a system, so total scans/hour ≈
//     markets / freshness-target — INDEPENDENT of N. More probes buy smaller
//     partitions (fresher data), NOT more API calls (sp-enry, Admiral doctrine).
//   - A single-hull post over a big system stays byte-identical to the pre-enry
//     travel-paced loop: when the circuit already takes at least the target (a large
//     partition), the wait is zero and the probe loops as fast as travel allows — no
//     wait, no pacing log.
//
// The wait is applied ONCE per circuit (end-of-circuit), never per hop: the Admiral
// doctrine forbids per-hop waits, and a probe that idled between markets would leave
// the ones behind it stale for the whole circuit.
func circuitPaceInterval(target, elapsed time.Duration) time.Duration {
	if elapsed >= target {
		return 0
	}
	return target - elapsed
}

// effectiveScanInterval resolves cmd.ScanInterval to the cadence
// continuousMarketScanning actually waits on (sp-zixw). Zero/negative means "no
// interval supplied" — the direct/legacy launch path — and defaults to
// defaultDirectScanInterval before clamping. A coordinator-supplied interval
// (deriveScanInterval) is already clamped, so re-clamping here is a defensive
// no-op for that path and the only effective clamp for a direct caller that
// supplies an explicit out-of-band value.
func effectiveScanInterval(interval time.Duration) time.Duration {
	if interval <= 0 {
		interval = defaultDirectScanInterval
	}
	return clampScanInterval(interval)
}

// ScoutTourCommand - Command to execute a market scouting tour with a single ship
type ScoutTourCommand struct {
	PlayerID   shared.PlayerID
	ShipSymbol string
	Markets    []string // Waypoint symbols to scout
	Iterations int      // Number of complete tours (-1 for infinite)

	// CoordinatorID names the scout_post_coordinator that spawned this tour as a
	// managed worker (sp-cxpq). When non-empty it is persisted into the
	// container's config so daemon restart recovery SKIPS the tour (marks it
	// worker_interrupted, preserving the ship assignment) and leaves respawning to
	// the coordinator's reconcile pass — the contract_workflow worker pattern.
	// Empty for the standalone `workflow scout-markets` tours, which recover
	// independently as before.
	CoordinatorID string

	// ScanInterval is the cadence continuousMarketScanning waits between market
	// scans at a stationary post (sp-zixw). Coordinator-spawned tours set it from
	// the post's freshness target (deriveScanInterval, run_scout_post_coordinator.go);
	// zero/negative means "unset" — the direct/legacy launch path — and resolves to
	// defaultDirectScanInterval. Either way effectiveScanInterval clamps the final
	// value to [scanIntervalFloor, scanIntervalCap] so no path can drift outside the
	// API budget at 54+ hulls (replaces the old hardcoded 5m wait).
	ScanInterval time.Duration

	// StartJitterMaxSecs bounds a one-time deterministic phase offset waited out
	// before this tour's first scan (sp-x8i5): derived from a stable hash of
	// ShipSymbol, so the SAME ship always gets the SAME offset (no math/rand,
	// stable across restarts) in [0, StartJitterMaxSecs). Zero/absent resolves to
	// defaultTourStartJitterMax. Decoheres a fleet of scouts that would otherwise
	// wake in near-lockstep and burst the API rate budget every cycle.
	StartJitterMaxSecs int
}

// ScoutTourResponse - Response from scout tour execution
type ScoutTourResponse struct {
	MarketsVisited int
	TourOrder      []string // Order in which markets were visited
	Iterations     int
}

// ScoutTourHandler - Handles scout tour commands
type ScoutTourHandler struct {
	shipRepo      navigation.ShipRepository
	mediator      common.Mediator
	marketScanner *ship.MarketScanner
	// shipyardScanner piggybacks a shipyard scan on the SAME market visit
	// (sp-42ow): no extra trips, no new tour legs. Nil-safe — a tour without
	// one (tests, minimal wiring) simply skips shipyard scans.
	shipyardScanner *ship.ShipyardScanner
	clock           shared.Clock
}

// NewScoutTourHandler creates a new scout tour command handler. A nil clock
// defaults to shared.NewRealClock() (sp-zixw), matching the sibling coordinator
// handlers' constructor idiom. A nil shipyardScanner disables the piggybacked
// shipyard scan (sp-42ow) without affecting market scanning.
func NewScoutTourHandler(
	shipRepo navigation.ShipRepository,
	mediator common.Mediator,
	marketScanner *ship.MarketScanner,
	shipyardScanner *ship.ShipyardScanner,
	clock shared.Clock,
) *ScoutTourHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &ScoutTourHandler{
		shipRepo:        shipRepo,
		mediator:        mediator,
		marketScanner:   marketScanner,
		shipyardScanner: shipyardScanner,
		clock:           clock,
	}
}

// Handle executes the scout tour command
func (h *ScoutTourHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*ScoutTourCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	ship, tourOrder, response, err := h.loadShipAndPrepareTour(ctx, cmd)
	if err != nil {
		return nil, err
	}

	if !h.waitStartJitter(ctx, cmd) {
		return response, nil
	}

	if len(tourOrder) == 1 {
		err = h.executeStationaryScout(ctx, cmd, ship, tourOrder[0], response)
	} else {
		err = h.executeMultiMarketTour(ctx, cmd, tourOrder, response)
	}

	return response, err
}

// waitStartJitter waits out this tour's deterministic start-of-tour phase offset
// (sp-x8i5) before any navigation/scanning begins. Applied exactly once, here —
// the scan interval itself stays fixed for the tour's lifetime, so a one-time
// start offset permanently decoheres the wave through every later wake cycle;
// re-jittering per-iteration would buy nothing further and would complicate
// pacing math (circuitPaceInterval) for no benefit. Returns false if ctx is
// cancelled during the wait, mirroring sleepInterruptibly's contract, so the
// caller can return the (unmodified) response cleanly instead of starting a tour
// that was already asked to stop.
func (h *ScoutTourHandler) waitStartJitter(ctx context.Context, cmd *ScoutTourCommand) bool {
	ceiling := time.Duration(cmd.StartJitterMaxSecs) * time.Second
	if ceiling <= 0 {
		ceiling = defaultTourStartJitterMax
	}
	jitter := stableJitter(cmd.ShipSymbol, ceiling)
	if jitter <= 0 {
		return true
	}
	return h.sleepInterruptibly(ctx, jitter)
}

// loadShipAndPrepareTour loads ship data, rotates tour to start at current location, and initializes response
func (h *ScoutTourHandler) loadShipAndPrepareTour(
	ctx context.Context,
	cmd *ScoutTourCommand,
) (*navigation.Ship, []string, *ScoutTourResponse, error) {
	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to find ship: %w", err)
	}

	tourOrder := rotateTourToStart(cmd.Markets, ship.CurrentLocation().Symbol)

	response := &ScoutTourResponse{
		MarketsVisited: 0,
		TourOrder:      tourOrder,
		Iterations:     0,
	}

	return ship, tourOrder, response, nil
}

// executeStationaryScout executes a continuous scanning operation at a single market
func (h *ScoutTourHandler) executeStationaryScout(
	ctx context.Context,
	cmd *ScoutTourCommand,
	ship *navigation.Ship,
	marketWaypoint string,
	response *ScoutTourResponse,
) error {
	if err := h.navigateToMarketIfNeeded(ctx, ship, marketWaypoint, cmd.PlayerID, cmd.ShipSymbol); err != nil {
		return err
	}

	if ship.CurrentLocation().Symbol == marketWaypoint {
		if err := h.performInitialScan(ctx, uint(cmd.PlayerID.Value()), marketWaypoint, cmd.ShipSymbol); err == nil {
			response.MarketsVisited++
		}
	} else {
		response.MarketsVisited++
	}

	response.Iterations++

	return h.continuousMarketScanning(ctx, cmd, marketWaypoint, response)
}

// navigateToMarketIfNeeded navigates ship to market if not already there
func (h *ScoutTourHandler) navigateToMarketIfNeeded(
	ctx context.Context,
	ship *navigation.Ship,
	marketWaypoint string,
	playerID shared.PlayerID,
	shipSymbol string,
) error {
	if ship.CurrentLocation().Symbol == marketWaypoint {
		return nil
	}

	logger := common.LoggerFromContext(ctx)
	logger.Log("INFO", "Ship navigating to stationary scout position", map[string]interface{}{
		"ship_symbol": shipSymbol,
		"action":      "navigate",
		"destination": marketWaypoint,
		"tour_type":   "stationary_scout",
	})

	navCmd := &shipNav.NavigateRouteCommand{
		ShipSymbol:  shipSymbol,
		Destination: marketWaypoint,
		PlayerID:    playerID,
	}

	navResp, err := h.mediator.Send(ctx, navCmd)
	if err != nil {
		return fmt.Errorf("failed to navigate to %s: %w", marketWaypoint, err)
	}

	navResult := navResp.(*shipNav.NavigateRouteResponse)
	logger.Log("INFO", "Ship navigation complete - market scanned", map[string]interface{}{
		"ship_symbol":    shipSymbol,
		"action":         "navigation_complete",
		"status":         navResult.Status,
		"fuel":           navResult.FuelRemaining,
		"market_scanned": true,
	})

	return nil
}

// performInitialScan performs the first market scan when ship is already at market
func (h *ScoutTourHandler) performInitialScan(
	ctx context.Context,
	playerID uint,
	marketWaypoint string,
	shipSymbol string,
) error {
	logger := common.LoggerFromContext(ctx)
	logger.Log("INFO", "Ship performing initial market scan", map[string]interface{}{
		"ship_symbol": shipSymbol,
		"action":      "scan_market",
		"waypoint":    marketWaypoint,
		"reason":      "already_present",
	})

	if err := h.marketScanner.ScanAndSaveMarket(ctx, playerID, marketWaypoint); err != nil {
		logger.Log("ERROR", "Initial market scan failed", map[string]interface{}{
			"ship_symbol": shipSymbol,
			"action":      "scan_market",
			"waypoint":    marketWaypoint,
			"error":       err.Error(),
		})
		return err
	}

	h.scanShipyardAlongside(ctx, playerID, marketWaypoint, shipSymbol)

	return nil
}

// scanShipyardAlongside piggybacks a shipyard scan on the SAME market visit
// (sp-42ow): if the waypoint bears the SHIPYARD trait, its ship-type
// availability + prices are persisted to the shipyard-inventory store. No
// extra trips, no new tour legs — the scout is already here. Strictly
// non-fatal: a shipyard failure is logged and the tour proceeds (the market
// scan, the tour's primary duty, already succeeded).
func (h *ScoutTourHandler) scanShipyardAlongside(ctx context.Context, playerID uint, marketWaypoint, shipSymbol string) {
	if h.shipyardScanner == nil {
		return
	}
	if err := h.shipyardScanner.ScanAndSaveShipyard(ctx, playerID, marketWaypoint); err != nil {
		common.LoggerFromContext(ctx).Log("ERROR", "Shipyard scan failed (non-fatal to tour)", map[string]interface{}{
			"ship_symbol": shipSymbol,
			"action":      "scan_shipyard",
			"waypoint":    marketWaypoint,
			"error":       err.Error(),
		})
	}
}

// continuousMarketScanning runs a loop that scans the market on cmd.ScanInterval
// (sp-zixw) — resolved and clamped by effectiveScanInterval, so no launch path can
// hammer the API below the floor or drift stale above the cap.
func (h *ScoutTourHandler) continuousMarketScanning(
	ctx context.Context,
	cmd *ScoutTourCommand,
	marketWaypoint string,
	response *ScoutTourResponse,
) error {
	logger := common.LoggerFromContext(ctx)
	interval := effectiveScanInterval(cmd.ScanInterval)

	for iteration := 1; iteration < cmd.Iterations || cmd.Iterations == -1; iteration++ {
		logger.Log("INFO", "Waiting before next market scan", map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol,
			"action":      "wait_scan",
			"duration":    interval.String(),
		})

		if !h.sleepInterruptibly(ctx, interval) {
			logger.Log("INFO", "Scout tour cancelled by context", map[string]interface{}{
				"ship_symbol":          cmd.ShipSymbol,
				"action":               "tour_cancelled",
				"iterations_completed": response.Iterations,
			})
			return nil
		}

		logger.Log("INFO", "Scanning market at waypoint", map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol,
			"action":      "scan_market",
			"waypoint":    marketWaypoint,
			"iteration":   iteration + 1,
		})

		if err := h.marketScanner.ScanAndSaveMarket(ctx, uint(cmd.PlayerID.Value()), marketWaypoint); err != nil {
			logger.Log("ERROR", "Market scan failed", map[string]interface{}{
				"ship_symbol": cmd.ShipSymbol,
				"action":      "scan_market",
				"waypoint":    marketWaypoint,
				"iteration":   iteration + 1,
				"error":       err.Error(),
			})
		} else {
			response.MarketsVisited++
			h.scanShipyardAlongside(ctx, uint(cmd.PlayerID.Value()), marketWaypoint, cmd.ShipSymbol)
		}

		response.Iterations++
	}

	return nil
}

// sleepInterruptibly waits for d on h.clock, returning true if the wait completed
// normally or false if ctx was cancelled first (sp-zixw). Clock-injected so tests
// run on a MockClock with no wall-time cost, mirroring the sleepInterruptibly
// idiom used by run_factory_coordinator.go and run_trade_route_coordinator_travel.go
// — this handler's own private copy, returning bool so the caller can react to
// cancellation on the same tick (same log message, same immediate return nil) that
// the previous time.After/ctx.Done select achieved.
func (h *ScoutTourHandler) sleepInterruptibly(ctx context.Context, d time.Duration) bool {
	done := make(chan struct{})
	go func() {
		h.clock.Sleep(d)
		close(done)
	}()

	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}

// executeMultiMarketTour executes a tour visiting multiple markets in sequence.
// Each circuit is travel-paced with NO per-hop waits (Admiral doctrine, sp-enry):
// the probe navigates market-to-market scanning on arrival, then — for an
// infinite/standing tour — paces the CIRCUIT PERIOD to the freshness target once at
// the end (circuitPaceInterval) so a small partition does not over-scan. This is the
// per-partition consumer of the zixw freshness plumbing: a partitioned probe from
// a multi-hull post (run_scout_post_coordinator.go) and a direct scout-markets tour
// both flow through here, and both hit the same API-budget invariant.
func (h *ScoutTourHandler) executeMultiMarketTour(
	ctx context.Context,
	cmd *ScoutTourCommand,
	tourOrder []string,
	response *ScoutTourResponse,
) error {
	logger := common.LoggerFromContext(ctx)
	interval := effectiveScanInterval(cmd.ScanInterval)

	for iteration := 0; iteration < cmd.Iterations || cmd.Iterations == -1; iteration++ {
		circuitStart := h.clock.Now()

		for _, marketWaypoint := range tourOrder {
			navResult, err := h.navigateToMarket(ctx, cmd, marketWaypoint, iteration)
			if err != nil {
				return err
			}

			logger.Log("INFO", "Ship navigation complete", map[string]interface{}{
				"ship_symbol": cmd.ShipSymbol,
				"action":      "navigation_complete",
				"status":      navResult.Status,
				"fuel":        navResult.FuelRemaining,
			})

			// OPTIMIZATION: Market scan is already performed by RouteExecutor.scanMarketIfPresent()
			// when the ship arrives at a marketplace waypoint. No need to scan again here.
			response.MarketsVisited++
		}

		response.Iterations++

		// End-of-circuit pacing (sp-enry): pace the circuit period to the freshness
		// target. Skipped after the FINAL circuit of a finite tour (nothing follows),
		// and a zero wait — a big partition whose circuit already exceeds the target —
		// emits no wait and no log, keeping a single-hull big-system tour byte-identical
		// to the pre-enry travel-paced loop.
		lastCircuit := cmd.Iterations != -1 && iteration+1 >= cmd.Iterations
		if lastCircuit {
			continue
		}
		wait := circuitPaceInterval(interval, h.clock.Now().Sub(circuitStart))
		if wait <= 0 {
			continue
		}
		logger.Log("INFO", "Circuit complete — pacing to freshness target", map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol,
			"action":      "circuit_pace",
			"duration":    wait.String(),
			"markets":     len(tourOrder),
		})
		if !h.sleepInterruptibly(ctx, wait) {
			logger.Log("INFO", "Scout tour cancelled by context", map[string]interface{}{
				"ship_symbol":          cmd.ShipSymbol,
				"action":               "tour_cancelled",
				"iterations_completed": response.Iterations,
			})
			return nil
		}
	}

	return nil
}

// navigateToMarket navigates ship to specified market waypoint
func (h *ScoutTourHandler) navigateToMarket(
	ctx context.Context,
	cmd *ScoutTourCommand,
	marketWaypoint string,
	iteration int,
) (*shipNav.NavigateRouteResponse, error) {
	logger := common.LoggerFromContext(ctx)
	logger.Log("INFO", "Ship navigating to market on tour", map[string]interface{}{
		"ship_symbol": cmd.ShipSymbol,
		"action":      "navigate",
		"destination": marketWaypoint,
		"tour_type":   "multi_market",
		"iteration":   iteration + 1,
	})

	navCmd := &shipNav.NavigateRouteCommand{
		ShipSymbol:  cmd.ShipSymbol,
		Destination: marketWaypoint,
		PlayerID:    cmd.PlayerID,
	}

	navResp, err := h.mediator.Send(ctx, navCmd)
	if err != nil {
		return nil, fmt.Errorf("failed to navigate to %s: %w", marketWaypoint, err)
	}

	return navResp.(*shipNav.NavigateRouteResponse), nil
}

// rotateTourToStart rotates the tour slice so it starts from the ship's current position
// This provides idempotency: if the command is re-run, it continues from where the ship is
func rotateTourToStart(markets []string, currentPosition string) []string {
	// Find index of current position in markets
	startIndex := -1
	for i, waypoint := range markets {
		if waypoint == currentPosition {
			startIndex = i
			break
		}
	}

	// If current position not in tour, return original order
	if startIndex == -1 {
		return markets
	}

	// Rotate slice to start from current position
	rotated := make([]string, len(markets))
	for i := 0; i < len(markets); i++ {
		rotated[i] = markets[(startIndex+i)%len(markets)]
	}

	return rotated
}

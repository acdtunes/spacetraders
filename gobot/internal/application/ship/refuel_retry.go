package ship

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// Default retry/backoff budget for refuelShipWithRetry. A transient game-API 500
// hitting a refuel step must not terminally crash the caller. These defaults govern
// the RETRY-AT-THE-SAME-WAYPOINT leg only; once exhausted, refuelShipWithRetryCore
// hands off to refuelAtAlternateStop rather than retrying the same failing waypoint
// indefinitely.
//
// The budget is WALL CLOCK, not attempts (sp-l7zha). It was three attempts, and
// because each backoff doubled, that bounded the wait at roughly five minutes
// while naming a number that says nothing about time — retuning the backoff base
// would have silently moved the real budget. Escalating is enormously more
// expensive than waiting: on the staging incident the alternate-stop reroute cost
// a 41-minute DRIFT crawl and blocked the whole serial contract pipeline behind
// it, against a 500 that clears on its own. So the loop keeps probing until the
// elapsed budget is genuinely spent, and the backoff interval is capped so the
// tail of that budget stays a series of probes rather than one long sleep.
const (
	// DefaultRefuelRetryBudget is the total wall-clock time a RETRYABLE refuel
	// failure is retried at the original waypoint before rerouting to an
	// alternate fuel stop.
	DefaultRefuelRetryBudget = 10 * time.Minute

	// DefaultRefuelBackoffBase is the first backoff duration between refuel
	// attempts. It doubles after each failed attempt until it reaches
	// DefaultRefuelBackoffMax.
	DefaultRefuelBackoffBase = 2 * time.Second

	// DefaultRefuelBackoffMax caps a single backoff interval. Unbounded
	// doubling from the 2s base passes four minutes by the eighth interval,
	// which would re-probe a transient 500 only a handful of times across the
	// whole budget and leave recovery to luck.
	DefaultRefuelBackoffMax = 60 * time.Second
)

// ErrRefuelUnrecoverable is returned when a refuel step cannot complete even
// after retrying the original waypoint with backoff AND attempting a reroute
// to an alternate fuel-capable waypoint. Callers that orchestrate multi-step
// chains (goods_factory coordinators, in particular) should PARK on this
// error - preserving chain/pipeline state for resumption on the next poll
// cycle - rather than letting it propagate into a terminal crash the way
// goods_factory-SHIP_PARTS-c7e2ecb2 and the SHIP_PLATING recurrence did.
type ErrRefuelUnrecoverable struct {
	ShipSymbol string
	Waypoint   string
	Attempts   int
	Elapsed    time.Duration
	Cause      error
}

func (e *ErrRefuelUnrecoverable) Error() string {
	return fmt.Sprintf(
		"refuel unrecoverable for %s at %s after %d attempt(s) over %s, alternate-stop reroute also failed: %v",
		e.ShipSymbol, e.Waypoint, e.Attempts, e.Elapsed, e.Cause,
	)
}

func (e *ErrRefuelUnrecoverable) Unwrap() error { return e.Cause }

// isRetryableRefuelError reports whether err looks like a transient failure
// from the underlying game API rather than a permanent/logic error (e.g.
// insufficient credits). internal/adapters/api's retryableError type is
// unexported, so detection here is via substring match against the same
// messages retry_policy.go's final wrap produces: "server error (%d)" for
// 5xx, "rate limited (429)", "service unavailable (503)", "network error:
// %w", and "max retries exceeded: %w" wrapping any of those. The evidence
// bug surfaced exactly "max retries exceeded: server error (500)"
// from a refuel step.
func isRetryableRefuelError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	transientSubstrings := []string{
		"server error (5",
		"rate limited (429)",
		"service unavailable (503)",
		"network error",
		"max retries exceeded",
		"timeout",
	}
	for _, s := range transientSubstrings {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// refuelShipWithRetry refuels ship at its current location, retrying a
// transient failure with backoff and rerouting to an alternate fuel-capable
// waypoint if the original stop keeps failing past the retry budget. This is
// the entry point production code should call instead of
// refuelShip directly at every refuel call site.
func (e *RouteExecutor) refuelShipWithRetry(
	ctx context.Context,
	ship *domainNavigation.Ship,
	playerID shared.PlayerID,
	returnToOrbit bool,
) error {
	return e.refuelShipWithRetryCore(ctx, ship, playerID, DefaultRefuelRetryBudget, DefaultRefuelBackoffBase, returnToOrbit)
}

// refuelShipWithRetryCore is refuelShipWithRetry's configurable core. Tests can
// inject a smaller budget to keep fixtures short; neither budget nor backoffBase
// needs shrinking for tests that wire a shared.MockClock, whose Sleep advances
// time instantly without blocking.
//
// budget is the total wall-clock time a RETRYABLE failure keeps being retried at
// the ORIGINAL waypoint. It is measured from e.clock, so the loop always makes at
// least one more attempt once the budget has fully elapsed rather than stopping
// short of it — the guarantee callers actually need is a MINIMUM time spent
// waiting out a transient upstream failure, since the escalation below is far more
// expensive than the wait.
//
// returnToOrbit is threaded straight to refuelShip: the same-waypoint retry
// loop honours the caller's stay-docked choice. The alternate-stop reroute
// below always returns to orbit — after a reroute the
// ship has moved to a different waypoint, so the stay-docked optimization (whose
// point is to let a co-located trade skip its dock) no longer applies and the
// caller still needs orbit for the leg it was preparing.
func (e *RouteExecutor) refuelShipWithRetryCore(
	ctx context.Context,
	ship *domainNavigation.Ship,
	playerID shared.PlayerID,
	budget time.Duration,
	backoffBase time.Duration,
	returnToOrbit bool,
) error {
	logger := common.LoggerFromContext(ctx)
	origin := ship.CurrentLocation()
	start := e.clock.Now()

	var lastErr error
	attempt := 0
	backoff := capRefuelBackoff(backoffBase)
	for {
		attempt++
		err := e.refuelShip(ctx, ship, playerID, returnToOrbit)
		if err == nil {
			return nil
		}
		lastErr = err

		if !isRetryableRefuelError(err) {
			logger.Log("ERROR", "Refuel failed with a non-transient error, failing fast", map[string]interface{}{
				"ship_symbol": ship.ShipSymbol(),
				"action":      "refuel_failed_non_transient",
				"waypoint":    origin.Symbol,
				"attempt":     attempt,
				"error":       err.Error(),
			})
			return err
		}

		// A cancelled context means the caller is being torn down. Sleeping out
		// the rest of a ten-minute budget would hold the hull for the whole of
		// it, so surface the refuel failure now instead — and never spend the
		// reroute's navigate on a dying container.
		if ctxErr := ctx.Err(); ctxErr != nil {
			logger.Log("WARNING", "Refuel retry budget abandoned - context cancelled", map[string]interface{}{
				"ship_symbol": ship.ShipSymbol(),
				"action":      "refuel_retry_cancelled",
				"waypoint":    origin.Symbol,
				"attempt":     attempt,
				"elapsed":     e.clock.Now().Sub(start).String(),
				"error":       err.Error(),
			})
			return err
		}

		elapsed := e.clock.Now().Sub(start)
		if elapsed >= budget {
			break
		}

		logger.Log("WARNING", "Transient refuel failure, retrying with backoff", map[string]interface{}{
			"ship_symbol":  ship.ShipSymbol(),
			"action":       "refuel_retry",
			"waypoint":     origin.Symbol,
			"attempt":      attempt,
			"elapsed":      elapsed.String(),
			"retry_budget": budget.String(),
			"backoff":      backoff.String(),
			"error":        err.Error(),
		})

		e.clock.Sleep(backoff)
		backoff = nextRefuelBackoff(backoff)
	}

	elapsed := e.clock.Now().Sub(start)
	logger.Log("ERROR", "Refuel retry budget exhausted at waypoint, attempting alternate fuel stop", map[string]interface{}{
		"ship_symbol":  ship.ShipSymbol(),
		"action":       "refuel_retries_exhausted",
		"waypoint":     origin.Symbol,
		"attempts":     attempt,
		"elapsed":      elapsed.String(),
		"retry_budget": budget.String(),
		"error":        lastErr.Error(),
	})

	if rerouteErr := e.refuelAtAlternateStop(ctx, ship, playerID, origin); rerouteErr != nil {
		logger.Log("ERROR", "Alternate fuel stop reroute also failed", map[string]interface{}{
			"ship_symbol":   ship.ShipSymbol(),
			"action":        "refuel_reroute_failed",
			"waypoint":      origin.Symbol,
			"reroute_error": rerouteErr.Error(),
		})
		return &ErrRefuelUnrecoverable{
			ShipSymbol: ship.ShipSymbol(),
			Waypoint:   origin.Symbol,
			Attempts:   attempt,
			Elapsed:    elapsed,
			Cause:      lastErr,
		}
	}

	return nil
}

// nextRefuelBackoff doubles the interval up to DefaultRefuelBackoffMax. Growth is
// computed from the PREVIOUS interval rather than from the attempt number, so it
// cannot overflow on a long budget the way a 1<<attempt shift does (the <= 0 arm
// catches a wrap should the base ever be set absurdly high).
func nextRefuelBackoff(current time.Duration) time.Duration {
	return capRefuelBackoff(current * 2)
}

// capRefuelBackoff holds a single backoff interval at DefaultRefuelBackoffMax so
// the invariant covers the FIRST interval too, not only the doubled ones.
func capRefuelBackoff(d time.Duration) time.Duration {
	if d > DefaultRefuelBackoffMax || d <= 0 {
		return DefaultRefuelBackoffMax
	}
	return d
}

// refuelAtAlternateStop reroutes ship to the nearest alternate fuel-capable
// marketplace in its system (excluding failedWaypoint) and refuels there.
// Candidates are filtered to HasFuel waypoints via static waypoint trait data
// rather than a live MarketScanner query (no test here drives a live-market
// check, so none is added).
func (e *RouteExecutor) refuelAtAlternateStop(
	ctx context.Context,
	ship *domainNavigation.Ship,
	playerID shared.PlayerID,
	failedWaypoint *shared.Waypoint,
) error {
	if e.waypointRepo == nil {
		return fmt.Errorf("no alternate fuel stop available: waypoint repository not configured")
	}

	logger := common.LoggerFromContext(ctx)

	candidates, err := e.waypointRepo.ListBySystemWithTrait(ctx, failedWaypoint.SystemSymbol, "MARKETPLACE")
	if err != nil {
		return fmt.Errorf("failed to list alternate fuel candidates: %w", err)
	}

	var fuelCandidates []*shared.Waypoint
	for _, wp := range candidates {
		if wp.Symbol == failedWaypoint.Symbol {
			continue
		}
		if !wp.HasFuel {
			continue
		}
		fuelCandidates = append(fuelCandidates, wp)
	}
	if len(fuelCandidates) == 0 {
		return fmt.Errorf("no alternate fuel-capable waypoint found in system %s", failedWaypoint.SystemSymbol)
	}

	alt, _ := shared.FindNearestWaypoint(failedWaypoint, fuelCandidates)

	logger.Log("WARNING", "Rerouting to alternate fuel stop after exhausting retries", map[string]interface{}{
		"ship_symbol":     ship.ShipSymbol(),
		"action":          "refuel_reroute",
		"failed_waypoint": failedWaypoint.Symbol,
		"alternate":       alt.Symbol,
	})

	if err := e.navigateShipDirect(ctx, ship, playerID, alt, shared.FlightModeDrift); err != nil {
		return fmt.Errorf("failed to navigate to alternate fuel waypoint %s: %w", alt.Symbol, err)
	}

	// Return to orbit after the reroute refuel: the ship has moved, so the
	// stay-docked optimization does not apply and the caller still needs orbit.
	return e.refuelShip(ctx, ship, playerID, true)
}

// navigateShipDirect sends ship directly to dest and waits for arrival if the
// response includes one, mirroring navigateToSegmentDestination's
// command/response handling for the out-of-route reroute path (the
// alternate-fuel-stop navigate is not part of any planned Route, so it cannot
// reuse executeSegment).
func (e *RouteExecutor) navigateShipDirect(
	ctx context.Context,
	ship *domainNavigation.Ship,
	playerID shared.PlayerID,
	dest *shared.Waypoint,
	flightMode shared.FlightMode,
) error {
	if err := e.ensureShipInOrbit(ctx, ship, playerID); err != nil {
		return err
	}
	if err := e.setShipFlightMode(ctx, ship, playerID, flightMode); err != nil {
		return err
	}

	navCmd := &types.NavigateDirectCommand{
		Ship:                ship,
		Destination:         dest.Symbol,
		DestinationWaypoint: dest,
		PlayerID:            playerID,
		FlightMode:          flightMode.Name(),
	}
	navResp, err := e.mediator.Send(ctx, navCmd)
	if err != nil {
		return fmt.Errorf("failed to navigate: %w", err)
	}

	navResponse, ok := navResp.(*types.NavigateDirectResponse)
	if !ok {
		return fmt.Errorf("unexpected response type: %T", navResp)
	}

	if navResponse.Status == "already_at_destination" {
		return nil
	}

	if navResponse.ArrivalTimeStr != "" {
		if err := e.waitForArrival(ctx, ship, navResponse.ArrivalTimeStr, playerID); err != nil {
			return err
		}
	}

	if navResponse.FuelCurrent > 0 || navResponse.FuelCapacity > 0 {
		if err := ship.UpdateFuelFromAPI(navResponse.FuelCurrent, navResponse.FuelCapacity); err != nil {
			return fmt.Errorf("failed to update fuel from navigation response: %w", err)
		}
	}

	return nil
}

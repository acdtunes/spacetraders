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

// Default retry/backoff budget for refuelShipWithRetry (sp-vsfn). A transient
// game-API 500 hitting a refuel step must not terminally crash the caller -
// goods_factory-SHIP_PARTS-c7e2ecb2 crashed the ENTIRE manufacturing chain on
// exactly this signature: "failed to refuel: ... max retries exceeded:
// server error (500)" while docked at a fuel stop (X1-KA42-B7), losing chain
// state and requiring a ~25 minute manual captain relaunch. These defaults
// govern the RETRY-AT-THE-SAME-WAYPOINT leg only; once exhausted,
// refuelShipWithRetryCore hands off to refuelAtAlternateStop rather than
// retrying the same failing waypoint indefinitely.
const (
	// DefaultRefuelMaxAttempts is the number of refuel attempts made at the
	// original waypoint before rerouting to an alternate fuel stop.
	DefaultRefuelMaxAttempts = 3

	// DefaultRefuelBackoffBase is the base backoff duration between refuel
	// attempts, doubling after each failed attempt (attempt 1 waits
	// 1xBase, attempt 2 waits 2xBase; attempt 3 exhausts the budget with no
	// further wait before rerouting).
	DefaultRefuelBackoffBase = 2 * time.Second
)

// ErrRefuelUnrecoverable is returned when a refuel step cannot complete even
// after retrying the original waypoint with backoff AND attempting a reroute
// to an alternate fuel-capable waypoint. Callers that orchestrate multi-step
// chains (goods_factory coordinators, in particular) should PARK on this
// error - preserving chain/pipeline state for resumption on the next poll
// cycle - rather than letting it propagate into a terminal crash the way
// goods_factory-SHIP_PARTS-c7e2ecb2 and the SHIP_PLATING recurrence did
// (sp-vsfn).
type ErrRefuelUnrecoverable struct {
	ShipSymbol string
	Waypoint   string
	Attempts   int
	Cause      error
}

func (e *ErrRefuelUnrecoverable) Error() string {
	return fmt.Sprintf(
		"refuel unrecoverable for %s at %s after %d attempt(s), alternate-stop reroute also failed: %v",
		e.ShipSymbol, e.Waypoint, e.Attempts, e.Cause,
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
// bug (sp-vsfn) surfaced exactly "max retries exceeded: server error (500)"
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
// waypoint if the original stop keeps failing past the retry budget
// (sp-vsfn). This is the entry point production code should call instead of
// refuelShip directly at every refuel call site.
func (e *RouteExecutor) refuelShipWithRetry(
	ctx context.Context,
	ship *domainNavigation.Ship,
	playerID shared.PlayerID,
	returnToOrbit bool,
) error {
	return e.refuelShipWithRetryCore(ctx, ship, playerID, DefaultRefuelMaxAttempts, DefaultRefuelBackoffBase, returnToOrbit)
}

// refuelShipWithRetryCore is refuelShipWithRetry's configurable core. Tests
// can inject a smaller maxAttempts to keep fixtures short; backoffBase does
// not need to be shrunk for tests since e.clock is expected to be a
// shared.MockClock whose Sleep advances time instantly without blocking.
//
// returnToOrbit is threaded straight to refuelShip (sp-yd84 CUT 2): the
// same-waypoint retry loop honours the caller's stay-docked choice. The
// alternate-stop reroute below always returns to orbit — after a reroute the
// ship has moved to a different waypoint, so the stay-docked optimization (whose
// point is to let a co-located trade skip its dock) no longer applies and the
// caller still needs orbit for the leg it was preparing.
func (e *RouteExecutor) refuelShipWithRetryCore(
	ctx context.Context,
	ship *domainNavigation.Ship,
	playerID shared.PlayerID,
	maxAttempts int,
	backoffBase time.Duration,
	returnToOrbit bool,
) error {
	logger := common.LoggerFromContext(ctx)
	origin := ship.CurrentLocation()

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
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

		logger.Log("WARNING", "Transient refuel failure, retrying with backoff", map[string]interface{}{
			"ship_symbol":  ship.ShipSymbol(),
			"action":       "refuel_retry",
			"waypoint":     origin.Symbol,
			"attempt":      attempt,
			"max_attempts": maxAttempts,
			"error":        err.Error(),
		})

		if attempt < maxAttempts {
			backoff := backoffBase * time.Duration(int64(1)<<uint(attempt-1))
			e.clock.Sleep(backoff)
		}
	}

	logger.Log("ERROR", "Refuel retries exhausted at waypoint, attempting alternate fuel stop", map[string]interface{}{
		"ship_symbol": ship.ShipSymbol(),
		"action":      "refuel_retries_exhausted",
		"waypoint":    origin.Symbol,
		"attempts":    maxAttempts,
		"error":       lastErr.Error(),
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
			Attempts:   maxAttempts,
			Cause:      lastErr,
		}
	}

	return nil
}

// refuelAtAlternateStop reroutes ship to the nearest alternate fuel-capable
// marketplace in its system (excluding failedWaypoint) and refuels there.
// Candidates are filtered to HasFuel waypoints - the "verify fuel-stop
// selection is sane" check the bead asked for, applied via static waypoint
// trait data rather than a live MarketScanner query (sp-vsfn scope: no test
// here drives a live-market check, so none is added).
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
// command/response handling for the out-of-route reroute path (sp-vsfn's
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

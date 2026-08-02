package ship

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

func (e *RouteExecutor) ensureShipInOrbit(ctx context.Context, ship *domainNavigation.Ship, playerID shared.PlayerID) error {
	orbitCmd := &types.OrbitShipCommand{
		Ship:     ship,
		PlayerID: playerID,
	}
	if _, err := e.mediator.Send(ctx, orbitCmd); err != nil {
		err = e.retryOnceAfterAdoptedTransit(ctx, ship, playerID, err, func() error {
			_, retryErr := e.mediator.Send(ctx, orbitCmd)
			return retryErr
		})
		if err != nil {
			return fmt.Errorf("failed to orbit: %w", err)
		}
	}
	return nil
}

// retryOnceAfterAdoptedTransit resumes an op the server rejected with "ship is
// in transit" (4214). By contract the rejecting handler has already adopted
// the server's nav into ship, so the transit is real and bounded: wait it out
// through the standard arrival machinery (a no-op when it already ended) and
// give op exactly one more attempt. Any other error passes through untouched.
// This keeps a routine desync from failing the command - each surfaced
// failure burns a slot of the container's LIFETIME restart budget.
func (e *RouteExecutor) retryOnceAfterAdoptedTransit(ctx context.Context, ship *domainNavigation.Ship, playerID shared.PlayerID, cause error, op func() error) error {
	var transitErr *types.ErrShipInTransit
	if !errors.As(cause, &transitErr) {
		return cause
	}
	common.LoggerFromContext(ctx).Log("WARNING", "Ship action rejected: server shows the hull mid-transit - waiting out the adopted transit and retrying once", map[string]interface{}{
		"ship_symbol":         ship.ShipSymbol(),
		"action":              "adopted_transit_retry",
		"transit_destination": transitErr.Destination,
	})
	if err := e.waitForCurrentTransit(ctx, ship, playerID); err != nil {
		return err
	}
	return op()
}

// resumeSegmentAfterAdoptedTransit resumes a segment whose navigate was
// rejected because the SERVER shows the hull mid-transit. By contract the
// rejecting handler has already adopted the server's nav into ship, so the
// transit is real and bounded: wait it out through the standard arrival
// machinery, then resume from wherever the hull actually landed:
//   - the segment's destination => the transit WAS this leg, arrival
//     completes it;
//   - the segment's origin => the premises still hold, issue the navigate
//     exactly once more;
//   - anywhere else => the route's plan no longer applies; surface an error
//     for the caller to re-plan - now from TRUTHFUL local state, so the
//     re-plan succeeds instead of repeating the doomed navigate until the
//     container's restart budget dies.
func (e *RouteExecutor) resumeSegmentAfterAdoptedTransit(
	ctx context.Context,
	segment *domainNavigation.RouteSegment,
	ship *domainNavigation.Ship,
	playerID shared.PlayerID,
	flightMode shared.FlightMode,
	transitErr *types.ErrShipInTransit,
) error {
	common.LoggerFromContext(ctx).Log("WARNING", "Navigate rejected: server shows the hull mid-transit - waiting out the adopted transit before resuming the segment", map[string]interface{}{
		"ship_symbol":         ship.ShipSymbol(),
		"action":              "segment_adopted_transit",
		"transit_destination": transitErr.Destination,
		"segment_from":        segment.FromWaypoint.Symbol,
		"segment_to":          segment.ToWaypoint.Symbol,
	})
	if err := e.waitForCurrentTransit(ctx, ship, playerID); err != nil {
		return err
	}

	at := ship.CurrentLocation()
	switch {
	case at != nil && at.Symbol == segment.ToWaypoint.Symbol:
		return nil
	case at != nil && at.Symbol == segment.FromWaypoint.Symbol:
		return e.attemptSegmentNavigate(ctx, segment, ship, playerID, flightMode, false)
	default:
		location := ""
		if at != nil {
			location = at.Symbol
		}
		return fmt.Errorf("hull %s landed at %s after an adopted transit; segment %s->%s no longer applies: %w",
			ship.ShipSymbol(), location, segment.FromWaypoint.Symbol, segment.ToWaypoint.Symbol, transitErr)
	}
}

// waitForCurrentTransit waits for ship to complete its current transit using event-based notification.
// CRITICAL: After waiting, persists ship state to DB to prevent stale state loops.
func (e *RouteExecutor) waitForCurrentTransit(
	ctx context.Context,
	ship *domainNavigation.Ship,
	playerID shared.PlayerID,
) error {
	logger := common.LoggerFromContext(ctx)

	// If ship is not in transit, nothing to wait for
	if ship.NavStatus() != domainNavigation.NavStatusInTransit {
		return nil
	}

	logger.Log("INFO", "Ship in transit from previous command", map[string]interface{}{
		"ship_symbol": ship.ShipSymbol(),
		"action":      "wait_previous_transit",
		"status":      "IN_TRANSIT",
	})

	// Calculate wait time from DB arrival time
	var waitTimeSeconds int
	if ship.ArrivalTime() != nil {
		waitTime := time.Until(*ship.ArrivalTime())
		if waitTime > 0 {
			waitTimeSeconds = int(waitTime.Seconds())
		}
	}

	// Event-based waiting, with a timeout->resync->park backstop if the
	// ARRIVED event is lost or raced against subscription.
	if err := WaitForShipArrival(ctx, e.shipRepo, e.shipEventSubscriber, ship, playerID, waitTimeSeconds, logger); err != nil {
		return err
	}

	// Persist ship state to DB after arrival to prevent stale state loops.
	// Clear the local pointer's arrival clock first so the caller keeps using a
	// consistent ship, then persist the arrival under CAS-retry: the
	// closure re-applies the IN_TRANSIT->arrived transition on the FRESH row so a
	// concurrent writer's cargo/fuel update on the same hull survives instead of
	// being last-write-wins clobbered by this executor's older in-memory snapshot.
	// This op owns ONLY the arrival: it touches nothing but nav status + arrival
	// clock. If a concurrent writer (typically ShipStateScheduler) already landed
	// the arrival, the fresh row is no longer IN_TRANSIT -> changed=false -> no
	// write and no spurious version bump.
	if e.shipRepo != nil && ship.NavStatus() != domainNavigation.NavStatusInTransit {
		// Clear arrival time since ship has arrived
		ship.ClearArrivalTime()

		if _, _, err := e.shipRepo.SaveWithRetry(ctx, ship.ShipSymbol(), playerID,
			func(sh *domainNavigation.Ship) (bool, error) {
				if sh.NavStatus() != domainNavigation.NavStatusInTransit {
					return false, nil
				}
				if aerr := sh.Arrive(); aerr != nil {
					return false, aerr
				}
				sh.ClearArrivalTime()
				return true, nil
			}); err != nil {
			logger.Log("WARNING", "Failed to persist ship state after transit wait", map[string]interface{}{
				"ship_symbol": ship.ShipSymbol(),
				"error":       err.Error(),
			})
		} else {
			logger.Log("DEBUG", "Persisted ship state after transit wait", map[string]interface{}{
				"ship_symbol": ship.ShipSymbol(),
				"location":    ship.CurrentLocation().Symbol,
				"nav_status":  string(ship.NavStatus()),
			})
		}
	}

	logger.Log("INFO", "Ship arrival confirmed", map[string]interface{}{
		"ship_symbol": ship.ShipSymbol(),
		"action":      "arrival_confirmed",
		"status":      string(ship.NavStatus()),
	})
	return nil
}

// waitForArrival waits for ship to arrive at destination using event-based notification.
// Uses ShipEventSubscriber to receive ARRIVED event from ShipStateScheduler.
func (e *RouteExecutor) waitForArrival(
	ctx context.Context,
	ship *domainNavigation.Ship,
	arrivalTimeStr string,
	playerID shared.PlayerID,
) error {
	logger := common.LoggerFromContext(ctx)

	arrivalTime, err := shared.NewArrivalTime(arrivalTimeStr)
	if err != nil {
		return fmt.Errorf("failed to parse arrival time: %w", err)
	}
	waitTime := arrivalTime.CalculateWaitTime()

	// If ship is not in transit, no need to wait
	if ship.NavStatus() != domainNavigation.NavStatusInTransit {
		return nil
	}

	// Event-based waiting, with a timeout->resync->park backstop if the
	// ARRIVED event is lost or raced against subscription.
	return WaitForShipArrival(ctx, e.shipRepo, e.shipEventSubscriber, ship, playerID, waitTime, logger)
}

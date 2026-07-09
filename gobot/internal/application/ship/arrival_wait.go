package ship

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// Default grace period and attempt budget for WaitForShipArrival's
// timeout->resync->park backstop (sp-pafv). ShipEventBus delivers ARRIVED
// events via a non-blocking, non-replaying send (PublishArrived): if
// PublishArrived races ahead of SubscribeArrived, or a subscriber's buffered
// channel is already full, the event is dropped forever with no redelivery.
// Without a bound, a lost/raced event stalls the waiting worker/coordinator
// permanently. These defaults only govern the timeout leg - the happy path
// (event arrives) is unaffected and still returns as soon as the event shows up.
const (
	DefaultArrivalGracePeriod = 30 * time.Second
	DefaultArrivalMaxAttempts = 3
)

// ErrArrivalWaitExhausted is returned when a ship-arrival wait gives up: the
// ARRIVED event never arrived AND repeated resyncs against the ship
// repository kept showing the ship still IN_TRANSIT. Callers should park or
// defer the task rather than retry inline.
type ErrArrivalWaitExhausted struct {
	ShipSymbol string
	Attempts   int
}

func (e *ErrArrivalWaitExhausted) Error() string {
	return fmt.Sprintf(
		"ship arrival wait exhausted for %s after %d attempt(s): event never received and resync still shows IN_TRANSIT",
		e.ShipSymbol, e.Attempts,
	)
}

// WaitForShipArrival waits for ship to leave IN_TRANSIT via the ARRIVED
// event published by ShipStateScheduler. If that event is lost or raced
// against subscription (ShipEventBus.PublishArrived is a non-blocking,
// non-replaying send - see ship_event_bus.go), this falls back to
// periodically resyncing ship against shipRepo instead of blocking forever.
// This is the shared safety net for every evented arrival wait in the
// codebase (sp-pafv).
//
// On success, ship's own NavStatus is transitioned via Arrive() so the
// caller's existing pointer reflects the new state - callers do not need to
// discard ship and switch to a freshly reloaded copy.
func WaitForShipArrival(
	ctx context.Context,
	shipRepo domainNavigation.ShipQueryRepository,
	subscriber domainNavigation.ShipEventSubscriber,
	ship *domainNavigation.Ship,
	playerID shared.PlayerID,
	waitTimeSeconds int,
	logger common.ContainerLogger,
) error {
	return waitForShipArrivalCore(
		ctx, shipRepo, subscriber, ship, playerID, waitTimeSeconds, logger,
		DefaultArrivalGracePeriod, DefaultArrivalMaxAttempts,
	)
}

// waitForShipArrivalCore is WaitForShipArrival's configurable core. Tests
// inject a tiny gracePeriod and small maxAttempts to exercise the
// timeout->resync->park backstop without slowing down the suite; production
// always goes through WaitForShipArrival's fixed defaults above.
func waitForShipArrivalCore(
	ctx context.Context,
	shipRepo domainNavigation.ShipQueryRepository,
	subscriber domainNavigation.ShipEventSubscriber,
	ship *domainNavigation.Ship,
	playerID shared.PlayerID,
	waitTimeSeconds int,
	logger common.ContainerLogger,
	gracePeriod time.Duration,
	maxAttempts int,
) error {
	shipSymbol := ship.ShipSymbol()

	arrivedCh := subscriber.SubscribeArrived(shipSymbol)
	defer subscriber.UnsubscribeArrived(shipSymbol, arrivedCh)

	logger.Log("INFO", "Waiting for ship arrival event", map[string]interface{}{
		"ship_symbol":      shipSymbol,
		"action":           "wait_arrival_event",
		"expected_seconds": waitTimeSeconds,
	})

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		select {
		case event := <-arrivedCh:
			// Ship arrived - update domain state to match. Domain state is
			// already updated by ShipStateScheduler; just sync our local
			// copy to reflect the new state (unchanged happy path).
			logger.Log("INFO", "Ship arrival event received", map[string]interface{}{
				"ship_symbol": shipSymbol,
				"action":      "arrival_event_received",
				"location":    event.Location,
				"status":      string(event.Status),
			})
			return applyArrival(ship)

		case <-ctx.Done():
			return ctx.Err()

		case <-time.After(gracePeriod):
			// No event within the grace period: resync against the source of
			// truth instead of assuming the event is merely slow - it may
			// have been dropped by ShipEventBus's non-blocking, non-replaying
			// send (lost if PublishArrived raced ahead of SubscribeArrived).
			logger.Log("WARNING", "Ship arrival event not received within grace period, resyncing", map[string]interface{}{
				"ship_symbol":  shipSymbol,
				"action":       "arrival_wait_resync",
				"attempt":      attempt,
				"max_attempts": maxAttempts,
			})
			fresh, err := shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
			if err != nil {
				logger.Log("WARNING", "Arrival resync lookup failed, will retry", map[string]interface{}{
					"ship_symbol": shipSymbol,
					"attempt":     attempt,
					"error":       err.Error(),
				})
				continue
			}
			if fresh.NavStatus() != domainNavigation.NavStatusInTransit {
				logger.Log("INFO", "Arrival resync confirmed ship left transit", map[string]interface{}{
					"ship_symbol": shipSymbol,
					"action":      "arrival_wait_resync_confirmed",
					"status":      string(fresh.NavStatus()),
				})
				return applyArrival(ship)
			}
			// Source of truth still shows IN_TRANSIT: the ship legitimately
			// has not arrived yet (or the event was lost but arrival is
			// still pending). Loop back for another grace period.
		}
	}

	logger.Log("ERROR", "Ship arrival wait exhausted, parking", map[string]interface{}{
		"ship_symbol":  shipSymbol,
		"action":       "arrival_wait_exhausted",
		"max_attempts": maxAttempts,
	})
	return &ErrArrivalWaitExhausted{ShipSymbol: shipSymbol, Attempts: maxAttempts}
}

// applyArrival transitions ship's local NavStatus via Arrive() when it is
// still IN_TRANSIT. Shared by both the event-received and resync-confirmed
// paths so both converge on identical local-state semantics.
func applyArrival(ship *domainNavigation.Ship) error {
	if ship.NavStatus() == domainNavigation.NavStatusInTransit {
		if err := ship.Arrive(); err != nil {
			return fmt.Errorf("failed to update ship domain state: %w", err)
		}
	}
	return nil
}

package ship

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// Default grace period and wait-budget margin for WaitForShipArrival's
// timeout->resync->park backstop (sp-pafv). ShipEventBus delivers ARRIVED
// events via a non-blocking, non-replaying send (PublishArrived): if
// PublishArrived races ahead of SubscribeArrived, or a subscriber's buffered
// channel is already full, the event is dropped forever with no redelivery.
// Without a bound, a lost/raced event stalls the waiting worker/coordinator
// permanently. These defaults only govern the timeout leg - the happy path
// (event arrives) is unaffected and still returns as soon as the event shows up.
//
// sp-pafv originally shipped this as a FIXED grace*maxAttempts budget
// (~90s total) regardless of the ship's actual route ETA, so every transit
// longer than ~90s aborted early even though it was legitimately still
// IN_TRANSIT with the real arrival simply not due yet (e.g. the real-world
// navigate-TORWIND-F-a36d793d 23-minute DF9E->B10D leg, aborted at ~2
// minutes). sp-ht1f replaces the fixed attempt count with a budget that
// scales with the route's own ETA - see calculateArrivalWaitBudget.
const (
	// DefaultArrivalGracePeriod is the poll cadence once the arrival is due
	// (or its ETA unknown), the delay of the FIRST safety poll (the fast check
	// for an event lost before the subscription existed), and the slack added
	// past the expected arrival before polling. While the arrival is still
	// ahead, polls are ETA-ALIGNED — one sleep to arrival+grace — not fired
	// every grace period (sp-7yej invariant 5: the event path is the norm,
	// the poll the exception; the old fixed 30s tick logged every healthy
	// minutes-long transit as a stream of WARNING "event not received").
	DefaultArrivalGracePeriod = 30 * time.Second

	// DefaultArrivalMarginFactor and DefaultArrivalMinMargin size the safety
	// margin layered on top of a route's own ETA to form the total wait
	// budget: budget = max(eta*DefaultArrivalMarginFactor,
	// eta+DefaultArrivalMinMargin). The margin absorbs scheduler jitter and
	// API latency around the real arrival instant - it is not, by itself,
	// what keeps a healthy transit from being parked early; see
	// arrivalIsPast for the earlier, ETA-driven park on a genuinely lost
	// event.
	DefaultArrivalMarginFactor = 1.25
	DefaultArrivalMinMargin    = 2 * time.Minute
)

// ErrArrivalWaitExhausted is returned when a ship-arrival wait gives up: the
// ARRIVED event never arrived AND repeated resyncs against the ship
// repository kept showing the ship still IN_TRANSIT without a resolving
// signal (either a status change or a provably-past ETA) before the wait
// budget ran out. Callers should park or defer the task rather than retry
// inline.
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
// The overall wait budget scales with waitTimeSeconds (the route's own ETA)
// via calculateArrivalWaitBudget, so a legitimately long transit is not
// parked early just because a fixed number of grace periods elapsed
// (sp-ht1f). Within that budget, a resync that still shows IN_TRANSIT is
// only treated as a genuinely lost event - and parked immediately - once the
// ship's own ArrivalTime has passed; a future ArrivalTime means the wait
// keeps polling.
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
	budget := calculateArrivalWaitBudget(
		time.Duration(waitTimeSeconds)*time.Second,
		DefaultArrivalMarginFactor,
		DefaultArrivalMinMargin,
	)
	return waitForShipArrivalCore(
		ctx, shipRepo, subscriber, ship, playerID, waitTimeSeconds, logger,
		DefaultArrivalGracePeriod, budget,
	)
}

// calculateArrivalWaitBudget returns the total time WaitForShipArrival keeps
// waiting/resyncing for a ship whose route ETA is eta. The budget is always
// at least eta+minMargin, so short or unknown ETAs still get a sane floor,
// and it grows proportionally with eta via marginFactor, so long transits
// get a proportionally larger absolute cushion against scheduler jitter and
// API latency around the real arrival instant (sp-ht1f). A negative eta
// (e.g. from API/clock skew already putting "now" past the reported arrival)
// is clamped to zero before either term is computed.
func calculateArrivalWaitBudget(eta time.Duration, marginFactor float64, minMargin time.Duration) time.Duration {
	if eta < 0 {
		eta = 0
	}
	scaled := time.Duration(float64(eta) * marginFactor)
	floor := eta + minMargin
	if scaled > floor {
		return scaled
	}
	return floor
}

// waitForShipArrivalCore is WaitForShipArrival's configurable core. Tests
// inject a tiny gracePeriod and small budget to exercise the
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
	budget time.Duration,
) error {
	shipSymbol := ship.ShipSymbol()

	arrivedCh := subscriber.SubscribeArrived(shipSymbol)
	defer subscriber.UnsubscribeArrived(shipSymbol, arrivedCh)

	deadline := time.Now().Add(budget)

	logger.Log("INFO", "Waiting for ship arrival event", map[string]interface{}{
		"ship_symbol":      shipSymbol,
		"action":           "wait_arrival_event",
		"expected_seconds": waitTimeSeconds,
		"budget_seconds":   budget.Seconds(),
	})

	// expectedArrival is the best current estimate of when the ship actually
	// lands: seeded from the caller's ETA, refined to the resynced ship's own
	// ArrivalTime after each safety poll. It drives two things (sp-7yej
	// invariant 5 — the event path is the norm, the poll the exception):
	//
	//   - the poll SCHEDULE: the first poll fires after one gracePeriod (the
	//     fast check for an event lost BEFORE this subscription existed), then
	//     each subsequent poll sleeps all the way to expectedArrival plus one
	//     gracePeriod of slack in a single tick instead of waking every 30s of
	//     a minutes-long transit. A healthy 23-minute leg now costs ~2 resyncs
	//     instead of ~46 — and the event, which the select still watches
	//     throughout, interrupts any of these sleeps the instant it lands.
	//   - the poll SEVERITY: a poll while the arrival is not yet due is routine
	//     (INFO); only a poll past the expected arrival means the event is
	//     genuinely overdue and worth a WARNING. The old code logged every
	//     30-second tick of every healthy transit as a WARNING "event not
	//     received", drowning the real lost-event signal in ~46 false alarms
	//     per leg.
	expectedArrival := time.Now().Add(time.Duration(waitTimeSeconds) * time.Second)
	nextTick := gracePeriod

	attempt := 0
	for {
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

		case <-time.After(nextTick):
			attempt++
			// No event yet: resync against the source of truth instead of
			// assuming the event is merely slow - it may have been dropped by
			// ShipEventBus's non-blocking, non-replaying send (lost if
			// PublishArrived raced ahead of SubscribeArrived). Severity tracks
			// whether the arrival is actually due (see expectedArrival above).
			dueIn := time.Until(expectedArrival)
			if dueIn > 0 {
				logger.Log("INFO", "Arrival not due yet - safety resync while in transit", map[string]interface{}{
					"ship_symbol":    shipSymbol,
					"action":         "arrival_wait_resync",
					"attempt":        attempt,
					"due_in_seconds": int(dueIn.Seconds()),
				})
			} else {
				logger.Log("WARNING", "Ship arrival event overdue - resyncing", map[string]interface{}{
					"ship_symbol":     shipSymbol,
					"action":          "arrival_wait_resync",
					"attempt":         attempt,
					"overdue_seconds": int((-dueIn).Seconds()),
				})
			}
			fresh, err := shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
			switch {
			case err != nil:
				logger.Log("WARNING", "Arrival resync lookup failed, will retry", map[string]interface{}{
					"ship_symbol": shipSymbol,
					"attempt":     attempt,
					"error":       err.Error(),
				})

			case fresh.NavStatus() != domainNavigation.NavStatusInTransit:
				logger.Log("INFO", "Arrival resync confirmed ship left transit", map[string]interface{}{
					"ship_symbol": shipSymbol,
					"action":      "arrival_wait_resync_confirmed",
					"status":      string(fresh.NavStatus()),
				})
				return applyArrival(ship)

			case arrivalIsPast(fresh, time.Now()):
				// Still IN_TRANSIT but the ship's own ETA has already passed:
				// the genuine lost/raced-event case sp-pafv targeted. Park
				// now instead of waiting out the rest of the budget -
				// waiting longer cannot recover an event that is already
				// gone (sp-ht1f).
				logger.Log("ERROR", "Arrival resync still IN_TRANSIT past its own ETA - lost event, parking", map[string]interface{}{
					"ship_symbol": shipSymbol,
					"action":      "arrival_wait_past_eta_parked",
					"attempt":     attempt,
				})
				return &ErrArrivalWaitExhausted{ShipSymbol: shipSymbol, Attempts: attempt}

			default:
				// Still IN_TRANSIT with a future (or unknown) ETA: the ship
				// is healthy and legitimately still travelling. Keep
				// waiting rather than parking - this is the sp-ht1f fix.
				// sp-pafv's fixed grace*maxAttempts budget parked here
				// regardless of how far away the real arrival was. The
				// resynced ship's own ArrivalTime is the authoritative ETA,
				// so it refines the poll schedule below.
				if arrival := fresh.ArrivalTime(); arrival != nil {
					expectedArrival = *arrival
				}
				logger.Log("INFO", "Arrival resync still shows IN_TRANSIT with a future ETA, continuing to wait", map[string]interface{}{
					"ship_symbol": shipSymbol,
					"action":      "arrival_wait_resync_still_future",
					"attempt":     attempt,
				})
			}

			if !time.Now().Before(deadline) {
				logger.Log("ERROR", "Ship arrival wait budget exhausted, parking", map[string]interface{}{
					"ship_symbol":    shipSymbol,
					"action":         "arrival_wait_exhausted",
					"attempts":       attempt,
					"budget_seconds": budget.Seconds(),
				})
				return &ErrArrivalWaitExhausted{ShipSymbol: shipSymbol, Attempts: attempt}
			}

			// ETA-aligned schedule (sp-7yej invariant 5): while the arrival is
			// still ahead, sleep to just past it in ONE tick — the event wins
			// the select the moment it lands, so a long sleep never delays the
			// happy path. Once at/past the ETA (or when it is unknown), poll at
			// the gracePeriod cadence. Capped so a tick never sleeps far past
			// the budget deadline — the check above must get its turn.
			nextTick = gracePeriod
			if remaining := time.Until(expectedArrival) + gracePeriod; remaining > nextTick {
				nextTick = remaining
			}
			if untilDeadline := time.Until(deadline) + gracePeriod; nextTick > untilDeadline {
				nextTick = untilDeadline
			}
		}
	}
}

// arrivalIsPast reports whether fresh's own ArrivalTime is already behind
// now while fresh is still IN_TRANSIT - the signature of a genuinely
// lost/raced ARRIVED event (sp-pafv's original target) as opposed to a
// healthy transit that simply is not finished yet. An unknown ArrivalTime
// (nil) cannot be proven past, so it is treated as "not yet due" and the
// wait falls back to the overall budget deadline instead (sp-ht1f).
func arrivalIsPast(fresh *domainNavigation.Ship, now time.Time) bool {
	arrival := fresh.ArrivalTime()
	return arrival != nil && now.After(*arrival)
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

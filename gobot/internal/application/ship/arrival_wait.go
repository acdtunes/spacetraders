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
// timeout->resync->park backstop. ShipEventBus delivers ARRIVED events via a
// non-blocking, non-replaying send (PublishArrived): if PublishArrived races
// ahead of SubscribeArrived, or a subscriber's buffered channel is already
// full, the event is dropped forever with no redelivery. Without a bound, a
// lost/raced event stalls the waiting worker/coordinator permanently. These
// defaults only govern the timeout leg - the happy path (event arrives) is
// unaffected and still returns as soon as the event shows up.
//
// The wait budget scales with the route's own ETA rather than a fixed
// attempt count, so a legitimately long transit is never aborted early just
// because a fixed number of grace periods elapsed - see
// calculateArrivalWaitBudget.
const (
	// DefaultArrivalGracePeriod is the poll cadence once the arrival is due
	// (or its ETA unknown), the delay of the FIRST safety poll (the fast check
	// for an event lost before the subscription existed), and the slack added
	// past the expected arrival before polling. While the arrival is still
	// ahead, polls are ETA-ALIGNED — aimed at arrival+grace, capped by
	// maxArrivalRecheckGracePeriods — not fired every grace period: the event
	// path is the norm, the poll the exception.
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

// requiredPastETAObservationsBeforePark is Fix B's short-leg debounce depth: the
// number of CONSECUTIVE local-DB polls that must show the ship still IN_TRANSIT past
// its own ETA before the wait treats the ARRIVED event as lost and enters the park
// path. Two means a single stale FIRST poll on a short leg — the DB row not yet
// caught up to the async, best-effort IN_TRANSIT->IN_ORBIT transition
// (ShipStateScheduler.handleArrival, non-replaying) — gets exactly one more local
// re-read (one gracePeriod later) for that transition to commit before any park
// decision is taken. It is a LOCAL DB re-read, never an API call, so tightening it
// costs zero API budget.
const requiredPastETAObservationsBeforePark = 2

// maxArrivalRecheckGracePeriods bounds how long the wait may go without looking at
// the ship, as a multiple of the gracePeriod (2 minutes at the production default).
// The poll schedule aims at the ship's own ETA, which on a long leg is a single
// sleep spanning the whole remaining transit — so an ARRIVED event lost mid-flight
// left the wait blind until that one tick fired, even though the hull was already
// parked at its destination and readable from its own row. Arrival is knowable at
// any moment, so the re-check cannot be starved by a distant ETA. The wait stays
// event-driven: the event still wins the select the instant it lands, and the
// re-check is a LOCAL DB read, never an API call.
const maxArrivalRecheckGracePeriods = 4

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
// codebase.
//
// The overall wait budget scales with waitTimeSeconds (the route's own ETA)
// via calculateArrivalWaitBudget, so a legitimately long transit is not
// parked early just because a fixed number of grace periods elapsed. Within
// that budget, a resync that still shows IN_TRANSIT is only treated as a
// genuinely lost event - and parked immediately - once the ship's own
// ArrivalTime has passed; a future ArrivalTime means the wait keeps polling.
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
	return arrivalWait{
		shipRepo:        shipRepo,
		subscriber:      subscriber,
		ship:            ship,
		playerID:        playerID,
		waitTimeSeconds: waitTimeSeconds,
		logger:          logger,
		gracePeriod:     DefaultArrivalGracePeriod,
		budget:          budget,
	}.run(ctx)
}

// calculateArrivalWaitBudget returns the total time WaitForShipArrival keeps
// waiting/resyncing for a ship whose route ETA is eta. The budget is always
// at least eta+minMargin, so short or unknown ETAs still get a sane floor,
// and it grows proportionally with eta via marginFactor, so long transits
// get a proportionally larger absolute cushion against scheduler jitter and
// API latency around the real arrival instant. A negative eta (e.g. from
// API/clock skew already putting "now" past the reported arrival) is clamped
// to zero before either term is computed.
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

// arrivalWait is WaitForShipArrival's configurable core. Tests
// inject a tiny gracePeriod and small budget to exercise the
// timeout->resync->park backstop without slowing down the suite; production
// always goes through WaitForShipArrival's fixed defaults above.
//
// A would-be park requires two consecutive past-ETA local-DB observations
// (Fix B short-leg debounce) and is then re-confirmed ONCE against the
// authoritative live API (Fix A) before parking - the ship's local row can lag
// the async IN_TRANSIT->IN_ORBIT transition on a short leg, so a DB-only park
// is a false positive. The happy path (ARRIVED event, or a DB poll that
// already shows the hull left transit) makes ZERO API calls.
type arrivalWait struct {
	shipRepo        domainNavigation.ShipQueryRepository
	subscriber      domainNavigation.ShipEventSubscriber
	ship            *domainNavigation.Ship
	playerID        shared.PlayerID
	waitTimeSeconds int
	logger          common.ContainerLogger
	gracePeriod     time.Duration
	budget          time.Duration
}

func (w arrivalWait) run(ctx context.Context) error {
	shipSymbol := w.ship.ShipSymbol()

	arrivedCh := w.subscriber.SubscribeArrived(shipSymbol)
	defer w.subscriber.UnsubscribeArrived(shipSymbol, arrivedCh)

	deadline := time.Now().Add(w.budget)

	w.logger.Log("INFO", "Waiting for ship arrival event", map[string]interface{}{
		"ship_symbol":      shipSymbol,
		"action":           "wait_arrival_event",
		"expected_seconds": w.waitTimeSeconds,
		"budget_seconds":   w.budget.Seconds(),
	})

	// expectedArrival is the best current estimate of when the ship actually
	// lands: seeded from the caller's ETA, refined to the resynced ship's own
	// ArrivalTime after each safety poll. It drives two things — the event
	// path is the norm, the poll the exception:
	//
	//   - the poll SCHEDULE: the first poll fires after one gracePeriod (the
	//     fast check for an event lost BEFORE this subscription existed), then
	//     each subsequent poll aims at expectedArrival plus one gracePeriod of
	//     slack rather than waking on a fixed cadence — capped at
	//     maxArrivalRecheckGracePeriods so no single sleep can leave an
	//     already-arrived hull unobserved for a whole transit. The event, which
	//     the select still watches throughout, interrupts any of these sleeps
	//     the instant it lands.
	//   - the poll SEVERITY: a poll while the arrival is not yet due is routine
	//     (INFO); only a poll past the expected arrival means the event is
	//     genuinely overdue and worth a WARNING, so the real lost-event signal
	//     is never drowned in routine-poll noise.
	expectedArrival := time.Now().Add(time.Duration(w.waitTimeSeconds) * time.Second)
	nextTick := w.gracePeriod

	attempt := 0
	// pastETAObservations counts CONSECUTIVE past-ETA DB polls for Fix B's
	// short-leg debounce (see requiredPastETAObservationsBeforePark). It is reset
	// by any poll that does NOT observe a still-IN_TRANSIT-past-ETA ship, so the
	// two observations that trigger a park must be back-to-back.
	pastETAObservations := 0
	for {
		select {
		case event := <-arrivedCh:
			// Ship arrived - update domain state to match. Domain state is
			// already updated by ShipStateScheduler; just sync our local
			// copy to reflect the new state (unchanged happy path).
			w.logger.Log("INFO", "Ship arrival event received", map[string]interface{}{
				"ship_symbol": shipSymbol,
				"action":      "arrival_event_received",
				"location":    event.Location,
				"status":      string(event.Status),
			})
			return applyArrival(w.ship)

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
			logArrivalResync(w.logger, shipSymbol, attempt, dueIn)
			fresh, err := w.shipRepo.FindBySymbol(ctx, shipSymbol, w.playerID)
			switch {
			case err != nil:
				pastETAObservations = 0 // Fix B: a failed poll breaks the past-ETA streak.
				w.logger.Log("WARNING", "Arrival resync lookup failed, will retry", map[string]interface{}{
					"ship_symbol": shipSymbol,
					"attempt":     attempt,
					"error":       err.Error(),
				})

			case fresh.NavStatus() != domainNavigation.NavStatusInTransit:
				pastETAObservations = 0 // Fix B: not IN_TRANSIT breaks the past-ETA streak.
				// fresh is no longer IN_TRANSIT — normally the arrival. But a resync can
				// also return a STALE PRE-DEPARTURE snapshot: a repo/nav-cache read that
				// has not yet caught up to this leg's departure still shows the hull
				// DOCKED/IN_ORBIT at the ORIGIN. Confirming off that snapshot would
				// report the hull at its destination while it is really still at the
				// start of the leg.
				//
				// At/after the scheduled arrival (dueIn<=0) a not-in-transit read is
				// overwhelmingly the genuine arrival, so the on-time path confirms on
				// the status change alone. BEFORE it, confirm only when the
				// AUTHORITATIVE position proves the hull actually reached the
				// destination it is heading to (a genuine early arrival, which must
				// still pass) rather than still sitting at the origin. A stale origin
				// snapshot falls through to keep polling until the cache catches up
				// (then this same branch confirms at the destination, or the
				// IN_TRANSIT branches take over) or the budget deadline parks it.
				if dueIn <= 0 || arrivedAtDestination(fresh, w.ship) {
					w.logger.Log("INFO", "Arrival resync confirmed ship left transit", map[string]interface{}{
						"ship_symbol": shipSymbol,
						"action":      "arrival_wait_resync_confirmed",
						"status":      string(fresh.NavStatus()),
					})
					return applyArrival(w.ship)
				}
				w.logger.Log("WARNING", "Arrival resync not-in-transit before ETA but hull still at origin - stale pre-departure snapshot, continuing to wait", map[string]interface{}{
					"ship_symbol":    shipSymbol,
					"action":         "arrival_wait_resync_stale_predeparture",
					"attempt":        attempt,
					"due_in_seconds": int(dueIn.Seconds()),
					"fresh_status":   string(fresh.NavStatus()),
					"fresh_location": shipLocationSymbol(fresh),
					"destination":    shipLocationSymbol(w.ship),
				})

			case arrivalIsPast(fresh, time.Now()):
				// The LOCAL DB poll shows the ship still IN_TRANSIT with its own ETA
				// already past. On a SHORT leg (ETA <= ~gracePeriod) that can be a
				// FALSE positive: the hull has physically arrived, but the async,
				// best-effort local IN_TRANSIT->IN_ORBIT transition
				// (ShipStateScheduler.handleArrival, non-replaying) has not committed
				// to the DB row yet, so the stale row still reads IN_TRANSIT-past-ETA
				// on the first poll. A DB-only park here fails the route segment and
				// crash-loops the container — hence Fix B's debounce below.

				// Fix B (DB-only, no API): require two CONSECUTIVE past-ETA
				// observations before parking, so a single stale first poll on a
				// short leg gets one more LOCAL DB re-read (one gracePeriod later)
				// for the async transition to land. Very often the second poll then
				// shows the hull arrived and resolves via the not-in-transit branch
				// above — with ZERO API calls.
				pastETAObservations++
				if pastETAObservations < requiredPastETAObservationsBeforePark {
					w.logger.Log("INFO", "Arrival past its own ETA but still IN_TRANSIT - re-reading the local row once before parking (short-leg stale-transition debounce)", map[string]interface{}{
						"ship_symbol":  shipSymbol,
						"action":       "arrival_wait_past_eta_debounce",
						"attempt":      attempt,
						"observations": pastETAObservations,
					})
					break // exit the switch → reschedule + re-poll; do NOT park, no API.
				}

				return w.reconfirmBeforePark(ctx, shipSymbol, attempt)

			default:
				pastETAObservations = 0 // Fix B: a future-ETA poll breaks the past-ETA streak.
				// Still IN_TRANSIT with a future (or unknown) ETA: the ship is
				// healthy and legitimately still travelling. Keep waiting rather
				// than parking. The resynced ship's own ArrivalTime is the
				// authoritative ETA, so it refines the poll schedule below.
				if arrival := fresh.ArrivalTime(); arrival != nil {
					expectedArrival = *arrival
				}
				w.logger.Log("INFO", "Arrival resync still shows IN_TRANSIT with a future ETA, continuing to wait", map[string]interface{}{
					"ship_symbol": shipSymbol,
					"action":      "arrival_wait_resync_still_future",
					"attempt":     attempt,
				})
			}

			if !time.Now().Before(deadline) {
				w.logger.Log("ERROR", "Ship arrival wait budget exhausted, parking", map[string]interface{}{
					"ship_symbol":    shipSymbol,
					"action":         "arrival_wait_exhausted",
					"attempts":       attempt,
					"budget_seconds": w.budget.Seconds(),
				})
				return &ErrArrivalWaitExhausted{ShipSymbol: shipSymbol, Attempts: attempt}
			}

			nextTick = nextArrivalTick(w.gracePeriod, expectedArrival, deadline)
		}
	}
}

// logArrivalResync logs a resync at the severity the schedule earns: only an overdue
// poll is a WARNING, so the real lost-event signal is not drowned in routine noise.
func logArrivalResync(logger common.ContainerLogger, shipSymbol string, attempt int, dueIn time.Duration) {
	if dueIn > 0 {
		logger.Log("INFO", "Arrival not due yet - safety resync while in transit", map[string]interface{}{
			"ship_symbol":    shipSymbol,
			"action":         "arrival_wait_resync",
			"attempt":        attempt,
			"due_in_seconds": int(dueIn.Seconds()),
		})
		return
	}
	logger.Log("WARNING", "Ship arrival event overdue - resyncing", map[string]interface{}{
		"ship_symbol":     shipSymbol,
		"action":          "arrival_wait_resync",
		"attempt":         attempt,
		"overdue_seconds": int((-dueIn).Seconds()),
	})
}

// reconfirmBeforePark re-confirms a would-be park ONCE against the live API (the DB
// LAGS the arrival transition). The only API call in the wait; errors fall back to park.
func (w arrivalWait) reconfirmBeforePark(ctx context.Context, shipSymbol string, attempt int) error {
	leftTransit, apiErr := liveAPIShowsLeftTransit(ctx, w.shipRepo, shipSymbol, w.playerID)
	if apiErr != nil {
		w.logger.Log("WARNING", "Arrival live-API re-confirm failed - falling back to DB-only park", map[string]interface{}{
			"ship_symbol": shipSymbol,
			"action":      "arrival_wait_live_reconfirm_error",
			"attempt":     attempt,
			"error":       apiErr.Error(),
		})
		return parkLostEvent(w.logger, shipSymbol, attempt)
	}
	if leftTransit {
		w.logger.Log("INFO", "Arrival live-API re-confirm shows ship left transit - stale DB row, applying arrival instead of parking", map[string]interface{}{
			"ship_symbol": shipSymbol,
			"action":      "arrival_wait_live_reconfirm_arrived",
			"attempt":     attempt,
		})
		return applyArrival(w.ship)
	}
	// The live API AGREES the hull is genuinely still IN_TRANSIT: a real lost
	// event / stuck hull, not a stale-row race.
	return parkLostEvent(w.logger, shipSymbol, attempt)
}

// nextArrivalTick aims the next poll just past the expected arrival, capped by the
// re-check ceiling and the budget deadline so a tick never oversleeps either.
func nextArrivalTick(gracePeriod time.Duration, expectedArrival, deadline time.Time) time.Duration {
	tick := gracePeriod
	if remaining := time.Until(expectedArrival) + gracePeriod; remaining > tick {
		tick = remaining
	}
	if ceiling := maxArrivalRecheckGracePeriods * gracePeriod; tick > ceiling {
		tick = ceiling
	}
	if untilDeadline := time.Until(deadline) + gracePeriod; tick > untilDeadline {
		tick = untilDeadline
	}
	return tick
}

// parkLostEvent logs the genuine lost/stuck-event park and returns the typed
// exhaustion error. Factored out so both park paths — the
// debounce-satisfied-but-live-confirmed-stuck path and the live-API error
// fallback — emit one identical ERROR log and error.
func parkLostEvent(logger common.ContainerLogger, shipSymbol string, attempt int) error {
	logger.Log("ERROR", "Arrival resync still IN_TRANSIT past its own ETA - lost event, parking", map[string]interface{}{
		"ship_symbol": shipSymbol,
		"action":      "arrival_wait_past_eta_parked",
		"attempt":     attempt,
	})
	return &ErrArrivalWaitExhausted{ShipSymbol: shipSymbol, Attempts: attempt}
}

// liveAPIShowsLeftTransit re-confirms a would-be park against the AUTHORITATIVE
// live API (Fix A). The local DB is the source of truth for ship state but LAGS the
// async, best-effort IN_TRANSIT->IN_ORBIT transition; a live GetShipData reflects
// the hull's real status immediately. Returns true when the API shows the hull is
// no longer IN_TRANSIT (arrived / in-orbit / docked) — i.e. it physically arrived
// and the local row is merely stale, so the caller must apply the arrival rather
// than false-park. Returns (false, err) on any API failure so the caller falls back
// to today's DB-only park (never worse than status quo). This is the ONLY API call
// in the whole wait and fires only on the rare park path; the happy path makes ZERO
// API calls.
func liveAPIShowsLeftTransit(ctx context.Context, shipRepo domainNavigation.ShipQueryRepository, shipSymbol string, playerID shared.PlayerID) (bool, error) {
	data, err := shipRepo.GetShipData(ctx, shipSymbol, playerID)
	if err != nil {
		return false, err
	}
	return domainNavigation.NavStatus(data.NavStatus) != domainNavigation.NavStatusInTransit, nil
}

// arrivalIsPast reports whether fresh's own ArrivalTime is already behind
// now while fresh is still IN_TRANSIT - the signature of a genuinely
// lost/raced ARRIVED event, as opposed to a healthy transit that simply is
// not finished yet. An unknown ArrivalTime (nil) cannot be proven past, so
// it is treated as "not yet due" and the wait falls back to the overall
// budget deadline instead.
func arrivalIsPast(fresh *domainNavigation.Ship, now time.Time) bool {
	arrival := fresh.ArrivalTime()
	return arrival != nil && now.After(*arrival)
}

// arrivedAtDestination reports whether the resynced snapshot fresh proves the
// hull has actually reached the destination the in-transit ship is heading to,
// as opposed to a stale pre-departure snapshot still at the origin.
// While IN_TRANSIT, ship.CurrentLocation() is the destination (StartTransit sets
// it on departure), and a genuine arrival leaves the hull there; a pre-departure
// snapshot is still at the origin, which — because a leg's origin and
// destination are always distinct (StartTransit rejects a same-waypoint hop) —
// carries a different symbol. A nil location on either side cannot be proven
// arrived, so it returns false and the wait falls back to the ETA/budget rather
// than confirm on missing data.
func arrivedAtDestination(fresh, ship *domainNavigation.Ship) bool {
	dest := ship.CurrentLocation()
	at := fresh.CurrentLocation()
	if dest == nil || at == nil {
		return false
	}
	return at.Symbol == dest.Symbol
}

// shipLocationSymbol is a nil-safe accessor for a ship's current-location
// symbol, used only for diagnostics in the stale-snapshot log line.
func shipLocationSymbol(ship *domainNavigation.Ship) string {
	if loc := ship.CurrentLocation(); loc != nil {
		return loc.Symbol
	}
	return ""
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

package commands

// run_tour_coordinator_relocation_offer.go — FIRST REFUSAL at the tour boundary (sp-e8d92).
//
// THE PROBLEM, measured. 40 trade hulls occupy 23 of 373 priced, tradeable systems — 6% of the
// reachable market, with 7 stacked in one system. A tour is planned from the hull's CURRENT position at
// cap-2, so it ends roughly where it began: the tradeable envelope travels with the hull and never
// leaves its neighbourhood. The opportunity relocator is the only mechanism that moves a hull to new
// ground, and it works — 24 decisions, one at 9.67M cr NPV. It is simply outnumbered: hulls are busy
// 97.4% of the time (101 inter-tour gaps, median 224s), so the relocator reports mid_tour=38..40 and
// evaluates 1-2 hulls per tick, and the instant one is free its tour re-plans it locally.
//
// WHAT THIS IS NOT. It is not a faster tick — at 120s against a 224s window the relocator already sees
// essentially every free hull, so sampling was never the constraint. And it is not an event bus: no
// coordinator here wakes on an event, and the PUBLISHER is the hazard — a tour ends normally, on error,
// interrupted, or via a restart sweep, and a path that forgot to fire would silently and permanently
// stop offering that hull. That fail-silent class has been removed four times over tonight.
//
// WHAT IT IS. Durable state in the tour's OWN container config, written at the boundary and re-derived
// by both sides every tick (RULINGS #2):
//
//	relocation_offer_until   — this hull is available for relocation until T.
//	relocation_offer_backoff — do not offer it again before T.
//
// The relocator reads the offer in the SAME container query it already runs to decide OnTour, so the
// read costs nothing new and makes no API call. The tour waits while its own offer stands.
//
// EVERY FAILURE DEGRADES TO TODAY. An unwritten offer, an unreadable one, a relocator that is down, a
// window that lapses — each leaves the tour planning locally exactly as it does now. The one thing that
// would be WORSE than never spreading is a hull held out of touring forever, which is why the expiry
// below is not optional and is checked by the waiter rather than trusted from the writer.

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

const (
	// defaultRelocationOfferWindowSeconds is how long a tour waits for the relocator: 150s.
	//
	// Bounded from BELOW by the relocator's own cadence — an offer shorter than one tick can open and
	// expire between two observations and never be seen, which would make the whole feature inert while
	// looking wired. Bounded from ABOVE by the measured 224s median inter-tour gap: the hull was already
	// going to spend that long being re-planned, so a window under it costs less than the planning it
	// displaces.
	defaultRelocationOfferWindowSeconds = 150

	// defaultRelocationOfferMinHullsInSystem is the HERD GATE: a hull is offered only when its system
	// already holds at least this many trade hulls (2).
	//
	// This is the answer to "do not regress earning". An offered hull is not trading, and 40 hulls each
	// paying a window every tour cycle is real revenue — so the offer is spent only on the population
	// whose relocation actually raises the success metric. A hull ALONE in its system is already
	// occupying a distinct system; stalling it buys no spread at all. A hull in a stack of 7 is exactly
	// what the metric is counting.
	//
	// It self-limits: as the fleet spreads, fewer systems hold a stack, so fewer hulls qualify and the
	// earning cost falls as the feature succeeds.
	defaultRelocationOfferMinHullsInSystem = 2

	// defaultRelocationOfferBackoffMinutes is how long a hull whose offer LAPSED unclaimed waits before
	// being offered again: 30 minutes, roughly one tour cycle.
	//
	// Without it, a herded hull the relocator cannot move — nowhere clears the NPV floor — would be
	// offered at every boundary forever, paying a window each time for a relocation that never comes.
	// A timer rather than a permanent exclusion, because the ground genuinely changes: the sensing surge
	// prices new systems continuously, and today's "nowhere better" is not tomorrow's.
	defaultRelocationOfferBackoffMinutes = 30

	// relocationOfferPollSeconds is how often the waiting tour re-reads its hull's position, so a taken
	// offer resumes IMMEDIATELY at the new ground instead of waiting out the rest of the window. A
	// durable-state read (the ships table), never an API call — the account is currently pinned at its
	// request ceiling and the offer path must add no load to it.
	relocationOfferPollSeconds = 10
)

// resolveRelocationOfferWindow applies the 0/absent -> documented default rule (RULINGS #5).
func resolveRelocationOfferWindow(configuredSeconds int) time.Duration {
	if configuredSeconds <= 0 {
		return time.Duration(defaultRelocationOfferWindowSeconds) * time.Second
	}
	return time.Duration(configuredSeconds) * time.Second
}

// resolveRelocationOfferMinHulls applies the 0/absent -> 2 rule to the herd gate.
func resolveRelocationOfferMinHulls(configured int) int {
	if configured <= 0 {
		return defaultRelocationOfferMinHullsInSystem
	}
	return configured
}

// resolveRelocationOfferBackoff applies the 0/absent -> 30 minutes rule.
func resolveRelocationOfferBackoff(configuredMinutes int) time.Duration {
	if configuredMinutes <= 0 {
		return time.Duration(defaultRelocationOfferBackoffMinutes) * time.Minute
	}
	return time.Duration(configuredMinutes) * time.Minute
}

// shouldOfferForRelocation decides whether THIS hull, at THIS boundary, is worth stalling for the
// relocator. Pure: it judges facts the caller already read.
//
// hullsInSystem is the count of active trade hulls in the hull's current system, INCLUDING itself. A
// zero means the fleet snapshot was unreadable, which fails CLOSED — an unreadable count is not evidence
// of a stack, and the cost of guessing wrong is a hull that stops trading for no reason.
func shouldOfferForRelocation(hullsInSystem, minHullsInSystem int, now, backoffUntil time.Time) bool {
	if hullsInSystem < minHullsInSystem {
		return false // alone (or unreadable): already spreading, or unprovable
	}
	if !backoffUntil.IsZero() && now.Before(backoffUntil) {
		return false // its last offer lapsed unclaimed; do not pay another window yet
	}
	return true
}

// relocationOfferStands reports whether an offer is live at now. THE EXPIRY IS ENFORCED HERE, by the
// waiter, rather than trusted from whoever wrote the key: a hull held out of touring forever is a
// stranded trade hull, which is strictly worse than a hull that never spread. An absent (zero) deadline
// never stands, so an unwritten or unreadable offer degrades to exactly today's behaviour.
func relocationOfferStands(offerUntil, now time.Time) bool {
	if offerUntil.IsZero() {
		return false
	}
	return now.Before(offerUntil)
}

// RelocationOffer is the durable offer as the tour writes it. Both deadlines are ABSOLUTE instants, not
// durations: a restart must not silently extend a hold, and a relative value would do exactly that.
type RelocationOffer struct {
	// OfferedUntil is when the offer lapses. Zero CLEARS it.
	OfferedUntil time.Time
	// BackoffUntil is when this hull may be offered again after a lapse. Zero leaves it unset.
	BackoffUntil time.Time
}

// RelocationOfferPersister durably records the offer in the tour's own container config — the same map
// the relocator already reads to decide OnTour, so the read side costs no extra query and no API call.
//
// A SEPARATE port from RepositionStatePersister on purpose: that one is typed to a reposition episode
// and has one job. A returned error is advisory (the offer is an optimisation, never a movement guard),
// so the caller logs it and keeps touring — which degrades to exactly today's behaviour.
type RelocationOfferPersister interface {
	PersistRelocationOffer(ctx context.Context, containerID string, playerID int, offer RelocationOffer) error
}

// SetRelocationOfferPersister wires the durable offer store. Optional and nil-safe: unset, the tour never
// offers and the fleet behaves exactly as it does today (fail-open).
func (h *RunTourCoordinatorHandler) SetRelocationOfferPersister(p RelocationOfferPersister) {
	h.offerPersister = p
}

// maybeOfferForRelocation writes the offer at a productive tour's boundary, and returns the deadline it
// wrote (zero when it did not offer).
//
// THE GATE IS THE EARNING GUARD. An offered hull is not trading, so the offer is spent only where a
// relocation would plausibly raise the metric: a hull SHARING its system. A hull alone in its system is
// already occupying a distinct system, so stalling it would buy nothing and cost a window.
//
// Fail-open at every step. No persister wired, no container id, an unreadable fleet, a failed write —
// each returns "no offer" and the tour plans locally exactly as it does today. That is the whole safety
// argument for this feature: the failure mode is today's behaviour, never a stalled hull.
func (h *RunTourCoordinatorHandler) maybeOfferForRelocation(ctx context.Context, cmd *RunTourCoordinatorCommand, currentSystem string) time.Time {
	if h.offerPersister == nil || cmd.ContainerID == "" || currentSystem == "" {
		return time.Time{}
	}
	now := h.clock.Now()
	counts, ok := h.activeTradeHullsBySystem(ctx, cmd.PlayerID)
	if !ok {
		return time.Time{} // unreadable fleet: not evidence of a stack, so keep touring (fail closed)
	}
	if !shouldOfferForRelocation(counts[currentSystem], resolveRelocationOfferMinHulls(cmd.RelocationOfferMinHulls), now, cmd.RelocationOfferBackoffUntil) {
		return time.Time{}
	}
	deadline := now.Add(resolveRelocationOfferWindow(cmd.RelocationOfferWindowSeconds))
	logger := common.LoggerFromContext(ctx)
	if err := h.offerPersister.PersistRelocationOffer(ctx, cmd.ContainerID, cmd.PlayerID, RelocationOffer{OfferedUntil: deadline}); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Could not persist %s's relocation offer, touring on without offering (fail-open): %v", cmd.ShipSymbol, err), map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol, "container_id": cmd.ContainerID, "error": err.Error(),
		})
		return time.Time{}
	}
	logger.Log("INFO", fmt.Sprintf("Relocation offer: %s shares %s with %d trade hulls - offering it to the relocator until %s before planning here again (sp-e8d92 first refusal)", cmd.ShipSymbol, currentSystem, counts[currentSystem], deadline.Format(time.RFC3339)), map[string]interface{}{
		"ship_symbol": cmd.ShipSymbol, "system": currentSystem, "hulls_in_system": counts[currentSystem],
		"offered_until": deadline.Format(time.RFC3339), "trigger": "relocation_offer",
	})
	return deadline
}

// holdForRelocationOffer waits out a standing offer before the tour plans again, and reports whether the
// relocator TOOK the hull.
//
// It polls the hull's position rather than sleeping the whole window, so a taken offer resumes
// IMMEDIATELY — the tour then re-plans at the NEW ground, which is the entire point of the exercise. A
// durable-state read per poll (the ships table), never an API call: the account is pinned at its request
// ceiling and this path must add nothing to it.
//
// THE EXPIRY IS ENFORCED HERE. Whatever the config says, this loop ends at the deadline, so the worst
// case is one bounded window of lost trading rather than a hull held out of touring forever. A cancelled
// context ends it immediately — a stop must never be delayed by an optimisation.
func (h *RunTourCoordinatorHandler) holdForRelocationOffer(ctx context.Context, cmd *RunTourCoordinatorCommand, offerUntil time.Time, fromSystem string) bool {
	if !relocationOfferStands(offerUntil, h.clock.Now()) {
		return false
	}
	logger := common.LoggerFromContext(ctx)
	poll := h.relocationOfferPoll()
	for relocationOfferStands(offerUntil, h.clock.Now()) {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(poll):
		}
		ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
		if err != nil || ship == nil || ship.CurrentLocation() == nil {
			continue // an unreadable position is not evidence the hull moved; keep waiting out the window
		}
		if ship.CurrentLocation().SystemSymbol != fromSystem {
			logger.Log("INFO", fmt.Sprintf("Relocation offer TAKEN: %s was relocated from %s to %s during its offer window - re-planning on the new ground", cmd.ShipSymbol, fromSystem, ship.CurrentLocation().SystemSymbol), map[string]interface{}{
				"ship_symbol": cmd.ShipSymbol, "from_system": fromSystem, "to_system": ship.CurrentLocation().SystemSymbol,
				"trigger": "relocation_offer", "outcome": "taken",
			})
			h.clearRelocationOffer(ctx, cmd, time.Time{})
			return true
		}
	}
	// LAPSED. Back off before offering this hull again: a herded hull the relocator cannot move would
	// otherwise pay a window at every boundary forever, for a relocation that never comes.
	backoff := h.clock.Now().Add(resolveRelocationOfferBackoff(cmd.RelocationOfferBackoffMinutes))
	logger.Log("INFO", fmt.Sprintf("Relocation offer LAPSED: %s was not relocated within its window - touring %s again, and not re-offering before %s", cmd.ShipSymbol, fromSystem, backoff.Format(time.RFC3339)), map[string]interface{}{
		"ship_symbol": cmd.ShipSymbol, "system": fromSystem, "backoff_until": backoff.Format(time.RFC3339),
		"trigger": "relocation_offer", "outcome": "lapsed",
	})
	h.clearRelocationOffer(ctx, cmd, backoff)
	return false
}

// clearRelocationOffer removes the standing offer (and optionally stamps the backoff). Best-effort: the
// waiter already enforces the deadline from its own clock, so a failed clear cannot strand the hull — the
// next boundary simply re-reads an expired key, which does not stand.
func (h *RunTourCoordinatorHandler) clearRelocationOffer(ctx context.Context, cmd *RunTourCoordinatorCommand, backoffUntil time.Time) {
	if h.offerPersister == nil || cmd.ContainerID == "" {
		return
	}
	if err := h.offerPersister.PersistRelocationOffer(ctx, cmd.ContainerID, cmd.PlayerID, RelocationOffer{BackoffUntil: backoffUntil}); err != nil {
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf("Could not clear %s's relocation offer (the waiter enforces the deadline regardless, so the hull is not held): %v", cmd.ShipSymbol, err), map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol, "container_id": cmd.ContainerID, "error": err.Error(),
		})
	}
	cmd.RelocationOfferBackoffUntil = backoffUntil
}

// relocationOfferPoll is how long the hold waits between position re-reads.
//
// A FIELD rather than the bare constant purely so a test can drive the loop at millisecond speed: the
// production interval is 10s, and a test that actually waited it would either take minutes or be
// rewritten into something that no longer exercises the real loop. The waiting is done with a select on
// ctx.Done() rather than clock.Sleep so a stop is honoured IMMEDIATELY — a shutdown must never be held
// up by an optimisation, and a mock clock's instant Sleep would have hidden that requirement rather
// than met it.
func (h *RunTourCoordinatorHandler) relocationOfferPoll() time.Duration {
	if h.offerPollInterval > 0 {
		return h.offerPollInterval
	}
	return time.Duration(relocationOfferPollSeconds) * time.Second
}

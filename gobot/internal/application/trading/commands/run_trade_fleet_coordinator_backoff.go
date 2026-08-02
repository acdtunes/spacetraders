package commands

import (
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// cooldownRemaining returns how much of the per-hull cooldown is still pending for an
// idle trade hull (sp-1278), or 0 when it is clear to relaunch. The cooldown is
// measured from the hull's last release time — the ContainerRunner stamps released_at
// when a tour terminates (ForceRelease -> assignment.Released), and a trade hull is
// only ever claimed by tour_run, so its last release IS its last tour's honest-exit
// time. That timestamp is persisted on the ship row, so the cooldown is respected
// across coordinator restarts with zero new state (RULINGS #2). A hull that has never
// run a tour (no release time) is clear immediately.
func cooldownRemaining(ship *navigation.Ship, now time.Time, cooldown time.Duration) time.Duration {
	if cooldown <= 0 {
		return 0
	}
	assignment := ship.Assignment()
	if assignment == nil || assignment.ReleasedAt() == nil {
		return 0 // never toured (or no terminal recorded) — nothing to cool down from
	}
	elapsed := now.Sub(*assignment.ReleasedAt())
	if elapsed >= cooldown {
		return 0
	}
	return cooldown - elapsed
}

// detectMassPark returns the set of idle hull symbols whose park is part of a
// restart-induced mass-park (sp-nkci): at least minHulls idle hulls released within
// `window` of each other. A daemon blip/restart force-parks the whole trade fleet in one
// narrow window, and that synchronized park must NOT be fed to the sp-1pli thin-depth
// backoff — organic thin-depth parks a hull at a time (when ITS market dies), so a
// tight cluster of many simultaneous parks is a restart signature, not a depth signal.
// An empty set (no cluster, or fewer than minHulls idle hulls) means nothing is exempt,
// so the backoff behaves exactly as before for the spread-out single-hull case.
func detectMassPark(idle []*navigation.Ship, window time.Duration, minHulls int) map[string]bool {
	exempt := make(map[string]bool)
	if minHulls <= 0 || len(idle) < minHulls {
		return exempt
	}

	// Only hulls with a real release anchor can be part of a park cluster (a never-toured
	// hull has no releasedAt and is not adaptive anyway — cooldownFor short-circuits it).
	type park struct {
		symbol string
		at     time.Time
	}
	parks := make([]park, 0, len(idle))
	for _, ship := range idle {
		assignment := ship.Assignment()
		if assignment == nil || assignment.ReleasedAt() == nil {
			continue
		}
		parks = append(parks, park{symbol: ship.ShipSymbol(), at: *assignment.ReleasedAt()})
	}
	if len(parks) < minHulls {
		return exempt
	}

	// A hull is in a mass-park when at least minHulls parks (including itself) fall within
	// `window` of its own release. O(n^2) over the fleet's idle hulls (tens) — trivial.
	for i := range parks {
		coincident := 0
		for j := range parks {
			if absDuration(parks[i].at.Sub(parks[j].at)) <= window {
				coincident++
			}
		}
		if coincident >= minHulls {
			exempt[parks[i].symbol] = true
		}
	}
	return exempt
}

// hullBackoff is the adaptive per-hull relaunch-cooldown state sp-1pli tracks in
// memory (RunTradeFleetCoordinatorHandler.backoff). cooldown starts at the base and
// only ever changes through cooldownFor: doubled (clamped to the configured max) on a
// freshly-scored unproductive exit, reset to base on a freshly-scored productive one.
// scoredRelease is the release timestamp already folded into cooldown/
// consecutiveUnproductive — it guards against rescoring the SAME parked exit on every
// subsequent reconcile tick while the hull just sits out its cooldown, which would
// otherwise runaway-escalate a single unproductive tour to the max within a few ticks
// instead of once per real tour cycle ("no per-tick spam", per the bead).
type hullBackoff struct {
	consecutiveUnproductive int
	cooldown                time.Duration
	scoredRelease           time.Time
	// reachEscalated is set once a hull hits its 2nd consecutive fast-fail (sp-nxrt part a):
	// the relaunch is armed with reposition-reach (the broadened 2-4-gate-hop discovery)
	// so the hull MOVES to a fresh system instead of the coordinator sleeping ever longer
	// on a lane that is gone from HERE. It stays armed while the coordinator backs off a
	// map-wide-dead neighbourhood (streak >= 3) and is cleared only by a productive tour —
	// a recovered hull relaunches normally. reconcileOnce copies it onto the launch spec.
	reachEscalated bool
}

// cooldownFor resolves the relaunch cooldown to apply to one idle hull this pass AND
// whether that relaunch should be reach-escalated (sp-1pli + sp-nxrt). A hull that has
// never toured (no release recorded) is unscored, uses base, and is not escalated —
// exactly like cooldownRemaining's own nil-check.
//
// Otherwise the hull's last release is scored AT MOST ONCE (guarded by scoredRelease):
// a tour that ran for at least minProductiveTourDuration is productive and resets the
// hull straight back to base (and disarms any reach escalation); a shorter exit is an
// unproductive fast-fail and drives the escalation ladder below. Every escalation logs
// one INFO line (never a reset), so an idle hull merely waiting out an already-scored
// cooldown across many ticks stays silent.
//
// The fast-fail ladder (sp-nxrt part a) — the fix for ~238 hull-hours/day of pure
// parking, where SLEEP was the sole response and a hull spiralled 6->12->24->30min:
//
//	1st fast-fail   -> DOUBLE the sleep (base -> 2*base). The market HERE may just be
//	                   thin (the lxwn rich->tapped->rich cycle), so wait one cycle in
//	                   place — cheaper than moving. Reach stays off.
//	2nd consecutive -> ESCALATE TO MOVEMENT. Waiting-in-place did not help: the lane is
//	                   gone from HERE. Arm reposition-reach on the relaunch and drop the
//	                   sleep back to the base breather so the hull MOVES promptly instead
//	                   of a longer sleep. This is the biggest single tempo lever.
//	3rd+ consecutive-> Even the reach-armed relaunch (broadened to 2-4 gate hops) found
//	                   no ground worth the jump — genuine map-wide margin exhaustion.
//	                   RESUME the bounded sleep backoff (do not hammer a dead map every
//	                   base cooldown) while KEEPING reach armed for the instant a ground
//	                   reopens.
func (h *RunTradeFleetCoordinatorHandler) cooldownFor(ship *navigation.Ship, base, ceiling time.Duration, massParkExempt bool, logger common.ContainerLogger) (time.Duration, bool) {
	assignment := ship.Assignment()
	if assignment == nil || assignment.ReleasedAt() == nil {
		return base, false // never toured — nothing to score
	}
	releasedAt := *assignment.ReleasedAt()

	bo := h.backoff[ship.ShipSymbol()]
	if bo == nil {
		bo = &hullBackoff{cooldown: base}
		h.backoff[ship.ShipSymbol()] = bo
	}

	if !releasedAt.After(bo.scoredRelease) {
		return bo.cooldown, bo.reachEscalated // this exit was already scored on a prior tick
	}
	bo.scoredRelease = releasedAt

	// sp-nkci: a restart-induced mass-park (many hulls force-parked in one window) is not
	// a thin-depth signal — do NOT feed it to the adaptive backoff, and (sp-nxrt) do NOT
	// let it trigger the movement escalation: repositioning the whole fleet off a daemon
	// blip would be a mass reposition-churn event. Mark the release scored (so the same
	// park is never re-scored on a later tick as hulls relaunch and the cluster dissipates)
	// but leave the hull's cooldown, streak, AND reach flag untouched: a synchronized park
	// says nothing about market depth, so it neither escalates nor resets. One INFO line
	// (guarded by scoredRelease, so once per park) records why the fleet did not ramp.
	if massParkExempt {
		logger.Log("INFO", fmt.Sprintf(
			"Trade hull %s parked in a fleet-wide mass-park window — exempt from sp-1pli adaptive backoff (sp-nkci), cooldown held at %s",
			ship.ShipSymbol(), bo.cooldown.Truncate(time.Second)), map[string]interface{}{
			"action":        "trade_fleet_masspark_exempt",
			"ship_symbol":   ship.ShipSymbol(),
			"cooldown_secs": int(bo.cooldown.Seconds()),
		})
		return bo.cooldown, bo.reachEscalated
	}

	if releasedAt.Sub(assignment.AssignedAt()) >= minProductiveTourDuration {
		// Productive: a fresh ground was found and traded. Reset to base and disarm reach —
		// the recovered hull relaunches normally, not force-armed forever.
		bo.consecutiveUnproductive = 0
		bo.cooldown = base
		bo.reachEscalated = false
		return bo.cooldown, false
	}

	bo.consecutiveUnproductive++
	switch {
	case bo.consecutiveUnproductive == 1:
		// 1st fast-fail: wait ONE lengthened cycle in place (the market may just be thin).
		bo.cooldown = clampDuration(base*2, ceiling)
		logger.Log("INFO", fmt.Sprintf(
			"Trade hull %s cooldown escalating to %s after %d consecutive unproductive exit(s) — fleet-wide infeasibility backoff",
			ship.ShipSymbol(), bo.cooldown.Truncate(time.Second), bo.consecutiveUnproductive), map[string]interface{}{
			"action":                   "trade_fleet_backoff_escalate",
			"ship_symbol":              ship.ShipSymbol(),
			"new_cooldown_secs":        int(bo.cooldown.Seconds()),
			"consecutive_unproductive": bo.consecutiveUnproductive,
		})
	case bo.consecutiveUnproductive == 2:
		// 2nd consecutive fast-fail: the lane is gone from HERE — MOVE instead of sleeping
		// longer. Arm reposition-reach and relaunch at the base breather (sp-nxrt part a).
		bo.reachEscalated = true
		bo.cooldown = base
		logger.Log("INFO", fmt.Sprintf(
			"Trade hull %s escalating to MOVEMENT after %d consecutive unproductive exit(s) — arming reposition-reach and relaunching at the %s base breather instead of a longer sleep (sp-nxrt)",
			ship.ShipSymbol(), bo.consecutiveUnproductive, bo.cooldown.Truncate(time.Second)), map[string]interface{}{
			"action":                   "trade_fleet_movement_escalate",
			"ship_symbol":              ship.ShipSymbol(),
			"new_cooldown_secs":        int(bo.cooldown.Seconds()),
			"consecutive_unproductive": bo.consecutiveUnproductive,
			"reposition_reach_armed":   true,
		})
	default:
		// 3rd+ consecutive fast-fail: the reach-armed relaunch could not escape either —
		// genuine map-wide exhaustion. Resume the bounded sleep backoff, keep reach armed.
		bo.cooldown = clampDuration(bo.cooldown*2, ceiling)
		logger.Log("INFO", fmt.Sprintf(
			"Trade hull %s cooldown escalating to %s after %d consecutive unproductive exit(s) — reposition-reach did not rescue it, backing off (bounded, reposition-reach stays armed)",
			ship.ShipSymbol(), bo.cooldown.Truncate(time.Second), bo.consecutiveUnproductive), map[string]interface{}{
			"action":                   "trade_fleet_backoff_escalate",
			"ship_symbol":              ship.ShipSymbol(),
			"new_cooldown_secs":        int(bo.cooldown.Seconds()),
			"consecutive_unproductive": bo.consecutiveUnproductive,
			"reposition_reach_armed":   true,
		})
	}
	return bo.cooldown, bo.reachEscalated
}

// clampDuration caps d at ceiling (the per-hull backoff ceiling, RULINGS #5). A tiny helper
// so the ladder's two escalation arms clamp identically.
func clampDuration(d, ceiling time.Duration) time.Duration {
	if d > ceiling {
		return ceiling
	}
	return d
}

// priorExitReason returns the release reason stamped on the hull when its last tour
// terminated, or "" if none — read-only (the coordinator never rewrites it, so
// honest-exit telemetry is untouched).
func priorExitReason(ship *navigation.Ship) string {
	assignment := ship.Assignment()
	if assignment == nil || assignment.ReleaseReason() == nil {
		return ""
	}
	return *assignment.ReleaseReason()
}

// priorExitReasonLabel is priorExitReason with a human placeholder for the empty case,
// for the relaunch log line.
func priorExitReasonLabel(ship *navigation.Ship) string {
	if reason := priorExitReason(ship); reason != "" {
		return reason
	}
	return "unknown"
}

// absDuration returns the absolute value of a duration.
func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

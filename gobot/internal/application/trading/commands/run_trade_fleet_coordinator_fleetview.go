package commands

import (
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// partitionTradeFleet splits a fleet snapshot into the trade-dedicated hulls that
// are parked and eligible for relaunch (idle, not in transit) and a count of those
// currently running a tour — the reconcile predicate (sp-1278), the analog of the
// scout reconciler detecting unmanned slots.
//
// Membership is isTradeFleetTag: "trade", "trade-mvt" and "trade-lane" are ALL this
// coordinator's hulls (spec §8). The tag selects the tour path per hull downstream (it
// rides the launch spec), never whether the hull is launched at all, so re-tagging a
// hull between the three is a pure path switch with no relaunch gap.
//
// The two captain off-switches are honored here for free, with no extra guard:
//   - Unpinned: a hull carrying none of the trade tags is simply not ours — skipped.
//   - Captain-reserved: a captain reservation is an ACTIVE assignment (owner=captain),
//     so it is neither idle nor a container-owned tour; IsReservedByCaptain() drops it
//     before either bucket, so a reserved hull is never relaunched AND never counted
//     against the concurrency cap.
//
// A hull mid-tour (a live container claim) is returned in `running` — the sp-m3122
// watchdog inspects each for liveness (a RUNNING claim is no longer trusted as healthy),
// and len(running) is the concurrency-cap count. An in-transit idle hull (a rare gap
// between release and the next claim while still flying) is neither relaunched this tick
// nor counted — StartTourRun would refuse a non-idle hull anyway, and counting it would
// distort the cap.
func partitionTradeFleet(ships []*navigation.Ship) (idle []*navigation.Ship, running []*navigation.Ship) {
	for _, ship := range ships {
		if !isTradeFleetTag(ship.DedicatedFleet()) {
			continue // not a trade hull (or unpinned — the captain's per-hull opt-out)
		}
		if ship.IsReservedByCaptain() {
			continue // captain reserved: respect it, never relaunch and never count it
		}
		if ship.IsAssigned() {
			running = append(running, ship) // live container claim => a tour is running
			continue
		}
		if ship.IsInTransit() {
			continue // idle but still flying — not a launch candidate this tick
		}
		idle = append(idle, ship) // parked by an honest tour exit: relaunch candidate
	}
	return idle, running
}

// fullHullPressure counts how many of the fleet's trade hulls (idle + running) are sitting
// FULL — the sp-tgll8 inventory-pressure signal — alongside the total, so reconcileOnce can
// decide whether the fleet is too saturated with unsold cargo to launch NEW buying tours. A
// FULL hull genuinely cannot offload; a merely LADEN (partly-loaded) hull is a healthy
// mid-run tour and is NOT counted — the naive "laden fraction" would false-trip on a working
// fleet, so the signal keys on FULL/stuck, not laden.
func fullHullPressure(idle, running []*navigation.Ship) (full, total int) {
	for _, ship := range idle {
		if isFullTradeHull(ship) {
			full++
		}
	}
	for _, ship := range running {
		if isFullTradeHull(ship) {
			full++
		}
	}
	return full, len(idle) + len(running)
}

// isFullTradeHull reports whether a hull's hold is at capacity — the sp-tgll8 saturation
// signal. The capacity guard rejects a zero-capacity hull (which would spuriously read
// units>=capacity as 0>=0) so only a real cargo hull genuinely stuck full counts.
func isFullTradeHull(ship *navigation.Ship) bool {
	return ship.CargoCapacity() > 0 && ship.CargoUnits() >= ship.CargoCapacity()
}

// maxConcurrentLabel renders the concurrency cap for the start log — "unlimited" for
// the <=0 (fleet-size-bounded) case.
func maxConcurrentLabel(limit int) string {
	if limit <= 0 {
		return "unlimited"
	}
	return fmt.Sprintf("%d", limit)
}

// cooldownDuration resolves the command's cooldown, applying the default when unset.
func (c *RunTradeFleetCoordinatorCommand) cooldownDuration() time.Duration {
	secs := c.CooldownSecs
	if secs <= 0 {
		secs = defaultTradeFleetCooldownSeconds
	}
	return time.Duration(secs) * time.Second
}

// relaunchBackoffMaxDuration resolves the command's adaptive-backoff ceiling (sp-1pli),
// applying the default when unset.
func (c *RunTradeFleetCoordinatorCommand) relaunchBackoffMaxDuration() time.Duration {
	secs := c.RelaunchBackoffMaxSecs
	if secs <= 0 {
		secs = defaultRelaunchBackoffMaxSeconds
	}
	return time.Duration(secs) * time.Second
}

// massParkWindow resolves the mass-park co-park window (sp-nkci), applying the default
// when unset.
func (c *RunTradeFleetCoordinatorCommand) massParkWindow() time.Duration {
	secs := c.MassParkWindowSecs
	if secs <= 0 {
		secs = defaultMassParkWindowSeconds
	}
	return time.Duration(secs) * time.Second
}

// massParkMinHulls resolves the mass-park hull threshold (sp-nkci), applying the default
// when unset.
func (c *RunTradeFleetCoordinatorCommand) massParkMinHulls() int {
	if c.MassParkMinHulls <= 0 {
		return defaultMassParkMinHulls
	}
	return c.MassParkMinHulls
}

// watchdogStallThreshold resolves the sp-m3122 liveness-watchdog stall threshold, applying
// the default (12 min) when unset.
func (c *RunTradeFleetCoordinatorCommand) watchdogStallThreshold() time.Duration {
	secs := c.WatchdogStallSecs
	if secs <= 0 {
		secs = defaultWatchdogStallSeconds
	}
	return time.Duration(secs) * time.Second
}

// fullHullPausePct resolves the sp-tgll8 inventory-pressure governor threshold, applying the
// default (65%) when unset.
func (c *RunTradeFleetCoordinatorCommand) fullHullPausePct() int {
	if c.FullHullPausePct <= 0 {
		return defaultFullHullPausePct
	}
	return c.FullHullPausePct
}

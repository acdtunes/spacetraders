package parkedsensing

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// seedlessTargets are the systems with charting work and no hull on the way.
//
// Charting work is uncharted waypoints OR a waypoint list we have never swept.
// The second is not a lesser case of the first: an unswept system reports zero
// uncharted waypoints precisely BECAUSE we have never looked, so a count-only
// rule would leave every genuinely unexplored system permanently invisible —
// which is the entire population expansion exists to reach.
//
// Ordered deepest-dark first so the biggest KNOWN unknown is resolved soonest,
// then by symbol for a queue that is reproducible tick to tick. An unswept
// system carries an honest count of zero and therefore sorts last, which is
// deliberate: it might hold thirty uncharted waypoints or none at all, and
// ranking a guess above a measured thirty would be inventing evidence.
func seedlessTargets(systems []ExpandSystem) []ExpandSystem {
	var out []ExpandSystem
	for _, s := range systems {
		if (s.UnchartedCount > 0 || !s.CatalogKnown) && !hasActiveSeed(s) {
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].UnchartedCount != out[j].UnchartedCount {
			return out[i].UnchartedCount > out[j].UnchartedCount
		}
		return out[i].System < out[j].System
	})
	return out
}

// hasActiveSeed reports whether a system already has a hull on the errand. DONE
// is not active: the errand is over and the row is only still naming its hull so
// the probe stays attributable.
func hasActiveSeed(s ExpandSystem) bool {
	return s.SeedShip != "" && (s.SeedState == SeedStateDispatched || s.SeedState == SeedStateCharting)
}

// advanceSeeds moves every running errand one step, up to the tick's budget.
//
// One step per seed per tick, and a step is a step whether it commands a ship or
// merely advances the record: both end the seed's turn. That is what keeps the
// engine from reading a position its own command has just invalidated — the next
// tick reads the ships table instead.
func (t *expandTick) advanceSeeds(ctx context.Context, systems []ExpandSystem) error {
	active := make([]ExpandSystem, 0, len(systems))
	for _, s := range systems {
		if hasActiveSeed(s) {
			active = append(active, s)
		}
	}
	sort.SliceStable(active, func(i, j int) bool { return active[i].System < active[j].System })

	for _, s := range active {
		if t.rep.Actions >= MaxExpansionActions {
			return nil
		}
		acted, err := t.advanceSeed(ctx, s)
		if err != nil {
			return err
		}
		if acted {
			t.rep.Actions++
		}
	}
	return nil
}

// advanceSeed applies the one step available to a single errand and reports
// whether it consumed the tick's budget.
func (t *expandTick) advanceSeed(ctx context.Context, s ExpandSystem) (bool, error) {
	pos, err := t.p.Ships.ShipAt(ctx, t.playerID, s.SeedShip)
	if err != nil || !pos.Found {
		// Never command a hull we cannot locate. An unreadable row and an absent
		// one leave the errand exactly as it is, to be retried once the ships
		// table can answer.
		return false, nil
	}
	if pos.NavStatus == navigation.NavStatusInTransit {
		return false, nil // already flying, under this or an earlier tick's command
	}

	if s.SeedState == SeedStateDispatched {
		return t.dispatchSeed(ctx, s, pos)
	}
	return t.chartSeed(ctx, s, pos)
}

// dispatchSeed gets a hull to its target system, one gate hop at a time.
func (t *expandTick) dispatchSeed(ctx context.Context, s ExpandSystem, pos ShipPos) (bool, error) {
	if shared.ExtractSystemSymbol(pos.Waypoint) == s.System {
		// Arrived. Sweep the waypoint LIST before the tour begins, because the
		// tour is driven entirely off the stored uncharted set and, for a system
		// nobody has visited, that set is EMPTY — not because the system is
		// charted but because we have never asked. Without this the seed would
		// arrive, read nothing to do, and immediately "finish" a tour during
		// which it charted precisely nothing.
		//
		// This ONE action may issue several API calls: the waypoint list is
		// paginated, so the adapter walks its pages. That is deliberate rather
		// than a leak in the budget — it is bounded by a single system's page
		// count, happens once per system for the life of the era, and splitting
		// it across ticks would leave a half-known catalog that the very next
		// step would misread as a finished tour.
		if err := t.p.SeedShip.SyncWaypoints(ctx, t.playerID, s.System); err != nil {
			// The catalog is not written, so it is not stamped either. The seed
			// stays DISPATCHED and the next tick retries from the same place.
			//
			// Logged rather than swallowed because the retry is UNBOUNDED and
			// not cheap: each attempt may walk several pages of the waypoint
			// list, every tick, for as long as the sweep keeps failing — and
			// this is the engine that is supposed to yield first when the API is
			// under pressure. A silent version of this loop is invisible until
			// it shows up as rate-limit pressure somewhere else entirely.
			logging.LoggerFromContext(ctx).Log("WARN", "charting seed could not sweep its target's waypoint catalog; retrying next tick", map[string]interface{}{
				"action":      "parked_sensing_catalog_sweep_failed",
				"ship_symbol": s.SeedShip,
				"system":      s.System,
				"error":       err.Error(),
			})
			return true, nil
		}
		if err := t.p.Ledger.StampCatalogSynced(ctx, t.playerID, s.System); err != nil {
			return false, fmt.Errorf("failed to record the swept waypoint catalog of %q: %w", s.System, err)
		}

		// The state advances on the SHIP ROW, never on a jump command returning
		// — a command that succeeded and a hull that is actually there are
		// different facts, and only the second one licenses charting.
		if err := t.p.Ledger.SetSeed(ctx, t.playerID, s.System, s.SeedShip, SeedStateCharting); err != nil {
			return false, fmt.Errorf("failed to start the charting tour of %q: %w", s.System, err)
		}
		return true, nil
	}

	// ONE STEP of the gate hop — the move to the gate, or the jump off it. Which
	// one is decided by the hull's own position, which is why it is handed down
	// rather than re-derived. A hull mid-move is never here at all: advanceSeed
	// has already returned on the IN_TRANSIT reading above, so the step issued
	// is always the next one actually available.
	if err := t.p.SeedShip.JumpTo(ctx, t.playerID, s.SeedShip, pos.Waypoint, s.System); err != nil {
		// The hull did not leave. Holding the errand at DISPATCHED is what makes
		// the retry free: the next tick re-reads the position and re-issues.
		return true, nil
	}
	t.rep.Jumped++
	return true, nil
}

// chartSeed works one waypoint of a tour, or ends it.
func (t *expandTick) chartSeed(ctx context.Context, s ExpandSystem, pos ShipPos) (bool, error) {
	remaining, err := t.p.Uncharted.UnchartedWaypoints(ctx, s.System)
	if err != nil {
		return false, nil // unreadable: leave the tour alone and retry next tick
	}
	if len(remaining) == 0 {
		return t.finishTour(ctx, s, pos)
	}

	if !contains(remaining, pos.Waypoint) {
		if err := t.p.SeedShip.NavigateTo(ctx, t.playerID, s.SeedShip, remaining[0]); err != nil {
			return true, nil // retried next tick from the same position
		}
		t.rep.Navigated++
		return true, nil
	}
	return t.chartHere(ctx, s, pos)
}

// chartHere charts the waypoint under the hull and reads what it revealed. The
// whole bundle — chart, refresh, and the market read behind it — is ONE step:
// it is a single indivisible piece of progress on one waypoint, and splitting it
// across ticks would leave a charted waypoint whose market we then have to fly
// back to.
func (t *expandTick) chartHere(ctx context.Context, s ExpandSystem, pos ShipPos) (bool, error) {
	if err := t.p.SeedShip.Chart(ctx, t.playerID, s.SeedShip); err != nil {
		return true, nil // nothing charted, so nothing to read back
	}
	isMarket, err := t.p.SeedShip.RefreshWaypoint(ctx, t.playerID, s.System, pos.Waypoint)
	if err != nil {
		// The chart landed but the waypoint was not written back, so the stored
		// uncharted set still names it and the next tick charts it again — a
		// benign no-op at the API. Nothing further can be trusted about it here.
		return true, nil
	}
	t.rep.Charted++

	if !isMarket {
		return true, nil
	}
	if err := t.p.SeedShip.ReadMarketAt(ctx, t.playerID, pos.Waypoint); err != nil {
		return true, nil // prices unread; the screen will resolve the market later
	}
	return true, t.recordSeedMarket(ctx, s.System, pos.Waypoint)
}

// recordSeedMarket writes the placement a seed-revealed market earns.
//
// The write goes STRAIGHT to the ledger rather than waiting for the next screen,
// and the screen's slot cache is built on that: for a system still marked
// PENDING, market_data is the only record that the seed was ever here, and a
// placement row is what lets the screen resolve this waypoint from the cache
// instead of paying the API to rediscover it.
func (t *expandTick) recordSeedMarket(ctx context.Context, system, waypoint string) error {
	if t.book.occupied(waypoint, SlotKindMarket) {
		return nil // already placed; the existing row holds the live state
	}
	goods, known, err := t.p.MarketGoods.GoodsAt(ctx, t.playerID, waypoint)
	if err != nil || !known {
		return nil
	}
	matched := matchWhitelist(goods, t.k.Whitelist)
	if len(matched) == 0 {
		return nil // a market dealing in nothing we want earns no probe
	}

	// Depth is measured, not assumed: the seed is standing at the counter, so
	// the scan it just took carries real prices — unlike a market discovered
	// remotely, which is slotted with a blind zero.
	rows, err := t.p.MarketGoods.DepthRowsAt(ctx, t.playerID, waypoint)
	if err != nil {
		return nil
	}
	if err := t.p.Ledger.UpsertSlotMetadata(ctx, t.playerID, SlotRecord{
		Waypoint:       waypoint,
		System:         system,
		Kind:           SlotKindMarket,
		State:          SlotStateWanted,
		WhitelistGoods: matched,
		DepthCredits:   depthOf(rows, t.k.Whitelist),
	}); err != nil {
		return fmt.Errorf("failed to record the market a seed found at %q: %w", waypoint, err)
	}
	t.book.take(system, waypoint, SlotKindMarket, SlotStateWanted)
	t.rep.MarketsFound++
	return nil
}

// finishTour stands a seed down once its target is charted through.
//
// The system is re-screened first, because everything the tour learned has been
// written to the local caches and the verdict it produces is what decides the
// hull's fate. Then, in order of what the hull is worth where it is:
//
//  1. fill a placement in this system — the errand ended somewhere we want
//     watched, and a hull already standing there is the cheapest probe we will
//     ever place. The system's SHIPYARD is taken ahead of any market, because
//     that is the placement which turns the system into a staging origin and
//     lets it buy its own next probe; see wantedIn;
//  2. otherwise push on to the next frontier system reachable from here, which
//     is a free extension of an errand already paid for;
//  3. otherwise stand down as a spare, staying on the books for the buy queue to
//     re-task.
func (t *expandTick) finishTour(ctx context.Context, s ExpandSystem, pos ShipPos) (bool, error) {
	result, err := t.p.Screen(ctx, s.System)
	if err != nil {
		// No verdict, no decision. Standing the hull down on a system we failed
		// to judge would either strand it or write off a system we never read.
		return true, nil
	}

	current := shared.ExtractSystemSymbol(pos.Waypoint)
	if result.Verdict == VerdictInScope {
		// A catalog that cannot answer stops the tick rather than falling back to
		// an unordered fill. Reading the failure as "no yards here" would silently
		// hand the hull to a market and leave the system unable to seed its
		// neighbours — for good, since the seed is consumed by the placement it
		// takes and no later tick revisits the choice. The tick is idempotent, so
		// failing loudly costs one cycle.
		yards, err := t.probeYardsIn(ctx, current)
		if err != nil {
			return true, err
		}
		filled, err := t.fillPlacement(ctx, s, current, t.book.wantedIn(current, pos.Waypoint, yards))
		if err != nil || filled {
			return true, err
		}
	}

	retargeted, err := t.retargetSeed(ctx, s, current)
	if err != nil || retargeted {
		return true, err
	}
	return true, t.standDownAsSpare(ctx, s, pos, current)
}

// fillPlacement hands the seed hull to the first of the offered placements it
// can claim, and reports whether one was taken.
func (t *expandTick) fillPlacement(ctx context.Context, s ExpandSystem, current string, wants []QueuedSlot) (bool, error) {
	for _, want := range wants {
		// IN_TRANSIT rather than PARKED even when the hull is already standing
		// on the waypoint: PARKED is recorded only on a CONFIRMED docked
		// reading, and the placement machine is the only thing that takes it.
		hull := s.SeedShip
		err := t.p.Ledger.TransitionSlot(ctx, t.playerID, SlotTransition{
			Waypoint: want.Waypoint, Kind: want.Kind, From: SlotStateWanted, To: SlotStateInTransit,
		}, SlotFields{AssignedShip: &hull})
		switch {
		case errors.Is(err, ErrSlotClaimed):
			continue // another writer took this placement; the next one may be free
		case err != nil:
			// Not contention: the ledger is refusing writes. Reading that as a
			// lost race would have the seed fall through to standing itself
			// down as a spare — writing to the very ledger that just failed.
			return false, fmt.Errorf("failed to hand seed %s to placement %s: %w", hull, want.Waypoint, err)
		}
		t.book.take(current, want.Waypoint, want.Kind, SlotStateInTransit)
		if err := t.p.Ledger.SetSeed(ctx, t.playerID, s.System, "", ""); err != nil {
			return true, fmt.Errorf(
				"seed %s filled placement %s but its errand on %q was not cleared: %w",
				hull, want.Waypoint, s.System, err)
		}
		t.rep.Parked++
		return true, nil
	}
	return false, nil
}

// retargetSeed sends a finished seed on to the next system reachable from where
// it stands that still needs charting, and reports whether it found one.
//
// Reachability is MaxWalkRings gate hops, because that is what a dispatched seed
// executes — the errand is re-stamped onto the new target and the seed walks
// there a hop per tick, exactly as it walked to this one. A one-hop bound here
// would make this the one place in the engine that refuses reach the rest of it
// grants: a finished hull standing two hops from a dark system would be parked
// as a spare and a FRESH probe bought to cover the very target it was already
// next to.
//
// The candidate must also be a system the ledger says has charting work: this is
// the same seedless-target list the spare claim and the purchase request draw
// from, so a system covered by a retarget cannot also be sent a hull it does not
// need — and, just as importantly, "needs charting" means one thing across the
// whole engine rather than one thing here and another there.
//
// Retargeting is two writes because the row IS the target, and neither order is
// safe on its own:
//
//   - Stamp first, and a failure between the writes leaves TWO systems naming
//     one hull. Both would drive it as their own seed, double-stepping it every
//     tick and sending it two conflicting places.
//   - Clear first, and a failure leaves it named by NEITHER. A mid-tour hull has
//     no placement row — it was deleted when the seed was claimed — so the
//     errand is the only thing naming it at all, and losing that orphans a probe
//     we paid for: invisible to the probe cap, and re-bought. That is the
//     direction claimSpares refuses for exactly the same reason.
//
// So it clears first (the single-driver invariant is preserved unconditionally)
// and then RESTORES the old errand if the stamp fails, which closes the orphan
// window in the only case that opens it. A restore that itself fails is logged
// loudly with both systems and the hull named, because at that point the probe
// can only be recovered by hand.
func (t *expandTick) retargetSeed(ctx context.Context, s ExpandSystem, current string) (bool, error) {
	for _, target := range t.targets {
		if t.covered[target.System] || target.System == s.System {
			continue
		}
		within, err := t.reach.canReach(ctx, current, target.System)
		if err != nil {
			return false, err
		}
		if !within {
			continue
		}
		if err := t.p.Ledger.SetSeed(ctx, t.playerID, s.System, "", ""); err != nil {
			return false, fmt.Errorf("failed to end the charting tour of %q: %w", s.System, err)
		}
		if err := t.p.Ledger.SetSeed(ctx, t.playerID, target.System, s.SeedShip, SeedStateDispatched); err != nil {
			return true, t.restoreErrand(ctx, s, target.System, err)
		}
		t.covered[target.System] = true
		t.rep.Retargeted++
		return true, nil
	}
	return false, nil
}

// restoreErrand puts a half-retargeted seed back where it was, and returns the
// error the caller should surface.
//
// The hull is unnamed at this instant: the old errand is cleared and the new one
// did not land. Since a mid-tour hull has no placement row either, nothing in
// the ledger knows it exists — so restoring the ORIGINAL errand (rather than
// leaving it, or retrying the new one) is what keeps a probe we paid for
// attributable. The seed simply finishes its tour again next tick and retries
// the move; a repeated no-op tour is cheap, an orphaned probe is not.
func (t *expandTick) restoreErrand(ctx context.Context, s ExpandSystem, target string, cause error) error {
	if restoreErr := t.p.Ledger.SetSeed(ctx, t.playerID, s.System, s.SeedShip, s.SeedState); restoreErr != nil {
		logging.LoggerFromContext(ctx).Log("ERROR", "charting seed is named by no errand after a failed retarget; the hull is unattributable until an operator restores it", map[string]interface{}{
			"action":        "parked_sensing_seed_orphaned",
			"ship_symbol":   s.SeedShip,
			"from_system":   s.System,
			"target_system": target,
			"error":         restoreErr.Error(),
		})
		return fmt.Errorf(
			"failed to retarget seed %s onto %q AND could not restore its errand on %q (hull now unattributable): %w",
			s.SeedShip, target, s.System, cause)
	}
	return fmt.Errorf("failed to retarget seed %s onto %q (errand on %q restored): %w",
		s.SeedShip, target, s.System, cause)
}

// standDownAsSpare parks a finished seed where it stands, as a reserve hull the
// buy queue can re-task for free.
//
// The placement it writes is what puts the probe back on the books: for the
// length of the errand the hull was named only by its system row, and this is
// where that ends.
//
// An unfilled placement on the very waypoint the hull is standing on is taken
// INSTEAD, and takes precedence over the whole-system verdict that got us here.
// A system can be rejected as a whole and still hold one market worth watching
// — the seed's own market reads write wants directly, before any verdict — and
// a hull already berthed on one is the cheapest probe we will ever place.
//
// A waypoint carrying any OTHER placement is left strictly alone: overwriting it
// would reassign the hull that row names. The seed is stood down DONE instead,
// keeping this hull named by the system row rather than by nothing at all. That
// branch should be unreachable — a waypoint the seed had to CHART cannot already
// hold a filled placement, which the screen only ever writes for charted
// waypoints — so it is logged rather than handled silently.
func (t *expandTick) standDownAsSpare(ctx context.Context, s ExpandSystem, pos ShipPos, current string) error {
	if want, wanted := t.book.wantedAt(current, pos.Waypoint); wanted {
		filled, err := t.fillPlacement(ctx, s, current, []QueuedSlot{want})
		if err != nil || filled {
			return err
		}
	}

	// Asked of the SPARE half only. This branch strands a hull — it
	// stands the seed down with NO placement row, so the probe cap stops counting
	// a probe we own, which is the money-unsafe direction and is why it is logged
	// rather than passed over. The test is kind-scoped, so only a genuine
	// SPARE-on-SPARE collision lands here — a seed finishing on a market it has
	// just charted parks properly and stays counted.
	if t.book.occupied(pos.Waypoint, SlotKindSpare) {
		logging.LoggerFromContext(ctx).Log("WARN", "charting seed finished on a waypoint that already holds a spare placement; standing it down without a slot", map[string]interface{}{
			"action":      "parked_sensing_seed_standdown_blocked",
			"ship_symbol": s.SeedShip,
			"waypoint":    pos.Waypoint,
			"system":      s.System,
		})
		if err := t.p.Ledger.SetSeed(ctx, t.playerID, s.System, s.SeedShip, SeedStateDone); err != nil {
			return fmt.Errorf("failed to stand seed %s down on %q: %w", s.SeedShip, s.System, err)
		}
		return nil
	}

	if err := t.p.Ledger.UpsertSpareSlot(ctx, t.playerID, SlotRecord{
		Waypoint:     pos.Waypoint,
		System:       current,
		Kind:         SlotKindSpare,
		State:        SlotStateParked,
		AssignedShip: s.SeedShip,
	}); err != nil {
		return fmt.Errorf("failed to park seed %s as a spare at %q: %w", s.SeedShip, pos.Waypoint, err)
	}
	t.book.addSpare(current, pos.Waypoint, SlotStateParked)
	if err := t.p.Ledger.SetSeed(ctx, t.playerID, s.System, "", ""); err != nil {
		return fmt.Errorf(
			"seed %s parked as a spare at %s but its errand on %q was not cleared (hull double-counted, probe cap reads high): %w",
			s.SeedShip, pos.Waypoint, s.System, err)
	}
	t.rep.Parked++
	return nil
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

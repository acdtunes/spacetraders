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

// seedlessTargets are the systems with charting work and a crew short of its budget.
//
// Charting work is uncharted waypoints OR a waypoint list we have never swept. The
// second is not a lesser case of the first: an unswept system reports zero uncharted
// waypoints precisely BECAUSE we have never looked, so a count-only rule would leave
// every genuinely unexplored system permanently invisible. Ordered deepest-dark
// first, then by symbol for a queue reproducible tick to tick; an unswept system
// carries an honest count of zero and therefore sorts last, deliberately, because
// ranking a guess above a measured count would be inventing evidence.
//
// A SYSTEM ALREADY BEING CHARTED IS STILL A TARGET while its crew is short of the
// budget its size earns, and it keeps its place in the deepest-dark order rather
// than queueing behind every untouched system. That ordering is the point: the
// deepest system is the one whose serial tour runs longest, so it is where the next
// hull buys the most time. A hull budget of one restores the old rule outright.
func seedlessTargets(systems []ExpandSystem, hulls chartHulls) []ExpandSystem {
	var out []ExpandSystem
	for _, s := range systems {
		if (s.UnchartedCount > 0 || !s.CatalogKnown) && activeSeedCount(s) < hulls.budgetFor(s.UnchartedCount) {
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

// ChartQueueDepth counts the systems still carrying charting work — uncharted
// waypoints, or a waypoint list never swept: the same two conditions
// seedlessTargets reads as work, so the coordinator's crew-first ordering fires
// exactly when the claim pass has something to target. Crew budgets are ignored
// on purpose: outstanding work holds the queue open even while crews are full.
func ChartQueueDepth(systems []ExpandSystem) int {
	depth := 0
	for _, s := range systems {
		if s.UnchartedCount > 0 || !s.CatalogKnown {
			depth++
		}
	}
	return depth
}

// activeSeedCount is how many hulls a system row has out charting. DONE is not
// active: the errand is over and the row is only still naming its hull so the
// probe stays attributable.
func activeSeedCount(s ExpandSystem) int {
	n := 0
	if s.SeedShip != "" && activeSeedState(s.SeedState) {
		n++
	}
	for _, extra := range s.ExtraSeeds {
		if extra.Ship != "" && activeSeedState(extra.State) {
			n++
		}
	}
	return n
}

// chartSeedRun is ONE hull's turn on a charting tour: the errand's own hull and
// state, beside the system row it is stamped on. The whole row is carried because
// finishing a tour re-screens the system and reads its verdict; `primary` says
// which of the ledger's two errand slots holds this hull.
type chartSeedRun struct {
	row     ExpandSystem
	ship    string
	state   string
	primary bool
}

func (r chartSeedRun) system() string { return r.row.System }

// seedRuns flattens the ledger's system rows into the individual errands a tick
// serves: the row's own seed columns first, then the crew beyond them.
func seedRuns(systems []ExpandSystem) []chartSeedRun {
	var out []chartSeedRun
	for _, s := range systems {
		if s.SeedShip != "" && activeSeedState(s.SeedState) {
			out = append(out, chartSeedRun{row: s, ship: s.SeedShip, state: s.SeedState, primary: true})
		}
		for _, extra := range s.ExtraSeeds {
			if extra.Ship != "" && activeSeedState(extra.State) {
				out = append(out, chartSeedRun{row: s, ship: extra.Ship, state: extra.State})
			}
		}
	}
	return out
}

// seedServiceRank orders an errand by what its turn can accomplish. A seed already
// standing in its target system charts on its turn; one still walking only moves.
func seedServiceRank(r chartSeedRun) int {
	if r.state == SeedStateCharting {
		return 0
	}
	return 1
}

// advanceSeeds moves every running errand one step, up to the tick's budget.
//
// One step per seed per tick, and a step is a step whether it commands a ship or
// merely advances the record: both end the seed's turn. That is what keeps the
// engine from reading a position its own command has just invalidated.
//
// The queue is ranked before it is truncated, or a budget smaller than the fleet
// serves one alphabetical prefix forever and the rest of the errands never move.
// Hulls sharing a system are ranked by symbol behind that, so a crew's turns are
// as reproducible as a lone seed's.
func (t *expandTick) advanceSeeds(ctx context.Context, systems []ExpandSystem) error {
	active := seedRuns(systems)
	sort.SliceStable(active, func(i, j int) bool {
		if ri, rj := seedServiceRank(active[i]), seedServiceRank(active[j]); ri != rj {
			return ri < rj
		}
		if active[i].system() != active[j].system() {
			return active[i].system() < active[j].system()
		}
		return active[i].ship < active[j].ship
	})

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

// stampErrand puts one hull on a system's charting crew: into the row's own seed
// columns when they are free, and onto the crew beyond them otherwise.
//
// PRIMARY-FIRST IS WHAT KEEPS A SINGLE-HULL SYSTEM STORED AS IT ALWAYS WAS, so
// every reader of those columns — the probe-cap union, the surge's errand index,
// an operator reading the row — keeps its meaning on the common case.
func (t *expandTick) stampErrand(ctx context.Context, system, ship, state string) error {
	if t.roster.primaryFree(system) {
		if err := t.p.Ledger.SetSeed(ctx, t.playerID, system, ship, state); err != nil {
			return err
		}
	} else if err := t.p.Ledger.SetExtraSeed(ctx, t.playerID, system, ship, state); err != nil {
		return err
	}
	t.roster.join(system, ship)
	return nil
}

// restampErrand advances a RUNNING errand's state, leaving it in the slot that
// already holds it: a hull mid-errand never changes slots, or the ledger and the
// roster come to disagree about which hull is the system's first.
func (t *expandTick) restampErrand(ctx context.Context, r chartSeedRun, state string) error {
	if r.primary {
		return t.p.Ledger.SetSeed(ctx, t.playerID, r.system(), r.ship, state)
	}
	return t.p.Ledger.SetExtraSeed(ctx, t.playerID, r.system(), r.ship, state)
}

// clearErrand ends one hull's errand, addressed in the slot that holds it, and
// drops the charting share the errand carried. The share goes LAST: it is
// bookkeeping for a hull that is no longer charting, where the errand row is what
// keeps the hull attributable, so a failed share write must not leave the errand
// standing.
func (t *expandTick) clearErrand(ctx context.Context, r chartSeedRun) error {
	if r.primary {
		if err := t.p.Ledger.SetSeed(ctx, t.playerID, r.system(), "", ""); err != nil {
			return err
		}
	} else if err := t.p.Ledger.ClearExtraSeed(ctx, t.playerID, r.ship); err != nil {
		return err
	}
	t.roster.leave(r.system(), r.ship)
	return t.p.Ledger.ClearChartShare(ctx, t.playerID, r.ship)
}

// advanceSeed applies the one step available to a single errand and reports
// whether it consumed the tick's budget.
func (t *expandTick) advanceSeed(ctx context.Context, r chartSeedRun) (bool, error) {
	pos, err := t.p.Ships.ShipAt(ctx, t.playerID, r.ship)
	if err != nil || !pos.Found {
		// Never command a hull we cannot locate. An unreadable row and an absent
		// one leave the errand exactly as it is, to be retried once the ships
		// table can answer.
		return false, nil
	}
	if pos.NavStatus == navigation.NavStatusInTransit {
		return false, nil // already flying, under this or an earlier tick's command
	}

	if r.state == SeedStateDispatched {
		return t.dispatchSeed(ctx, r, pos)
	}
	return t.chartSeed(ctx, r, pos)
}

// dispatchSeed gets a hull to its target system, one gate hop at a time.
func (t *expandTick) dispatchSeed(ctx context.Context, r chartSeedRun, pos ShipPos) (bool, error) {
	if shared.ExtractSystemSymbol(pos.Waypoint) == r.system() {
		// Arrived. Sweep the waypoint LIST before the tour begins: the tour is
		// driven entirely off the stored uncharted set, and for a system nobody
		// has visited that set is EMPTY — not because the system is charted but
		// because we have never asked. Without this the seed arrives, reads
		// nothing to do, and immediately "finishes" a tour it charted nothing on.
		// This ONE action may issue several API calls, the waypoint list being
		// paginated; it is bounded by a single system's page count and happens
		// once per system for the life of the era, and splitting it across ticks
		// would leave a half-known catalog the next step misreads as finished.
		if err := t.p.SeedShip.SyncWaypoints(ctx, t.playerID, r.system()); err != nil {
			// The catalog is not written, so it is not stamped either. The seed
			// stays DISPATCHED and the next tick retries from the same place.
			// Logged rather than swallowed because the retry is UNBOUNDED and not
			// cheap — each attempt may walk several pages of the waypoint list,
			// every tick, for as long as the sweep keeps failing — and this is the
			// engine that is supposed to yield first under API pressure.
			logging.LoggerFromContext(ctx).Log("WARN", "charting seed could not sweep its target's waypoint catalog; retrying next tick", map[string]interface{}{
				"action":      "parked_sensing_catalog_sweep_failed",
				"ship_symbol": r.ship,
				"system":      r.system(),
				"error":       err.Error(),
			})
			return true, nil
		}
		if err := t.p.Ledger.StampCatalogSynced(ctx, t.playerID, r.system()); err != nil {
			return false, fmt.Errorf("failed to record the swept waypoint catalog of %q: %w", r.system(), err)
		}

		// The state advances on the SHIP ROW, never on a jump command returning:
		// a command that succeeded and a hull that is actually there are
		// different facts, and only the second licenses charting.
		if err := t.restampErrand(ctx, r, SeedStateCharting); err != nil {
			return false, fmt.Errorf("failed to start the charting tour of %q: %w", r.system(), err)
		}
		return true, nil
	}

	// ONE STEP of the gate hop — the move to the gate, or the jump off it, decided
	// by the hull's own position, which is why the position is handed down rather
	// than re-derived. A hull mid-move is never here at all: advanceSeed has already
	// returned on the IN_TRANSIT reading, so the step issued is the next available.
	if err := t.p.SeedShip.JumpTo(ctx, t.playerID, r.ship, pos.Waypoint, r.system()); err != nil {
		// The hull did not leave, and the errand holds at DISPATCHED either way. A HELD
		// step made no API call and costs nothing; a REFUSED one reached the API and pays.
		return !errors.Is(err, ErrSeedStepHeld), nil
	}
	t.rep.Jumped++
	return true, nil
}

// chartSeed works one waypoint of a tour, or ends it.
//
// THE TOUR IS THIS HULL'S SHARE OF THE SYSTEM, not the system: a crewed system is
// divided into disjoint shares, so a hull's list ends — and its tour with it —
// while its crewmates still have work (chartshare.go). The share is the STORED
// assignment intersected with the outstanding set every turn, so a waypoint
// charted meanwhile, by a crewmate or by anybody else, simply leaves the list.
func (t *expandTick) chartSeed(ctx context.Context, r chartSeedRun, pos ShipPos) (bool, error) {
	stops, err := t.p.Uncharted.UnchartedStops(ctx, r.system())
	if err != nil {
		return false, nil // unreadable: leave the tour alone and retry next tick
	}
	remaining, err := t.shareFor(ctx, r.system(), r.ship, stops)
	if err != nil {
		return false, err
	}
	if len(remaining) == 0 {
		return t.finishTour(ctx, r, pos)
	}

	if !contains(remaining, pos.Waypoint) {
		if err := t.p.SeedShip.NavigateTo(ctx, t.playerID, r.ship, remaining[0]); err != nil {
			return true, nil // retried next tick from the same position
		}
		t.rep.Navigated++
		return true, nil
	}
	return t.chartHere(ctx, r, pos)
}

// chartHere charts the waypoint under the hull and reads what it revealed. The whole
// bundle — chart, refresh, and the market read behind it — is ONE step: splitting it
// across ticks would leave a charted waypoint whose market we then fly back to.
func (t *expandTick) chartHere(ctx context.Context, r chartSeedRun, pos ShipPos) (bool, error) {
	if err := t.p.SeedShip.Chart(ctx, t.playerID, r.ship); err != nil {
		return true, nil // nothing charted, so nothing to read back
	}
	isMarket, err := t.p.SeedShip.RefreshWaypoint(ctx, t.playerID, r.system(), pos.Waypoint)
	if err != nil {
		// The chart landed but the waypoint was not written back, so the stored
		// uncharted set still names it and the next tick charts it again — a
		// benign no-op at the API.
		return true, nil
	}
	t.rep.Charted++

	if !isMarket {
		return true, nil
	}
	if err := t.p.SeedShip.ReadMarketAt(ctx, t.playerID, pos.Waypoint); err != nil {
		return true, nil // prices unread; the screen will resolve the market later
	}
	return true, t.recordSeedMarket(ctx, r.system(), pos.Waypoint)
}

// recordSeedMarket writes the placement a seed-revealed market earns.
//
// The write goes STRAIGHT to the ledger rather than waiting for the next screen, and
// the screen's slot cache is built on that: for a system still marked PENDING,
// market_data is the only record that the seed was ever here, and a placement row
// lets the screen resolve this waypoint from the cache rather than from the API.
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

// finishTour stands a seed down once the share it owns is charted through.
//
// The system is re-screened first, because everything the tour learned has been
// written to the local caches and the verdict decides the hull's fate. Then, in
// order of what the hull is worth where it is:
//
//  1. fill a placement in this system — a hull already standing there is the
//     cheapest probe we will ever place. The system's SHIPYARD is taken ahead of
//     any market, because that is the placement which turns the system into a
//     staging origin and lets it buy its own next probe; see wantedIn;
//  2. otherwise push on to the next frontier system reachable from here, a free
//     extension of an errand already paid for;
//  3. otherwise stand down as a spare, on the books for the buy queue to re-task.
func (t *expandTick) finishTour(ctx context.Context, r chartSeedRun, pos ShipPos) (bool, error) {
	result, err := t.p.Screen(ctx, r.system())
	if err != nil {
		// No verdict, no decision. Standing the hull down on a system we failed
		// to judge would either strand it or write off a system we never read.
		return true, nil
	}

	current := shared.ExtractSystemSymbol(pos.Waypoint)
	if result.Verdict == VerdictInScope {
		// A catalog that cannot answer stops the tick rather than falling back to
		// an unordered fill. Reading the failure as "no yards here" would hand the
		// hull to a market and leave the system unable to seed its neighbours for
		// good, since the seed is consumed by the placement it takes and no later
		// tick revisits the choice. The tick is idempotent, so failing loudly
		// costs one cycle.
		yards, err := t.probeYardsIn(ctx, current)
		if err != nil {
			return true, err
		}
		filled, err := t.fillPlacement(ctx, r, current, t.book.wantedIn(current, pos.Waypoint, yards))
		if err != nil || filled {
			return true, err
		}
	}

	retargeted, err := t.retargetSeed(ctx, r, current)
	if err != nil || retargeted {
		return true, err
	}
	return true, t.standDownAsSpare(ctx, r, pos, current)
}

// fillPlacement hands the seed hull to the first of the offered placements it
// can claim, and reports whether one was taken.
func (t *expandTick) fillPlacement(ctx context.Context, r chartSeedRun, current string, wants []QueuedSlot) (bool, error) {
	for _, want := range wants {
		// IN_TRANSIT rather than PARKED even when the hull is already standing
		// on the waypoint: PARKED is recorded only on a CONFIRMED docked
		// reading, and the placement machine is the only thing that takes it.
		hull := r.ship
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
		if err := t.clearErrand(ctx, r); err != nil {
			return true, fmt.Errorf(
				"seed %s filled placement %s but its errand on %q was not cleared: %w",
				hull, want.Waypoint, r.system(), err)
		}
		t.rep.Parked++
		return true, nil
	}
	return false, nil
}

// retargetSeed sends a finished seed on to the next system reachable from where it
// stands that still needs charting, and reports whether it found one.
//
// Reachability is MaxWalkRings gate hops, because that is what a dispatched seed
// executes: the errand is re-stamped onto the new target and the seed walks there a
// hop per tick. A one-hop bound would refuse reach the rest of the engine grants,
// parking a hull two hops from a dark system as a spare and buying a FRESH probe to
// cover the target it was already next to. The candidate must also be a system the
// ledger says has charting work — the same seedless-target list the spare claim and
// the purchase request draw from, so "needs charting" means one thing engine-wide.
//
// Retargeting is two writes because the row IS the target, and neither order is safe
// on its own:
//
//   - Stamp first, and a failure between the writes leaves TWO systems naming one
//     hull, each driving it as its own seed and sending it two conflicting places.
//   - Clear first, and a failure leaves it named by NEITHER. A mid-tour hull has no
//     placement row — deleted when the seed was claimed — so the errand is the only
//     thing naming it, and losing that orphans a probe we paid for: invisible to
//     the probe cap, and re-bought. claimSpares refuses that direction likewise.
//
// So it clears first (the single-driver invariant holds unconditionally) and RESTORES
// the old errand if the stamp fails, closing the orphan window in the only case that
// opens it. A restore that itself fails is logged loudly with both systems and the
// hull named, because the probe can then only be recovered by hand.
//
// IT IS A NEW ERRAND, not a continuation, so the seed gate refuses it: the finished hull
// stands down where it is, draining the charting commitment rather than renewing it.
func (t *expandTick) retargetSeed(ctx context.Context, r chartSeedRun, current string) (bool, error) {
	if !t.k.SeedsEnabled {
		return false, nil
	}
	for _, target := range t.targets {
		if t.covered[target.System] || target.System == r.system() {
			continue
		}
		within, err := t.reach.canReach(ctx, current, target.System)
		if err != nil {
			return false, err
		}
		if !within {
			continue
		}
		if err := t.clearErrand(ctx, r); err != nil {
			return false, fmt.Errorf("failed to end the charting tour of %q: %w", r.system(), err)
		}
		if err := t.stampErrand(ctx, target.System, r.ship, SeedStateDispatched); err != nil {
			return true, t.restoreErrand(ctx, r, target.System, err)
		}
		t.covered[target.System] = true
		t.rep.Retargeted++
		return true, nil
	}
	return false, nil
}

// restoreErrand puts a half-retargeted seed back where it was, and returns the error
// the caller should surface.
//
// The hull is unnamed at this instant: the old errand is cleared and the new one did
// not land. A mid-tour hull has no placement row either, so nothing in the ledger
// knows it exists — restoring the ORIGINAL errand rather than retrying the new one
// keeps a probe we paid for attributable. The seed finishes its tour again next tick
// and retries; a repeated no-op tour is cheap, an orphaned probe is not.
func (t *expandTick) restoreErrand(ctx context.Context, r chartSeedRun, target string, cause error) error {
	if restoreErr := t.stampErrand(ctx, r.system(), r.ship, r.state); restoreErr != nil {
		logging.LoggerFromContext(ctx).Log("ERROR", "charting seed is named by no errand after a failed retarget; the hull is unattributable until an operator restores it", map[string]interface{}{
			"action":        "parked_sensing_seed_orphaned",
			"ship_symbol":   r.ship,
			"from_system":   r.system(),
			"target_system": target,
			"error":         restoreErr.Error(),
		})
		return fmt.Errorf(
			"failed to retarget seed %s onto %q AND could not restore its errand on %q (hull now unattributable): %w",
			r.ship, target, r.system(), cause)
	}
	return fmt.Errorf("failed to retarget seed %s onto %q (errand on %q restored): %w",
		r.ship, target, r.system(), cause)
}

// standDownAsSpare parks a finished seed where it stands, as a reserve hull the buy
// queue can re-task for free. The placement it writes is what puts the probe back on
// the books: for the length of the errand the hull was named only by its errand row.
//
// An unfilled placement on the very waypoint the hull is standing on is taken
// INSTEAD, and takes precedence over the whole-system verdict that got us here: a
// system can be rejected as a whole and still hold one market worth watching — the
// seed's own market reads write wants directly, before any verdict.
//
// A waypoint carrying any OTHER placement is left strictly alone, since overwriting
// it would reassign the hull that row names; the seed is stood down DONE instead,
// keeping this hull named by its errand row rather than by nothing at all. That
// branch should be unreachable — a waypoint the seed had to CHART cannot already
// hold a filled placement — so it is logged rather than handled silently.
func (t *expandTick) standDownAsSpare(ctx context.Context, r chartSeedRun, pos ShipPos, current string) error {
	if want, wanted := t.book.wantedAt(current, pos.Waypoint); wanted {
		filled, err := t.fillPlacement(ctx, r, current, []QueuedSlot{want})
		if err != nil || filled {
			return err
		}
	}

	// Asked of the SPARE half only. This branch strands a hull: it stands the seed
	// down with NO placement row, so the probe cap stops counting a probe we own —
	// the money-unsafe direction, and why it is logged rather than passed over. The
	// test is kind-scoped, so only a genuine SPARE-on-SPARE collision lands here.
	if t.book.occupied(pos.Waypoint, SlotKindSpare) {
		logging.LoggerFromContext(ctx).Log("WARN", "charting seed finished on a waypoint that already holds a spare placement; standing it down without a slot", map[string]interface{}{
			"action":      "parked_sensing_seed_standdown_blocked",
			"ship_symbol": r.ship,
			"waypoint":    pos.Waypoint,
			"system":      r.system(),
		})
		if err := t.restampErrand(ctx, r, SeedStateDone); err != nil {
			return fmt.Errorf("failed to stand seed %s down on %q: %w", r.ship, r.system(), err)
		}
		t.roster.leave(r.system(), r.ship)
		return nil
	}

	if err := t.p.Ledger.UpsertSpareSlot(ctx, t.playerID, SlotRecord{
		Waypoint:     pos.Waypoint,
		System:       current,
		Kind:         SlotKindSpare,
		State:        SlotStateParked,
		AssignedShip: r.ship,
	}); err != nil {
		return fmt.Errorf("failed to park seed %s as a spare at %q: %w", r.ship, pos.Waypoint, err)
	}
	t.book.addSpare(current, pos.Waypoint, SlotStateParked)
	if err := t.clearErrand(ctx, r); err != nil {
		return fmt.Errorf(
			"seed %s parked as a spare at %s but its errand on %q was not cleared (hull double-counted, probe cap reads high): %w",
			r.ship, pos.Waypoint, r.system(), err)
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

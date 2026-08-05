package services

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// FeedResult is one feed leg's outcome: where the inputs went, how much arrived, and — when the
// destination refused them — that the leg declined rather than delivered.
//
// Refused is a distinct field, not an error and not a zero. A leg that skipped silently and one
// that delivered nothing look identical from a zero-unit result, and telling those two apart is
// the entire reason this design exists.
//
// UnitsDelivered is the units the market actually TOOK (summed from the sell responses), never
// the units offered off the hold. Where a market fills a lot only partially the two differ, and
// the offered figure would report a fed factory that is still starving.
type FeedResult struct {
	WaypointSymbol string
	UnitsDelivered int
	Revenue        int
	Refused        bool
}

// FeedFactory delivers a hull's inputs INTO a PINNED factory — the waypoint the caller already
// resolved by market role — and reports what arrived.
//
// It deliberately does NOT resolve a destination. The caller resolved it by role; re-deciding
// here would silently undo that, exactly as selecting a source inside BuyAtTerminalFactory would
// undo the terminal-factory pinning. Refusing an unusable request is correct; substituting
// another waypoint is the failure mode this phase exists to prevent.
//
// `inputs` IS THE TRIP'S SUBJECT, NOT AN INVENTORY OF THE HOLD, and the difference is
// load-bearing in both directions:
//
//   - It is what the sp-b27a2 guard judges. ValidateFeedDestination refuses the NAVIGATE unless
//     the destination imports EVERY good named here, so a caller that named its whole hold would
//     refuse trips over unrelated cargo the hull merely happens to carry — reversing sp-w2qg5,
//     whose ruling is that unsellable cargo aboard RIDES ON rather than vetoing the trip. Callers
//     therefore name what this run acquired FOR THIS FACTORY: the fabricate path passes
//     run.haulingInputs(), the gate FACTORY leg passes the input it just bought for the step.
//
//   - The delivery underneath does NOT restrict itself to that list. deliverInputs offers the
//     WHOLE HOLD and decides good-by-good against the destination's own listing (marketBuys), so
//     a factory can receive more than is named here — but only goods it genuinely imports. Its
//     own EXPORT is refused, which is why a hull carrying a gate material must be flushed
//     elsewhere rather than expecting a factory to take it back, and anything the market does not
//     list is held aboard.
//
// IT ISSUES NO PURCHASE. Feeding a factory is a SELL into its import listing. The BUY that put the
// inputs aboard is BuyAtTerminalFactory's, which routes through the shared fillFromSource tranche
// loop where the working-capital floor (spendFloorBreached, re-read against live treasury) and
// the cross-container concurrent-spend reservation are re-checked EVERY iteration and both fail
// closed. Keeping the buy there means this phase adds no second spend primitive and edits none of
// the existing one (RULINGS #4). The floor in force is defaultWorkingCapitalReserve —
// common.NonContractWorkingCapitalFloor, the 150k non-contract band, NOT the 50k base — raised
// further by the per-operation capital budget when a work sensor is wired.
//
// That is a claim about MARKET SPEND and nothing wider: the NavigateAndDock below is an ordinary
// movement, so refuel and jump-gate fees ride on it exactly as they do on every other leg. What
// this function must never do is open a second purchasing path around the guard stack.
//
// THE sp-b27a2 GUARD RUNS BEFORE THE NAVIGATE, through the SAME feedDestinationRefusedFor the
// fabricate path uses. That incident dispatched IRON_ORE to a waypoint which did not import it,
// and the haulers then sat at 80/80 unable to deliver OR dump. Checking the destination's own
// listing before flying is the root-cause fix; deliverInputs' hold-what-it-cannot-sell behaviour
// only limits the damage after the hull is already at the wrong waypoint.
//
// Refuses (error, nil result) on a nil or unnamed destination and on an empty input list. The
// last one matters: ValidateFeedDestination accepts an empty list by design (carrying nothing
// cannot strand anything), so without this check a caller bug would fly a hull across a system to
// deliver nothing.
func (e *ProductionExecutor) FeedFactory(
	ctx context.Context,
	ship *navigation.Ship,
	destination *MarketLocatorResult,
	inputs []string,
	playerID int,
	opContext *shared.OperationContext,
) (*FeedResult, error) {
	// ship is checked with its siblings, not left to blow up at the navigate. A nil hull reaches
	// ShipSymbol() only AFTER the destination guard has run, so it panics on exactly the accepted
	// path and returns cleanly on the refused one — a caller bug that surfaces as a daemon crash in
	// the good case and as silence in the bad one.
	if ship == nil {
		return nil, fmt.Errorf("cannot feed: no ship was given for the trip")
	}
	if destination == nil {
		return nil, fmt.Errorf("cannot feed: no factory was resolved — refusing to pick a destination here")
	}
	if destination.WaypointSymbol == "" {
		return nil, fmt.Errorf("cannot feed: the resolved factory has no waypoint")
	}
	if len(inputs) == 0 {
		return nil, fmt.Errorf("cannot feed %s: no inputs were named for the trip", destination.WaypointSymbol)
	}

	if opContext != nil && opContext.IsValid() {
		ctx = shared.WithOperationContext(ctx, opContext)
	}
	playerIDValue := shared.MustNewPlayerID(playerID)

	if e.feedDestinationRefusedFor(ctx, inputs, destination.WaypointSymbol, playerIDValue) {
		return &FeedResult{WaypointSymbol: destination.WaypointSymbol, Refused: true}, nil
	}

	updatedShip, err := e.NavigateAndDock(ctx, ship.ShipSymbol(), destination.WaypointSymbol, playerIDValue)
	if err != nil {
		return nil, fmt.Errorf("failed to navigate to the feed target %s: %w", destination.WaypointSymbol, err)
	}

	// The factory IMPORTS these goods, so delivering them is a sell. deliverInputs holds back
	// anything this market will not take rather than aborting the whole delivery — a refused sell
	// must not poison the rest of the hold.
	revenue, units := e.deliverInputs(ctx, updatedShip, playerIDValue)
	return &FeedResult{WaypointSymbol: destination.WaypointSymbol, UnitsDelivered: units, Revenue: revenue}, nil
}

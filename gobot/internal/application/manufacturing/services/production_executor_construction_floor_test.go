package services

import (
	"context"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// sp-lpy9i: THE SHRINK FLOOR BELONGS TO THE SINK, NOT TO THE LOOP.
//
// The live defect these pin: the gate DELIVERY fleet buys a terminal factory's output and hands it
// to a construction site, which consumes it against a fixed bill. It shared fillFromSource with the
// gate FACTORY fleet, whose buys are sold into a factory's import listing and must clear that
// factory's activity-saturation point to be worth the hull-hours. One constant served both, so the
// delivery fleet inherited a 25-unit floor that means nothing at a construction site: 52 declines
// over 2h33m with 80 units left on the bill and 17 affordable the whole time.
//
// FIXTURE ARITHMETIC, stated because every assertion below depends on it: newPinnedSourceExecutor
// builds a 40-unit hull, and pinnedSourceMarketRepo quotes the live ask at 10. So the construction
// floor is 40/constructionMinHullFraction = 5 units, against the factory floor's 25 — and headroom
// of 100 credits affords exactly 10 units, which falls BETWEEN the two floors. That gap is what
// makes the pair below a discriminating test rather than two tests that happen to agree.

// THE BEAD. One fixture, one treasury, one affordable quantity — and the sink alone decides whether
// the leg buys. Against the pre-fix loop the construction case returns 0 like the factory case.
func TestFillFromSource_AConstructionSinkBuysWhatAFactorySinkWouldRefuse(t *testing.T) {
	buyWithSink := func(t *testing.T, sink TrancheSink) (int, string) {
		t.Helper()
		// headroom 100 at an ask of 10 affords 10 units: over the 5-unit construction floor,
		// under the 25-unit factory floor.
		client := &sequentialCreditsAPIClient{credits: []int{effectiveReserveFloor() + 100}}
		executor, repo, _ := newPinnedSourceExecutor(t, client)
		logger := &dwellCapturingLogger{}
		ctx := common.WithLogger(common.WithPlayerToken(context.Background(), "TOKEN-SINK"), logger)

		result, err := executor.BuyAtTerminalFactory(ctx, repo.buildShip(),
			dockRaceGood, liveStallSource(), 40, "X1-DR", 1, nil, sink)
		if err != nil {
			t.Fatalf("BuyAtTerminalFactory returned error: %v", err)
		}
		return result.QuantityAcquired, dwellLogText(logger)
	}

	construction, constructionLog := buyWithSink(t, SinkConstructionSite)
	if construction == 0 {
		t.Fatalf("a CONSTRUCTION sink acquired nothing while 10 units were affordable. A construction site has no activity to move — it has a bill, and 10 units is 10 units of permanent progress. This is the 2h33m of declines sp-lpy9i was filed on:\n%s", constructionLog)
	}
	if construction != 10 {
		t.Fatalf("acquired %d, want the 10 units the reserve affords: the shrink must size to the reserve, not past it", construction)
	}

	factory, _ := buyWithSink(t, SinkFactoryFeed)
	if factory != 0 {
		t.Fatalf("a FACTORY sink acquired %d, but 10 units is below the %d-unit min-effective delivery and moves that factory's activity nothing. sp-lpy9i lowers the CONSTRUCTION floor only; the feeding floor is unchanged and its re-justification is explicitly out of scope", factory, minViableTrancheUnits)
	}

	// The whole point is that ONE input differed. If both sinks agreed, this file would be pinning
	// nothing at all.
	if construction == factory {
		t.Fatal("both sinks behaved identically — this test cannot detect the defect it exists for")
	}
}

// CRITERION 4/5, THE ANTI-SPAM DIRECTION. Lowering the floor is not removing it: a trickle too
// small to be worth a round trip must still decline, or a large bill turns into a spam of
// near-empty legs.
func TestFillFromSource_AConstructionSinkStillDeclinesATrickleTooSmallForATrip(t *testing.T) {
	// headroom 30 at an ask of 10 affords 3 units — under the 5-unit construction floor.
	client := &sequentialCreditsAPIClient{credits: []int{effectiveReserveFloor() + 30}}
	executor, repo, mediator := newPinnedSourceExecutor(t, client)
	logger := &dwellCapturingLogger{}
	ctx := common.WithLogger(common.WithPlayerToken(context.Background(), "TOKEN-TRICKLE"), logger)

	result, err := executor.BuyAtTerminalFactory(ctx, repo.buildShip(),
		dockRaceGood, liveStallSource(), 40, "X1-DR", 1, nil, SinkConstructionSite)
	if err != nil {
		t.Fatalf("BuyAtTerminalFactory returned error: %v", err)
	}
	if result.QuantityAcquired != 0 {
		t.Fatalf("acquired %d on 3 affordable units against a 5-unit trip floor. The floor is LOWER for a construction sink, not absent — a hull round trip carrying 3 units of a 1600-unit bill is the trip-count inflation constructionMinHullFraction exists to bound", result.QuantityAcquired)
	}
	if mediator.purchaseAttempts() != 0 {
		t.Fatalf("purchaseAttempts = %d; a declined trickle must not reach the market", mediator.purchaseAttempts())
	}
}

// RULINGS #4, THE DIRECTION. The sink can only ever LOWER the floor, and only for construction.
// A factory sink must be byte-identical to sp-xcjuy at every hull size.
func TestMinTrancheUnitsFor_OnlyEverLowersAndOnlyForConstruction(t *testing.T) {
	for _, capacity := range []int{0, 1, 7, 8, 40, 80, 120, 400} {
		if got := minTrancheUnitsFor(SinkFactoryFeed, capacity); got != minViableTrancheUnits {
			t.Fatalf("capacity %d: factory floor is %d, want %d unchanged — sp-lpy9i lowers the construction floor ONLY", capacity, got, minViableTrancheUnits)
		}
		got := minTrancheUnitsFor(SinkConstructionSite, capacity)
		if got > minViableTrancheUnits {
			t.Fatalf("capacity %d: construction floor %d EXCEEDS the factory floor %d. This may only ever lower a floor (RULINGS #4); raising one would refuse buys that work today", capacity, got, minViableTrancheUnits)
		}
		if got < 1 {
			t.Fatalf("capacity %d: floor %d — a floor below 1 unit makes every buy trivially viable and removes the anti-spam bound entirely", capacity, got)
		}
	}

	// The zero value must be the STRICTER floor, so a caller that never chooses keeps today's
	// behaviour rather than silently opting into the looser one.
	var unchosen TrancheSink
	if minTrancheUnitsFor(unchosen, 40) != minViableTrancheUnits {
		t.Fatal("the zero-value sink does not resolve to the factory floor; an unstamped caller must inherit the STRICTER floor, never the looser")
	}
}

// THE CLAMP THAT IS NOT THERE, pinned so nobody adds it back believing it protects something.
//
// "Never floor higher than what is left of the bill" reads as obviously right and is provably
// inert: this floor is consulted ONLY after a breach, and a breach means trancheQty*ask > headroom,
// so affordable == min(trancheQty, headroom/ask) is STRICTLY below trancheQty, which is itself
// <= tripTarget. Wherever the clamp could bind, affordable is already under it.
func TestMinTrancheUnitsFor_AClampToTheRemainingBillCouldNotChangeAnyOutcome(t *testing.T) {
	bound := 0
	for _, capacity := range []int{8, 40, 80, 400} {
		for _, tripTarget := range []int{1, 2, 5, 9, 40, 200} {
			for _, ask := range []int{1, 7, 10, 4044} {
				for _, headroom := range []int{0, 1, 30, 100, 999} {
					trancheQty := min(capacity, tripTarget)
					if trancheQty <= 0 || trancheQty*ask <= headroom {
						continue // no breach: the floor is never consulted at all
					}
					affordable := min(trancheQty, headroom/ask)
					unclamped := minTrancheUnitsFor(SinkConstructionSite, capacity)
					clamped := min(unclamped, tripTarget)
					if clamped < unclamped {
						bound++
					}
					if (affordable >= unclamped) != (affordable >= clamped) {
						t.Fatalf("capacity=%d tripTarget=%d ask=%d headroom=%d: clamping the floor from %d to %d CHANGED the outcome (affordable=%d). The clamp is not inert after all and the comment on minTrancheUnitsFor is wrong",
							capacity, tripTarget, ask, headroom, unclamped, clamped, affordable)
					}
				}
			}
		}
	}
	// A grid where the clamp never binds would prove nothing — it would pass vacuously.
	if bound == 0 {
		t.Fatal("no grid point had the clamp binding (tripTarget below the floor), so this test cannot show inertness; widen the grid")
	}
}

// CRITERION 5: the decline names its binding constraint BEFORE any truncation, and names the
// sink's own floor rather than a constant that no longer governs every caller.
func TestFillFromSource_TheDeclineNamesTheSinksOwnFloorNotTheFactoryConstant(t *testing.T) {
	client := &sequentialCreditsAPIClient{credits: []int{effectiveReserveFloor() + 30}}
	executor, repo, _ := newPinnedSourceExecutor(t, client)
	logger := &dwellCapturingLogger{}
	ctx := common.WithLogger(common.WithPlayerToken(context.Background(), "TOKEN-LOGFLOOR"), logger)

	if _, err := executor.BuyAtTerminalFactory(ctx, repo.buildShip(),
		dockRaceGood, liveStallSource(), 40, "X1-DR", 1, nil, SinkConstructionSite); err != nil {
		t.Fatalf("BuyAtTerminalFactory returned error: %v", err)
	}

	text := dwellLogText(logger)
	// The construction floor at this hull is 5. Reporting 25 would be the old constant leaking into
	// a line about a sink it no longer governs — and a reader would size the shortfall wrongly.
	if !strings.Contains(text, "below the 5-unit minimum") {
		t.Fatalf("the decline does not name the CONSTRUCTION floor of 5 for this 40-unit hull:\n%s", text)
	}
	if strings.Contains(text, "below the 25-unit minimum") {
		t.Fatalf("the decline reports the FACTORY floor of 25 on a construction buy; that constant no longer governs this sink:\n%s", text)
	}
}

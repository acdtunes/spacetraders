package services

import (
	"context"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// sp-xcjuy: A TRANCHE TOO BIG FOR THE RESERVE SHRINKS; IT DOES NOT KILL THE FILL.
//
// The live stall these pin: FAB_MATS 1301/1600, no progress for 78 minutes, because IRON at
// X1-KP46-H51 had risen to 4,044/unit and a full 60-unit tranche cost 242,640 against a 460,373
// reserve on a 648,388 treasury. Treasury was FALLING across the stall, so waiting for reserve plus
// a full tranche to become affordable was moving further away, not closer.

// liveStallSource mirrors that market: the ask, the trade volume, the supply.
func liveStallSource() *MarketLocatorResult {
	return &MarketLocatorResult{
		WaypointSymbol: pinnedFactoryWP,
		Supply:         supplyHigh,
		Activity:       "STRONG",
		Price:          4044,
		TradeVolume:    60,
	}
}

// THE BEAD. A reserve that cannot absorb the full tranche but can absorb a useful one must buy the
// useful one. Against the unshrunk loop this fails at zero acquired.
func TestFillFromSource_ShrinksABreachingTrancheInsteadOfKillingTheFill(t *testing.T) {
	// THE INCIDENT'S SHAPE, at this fixture's economics. The live case was 60 units at 4,044
	// against 188,015 of headroom; here it is 40 units at the fixture's live ask of 10 against 300
	// of headroom. Both are "the full tranche breaches, a useful smaller one does not" — the ratio
	// is what matters, and the fixture's market repo re-reads the live ask on the hull-fill path,
	// so the source's own Price is not what the loop prices against.
	//
	// 40 x 10 = 400 > 300 headroom -> breach. 300/10 = 30 units affordable, which clears the
	// 25-unit min-effective floor -> the tranche resizes to 30 instead of the fill dying at zero.
	client := &sequentialCreditsAPIClient{credits: []int{effectiveReserveFloor() + 300}}
	executor, repo, mediator := newPinnedSourceExecutor(t, client)
	logger := &dwellCapturingLogger{}
	ctx := common.WithLogger(common.WithPlayerToken(context.Background(), "TOKEN-SHRINK"), logger)

	result, err := executor.BuyAtTerminalFactory(ctx, repo.buildShip(),
		dockRaceGood, liveStallSource(), 40, "X1-DR", 1, nil, SinkFactoryFeed)
	if err != nil {
		t.Fatalf("BuyAtTerminalFactory returned error: %v", err)
	}

	if result.QuantityAcquired == 0 {
		t.Fatal("acquired NOTHING. The full tranche breaches the reserve but a 30-unit one fits — declining the whole step is the 78-minute gate stall, where a 60-unit IRON tranche was refused while a 25-unit one had 100k to spare")
	}
	if result.QuantityAcquired < minViableTrancheUnits {
		t.Fatalf("acquired %d, below the %d-unit min-effective delivery — a dribble is wasted hull-hours", result.QuantityAcquired, minViableTrancheUnits)
	}
	if mediator.purchaseAttempts() == 0 {
		t.Fatal("no purchase was attempted at all")
	}
	if want := "Resized the"; !strings.Contains(dwellLogText(logger), want) {
		t.Fatalf("no %q line: the fill must say it went smaller, or 'progress, just smaller' is indistinguishable from a park:\n%s", want, dwellLogText(logger))
	}
}

// RULINGS #4, THE DIRECTION. The resize only ever makes a buy SMALLER, and the commit-time guard
// still decides. A treasury below the reserve admits NOTHING — no shrink can rescue it.
func TestFillFromSource_SizesDownButNeverPastTheGuard(t *testing.T) {
	// Treasury under the floor: headroom is negative, so no tranche of any size can clear.
	client := &sequentialCreditsAPIClient{credits: []int{effectiveReserveFloor() - 1}}
	executor, repo, mediator := newPinnedSourceExecutor(t, client)
	logger := &dwellCapturingLogger{}
	ctx := common.WithLogger(common.WithPlayerToken(context.Background(), "TOKEN-NOROOM"), logger)

	result, err := executor.BuyAtTerminalFactory(ctx, repo.buildShip(),
		dockRaceGood, liveStallSource(), 60, "X1-DR", 1, nil, SinkFactoryFeed)
	if err != nil {
		t.Fatalf("BuyAtTerminalFactory returned error: %v", err)
	}
	if result.QuantityAcquired != 0 {
		t.Fatalf("acquired %d below the working-capital floor. The resize proposes a SIZE; it must never let a buy past the guard (RULINGS #4)", result.QuantityAcquired)
	}
	if mediator.purchaseAttempts() != 0 {
		t.Fatalf("purchaseAttempts = %d — nothing may be bought when the treasury is under the reserve", mediator.purchaseAttempts())
	}
}

// ACCEPTANCE CRITERION 5: the decline that means "no USEFUL tranche fits" must be distinguishable
// from the ordinary "the next tranche did not fit". Conflating them is how the stall read as
// routine tranche accounting for 78 minutes.
func TestFillFromSource_NamesTheDeclineWhenEvenTheMinimumTrancheCannotFit(t *testing.T) {
	client := &sequentialCreditsAPIClient{credits: []int{effectiveReserveFloor() - 1}}
	executor, repo, _ := newPinnedSourceExecutor(t, client)
	logger := &dwellCapturingLogger{}
	ctx := common.WithLogger(common.WithPlayerToken(context.Background(), "TOKEN-MIN"), logger)

	if _, err := executor.BuyAtTerminalFactory(ctx, repo.buildShip(),
		dockRaceGood, liveStallSource(), 60, "X1-DR", 1, nil, SinkFactoryFeed); err != nil {
		t.Fatalf("BuyAtTerminalFactory returned error: %v", err)
	}

	text := dwellLogText(logger)
	// The wording moved in sp-lpy9i so the two deciding numbers lead the line rather than trailing
	// it (a 140-char truncation of the old phrasing read as though the ATTEMPTED tranche were the
	// minimum, and that misreading became sp-jt14b). The pin is unchanged in substance: this
	// decline must still be distinguishable from the ordinary tranche stop.
	if want := "below the"; !strings.Contains(text, want) {
		t.Fatalf("no %q line — this decline is a statement about the TREASURY, not about this buy, and must not share wording with the ordinary tranche stop:\n%s", want, text)
	}
	// AND it must not read like the ordinary stop, whose whole text is the breach arithmetic.
	if ordinary := "would breach working-capital reserve"; strings.Contains(text, ordinary) {
		t.Fatalf("the too-tight decline is using the ORDINARY stop's wording (%q); conflating them is how the 78-minute stall read as routine tranche accounting:\n%s", ordinary, text)
	}
	// CRITERION 5: the two numbers that decide the outcome must appear before any truncation.
	// Measured WITHIN the decline's own line — a truncation is applied per line, so an offset taken
	// across the whole captured log would count unrelated preceding lines and fail for the wrong
	// reason (it did, at char 216, on its first run).
	var decline string
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "Parked input purchase") {
			decline = line
			break
		}
	}
	if decline == "" {
		t.Fatalf("no 'Parked input purchase' line to measure:\n%s", text)
	}
	if idx := strings.Index(decline, "below the"); idx < 0 || idx > 140 {
		t.Fatalf("the binding constraint appears at char %d of its own line, past the 140-char truncation that produced sp-jt14b's false premise:\n%s", idx, decline)
	}
	if !strings.Contains(text, "spend_floor_below_min_tranche") && !strings.Contains(text, "minimum") {
		t.Fatalf("the decline does not name the minimum-tranche reason:\n%s", text)
	}
}

// REGRESSION (criterion 4): a fully affordable step still buys the full tranche, and the resize
// path is never entered.
func TestFillFromSource_AnAffordableTrancheIsUnchangedAndNeverResized(t *testing.T) {
	// Comfortably clear: the whole 60-unit fill costs 242,640.
	client := &sequentialCreditsAPIClient{credits: []int{effectiveReserveFloor() + 5_000_000}}
	executor, repo, mediator := newPinnedSourceExecutor(t, client)
	logger := &dwellCapturingLogger{}
	ctx := common.WithLogger(common.WithPlayerToken(context.Background(), "TOKEN-RICH"), logger)

	result, err := executor.BuyAtTerminalFactory(ctx, repo.buildShip(),
		dockRaceGood, liveStallSource(), 40, "X1-DR", 1, nil, SinkFactoryFeed)
	if err != nil {
		t.Fatalf("BuyAtTerminalFactory returned error: %v", err)
	}
	if result.QuantityAcquired != 40 {
		t.Fatalf("acquired %d of an affordable 40-unit allocation — an affordable fill must be byte-identical to before the resize existed", result.QuantityAcquired)
	}
	if mediator.purchaseAttempts() == 0 {
		t.Fatal("no purchase attempted on a fully affordable fill")
	}
	text := dwellLogText(logger)
	if strings.Contains(text, "Resized the") {
		t.Fatalf("an affordable tranche was resized:\n%s", text)
	}
	if strings.Contains(text, "UNAFFORDABLE EVEN AT THE MINIMUM") {
		t.Fatalf("an affordable tranche logged a minimum-tranche decline:\n%s", text)
	}
}

// THE FLOOR IS DERIVED, NOT PICKED. It is the feed side's own min-effective delivery, so the buy
// side and the feed side agree on what "too small to matter" means.
func TestMinViableTranche_IsTheFeedSidesMinEffectiveDelivery(t *testing.T) {
	if minViableTrancheUnits != defaultFeedSaturationMinUnits {
		t.Fatalf("minViableTrancheUnits = %d but the feed side's min-effective delivery is %d. A buy floor BELOW it purchases amounts the feeding policy has measured as moving activity nothing; a buy floor ABOVE it refuses buys the feed side considers effective",
			minViableTrancheUnits, defaultFeedSaturationMinUnits)
	}
	if MinViableTrancheUnits != minViableTrancheUnits {
		t.Fatalf("the exported floor (%d) and the internal one (%d) disagree — the gate feed's precheck would price a different quantity than the buy shrinks to, which is the stall again",
			MinViableTrancheUnits, minViableTrancheUnits)
	}
}

// dwellLogText flattens the captured container log so a test can assert on a line the executor
// emits rather than on a return value it does not have.
func dwellLogText(l *dwellCapturingLogger) string {
	parts := make([]string, 0, len(l.entries))
	for _, e := range l.entries {
		parts = append(parts, e.level+" "+e.message)
	}
	return strings.Join(parts, "\n")
}

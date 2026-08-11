package commands

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
)

// A tick gathers its facts EARLY and reads the yard ask LATE, at decision time, so a context dying
// mid-tick hits the price arm alone and the decision reads as a considered refusal on price.

// A DYING TICK PUBLISHES NO VERDICT AT ALL: no decision line, and nothing fed to the stall
// escalator, whose streak is meant to detect genuine blockage rather than shutdowns.
func TestGrowthReconcile_CancelledTickPublishesNoDecisionAndNoStallVerdict(t *testing.T) {
	h, obs := blockedGrowthHandler(t)
	logger := &capturingLogger{}
	ctx, cancel := context.WithCancel(logging.WithLogger(context.Background(), logger))
	cancel()

	if _, err := h.reconcileOnce(ctx, growthCmd()); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}

	if logger.sawAction("growth_decision") {
		t.Fatalf("a cancelled tick must publish no buy decision:\n%s", logger.joined())
	}
	if heavy := obs.forScope(string(HullClassHeavy)); len(heavy) != 0 {
		t.Fatalf("a cancelled tick must feed the stall escalator nothing, got %+v", heavy)
	}
	// The gap must be NAMED, or a truncated tick is indistinguishable from a dead coordinator.
	if !logger.sawAction("growth_tick_abandoned") {
		t.Fatalf("an abandoned tick must say so:\n%s", logger.joined())
	}

	// CALIBRATION. The same handler on a LIVE context does decide and does report, so the silence
	// above is the bail and not a fixture that never reached the buy.
	live := &capturingLogger{}
	if _, err := h.reconcileOnce(logging.WithLogger(context.Background(), live), growthCmd()); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if !live.sawAction("growth_decision") {
		t.Fatalf("the fixture never reached the buy decision, so the cancelled case proves nothing:\n%s", live.joined())
	}
	if heavy := obs.forScope(string(HullClassHeavy)); len(heavy) != 1 {
		t.Fatalf("a live tick must report exactly one heavy verdict, got %+v", heavy)
	}
}

// THE DECISION LINE MUST NAME WHICH UNREADABLE IT IS. Both cases fail CLOSED and both must keep
// doing so (RULINGS #4); only the wording tells an operator whether to look at a shipyard or a log.
func TestGrowthDecision_NamesAFailedAskReadApartFromNoYardSellingIt(t *testing.T) {
	for _, tc := range []struct {
		name  string
		price *fakeYardPrice
		names string
	}{
		{"the read failed", &fakeYardPrice{err: errors.New("context canceled")}, "the ask could not be read"},
		{"nothing sells it", &fakeYardPrice{}, "no yard we stand on sells it"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := blockedGrowthHandler(t)
			h.SetYardPriceReader(tc.price)
			logger := &capturingLogger{}

			if _, err := h.reconcileOnce(logging.WithLogger(context.Background(), logger), growthCmd()); err != nil {
				t.Fatalf("reconcileOnce error: %v", err)
			}

			line := logger.joined()
			if !strings.Contains(line, "price[BLOCK:") {
				t.Fatalf("an unreadable ask must still refuse the buy (RULINGS #4):\n%s", line)
			}
			if !strings.Contains(line, tc.names) {
				t.Fatalf("the decision line must name %q:\n%s", tc.names, line)
			}
		})
	}
}

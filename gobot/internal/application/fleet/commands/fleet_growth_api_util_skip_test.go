package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/health"
	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
)

// The api_util rung is judged BEFORE the yard walk that prices the hull, because the walk's own
// requests are part of the utilisation the ceiling refuses.

// overCeilingAPIUtil is a utilisation the shipped ceiling refuses outright; underCeilingAPIUtil is
// one it admits. Named so a reader can see which side of the ceiling each case sits on.
const (
	overCeilingAPIUtil  = 238.5
	underCeilingAPIUtil = 10
)

// armedAtAPIUtil is armedForHeavy with ONE input moved: every other rung clears with margin, so
// api_util alone decides and the counted walk is unambiguous.
func armedAtAPIUtil(t *testing.T, pct float64) (*RunFleetGrowthCoordinatorHandler, *growthPurchaseRecorder, *growthBlockRecorder, *growthYardPriceCounter) {
	t.Helper()
	h, buyer, blocks := armedForHeavy(t, growthFixture{})
	yards := &growthYardPriceCounter{ask: 1_400_000}
	h.SetYardPriceReader(yards)
	h.SetAPIUtilizationReader(&fakeGrowthAPIUtil{pct: pct})
	return h, buyer, blocks, yards
}

// A tick the ceiling already refuses must read no shipyard: the walk is discretionary spending of
// the very budget the rung is protecting, and its answer is discarded either way.
func TestGrowthHeavyBuy_OverTheAPICeilingReadsNoShipyard(t *testing.T) {
	h, buyer, _, yards := armedAtAPIUtil(t, overCeilingAPIUtil)

	if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if yards.calls != 0 {
		t.Fatalf("a tick the api_util ceiling refuses must walk no shipyard, got %d PriceFor calls", yards.calls)
	}
	if buyer.calls != 0 {
		t.Fatalf("the ceiling must still refuse the buy, bought %d", buyer.calls)
	}
}

// A refusal that costs nothing must still BE a refusal an operator can read and an escalator can
// count. Silence here is indistinguishable from a dead coordinator.
func TestGrowthHeavyBuy_OverTheAPICeilingStillPublishesItsRefusal(t *testing.T) {
	h, _, blocks, _ := armedAtAPIUtil(t, overCeilingAPIUtil)
	obs := &recordingStallObserver{}
	h.SetStallObserver(obs)
	logger := &capturingLogger{}

	if _, err := h.reconcileOnce(logging.WithLogger(context.Background(), logger), growthCmd()); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}

	line := logger.joined()
	if !logger.sawAction("growth_decision") {
		t.Fatalf("a blocked tick must publish its decision, not go silent:\n%s", line)
	}
	if !strings.Contains(line, "api_util[BLOCK:") {
		t.Fatalf("the decision line must name the rung that refused, with its arithmetic:\n%s", line)
	}
	if len(blocks.blocked) != 1 || blocks.blocked[0] != GuardAPIUtil {
		t.Fatalf("blocked guards = %v, want api_util metered exactly once", blocks.blocked)
	}
	heavy := obs.forScope(string(HullClassHeavy))
	if len(heavy) != 1 || heavy[0].Outcome != health.StallBlocked {
		t.Fatalf("the escalator must still see one BLOCKED tick, got %+v", heavy)
	}
	if heavy[0].Reason != health.StallReason(GuardAPIUtil) {
		t.Fatalf("the BLOCKED verdict must still name api_util, got %q", heavy[0].Reason)
	}
}

// CALIBRATION: the skip is conditional. The same tick under the ceiling walks the yard and buys, so
// what changed is the ORDER the rungs are asked in and not the fleet's ability to grow.
func TestGrowthHeavyBuy_UnderTheAPICeilingStillWalksAndBuys(t *testing.T) {
	h, buyer, blocks, yards := armedAtAPIUtil(t, underCeilingAPIUtil)

	if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if yards.calls != 1 {
		t.Fatalf("under the ceiling the tick must still price the hull, got %d PriceFor calls", yards.calls)
	}
	if buyer.calls != 1 {
		t.Fatalf("under the ceiling the tick must still buy, got %d (blocked by %v)", buyer.calls, blocks.blocked)
	}
}

// THE RUNG ASKED FIRST IS THE RUNG NAMED. Over the ceiling with the ask unreadable too, the
// refusal is attributed to api_util — the price was never asked for, so it cannot be the cause.
func TestGrowthHeavyBuy_OverTheAPICeilingNamesItselfWhenNothingIsPriceableEither(t *testing.T) {
	h, buyer, blocks, _ := armedAtAPIUtil(t, overCeilingAPIUtil)
	h.SetYardPriceReader(&growthYardPriceCounter{}) // no reachable yard sells the hull

	if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if buyer.calls != 0 {
		t.Fatalf("both rungs refuse this tick, bought %d", buyer.calls)
	}
	if len(blocks.blocked) != 1 || blocks.blocked[0] != GuardAPIUtil {
		t.Fatalf("blocked guards = %v, want api_util: the rung ahead of the walk is the one that decided", blocks.blocked)
	}
}

// The zero-effect alarm counts a skipped-walk refusal exactly as it counted a walked one: unmet
// demand that bought nothing, streak after streak, until it pages once.
func TestGrowthHeavyBuy_OverTheAPICeilingStillFeedsTheZeroEffectAlarm(t *testing.T) {
	h, _, _, _ := armedAtAPIUtil(t, overCeilingAPIUtil)
	logger := &capturingLogger{}
	cmd := growthCmd()
	ctx := logging.WithLogger(context.Background(), logger)

	for i := 0; i < defaultGrowthZeroEffectAlarmTicks; i++ {
		if _, err := h.reconcileOnce(ctx, cmd); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}

	if !logger.sawAction("growth_zero_effect_alarm") {
		t.Fatalf("a persistently refused shortfall must still raise the alarm:\n%s", logger.joined())
	}
}

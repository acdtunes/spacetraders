package gate

import (
	"strings"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-vrnjx: A PAUSE BELOW THE BUY FLOOR WAS COMPLETELY INVISIBLE.
//
// sp-c9wuu anchored its clock on REACHING the buy floor, which covers supply plateauing BETWEEN the
// floors. It does not cover supply sitting strictly BELOW the buy floor: the clock never anchors, so
// `held` stays 0 and BOTH signals are unreachable. Live, F45 FAB_MATS sat SCARCE against a LIMITED
// floor for 3.5 hours with suspected_stuck = 0 and resumed_by_timeout = 0.

// belowFloorPolicy pins supply strictly BELOW the buy floor. CRITERION 2: a fixture that reaches the
// floor passes vacuously — that is exactly how this gap survived sp-c9wuu — so the guard below
// asserts the fixture really is below it.
func belowFloorPolicy(t *testing.T) (*BuyPolicy, *shared.MockClock) {
	t.Helper()
	clock := &shared.MockClock{CurrentTime: time.Unix(0, 0).UTC()}
	policy := NewBuyPolicyWithClock(shared.SupplyLevelLimited, shared.SupplyLevelModerate, clock)
	if shared.SupplyLevelScarce.Order() >= shared.SupplyLevelLimited.Order() {
		t.Fatal("fixture is inert: SCARCE is not below the LIMITED buy floor, so this cannot exercise the below-floor path at all")
	}
	return policy, clock
}

// CRITERION 1 (the observable half): a pause whose supply NEVER reaches the buy floor must still
// escalate. Against the unmodified policy this fails — SuspectedStuck keys on the buy-floor clock,
// which never anchors here.
func TestBuyPolicy_APauseBelowTheBuyFloorStillEscalates(t *testing.T) {
	policy, clock := belowFloorPolicy(t)

	// Drained to SCARCE and it never recovers even to LIMITED — the live chain state.
	d := policy.Decide("FAB_MATS", "X1-KP46-F45", shared.SupplyLevelScarce)
	if !d.Paused {
		t.Fatal("a market below the buy floor must pause")
	}
	if d.SuspectedStuck {
		t.Fatal("a one-tick-old pause must not report stuck — that would make the escalation noise")
	}

	clock.Advance(SuspectedStuckAfter + 30*time.Minute)
	d = policy.Decide("FAB_MATS", "X1-KP46-F45", shared.SupplyLevelScarce)

	if d.HeldAtBuyFloor != 0 {
		t.Fatalf("HeldAtBuyFloor = %s below the buy floor; that clock must stay at zero here, which is precisely why it cannot drive the escalation", d.HeldAtBuyFloor)
	}
	if !d.SuspectedStuck {
		t.Fatalf("a pause held %s BELOW the buy floor reported no escalation. This is the 3.5-hour invisible stall: both signals read zero while the gate sat still", d.PausedFor)
	}
	line := d.LogLine()
	if !strings.Contains(line, "SUSPECTED STUCK") || !strings.Contains(line, "BELOW") {
		t.Fatalf("the below-floor escalation must name its own case: %q", line)
	}
	if !strings.Contains(line, "1h0m0s") {
		t.Fatalf("the line must name how long the pause has held: %q", line)
	}
	// It must NOT claim something will release it — nothing will (see the release decision).
	if !strings.Contains(line, "lower the buy floor") {
		t.Fatalf("the line must name the operator's lever, since no automatic release crosses the buy floor: %q", line)
	}
}

// THE RELEASE DELIBERATELY DOES NOT CROSS THE BUY FLOOR (RULINGS #4).
//
// The buy floor is the operator's stated policy about which supply levels are acceptable to buy at —
// supply and price discipline, not an anti-chatter device. sp-c9wuu's release only ever overrode the
// RESUME floor, which is why it was safe. Releasing below the buy floor would make the system decide
// to buy into a depleted, laddering market on its own, which no money guard downstream would catch
// because none of them look at supply.
func TestBuyPolicy_TheTimeoutNeverReleasesBelowTheBuyFloor(t *testing.T) {
	policy, clock := belowFloorPolicy(t)
	policy.Decide("FAB_MATS", "F45", shared.SupplyLevelScarce)

	// Ten full timeout-lengths below the floor.
	for i := 0; i < 10; i++ {
		clock.Advance(StuckPauseTimeout)
		d := policy.Decide("FAB_MATS", "F45", shared.SupplyLevelScarce)
		if d.Buy {
			t.Fatalf("iteration %d: RESUMED at SCARCE, below the LIMITED buy floor. The buy floor is the operator's policy on which supply levels may be bought at, and overriding it automatically means laddering a depleted market with nothing downstream to catch it", i)
		}
		if d.ResumedByTimeout {
			t.Fatalf("iteration %d: the timeout released below the buy floor", i)
		}
	}
}

// CRITERION 3: the between-the-floors case sp-c9wuu fixed must keep working, release and all.
func TestBuyPolicy_TheBetweenFloorsReleaseStillWorks(t *testing.T) {
	policy, clock := belowFloorPolicy(t)
	policy.Decide("FAB_MATS", "F45", shared.SupplyLevelScarce)

	// Recovers to LIMITED — at the buy floor, below the MODERATE resume floor — and holds.
	policy.Decide("FAB_MATS", "F45", shared.SupplyLevelLimited)
	clock.Advance(StuckPauseTimeout + time.Minute)

	d := policy.Decide("FAB_MATS", "F45", shared.SupplyLevelLimited)
	if !d.Buy || !d.ResumedByTimeout {
		t.Fatalf("the between-floors release stopped working: buy=%v byTimeout=%v held=%s. sp-vrnjx must not trade one deadlock for another", d.Buy, d.ResumedByTimeout, d.HeldAtBuyFloor)
	}
}

// CRITERION 4: a flapping market must still never RELEASE. The anti-chatter property protects the
// spend decision, and the release is still anchored on the buy-floor clock, which resets on every dip.
//
// The escalation DOES accumulate across flapping, deliberately: a market bouncing for hours while the
// gate stands still is exactly what an operator should be told about, and suppressing it would rebuild
// the invisible pause one level down.
func TestBuyPolicy_AFlappingMarketStillNeverReleasesButDoesEscalate(t *testing.T) {
	policy, clock := belowFloorPolicy(t)
	policy.Decide("FAB_MATS", "F45", shared.SupplyLevelScarce)

	for i := 0; i < 10; i++ {
		clock.Advance(StuckPauseTimeout - time.Minute)
		if d := policy.Decide("FAB_MATS", "F45", shared.SupplyLevelLimited); d.Buy {
			t.Fatalf("iteration %d: released while flapping — the release must stay unreachable for a market that keeps dipping below the buy floor", i)
		}
		if d := policy.Decide("FAB_MATS", "F45", shared.SupplyLevelScarce); d.Buy {
			t.Fatalf("iteration %d: released at SCARCE", i)
		}
	}

	final := policy.Decide("FAB_MATS", "F45", shared.SupplyLevelScarce)
	if final.Buy {
		t.Fatal("released immediately after a dip: the buy-floor clock did not reset")
	}
	if !final.SuspectedStuck {
		t.Fatal("a market that has flapped for ten hours while the gate stood still reported nothing — that is the invisible pause rebuilt one level down")
	}
}

// The escalation clock must not survive a resume: a material that recovers and is bought starts its
// next pause clean, or the very next pause reports stuck immediately.
func TestBuyPolicy_TheEscalationClockResetsOnResume(t *testing.T) {
	policy, clock := belowFloorPolicy(t)
	policy.Decide("FAB_MATS", "F45", shared.SupplyLevelScarce)
	clock.Advance(SuspectedStuckAfter + time.Hour)
	if d := policy.Decide("FAB_MATS", "F45", shared.SupplyLevelScarce); !d.SuspectedStuck {
		t.Fatal("fixture is inert: the pause did not reach the escalation threshold, so a reset cannot be observed")
	}

	// Full recovery to the resume floor: a real buy.
	if d := policy.Decide("FAB_MATS", "F45", shared.SupplyLevelModerate); !d.Buy {
		t.Fatal("supply reached the resume floor and must resume")
	}

	clock.Advance(time.Minute)
	d := policy.Decide("FAB_MATS", "F45", shared.SupplyLevelScarce)
	if d.SuspectedStuck {
		t.Fatalf("the new pause reported stuck after one minute (PausedFor=%s) — the clock did not reset on resume", d.PausedFor)
	}
}

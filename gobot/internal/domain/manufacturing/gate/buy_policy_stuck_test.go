package gate

import (
	"strings"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-c9wuu: HYSTERESIS MUST NOT BE ABLE TO DEADLOCK.
//
// The incident: buy=LIMITED, resume=MODERATE. The fleet drained F45's FAB_MATS to SCARCE and paused
// correctly. Supply then recovered to LIMITED — a level it was explicitly configured to buy at —
// and plateaued there for seven and a half hours, because the resume floor sat one ladder level
// further up and the market never reached it while we were its only consumer.
//
// With ONE threshold the fleet flaps: wasteful, but it keeps buying. With TWO it can stop entirely.
// The timeout keeps the anti-chatter benefit for the fast oscillation it exists to damp while
// bounding the stall.

func stuckTestPolicy(t *testing.T, buy, resume shared.SupplyLevel) (*BuyPolicy, *shared.MockClock) {
	t.Helper()
	clock := &shared.MockClock{CurrentTime: time.Unix(0, 0).UTC()}
	return NewBuyPolicyWithClock(buy, resume, clock), clock
}

// THE INCIDENT, REPRODUCED. Against the unmodified policy this never resumes, at any elapsed time.
func TestBuyPolicy_ResumesWhenSupplyHoldsAtTheBuyFloorInsideTheGap(t *testing.T) {
	policy, clock := stuckTestPolicy(t, shared.SupplyLevelLimited, shared.SupplyLevelModerate)

	// Drained to SCARCE: pause, correctly.
	if d := policy.Decide("FAB_MATS", "X1-KP46-F45", shared.SupplyLevelScarce); !d.Paused {
		t.Fatal("a drained market must pause")
	}

	// Recovers to LIMITED — at the buy floor, below the resume floor — and holds there.
	d := policy.Decide("FAB_MATS", "X1-KP46-F45", shared.SupplyLevelLimited)
	if !d.Paused {
		t.Fatal("the pause must still hold on the first tick at the buy floor: releasing instantly is the chatter the resume floor exists to prevent")
	}

	clock.Advance(StuckPauseTimeout - time.Minute)
	d = policy.Decide("FAB_MATS", "X1-KP46-F45", shared.SupplyLevelLimited)
	if !d.Paused {
		t.Fatalf("released after %s, before the timeout — the anti-chatter window must be honoured in full", StuckPauseTimeout-time.Minute)
	}
	if !d.SuspectedStuck {
		// Not yet: one minute short.
		_ = d
	}

	clock.Advance(2 * time.Minute) // now past StuckPauseTimeout
	d = policy.Decide("FAB_MATS", "X1-KP46-F45", shared.SupplyLevelLimited)
	if d.Paused {
		t.Fatalf("STILL PAUSED after %s at the buy floor. This is the 7.5-hour deadlock: supply is at a level the operator configured us to BUY at, and the resume floor is further up the ladder than this market recovers", d.HeldAtBuyFloor)
	}
	if !d.ResumedByTimeout {
		t.Fatal("the resume must be attributed to the TIMEOUT, not to the resume floor — the market never reached MODERATE")
	}
	if !strings.Contains(d.LogLine(), "RESUMING") || !strings.Contains(d.LogLine(), "TIMEOUT") {
		t.Fatalf("the timeout resume must say so: %q", d.LogLine())
	}
}

// CRITERION 4, THE ANTI-CHATTER REGRESSION. A market that keeps dipping below the buy floor must
// NEVER reach the timeout, however long the flapping goes on.
//
// This is the property the fix could most easily have traded away, and it is preserved by WHERE the
// clock is anchored: it starts when supply reaches the buy floor and resets the moment it drops
// back, so a flapping market can never accumulate the hour.
func TestBuyPolicy_AFlappingMarketNeverReachesTheStuckTimeout(t *testing.T) {
	policy, clock := stuckTestPolicy(t, shared.SupplyLevelLimited, shared.SupplyLevelModerate)
	policy.Decide("FAB_MATS", "F45", shared.SupplyLevelScarce) // paused

	// Ten full timeout-lengths of oscillation: recovers to the buy floor, dips back, repeatedly.
	for i := 0; i < 10; i++ {
		clock.Advance(StuckPauseTimeout - time.Minute)
		if d := policy.Decide("FAB_MATS", "F45", shared.SupplyLevelLimited); d.Buy {
			t.Fatalf("iteration %d: resumed while flapping — the timeout must be unreachable for a market that keeps dipping below the buy floor, or the fix trades a deadlock for the chatter the resume floor exists to prevent", i)
		}
		// The dip. This is what resets the clock.
		if d := policy.Decide("FAB_MATS", "F45", shared.SupplyLevelScarce); d.Buy {
			t.Fatalf("iteration %d: resumed at SCARCE, below the buy floor", i)
		}
	}

	// Total elapsed is ~10 hours, far past the timeout, and it never fired.
	if d := policy.Decide("FAB_MATS", "F45", shared.SupplyLevelLimited); d.Buy {
		t.Fatal("resumed immediately after a dip: the held-at-buy-floor clock did not reset")
	}
}

// AND THE ORDINARY RESUME IS UNTOUCHED: reaching the resume floor still resumes at once, with no
// timeout involved.
func TestBuyPolicy_ReachingTheResumeFloorStillResumesImmediately(t *testing.T) {
	policy, clock := stuckTestPolicy(t, shared.SupplyLevelLimited, shared.SupplyLevelModerate)
	policy.Decide("FAB_MATS", "F45", shared.SupplyLevelScarce)
	clock.Advance(time.Minute)

	d := policy.Decide("FAB_MATS", "F45", shared.SupplyLevelModerate)
	if !d.Buy {
		t.Fatal("supply reached the resume floor and must resume at once")
	}
	if d.ResumedByTimeout {
		t.Fatal("a resume-floor resume must not be attributed to the timeout")
	}
}

// CRITERION 3: A STUCK PAUSE MUST BE DISTINGUISHABLE FROM HEALTHY WAITING.
//
// The observability half, and arguably the more important one: for 7.5 hours the pause logged a
// clear, reassuring line every tick, indistinguishable from correct behaviour, and it was twice read
// as "thin market, being patient".
func TestBuyPolicy_APauseHeldAtTheBuyFloorEscalatesAndNamesHowLong(t *testing.T) {
	// resume=ABUNDANT so the timeout is reachable but the ladder never releases it by supply alone.
	policy, clock := stuckTestPolicy(t, shared.SupplyLevelLimited, shared.SupplyLevelAbundant)
	policy.Decide("FAB_MATS", "F45", shared.SupplyLevelScarce)

	early := policy.Decide("FAB_MATS", "F45", shared.SupplyLevelLimited)
	if early.SuspectedStuck {
		t.Fatal("a pause one tick old must not be reported as stuck — that would make the escalation noise")
	}
	if !strings.Contains(early.LogLine(), "held") {
		t.Fatalf("even a young pause at the buy floor should say it has recovered and is holding: %q", early.LogLine())
	}

	// Past the ESCALATION threshold but before the release, so the pause is still held and can be
	// observed reporting itself stuck. That ordering is the point: the warning must not depend on
	// the release working.
	clock.Advance(SuspectedStuckAfter + time.Minute)
	stuck := policy.Decide("FAB_MATS", "F45", shared.SupplyLevelLimited)

	if !stuck.Paused {
		t.Fatal("fixture is inert: the pause released before the escalation could be seen, so this test cannot observe the stuck report at all")
	}

	if !stuck.SuspectedStuck {
		t.Fatalf("a pause held %s at the buy floor is not patience and must be flagged", stuck.HeldAtBuyFloor)
	}
	line := stuck.LogLine()
	if !strings.Contains(line, "SUSPECTED STUCK") {
		t.Fatalf("the escalated line must say it is suspected stuck, not repeat the reassuring wording: %q", line)
	}
	if !strings.Contains(line, "31m0s") {
		t.Fatalf("the line must name HOW LONG the pause has held — that number is what turns 'be patient' into 'go look': %q", line)
	}
	if !strings.Contains(line, string(shared.SupplyLevelLimited)) {
		t.Fatalf("the line must name the supply level it is stuck at: %q", line)
	}
}

// CRITERION 5: THE SHIPPED DEFAULTS ARE THE MOST EXPOSED CASE, and the fix must reach them. HIGH is
// harder to reach than MODERATE, so a fleet on the defaults can plateau in the gap more easily than
// the overridden pipeline that actually deadlocked.
func TestBuyPolicy_TheShippedDefaultsAreCoveredToo(t *testing.T) {
	// Unset floors resolve to DefaultBuyFloor=MODERATE / DefaultResumeFloor=HIGH.
	policy, clock := stuckTestPolicy(t, "", "")
	buy, resume := policy.Floors()
	if buy != DefaultBuyFloor || resume != DefaultResumeFloor {
		t.Fatalf("floors resolved to %s/%s, want the shipped defaults %s/%s", buy, resume, DefaultBuyFloor, DefaultResumeFloor)
	}
	// THE GAP IS ONE LADDER LEVEL, NOT TWO, and that is the point rather than a mitigation: the
	// incident's overridden floors (LIMITED/MODERATE) were also one level apart. A single level is
	// enough to deadlock whenever a market plateaus exactly AT the buy floor, which is what F45 did.
	// So the defaults are exposed identically, not less — and HIGH being harder to reach than
	// MODERATE makes plateauing below it more likely, not less.
	if resume.Order()-buy.Order() < 1 {
		t.Fatalf("fixture is inert: the default floors are %s/%s with no gap at all, so nothing can plateau inside", buy, resume)
	}

	policy.Decide("FAB_MATS", "F45", shared.SupplyLevelLimited) // below MODERATE: pause
	policy.Decide("FAB_MATS", "F45", shared.SupplyLevelModerate)
	clock.Advance(StuckPauseTimeout + time.Minute)

	if d := policy.Decide("FAB_MATS", "F45", shared.SupplyLevelModerate); !d.Buy {
		t.Fatal("the shipped defaults must get the same release: a fleet that never overrode its floors is MORE exposed, since HIGH is harder to reach than MODERATE")
	}
}

// THE TIMEOUT IS DERIVED, NOT PICKED. It is two full supply-task lifetimes, so the pause always
// outlasts the leg that depleted the market.
func TestStuckPauseTimeout_IsTwoSupplyTaskLifetimes(t *testing.T) {
	const supplyTaskTimeout = 30 * time.Minute // constructionSupplyTaskDefaultTimeout
	if StuckPauseTimeout != 2*supplyTaskTimeout {
		t.Fatalf("StuckPauseTimeout is %s; it is meant to be two supply-task lifetimes (2 x %s). One is the minimum that outlasts the depleting leg — release sooner and the timeout becomes the chatter the resume floor exists to prevent — and the second gives room to see whether supply is climbing or merely bouncing",
			StuckPauseTimeout, supplyTaskTimeout)
	}
	if StuckPauseTimeout >= 7*time.Hour {
		t.Fatalf("StuckPauseTimeout is %s, which does not bound the 7.5-hour stall it exists to prevent", StuckPauseTimeout)
	}
}

// THE ESCALATION MUST BE REACHABLE. Written with both thresholds equal — as it first was — the
// suspected-stuck flag is computed on a decision that has already resumed, so it can never be true
// and criterion 3's counter would sit at zero forever while the fleet stalled.
func TestSuspectedStuckAfter_IsStrictlyBelowTheReleaseTimeout(t *testing.T) {
	if SuspectedStuckAfter >= StuckPauseTimeout {
		t.Fatalf("SuspectedStuckAfter (%s) is not below StuckPauseTimeout (%s), so a pause is released on the same tick it would be reported stuck and the report is unreachable",
			SuspectedStuckAfter, StuckPauseTimeout)
	}
	// And the warning window must be long enough to actually be seen by an operator.
	if StuckPauseTimeout-SuspectedStuckAfter < 15*time.Minute {
		t.Fatalf("the warning window is only %s; too short to be noticed before the automatic release hides the problem", StuckPauseTimeout-SuspectedStuckAfter)
	}
}

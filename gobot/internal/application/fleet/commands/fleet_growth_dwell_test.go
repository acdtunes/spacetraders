package commands

// THE ANTI-THRASH MEASURES THE CONDITION, NOT THE PROCESS (sp-739gf item 2).
//
// The old rule counted TICKS: three consecutive reconciles with a positive shortfall. That quantity
// is a fact about the coordinator, not about the fleet — it multiplies by whatever the tick happens
// to cost (20 to 190 minutes on the live frame), and it is the same three ticks whether the shortfall
// is 1 or 69. These tests pin the replacement: a WALL-CLOCK dwell on the shortfall itself, waived
// when the shortfall is too large for the dwell to be protecting anything.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/fleetgrowth"
)

// shortfallRequest is the demand guard's inputs and nothing else: every other guard is given a
// passing value elsewhere, and these tests judge guardDemand alone.
func shortfallRequest(shortfall, pool int, held, dwell time.Duration) PurchaseRequest {
	return PurchaseRequest{
		Class:          HullClassHeavy,
		Shortfall:      shortfall,
		PoolCurrent:    pool,
		ShortfallHeld:  held,
		ShortfallDwell: dwell,
	}
}

// THE LIVE FRAME. Shortfall 69 against a pool of 10: the surface would have to be overstated by more
// than 2x for this hull to be unwanted, so there is nothing for a dwell to protect. It must pass on
// the FIRST tick that sees it — including the first tick after a restart, which is the six-restarts-
// today case that cost the fleet most of a day.
func TestGuardDemand_DecisiveShortfallNeedsNoDwell(t *testing.T) {
	v := guardDemand(shortfallRequest(69, 10, 0, fleetgrowth.DefaultSaturationDwell))
	require.True(t, v.Passed, "a shortfall of 69 against 10 hulls is not a close call: %s", v.Detail)
	require.Contains(t, v.Detail, "decisive", "the line must say WHY the dwell did not apply")
}

// THE BOUNDARY. A shortfall of 1-2 against the same pool IS a close call — one lane's worth of
// ranking noise flips it — so the dwell applies in full and an unheld shortfall does not buy.
func TestGuardDemand_MarginalShortfallStillWaitsTheFullDwell(t *testing.T) {
	v := guardDemand(shortfallRequest(2, 10, 0, fleetgrowth.DefaultSaturationDwell))
	require.False(t, v.Passed, "a marginal shortfall must still settle before a 1.7M hull is bought")

	held := guardDemand(shortfallRequest(2, 10, fleetgrowth.DefaultSaturationDwell, fleetgrowth.DefaultSaturationDwell))
	require.True(t, held.Passed, "and once it HAS persisted the dwell, it buys")
}

// THE DWELL IS WALL CLOCK, so a shortfall one second short of it does not buy and one second past it
// does. Nothing here counts ticks — a fast coordinator and a slow one wait the same wall time.
func TestGuardDemand_DwellIsWallClockNotTicks(t *testing.T) {
	dwell := fleetgrowth.DefaultSaturationDwell
	require.False(t, guardDemand(shortfallRequest(2, 10, dwell-time.Second, dwell)).Passed)
	require.True(t, guardDemand(shortfallRequest(2, 10, dwell, dwell)).Passed)
}

// THE COLD POOL. No trade hull owns a lane, so any unserved lane is decisive: the FIRST hull can
// never be made to wait for a fleet that does not exist yet to prove something.
func TestGuardDemand_ColdPoolIsDecisive(t *testing.T) {
	require.True(t, guardDemand(shortfallRequest(1, 0, 0, fleetgrowth.DefaultSaturationDwell)).Passed)
}

// NO SHORTFALL NEVER BUYS, dwell or no dwell, decisive test or no decisive test. The anti-thrash may
// only ever SUBTRACT from what the shortfall authorises.
func TestGuardDemand_NoShortfallNeverBuys(t *testing.T) {
	require.False(t, guardDemand(shortfallRequest(0, 10, time.Hour, fleetgrowth.DefaultSaturationDwell)).Passed)
	require.False(t, guardDemand(shortfallRequest(-3, 0, time.Hour, fleetgrowth.DefaultSaturationDwell)).Passed)
}

// AN UNCONFIGURED DWELL IS NOT AN ABSENT ONE. A zero would make the anti-thrash a no-op — the
// `tune <key> 0` trap — so the guard falls back to the documented dwell rather than to "no wait".
func TestGuardDemand_ZeroDwellFallsBackRatherThanDisabling(t *testing.T) {
	require.False(t, guardDemand(shortfallRequest(2, 10, 0, 0)).Passed,
		"a zero dwell must resolve to the default, never disable the anti-thrash")
	require.True(t, guardDemand(shortfallRequest(2, 10, fleetgrowth.DefaultSaturationDwell, 0)).Passed)
}

// --- the coordinator side: the dwell is ANCHORED, not counted -----------------------------------

// The anchor is set once when the shortfall appears and left alone while it persists, so the held
// duration grows with WALL CLOCK rather than with the number of times anyone looked.
func TestShortfallDwell_AnchorSurvivesRepeatedObservation(t *testing.T) {
	h := NewRunFleetGrowthCoordinatorHandler(nil)
	st := &growthState{}
	start := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	short := ClassDemand{Class: HullClassHeavy, Readable: true, Demand: 79, Current: 10}

	h.advanceShortfallDwell(st, short, start)
	require.Equal(t, time.Duration(0), st.heldFor(start))

	// Two more observations, ten minutes apart: the anchor must not move.
	h.advanceShortfallDwell(st, short, start.Add(5*time.Minute))
	h.advanceShortfallDwell(st, short, start.Add(10*time.Minute))
	require.Equal(t, 10*time.Minute, st.heldFor(start.Add(10*time.Minute)),
		"the dwell measures how long the SHORTFALL has held, not how often the coordinator ticked")
}

// A cleared shortfall clears the anchor, and a returning one starts a fresh dwell: the window is
// CONSECUTIVE persistence, exactly as the tick streak's reset rule was.
func TestShortfallDwell_ClearedShortfallResetsTheAnchor(t *testing.T) {
	h := NewRunFleetGrowthCoordinatorHandler(nil)
	st := &growthState{}
	start := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	short := ClassDemand{Class: HullClassHeavy, Readable: true, Demand: 79, Current: 10}
	met := ClassDemand{Class: HullClassHeavy, Readable: true, Demand: 10, Current: 10}

	h.advanceShortfallDwell(st, short, start)
	h.advanceShortfallDwell(st, met, start.Add(time.Hour))
	require.Equal(t, time.Duration(0), st.heldFor(start.Add(time.Hour)), "a met demand clears the window")

	h.advanceShortfallDwell(st, short, start.Add(2*time.Hour))
	require.Equal(t, time.Duration(0), st.heldFor(start.Add(2*time.Hour)), "a returning shortfall starts fresh")
}

// AN UNREADABLE DEMAND IS NOT A MET ONE, but it must not accumulate toward a spend either. It
// clears the window — the same direction the lane reader's own blind tick takes, and the direction
// that never buys on evidence nobody could see.
func TestShortfallDwell_UnreadableDemandClearsTheWindow(t *testing.T) {
	h := NewRunFleetGrowthCoordinatorHandler(nil)
	st := &growthState{}
	start := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	short := ClassDemand{Class: HullClassHeavy, Readable: true, Demand: 79, Current: 10}

	h.advanceShortfallDwell(st, short, start)
	h.advanceShortfallDwell(st, unreadableHeavy("lane signal down"), start.Add(30*time.Minute))
	require.Equal(t, time.Duration(0), st.heldFor(start.Add(30*time.Minute)))
}

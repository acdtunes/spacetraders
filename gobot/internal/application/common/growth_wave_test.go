package common

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/hullbuy"
)

// heavyTarget is spelled long because this package's other tests bind a local named target; a
// package-level func of that name would be shadowed by them and a later call would fail as
// "cannot call non-function" rather than as anything a reader could act on.
func heavyTarget(ask int64) HeavyReserveTarget {
	return HeavyReserve(HeavyReserveInputs{
		CapabilityOpen: ask > 0, HeaviesOwned: 1, HeavyCap: 5, TargetYardPrice: ask,
	})
}

// heavyInputs is the canonical HEAVY state: lanes unserved, a surface deeper than the fleet's hold,
// under cap, a priced target, and a high-water mark comfortably past the entry threshold.
func heavyInputs() WaveInputs {
	return WaveInputs{
		GrowthEnabled:           true,
		UnservedLanes:           3,
		UnservedLanesReadable:   true,
		TradeSaturated:          false,
		TradeSaturationReadable: true,
		Target:                  heavyTarget(1_000_000),
		HighWaterTreasury:       2_000_000,
		HighWaterReadable:       true,
	}
}

// THE WIRE VALUES ARE THE OPERATOR'S VOCABULARY, not an internal detail: they are the label values
// on the wave metric and the words in a decision log, so a dashboard and an alert route are written
// against them. Every other assertion in this file compares symbolically, which is what a test
// SHOULD do for a decision — but it leaves the strings themselves free to change under a rename
// with nothing failing, silently renaming a series out from under whoever is watching it.
func TestWaveWireValuesArePinned(t *testing.T) {
	for _, tc := range []struct {
		constant string
		got      string
		want     string
	}{
		{"WaveHeavy", string(WaveHeavy), "heavy"},
		{"WaveProbe", string(WaveProbe), "probe"},
		{"WaveProbeReasonNone", string(WaveProbeReasonNone), ""},
		{"WaveProbeReasonGrowthDisabled", string(WaveProbeReasonGrowthDisabled), "growth_disabled"},
		{"WaveProbeReasonLanesUnreadable", string(WaveProbeReasonLanesUnreadable), "lanes_unreadable"},
		{"WaveProbeReasonLanesServed", string(WaveProbeReasonLanesServed), "lanes_served"},
		{"WaveProbeReasonSaturationUnreadable", string(WaveProbeReasonSaturationUnreadable), "saturation_unreadable"},
		{"WaveProbeReasonTradeSaturated", string(WaveProbeReasonTradeSaturated), "trade_saturated"},
		{"WaveProbeReasonCapacityUnreadable", string(WaveProbeReasonCapacityUnreadable), "capacity_unreadable"},
		{"WaveProbeReasonUnreachable", string(WaveProbeReasonUnreachable), "unreachable"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s emits %q, want %q", tc.constant, tc.got, tc.want)
		}
	}
}

func TestDeriveWave_AllClausesTrue_IsHeavy(t *testing.T) {
	w, reason := DeriveWave(heavyInputs())
	if w != WaveHeavy || reason != WaveProbeReasonNone {
		t.Fatalf("expected HEAVY with no probe reason, got %q/%q", w, reason)
	}
}

// The master switch is the FIRST clause. Off means there is no heavy buyer, so there is nothing
// to save for and probe buying must NOT be paused — pausing it for a switched-off buyer is a
// deadlock with no spender able to clear it.
func TestDeriveWave_GrowthDisabled_IsProbe(t *testing.T) {
	in := heavyInputs()
	in.GrowthEnabled = false
	if w, reason := DeriveWave(in); w != WaveProbe || reason != WaveProbeReasonGrowthDisabled {
		t.Fatalf("expected PROBE/growth_disabled, got %q/%q", w, reason)
	}
}

func TestDeriveWave_LanesServed_IsProbe(t *testing.T) {
	in := heavyInputs()
	in.UnservedLanes = 0
	if w, reason := DeriveWave(in); w != WaveProbe || reason != WaveProbeReasonLanesServed {
		t.Fatalf("expected PROBE/lanes_served, got %q/%q", w, reason)
	}
}

// THE LANE CLAUSE IS A THRESHOLD AT ONE, NOT A MAGNITUDE, and that is the whole of what a bigger
// lane count does to the regime. The census that feeds it now counts every reachable circuit rather
// than one best lane per good per system, so the number it reports is an order of magnitude larger
// — and the wave cannot tell those apart. One unserved lane already pauses probe buying; the only
// road back to PROBE is the trade pool covering EVERY lane. Nothing here rate-limits that: this
// pins the behaviour so it is stated rather than inferred.
func TestDeriveWave_LaneClauseIsAThresholdNotAMagnitude(t *testing.T) {
	for _, lanes := range []int{1, 8, 500} {
		in := heavyInputs()
		in.UnservedLanes = lanes
		if w, reason := DeriveWave(in); w != WaveHeavy || reason != WaveProbeReasonNone {
			t.Fatalf("%d unserved lanes gave %q/%q, want HEAVY — every positive count is one regime", lanes, w, reason)
		}
	}
}

// THE PIVOTAL FIXTURE — the live frame of 2026-08-08 01:51, and the defect in one assertion. The
// census reported fifteen unserved lanes, so the lane clause held the regime HEAVY and the demand
// asked for sixteen more hulls; the fleet's nine trade hulls meanwhile held ~820 units against a
// reachable surface absorbing 741, and every one of them sat empty. UNSERVED LANES REMAIN POSITIVE
// HERE, which is the whole point: the saturation term must override the lane count, not agree with
// it, or it changes nothing in the state it was built for.
func TestDeriveWave_SaturatedSurfaceIsProbeEvenWithUnservedLanes(t *testing.T) {
	in := heavyInputs()
	in.UnservedLanes = 15
	in.TradeSaturated = true
	if w, reason := DeriveWave(in); w != WaveProbe || reason != WaveProbeReasonTradeSaturated {
		t.Fatalf("15 unserved lanes on a SATURATED surface gave %q/%q, want PROBE/trade_saturated — the lane count must not outrank saturation", w, reason)
	}
}

// THE ANTI-VACUITY CONTROL, and the statement that this change is demand-REDUCING only: wherever the
// surface is not saturated the regime is exactly what it was before the term existed. A term that
// answered PROBE here would be pausing a fleet that genuinely has work its hold cannot cover.
func TestDeriveWave_AnUnsaturatedSurfaceIsHeavyUnchanged(t *testing.T) {
	for _, lanes := range []int{1, 15, 500} {
		in := heavyInputs()
		in.UnservedLanes = lanes
		in.TradeSaturated = false
		if w, reason := DeriveWave(in); w != WaveHeavy || reason != WaveProbeReasonNone {
			t.Fatalf("%d unserved lanes on an UNSATURATED surface gave %q/%q, want HEAVY — the term must be inert here", lanes, w, reason)
		}
	}
}

// SATURATION CAN ONLY EVER SUBTRACT HEAVY TICKS. Swept across the whole input space, setting the
// flag must never turn a PROBE tick HEAVY — that is RULINGS #6/#4 by direction, and it is the
// property that makes this change unable to authorise a purchase today's code would refuse. It is
// swept rather than spot-checked because a clause inserted at the wrong precedence could satisfy
// every named case above and still release one combination.
func TestDeriveWave_SaturationNeverReleasesAHeavy(t *testing.T) {
	sawFlip := false
	for _, enabled := range []bool{true, false} {
		for _, lanes := range []int{0, 1, 15} {
			for _, lanesReadable := range []bool{true, false} {
				for _, satReadable := range []bool{true, false} {
					for _, ask := range []int64{0, 1_000_000} {
						for _, peakReadable := range []bool{true, false} {
							for _, hw := range []int64{0, 400_000, 2_000_000} {
								base := WaveInputs{
									GrowthEnabled: enabled, UnservedLanes: lanes, UnservedLanesReadable: lanesReadable,
									TradeSaturationReadable: satReadable,
									Target:                  heavyTarget(ask), HighWaterTreasury: hw, HighWaterReadable: peakReadable,
								}
								unsaturated, saturated := base, base
								saturated.TradeSaturated = true
								was, _ := DeriveWave(unsaturated)
								now, _ := DeriveWave(saturated)
								if was == WaveProbe && now == WaveHeavy {
									t.Fatalf("saturation RELEASED a heavy at %+v: probe → heavy", base)
								}
								if was == WaveHeavy && now == WaveProbe {
									sawFlip = true
								}
							}
						}
					}
				}
			}
		}
	}
	// CALIBRATION: a sweep in which the flag never changed an answer would prove nothing about its
	// direction, because "never releases" is trivially true of an unread field.
	if !sawFlip {
		t.Fatalf("the sweep never saw saturation change a regime — it cannot witness its direction")
	}
}

// AND THE PAUSE IT OPENS IS BOUNDED BY THE HEAVY CAP. A large unserved count holds the lane clause
// open for good, so the only remaining road back to PROBE is the reservation standing down — and it
// does, at the cap, whatever the census reports and however rich the fleet is. Probe buying resumes
// after at most DefaultHeavyCap heavies rather than never, which is the difference between a regime
// that saves for a hull and one that starves coverage growth permanently.
func TestDeriveWave_ProbePauseIsBoundedByTheHeavyCap(t *testing.T) {
	in := heavyInputs()
	in.UnservedLanes = 900
	in.HighWaterTreasury = 1_000_000_000
	in.Target = HeavyReserve(HeavyReserveInputs{
		CapabilityOpen:  true,
		HeaviesOwned:    hullbuy.DefaultHeavyCap,
		HeavyCap:        hullbuy.DefaultHeavyCap,
		TargetYardPrice: 1_000_000,
	})

	if w, reason := DeriveWave(in); w != WaveProbe || reason != WaveProbeReasonUnreachable {
		t.Fatalf("at the heavy cap with %d unserved lanes the wave gave %q/%q, want PROBE/unreachable — the pause must end at the cap", in.UnservedLanes, w, reason)
	}
}

// Every unreadable input yields PROBE. PROBE authorises no spend of its own — the drain's floor,
// probe cap and the immutable reserve all still bind — so it is the release direction, matching
// HeavyReserve's documented treatment of a blind signal.
func TestDeriveWave_UnreadableInputs_AreProbe(t *testing.T) {
	cases := map[string]struct {
		mutate func(*WaveInputs)
		reason WaveProbeReason
	}{
		"lane surface down":   {func(in *WaveInputs) { in.UnservedLanesReadable = false }, WaveProbeReasonLanesUnreadable},
		"empty ledger window": {func(in *WaveInputs) { in.HighWaterReadable = false }, WaveProbeReasonCapacityUnreadable},
		// An unreadable saturation term is a blind read of the fleet's own hold or of the surface's
		// depth, and it releases toward PROBE like every other blind input — never toward HEAVY,
		// which would buy a hull against a saturation nobody could measure.
		"saturation unmeasurable": {func(in *WaveInputs) { in.TradeSaturationReadable = false }, WaveProbeReasonSaturationUnreadable},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			in := heavyInputs()
			tc.mutate(&in)
			if w, reason := DeriveWave(in); w != WaveProbe || reason != tc.reason {
				t.Fatalf("expected PROBE/%q, got %q/%q", tc.reason, w, reason)
			}
		})
	}
}

// EMPTY IS NOT ZERO. A window with no ledger rows must arrive as unreadable, never as a
// high-water of 0 — a zero would read as a genuine "this fleet has never held money", which is
// a different and much stronger claim than "we could not see".
func TestDeriveWave_ZeroHighWaterIsNotTheSameAsUnreadable(t *testing.T) {
	in := heavyInputs()
	in.HighWaterTreasury, in.HighWaterReadable = 0, true
	if w, reason := DeriveWave(in); w != WaveProbe || reason != WaveProbeReasonUnreachable {
		t.Fatalf("a readable zero is a genuine unreachable, got %q/%q", w, reason)
	}
	in.HighWaterReadable = false
	if _, reason := DeriveWave(in); reason != WaveProbeReasonCapacityUnreadable {
		t.Fatalf("an unreadable window must be distinguishable from a readable zero, got %q", reason)
	}
}

// CLAUSE ORDER IS PART OF THE CONTRACT, not an implementation detail. Every clause returns PROBE,
// so a reorder cannot change the MODE — it changes only which clause is reported, and reporting the
// right clause is the entire reason the companion reason exists. Mutating one field at a time
// cannot see that: it leaves every other clause true, so any permutation passes. Each step below
// mends exactly one clause and leaves all the LATER ones still false, so a step passing means the
// clause it names outranks every clause after it.
func TestDeriveWave_ClausePrecedenceIsPinned(t *testing.T) {
	in := WaveInputs{
		GrowthEnabled:           false,
		UnservedLanes:           0,
		UnservedLanesReadable:   false,
		TradeSaturated:          true,
		TradeSaturationReadable: false,
		Target:                  heavyTarget(1_000_000),
		HighWaterTreasury:       0,
		HighWaterReadable:       false,
	}
	for _, step := range []struct {
		outranks string
		mend     func(*WaveInputs)
		want     WaveProbeReason
	}{
		{"the master switch outranks every other clause", func(*WaveInputs) {}, WaveProbeReasonGrowthDisabled},
		{"a blind lane surface outranks the lane count", func(in *WaveInputs) { in.GrowthEnabled = true }, WaveProbeReasonLanesUnreadable},
		{"served lanes outrank the saturation term", func(in *WaveInputs) { in.UnservedLanesReadable = true }, WaveProbeReasonLanesServed},
		// THE DEMAND CLAUSES ANSWER BEFORE THE AFFORDABILITY ONES, and the blind read outranks the
		// verdict derived from it — the same shape the lane surface already has one rung above.
		{"a blind saturation read outranks its own verdict", func(in *WaveInputs) { in.UnservedLanes = 3 }, WaveProbeReasonSaturationUnreadable},
		{"saturation outranks the capacity read", func(in *WaveInputs) { in.TradeSaturationReadable = true }, WaveProbeReasonTradeSaturated},
		{"a blind capacity read outranks reachability", func(in *WaveInputs) { in.TradeSaturated = false }, WaveProbeReasonCapacityUnreadable},
		{"reachability answers last", func(in *WaveInputs) { in.HighWaterReadable = true }, WaveProbeReasonUnreachable},
	} {
		step.mend(&in)
		if w, reason := DeriveWave(in); w != WaveProbe || reason != step.want {
			t.Fatalf("%s: expected PROBE/%q, got %q/%q", step.outranks, step.want, w, reason)
		}
	}
}

// A ZERO RESERVE TARGET REACHES THE WAVE AS UNREACHABLE, whichever rung produced it. The cap, the
// unpriced target and the closed capability are not re-derived here — they arrive already folded
// into the target — so the cases below enumerate the ROUTES into a zero rather than re-testing the
// rungs themselves, which heavy_reserve_test.go owns. What this pins at the predicate's own
// boundary is that none of them is special-cased into a different reason or into HEAVY.
func TestDeriveWave_AZeroReserveTargetIsUnreachableWhicheverRungZeroedIt(t *testing.T) {
	cases := map[string]HeavyReserveInputs{
		"at the cap":       {CapabilityOpen: true, HeaviesOwned: 5, HeavyCap: 5, TargetYardPrice: 1_000_000},
		"no priced target": {CapabilityOpen: true, HeaviesOwned: 1, HeavyCap: 5, TargetYardPrice: 0},
		"no capability":    {CapabilityOpen: false, HeaviesOwned: 1, HeavyCap: 5, TargetYardPrice: 1_000_000},
	}
	for name, ri := range cases {
		t.Run(name, func(t *testing.T) {
			in := heavyInputs()
			in.Target = HeavyReserve(ri)
			if w, reason := DeriveWave(in); w != WaveProbe || reason != WaveProbeReasonUnreachable {
				t.Fatalf("expected PROBE/unreachable, got %q/%q", w, reason)
			}
		})
	}
}

// THE §395 ANTI-DEADLOCK REGRESSION, restated on the ruled measure. A fleet whose PEAK treasury
// across a full trade cycle is far below the entry threshold has not touched enough money to be
// credibly saving for this hull, and must not have its probe buying paused for it.
//
// This is a strictly stronger statement than the instantaneous form it replaces: that one asked
// "are we short at this instant", which a fleet that is genuinely rich fails several times an
// hour purely on trade-cycle phase.
func TestDeriveWave_PeakFarBelowEntry_DoesNotPauseProbes(t *testing.T) {
	in := heavyInputs()
	in.Target = heavyTarget(1_916_613)
	in.HighWaterTreasury = 400_000
	if w, reason := DeriveWave(in); w != WaveProbe || reason != WaveProbeReasonUnreachable {
		t.Fatalf("an unreachable heavy must leave probe buying running: got %q/%q", w, reason)
	}
}

// THE PROPERTY THE RULING BUYS: the mode is CONSTANT across a trade cycle. The live balance
// swings across the whole observed band while the high-water mark — the peak of that same band —
// does not move, so every point in the cycle yields the same regime. A predicate that answered
// differently at the trough than at the peak would be reporting phase, not economics.
func TestDeriveWave_ModeIsConstantAcrossTheTradeCycle(t *testing.T) {
	reachable := heavyInputs()
	reachable.Target = heavyTarget(1_916_613)
	reachable.HighWaterTreasury = 1_500_000 // the band's peak clears floor + entry

	unreachable := reachable
	unreachable.HighWaterTreasury = 400_000

	// Sweeping the live balance must change NOTHING: it is not an input to the predicate at all,
	// which is precisely what removes the flapping. The sweep is the falsifier — a predicate that
	// reintroduced a live-balance term would break here and nowhere else.
	for live := int64(119_000); live <= 1_500_000; live += 37_313 {
		reachable.LiveTreasuryForReporting = live
		unreachable.LiveTreasuryForReporting = live
		if w, _ := DeriveWave(reachable); w != WaveHeavy {
			t.Fatalf("reachable fleet flipped to %q at live balance %d", w, live)
		}
		if w, _ := DeriveWave(unreachable); w != WaveProbe {
			t.Fatalf("unreachable fleet flipped to %q at live balance %d", w, live)
		}
	}
}

// LOCKSTEP: the reachability clause must be HoldAt with a different balance, never a copy of its
// arithmetic. A second derivation would pass every case above and still drift the moment HoldAt's
// entry computation changes, so this pins the two against each other across the whole boundary.
func TestDeriveWave_ReachabilityIsHoldAtOnTheHighWater(t *testing.T) {
	tgt := heavyTarget(1_000_000)
	for hw := int64(0); hw <= 2_000_000; hw += 9_973 {
		in := WaveInputs{
			GrowthEnabled: true, UnservedLanes: 1, UnservedLanesReadable: true,
			TradeSaturationReadable: true,
			Target:                  tgt, HighWaterTreasury: hw, HighWaterReadable: true,
		}
		w, _ := DeriveWave(in)
		wantHeavy := tgt.HoldAt(hw) > 0
		if (w == WaveHeavy) != wantHeavy {
			t.Fatalf("wave and HoldAt disagree at high-water %d: wave=%q HoldAt=%d", hw, w, tgt.HoldAt(hw))
		}
		if Reachable(tgt, hw) != wantHeavy {
			t.Fatalf("Reachable disagrees with HoldAt at high-water %d", hw)
		}
	}
}

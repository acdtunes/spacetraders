package common

import "testing"

// heavyTarget is spelled long because this package's other tests bind a local named target; a
// package-level func of that name would be shadowed by them and a later call would fail as
// "cannot call non-function" rather than as anything a reader could act on.
func heavyTarget(ask int64) HeavyReserveTarget {
	return HeavyReserve(HeavyReserveInputs{
		CapabilityOpen: ask > 0, HeaviesOwned: 1, HeavyCap: 5, TargetYardPrice: ask,
	})
}

// heavyInputs is the canonical HEAVY state: lanes unserved, under cap, a priced target, and a
// high-water mark comfortably past the entry threshold.
func heavyInputs() WaveInputs {
	return WaveInputs{
		GrowthEnabled:         true,
		UnservedLanes:         3,
		UnservedLanesReadable: true,
		Target:                heavyTarget(1_000_000),
		HighWaterTreasury:     2_000_000,
		HighWaterReadable:     true,
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
		GrowthEnabled:         false,
		UnservedLanes:         0,
		UnservedLanesReadable: false,
		Target:                heavyTarget(1_000_000),
		HighWaterTreasury:     0,
		HighWaterReadable:     false,
	}
	for _, step := range []struct {
		outranks string
		mend     func(*WaveInputs)
		want     WaveProbeReason
	}{
		{"the master switch outranks every other clause", func(*WaveInputs) {}, WaveProbeReasonGrowthDisabled},
		{"a blind lane surface outranks the lane count", func(in *WaveInputs) { in.GrowthEnabled = true }, WaveProbeReasonLanesUnreadable},
		{"served lanes outrank the capacity read", func(in *WaveInputs) { in.UnservedLanesReadable = true }, WaveProbeReasonLanesServed},
		{"a blind capacity read outranks reachability", func(in *WaveInputs) { in.UnservedLanes = 3 }, WaveProbeReasonCapacityUnreadable},
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
			Target: tgt, HighWaterTreasury: hw, HighWaterReadable: true,
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

package common

import "testing"

// cappedFrame is the LIVE frame this term was written against — staging 2026-08-08, the hour that
// cost 1,585,996 credits. The operator capped heavies at the 4 owned; depth then ran 1,983 -> 2,772
// against an unmoving 1,540 units of hold while 82 lanes stayed unserved, and every probe bought
// widened a gap only a hull could close.
func cappedFrame() ProbeSpendInputs {
	return ProbeSpendInputs{
		GrowthEnabled:           true,
		UnservedLanes:           82,
		UnservedLanesReadable:   true,
		TradeHoldCapacity:       1_540,
		TradeSaturated:          false,
		TradeSaturationReadable: true,
		HeavyCapBinding:         true,
	}
}

// THE WIRE VALUE IS THE OPERATOR'S VOCABULARY, exactly as the wave's own reasons are: it is a
// metric label and a heartbeat field, so a rename with nothing failing renames a series out from
// under whoever is watching it.
func TestProbeSpendHoldWireValuesArePinned(t *testing.T) {
	for _, tc := range []struct {
		constant string
		got      string
		want     string
	}{
		{"ProbeSpendHoldNone", string(ProbeSpendHoldNone), ""},
		{"ProbeSpendHoldHeavyCapped", string(ProbeSpendHoldHeavyCapped), "heavy_capped_ample_depth"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s emits %q, want %q", tc.constant, tc.got, tc.want)
		}
	}
}

// THE BEAD'S OWN FRAME. Everything the loop needs is present: a capped class, a surface deeper than
// the pool, and lanes still asking for hulls.
func TestProbeSpendHold_CappedFleetIntoAmpleDepthIsHeld(t *testing.T) {
	if got := DeriveProbeSpendHold(cappedFrame()); got != ProbeSpendHoldHeavyCapped {
		t.Fatalf("a capped fleet holding 1,540 against 2,772 units of depth: got %q, want %q",
			got, ProbeSpendHoldHeavyCapped)
	}
}

// THE ANTI-VACUITY CONTROL, and the more important of the two directions. Depth 1,983 on 1,540 of
// hold with heavies UNCAPPED is the ordinary expansion regime — the whole growth leg of the design
// — and this term must be invisible in it. A fix that stops probe buying here is a regression.
func TestProbeSpendHold_UncappedGrowthRegimeIsUntouched(t *testing.T) {
	in := cappedFrame()
	in.HeavyCapBinding = false
	if got := DeriveProbeSpendHold(in); got != ProbeSpendHoldNone {
		t.Fatalf("uncapped growth regime: got %q, want no hold", got)
	}
}

// EVERY RELEASE RUNG, one case each. The term refuses a purchase only on POSITIVE evidence of both
// halves; anything absent, unreadable or non-positive releases, which leaves today's behaviour
// exactly as it is.
func TestProbeSpendHold_EveryAbsentOrBlindInputReleases(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*ProbeSpendInputs)
	}{
		{"no heavy buyer switched on — a probe-only deployment must keep probing", func(in *ProbeSpendInputs) { in.GrowthEnabled = false }},
		{"the cap is not what bars the heavy", func(in *ProbeSpendInputs) { in.HeavyCapBinding = false }},
		{"the lane surface could not be read", func(in *ProbeSpendInputs) { in.UnservedLanesReadable = false }},
		{"no lane is asking for a hull, so coverage is the right spend", func(in *ProbeSpendInputs) { in.UnservedLanes = 0 }},
		{"the depth reading could not be taken", func(in *ProbeSpendInputs) { in.TradeSaturationReadable = false }},
		{"no trade pool stands, so there is no hold to exceed", func(in *ProbeSpendInputs) { in.TradeHoldCapacity = 0 }},
		{"the surface is already saturated — probing IS the answer", func(in *ProbeSpendInputs) { in.TradeSaturated = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := cappedFrame()
			tc.mutate(&in)
			if got := DeriveProbeSpendHold(in); got != ProbeSpendHoldNone {
				t.Fatalf("%s: got %q, want no hold", tc.name, got)
			}
		})
	}
}

// DEMAND-REDUCING BY CONSTRUCTION, pinned over the whole input space rather than argued. The
// function's range is exactly {none, the one hold}: no input can produce a THIRD answer, and in
// particular none can produce a value a caller would read as permission. The caller's own gate is a
// conjunction, so "not none" can only ever subtract a purchase.
//
// sawHold is the calibration: a sweep in which the term never fired would pass this vacuously, and
// then keep passing after a mutation that disabled the term entirely.
func TestProbeSpendHold_RangeIsNoneOrTheOneHold(t *testing.T) {
	sawHold, sawNone := false, false
	for _, growth := range []bool{false, true} {
		for _, lanes := range []int{-1, 0, 1, 82} {
			for _, lanesOK := range []bool{false, true} {
				for _, hold := range []int{-1, 0, 1, 1_540} {
					for _, sat := range []bool{false, true} {
						for _, satOK := range []bool{false, true} {
							for _, capped := range []bool{false, true} {
								got := DeriveProbeSpendHold(ProbeSpendInputs{
									GrowthEnabled:           growth,
									UnservedLanes:           lanes,
									UnservedLanesReadable:   lanesOK,
									TradeHoldCapacity:       hold,
									TradeSaturated:          sat,
									TradeSaturationReadable: satOK,
									HeavyCapBinding:         capped,
								})
								switch got {
								case ProbeSpendHoldNone:
									sawNone = true
								case ProbeSpendHoldHeavyCapped:
									sawHold = true
									// Every hold must carry the full positive case. This is the
									// clause that makes the sweep an assertion rather than a census.
									if !growth || !capped || !lanesOK || lanes <= 0 || !satOK || hold <= 0 || sat {
										t.Fatalf("held on incomplete evidence: growth=%v capped=%v lanes=%d/%v hold=%d sat=%v/%v",
											growth, capped, lanes, lanesOK, hold, sat, satOK)
									}
								default:
									t.Fatalf("a third answer escaped the predicate: %q", got)
								}
							}
						}
					}
				}
			}
		}
	}
	if !sawHold || !sawNone {
		t.Fatalf("the sweep never exercised both answers (hold=%v none=%v) — it would pass with the term deleted", sawHold, sawNone)
	}
}

// THE PUBLISHED SET MUST COVER THE PREDICATE. Every reason is written to the gauge on every tick so
// that a superseded one falls to 0; a hold the predicate can return but the list does not name
// would publish a 1 when it fired and then never fall back — a series claiming the fleet is
// refusing to spend long after it resumed. This is the test that fails when a hold is added and its
// registration is forgotten.
func TestProbeSpendHolds_ListsEveryHoldThePredicateCanReturn(t *testing.T) {
	published := map[ProbeSpendHold]bool{}
	for _, h := range ProbeSpendHolds() {
		if h == ProbeSpendHoldNone {
			t.Fatal("ProbeSpendHoldNone is the absence of a reason and must not be published as one")
		}
		published[h] = true
	}

	reachable := map[ProbeSpendHold]bool{}
	for _, growth := range []bool{false, true} {
		for _, lanes := range []int{0, 82} {
			for _, lanesOK := range []bool{false, true} {
				for _, hold := range []int{0, 1_540} {
					for _, sat := range []bool{false, true} {
						for _, satOK := range []bool{false, true} {
							for _, capped := range []bool{false, true} {
								got := DeriveProbeSpendHold(ProbeSpendInputs{
									GrowthEnabled:           growth,
									UnservedLanes:           lanes,
									UnservedLanesReadable:   lanesOK,
									TradeHoldCapacity:       hold,
									TradeSaturated:          sat,
									TradeSaturationReadable: satOK,
									HeavyCapBinding:         capped,
								})
								if got != ProbeSpendHoldNone {
									reachable[got] = true
								}
							}
						}
					}
				}
			}
		}
	}
	if len(reachable) == 0 {
		t.Fatal("the sweep reached no hold at all — it cannot vouch for the published set")
	}
	for h := range reachable {
		if !published[h] {
			t.Fatalf("DeriveProbeSpendHold can return %q, but ProbeSpendHolds does not publish it — its gauge would never fall back to 0", h)
		}
	}
}

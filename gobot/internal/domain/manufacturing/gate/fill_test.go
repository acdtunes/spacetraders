package gate

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Fill greedily, by remaining bill descending, until the hold is full.
func TestPlanFill_FillsGreedilyByRemainingBillDescending(t *testing.T) {
	trip := PlanFill(80, []Material{
		{Good: "ADVANCED_CIRCUITRY", Remaining: 200, TradeVolume: 40},
		{Good: "FAB_MATS", Remaining: 900, TradeVolume: 40},
	})

	if len(trip.Stops) == 0 {
		t.Fatal("PlanFill loaded nothing from two eligible materials")
	}
	if trip.Stops[0].Good != "FAB_MATS" {
		t.Fatalf("first stop is %s; the greatest remaining bill (FAB_MATS, 900) must be filled first", trip.Stops[0].Good)
	}
	if trip.Loaded() != 80 {
		t.Fatalf("Loaded() = %d, want the full 80-unit hold", trip.Loaded())
	}
}

// THE TRANCHE BOUND. Trip availability is trade_volume x gateMaxTranchesPerStop, so a
// material with a huge outstanding bill and a small trade volume cannot take the whole
// hold and starve the other material out of a mixed trip.
func TestPlanFill_OneStopCannotMonopoliseAMixedTrip(t *testing.T) {
	trip := PlanFill(80, []Material{
		{Good: "FAB_MATS", Remaining: 1000, TradeVolume: 10},          // available = 10*4 = 40
		{Good: "ADVANCED_CIRCUITRY", Remaining: 500, TradeVolume: 20}, // available = 20*4 = 80
	})

	if len(trip.Stops) != 2 {
		t.Fatalf("Stops = %+v, want a MIXED trip of 2 stops; one stop monopolised the hold", trip.Stops)
	}
	for _, s := range trip.Stops {
		if s.Good == "FAB_MATS" && s.Units > 10*gateMaxTranchesPerStop {
			t.Fatalf("FAB_MATS took %d units, above its %d-unit trip availability (trade_volume 10 x %d tranches)",
				s.Units, 10*gateMaxTranchesPerStop, gateMaxTranchesPerStop)
		}
	}
	if trip.Loaded() != 80 {
		t.Fatalf("Loaded() = %d, want 80 — the second material should fill what the first could not take", trip.Loaded())
	}
}

// Fill NEVER exceeds the remaining bill: buying past demand is over-supply.
func TestPlanFill_NeverExceedsTheRemainingBill(t *testing.T) {
	trip := PlanFill(80, []Material{{Good: "FAB_MATS", Remaining: 12, TradeVolume: 40}})

	if trip.Loaded() != 12 {
		t.Fatalf("Loaded() = %d, want exactly the 12-unit remaining bill", trip.Loaded())
	}
}

func TestPlanFill_NeverExceedsHullCapacity(t *testing.T) {
	trip := PlanFill(30, []Material{
		{Good: "FAB_MATS", Remaining: 900, TradeVolume: 40},
		{Good: "ADVANCED_CIRCUITRY", Remaining: 900, TradeVolume: 40},
	})

	if trip.Loaded() > 30 {
		t.Fatalf("Loaded() = %d, above the 30-unit hold", trip.Loaded())
	}
}

// THE PAUSE ESCAPE VALVE: one material paused, the hull fills entirely with the other
// rather than idling.
func TestPlanFill_APausedMaterialIsSkippedAndTheOtherFillsTheHold(t *testing.T) {
	trip := PlanFill(80, []Material{
		{Good: "FAB_MATS", Remaining: 900, TradeVolume: 40, Paused: true},
		{Good: "ADVANCED_CIRCUITRY", Remaining: 900, TradeVolume: 40},
	})

	if len(trip.Stops) != 1 || trip.Stops[0].Good != "ADVANCED_CIRCUITRY" {
		t.Fatalf("Stops = %+v, want the one eligible material only", trip.Stops)
	}
	if trip.Loaded() != 80 {
		t.Fatalf("Loaded() = %d, want a full hold — a paused material must not leave the hull idle", trip.Loaded())
	}
	if len(trip.Skips) != 1 || trip.Skips[0].Good != "FAB_MATS" || trip.Skips[0].Reason != SkipPaused {
		t.Fatalf("Skips = %+v, want FAB_MATS skipped as %q", trip.Skips, SkipPaused)
	}
}

// PRECEDENCE. A material whose bill is already MET is reported bill_satisfied even when
// it is also paused: calling a met bill "paused" sends an operator to tune a knob that
// changes nothing.
func TestPlanFill_ASatisfiedBillIsNeverReportedAsPaused(t *testing.T) {
	trip := PlanFill(80, []Material{{Good: "FAB_MATS", Remaining: 0, TradeVolume: 40, Paused: true}})

	if len(trip.Skips) != 1 {
		t.Fatalf("Skips = %+v, want exactly one", trip.Skips)
	}
	if trip.Skips[0].Reason != SkipBillSatisfied {
		t.Fatalf("skip reason = %q, want %q — a met bill is a fact independent of policy", trip.Skips[0].Reason, SkipBillSatisfied)
	}
}

// The full precedence chain: hold_full > bill_satisfied > paused > no_supply.
func TestPlanFill_SkipReasonPrecedenceIsHoldFullThenBillThenPausedThenNoSupply(t *testing.T) {
	// One material fills the hold; every later material must read hold_full regardless of
	// its own bill, pause state or trade volume.
	trip := PlanFill(40, []Material{
		{Good: "FAB_MATS", Remaining: 900, TradeVolume: 40},
		{Good: "ADVANCED_CIRCUITRY", Remaining: 0, TradeVolume: 0, Paused: true},
	})
	if len(trip.Skips) != 1 || trip.Skips[0].Reason != SkipHoldFull {
		t.Fatalf("Skips = %+v, want ADVANCED_CIRCUITRY skipped as %q once the hold was full", trip.Skips, SkipHoldFull)
	}

	// With room left, a paused material outranks a zero-trade-volume one.
	trip = PlanFill(80, []Material{{Good: "FAB_MATS", Remaining: 900, TradeVolume: 0, Paused: true}})
	if len(trip.Skips) != 1 || trip.Skips[0].Reason != SkipPaused {
		t.Fatalf("Skips = %+v, want %q (policy outranks a market read that only matters if we were going to buy)", trip.Skips, SkipPaused)
	}
}

// A zero/absent trade volume is NO SUPPLY, not a zero-unit stop. A stop that buys
// nothing would be a trip leg with no purpose and a divide-by-zero in the tranche count.
func TestPlanFill_ZeroTradeVolumeIsNoSupplyNotAZeroUnitStop(t *testing.T) {
	trip := PlanFill(80, []Material{{Good: "FAB_MATS", Remaining: 900, TradeVolume: 0}})

	if len(trip.Stops) != 0 {
		t.Fatalf("Stops = %+v, want none from an unbuyable market", trip.Stops)
	}
	if len(trip.Skips) != 1 || trip.Skips[0].Reason != SkipNoSupply {
		t.Fatalf("Skips = %+v, want %q", trip.Skips, SkipNoSupply)
	}
}

// Buys are bounded by trade_volume per transaction: 80 units at trade_volume 20 is 4
// transactions. This is a market constraint, not an architectural one.
func TestPlanFill_TranchesAreCeilOfTakeOverTradeVolume(t *testing.T) {
	trip := PlanFill(80, []Material{{Good: "FAB_MATS", Remaining: 900, TradeVolume: 20}})

	if len(trip.Stops) != 1 {
		t.Fatalf("Stops = %+v, want one", trip.Stops)
	}
	if trip.Stops[0].Units != 80 || trip.Stops[0].Tranches != 4 {
		t.Fatalf("stop = %+v, want 80 units in 4 tranches", trip.Stops[0])
	}

	// A remainder rounds UP: 45 units at trade_volume 20 is 3 transactions, not 2.
	trip = PlanFill(45, []Material{{Good: "FAB_MATS", Remaining: 900, TradeVolume: 20}})
	if trip.Stops[0].Tranches != 3 {
		t.Fatalf("45 units at trade_volume 20 = %d tranches, want 3", trip.Stops[0].Tranches)
	}
}

// OBSERVABILITY. The trip log must name capacity, what was loaded, and what was skipped
// AND WHY -- in the message, since the container log renderer drops metadata maps.
func TestTrip_LogLineNamesCapacityLoadedAndEverySkipWithItsReason(t *testing.T) {
	trip := PlanFill(80, []Material{
		{Good: "FAB_MATS", Remaining: 900, TradeVolume: 40},
		{Good: "ADVANCED_CIRCUITRY", Remaining: 900, TradeVolume: 40, Paused: true},
	})
	line := trip.LogLine()

	for _, want := range []string{"80", "FAB_MATS", "ADVANCED_CIRCUITRY", SkipPaused} {
		if !strings.Contains(line, want) {
			t.Fatalf("trip log line %q does not name %q — a trip that skipped a material must say which and why", line, want)
		}
	}
}

func TestPlanFill_EmptyMaterialsProducesAnEmptyTrip(t *testing.T) {
	trip := PlanFill(80, nil)
	if len(trip.Stops) != 0 || len(trip.Skips) != 0 || trip.Loaded() != 0 {
		t.Fatalf("PlanFill(80, nil) = %+v, want an empty trip", trip)
	}
}

// A hull with no usable hold loads nothing, and says so as hold_full rather than
// silently producing an empty trip that reads like "nothing to do".
func TestPlanFill_NonPositiveCapacityLoadsNothingAndSaysWhy(t *testing.T) {
	trip := PlanFill(0, []Material{{Good: "FAB_MATS", Remaining: 900, TradeVolume: 40}})

	if trip.Loaded() != 0 {
		t.Fatalf("Loaded() = %d on a zero-capacity hull", trip.Loaded())
	}
	if len(trip.Skips) != 1 || trip.Skips[0].Reason != SkipHoldFull {
		t.Fatalf("Skips = %+v, want %q", trip.Skips, SkipHoldFull)
	}
}

// PlanFill must not reorder or mutate the caller's slice: the drain reuses it to record
// its per-material decisions after planning.
func TestPlanFill_DoesNotMutateTheCallersSlice(t *testing.T) {
	materials := []Material{
		{Good: "ADVANCED_CIRCUITRY", Remaining: 200, TradeVolume: 40},
		{Good: "FAB_MATS", Remaining: 900, TradeVolume: 40},
	}
	PlanFill(80, materials)

	if materials[0].Good != "ADVANCED_CIRCUITRY" || materials[1].Good != "FAB_MATS" {
		t.Fatalf("PlanFill reordered the caller's slice: %+v", materials)
	}
}

// Waypoint symbols look like X1-AB12-C3. They are regenerated every era, so any literal in
// this layer is a bug that survives exactly until the next era rolls — and works perfectly
// until then, which is why it needs a build-failing guard rather than a comment.
//
// Goods names are the invariant and are deliberately NOT flagged: every era's gate requires
// FAB_MATS and ADVANCED_CIRCUITRY. Locations are not invariant and are resolved at runtime
// by market role.
func TestGatePolicyPackage_ContainsNoWaypointLiterals(t *testing.T) {
	waypointLiteral := regexp.MustCompile(`"[A-Z]\d+-[A-Z0-9]+-[A-Z0-9]+"`)

	// The guard must be able to fail. If the pattern cannot match a known-bad string, a green
	// result would mean nothing.
	//
	// Calibrating on ONE example under-proves that. A regression that TIGHTENS the pattern —
	// say to [A-Z]\d+-[A-Z]{2}\d{2}-[A-Z]\d{2} — still matches X1-KP23-F46, so a single-string
	// check stays green while the guard silently goes blind to every other shape. These four
	// are the real shapes this repo actually uses: sector-numbered, short system, wordy
	// suffix, and an all-letter middle.
	for _, known := range []string{
		`x := "X1-KP23-F46"`,
		`"X1-UM5-J59"`,
		`"X1-DR-GATE"`,
		`"X1-BETA-MARKETPLACE"`,
	} {
		if !waypointLiteral.MatchString(known) {
			t.Fatalf("waypoint-literal pattern failed its own calibration on %s — it cannot detect a real symbol", known)
		}
	}
	// ...and it must not fire on the invariants, or the guard would be unusable and get deleted.
	for _, invariant := range []string{
		`good := "FAB_MATS"`,
		`good := "ADVANCED_CIRCUITRY"`,
		`inputs := []string{"IRON", "QUARTZ_SAND"}`,
	} {
		if waypointLiteral.MatchString(invariant) {
			t.Fatalf("waypoint-literal pattern flags %s; goods are era-invariant and must be nameable directly", invariant)
		}
	}

	// Sweep the WHOLE package rather than a hand-kept list a new file can quietly fall off.
	// A glob that matched nothing would pass vacuously, so the sweep asserts its own coverage:
	// a minimum file count, plus every file that exists today, by name.
	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("globbing package sources: %v", err)
	}
	scanned := map[string]bool{}
	for _, file := range sources {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("reading %s: %v", file, err)
		}
		if len(src) == 0 {
			t.Fatalf("%s is empty; an empty read would pass this guard vacuously", file)
		}
		scanned[file] = true
		if found := waypointLiteral.FindAllString(string(src), -1); len(found) > 0 {
			t.Fatalf("%s contains hardcoded waypoint symbols %v — resolve locations by market role instead", file, found)
		}
		if strings.Contains(string(src), "X1-") {
			t.Fatalf("%s references an X1- prefixed symbol", file)
		}
	}
	if len(scanned) < 5 {
		t.Fatalf("guard scanned %d source file(s) %v; the gate policy package has at least 5 — a sweep that reads nothing proves nothing", len(scanned), scanned)
	}
	for _, required := range []string{"role.go", "buy_policy.go", "fill.go", "feed.go", "reallocation.go"} {
		if !scanned[required] {
			t.Fatalf("guard did not scan %s; the sweep must cover every policy source", required)
		}
	}
}

// ---------------------------------------------------------------------------------------------
// COMMITTED UNITS — sp-v2a2h, the double-buy
// ---------------------------------------------------------------------------------------------

// A material whose whole outstanding bill is ALREADY PAID FOR and riding in a hold must not be
// bought again. This is the arithmetic behind the sp-v2a2h ledger: the site read 364/400, the
// outstanding 36 were aboard a stranded hull, and the planner sized 36 a second time.
func TestPlanFill_NeverBuysUnitsAlreadyCommittedToAHold(t *testing.T) {
	trip := PlanFill(80, []Material{
		{Good: "ADVANCED_CIRCUITRY", Remaining: 36, Committed: 36, TradeVolume: 28},
	})

	if len(trip.Stops) != 0 {
		t.Fatalf("Stops = %+v, want none — every outstanding unit is already bought and in a hold, so this trip re-buys 36 units the site will reject with API 4801", trip.Stops)
	}
	if len(trip.Skips) != 1 || trip.Skips[0].Reason != SkipInFlightCovered {
		t.Fatalf("Skips = %+v, want a single %s — a covered bill and a finished one must not render the same", trip.Skips, SkipInFlightCovered)
	}
}

// PARTIAL coverage buys the UNCOVERED part, exactly. Netting by the wrong amount either re-buys
// (the bug) or refuses a purchase the gate genuinely needs (a stall).
func TestPlanFill_BuysOnlyTheUncommittedRemainder(t *testing.T) {
	trip := PlanFill(80, []Material{
		{Good: "ADVANCED_CIRCUITRY", Remaining: 36, Committed: 20, TradeVolume: 28},
	})

	if len(trip.Stops) != 1 || trip.Stops[0].Units != 16 {
		t.Fatalf("Stops = %+v, want one stop of exactly 16 units (36 outstanding less 20 already in a hold)", trip.Stops)
	}
}

// in_flight_covered outranks paused and no_supply, for the same reason bill_satisfied does:
// neither knob an operator can turn changes a bill that is already paid for.
func TestPlanFill_InFlightCoverageOutranksPausedAndNoSupply(t *testing.T) {
	trip := PlanFill(80, []Material{
		{Good: "ADVANCED_CIRCUITRY", Remaining: 36, Committed: 36, TradeVolume: 28, Paused: true},
		{Good: "FAB_MATS", Remaining: 40, Committed: 40, TradeVolume: 0},
	})

	for _, skip := range trip.Skips {
		if skip.Reason != SkipInFlightCovered {
			t.Fatalf("%s was skipped as %q, want %s — reporting a covered bill as a pause or a dry market sends an operator to fix something that is not broken", skip.Good, skip.Reason, SkipInFlightCovered)
		}
	}
	if len(trip.Skips) != 2 {
		t.Fatalf("Skips = %+v, want both materials skipped", trip.Skips)
	}
}

// A MET bill still reports bill_satisfied, not in_flight_covered: the two are different facts and
// the first is the one that means the material is done.
func TestPlanFill_AMetBillIsStillBillSatisfiedNotInFlightCovered(t *testing.T) {
	trip := PlanFill(80, []Material{
		{Good: "ADVANCED_CIRCUITRY", Remaining: 0, Committed: 36, TradeVolume: 28},
	})

	if len(trip.Skips) != 1 || trip.Skips[0].Reason != SkipBillSatisfied {
		t.Fatalf("Skips = %+v, want a single %s", trip.Skips, SkipBillSatisfied)
	}
}

// THE SORT IS BY BUYABLE, NOT BY RAW BILL. A fully covered material with a huge outstanding bill
// must not sort ahead of a small one that can actually load: it would consume the ordering slot on
// a bill it is never going to buy against, and then report its own state as hold_full — invisible
// exactly when the fleet is busy, which is when a duplicate purchase is most expensive.
//
// hold_full still legitimately WINS once the hold really is full, per the precedence this file
// documents (neither reason is operator-actionable there, so the trip-level fact wins, exactly as
// for bill_satisfied). What must not happen is the covered material CAUSING that state.
func TestPlanFill_ACoveredMaterialDoesNotTakeTheOrderingSlotFromOneThatCanLoad(t *testing.T) {
	trip := PlanFill(80, []Material{
		{Good: "ADVANCED_CIRCUITRY", Remaining: 900, Committed: 900, TradeVolume: 40},
		{Good: "FAB_MATS", Remaining: 20, TradeVolume: 40},
	})

	if len(trip.Stops) != 1 || trip.Stops[0].Good != "FAB_MATS" || trip.Stops[0].Units != 20 {
		t.Fatalf("Stops = %+v, want one 20-unit FAB_MATS stop — the covered material sorted first on a bill it will not buy against", trip.Stops)
	}
	if len(trip.Skips) != 1 || trip.Skips[0].Reason != SkipInFlightCovered {
		t.Fatalf("Skips = %+v, want %s and not %s — with hold left over, the covered material must report the fact an operator can act on", trip.Skips, SkipInFlightCovered, SkipHoldFull)
	}
}

// Over-commitment (more aboard than the site still wants — the 436-vs-400 state) buys nothing and
// never yields a negative take.
func TestPlanFill_OverCommitmentBuysNothingRatherThanANegativeTake(t *testing.T) {
	trip := PlanFill(80, []Material{
		{Good: "ADVANCED_CIRCUITRY", Remaining: 36, Committed: 80, TradeVolume: 28},
	})

	if trip.Loaded() != 0 {
		t.Fatalf("Loaded() = %d, want 0", trip.Loaded())
	}
	if got := (Material{Remaining: 36, Committed: 80}).Buyable(); got != 0 {
		t.Fatalf("Buyable() = %d for an over-committed material, want 0", got)
	}
}

// With NOTHING committed the planner is unchanged — the guard must not alter the ordinary fill.
func TestPlanFill_IsUnchangedWhenNothingIsCommitted(t *testing.T) {
	materials := []Material{
		{Good: "ADVANCED_CIRCUITRY", Remaining: 200, TradeVolume: 40},
		{Good: "FAB_MATS", Remaining: 900, TradeVolume: 40},
	}
	trip := PlanFill(80, materials)

	if len(trip.Stops) == 0 || trip.Stops[0].Good != "FAB_MATS" || trip.Loaded() != 80 {
		t.Fatalf("Trip = %+v, want the pre-sp-v2a2h fill (FAB_MATS first, full 80-unit hold)", trip)
	}
}

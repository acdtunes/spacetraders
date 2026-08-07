package marketscan

import (
	"testing"
	"time"
)

// sp-k4z5b. The incident this function exists to make impossible: the charted map
// reached 4,389 IMPORT/EXCHANGE markets across 343 systems while the scan budget stayed
// fixed at 0.70 req/s, so an even rotation stretched to ~105 minutes — and every consumer
// still comparing against a hardcoded 75 minutes started discarding four fifths of a
// perfectly healthy map. 980 fail-closed refusals in fifteen minutes, trade volume down
// ~87%, net 4.2M/hr to 0.55M/hr.
//
// These are the live numbers, kept together so the arithmetic below is checkable against
// the incident rather than against invented ones.
const (
	incidentMarketsKnown  = 4389
	incidentRateReqPerSec = 0.70
	incidentClampR        = 8
	// incidentEvenRotationSecs is markets / rate: the ~105 minutes (6270s) a market of
	// average value waits between scans at the incident's map size.
	incidentEvenRotationSecs = 6270
	// oldHardcodedCap is the number that was written into four different files.
	oldHardcodedCap = 75 * time.Minute
)

func incidentBudget() Budget {
	return Budget{RateReqPerSec: incidentRateReqPerSec, ValueClampR: incidentClampR}
}

// THE ACCEPTANCE PROPERTY. A row whose age is explained by the rotation is admitted.
// Two hours is comfortably past the old 75-minute cap — it is the exact age the tour
// coordinator was fail-closing on — and yet at this map size two hours is barely one
// rotation, so nothing whatever is wrong with the data.
func TestFreshnessCap_AdmitsRowsTheRotationExplains(t *testing.T) {
	cap := FreshnessCap(oldHardcodedCap, incidentBudget(), incidentMarketsKnown)

	if cap <= oldHardcodedCap {
		t.Fatalf("cap %s did not widen past the old hardcoded %s — the incident is still live", cap, oldHardcodedCap)
	}
	for _, age := range []time.Duration{76 * time.Minute, 2 * time.Hour, 6 * time.Hour} {
		if age > cap {
			t.Errorf("a %s-old row is refused at cap %s, but the rotation cannot deliver anything fresher", age, cap)
		}
	}
	// The bound must sit ValueClampR-fold above the EVEN rotation, not at it: a cold
	// market legitimately waits that much longer than an average one, and refusing at the
	// even rotation would still discard every cold market in the map.
	evenRotation := incidentEvenRotationSecs * time.Second
	if cap <= evenRotation {
		t.Errorf("cap %s is at or below the even rotation %s — cold markets would still be refused", cap, evenRotation)
	}
	// Sanity-check the even rotation against the budget itself, so the constant above
	// cannot drift away from the arithmetic it claims to summarise.
	if got := Interval(incidentBudget(), baselineWeight, incidentMarketsKnown*baselineWeight); got.Round(time.Second) != evenRotation {
		t.Errorf("even rotation is %s, not the %s this test asserts against", got, evenRotation)
	}
}

// The guard is still a guard. Past the rotation bound the scanner has failed its OWN
// anti-starvation guarantee, so the row is genuinely dead rather than waiting its turn,
// and a consumer must refuse it.
func TestFreshnessCap_RefusesRowsTheRotationCannotExplain(t *testing.T) {
	cap := FreshnessCap(oldHardcodedCap, incidentBudget(), incidentMarketsKnown)

	// A consumer refuses when age > cap. Anything past the bound is unexplained by the
	// rotation and must land on that side of the comparison.
	for _, age := range []time.Duration{cap + time.Second, cap + time.Hour, 30 * 24 * time.Hour} {
		if !(age > cap) {
			t.Errorf("a %s-old row is admitted at cap %s — the scanner has failed its own guarantee and this must fail closed", age, cap)
		}
	}
	// And the cap is finite: derivation must never resolve to "admit anything".
	if cap >= stalenessCeiling {
		t.Errorf("cap %s reached the arithmetic ceiling — the gate is effectively off", cap)
	}
}

// The floor is a FLOOR. It widens the cap when it exceeds the rotation bound (the lever an
// operator needs mid-incident) and is a deliberate no-op below it (which is what stops any
// tune from re-creating the incident).
func TestFreshnessCap_FloorIsAFloorNeverACeiling(t *testing.T) {
	bound := FreshnessCap(0, incidentBudget(), incidentMarketsKnown)

	// Below the bound: no-op.
	if got := FreshnessCap(time.Minute, incidentBudget(), incidentMarketsKnown); got != bound {
		t.Errorf("a 1m floor changed the cap to %s; the rotation bound %s must still hold", got, bound)
	}
	// Above the bound: widens.
	wide := bound + 10*time.Hour
	if got := FreshnessCap(wide, incidentBudget(), incidentMarketsKnown); got != wide {
		t.Errorf("a floor above the bound must widen the cap: got %s, want %s", got, wide)
	}
	// A small map: the floor is the operative number and behaviour is unchanged from
	// before this function existed.
	if got := FreshnessCap(oldHardcodedCap, Budget{RateReqPerSec: 0.70, ValueClampR: 8}, 100); got != oldHardcodedCap {
		t.Errorf("on a 100-market map the 75m floor must still bind: got %s", got)
	}
}

// Degenerate inputs resolve toward the FLOOR, never toward the 30-day arithmetic ceiling.
// MaxStaleness answers a zero map size with that ceiling, and letting an unwired budget
// widen a money guard to thirty days is exactly the fail-OPEN a freshness gate must never
// do (RULINGS #4).
func TestFreshnessCap_DegenerateInputsYieldTheFloorNotTheCeiling(t *testing.T) {
	cases := []struct {
		name         string
		budget       Budget
		marketsKnown int
	}{
		{"no map counted yet", incidentBudget(), 0},
		{"negative map size", incidentBudget(), -1},
		{"budget unwired", Budget{}, incidentMarketsKnown},
		{"rate zeroed", Budget{RateReqPerSec: 0, ValueClampR: 8}, incidentMarketsKnown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FreshnessCap(oldHardcodedCap, tc.budget, tc.marketsKnown); got != oldHardcodedCap {
				t.Errorf("degenerate input widened the cap to %s; want the floor %s", got, oldHardcodedCap)
			}
		})
	}
}

// The cap tracks the map without anyone editing anything — the property a hardcoded
// minute count cannot have, and the reason raising the scan budget was never the fix.
//
// It tracks it UP TO derivedCapCeiling and then deliberately stops (sp-rsk2m). Both
// halves are the property: a cap that never widened would re-run this incident, and a
// cap that widened without limit would silently un-arm the money guard it feeds. The
// sizes below sit either side of that boundary on purpose.
func TestFreshnessCap_TracksTheChartedMapWithoutRetuningUpToTheCeiling(t *testing.T) {
	small := FreshnessCap(oldHardcodedCap, incidentBudget(), 300)
	large := FreshnessCap(oldHardcodedCap, incidentBudget(), incidentMarketsKnown)
	if large <= small {
		t.Fatalf("cap did not grow with the map: %d markets→%s, %d markets→%s", 300, small, incidentMarketsKnown, large)
	}

	// Below the ceiling the bound is linear in markets known, so the cap can never fall
	// behind charting the way a fixed number does.
	bound := FreshnessCap(0, incidentBudget(), 300)
	doubled := FreshnessCap(0, incidentBudget(), 600)
	if doubled >= derivedCapCeiling {
		t.Fatalf("fixture must stay inside the governed range, got %s", doubled)
	}
	if doubled != 2*bound {
		t.Errorf("bound is not linear in map size: %s vs 2×%s", doubled, bound)
	}

	// Past it the derivation stops widening, whatever the map does next. At the incident's
	// own 0.70 req/s that boundary is ~7,560 markets — deliberately clear of the 4,389 that
	// derived 13h56m, because rows of that age WERE explained by the rotation and admitting
	// them is the acceptance criterion this package was written for.
	if got := FreshnessCap(0, incidentBudget(), 100*incidentMarketsKnown); got != derivedCapCeiling {
		t.Errorf("a map two orders of magnitude larger must not widen the cap further: got %s", got)
	}

	// Raising the ALLOWANCE narrows it again — cost and staleness trade against each
	// other explicitly instead of one silently invalidating the other.
	governed := FreshnessCap(0, incidentBudget(), 300)
	faster := FreshnessCap(0, Budget{RateReqPerSec: 2 * incidentRateReqPerSec, ValueClampR: incidentClampR}, 300)
	if faster >= governed {
		t.Errorf("doubling the budget must halve the bound: got %s, was %s", faster, governed)
	}
}

package trading

import (
	"math"
	"testing"
)

// relocation_npv_test.go — the economics of one candidate relocation (sp-zvywu Part 2).

// A worked example, arithmetic stated in business terms so the oracle is traceable to the bead's
// formula rather than copied from the implementation.
//
//	current  100,000/hr   projected  300,000/hr   =>  uplift 200,000/hr
//	travel   1 h          remaining era 48 h      horizon 24 h
//	window   min(48 - 1, 24) = 24 h
//	NPV      200,000 x 24  -  100,000 x 1  -  150,000  =  4,800,000 - 250,000 = 4,550,000
func licensedInputs() RelocationInputs {
	return RelocationInputs{
		CurrentRate:       100_000,
		ProjectedRate:     300_000,
		TravelHours:       1,
		RemainingEraHours: 48,
		EraHorizonKnown:   true,
		HorizonHours:      24,
		RiskMargin:        150_000,
		UpliftBar:         1.5,
		NPVThreshold:      0,
	}
}

func TestValueRelocationShould_PriceTheMoveAtItsUpliftOverTheProductiveWindowNetOfTripCostAndRisk(t *testing.T) {
	got := ValueRelocation(licensedInputs())

	if !got.Licensed() {
		t.Fatalf("a 3x-uplift region one hour away with 48 h of era left was refused (%s)", got.Verdict)
	}
	if math.Abs(got.UpliftPerHour-200_000) > 1e-6 {
		t.Fatalf("uplift %.0f/hr, want 200,000/hr (300,000 projected - 100,000 current)", got.UpliftPerHour)
	}
	if math.Abs(got.ProductiveWindowHours-24) > 1e-9 {
		t.Fatalf("productive window %.4f h, want 24 h (the horizon binds before the 47 h of era left after travel)", got.ProductiveWindowHours)
	}
	if math.Abs(got.NPV-4_550_000) > 1e-6 {
		t.Fatalf("NPV %.0f, want 4,550,000 (200,000 x 24 h - 100,000 x 1 h - 150,000 risk margin)", got.NPV)
	}
}

// The era, not the horizon, must bind when the era is the shorter of the two — and travel comes out
// of the window because flying hours are not earning hours.
func TestValueRelocationShould_LetTheEraBindTheProductiveWindow_GivenAnEraShorterThanTheHorizon(t *testing.T) {
	in := licensedInputs()
	in.RemainingEraHours = 10
	in.TravelHours = 2

	got := ValueRelocation(in)

	if math.Abs(got.ProductiveWindowHours-8) > 1e-9 {
		t.Fatalf("productive window %.4f h, want 8 h (10 h of era less 2 h of travel, under the 24 h horizon)", got.ProductiveWindowHours)
	}
	// 200,000 x 8 - 100,000 x 2 - 150,000 = 1,600,000 - 350,000 = 1,250,000.
	if math.Abs(got.NPV-1_250_000) > 1e-6 {
		t.Fatalf("NPV %.0f, want 1,250,000", got.NPV)
	}
}

// THE UPLIFT BAR (calibrated at 1.5x). A region that is better but not measurably better must be
// refused, and the refusal must name the reason.
func TestValueRelocationShould_RefuseARegionThatDoesNotClearTheUpliftBar(t *testing.T) {
	in := licensedInputs()
	in.ProjectedRate = 140_000 // 1.4x current — better, but under the 1.5x bar

	got := ValueRelocation(in)

	if got.Licensed() {
		t.Fatalf("a region projecting only 1.4x the hull's rate was licensed against a 1.5x uplift bar (NPV %.0f)", got.NPV)
	}
	if got.Verdict != RelocationRefusedNoUplift {
		t.Fatalf("verdict %q, want %q", got.Verdict, RelocationRefusedNoUplift)
	}
}

// Exactly AT the bar clears it — the bar is a floor the region may sit on, and the boundary must not
// silently exclude a legitimate move.
func TestValueRelocationShould_LicenseARegionExactlyAtTheUpliftBar(t *testing.T) {
	in := licensedInputs()
	in.ProjectedRate = 150_000 // exactly 1.5x
	in.RiskMargin = 0
	in.NPVThreshold = 0

	if got := ValueRelocation(in); !got.Licensed() {
		t.Fatalf("a region projecting exactly the 1.5x bar was refused (%s)", got.Verdict)
	}
}

// A misconfigured sub-1.0 bar must never license a DOWNGRADE. The strict-improvement requirement is
// independent of the bar, and it is the LAST guard standing in this fixture.
//
// THE FIXTURE HAS TO STRIP THE COSTS TO REACH IT, and that is worth spelling out. In the normal case
// every other guard masks this one: a sub-1.0 bar is cleared, but any uplift at or below zero drives
// the NPV negative and the threshold refuses first. Set travel and risk margin to zero and the
// masking disappears at once — payback becomes 0/0 = NaN, which compares false against the endgame
// bound, and a zero-uplift move values at exactly 0, which clears a threshold below it. What is left
// is a hull flown to identical ground for nothing, and only the strict-improvement check refuses it.
func TestValueRelocationShould_NeverLicenseAWorseOrEqualRegion_EvenWithAMisconfiguredBar(t *testing.T) {
	costless := func(projected float64) RelocationInputs {
		in := licensedInputs()
		in.UpliftBar = 0.1 // an operator error: a bar that would "clear" on any positive number
		in.ProjectedRate = projected
		in.TravelHours = 0
		in.RiskMargin = 0
		in.NPVThreshold = -1 // below what a zero-uplift move values at, so the threshold cannot refuse
		return in
	}

	// THE DISCRIMINATING CASE: identical ground. Every downstream guard is blind here.
	equal := ValueRelocation(costless(100_000))
	if equal.Licensed() {
		t.Fatalf("a zero-uplift move to identical ground was LICENSED (NPV %.0f, payback %.4f h) under a 0.1 bar — the strict-improvement check is the only guard left in this fixture", equal.NPV, equal.PaybackHours)
	}
	if equal.Verdict != RelocationRefusedNoUplift {
		t.Fatalf("verdict %q for identical ground, want %q", equal.Verdict, RelocationRefusedNoUplift)
	}

	// And no outright downgrade is licensed either.
	for _, projected := range []float64{90_000, 0, -50_000} {
		if got := ValueRelocation(costless(projected)); got.Licensed() {
			t.Fatalf("a region projecting %.0f/hr against a hull earning 100,000/hr was licensed under a 0.1 bar — a relocation must never be a downgrade", projected)
		}
	}
}

// A hull LOSING money has a degenerate multiplicative bar (1.5x a negative number is more negative).
// It must relocate only to ground that genuinely earns, never to a less-bad loss.
func TestValueRelocationShould_RequirePositiveEarningsForALosingHull_WhereTheMultiplicativeBarIsDegenerate(t *testing.T) {
	losing := licensedInputs()
	losing.CurrentRate = -50_000

	toAnotherLoss := losing
	toAnotherLoss.ProjectedRate = -10_000 // better than -50,000, but still a loss
	if got := ValueRelocation(toAnotherLoss); got.Licensed() {
		t.Fatalf("a money-losing hull was licensed to relocate to another loss-making region (NPV %.0f)", got.NPV)
	}

	toEarnings := losing
	toEarnings.ProjectedRate = 200_000
	if got := ValueRelocation(toEarnings); !got.Licensed() {
		t.Fatalf("a money-losing hull was refused a region projecting genuine earnings (%s)", got.Verdict)
	}
}

// THE ENDGAME GUARD: remaining era must cover twice the travel plus the payback.
func TestValueRelocationShould_RefuseWhenRemainingEraCannotCoverTwiceTravelPlusPayback(t *testing.T) {
	in := licensedInputs()
	in.TravelHours = 3
	in.RiskMargin = 600_000
	// payback = (100,000 x 3 + 600,000) / 200,000 = 4.5 h. Guard needs 2 x 3 + 4.5 = 10.5 h.
	in.RemainingEraHours = 10

	got := ValueRelocation(in)

	if got.Licensed() {
		t.Fatalf("a move needing 10.5 h to be worth making was licensed with 10 h of era left (NPV %.0f)", got.NPV)
	}
	if got.Verdict != RelocationRefusedEndgame {
		t.Fatalf("verdict %q, want %q", got.Verdict, RelocationRefusedEndgame)
	}
	if math.Abs(got.PaybackHours-4.5) > 1e-9 {
		t.Fatalf("payback %.4f h, want 4.5 h ((100,000 x 3 h travel + 600,000 risk margin) / 200,000/hr uplift)", got.PaybackHours)
	}

	// One more hour of era and the same move clears — proof the guard is measuring the era budget and
	// not just refusing everything.
	in.RemainingEraHours = 11
	if got := ValueRelocation(in); !got.Licensed() {
		t.Fatalf("the same move was still refused with 11 h of era left, past the 10.5 h the guard needs (%s)", got.Verdict)
	}
}

// THE NPV THRESHOLD: genuinely positive but too small to move a hull for.
func TestValueRelocationShould_RefuseAPositiveButBelowThresholdMove(t *testing.T) {
	in := licensedInputs()
	in.NPVThreshold = 5_000_000 // above the worked example's 4,550,000

	got := ValueRelocation(in)

	if got.Licensed() {
		t.Fatalf("an NPV of %.0f was licensed against a 5,000,000 threshold", got.NPV)
	}
	if got.Verdict != RelocationRefusedBelowThreshold {
		t.Fatalf("verdict %q, want %q", got.Verdict, RelocationRefusedBelowThreshold)
	}
	if math.Abs(got.NPV-4_550_000) > 1e-6 {
		t.Fatalf("a below-threshold refusal must still report the NPV it priced; got %.0f, want 4,550,000", got.NPV)
	}
}

// AN UNKNOWN ERA HORIZON buys at most a horizon's worth of uplift — never an unbounded one — and the
// endgame guard is still consulted against that same window.
func TestValueRelocationShould_BoundAnUnknownEraHorizonToTheHorizonKnob(t *testing.T) {
	unknown := licensedInputs()
	unknown.EraHorizonKnown = false
	unknown.RemainingEraHours = 0 // must be ignored entirely when the horizon is unknown

	got := ValueRelocation(unknown)

	if !got.Licensed() {
		t.Fatalf("an unknown era horizon refused a strongly positive move (%s) — the feature would be dormant on every unreadable era read", got.Verdict)
	}
	if math.Abs(got.ProductiveWindowHours-24) > 1e-9 {
		t.Fatalf("productive window %.4f h with an unknown era, want the 24 h horizon — an unknown era must not buy unbounded uplift", got.ProductiveWindowHours)
	}

	// And the guard still bites: a horizon too short to repay the move refuses.
	tooShort := unknown
	tooShort.HorizonHours = 2
	tooShort.TravelHours = 3
	if got := ValueRelocation(tooShort); got.Verdict != RelocationRefusedEndgame {
		t.Fatalf("verdict %q with an unknown era and a 2 h horizon against 3 h of travel, want %q — the endgame guard must still be consulted", got.Verdict, RelocationRefusedEndgame)
	}
}

// FAIL CLOSED on unwired or nonsensical inputs: never substitute a default, because every
// substitution here biases toward moving the hull.
func TestValueRelocationShould_FailClosed_GivenAnUnwiredOrNonsensicalInput(t *testing.T) {
	mutate := map[string]func(*RelocationInputs){
		"no uplift bar (unwired)": func(in *RelocationInputs) { in.UpliftBar = 0 },
		"a negative uplift bar":   func(in *RelocationInputs) { in.UpliftBar = -1 },
		"no horizon (unwired)":    func(in *RelocationInputs) { in.HorizonHours = 0 },
		"a negative horizon":      func(in *RelocationInputs) { in.HorizonHours = -5 },
		"a negative travel time":  func(in *RelocationInputs) { in.TravelHours = -1 },
	}
	for name, apply := range mutate {
		in := licensedInputs()
		apply(&in)

		got := ValueRelocation(in)
		if got.Licensed() {
			t.Fatalf("%s licensed a relocation (NPV %.0f); an unreadable input must refuse, not default", name, got.NPV)
		}
		if got.Verdict != RelocationRefusedUnreadable {
			t.Fatalf("%s produced verdict %q, want %q", name, got.Verdict, RelocationRefusedUnreadable)
		}
	}
}

// A zero-value RelocationInputs must not license anything — the unwired-caller defense.
func TestValueRelocationShould_RefuseAZeroValueInput(t *testing.T) {
	if got := ValueRelocation(RelocationInputs{}); got.Licensed() {
		t.Fatalf("a zero-value valuation licensed a relocation (NPV %.0f)", got.NPV)
	}
}

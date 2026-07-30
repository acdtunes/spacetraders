package trading

// relocation_npv.go — what a relocation is WORTH, and the three economic reasons it is refused
// (sp-zvywu Part 2). Pure: no clock, no repository, no ports. Every relocation the opportunity
// relocator performs is licensed by exactly one call here.
//
// THE FORMULATION, as the bead specifies it:
//
//	NPV = uplift x min(remaining_era_hours - travel_h, horizon)
//	      - current_rate x travel_h
//	      - risk_margin
//
// Read term by term:
//
//   - uplift = projected_rate - current_rate, in credits/hour: what the better ground pays over what
//     the hull earns where it stands. Multiplied by the hours it will actually get to enjoy it.
//   - min(remaining_era_hours - travel_h, horizon) is the PRODUCTIVE WINDOW: the era ends, and even a
//     long era is not worth planning against past the horizon, so the window is whichever binds
//     first. `- travel_h` is inside the min because hours spent flying are not hours spent earning.
//   - current_rate x travel_h is the OPPORTUNITY COST of the trip: the hull is already earning
//     something, and it stops for the whole flight.
//   - risk_margin is the flat haircut for being wrong. The projected rate is a pre-flight projection
//     against a snapshot; the current rate is booked money. A relocation must beat its own
//     optimism by roughly a tour's earnings before it is worth doing.

// RelocationVerdict names why a candidate relocation was or was not licensed. It exists so the
// refusal is a value the caller can log and a test can assert on, rather than a bare false.
type RelocationVerdict string

const (
	// RelocationLicensed means every economic gate cleared and the NPV beat the threshold.
	RelocationLicensed RelocationVerdict = "licensed"
	// RelocationRefusedNoUplift means the better ground is not measurably better: it fails the
	// uplift bar, or does not strictly beat staying at all.
	RelocationRefusedNoUplift RelocationVerdict = "no_uplift"
	// RelocationRefusedEndgame means the era (or the horizon) has too little time left for the move
	// to pay for itself twice over — the endgame guard.
	RelocationRefusedEndgame RelocationVerdict = "endgame"
	// RelocationRefusedBelowThreshold means the move is genuinely positive but too small to bother
	// moving a hull for.
	RelocationRefusedBelowThreshold RelocationVerdict = "below_npv_threshold"
	// RelocationRefusedUnreadable means an input the valuation needs was not readable, so no
	// valuation was attempted at all (fail closed).
	RelocationRefusedUnreadable RelocationVerdict = "unreadable"
)

// RelocationInputs is everything the valuation needs about ONE (hull, region) pair. Every field is
// an OBSERVATION the caller has already made — a rate it read, a travel time it priced through the
// TravelHopModel, a horizon it was told. The valuation invents nothing.
type RelocationInputs struct {
	// CurrentRate is the hull's transaction-based realized credits/hour where it stands now
	// (EwmaHullTourRate). Only a READABLE rate may be passed; see RelocationRefusedUnreadable.
	CurrentRate float64
	// ProjectedRate is the planner-projected credits/hour for this hull on the candidate region's
	// FRESH snapshot. A stale or unreadable region must be excluded by the caller before it gets
	// here, never passed in with an optimistic number.
	ProjectedRate float64
	// TravelHours is the wall-clock cost of getting there, priced through TravelHopModel.
	TravelHours float64
	// RemainingEraHours is how much era is left. Meaningful only when EraHorizonKnown.
	RemainingEraHours float64
	// EraHorizonKnown reports whether RemainingEraHours is a real reading. When false the valuation
	// falls back to HorizonHours as the effective window — see ProductiveWindowHours.
	EraHorizonKnown bool
	// HorizonHours caps the productive window however long the era runs: past it, a projection off
	// today's snapshot is not evidence about anything.
	HorizonHours float64
	// RiskMargin is the flat credit haircut a move must clear on top of breaking even — calibrated
	// at roughly one tour of the hull's earnings.
	RiskMargin float64
	// UpliftBar is the multiplicative bar the projected rate must clear against the current rate
	// (1.5 => the region must project at least 1.5x what the hull earns now).
	UpliftBar float64
	// NPVThreshold is the credit floor the NPV must strictly exceed to license the move.
	NPVThreshold float64
}

// RelocationValuation is the priced verdict: the NPV and the intermediate quantities that produced
// it, so a decision is auditable in a log line and assertable in a test.
type RelocationValuation struct {
	Verdict RelocationVerdict
	// NPV is the credits the relocation is worth over the productive window. Meaningful whenever the
	// uplift bar cleared; zero when the valuation refused before pricing.
	NPV float64
	// UpliftPerHour is projected minus current, in credits/hour.
	UpliftPerHour float64
	// ProductiveWindowHours is the hours of uplift the move actually gets paid for.
	ProductiveWindowHours float64
	// PaybackHours is how long the uplift takes to repay the move's whole cost (the opportunity cost
	// of the flight plus the risk margin). The endgame guard is expressed against it.
	PaybackHours float64
}

// Licensed reports whether this valuation permits the relocation.
func (v RelocationValuation) Licensed() bool { return v.Verdict == RelocationLicensed }

// ValueRelocation prices one candidate relocation and returns the verdict.
//
// The gates run CHEAPEST-AND-STRICTEST FIRST, and each is a separate refusal so the reason survives
// into the log:
//
//  1. READABILITY. A non-positive uplift bar or horizon, or a negative travel time, is an unwired or
//     nonsensical input — refuse rather than substitute a default, because every substitution here
//     biases toward moving the hull.
//  2. THE UPLIFT BAR. The region must STRICTLY beat staying, and clear the multiplicative bar. When
//     the hull's current rate is non-positive the multiplicative bar is degenerate (1.5x a negative
//     number is more negative, i.e. trivially cleared), so it falls back to requiring the region to
//     project genuinely POSITIVE earnings — a money-loser relocates only to ground that actually
//     earns, never to another loss. This mirrors rateFloorImprovementClears, which solved the same
//     degeneracy for the inverted trigger.
//  3. THE ENDGAME GUARD. Refuse when the remaining era cannot cover TWICE the travel plus the
//     payback. Twice, not once, because the hull has to be able to get there AND the fleet has to be
//     able to bring it back or re-place it afterwards without the move having been a net loss; the
//     doubling is what ties this to the end-of-era freeze. Payback is the move's whole cost divided
//     by the uplift that repays it.
//  4. THE NPV THRESHOLD. Positive is not enough; it must clear the operator's floor.
//
// UNKNOWN ERA HORIZON. When EraHorizonKnown is false the productive window falls back to
// HorizonHours — which is not a loosening, because the formula's own `min(..., horizon)` already
// caps the window there for ANY era long enough not to bind. An unknown era therefore buys at most a
// horizon's worth of uplift, never an unbounded one, and the endgame guard still bites: it is
// evaluated against the same effective window, so a horizon too short to repay the move refuses.
func ValueRelocation(in RelocationInputs) RelocationValuation {
	if in.UpliftBar <= 0 || in.HorizonHours <= 0 || in.TravelHours < 0 {
		return RelocationValuation{Verdict: RelocationRefusedUnreadable}
	}

	uplift := in.ProjectedRate - in.CurrentRate
	if !upliftBarClears(in.ProjectedRate, in.CurrentRate, in.UpliftBar) {
		return RelocationValuation{Verdict: RelocationRefusedNoUplift, UpliftPerHour: uplift}
	}

	payback := (in.CurrentRate*in.TravelHours + in.RiskMargin) / uplift
	window := in.ProductiveWindowHours()
	if in.effectiveRemainingHours() < 2*in.TravelHours+payback {
		return RelocationValuation{
			Verdict:               RelocationRefusedEndgame,
			UpliftPerHour:         uplift,
			ProductiveWindowHours: window,
			PaybackHours:          payback,
		}
	}

	npv := uplift*window - in.CurrentRate*in.TravelHours - in.RiskMargin
	valuation := RelocationValuation{
		Verdict:               RelocationLicensed,
		NPV:                   npv,
		UpliftPerHour:         uplift,
		ProductiveWindowHours: window,
		PaybackHours:          payback,
	}
	if npv <= in.NPVThreshold {
		valuation.Verdict = RelocationRefusedBelowThreshold
	}
	return valuation
}

// ProductiveWindowHours is min(remaining_era_hours - travel_h, horizon), floored at zero: the hours
// of uplift the move actually gets paid for. An unknown era horizon yields the horizon itself (see
// ValueRelocation).
func (in RelocationInputs) ProductiveWindowHours() float64 {
	window := in.HorizonHours
	if in.EraHorizonKnown {
		if untilReset := in.RemainingEraHours - in.TravelHours; untilReset < window {
			window = untilReset
		}
	}
	if window < 0 {
		return 0
	}
	return window
}

// effectiveRemainingHours is the era budget the endgame guard measures against: the real remaining
// era when it is readable, else the horizon (which is the window the valuation is priced on anyway).
func (in RelocationInputs) effectiveRemainingHours() float64 {
	if in.EraHorizonKnown {
		return in.RemainingEraHours
	}
	return in.HorizonHours
}

// upliftBarClears reports whether a candidate region is MEASURABLY better, not merely different.
// Two hard requirements, mirroring rateFloorImprovementClears:
//
//   - STRICTLY beats staying: a relocation is never to a worse-or-equal ground, whatever bar an
//     operator configures, so a misconfigured sub-1.0 bar can never cause a downgrade move.
//   - clears the multiplicative bar when the current rate is POSITIVE; when it is non-positive the
//     bar is degenerate, and the region must instead project genuinely positive earnings.
func upliftBarClears(projectedRate, currentRate, bar float64) bool {
	if projectedRate <= currentRate {
		return false
	}
	if currentRate > 0 {
		return projectedRate >= bar*currentRate
	}
	return projectedRate > 0
}

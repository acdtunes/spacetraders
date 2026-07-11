package contract

// defaultHullLoadForPrediction is the hull-load size the multi-delivery-remaining
// test falls back to when the caller cannot supply a concrete hull capacity. It is a
// standard early-hauler hold; the exact value only decides where the "one more trip
// finishes it" boundary sits, and erring toward a smaller hold keeps the signal
// conservative (it predicts only when clearly more than one load remains).
const defaultHullLoadForPrediction = 40

// SourcePrediction is the same-contract / same-good / multi-delivery-remaining
// pre-position signal (sp-1ef0). It names the good whose next delivery will
// near-certainly be sourced from the same market, with a confidence the caller
// gates against a threshold before moving an idle hull.
//
// It deliberately carries the GOOD, not a market: resolving good -> source market is
// the caller's job and must use live market availability, never the persisted
// purchase-history tracking removed in 71aceda (which biased coverage toward
// frequently-used markets over true availability).
type SourcePrediction struct {
	// Good is the trade symbol the next delivery will need.
	Good string
	// RemainingUnits is how many units of Good are still outstanding on the contract.
	RemainingUnits int
	// Confidence is the near-certainty of the prediction in [0,1]. It is 1.0 only when
	// a single good is outstanding and more than one hull-load of it remains; it drops
	// when more than one good is outstanding (ambiguous which is sourced next).
	Confidence float64
	// HasPrediction is true when there is a candidate good worth pre-positioning for
	// (i.e. more than one trip of it remains). A last-trip remainder or a completed
	// contract yields false.
	HasPrediction bool
}

// PredictNextContractSource derives the same-good/multi-delivery-remaining signal from
// a contract's live delivery progress (sp-1ef0).
//
// Rules (near-certain only, per the bead's restriction):
//   - No good outstanding (contract complete) => no prediction.
//   - Candidate = the outstanding good with the most remaining units.
//   - If the candidate's remainder fits a single hull-load, the current delivery leg
//     finishes it and there is no next delivery to pre-position for => no prediction.
//   - Otherwise the good will be sourced again; the next source is near-certainly the
//     same market. Confidence is 1.0 when the candidate is the ONLY outstanding good,
//     and 0.5 when others are outstanding too (the next source is ambiguous, so the
//     caller's threshold should reject it). No cross-contract reasoning.
//
// hullCapacity is the per-trip load used for the multi-trip test; <=0 falls back to
// defaultHullLoadForPrediction.
func PredictNextContractSource(c *Contract, hullCapacity int) SourcePrediction {
	if c == nil {
		return SourcePrediction{}
	}
	if hullCapacity <= 0 {
		hullCapacity = defaultHullLoadForPrediction
	}

	outstanding := outstandingDeliveries(c)
	if len(outstanding) == 0 {
		return SourcePrediction{}
	}

	candidate := outstanding[0]
	for _, d := range outstanding[1:] {
		if remainingUnits(d) > remainingUnits(candidate) {
			candidate = d
		}
	}

	remaining := remainingUnits(candidate)

	// Multi-delivery-remaining guard: only pre-position when clearly more than a single
	// load remains, otherwise the current trip completes the good.
	if remaining <= hullCapacity {
		return SourcePrediction{Good: candidate.TradeSymbol, RemainingUnits: remaining}
	}

	confidence := 1.0
	if len(outstanding) > 1 {
		// Ambiguity guard: more than one good outstanding — which market is next is not
		// certain, so weaken the signal below any sane threshold.
		confidence = 0.5
	}

	return SourcePrediction{
		Good:           candidate.TradeSymbol,
		RemainingUnits: remaining,
		Confidence:     confidence,
		HasPrediction:  true,
	}
}

func outstandingDeliveries(c *Contract) []Delivery {
	var out []Delivery
	for _, d := range c.Terms().Deliveries {
		if remainingUnits(d) > 0 {
			out = append(out, d)
		}
	}
	return out
}

func remainingUnits(d Delivery) int {
	r := d.UnitsRequired - d.UnitsFulfilled
	if r < 0 {
		return 0
	}
	return r
}

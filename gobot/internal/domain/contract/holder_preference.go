package contract

// The deterministic single-hull rule (sp-zve2q) hands the next sourcing run to
// any IDLE hull already carrying the contract good, so ONE hull — never two —
// sources and delivers a contract. That intent is right and is preserved here:
// it is what stops a second hull double-sourcing a duplicate load onto an empty
// hull while the first one's units sit orphaned (sp-1pf0r).
//
// Applying it UNCONDITIONALLY is the defect (sp-5jce2). A contract hull finishes
// every cycle AT THE DELIVERY DESTINATION — the point maximally far from the
// source market — and a hull whose leg ended partial is left standing there
// holding its residue. "Any hull holding any quantity wins" therefore re-selects
// the WORST-PLACED hull for the next source run, every cycle. Measured era 5,
// X1-KP23 (contract cms41jtz0, 19 ASSAULT_RIFLES -> J56, source E42):
//
//	dist to source:  TORWIND-5 39.0 | TORWIND-8 41.9 | incumbent at J56 673.3
//	incumbent round trip J56->E42->J56 ~= 1,346 units
//	near hull        A1->E42->J56      ~=   712 units
//
// ~47% of travel and fuel burned to top up a partial load, repeating every cycle.
//
// WeighHolderAgainstSource turns the rule into a WEIGHED preference. Because
// dist(source,destination) is identical for every candidate, the comparison
// reduces to dist(hull,source) — a scalar compare, never a routing solve.
//
// The load already aboard the holder is NEVER stranded and NEVER re-bought: when
// the near hull is preferred, the holder is dispatched FIRST for a deliver-held
// run that registers its units where it already stands (zero travel), which is
// why the decision REQUIRES the holder to be at the delivery destination. The
// holder then re-enters the pool empty, the short-circuit stops firing, and the
// ordinary source-nearest selection picks the well-placed hull for the remainder.
// See the coordinator's dispatch for that ordering.

const (
	// HolderNearlyCompleteFraction keeps the holder when its load already covers
	// this much of the remaining requirement. Topping up a nearly-complete load
	// is a small buy, and splitting the cycle costs an extra (if cheap) worker
	// run — not worth churning the assignment for the last quarter.
	HolderNearlyCompleteFraction = 0.75

	// HolderProximityMargin is the multiplicative margin the alternative hull
	// must beat the holder by. A strict `<` would flip the assignment on
	// near-ties and thrash it between two hulls a few units apart; requiring the
	// candidate to be at least this many times closer to the source makes the
	// preference stable pass over pass.
	HolderProximityMargin = 2.0

	// HolderMinDistanceSaving is the absolute floor, in waypoint units, on the
	// travel a split must actually save. It stops the ratio test from firing on
	// two hulls that are both effectively on top of the source (5 vs 20 units is
	// 4x closer and worth nothing).
	HolderMinDistanceSaving = 50.0
)

// HolderPlacement is the measured input to the holder-vs-source decision. All
// distances are in-system waypoint units to the SOURCE market; the caller is
// responsible for only supplying hulls that share the source's system (a
// cross-system Euclidean distance is meaningless, and such a hull could not
// reach the source anyway — RULINGS #14 home locality).
type HolderPlacement struct {
	// Holder is the idle pure-holder named by the single-hull short-circuit.
	Holder string
	// HeldUnits is the quantity of the contract good already aboard Holder.
	HeldUnits int
	// UnitsNeeded is the delivery's remaining requirement.
	UnitsNeeded int
	// HolderSourceDist is dist(Holder, source market).
	HolderSourceDist float64
	// HolderAtDestination reports whether Holder is standing at the delivery
	// waypoint, i.e. whether its held units can be registered without travel.
	HolderAtDestination bool
	// NearestHull is the spawnable candidate closest to the source, "" when
	// there is none.
	NearestHull string
	// NearestSourceDist is dist(NearestHull, source market).
	NearestSourceDist float64
}

// HolderDecision is the outcome of weighing the holder against the source-nearest
// candidate. Reason is always populated so the coordinator can log WHY, whichever
// way it went.
type HolderDecision struct {
	// DeliverHeldFirst is true when the near-source hull should take the next
	// sourcing run and the holder should first register the units it is standing
	// on. False keeps sp-zve2q's behaviour exactly: the holder takes the run.
	DeliverHeldFirst bool
	Reason           string
}

// WeighHolderAgainstSource decides whether a badly-placed partial holder should
// yield the next sourcing run to a hull dramatically closer to the source.
//
// It fails CLOSED in every ambiguous case — any missing input, any near-tie, any
// holder whose residue cannot be registered for free — because keeping the holder
// is sp-zve2q's proven behaviour and splitting is the change under test.
func WeighHolderAgainstSource(p HolderPlacement) HolderDecision {
	switch {
	case p.Holder == "":
		return HolderDecision{Reason: "no idle holder — ordinary source-nearest selection applies"}

	case p.NearestHull == "":
		return HolderDecision{Reason: "no spawnable alternative hull to compare against"}

	case p.UnitsNeeded <= 0:
		return HolderDecision{Reason: "no remaining requirement to source"}

	// The strongest keep, and the reason the rule exists: a load that already
	// covers the requirement makes NO source trip at all — its worker computes
	// zero units to purchase and delivers where it stands. Nothing can beat that.
	case p.HeldUnits >= p.UnitsNeeded:
		return HolderDecision{Reason: "holder's load already covers the remaining requirement — its run makes no source trip"}

	case float64(p.HeldUnits) >= HolderNearlyCompleteFraction*float64(p.UnitsNeeded):
		return HolderDecision{Reason: "holder's load is nearly complete — topping it up is cheaper than splitting the cycle"}

	// Never strand the residue: if the holder is not standing on the delivery, it
	// cannot register its units for free, and handing the run to another hull
	// would leave that load to be re-bought and liquidated (the sp-1pf0r
	// double-load). Keep the holder instead.
	case !p.HolderAtDestination:
		return HolderDecision{Reason: "holder is not at the delivery waypoint — its held load cannot be registered without travel, so it keeps the run rather than be stranded"}

	case p.NearestSourceDist*HolderProximityMargin > p.HolderSourceDist:
		return HolderDecision{Reason: "no candidate is decisively closer to the source — holding the assignment steady rather than thrashing on a near-tie"}

	case p.HolderSourceDist-p.NearestSourceDist < HolderMinDistanceSaving:
		return HolderDecision{Reason: "the travel a split would save is below the floor worth splitting for"}
	}

	return HolderDecision{
		DeliverHeldFirst: true,
		Reason:           "holder is far from the source with only a partial load while a candidate sits decisively closer — register the held units where it stands, then source with the near hull",
	}
}

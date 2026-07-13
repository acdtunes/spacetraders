package services

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
)

// The warehouse auto-cap optimizer (sp-5n7v) computes per-good target_units as a 0/1
// knapsack over LIVE demand × residual-buy-leg subject to Σ real hull capacity. These
// tests pin the acceptance criteria from the bead against the PURE optimizer core, whose
// inputs (capacity, per-good recurrence/payment/residual-buy-leg/size) are supplied by the
// caller — so every criterion is exercised deterministically without live infrastructure.

// incidentGoods reproduces the 2026-07-13 live incident on player-3: hubs central-cover
// EQUIPMENT and MEDICINE (residual buy-leg ~0 — a homed hauler grabs them fast), while
// DRUGS (far, J58) and ANTIMATTER (far, I56) have a large residual buy-leg (no hauler can
// source them quickly). SHIP_PARTS is mid-distance. The buggy stocker filled 80/80 with
// EQUIPMENT+MEDICINE; the optimizer must do the inverse.
func incidentGoods() []GoodDemand {
	return []GoodDemand{
		{Good: "EQUIPMENT", Recurrence: 8, Payment: 1200, ResidualBuyLeg: 0, Size: 20},
		{Good: "MEDICINE", Recurrence: 6, Payment: 1000, ResidualBuyLeg: 0, Size: 20},
		{Good: "DRUGS", Recurrence: 3, Payment: 700, ResidualBuyLeg: 6, Size: 24},
		{Good: "ANTIMATTER", Recurrence: 2, Payment: 900, ResidualBuyLeg: 8, Size: 8},
		{Good: "SHIP_PARTS", Recurrence: 2, Payment: 600, ResidualBuyLeg: 4, Size: 6},
	}
}

// selected returns the set of goods with a positive computed target.
func selected(res WarehouseCapResult) map[string]int {
	out := map[string]int{}
	for g, u := range res.Targets {
		if u > 0 {
			out[g] = u
		}
	}
	return out
}

func sumTargets(res WarehouseCapResult) int {
	total := 0
	for _, u := range res.Targets {
		total += u
	}
	return total
}

// Caps are COMPUTED from the supplied demand + capacity, not read from a static table:
// a selected good's target equals its (single-contract) size s_G, and the whole selection
// fits the capacity.
func TestComputeWarehouseCaps_CapsComputedFromDemandAndCapacity(t *testing.T) {
	in := WarehouseCapInput{
		Capacity: 80,
		Goods: []GoodDemand{
			{Good: "DRUGS", Recurrence: 3, Payment: 700, ResidualBuyLeg: 6, Size: 24},
			{Good: "ANTIMATTER", Recurrence: 2, Payment: 900, ResidualBuyLeg: 8, Size: 8},
		},
	}

	res := ComputeWarehouseCaps(in, WarehouseCapParams{})

	require.False(t, res.ColdStart, "enough demand history was supplied — not a cold start")
	assert.Equal(t, 24, res.Targets["DRUGS"], "a selected good is buffered fully at its size s_G")
	assert.Equal(t, 8, res.Targets["ANTIMATTER"])
	assert.LessOrEqual(t, sumTargets(res), in.Capacity)
}

// A hub-covered good (residual buy-leg ~0) is NOT buffered even at very high
// recurrence/payment; a far-sourced good IS buffered, capacity permitting.
func TestComputeWarehouseCaps_HubCoveredGoodNotBuffered(t *testing.T) {
	in := WarehouseCapInput{
		Capacity: 80,
		Goods: []GoodDemand{
			// Central + hub-covered: enormous recurrence×payment, but 0 residual buy-leg.
			{Good: "EQUIPMENT", Recurrence: 20, Payment: 5000, ResidualBuyLeg: 0, Size: 20},
			// Far-sourced: modest demand, large residual buy-leg.
			{Good: "DRUGS", Recurrence: 2, Payment: 300, ResidualBuyLeg: 6, Size: 24},
		},
	}

	res := ComputeWarehouseCaps(in, WarehouseCapParams{})

	assert.Zero(t, res.Targets["EQUIPMENT"], "a hub-covered good is never buffered — 0 residual buy-leg means ~0 buffer value")
	assert.Positive(t, res.Targets["DRUGS"], "a far-sourced good is buffered")
}

// Adding capacity (a 2nd warehouse or a heavy/cargo-module hull) raises C and the knapsack
// buffers MORE goods — with no code change and no assume-80.
func TestComputeWarehouseCaps_MoreCapacityBuffersMore(t *testing.T) {
	goods := []GoodDemand{
		{Good: "DRUGS", Recurrence: 3, Payment: 700, ResidualBuyLeg: 6, Size: 24},
		{Good: "ANTIMATTER", Recurrence: 2, Payment: 900, ResidualBuyLeg: 8, Size: 8},
		{Good: "SHIP_PARTS", Recurrence: 2, Payment: 600, ResidualBuyLeg: 4, Size: 6},
		{Good: "FUEL", Recurrence: 4, Payment: 400, ResidualBuyLeg: 5, Size: 40},
	}

	small := ComputeWarehouseCaps(WarehouseCapInput{Capacity: 30, Goods: goods}, WarehouseCapParams{})
	large := ComputeWarehouseCaps(WarehouseCapInput{Capacity: 120, Goods: goods}, WarehouseCapParams{})

	assert.Less(t, len(selected(small)), len(selected(large)),
		"a larger Σ hull capacity buffers strictly more goods")
	assert.LessOrEqual(t, sumTargets(small), 30)
	assert.LessOrEqual(t, sumTargets(large), 120)
}

// Every selected good is buffered FULLY at s_G (never a partial fraction of a contract),
// and the selected sizes never exceed capacity.
func TestComputeWarehouseCaps_SelectedGoodsBufferedFullyWithinCapacity(t *testing.T) {
	in := WarehouseCapInput{Capacity: 50, Goods: incidentGoods()}

	res := ComputeWarehouseCaps(in, WarehouseCapParams{})

	require.LessOrEqual(t, sumTargets(res), in.Capacity, "Σ selected s_G ≤ C")
	for _, g := range in.Goods {
		if tu := res.Targets[g.Good]; tu != 0 {
			assert.Equal(t, g.Size, tu, "%s must be buffered fully at s_G or not at all (0/1, never partial)", g.Good)
		}
	}
}

// A good whose single-contract size exceeds the whole capacity cannot be buffered fully,
// so the 0/1 rule excludes it rather than buffering a useless partial load.
func TestComputeWarehouseCaps_GoodLargerThanCapacityExcluded(t *testing.T) {
	in := WarehouseCapInput{
		Capacity: 20,
		Goods: []GoodDemand{
			{Good: "HUGE", Recurrence: 10, Payment: 9000, ResidualBuyLeg: 9, Size: 40}, // > capacity
			{Good: "DRUGS", Recurrence: 2, Payment: 300, ResidualBuyLeg: 6, Size: 12},
		},
	}

	res := ComputeWarehouseCaps(in, WarehouseCapParams{})

	assert.Zero(t, res.Targets["HUGE"], "a good that cannot fit fully is not buffered partially")
	assert.Equal(t, 12, res.Targets["DRUGS"])
}

// REGRESSION (bead acceptance): seed the incident and assert the optimizer buffers the
// FAR goods (DRUGS, ANTIMATTER) and refuses the hub-covered ones (EQUIPMENT, MEDICINE) —
// the exact inverse of the shipped bug.
func TestComputeWarehouseCaps_IncidentRegression(t *testing.T) {
	in := WarehouseCapInput{Capacity: 80, Goods: incidentGoods()}

	res := ComputeWarehouseCaps(in, WarehouseCapParams{})

	assert.Positive(t, res.Targets["DRUGS"], "DRUGS (far, J58) must be buffered")
	assert.Positive(t, res.Targets["ANTIMATTER"], "ANTIMATTER (far, I56) must be buffered")
	assert.Zero(t, res.Targets["EQUIPMENT"], "EQUIPMENT (central, hub-covered) must NOT be buffered")
	assert.Zero(t, res.Targets["MEDICINE"], "MEDICINE (central, hub-covered) must NOT be buffered")
	assert.LessOrEqual(t, sumTargets(res), in.Capacity)
}

// Stickiness: a ONE-TICK demand spike on a good that is NOT currently buffered must not
// churn the held selection. EWMA damps the spike (its smoothed value stays low) and the
// hysteresis bonus protects the incumbent — together the selection is unchanged even
// though the challenger's RAW value this tick exceeds the incumbent's.
func TestComputeWarehouseCaps_StickyOneTickSpikeDoesNotChurn(t *testing.T) {
	// Capacity fits exactly one of these size-40 goods. DRUGS is the incumbent.
	in := WarehouseCapInput{
		Capacity: 40,
		Goods: []GoodDemand{
			{Good: "DRUGS", Recurrence: 5, Payment: 2000, ResidualBuyLeg: 1, Size: 40},      // raw 10000, steady
			{Good: "EQUIPMENT", Recurrence: 5, Payment: 4000, ResidualBuyLeg: 1, Size: 40},  // raw 20000 — a one-tick spike
		},
		PriorSmoothed:  map[string]float64{"DRUGS": 10000, "EQUIPMENT": 1000}, // EQUIPMENT was quiet until now
		CurrentTargets: map[string]int{"DRUGS": 40},                          // DRUGS is held
	}

	res := ComputeWarehouseCaps(in, WarehouseCapParams{})

	assert.Equal(t, 40, res.Targets["DRUGS"], "the incumbent stays held through a one-tick spike")
	assert.Zero(t, res.Targets["EQUIPMENT"], "a one-tick spike does not churn the held stock in")
	// EWMA state advances toward the new observation (so a DURABLE shift eventually flips it).
	assert.Greater(t, res.Smoothed["EQUIPMENT"], 1000.0, "smoothed demand advances toward the spike")
	assert.Less(t, res.Smoothed["EQUIPMENT"], 20000.0, "but is not the raw spike — it is smoothed")
}

// A sustained (durable) shift DOES eventually flip the selection — hysteresis is a
// dead-band, not a latch.
func TestComputeWarehouseCaps_DurableShiftFlipsSelection(t *testing.T) {
	// EQUIPMENT has been high for a while (its prior smoothed value already dominates).
	in := WarehouseCapInput{
		Capacity: 40,
		Goods: []GoodDemand{
			{Good: "DRUGS", Recurrence: 5, Payment: 2000, ResidualBuyLeg: 1, Size: 40},
			{Good: "EQUIPMENT", Recurrence: 5, Payment: 6000, ResidualBuyLeg: 1, Size: 40},
		},
		PriorSmoothed:  map[string]float64{"DRUGS": 10000, "EQUIPMENT": 28000},
		CurrentTargets: map[string]int{"DRUGS": 40},
	}

	res := ComputeWarehouseCaps(in, WarehouseCapParams{})

	assert.Equal(t, 40, res.Targets["EQUIPMENT"], "a durable dominance flips the selection despite hysteresis")
	assert.Zero(t, res.Targets["DRUGS"])
}

// Cold-start fallback: with too little demand history the optimizer returns the static
// fallback caps, clipped (bin-packed in priority order) to the REAL capacity — never
// assume-80.
func TestComputeWarehouseCaps_ColdStartFallbackClippedToCapacity(t *testing.T) {
	// Only one thin observation → below the cold-start threshold.
	in := WarehouseCapInput{
		Capacity: 78,
		Goods:    []GoodDemand{{Good: "DRUGS", Recurrence: 1, Payment: 100, ResidualBuyLeg: 3, Size: 24}},
	}

	res := ComputeWarehouseCaps(in, WarehouseCapParams{})

	require.True(t, res.ColdStart, "thin history triggers the cold-start fallback")
	// Default fallback: DRUGS 24 / MEDICINE 20 / EQUIPMENT 20 / ANTIMATTER 8 / SHIP_PARTS 6 (Σ78).
	assert.Equal(t, 24, res.Targets["DRUGS"])
	assert.Equal(t, 20, res.Targets["MEDICINE"])
	assert.Equal(t, 20, res.Targets["EQUIPMENT"])
	assert.Equal(t, 8, res.Targets["ANTIMATTER"])
	assert.Equal(t, 6, res.Targets["SHIP_PARTS"])
	assert.LessOrEqual(t, sumTargets(res), in.Capacity)
}

// Cold-start fallback bin-packs in priority order and drops goods that no longer fit the
// real capacity (a small hull), never overflowing C.
func TestComputeWarehouseCaps_ColdStartFallbackDropsOverflowGoods(t *testing.T) {
	in := WarehouseCapInput{
		Capacity: 50, // only fits DRUGS(24)+MEDICINE(20) = 44; EQUIPMENT(20) overflows
		Goods:    nil, // no history at all → cold start
	}

	res := ComputeWarehouseCaps(in, WarehouseCapParams{})

	require.True(t, res.ColdStart)
	assert.Equal(t, 24, res.Targets["DRUGS"])
	assert.Equal(t, 20, res.Targets["MEDICINE"])
	assert.Zero(t, res.Targets["EQUIPMENT"], "the fallback bin-packs in priority order and drops what overflows C")
	assert.LessOrEqual(t, sumTargets(res), in.Capacity)
}

// On a cold reload (no persisted EWMA state) the smoothed signal seeds from the raw
// observation — so smoothing is re-derivable from the persisted inputs (RULINGS #2) and
// starts unbiased rather than snapping to zero.
func TestComputeWarehouseCaps_SmoothingSeedsFromRawWhenNoPrior(t *testing.T) {
	in := WarehouseCapInput{
		Capacity:      80,
		Goods:         []GoodDemand{{Good: "DRUGS", Recurrence: 3, Payment: 700, ResidualBuyLeg: 6, Size: 24}},
		PriorSmoothed: nil, // cold reload
	}

	res := ComputeWarehouseCaps(in, WarehouseCapParams{})

	assert.InDelta(t, float64(3*700), res.Smoothed["DRUGS"], 0.001,
		"with no prior state the smoothed value seeds from the raw recurrence×payment")
}

// Analyst-owned weights are honoured (RULINGS #5): raising the residual-buy-leg weight
// amplifies the coverage term enough to flip a borderline selection. A (higher raw demand,
// lower coverage) beats B at the default weight; dialling the coverage weight up flips it
// to B — proving the blend weight actually feeds the value formula.
func TestComputeWarehouseCaps_ResidualLegWeightShiftsSelection(t *testing.T) {
	in := WarehouseCapInput{
		Capacity: 24, // fits exactly one size-24 good
		Goods: []GoodDemand{
			{Good: "A", Recurrence: 4, Payment: 1000, ResidualBuyLeg: 2, Size: 24}, // raw 4000, coverage 2
			{Good: "B", Recurrence: 2, Payment: 1000, ResidualBuyLeg: 3, Size: 24}, // raw 2000, coverage 3
		},
	}

	// Default coverage weight (1.0): A(4000×2=8000) beats B(2000×3=6000).
	def := ComputeWarehouseCaps(in, WarehouseCapParams{})
	assert.Positive(t, def.Targets["A"])
	assert.Zero(t, def.Targets["B"])

	// Heavier coverage weight (3.0): A(4000×2³=32000) < B(2000×3³=54000) → B wins.
	heavy := ComputeWarehouseCaps(in, WarehouseCapParams{WeightResidualLeg: 3})
	assert.Positive(t, heavy.Targets["B"], "a heavier coverage weight amplifies residual buy-leg and flips the pick")
	assert.Zero(t, heavy.Targets["A"])
}

// ---- PlanWarehouseCaps: the composer over live demand candidates (the shared model) ----

// candidate builds a mined demand candidate for the composer tests.
func candidate(good, foreignSystem string, contractCount, maxContractUnits, homeAsk int) persistence.DemandCandidate {
	return persistence.DemandCandidate{
		Good:             good,
		ContractCount:    contractCount,
		DemandUnits:      maxContractUnits * contractCount,
		MaxContractUnits: maxContractUnits,
		ForeignSystem:    foreignSystem,
		HomeAsk:          homeAsk,
		HomeAskKnown:     true,
	}
}

// PlanWarehouseCaps buffers the FAR (cross-system) contract goods the single-system worker
// cannot source (RULING #14), each at its single-contract size s_G, within Σ hull capacity —
// the concrete fix for the incident's starvation of DRUGS/ANTIMATTER. (The composer's coarse
// location residual keeps in-system goods buffered too — sp-layd — but the per-good caps stop
// any one good from monopolising the hull; a later sp-q2zq coverage upgrade drops genuinely
// hub-covered goods to ~0 residual.)
func TestPlanWarehouseCaps_BuffersFarGoodsAtSingleContractSize(t *testing.T) {
	home := "X1-VB74"
	candidates := []persistence.DemandCandidate{
		candidate("DRUGS", "X1-J58", 3, 24, 700),     // cross-system → far
		candidate("ANTIMATTER", "X1-I56", 2, 8, 900),  // cross-system → far
	}

	res := PlanWarehouseCaps(candidates, 80, home, "", nil, nil, nil, WarehouseCapParams{})

	assert.Equal(t, 24, res.Targets["DRUGS"], "DRUGS (far) buffered at its single-contract size s_G")
	assert.Equal(t, 8, res.Targets["ANTIMATTER"], "ANTIMATTER (far) buffered at s_G")
	assert.LessOrEqual(t, sumTargets(res), 80)
}

// At EQUAL demand, a CROSS-system good out-ranks an IN-system one for a contested buffer slot:
// the residual buy-leg is higher for the good the single-system worker cannot chase. This is
// the coverage signal that steers scarce capacity toward the far/orphan goods.
func TestPlanWarehouseCaps_CrossSystemPreferredAtEqualDemand(t *testing.T) {
	home := "X1-VB74"
	candidates := []persistence.DemandCandidate{
		candidate("NEAR", home, 4, 40, 1000),      // in-system source
		candidate("FAR", "X1-Z99", 4, 40, 1000),   // cross-system source, identical demand/size
	}

	// Capacity fits exactly one size-40 good.
	res := PlanWarehouseCaps(candidates, 40, home, "", nil, nil, nil, WarehouseCapParams{})

	assert.Equal(t, 40, res.Targets["FAR"], "the cross-system good wins the contested slot")
	assert.Zero(t, res.Targets["NEAR"], "the in-system good yields when capacity is scarce")
}

// The composer sizes s_G from MaxContractUnits (the single-contract size), NOT the summed
// DemandUnits — so a good whose TOTAL demand dwarfs the hull is still buffered at one
// contract's worth.
func TestPlanWarehouseCaps_SizesFromSingleContractNotSummedDemand(t *testing.T) {
	home := "X1-VB74"
	// Summed demand 764 (way past an 80 hull), but a single contract is only 30.
	c := candidate("DRUGS", "X1-J58", 10, 30, 700)
	c.DemandUnits = 764

	res := PlanWarehouseCaps([]persistence.DemandCandidate{c}, 80, home, "", nil, nil, nil, WarehouseCapParams{})

	assert.Equal(t, 30, res.Targets["DRUGS"], "buffered at one contract's size (30), not the summed 764")
}

// With no candidates the composer falls back to the static cold-start caps clipped to the
// real capacity (never assume-80) — the same fallback the pure optimizer uses.
func TestPlanWarehouseCaps_NoCandidatesColdStart(t *testing.T) {
	res := PlanWarehouseCaps(nil, 78, "X1-VB74", "", nil, nil, nil, WarehouseCapParams{})

	require.True(t, res.ColdStart)
	assert.Equal(t, 24, res.Targets["DRUGS"])
	assert.LessOrEqual(t, sumTargets(res), 78)
}

// ---- sp-9274: distance-aware residual buy-leg (dist(warehouse, source) replaces the binary) ----

// coordsFrom builds a WaypointCoordsLookup from a static position map; an absent waypoint
// resolves ok=false, exercising the fail-open path (RULINGS #1). A cache-only stand-in for the
// waypoint repository the live callers wire in.
func coordsFrom(positions map[string][2]float64) WaypointCoordsLookup {
	return func(waypoint string) (float64, float64, bool) {
		if p, ok := positions[waypoint]; ok {
			return p[0], p[1], true
		}
		return 0, 0, false
	}
}

// The whole point of sp-9274: at IDENTICAL demand/size, a FAR in-system good out-ranks a CLOSE
// in-system one for a contested buffer slot — because dist(warehouse, source) now drives the
// residual buy-leg. The pre-fix binary proxy scored both in-system goods identically and could
// not tell them apart; this is the discriminating signal the incident lacked.
func TestPlanWarehouseCaps_FarInSystemGoodOutranksCloseInSystemGood(t *testing.T) {
	home := "X1-VB74"
	wh := "X1-VB74-A1"
	coords := coordsFrom(map[string][2]float64{
		wh:            {0, 0},   // warehouse + the CLOSE source are co-located
		"X1-VB74-J58": {400, 0}, // the FAR source, a long intra-system haul away
	})

	closeGood := candidate("CLOSE", home, 4, 40, 1000) // in-system (ForeignSystem == home)
	closeGood.ForeignMarket = wh                        // sourced AT the warehouse — nothing to pre-stage
	farGood := candidate("FAR", home, 4, 40, 1000)      // identical demand/size
	farGood.ForeignMarket = "X1-VB74-J58"               // sourced far away — the haul the buffer compresses

	// Capacity fits exactly one size-40 good, forcing the contest.
	res := PlanWarehouseCaps([]persistence.DemandCandidate{closeGood, farGood}, 40, home, wh, coords, nil, nil, WarehouseCapParams{})

	assert.Equal(t, 40, res.Targets["FAR"], "the FAR in-system good wins the contested slot — its dist-based residual is higher")
	assert.Zero(t, res.Targets["CLOSE"], "the CLOSE in-system good yields — a homed hauler reaches its source cheaply")
}

// The DRUGS@J58 incident, reproduced: a LOW-recurrence FAR good must beat a HIGHER-recurrence
// CLOSE good once the far haul dominates. With the binary proxy DRUGS (recurrence 3) always lost
// its slot to a central good (recurrence 8) and was market-bought at J58 every time; the distance
// premium now flips it, so the buffer holds DRUGS and contracts WITHDRAW it.
func TestPlanWarehouseCaps_LowRecurrenceFarGoodBeatsHighRecurrenceCloseGood(t *testing.T) {
	home := "X1-VB74"
	wh := "X1-VB74-A1"
	coords := coordsFrom(map[string][2]float64{
		wh:            {0, 0},
		"X1-VB74-J58": {400, 0},
	})

	central := candidate("CLOTHING", home, 8, 40, 700) // high recurrence, sourced at the hub
	central.ForeignMarket = wh
	drugs := candidate("DRUGS", home, 3, 40, 700) // low recurrence, sourced far (J58)
	drugs.ForeignMarket = "X1-VB74-J58"

	res := PlanWarehouseCaps([]persistence.DemandCandidate{central, drugs}, 40, home, wh, coords, nil, nil, WarehouseCapParams{})

	assert.Equal(t, 40, res.Targets["DRUGS"], "the far, low-recurrence good is now buffered — the far haul dominates recurrence")
	assert.Zero(t, res.Targets["CLOTHING"], "the close, high-recurrence good yields the scarce slot")
}

// REGRESSION GUARD (pairs with the test above): a nil coords lookup FAILS OPEN to the coarse
// binary proxy — byte-identical to the pre-sp-9274 behavior. With no distance signal the far
// DRUGS reverts to losing its slot to the higher-recurrence central good (the exact live bug),
// proving the ONLY thing that flipped the selection above is the distance residual.
func TestPlanWarehouseCaps_NilCoordsFailsOpenToBinaryProxy(t *testing.T) {
	home := "X1-VB74"

	central := candidate("CLOTHING", home, 8, 40, 700)
	central.ForeignMarket = "X1-VB74-A1"
	drugs := candidate("DRUGS", home, 3, 40, 700)
	drugs.ForeignMarket = "X1-VB74-J58"

	// nil coords + empty warehouse waypoint → every in-system good gets the coarse InSystemResidual.
	res := PlanWarehouseCaps([]persistence.DemandCandidate{central, drugs}, 40, home, "", nil, nil, nil, WarehouseCapParams{})

	assert.Equal(t, 40, res.Targets["CLOTHING"], "without coords the residual is binary — recurrence alone decides, as before the fix")
	assert.Zero(t, res.Targets["DRUGS"], "the far good is invisible to the binary proxy — the incident behavior, preserved on fail-open")
}

// RULING #14 preserved: a CROSS-system good (which the single-system worker can never chase) must
// rank at least as high as ANY in-system good — even one at the far end of the distance ramp. The
// ramp's ceiling is clamped at/below CrossSystemResidual, so at equal demand the cross-system good
// still wins the contested slot over a maximally-far in-system good.
func TestPlanWarehouseCaps_CrossSystemStillOutranksFarthestInSystem(t *testing.T) {
	home := "X1-VB74"
	wh := "X1-VB74-A1"
	coords := coordsFrom(map[string][2]float64{
		wh:            {0, 0},
		"X1-VB74-J58": {900, 0}, // well beyond saturation → the in-system residual is pinned at its ceiling
	})

	farIn := candidate("FARIN", home, 4, 40, 1000) // in-system but maximally far
	farIn.ForeignMarket = "X1-VB74-J58"
	cross := candidate("CROSS", "X1-Z99", 4, 40, 1000) // cross-system, identical demand/size
	cross.ForeignMarket = "X1-Z99-K1"

	res := PlanWarehouseCaps([]persistence.DemandCandidate{farIn, cross}, 40, home, wh, coords, nil, nil, WarehouseCapParams{})

	assert.Equal(t, 40, res.Targets["CROSS"], "cross-system still out-ranks even the farthest in-system good (RULING #14)")
	assert.Zero(t, res.Targets["FARIN"], "the far in-system good approaches but never passes the cross-system max")
}

// Determinism (RULINGS #2): identical inputs → identical targets across repeated solves, so the
// distance-aware plan is re-derivable and stable (no map-iteration nondeterminism leaking in).
func TestPlanWarehouseCaps_DistanceResidualDeterministic(t *testing.T) {
	home := "X1-VB74"
	wh := "X1-VB74-A1"
	coords := coordsFrom(map[string][2]float64{
		wh:            {0, 0},
		"X1-VB74-J58": {400, 0},
		"X1-VB74-K20": {150, 120},
	})
	build := func() []persistence.DemandCandidate {
		a := candidate("DRUGS", home, 3, 24, 700)
		a.ForeignMarket = "X1-VB74-J58"
		b := candidate("MEDICINE", home, 5, 20, 900)
		b.ForeignMarket = "X1-VB74-K20"
		c := candidate("CLOTHING", home, 8, 20, 700)
		c.ForeignMarket = wh
		return []persistence.DemandCandidate{a, b, c}
	}

	first := PlanWarehouseCaps(build(), 44, home, wh, coords, nil, nil, WarehouseCapParams{})
	for i := 0; i < 5; i++ {
		again := PlanWarehouseCaps(build(), 44, home, wh, coords, nil, nil, WarehouseCapParams{})
		assert.Equal(t, first.Targets, again.Targets, "identical inputs must yield identical targets every solve")
	}
}

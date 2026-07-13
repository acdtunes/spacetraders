package services

import (
	"math"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
)

// Warehouse auto-cap optimizer (sp-5n7v). The warehouse (contract-goods buffer) needs
// per-good target_units that are AUTO-COMPUTED from live demand and live capacity — not
// hand-set — so it buffers the goods a contract worker cannot source fast (far/orphan) and
// does NOT waste capacity on central/hub-covered goods. This is the sibling of the hub
// placement optimizer sp-q2zq: sp-q2zq allocates hauler POSITIONS, this allocates buffer
// CAPACITY, and both rank goods by the same demand × residual-buy-leg signal.
//
// The computation is a 0/1 KNAPSACK: pick the good-set maximising Σ value subject to
// Σ size ≤ capacity, where each good is buffered FULLY (at its single-contract size s_G)
// or not at all — a partial buffer under one contract's worth is useless. The value blends
// EWMA-smoothed demand (recurrence × payment) with the residual buy-leg (how far the
// cheapest source sits from a homed hauler): a central, hub-covered good has ~0 residual
// buy-leg → ~0 buffer value → correctly not buffered; a far/orphan good has a large residual
// buy-leg → high value → buffered, capacity permitting.
//
// RULINGS: #5 every tunable is a parameter (analyst-owned defaults below, never a hardcoded
// constant in the hot path); #2 the smoothing is re-derivable from the persisted demand
// inputs (a caller with no prior EWMA state seeds from the raw observation), and the caller
// persists the returned Smoothed map so stickiness survives a restart. The optimizer itself
// is a PURE function of its inputs — no clock, no I/O — so it is fully deterministic.

// GoodDemand is one contract good's live signal for the optimizer. Every field is derived
// from persisted contract history + live market/fleet state by the caller; the optimizer
// treats them as opaque numbers so it can be exercised deterministically.
type GoodDemand struct {
	Good string
	// Recurrence is how many distinct recent contracts demanded the good (never speculative,
	// RULINGS #6) — the ContractGoodDemand.ContractCount signal.
	Recurrence int
	// Payment is the per-good contract payment/value signal (higher = a good worth buffering).
	Payment float64
	// ResidualBuyLeg is coverage_G: the distance from the nearest homed hauler to the good's
	// cheapest in-system source. 0 = central/hub-covered (a hauler grabs it directly, no need
	// to pre-stage); large = far/orphan (pre-stage it, the worker cannot source it fast).
	ResidualBuyLeg float64
	// Size is s_G: the single-contract fill size. The good is buffered fully at Size or not at
	// all (0/1). A Size ≤ 0 or a Size greater than the whole capacity is unbufferable.
	Size int
}

// WarehouseCapInput is one solve's live state.
type WarehouseCapInput struct {
	// Capacity is C: the SUM of the real cargo_capacity of every RUNNING warehouse hull at the
	// waypoint (never assume-80 — a heavy frame, a cargo module, or a 2nd/3rd hull simply
	// raises C and the knapsack buffers more).
	Capacity int
	Goods    []GoodDemand
	// PriorSmoothed carries the EWMA state from the previous solve, persisted by the caller
	// (RULINGS #2). Nil/empty means a cold reload: each good's smoothed value seeds from its
	// raw observation this tick (re-derivable, unbiased).
	PriorSmoothed map[string]float64
	// CurrentTargets carries the currently-held per-good target_units, so hysteresis can keep
	// an incumbent buffered through a transient shift (never dumping bought stock on a one-tick
	// flip). Nil/empty on the first solve.
	CurrentTargets map[string]int
}

// WarehouseCapResult is one solve's decision.
type WarehouseCapResult struct {
	// Targets is target_units[G] = s_G for every SELECTED good; unselected goods are absent
	// (read as 0). The stocker holds each good ≤ its target, tops up after consumption, and
	// never over-buys past it.
	Targets map[string]int
	// Smoothed is the advanced EWMA state the caller persists for the next solve's PriorSmoothed.
	Smoothed map[string]float64
	// ColdStart is true when the static cold-start fallback was used (too little demand history).
	ColdStart bool
}

// GoodCap is one entry of the ordered cold-start fallback: buffer Good at Units if it still
// fits the (bin-packed, priority-ordered) real capacity.
type GoodCap struct {
	Good  string
	Units int
}

// WarehouseCapParams are the analyst-owned tunables (RULINGS #5). The zero value of every
// field falls back to the documented default, so callers may pass WarehouseCapParams{}.
type WarehouseCapParams struct {
	// EWMAHalfLife is the smoothing half-life in ticks: alpha = 1 − 0.5^(1/halfLife). A larger
	// half-life smooths harder (stickier). <= 0 => DefaultEWMAHalfLife.
	EWMAHalfLife float64
	// WeightRecurrence / WeightPayment / WeightResidualLeg are the exponents blending the value
	// formula: value = (recurrence^wRec × payment^wPay)-smoothed × residualBuyLeg^wLeg. An
	// exponent of 1 is linear; 0 neutralises a term. <= 0 => 1 (linear default). To neutralise a
	// term explicitly, set a tiny positive value.
	WeightRecurrence  float64
	WeightPayment     float64
	WeightResidualLeg float64
	// HysteresisMargin is the relative dead-band protecting an incumbent: a currently-held good's
	// value is boosted by (1 + margin) inside the knapsack, so a challenger must beat it by more
	// than the margin to displace it. < 0 => DefaultHysteresisMargin; exactly 0 disables the
	// dead-band (EWMA still smooths).
	HysteresisMargin float64
	// ColdStartMinContracts is the Σ-recurrence floor below which the static fallback is used
	// (not enough history to trust the computed caps). <= 0 => DefaultColdStartMinContracts.
	ColdStartMinContracts int
	// ColdStartCaps is the ordered static fallback, bin-packed into the real capacity. Nil =>
	// DefaultColdStartCaps.
	ColdStartCaps []GoodCap
	// InSystemResidual / CrossSystemResidual are the coarse self-contained residual buy-leg
	// PlanWarehouseCaps assigns by source LOCATION (RULINGS #5, analyst-owned). A CROSS-system
	// source scores CrossSystemResidual — the single-system contract worker cannot chase it
	// (RULING #14), so the buffer/trade-engine must pre-stage it. An IN-system source scores
	// the smaller InSystemResidual: it is still worth pre-staging (sp-layd — the buffer
	// compresses the in-system export→delivery haul the worker would fly), but ranks below the
	// far/orphan goods the buffer chiefly exists for. <= 0 => the documented defaults.
	//
	// This location proxy is deliberately COARSE: it cannot yet tell a truly hub-covered
	// in-system good (a homed hauler sits at its source → residual ~0, should NOT be buffered)
	// from an in-system good whose source is far from any hauler (should be buffered). The
	// sp-q2zq follow-on replaces both knobs with the shared dist(homed-hauler, source) coverage
	// service, at which point a genuinely hub-covered good scores ~0 and drops out on its own.
	InSystemResidual    float64
	CrossSystemResidual float64
}

// Default residual buy-legs by source location (analyst-owned, RULINGS #5). Cross-system is
// weighted well above in-system so the far/orphan goods win a contested buffer, while an
// in-system-sourced good still carries positive value (sp-layd) rather than being dropped.
const (
	DefaultInSystemResidual    = 1.0
	DefaultCrossSystemResidual = 4.0
)

// Analyst-owned defaults (RULINGS #5). These are the fallbacks the params substitute when a
// field is left zero — the captain/analyst overrides them per-run without touching code.
const (
	// DefaultEWMAHalfLife smooths over ~3 ticks, so a one-tick spike moves the signal only ~20%.
	DefaultEWMAHalfLife = 3.0
	// DefaultHysteresisMargin: a challenger must beat an incumbent by >15% of value to displace it.
	DefaultHysteresisMargin = 0.15
	// DefaultColdStartMinContracts: below 3 total contract observations the history is too thin
	// to trust — fall back to the static caps.
	DefaultColdStartMinContracts = 3
)

// DefaultColdStartCaps is the sp-5n7v cold-start set (Σ 78 ≤ a standard 80-cargo hull):
// DRUGS 24 / MEDICINE 20 / EQUIPMENT 20 / ANTIMATTER 8 / SHIP_PARTS 6, in priority order.
// Used only until enough demand history accrues; the live knapsack supersedes it.
func DefaultColdStartCaps() []GoodCap {
	return []GoodCap{
		{Good: "DRUGS", Units: 24},
		{Good: "MEDICINE", Units: 20},
		{Good: "EQUIPMENT", Units: 20},
		{Good: "ANTIMATTER", Units: 8},
		{Good: "SHIP_PARTS", Units: 6},
	}
}

// PlanWarehouseCaps is the live-state adapter over ComputeWarehouseCaps: it builds the
// per-good GoodDemand from mined contract-demand candidates (the shared sp-dchv Lane A
// demand model this bead consumes, sibling of sp-q2zq) and solves the knapsack over the real
// Σ hull capacity. It is where the DEMAND × RESIDUAL-BUY-LEG signal is assembled:
//
//   - Recurrence = the good's distinct recent contract count (never speculative, RULINGS #6).
//   - Payment    = the good's home ask (its market value — the per-unit "payment" magnitude
//     the buffer serves). A follow-on can thread the true contract reward.
//   - ResidualBuyLeg = CrossSystemResidual when the cheapest source is in ANOTHER system (the
//     single-system contract worker cannot chase it, RULING #14 — the buffer pre-stages it),
//     else the smaller InSystemResidual (still worth buffering per sp-layd, but ranked below
//     the far goods). This coarse location proxy fixes the incident's STARVATION — cross-system
//     DRUGS/ANTIMATTER out-rank central goods and, combined with the per-good caps, no good
//     monopolises the hull — but cannot yet zero a truly hub-covered in-system good; the sp-q2zq
//     dist(hauler, source) coverage service (follow-on) does that, and the pure optimizer
//     already honours a 0 residual by excluding the good.
//   - Size s_G = the largest single-contract size (MaxContractUnits), falling back to the
//     summed demand only when the single-contract size is unknown, so the buffer holds one
//     contract's worth — never the (often hull-dwarfing) summed demand.
//
// homeSystem is the warehouse's own system. prior/current carry the persisted EWMA + held
// targets for stickiness (RULINGS #2). A caller with no candidates gets the cold-start fallback.
func PlanWarehouseCaps(
	candidates []persistence.DemandCandidate,
	capacity int,
	homeSystem string,
	prior map[string]float64,
	current map[string]int,
	params WarehouseCapParams,
) WarehouseCapResult {
	inResidual := params.InSystemResidual
	if inResidual <= 0 {
		inResidual = DefaultInSystemResidual
	}
	crossResidual := params.CrossSystemResidual
	if crossResidual <= 0 {
		crossResidual = DefaultCrossSystemResidual
	}

	goods := make([]GoodDemand, 0, len(candidates))
	for _, c := range candidates {
		size := c.MaxContractUnits
		if size <= 0 {
			size = c.DemandUnits // fall back to summed demand only when the single-contract size is unknown
		}
		if size <= 0 {
			continue // nothing to size — cannot buffer
		}
		residual := inResidual
		if c.ForeignSystem != "" && c.ForeignSystem != homeSystem {
			residual = crossResidual
		}
		payment := float64(c.HomeAsk)
		if payment <= 0 {
			payment = float64(c.ForeignAsk) // last-resort value proxy when the home ask is unknown
		}
		goods = append(goods, GoodDemand{
			Good:           c.Good,
			Recurrence:     c.ContractCount,
			Payment:        payment,
			ResidualBuyLeg: residual,
			Size:           size,
		})
	}

	return ComputeWarehouseCaps(WarehouseCapInput{
		Capacity:       capacity,
		Goods:          goods,
		PriorSmoothed:  prior,
		CurrentTargets: current,
	}, params)
}

// ComputeWarehouseCaps solves one warehouse auto-cap allocation. It is a pure, deterministic
// function of (input, params): given the live per-good demand and the real Σ hull capacity it
// returns the per-good target_units and the advanced EWMA state. See the package comment for
// the model.
func ComputeWarehouseCaps(input WarehouseCapInput, params WarehouseCapParams) WarehouseCapResult {
	p := params.resolved()

	// Advance the EWMA state for every good first — it is returned regardless of which branch
	// (cold-start or knapsack) decides the targets, so the caller always persists fresh state.
	smoothed := advanceEWMA(input.Goods, input.PriorSmoothed, p.alpha)

	// Cold-start: too little demand history to trust computed caps → static fallback, bin-packed
	// into the REAL capacity in priority order (never assume-80).
	if coldStart(input.Goods, p.coldStartMinContracts) {
		return WarehouseCapResult{
			Targets:   binPackFallback(p.coldStartCaps, input.Capacity),
			Smoothed:  smoothed,
			ColdStart: true,
		}
	}

	// Value each good, then 0/1-knapsack the sizes into the capacity, with an incumbent bonus
	// for hysteresis.
	items := valueGoods(input.Goods, smoothed, input.CurrentTargets, p)
	targets := knapsack(items, input.Capacity)

	return WarehouseCapResult{Targets: targets, Smoothed: smoothed, ColdStart: false}
}

// resolvedParams holds params with every default substituted, so the hot path reads clean.
type resolvedParams struct {
	alpha                 float64
	wRecurrence           float64
	wPayment              float64
	wResidualLeg          float64
	hysteresisMargin      float64
	coldStartMinContracts int
	coldStartCaps         []GoodCap
}

func (params WarehouseCapParams) resolved() resolvedParams {
	halfLife := params.EWMAHalfLife
	if halfLife <= 0 {
		halfLife = DefaultEWMAHalfLife
	}
	// alpha = 1 − 0.5^(1/halfLife): the weight on the newest observation.
	alpha := 1 - math.Pow(0.5, 1/halfLife)

	margin := params.HysteresisMargin
	if margin < 0 {
		margin = DefaultHysteresisMargin
	}

	minContracts := params.ColdStartMinContracts
	if minContracts <= 0 {
		minContracts = DefaultColdStartMinContracts
	}

	caps := params.ColdStartCaps
	if caps == nil {
		caps = DefaultColdStartCaps()
	}

	return resolvedParams{
		alpha:                 alpha,
		wRecurrence:           weightOrLinear(params.WeightRecurrence),
		wPayment:              weightOrLinear(params.WeightPayment),
		wResidualLeg:          weightOrLinear(params.WeightResidualLeg),
		hysteresisMargin:      margin,
		coldStartMinContracts: minContracts,
		coldStartCaps:         caps,
	}
}

// weightOrLinear defaults a zero/negative weight to 1.0 (linear). A term is neutralised by
// setting a tiny positive exponent, keeping the zero value meaning "default".
func weightOrLinear(w float64) float64 {
	if w <= 0 {
		return 1
	}
	return w
}

// coldStart reports whether the demand history is too thin to trust computed caps: no goods,
// or fewer than minContracts total contract observations.
func coldStart(goods []GoodDemand, minContracts int) bool {
	total := 0
	for _, g := range goods {
		total += g.Recurrence
	}
	return len(goods) == 0 || total < minContracts
}

// advanceEWMA returns the new smoothed signal for every good: raw = recurrence × payment,
// smoothed = alpha·raw + (1−alpha)·prior. With no prior (cold reload) the smoothed value
// seeds from raw so it starts unbiased and re-derivable from the persisted inputs (RULINGS #2).
func advanceEWMA(goods []GoodDemand, prior map[string]float64, alpha float64) map[string]float64 {
	out := make(map[string]float64, len(goods))
	for _, g := range goods {
		raw := float64(g.Recurrence) * g.Payment
		if prev, ok := prior[g.Good]; ok {
			out[g.Good] = alpha*raw + (1-alpha)*prev
		} else {
			out[g.Good] = raw // seed from raw on a cold reload
		}
	}
	return out
}

// knapItem is one good staged for the knapsack: its size (weight) and its effective value
// (already incumbent-boosted for hysteresis).
type knapItem struct {
	good  string
	size  int
	value float64
}

// valueGoods computes each good's knapsack value: the smoothed (recurrence × payment) blend
// times the residual-buy-leg coverage, all under the analyst weights, then applies the
// hysteresis incumbent bonus. Goods that cannot be buffered fully (size ≤ 0) are dropped
// here; the >capacity case is handled in the knapsack (an item wider than C is never packable).
func valueGoods(goods []GoodDemand, smoothed map[string]float64, current map[string]int, p resolvedParams) []knapItem {
	items := make([]knapItem, 0, len(goods))
	for _, g := range goods {
		if g.Size <= 0 {
			continue
		}
		// demand^weights already folded into `smoothed` via raw = recurrence × payment; apply the
		// recurrence/payment exponents relative to that linear blend, then the coverage exponent.
		demand := math.Pow(smoothed[g.Good], effectiveDemandExponent(p))
		coverage := math.Pow(g.ResidualBuyLeg, p.wResidualLeg)
		value := demand * coverage

		// Hysteresis: an incumbent (currently held) good gets a value bonus so a challenger must
		// beat it by more than the margin to displace it — the dead-band that stops one-tick churn.
		if current[g.Good] > 0 {
			value *= 1 + p.hysteresisMargin
		}

		items = append(items, knapItem{good: g.Good, size: g.Size, value: value})
	}
	return items
}

// effectiveDemandExponent blends the recurrence and payment weights into a single exponent on
// the (recurrence × payment) product. With both weights 1 (default) it is 1 (linear); raising
// either amplifies the demand term relative to coverage.
func effectiveDemandExponent(p resolvedParams) float64 {
	return (p.wRecurrence + p.wPayment) / 2
}

// knapsack solves the 0/1 knapsack: select the item-set maximising Σ value subject to
// Σ size ≤ capacity, returning target_units[good] = size for each selected good. It is a
// standard O(n·C) dynamic program; n (contract goods) and C (hull capacity) are both small.
// Items are pre-sorted (value desc, good asc) so reconstruction is deterministic. A zero-value
// item is never selected (it only consumes capacity), so a hub-covered good (coverage 0) is
// naturally excluded.
func knapsack(items []knapItem, capacity int) map[string]int {
	targets := map[string]int{}
	if capacity <= 0 {
		return targets
	}

	// Deterministic order for a stable optimum among equal-value packings.
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].value != items[j].value {
			return items[i].value > items[j].value
		}
		return items[i].good < items[j].good
	})

	n := len(items)
	// dp[i][w] = best value using items[i:] within weight budget w.
	dp := make([][]float64, n+1)
	for i := range dp {
		dp[i] = make([]float64, capacity+1)
	}
	for i := n - 1; i >= 0; i-- {
		it := items[i]
		for w := 0; w <= capacity; w++ {
			skip := dp[i+1][w]
			take := math.Inf(-1)
			if it.size <= w && it.value > 0 {
				take = it.value + dp[i+1][w-it.size]
			}
			if take > skip {
				dp[i][w] = take
			} else {
				dp[i][w] = skip
			}
		}
	}

	// Reconstruct: at each item, take it iff taking beats skipping at the current budget.
	w := capacity
	for i := 0; i < n; i++ {
		it := items[i]
		if it.size > w || it.value <= 0 {
			continue
		}
		take := it.value + dp[i+1][w-it.size]
		if take > dp[i+1][w] {
			targets[it.good] = it.size
			w -= it.size
		}
	}
	return targets
}

// binPackFallback lays the ordered cold-start caps into the real capacity greedily: each entry
// is taken in priority order iff it still fits, so a small hull drops the low-priority overflow
// rather than exceeding C. Never assumes 80 — it respects whatever capacity is passed.
func binPackFallback(caps []GoodCap, capacity int) map[string]int {
	targets := map[string]int{}
	remaining := capacity
	for _, c := range caps {
		if c.Units <= 0 || c.Units > remaining {
			continue
		}
		targets[c.Good] = c.Units
		remaining -= c.Units
	}
	return targets
}

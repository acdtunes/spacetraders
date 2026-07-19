package commands

import (
	"fmt"
	"strings"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// The MONEY-GUARD HEART. A purchase fires ONLY when every guard passes; this is the
// fail-CLOSED inversion of vdld's fail-open kill-switch — spending is irreversible, not-buying is
// safe, so any UNREADABLE input (price, era clock, realized rate, treasury, API utilization) BLOCKS.
//
// EvaluateGuards is PURE: it judges a fully-populated PurchaseRequest and reports every guard's
// verdict plus the full arithmetic (the park-line idiom — the captain reads one line and
// knows exactly which knob to retune and to what value). The I/O that populates the request
// (reading treasury / era / price / rate) lives in the coordinator's ACT step; keeping the
// judgement pure makes every guard's refusal unit-testable in isolation.

// GuardName identifies a purchase guard for the decision log and the autosizer_blocked metric.
type GuardName string

const (
	GuardDemand         GuardName = "demand"          // there is unmet demand for the class
	GuardFleetCeiling   GuardName = "fleet_ceiling"   // per-class + absolute fleet-size ceilings
	GuardPerTickCap     GuardName = "per_tick_cap"    // hulls already bought this tick
	GuardPriceRead      GuardName = "price_read"      // the yard ask was readable (fail-closed)
	GuardPriceCeiling   GuardName = "price_ceiling"   // per-class absolute + premium-over-cheapest cap
	GuardEraPayback     GuardName = "era_payback"     // buy pays back before era reset; hard T-cutoff
	GuardRealizedRate   GuardName = "realized_rate"   // marginal $/hr clears the floor, not decaying
	GuardExplorerExempt GuardName = "explorer_exempt" // exploration-justified — REPLACES the two income guards for the explorer ONLY
	GuardTreasuryPct    GuardName = "treasury_pct"    // a single hull ≤ pct% of live treasury (analyst rule)
	GuardAPIUtil        GuardName = "api_util"        // sustained request-utilization below ceiling (fail-closed)
	GuardTreasuryFloor  GuardName = "treasury_floor"  // treasury net of the reserve floor covers price+margin
)

// GuardVerdict is one guard's outcome plus the arithmetic behind it (Detail), so the decision log
// carries the numbers the captain retunes from.
type GuardVerdict struct {
	Guard  GuardName
	Passed bool
	Detail string
}

// PurchaseRequest is a fully-resolved candidate purchase the guard stack judges. The coordinator's
// ACT step reads every field from the live ports; a *Readable=false field means that input could
// not be read, which its guard treats as fail-closed (BLOCK) — never as a pass.
type PurchaseRequest struct {
	Class    HullClass
	ShipType string

	// Demand.
	Shortfall int // unmet demand for the class (Demand − Current); must be > 0 to buy.

	// Fleet ceilings (the hard API-budget bound).
	CurrentClassCount int
	ClassCeiling      int
	CurrentTotalCount int
	TotalCeiling      int

	// Per-tick pacing.
	PurchasesThisTick int
	PerTickCap        int

	// Price (from a demand-proximal yard where preferred).
	Price         int64
	PriceReadable bool
	// CheapestKnownPrice is the cheapest known yard ask for the type (0 = unknown → premium check
	// skipped). MaxPriceClass is the per-class absolute cap (0 = none). MaxPremiumPct caps the
	// premium over CheapestKnownPrice.
	CheapestKnownPrice int64
	MaxPriceClass      int64
	MaxPremiumPct      int

	// Era-clock payback.
	HoursToEraEnd  float64
	EraReadable    bool
	EraCutoffHours float64
	PaybackSafety  float64

	// Realized-rate gate.
	MarginalRate  float64 // expected marginal realized credits/hour for the next hull.
	RateFloor     float64 // absolute $/hr floor the class must clear (fraction × fleet-avg, resolved upstream).
	RateReadable  bool
	RateDeclining bool // realized rate trending down (heavy stop-buy).
	// UnservedDemandFloor is the near-zero unserved-lane count — the heavy class's OWN
	// Shortfall — at or BELOW which a DECLINING aggregate realized-rate is treated as genuine
	// absorption saturation and STOPS the buy. ABOVE it, a declining aggregate rate is a hull-
	// CONCENTRATION artifact (the fleet piled onto a few fat lanes and compressed their realized
	// rate while profitable lanes sit UNFLOWN) — the next heavy flies a FRESH lane at fresh
	// economics, so the declining signal must NOT stop the buy. For heavies Shortfall is the
	// unserved profitable-lane count. Resolved from autosizer_declining_rate_unserved_floor
	// (default 2); the config resolver never lets it reach 0, so the stop-buy can never be silently
	// disabled (the demand guard already forces Shortfall>0).
	UnservedDemandFloor int

	// Treasury.
	LiveTreasury      int64
	TreasuryReadable  bool
	ReserveAbsolute   int64 // fed to common.EffectiveReserveFloor.
	ReservePct        int   // proportional reserve-floor percent (0 → the resolver's default).
	MarginOverFloor   int64 // credits of headroom required above the reserve floor after the buy.
	TreasuryPctPerBuy int   // analyst affordability rule: a single hull ≤ this pct% of treasury (0 = not applied).

	// API utilization (dynamic; fails CLOSED when unreadable). Holds concurrency growth
	// when sustained utilization is at/over the ceiling OR the signal cannot be read.
	APIUtilPct      float64
	APIUtilReadable bool
	APIUtilCeiling  int
}

// PurchaseDecision is the guard stack's verdict on one candidate: Approved iff every guard passed.
// BlockedBy is the first guard that failed (empty when approved); Verdicts carries every guard's
// arithmetic for the decision log.
type PurchaseDecision struct {
	Approved  bool
	BlockedBy GuardName
	Verdicts  []GuardVerdict
}

// Arithmetic renders the full per-guard arithmetic on one line (the park-line idiom).
func (d PurchaseDecision) Arithmetic() string {
	segs := make([]string, 0, len(d.Verdicts))
	for _, v := range d.Verdicts {
		mark := "ok"
		if !v.Passed {
			mark = "BLOCK"
		}
		segs = append(segs, fmt.Sprintf("%s[%s: %s]", v.Guard, mark, v.Detail))
	}
	return strings.Join(segs, " ")
}

// EvaluateGuards runs every guard against the candidate and returns the aggregate decision.
// Every guard is evaluated (they are cheap pure comparisons) so the decision log shows the FULL
// picture, not just the first blocker; Approved is true iff none blocked, and BlockedBy names the
// first that did.
func EvaluateGuards(req PurchaseRequest) PurchaseDecision {
	verdicts := []GuardVerdict{
		guardDemand(req),
		guardFleetCeiling(req),
		guardPerTickCap(req),
		guardPriceRead(req),
		guardPriceCeiling(req),
	}
	// THE EXPLORER PAYBACK EXEMPTION — the single, class-gated carve-out.
	//
	// The explorer buys REACH, not income: it warps off the gate network to chart new systems so
	// the cheap probe frontier resumes (growFrontierGraph picks up the charted cluster next cycle).
	// It therefore has NO marginal realized $/hr, so req.MarginalRate/req.RateReadable are unset —
	// which means the two realized-rate INCOME guards (era_payback: price must pay back before the
	// era reset; realized_rate: marginal $/hr clears the fleet-avg floor) would BOTH fail CLOSED and
	// the explorer could never buy. For the explorer ONLY we REPLACE that payback proof with the
	// explorer_exempt verdict. The proof is not dropped — it is replaced by three explorer-only
	// bounds ALREADY enforced above: the demand-gate (guardDemand; the provider emits demand only
	// when slice-B off-gate demand fires AND the class is armed), the HARD CAP of 1 (guardFleetCeiling
	// with ClassCeiling=1), and the price ceiling (guardPriceCeiling, MaxPriceClass ~= 819k+premium).
	//
	// The carve-out is gated to HullClassExplorer and NOTHING else: every other class still runs BOTH
	// income guards, so a non-explorer with an unprovable payback is STILL refused (regression-tested,
	// and the class-gate is mutation-verified — dropping it makes that test fail). Every OTHER guard
	// (demand, fleet ceiling, per-tick, price read+ceiling, 25%-treasury, api-util, reserve/spend)
	// applies to the explorer unchanged.
	if req.Class == HullClassExplorer {
		verdicts = append(verdicts, guardExplorerExempt(req))
	} else {
		verdicts = append(verdicts, guardEraPayback(req), guardRealizedRate(req))
	}
	verdicts = append(verdicts,
		guardTreasuryPct(req),
		guardAPIUtil(req),
		guardTreasuryFloor(req),
	)
	d := PurchaseDecision{Approved: true, Verdicts: verdicts}
	for _, v := range verdicts {
		if !v.Passed {
			d.Approved = false
			d.BlockedBy = v.Guard
			break
		}
	}
	return d
}

func guardDemand(req PurchaseRequest) GuardVerdict {
	return GuardVerdict{
		Guard:  GuardDemand,
		Passed: req.Shortfall > 0,
		Detail: fmt.Sprintf("shortfall=%d", req.Shortfall),
	}
}

func guardFleetCeiling(req PurchaseRequest) GuardVerdict {
	passed := req.CurrentClassCount < req.ClassCeiling && req.CurrentTotalCount < req.TotalCeiling
	return GuardVerdict{
		Guard:  GuardFleetCeiling,
		Passed: passed,
		Detail: fmt.Sprintf("class %d/%d, total %d/%d", req.CurrentClassCount, req.ClassCeiling, req.CurrentTotalCount, req.TotalCeiling),
	}
}

func guardPerTickCap(req PurchaseRequest) GuardVerdict {
	return GuardVerdict{
		Guard:  GuardPerTickCap,
		Passed: req.PurchasesThisTick < req.PerTickCap,
		Detail: fmt.Sprintf("bought %d/%d this tick", req.PurchasesThisTick, req.PerTickCap),
	}
}

func guardPriceRead(req PurchaseRequest) GuardVerdict {
	return GuardVerdict{
		Guard:  GuardPriceRead,
		Passed: req.PriceReadable, // fail-closed: an unreadable yard ask never buys.
		Detail: fmt.Sprintf("price=%d readable=%v", req.Price, req.PriceReadable),
	}
}

func guardPriceCeiling(req PurchaseRequest) GuardVerdict {
	// Unreadable price is caught by guardPriceRead; here treat an unreadable price as a fail so the
	// ceiling never "passes" on a zero price.
	if !req.PriceReadable {
		return GuardVerdict{Guard: GuardPriceCeiling, Passed: false, Detail: "price unreadable"}
	}
	absOK := req.MaxPriceClass <= 0 || req.Price <= req.MaxPriceClass
	premiumOK := true
	premiumDetail := "no cheapest ref"
	if req.CheapestKnownPrice > 0 {
		premiumCap := req.CheapestKnownPrice + req.CheapestKnownPrice*int64(req.MaxPremiumPct)/100
		premiumOK = req.Price <= premiumCap
		premiumDetail = fmt.Sprintf("price %d <= cheapest %d +%d%% = %d", req.Price, req.CheapestKnownPrice, req.MaxPremiumPct, premiumCap)
	}
	absDetail := "no abs cap"
	if req.MaxPriceClass > 0 {
		absDetail = fmt.Sprintf("price %d <= max %d", req.Price, req.MaxPriceClass)
	}
	return GuardVerdict{
		Guard:  GuardPriceCeiling,
		Passed: absOK && premiumOK,
		Detail: absDetail + "; " + premiumDetail,
	}
}

func guardEraPayback(req PurchaseRequest) GuardVerdict {
	// Fail-closed on an unreadable era clock or an unreadable/zero marginal rate: without both we
	// cannot prove the hull pays back before it evaporates at reset.
	if !req.EraReadable {
		return GuardVerdict{Guard: GuardEraPayback, Passed: false, Detail: "era clock unreadable"}
	}
	if !req.RateReadable || req.MarginalRate <= 0 {
		return GuardVerdict{Guard: GuardEraPayback, Passed: false, Detail: "marginal rate unreadable/zero — cannot prove payback"}
	}
	// Hard cutoff: no buys inside the last-buy window whatever the payback math says.
	if req.HoursToEraEnd <= req.EraCutoffHours {
		return GuardVerdict{
			Guard:  GuardEraPayback,
			Passed: false,
			Detail: fmt.Sprintf("%.2fh to era-end <= cutoff %.2fh (last-buy window)", req.HoursToEraEnd, req.EraCutoffHours),
		}
	}
	maxAffordable := req.MarginalRate * req.HoursToEraEnd * req.PaybackSafety
	return GuardVerdict{
		Guard:  GuardEraPayback,
		Passed: float64(req.Price) <= maxAffordable,
		Detail: fmt.Sprintf("price %d <= rate %.0f × %.2fh × safety %.2f = %.0f", req.Price, req.MarginalRate, req.HoursToEraEnd, req.PaybackSafety, maxAffordable),
	}
}

// guardExplorerExempt is the explorer's exploration-justification verdict that REPLACES the two
// realized-rate income guards (era_payback + realized_rate) — see the carve-out in EvaluateGuards.
// It ALWAYS passes: the payback proof is waived because the explorer buys REACH not income. It is
// reached ONLY for HullClassExplorer, so it can never waive an income guard for any other class.
// The detail names the three explorer-only bounds that replace the waived proof, so the decision
// log tells the captain exactly what still gates the ~819k spend.
func guardExplorerExempt(req PurchaseRequest) GuardVerdict {
	return GuardVerdict{
		Guard:  GuardExplorerExempt,
		Passed: true,
		Detail: fmt.Sprintf("exploration-justified: explorer buys REACH not income — payback proof WAIVED; replaced by demand-gate + hard cap %d + price ceiling %d (all still gating above)", req.ClassCeiling, req.MaxPriceClass),
	}
}

func guardRealizedRate(req PurchaseRequest) GuardVerdict {
	// Fail-closed on an unreadable rate: buying to a demand signal whose economics we cannot see is
	// exactly the "hidden loser" the rh2z gate exists to stop.
	if !req.RateReadable {
		return GuardVerdict{Guard: GuardRealizedRate, Passed: false, Detail: "realized rate unreadable"}
	}
	if req.RateDeclining {
		// The CONCENTRATION carve-out applies to the TRADE (heavy) pool ONLY: for a heavy,
		// req.Shortfall IS the count of profitable trade lanes that sit UNFLOWN. When that
		// count is ABOVE the floor, a DECLINING aggregate tour-rate is a hull-CONCENTRATION artifact —
		// the fleet piled onto a few fat lanes and compressed THEIR realized rate — not true absorption
		// saturation: the next heavy flies a FRESH unserved lane at fresh economics, so the decline must
		// NOT stop the buy. Every OTHER class keeps the unconditional declining stop-buy unchanged (a
		// light's Shortfall is worker slots, not lanes, and carries no lane-concentration story), so
		// this loosens NOTHING off the trade path. Either way the marginal must still clear the rate
		// floor below (never over-loosen a capital buy). The unserved-lane count is named in the detail
		// so the gate is auditable in the daemon log.
		concentration := req.Class == HullClassHeavy && req.Shortfall > req.UnservedDemandFloor
		if !concentration {
			detail := fmt.Sprintf("marginal %.0f but rate DECLINING (absorption saturating — stop-buy)", req.MarginalRate)
			if req.Class == HullClassHeavy {
				detail = fmt.Sprintf("marginal %.0f, rate DECLINING with only %d unserved lanes <= floor %d (absorption saturating — stop-buy)", req.MarginalRate, req.Shortfall, req.UnservedDemandFloor)
			}
			return GuardVerdict{Guard: GuardRealizedRate, Passed: false, Detail: detail}
		}
		return GuardVerdict{
			Guard:  GuardRealizedRate,
			Passed: req.MarginalRate >= req.RateFloor,
			Detail: fmt.Sprintf("rate DECLINING but %d unserved lanes > floor %d (concentration not saturation — next heavy flies a fresh lane); marginal %.0f >= floor %.0f", req.Shortfall, req.UnservedDemandFloor, req.MarginalRate, req.RateFloor),
		}
	}
	return GuardVerdict{
		Guard:  GuardRealizedRate,
		Passed: req.MarginalRate >= req.RateFloor,
		Detail: fmt.Sprintf("marginal %.0f >= floor %.0f", req.MarginalRate, req.RateFloor),
	}
}

func guardTreasuryPct(req PurchaseRequest) GuardVerdict {
	if req.TreasuryPctPerBuy <= 0 {
		return GuardVerdict{Guard: GuardTreasuryPct, Passed: true, Detail: "not applied for this class"}
	}
	if !req.TreasuryReadable {
		return GuardVerdict{Guard: GuardTreasuryPct, Passed: false, Detail: "treasury unreadable"}
	}
	treasuryCap := int64(req.TreasuryPctPerBuy) * req.LiveTreasury / 100
	return GuardVerdict{
		Guard:  GuardTreasuryPct,
		Passed: req.Price <= treasuryCap,
		Detail: fmt.Sprintf("price %d <= %d%% × treasury %d = %d", req.Price, req.TreasuryPctPerBuy, req.LiveTreasury, treasuryCap),
	}
}

func guardAPIUtil(req PurchaseRequest) GuardVerdict {
	// FAILS CLOSED: an unreadable utilization holds concurrency GROWTH. RULINGS #4: a guard that
	// cannot read its bound never permits the spend. Holding a buy only stops GROWTH (the autosizer
	// never sells), so failing closed cannot shrink a healthy fleet; the live reader
	// (metrics.APIBudgetTracker) makes the signal readable in the normal case, so this blocks only
	// genuine saturation or a genuinely-absent metrics surface, never wedging forever.
	if !req.APIUtilReadable {
		return GuardVerdict{Guard: GuardAPIUtil, Passed: false, Detail: "utilization unreadable — fail-CLOSED (hold growth; RULINGS #4)"}
	}
	return GuardVerdict{
		Guard:  GuardAPIUtil,
		Passed: req.APIUtilPct < float64(req.APIUtilCeiling),
		Detail: fmt.Sprintf("util %.1f%% < ceiling %d%%", req.APIUtilPct, req.APIUtilCeiling),
	}
}

func guardTreasuryFloor(req PurchaseRequest) GuardVerdict {
	// Fail-closed on an unreadable treasury: EffectiveReserveFloor's own contract forbids calling it
	// without a live balance (RULINGS #4), and a buy must never proceed on an unknown balance.
	if !req.TreasuryReadable {
		return GuardVerdict{Guard: GuardTreasuryFloor, Passed: false, Detail: "treasury unreadable"}
	}
	floor := common.EffectiveReserveFloor(req.ReserveAbsolute, req.ReservePct, req.LiveTreasury)
	spendable := req.LiveTreasury - floor
	need := req.Price + req.MarginOverFloor
	return GuardVerdict{
		Guard:  GuardTreasuryFloor,
		Passed: spendable >= need,
		Detail: fmt.Sprintf("treasury %d − floor %d = %d >= price %d + margin %d = %d", req.LiveTreasury, floor, spendable, req.Price, req.MarginOverFloor, need),
	}
}

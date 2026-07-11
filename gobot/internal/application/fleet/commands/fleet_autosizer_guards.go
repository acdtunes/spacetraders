package commands

import (
	"fmt"
	"strings"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// The MONEY-GUARD HEART (sp-1txd M2). A purchase fires ONLY when every guard passes; this is the
// fail-CLOSED inversion of vdld's fail-open kill-switch — spending is irreversible, not-buying is
// safe, so any UNREADABLE input (price, era clock, realized rate, treasury) BLOCKS. The one
// exception is the API-utilization guard, which fails OPEN with a warn: it is a dynamic rate
// protection, and the absolute + per-class fleet ceilings are the HARD API-request-budget bound,
// so an unreadable utilization must not freeze all buys forever.
//
// EvaluateGuards is PURE: it judges a fully-populated PurchaseRequest and reports every guard's
// verdict plus the full arithmetic (the iv65 park-line idiom — the captain reads one line and
// knows exactly which knob to retune and to what value). The I/O that populates the request
// (reading treasury / era / price / rate) lives in the coordinator's ACT step (M5); keeping the
// judgement pure makes every guard's refusal unit-testable in isolation.

// GuardName identifies a purchase guard for the decision log and the autosizer_blocked metric.
type GuardName string

const (
	GuardDemand        GuardName = "demand"         // there is unmet demand for the class
	GuardFleetCeiling  GuardName = "fleet_ceiling"  // per-class + absolute fleet-size ceilings
	GuardPerTickCap    GuardName = "per_tick_cap"   // hulls already bought this tick
	GuardPriceRead     GuardName = "price_read"     // the yard ask was readable (fail-closed)
	GuardPriceCeiling  GuardName = "price_ceiling"  // per-class absolute + premium-over-cheapest cap
	GuardEraPayback    GuardName = "era_payback"    // buy pays back before era reset; hard T-cutoff
	GuardRealizedRate  GuardName = "realized_rate"  // marginal $/hr clears the floor, not decaying
	GuardTreasuryPct   GuardName = "treasury_pct"   // a single hull ≤ pct% of live treasury (analyst rule)
	GuardAPIUtil       GuardName = "api_util"       // sustained request-utilization below ceiling (fail-open)
	GuardTreasuryFloor GuardName = "treasury_floor" // treasury net of the reserve floor covers price+margin
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

	// Treasury.
	LiveTreasury      int64
	TreasuryReadable  bool
	ReserveAbsolute   int64 // fed to common.EffectiveReserveFloor.
	ReservePct        int   // proportional reserve-floor percent (0 → the resolver's default).
	MarginOverFloor   int64 // credits of headroom required above the reserve floor after the buy.
	TreasuryPctPerBuy int   // analyst affordability rule: a single hull ≤ this pct% of treasury (0 = not applied).

	// API utilization (dynamic; fails OPEN when unreadable).
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

// Arithmetic renders the full per-guard arithmetic on one line (the iv65 park-line idiom).
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
		guardEraPayback(req),
		guardRealizedRate(req),
		guardTreasuryPct(req),
		guardAPIUtil(req),
		guardTreasuryFloor(req),
	}
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
		cap := req.CheapestKnownPrice + req.CheapestKnownPrice*int64(req.MaxPremiumPct)/100
		premiumOK = req.Price <= cap
		premiumDetail = fmt.Sprintf("price %d <= cheapest %d +%d%% = %d", req.Price, req.CheapestKnownPrice, req.MaxPremiumPct, cap)
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

func guardRealizedRate(req PurchaseRequest) GuardVerdict {
	// Fail-closed on an unreadable rate: buying to a demand signal whose economics we cannot see is
	// exactly the "hidden loser" the rh2z gate exists to stop.
	if !req.RateReadable {
		return GuardVerdict{Guard: GuardRealizedRate, Passed: false, Detail: "realized rate unreadable"}
	}
	if req.RateDeclining {
		return GuardVerdict{
			Guard:  GuardRealizedRate,
			Passed: false,
			Detail: fmt.Sprintf("marginal %.0f but rate DECLINING (absorption saturating — stop-buy)", req.MarginalRate),
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
	cap := int64(req.TreasuryPctPerBuy) * req.LiveTreasury / 100
	return GuardVerdict{
		Guard:  GuardTreasuryPct,
		Passed: req.Price <= cap,
		Detail: fmt.Sprintf("price %d <= %d%% × treasury %d = %d", req.Price, req.TreasuryPctPerBuy, req.LiveTreasury, cap),
	}
}

func guardAPIUtil(req PurchaseRequest) GuardVerdict {
	// FAILS OPEN: an unreadable utilization must not freeze all buys forever — the fleet ceilings
	// are the hard API-budget bound. Only a READ value at/above the ceiling blocks.
	if !req.APIUtilReadable {
		return GuardVerdict{Guard: GuardAPIUtil, Passed: true, Detail: "utilization unreadable — fail-open (ceilings are the hard bound)"}
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

package commands

import (
	"fmt"
	"strings"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// The MONEY-GUARD HEART. A purchase fires ONLY when every guard passes; this is the
// fail-CLOSED inversion of vdld's fail-open kill-switch — spending is irreversible, not-buying is
// safe, so any UNREADABLE input (price, treasury, heavy census, API utilization) BLOCKS.
//
// EvaluateGuards is PURE: it judges a fully-populated PurchaseRequest and reports every guard's
// verdict plus the full arithmetic (the park-line idiom — the captain reads one line and
// knows exactly which knob to retune and to what value). The I/O that populates the request
// (reading treasury / price / census) lives in the coordinator's ACT step; keeping the
// judgement pure makes every guard's refusal unit-testable in isolation.
//
// SIX GUARDS, ONE QUESTION EACH. The chain was twelve and asked three questions three times over
// (affordability twice, overpaying twice, payback three times). It is now one guard per question:
//
//	demand         — is there a real, SETTLED need? (shortfall + the anti-thrash streak)
//	per_tick_cap   — have we already spent this tick?
//	price          — is the ask readable, and are we overpaying? (abs cap + premium cap)
//	heavy_cap      — is capital exposure in large hulls within the operator's cap?
//	affordability  — can the treasury bear it? (pct-per-buy rule + reserve floor & margin)
//	api_util       — is there request budget to fly another hull?
//
// WHAT WAS DELETED, and why it is not a hole. era_payback required a marginal $/hr it could never
// read in production ("marginal rate unreadable/zero — cannot prove payback"), so it refused every
// buy unconditionally rather than refusing bad ones. realized_rate refused on a declining aggregate
// rate while its own detail conceded the case did not apply (hull concentration, not absorption
// saturation — the next hull flies a fresh lane). The autosizer therefore no longer forms an
// opinion on whether a hull will EARN; it judges whether the fleet can afford it and has room for
// it. Demand shortfall — which for heavies IS the unserved profitable-lane count — is the
// remaining economic input, and it must be > 0.
//
// class_ceiling IS GONE (sp-r7eiu, Admiral's order). It was a flat per-class POOL-SIZE cap —
// fleet_ceiling_{lights,heavies} — that refused a purchase purely for being the Nth hull, with no
// reference to whether the fleet could afford it or had work for it. It was the last descendant of
// the fleet-wide total ceiling that had already been deleted for the same reason: a bound that must
// be re-raised by hand every time the fleet legitimately grows is not a bound, it is a recurring
// outage. It bound at 14/15 while the operator was expanding the heavy fleet, and raising it was a
// config.yaml edit plus a daemon restart.
//
// WHAT BOUNDS EACH CLASS NOW — a pool cap was never the thing keeping spending honest:
//
//	every class — demand (no shortfall, no buy), affordability, per_tick_cap (1/tick), price, api_util
//	heavy       — heavy_cap, the fleet-wide heavy-HULL census (capital exposure), plus the
//	              3-tick anti-thrash streak on its unserved-lane shortfall
//	light       — the factory-chain rotation math ALONE (ceil(chains × rotation_slots) + vacancies).
//	              There is no light pool cap left. This is a DELIBERATE consequence of the removal,
//	              not an oversight: the operator was told, and the money guards below are what hold.
//
// NOTHING IN THE MONEY PATH MOVED with this removal. A ceiling only ever REFUSED a purchase the
// money guards had already permitted, so deleting it cannot authorise a spend they refuse
// (RULINGS #4). The removal is pinned by tests that hold every one of them firing with the ceiling
// gone.

// GuardName identifies a purchase guard for the decision log and the autosizer_blocked metric.
type GuardName string

const (
	GuardDemand        GuardName = "demand"        // unmet demand for the class AND it survived the anti-thrash streak
	GuardPerTickCap    GuardName = "per_tick_cap"  // hulls already bought this tick
	GuardPrice         GuardName = "price"         // yard ask readable (fail-closed) AND within both ceilings
	GuardHeavyCap      GuardName = "heavy_cap"     // owned HEAVY HULLS below the operator's heavy cap
	GuardAffordability GuardName = "affordability" // BOTH treasury tests: the pct-per-buy rule AND the reserve floor+margin
	GuardAPIUtil       GuardName = "api_util"      // sustained request-utilization below ceiling (fail-closed)
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

	// Demand. Shortfall is the unmet demand for the class (Demand − Current) and must be > 0.
	//
	// ShortfallStreak / ShortfallStreakMin are the ANTI-THRASH streak, folded into this guard so
	// the whole go/no-go is one line. The heavy class must show its unserved-lane shortfall for
	// StreakMin CONSECUTIVE ticks before a ~1.4M hull is bought, so a transient spike in the lane
	// ranking cannot trigger a purchase. StreakMin is 0 for classes that do not use it, which makes
	// the term a no-op.
	//
	// RULINGS #2 (re-derive each tick, hold no cross-tick state) is satisfied exactly as it was
	// before this fold — the MECHANISM is unchanged, only where its verdict is reported. The
	// counter is the coordinator's existing per-container edge-trigger bookkeeping
	// (autosizerState.heavyShortfallStreak): not config, not a cached decision, but a count of
	// CONSECUTIVE ticks, which by definition cannot be re-derived from one tick's store read. It is
	// reset the moment the shortfall clears, and every other input on this request is still read
	// fresh from the ports each pass. The guard stays PURE: it is HANDED the count, it never keeps
	// one.
	Shortfall          int
	ShortfallStreak    int
	ShortfallStreakMin int

	// There is no per-class pool count or ceiling on this request. The pool counts reach the
	// decision log through each class's ClassDemand (Demand/Current), which is where an operator
	// reads pool size.

	// The HEAVY-HULL cap — the ONLY remaining count-based bound on any class, and the reason
	// removing the pool ceiling did not leave heavies unbounded.
	//
	// HeaviesOwned is the broad, tag-INDEPENDENT heavy census (frame list primary,
	// cargo-capacity safety net): it counts heavy hulls fleet-wide whatever their
	// DedicatedFleet tag, so a heavy tagged contract or untagged still counts against
	// capital exposure. HeavyCap is the operator dial; 0 is a legitimate hold ("own no
	// heavies"), not an unset knob.
	HeaviesOwned int
	HeavyCap     int
	// HeaviesOwnedReadable reports whether the census could be read at all. false ⇒
	// fail-CLOSED (RULINGS #4): an unreadable census that silently read as 0 would say
	// "no heavies owned" and authorise buying a hull we already own.
	HeaviesOwnedReadable bool

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

	// Treasury.
	LiveTreasury      int64
	TreasuryReadable  bool
	MarginOverFloor   int64 // credits of headroom required above the reserve floor after the buy.
	TreasuryPctPerBuy int   // analyst affordability rule: a single hull ≤ this pct% of treasury (0 = not applied).
	// HeavyReserve is the derived hold-back for the NEXT heavy purchase, computed by
	// common.HeavyReserve (the ONE definition — never re-derive the arithmetic here).
	// It raises this buy's effective floor so treasury being accumulated toward a heavy
	// is not spent on something else first; without it the continuous small spender wins
	// every tick and the heavy never accumulates.
	//
	// WAIVED for HullClassHeavy: the reserve exists FOR that purchase, so charging it
	// against the buy it is saving for would demand ~2× the hull's price — the buyer
	// reserving against itself, which spec §4 names as circular.
	HeavyReserve int64

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
		guardPerTickCap(req),
		guardPrice(req),
	}
	verdicts = append(verdicts,
		guardHeavyCap(req),
		guardAffordability(req),
		guardAPIUtil(req),
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

// guardDemand answers the whole "is there a real, settled need?" question in ONE verdict: an unmet
// shortfall AND — where the class uses one — that shortfall having PERSISTED the anti-thrash streak.
//
// The streak is a demand condition, so it belongs to the demand guard's verdict. Holding the buy
// OUTSIDE the guard chain puts its reason on a separate log line ("shortfall 17 persisting 2/3
// ticks — holding for the anti-thrash streak") while the decision log prints nothing at all for
// that tick, forcing an operator to correlate two lines to learn why a heavy did not buy.
//
// NON-LOOSENING: the streak term is unchanged (streak >= min, same counter, same reset rule) and is
// now ANDed with the shortfall test rather than short-circuiting ahead of it. A tick held for the
// streak still does not buy — it says so in the decision line and meters a `demand` block, so the
// hold is visible on the autosizer_blocked series.
func guardDemand(req PurchaseRequest) GuardVerdict {
	if req.ShortfallStreakMin > 0 {
		return GuardVerdict{
			Guard:  GuardDemand,
			Passed: req.Shortfall > 0 && req.ShortfallStreak >= req.ShortfallStreakMin,
			Detail: fmt.Sprintf("shortfall=%d persisting %d/%d ticks (anti-thrash)", req.Shortfall, req.ShortfallStreak, req.ShortfallStreakMin),
		}
	}
	return GuardVerdict{
		Guard:  GuardDemand,
		Passed: req.Shortfall > 0,
		Detail: fmt.Sprintf("shortfall=%d", req.Shortfall),
	}
}

// guardHeavyCap bounds CAPITAL EXPOSURE in large hulls. It is the ONLY count-based bound left on
// any class, so it is the whole answer to "how many heavies may this fleet own".
//
// It is HEAVY-SCOPED: every other class passes untouched, because the census it reads
// (HeaviesOwned) counts heavy hulls fleet-wide and would otherwise starve the light
// worker pool for reasons that have nothing to do with it.
//
// Written >= so an over-cap fleet (a heavy acquired outside this path, or the cap
// tuned down below what is already owned) also blocks. A cap of 0 is a legitimate
// operator hold, so it correctly blocks every heavy buy rather than reading as unset.
func guardHeavyCap(req PurchaseRequest) GuardVerdict {
	if req.Class != HullClassHeavy {
		return GuardVerdict{
			Guard:  GuardHeavyCap,
			Passed: true,
			Detail: fmt.Sprintf("n/a for class %s", req.Class),
		}
	}
	if !req.HeaviesOwnedReadable {
		return GuardVerdict{
			Guard:  GuardHeavyCap,
			Passed: false,
			Detail: fmt.Sprintf("heavy census unreadable (cap %d) — fail closed", req.HeavyCap),
		}
	}
	return GuardVerdict{
		Guard:  GuardHeavyCap,
		Passed: req.HeaviesOwned < req.HeavyCap,
		Detail: fmt.Sprintf("heavies owned %d/%d (cap)", req.HeaviesOwned, req.HeavyCap),
	}
}

func guardPerTickCap(req PurchaseRequest) GuardVerdict {
	return GuardVerdict{
		Guard:  GuardPerTickCap,
		Passed: req.PurchasesThisTick < req.PerTickCap,
		Detail: fmt.Sprintf("bought %d/%d this tick", req.PurchasesThisTick, req.PerTickCap),
	}
}

// guardPrice answers the whole "are we overpaying?" question in ONE verdict: it merges the former
// price_read and price_ceiling guards, which asked it twice.
//
// STRUCTURAL MERGE, NOT A LOOSENING. It is exactly the conjunction of the two originals:
//   - price_read passed iff PriceReadable;
//   - price_ceiling ALREADY returned false when !PriceReadable (so it never "passed" on a zero
//     price), and otherwise required the absolute cap AND the premium-over-cheapest cap.
//
// So old = PriceReadable && absOK && premiumOK, which is precisely what this returns. Every price
// the pair refused, this refuses.
//
// STILL FAILS CLOSED on an unreadable ask (RULINGS #4): an unpriceable hull is never bought, and
// the verdict says so rather than reporting a vacuous 0 <= cap.
//
// The detail carries BOTH ceiling terms plus the readability, so the one bracketed term still holds
// every number an operator would retune from (max_price_<class>, max_premium_over_cheapest_pct).
func guardPrice(req PurchaseRequest) GuardVerdict {
	if !req.PriceReadable {
		return GuardVerdict{Guard: GuardPrice, Passed: false, Detail: "yard ask UNREADABLE — fail-CLOSED (never buy an unpriceable hull)"}
	}
	absOK := req.MaxPriceClass <= 0 || req.Price <= req.MaxPriceClass
	absDetail := "no abs cap"
	if req.MaxPriceClass > 0 {
		absDetail = fmt.Sprintf("price %d <= max %d", req.Price, req.MaxPriceClass)
	}
	premiumOK := true
	premiumDetail := "no cheapest ref"
	if req.CheapestKnownPrice > 0 {
		premiumCap := req.CheapestKnownPrice + req.CheapestKnownPrice*int64(req.MaxPremiumPct)/100
		premiumOK = req.Price <= premiumCap
		premiumDetail = fmt.Sprintf("price %d <= cheapest %d +%d%% = %d", req.Price, req.CheapestKnownPrice, req.MaxPremiumPct, premiumCap)
	}
	return GuardVerdict{
		Guard:  GuardPrice,
		Passed: absOK && premiumOK,
		Detail: fmt.Sprintf("price=%d readable; %s; %s", req.Price, absDetail, premiumDetail),
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

// guardAffordability answers the whole "can the fleet afford this hull?" question in ONE verdict.
// It merges the former treasury_pct and treasury_floor guards, which read the SAME live treasury
// and asked it twice.
//
// CONJUNCTIVE — every condition from BOTH originals must still hold. This is a structural merge,
// never a behavioural loosening:
//
//	treasury_pct   : pct<=0 ? pass : (TreasuryReadable && Price <= pct% × treasury)
//	treasury_floor : TreasuryReadable && (treasury − floor − heavyReserve) >= price + margin
//	merged         : TreasuryReadable && pctTerm && floorTerm
//
// The unreadable case is identical to the pair's: treasury_pct passed vacuously when the rule was
// off (pct<=0) but treasury_floor blocked regardless, so the PAIR always refused an unreadable
// treasury — and so does this. Fail-CLOSED on an unknown balance (RULINGS #4).
//
// Two separate tests pin the two terms independently (percent-only refusal, floor-only refusal),
// because a single test cannot prove a conjunctive merge kept both.
//
// The detail carries BOTH terms' arithmetic so the one bracketed term still holds every number an
// operator retunes from (heavy_treasury_pct_per_purchase, purchase_margin_over_floor) and still
// distinguishes "own reserve waived because this IS the heavy buy" from "reserve silently dropped".
func guardAffordability(req PurchaseRequest) GuardVerdict {
	// Fail-closed on an unreadable treasury: a buy must never proceed on an unknown balance
	// (RULINGS #4). Checked FIRST, exactly as the pair did — treasury_floor refused this case
	// unconditionally, so hoisting it changes nothing.
	if !req.TreasuryReadable {
		return GuardVerdict{Guard: GuardAffordability, Passed: false, Detail: "treasury UNREADABLE — fail-CLOSED"}
	}

	// TERM 1 — the analyst's single-hull percentage-of-treasury rule. 0 means the rule is not
	// applied to this class (lights are protected by the floor term alone), which is a PASS for
	// this term only; it never waives the floor term below.
	pctOK := true
	pctDetail := "pct rule n/a for this class"
	if req.TreasuryPctPerBuy > 0 {
		treasuryCap := int64(req.TreasuryPctPerBuy) * req.LiveTreasury / 100
		pctOK = req.Price <= treasuryCap
		pctDetail = fmt.Sprintf("price %d <= %d%% × treasury %d = %d", req.Price, req.TreasuryPctPerBuy, req.LiveTreasury, treasuryCap)
	}

	// TERM 2 — treasury net of the immutable reserve floor must still cover price + margin.
	// The floor is the flat, immutable common.ImmutableReserveFloor (sp-05glh scrapped the prior
	// proportional-of-treasury computation) — no config/tune seam.
	const floor = common.ImmutableReserveFloor
	// The heavy reservation raises this buy's effective floor — EXCEPT for the heavy purchase it
	// is saving for, which would otherwise have to clear roughly twice the hull's price (spec §4:
	// "deliberately not including its own reserve, which would be circular"). The waiver is
	// enforced HERE, in the pure guard, rather than left to each caller to remember to zero: a
	// caller that forgot would deadlock heavy buying silently, and this is the one place that can
	// never be bypassed.
	heavyReserve := req.HeavyReserve
	reserveNote := fmt.Sprintf(" − heavy reserve %d", heavyReserve)
	if req.Class == HullClassHeavy {
		heavyReserve = 0
		// Still NAMED in the arithmetic so the decision log distinguishes "waived because this IS
		// the heavy buy" from "reserve silently dropped".
		reserveNote = fmt.Sprintf(" (own reserve waived: %d)", req.HeavyReserve)
	}
	spendable := req.LiveTreasury - floor - heavyReserve
	need := req.Price + req.MarginOverFloor
	floorOK := spendable >= need
	floorDetail := fmt.Sprintf("treasury %d − floor %d%s = %d >= price %d + margin %d = %d", req.LiveTreasury, floor, reserveNote, spendable, req.Price, req.MarginOverFloor, need)

	return GuardVerdict{
		Guard:  GuardAffordability,
		Passed: pctOK && floorOK,
		Detail: pctDetail + "; " + floorDetail,
	}
}

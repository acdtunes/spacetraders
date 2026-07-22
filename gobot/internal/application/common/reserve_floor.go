package common

import "context"

// ImmutableReserveFloor is the non-tunable lower bound of the working-capital spend floor
// (RULINGS #5): no configured absolute, and no proportional treasury-percent resolution,
// may drop the ENFORCED floor below this. It mirrors the identically-valued
// defaultWorkingCapitalReserve consts in the tour and factory engines (both 50000) — the
// shared resolver owns the clamp here so the two engines can never disagree on the bound.
const ImmutableReserveFloor = 50000

// ContractReserveCushion is the contract OPERATING-capital floor (150_000): the working
// capital a light-hauler contract op holds so several concurrent contract cycles stay
// funded through a treasury dip. It is the immutable base (ImmutableReserveFloor) plus a
// 100_000 contract-operating headroom — ONE base, a derived tier (RULINGS #5), so the base
// and every cushion move together and can never drift. Gates the bootstrap first-hauler +
// gate-worker buys (run_bootstrap_*). IMMUTABLE: const-only, no config/tune/launch seam.
const ContractReserveCushion = ImmutableReserveFloor + 100_000

// ContractScalerCushion is the contract SCALER's CAPEX floor (200_000) and the SOLE money
// guard on a scaler hull/depot buy (RULINGS #6 amendment). It reserves MORE than the
// contract OPERATING cushion (ContractReserveCushion) because a scaler buy is LUMPY capital
// expenditure — a whole hull or depot role at once — not a per-cycle operating spend: the
// extra 50_000 over the operating cushion keeps the contract op funded THROUGH that capex
// draw rather than letting a single lumpy buy strand ongoing delivery cycles. Immutable base
// (ImmutableReserveFloor) plus 150_000 scaler-capex headroom — the SAME base, a higher
// derived tier. IMMUTABLE: const-only, no config/tune/launch seam.
const ContractScalerCushion = ImmutableReserveFloor + 150_000

// DefaultReserveTreasuryPct is the working-capital reserve's proportional floor as a
// percent of LIVE treasury, applied when the per-engine
// working_capital_reserve_treasury_pct config key is 0/absent. 40% is the
// deadlock-proof default PENDING the trade-analyst's economics ruling; it is only a
// fallback — the operating value is the config key, never a call-site constant (RULINGS
// #5), so the ruled number lands at deploy with no rebuild.
const DefaultReserveTreasuryPct = 40

// EffectiveReserveFloor resolves the working-capital floor actually enforced at a buy:
//
//	max(ImmutableReserveFloor, min(absolute, round(pct% × liveTreasury)))
//
// The proportional term (pct% of LIVE treasury) is what makes a floor ABOVE the treasury
// impossible: an absolute-only floor can leave every buy infeasible below it (allowance =
// balance − floor < 0 → skip), stalling the fleet exactly when it needs to trade its way
// out. Semantics:
//   - absolute is the per-run configured working_capital_reserve (already ≥ 50k in both
//     engines after their own resolution); at high treasury it binds (min picks it), so
//     nothing changes above ≈ absolute ÷ (pct/100) of treasury — e.g. with 1M/40% the
//     absolute binds at ≥ 2.5M.
//   - below that the proportional floor binds and keeps a (1 − pct/100) slice of treasury
//     spendable, so the fleet trades its way back up (counter-cyclical by design).
//   - the outer max keeps the immutable 50k bound whatever pct and treasury are.
//
// pct is an integer percent (the capital_ceiling_pct convention); pct ≤ 0 resolves to
// DefaultReserveTreasuryPct so the spec's "0/absent → 40%" holds wherever the helper is
// reached.
//
// FAIL-CLOSED CONTRACT (RULINGS #4): this computes the LOWERED proportional floor and so
// REQUIRES a readable liveTreasury. A caller that cannot read the live balance must NOT
// call this — it enforces the conservative ABSOLUTE floor directly (never the
// proportional, never zero). The helper is never handed an unreadable treasury.
func EffectiveReserveFloor(absolute int64, pct int, liveTreasury int64) int64 {
	if pct <= 0 {
		pct = DefaultReserveTreasuryPct
	}
	// round(pct/100 × liveTreasury) in integer credits. pct is ≈≤ 100 and liveTreasury is
	// a credit balance, so pct×liveTreasury stays far inside int64 (100 × 10^12 = 10^14).
	proportional := (int64(pct)*liveTreasury + 50) / 100
	floor := absolute
	if proportional < floor {
		floor = proportional
	}
	if floor < ImmutableReserveFloor {
		floor = ImmutableReserveFloor
	}
	return floor
}

// reserveTreasuryPctCtxKey carries the per-run working_capital_reserve_treasury_pct from a
// coordinator down to the buy-time money guard. It is stamped only by the production
// coordinators (tour, factory), never by the trade-route circuit or by tests that build
// commands directly — so the proportional floor is ADDITIVE: a guard whose ctx carries no
// pct enforces the absolute floor unchanged, and only a stamped pct engages the
// counter-cyclical resolution.
type reserveTreasuryPctCtxKey struct{}

// WithReserveTreasuryPct stamps the per-run treasury-percent onto ctx. The
// production tour and factory coordinators call this from their command's resolved pct
// (config default 40); the value rides ctx because the buy-time guards are reached through
// singletons shared across concurrent hulls/factories, where a struct field would be a
// data race between siblings running different runs.
func WithReserveTreasuryPct(ctx context.Context, pct int) context.Context {
	return context.WithValue(ctx, reserveTreasuryPctCtxKey{}, pct)
}

// ReserveTreasuryPctFromContext reports the stamped treasury-percent and whether one was
// stamped at all. ok=false means no coordinator stamped a pct (the trade-route circuit, or
// a direct test): the caller enforces the ABSOLUTE floor, unchanged. ok=true engages the
// proportional resolution via EffectiveReserveFloor.
func ReserveTreasuryPctFromContext(ctx context.Context) (int, bool) {
	pct, ok := ctx.Value(reserveTreasuryPctCtxKey{}).(int)
	return pct, ok
}

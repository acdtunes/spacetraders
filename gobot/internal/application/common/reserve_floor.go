package common

// ImmutableReserveFloor is the non-tunable working-capital spend floor (RULINGS #5): flat
// everywhere, no proportional-of-treasury computation, and no configured override may drop the
// ENFORCED floor below it. It binds every NON-contract spender — trade, factory, construction,
// arb, stocker, outfitting, probe/bootstrap capex — directly or through a derived tier below.
// A CONTRACT source-buy is EXEMPT and answers to ContractSolvencyReserve; nothing else is.
const ImmutableReserveFloor = 50000

// ContractSolvencyReserve is the ONLY money floor a contract source-buy answers to. Contract
// work is exempt from the floor above because a source-buy IS the fleet's earning path, and
// blocking it starves the cash flow that floor exists to protect. Deliberately NOT a derived
// tier: it is measured in FUEL, withholding a hull's worst-case out-and-back, so the fleet may
// spend its working capital on a contract but never its own mobility — a fleet that cannot
// refuel is bricked with no recovery path, which is worse than any parked buy. Test-bound at
// both ends: the mobility it must cover, and the distance below the floor above it must keep.
const ContractSolvencyReserve = 12_000

// NonContractWorkingCapitalFloor is the DEFAULT working-capital spend floor for the NON-CONTRACT
// buy engines — construction gate-fill (factory input buys + fabricated-output harvests) and the
// trading coordinators (tour / trade-route / arb / stocker). Admiral 2026-07-24: "We can't go
// down 150K! Or else we risk contracts stalling" — margin-blind gate-fill buys had drained the
// treasury until the contract engine, the sole earner, could no longer source. ONE base, a
// derived tier (RULINGS #5): the base and this band move together and can never drift. An
// explicitly configured launch-config reserve still wins; only the absent-config DEFAULT
// resolves here.
const NonContractWorkingCapitalFloor = ImmutableReserveFloor + 100_000

// GateFillWorkingCapitalFloor is the gate fill's own floor, above the non-contract tier so a
// margin-blind fill cannot spend the treasury down to where trade has nothing to deploy.
const GateFillWorkingCapitalFloor = ImmutableReserveFloor + 350_000

// ContractReserveCushion is the contract OPERATING-capital floor: the working capital a
// light-hauler contract op holds so several concurrent contract cycles stay funded through a
// treasury dip. Immutable base plus a contract-operating headroom — ONE base, a derived tier
// (RULINGS #5), so base and cushion can never drift. Gates the bootstrap first-hauler +
// gate-worker buys. IMMUTABLE: const-only, no config/tune/launch seam.
const ContractReserveCushion = ImmutableReserveFloor + 100_000

// ContractScalerCushion is the contract SCALER's CAPEX floor and the SOLE money guard on a
// scaler hull/depot buy (RULINGS #6 amendment). It reserves MORE than the operating cushion
// because a scaler buy is LUMPY capital expenditure — a whole hull or depot role at once, not a
// per-cycle operating spend — so the extra headroom keeps the contract op funded THROUGH that
// draw rather than letting one lumpy buy strand ongoing delivery cycles. The SAME base, a
// higher derived tier. IMMUTABLE: const-only, no config/tune/launch seam.
const ContractScalerCushion = ImmutableReserveFloor + 150_000

// EffectiveReserveFloor is a FLAT SHIM: it ignores every argument and always returns
// ImmutableReserveFloor. No caller in this codebase invokes it; it is kept solely so a
// concurrent, not-yet-merged lane's call sites keep resolving without churn at rebase. Do not
// add new callers — reference ImmutableReserveFloor directly instead.
func EffectiveReserveFloor(absolute int64, pct int, liveTreasury int64) int64 {
	return ImmutableReserveFloor
}

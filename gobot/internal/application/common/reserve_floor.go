package common

// ImmutableReserveFloor is the non-tunable working-capital spend floor (RULINGS #5): flat
// everywhere, no proportional-of-treasury computation, no configured override may drop the
// ENFORCED floor below this (sp-05glh scrapped the 40%-of-treasury proportional rule — the
// prior counter-cyclical shrink-toward-50k-as-treasury-falls resolver, EffectiveReserveFloor,
// is gone; every former caller now uses this constant directly). It mirrors the
// identically-valued defaultWorkingCapitalReserve consts in the tour and factory engines
// (both 50000) so every engine agrees on the one bound.
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

// EffectiveReserveFloor is a FLAT SHIM (sp-05glh): the counter-cyclical proportional-of-
// treasury computation it used to perform (max(ImmutableReserveFloor, min(absolute, pct% ×
// liveTreasury))) is gone — it now always returns the flat ImmutableReserveFloor, ignoring
// every argument. No caller in THIS codebase invokes it any more (every known caller was
// migrated to reference ImmutableReserveFloor directly); the shim is kept solely so a
// concurrent, not-yet-merged lane's call sites (contract source-buy / idle-arb gates) keep
// resolving without call-site churn at rebase. Do not add new callers — reference
// ImmutableReserveFloor directly instead.
func EffectiveReserveFloor(absolute int64, pct int, liveTreasury int64) int64 {
	return ImmutableReserveFloor
}

// Package commands holds the fleet capacity autosizer (sp-1txd): a standing per-player
// coordinator that SIZES the hull pool to demand and AUTO-BUYS hulls behind the full
// money-guard stack. It is the buy-side twin of the vdld siting coordinator (which sizes the
// factory-chain portfolio at zero cost); this one spends real credits, so its guard stack is
// fail-CLOSED (any unreadable input ⇒ no buy) where vdld's kill-switch is fail-open.
//
// Shape mirrors run_siting_coordinator.go: a registered singleton handler with optional
// setter-collaborators, one infinite reconcile loop in Handle(), resolveFleetAutosizerConfig()
// resolving every <=0 knob to a documented protective default (RULINGS #5). One coordinator,
// N pluggable demand providers (lights, heavies, warehouse) — the vdld pluggable-provider idiom.
package commands

// HullClass identifies an autosized hull pool. Each class has its own demand provider, fleet
// ceiling, price ceiling, realized-rate gate, purchased ship type, and dedicated-fleet name.
type HullClass string

const (
	// HullClassLight is the factory-worker pool (HAULER role), sized to factory-chain demand.
	HullClassLight HullClass = "light"
	// HullClassHeavy is the trade-tour pool (DedicatedFleet "trade"), sized to trade demand.
	HullClassHeavy HullClass = "heavy"
	// HullClassWarehouse is the storage/stocker pool (DedicatedFleet "warehouse"), sized to
	// producing-chain export demand (sp-1txd M7).
	HullClassWarehouse HullClass = "warehouse"
	// HullClassExplorer is the off-gate warp-exploration pool (DedicatedFleet "explorer", sp-a3yn
	// slice C of sp-4imi). It is sized to slice-B off-gate demand and is the ONE class EXEMPT from
	// the realized-$/hr payback guards: an explorer buys REACH (it charts new systems so the cheap
	// probe frontier resumes via growFrontierGraph), NOT income, so it has no marginal realized
	// rate. That exemption is REPLACED — not dropped — by three explorer-only bounds enforced in
	// EvaluateGuards: the demand-gate (buys only when off-gate demand fires AND the class is armed),
	// a HARD CAP of 1 (the class fleet ceiling), and a price ceiling (~819k SHIP_EXPLORER + premium).
	// Opt-IN (explorer_hulls_enabled, default OFF) and double-gated, so a bare deploy buys nothing.
	HullClassExplorer HullClass = "explorer"
	// HullClassContractDelivery is the capacity reconciler's contract-delivery capital pool
	// (delivery hulls + contract-depot warehouses + contract-depot stockers, sp-nkqn / st-7zk). The
	// reconciler EMITS its tier-4 gap into this class via the ContractDeliveryDemandBridge, so
	// arming it routes ROUTINE early-game hauler scaling through this coordinator's SINGLE
	// money-guard stack — guard-gated AUTO, not captain-approval-gated (RULINGS #6: the guards are
	// the gate). Opt-IN (contract_delivery_hulls_enabled, default OFF) exactly like the
	// warehouse/explorer classes, so a bare deploy keeps it dormant (byte-identical). It runs the
	// FULL realized-$/hr income guards (NOT explorer-exempt): a routine buy is a measured-demand
	// buy. The canonical constant lives here (the fleetCmd package the guard switches read); the
	// adapter-layer bridge aliases it to avoid a second string literal drifting.
	HullClassContractDelivery HullClass = "contract_delivery"
)

// ClassDemand is one class's demand read for a tick: how many hulls the demand model wants
// (Demand) vs how many exist now (Current), plus the marginal realized rate the era-clock
// payback and realized-rate guards judge, and whether that rate is decaying (heavies stop
// buying when absorption saturates). A demand model that cannot read its inputs sets
// Readable=false, which the coordinator treats as fail-closed: NO buy (a missing signal must
// never trigger a spend).
type ClassDemand struct {
	Class HullClass
	// Demand is how many hulls of this class the demand model wants standing.
	Demand int
	// Current is how many hulls of this class exist now.
	Current int
	// MarginalRate is the expected marginal realized credits/hour the NEXT hull of this class
	// would earn — the number the era-clock payback and realized-rate-floor guards judge. 0
	// when no rate signal is available (then the realized-rate guard fails closed).
	MarginalRate float64
	// FleetAvgRate is the fleet-average realized rate for this class, the reference the
	// realized-rate floor is a fraction of (heavies: fleet-avg tour $/hr). 0 when unavailable.
	FleetAvgRate float64
	// RateDeclining is true when the class's realized rate is trending down (absorption
	// saturating) — the heavy stop-buy signal.
	RateDeclining bool
	// RateReadable reports whether the realized-rate signal (MarginalRate/FleetAvgRate) was
	// readable. It is distinct from Readable: the DEMAND can be sized (Readable=true) while the
	// rate is not yet available (RateReadable=false, e.g. a pre-realization chain) — the coordinator
	// maps this into the guard request so the realized-rate guard fails closed on its own.
	RateReadable bool
	// Readable reports whether the demand model read all its inputs. false ⇒ fail-closed (no
	// buy this tick), with Reason naming what could not be read.
	Readable bool
	// Reason is a human note for the decision log (why unreadable, or how demand was derived).
	Reason string
}

// Shortfall is the unmet demand: how many hulls of the class are wanted beyond the current
// pool. 0 when the pool already meets or exceeds demand.
func (d ClassDemand) Shortfall() int {
	if d.Demand > d.Current {
		return d.Demand - d.Current
	}
	return 0
}

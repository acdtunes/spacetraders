// Package commands holds the fleet capacity autosizer: a standing per-player
// coordinator that SIZES the hull pool to demand and AUTO-BUYS hulls behind the full
// money-guard stack. It is the buy-side twin of the vdld siting coordinator (which sizes the
// factory-chain portfolio at zero cost); this one spends real credits, so its guard stack is
// fail-CLOSED (any unreadable input ⇒ no buy) where vdld's kill-switch is fail-open.
//
// Shape mirrors run_siting_coordinator.go: a registered singleton handler with optional
// setter-collaborators, one infinite reconcile loop in Handle(), resolveFleetAutosizerConfig()
// resolving every <=0 knob to a documented protective default (RULINGS #5). One coordinator,
// N pluggable demand providers (lights, heavies) — the vdld pluggable-provider idiom.
package commands

import "github.com/andrescamacho/spacetraders-go/internal/domain/hullbuy"

// The hull-purchase VOCABULARY is not the autosizer's to own — the dedicated contract scaler
// buys through the same classes and the same order/result shapes — so it lives in
// domain/hullbuy and these are aliases. They exist so this coordinator's internals keep reading
// as one package; every consumer outside it names hullbuy directly, and deleting this package
// deletes only the aliases.
type HullClass = hullbuy.HullClass

const (
	// HullClassLight is the factory-worker pool (HAULER role), sized to factory-chain demand.
	HullClassLight = hullbuy.HullClassLight
	// HullClassHeavy is the trade-tour pool (DedicatedFleet "trade"), sized to trade demand.
	HullClassHeavy = hullbuy.HullClassHeavy
	// HullClassContractDelivery is the capacity reconciler's contract-delivery capital pool. The
	// reconciler EMITS its tier-4 gap into this class via the ContractDeliveryDemandBridge, so
	// arming it routes ROUTINE early-game hauler scaling through this coordinator's SINGLE
	// money-guard stack — guard-gated AUTO, not captain-approval-gated (RULINGS #6: the guards are
	// the gate). Opt-IN (contract_delivery_hulls_enabled, default OFF), so a bare deploy keeps it
	// dormant (byte-identical).
	HullClassContractDelivery = hullbuy.HullClassContractDelivery
)

// ClassDemand is one class's demand read for a tick: how many hulls the demand model wants
// (Demand) vs how many exist now (Current). A demand model that cannot read its inputs sets
// Readable=false, which the coordinator treats as fail-closed: NO buy (a missing signal must
// never trigger a spend).
type ClassDemand struct {
	Class HullClass
	// Demand is how many hulls of this class the demand model wants standing.
	Demand int
	// Current is how many hulls of this class exist now.
	Current int
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

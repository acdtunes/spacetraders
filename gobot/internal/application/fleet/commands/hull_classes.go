// Package commands holds the fleet's HULL-BUYING coordinators and the fail-CLOSED money-guard
// stack they judge every purchase through. Spending is irreversible and not-buying is safe, so
// any unreadable input (price, treasury, census, lane count, API utilization) BLOCKS.
//
// The guard stack is the package's, not any one coordinator's: it is judged pure, it is shared,
// and it outlives whichever coordinator currently calls it.
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

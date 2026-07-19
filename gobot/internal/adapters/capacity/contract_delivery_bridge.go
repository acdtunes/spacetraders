package capacity

import (
	"context"
	"fmt"
	"sync"

	fleetCmd "github.com/andrescamacho/spacetraders-go/internal/application/fleet/commands"
	domcap "github.com/andrescamacho/spacetraders-go/internal/domain/capacity"
)

// ContractDeliveryDemandBridge is the seam between the capacity reconciler's
// CAPITAL tier and the fleet autosizer's single
// guarded buy path. One object plays two ports:
//   - capacity.CapitalDemandSink (write): the reconciler's GOVERN emitter
//     publishes each tick's contract-delivery capital demand here;
//   - fleetCmd.ClassDemandProvider (read): the autosizer consumes it as just
//     another registered demand provider, so the demand flows through the SAME
//     absolute fleet ceiling + per-tick cap + money-guard stack as lights /
//     heavies / production warehouses — evaluated over the COMBINED post-buy
//     fleet count, so this second source cannot over-buy. NO second guard stack
//     is built in this lane.
//
// DORMANT until armed: HullClassContractDelivery is outside the {light, heavy,
// warehouse} set the autosizer's classDisabled recognizes, and its documented default
// ("unknown class: never act") SKIPS the class every tick. So registering this
// provider adds a pluggable slot that reads no demand and buys nothing until an
// arming lane (paired with the proposal gate) explicitly wires the class
// into classDisabled + classGuardConfig. Combined with the reconciler being
// deploy-inert, that is the structural "nothing auto-buys yet" guarantee.
//
// The bridge is written on the reconciler's goroutine and read on the
// autosizer's, so access is mutex-guarded.

// HullClassContractDelivery is the hull-class label for the
// contract-delivery capital pool (delivery hulls + contract-depot warehouses +
// contract-depot stockers) — DISTINCT from HullClassWarehouse (the autosizer's
// production-chain warehouse pool), so the two providers' demands ADD rather
// than collide on one shared class (the ownership split). It ALIASES the
// canonical constant in the fleetCmd package (sp-nkqn wired the class into the
// autosizer's classDisabled/classGuardConfig there); aliasing keeps the two
// sides on one string so the label can never drift.
const HullClassContractDelivery = fleetCmd.HullClassContractDelivery

// ContractDeliveryDemandBridge holds the latest emitted capital demand.
type ContractDeliveryDemandBridge struct {
	mu     sync.Mutex
	latest domcap.CapitalDemand
}

// NewContractDeliveryDemandBridge builds an empty bridge. Until the reconciler's
// first emit it reads UNREADABLE, so the autosizer fails closed (never buys on a
// demand it was never told).
func NewContractDeliveryDemandBridge() *ContractDeliveryDemandBridge {
	return &ContractDeliveryDemandBridge{}
}

// EmitCapitalDemand implements capacity.CapitalDemandSink — the reconciler's
// GOVERN emitter publishes each tick's demand here (write side).
func (b *ContractDeliveryDemandBridge) EmitCapitalDemand(demand domcap.CapitalDemand) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.latest = demand
}

// Class implements fleetCmd.ClassDemandProvider — the distinct contract-delivery
// class the autosizer applies its per-class disable flag + guard knobs to.
func (b *ContractDeliveryDemandBridge) Class() fleetCmd.HullClass { return HullClassContractDelivery }

// Demand implements fleetCmd.ClassDemandProvider (read side) — it maps the
// latest emitted CapitalDemand into the autosizer's ClassDemand.
//
// The reconciler's DIFF gap surfaces as the shortfall directly: Demand=Hulls,
// Current=0 (delta semantics — the gap is already net of actual and recomputed
// fresh each stateless tick, so it shrinks as the autosizer buys). This is safe for
// the HARD ceiling the lane must preserve: the autosizer's ABSOLUTE fleet ceiling +
// per-tick cap are judged over the real total hull count (in.totalHulls),
// unaffected by this Current; the per-class contract-delivery sub-ceiling is the
// arming lane's concern (it does not exist until the class is wired into
// classGuardConfig). An un-emitted (or non-Present) snapshot reads unreadable so
// the guard stack fails closed.
func (b *ContractDeliveryDemandBridge) Demand(_ context.Context, _ int, _ fleetCmd.DemandParams) (fleetCmd.ClassDemand, error) {
	b.mu.Lock()
	latest := b.latest
	b.mu.Unlock()

	if !latest.Present {
		return fleetCmd.ClassDemand{
			Class:    HullClassContractDelivery,
			Readable: false,
			Reason:   "no capital demand emitted yet by the capacity reconciler (unarmed or pre-first-tick) — fail closed",
		}, nil
	}
	return fleetCmd.ClassDemand{
		Class:         HullClassContractDelivery,
		Demand:        latest.Hulls,
		Current:       0,
		MarginalRate:  latest.MarginalProjectedCrHr,
		FleetAvgRate:  latest.FleetPerHullCrHr,
		RateDeclining: false,
		RateReadable:  latest.RateReadable,
		Readable:      true,
		Reason: fmt.Sprintf("contract-delivery capital gap %d (warehouse %d + stocker %d + delivery %d) from the capacity reconciler; projected %.0f cr/hr/hull vs fleet %.0f",
			latest.Hulls, latest.WarehouseHulls, latest.StockerHulls, latest.DeliveryHulls, latest.MarginalProjectedCrHr, latest.FleetPerHullCrHr),
	}, nil
}

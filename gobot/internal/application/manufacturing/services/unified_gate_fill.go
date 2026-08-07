package services

import (
	"context"
)

// Unified gate-fill: the run-context surface that turns a generic goods-factory run into a gate
// fill. A gate fill IS a goods-factory run differing in exactly one thing — what happens to the
// finished root output: a profit factory SELLS it at a resale sink, a gate fill DELIVERS it to a
// construction site. That one difference is carried here as a DeliveryTarget on the run context.
//
// Everything rides on ctx rather than a struct field for the same singleton-executor race reason as
// the input price ceiling and the fabricate depth cap: ProductionExecutor and SupplyChainResolver
// are boot SINGLETONS shared across every concurrent factory container, so per-run config on a
// struct would race between sibling factories. context.Context threads BY VALUE through the
// recursive production chain, so ONE stamp in the coordinator reaches every child node.
//
// A caller that never stamps a target (every profit-factory run, the demand/siting estimators)
// reads the zero value, a resale sink. Gate mode engages ONLY where a construction-site target is
// stamped (IsUnifiedGateNode).

// DeliveryTargetKind distinguishes the two terminals a produced root output can take. The zero value
// is DeliverySink so an unstamped run is a resale sink (unchanged behavior).
type DeliveryTargetKind int

const (
	// DeliverySink (zero value) sells the fabricated root output at the chain-margin guard's resale
	// sink — the profit-factory terminal.
	DeliverySink DeliveryTargetKind = iota
	// DeliveryConstructionSite delivers the fabricated root output to a jump-gate construction site
	// instead of selling it — the gate-fill terminal (sp-vh1s §5.1).
	DeliveryConstructionSite
)

// DeliveryTarget names what happens to a production run's finished root output. The zero value is a
// resale sink (Kind == DeliverySink), so an unstamped context is byte-identical to today.
type DeliveryTarget struct {
	Kind     DeliveryTargetKind
	Waypoint string // the construction-site waypoint when Kind == DeliveryConstructionSite; empty for a sink
}

// ConstructionSiteTarget builds a delivery target that routes the root output to the given jump-gate
// construction-site waypoint (sp-vh1s §5.1).
func ConstructionSiteTarget(waypoint string) DeliveryTarget {
	return DeliveryTarget{Kind: DeliveryConstructionSite, Waypoint: waypoint}
}

// IsConstructionSite reports whether this target delivers to a construction site (vs. selling at a
// resale sink). The terminal switch in produceNodeOnly keys the Sink↔ConstructionSite branch on it.
func (t DeliveryTarget) IsConstructionSite() bool {
	return t.Kind == DeliveryConstructionSite
}

// SiteWaypoint returns the construction-site waypoint the root output is delivered to (empty for a
// resale-sink target).
func (t DeliveryTarget) SiteWaypoint() string {
	return t.Waypoint
}

type deliveryTargetCtxKey struct{}

// WithDeliveryTarget stamps the run's delivery target onto ctx (sp-vh1s). A caller that never stamps
// it reads the zero value (a resale sink) at the point of use.
func WithDeliveryTarget(ctx context.Context, target DeliveryTarget) context.Context {
	return context.WithValue(ctx, deliveryTargetCtxKey{}, target)
}

// DeliveryTargetFromContext reads the run's delivery target, returning the zero value (a resale sink,
// carrying no waypoint) when none was stamped — so a non-gate run keeps selling at the sink.
func DeliveryTargetFromContext(ctx context.Context) DeliveryTarget {
	if t, ok := ctx.Value(deliveryTargetCtxKey{}).(DeliveryTarget); ok {
		return t
	}
	return DeliveryTarget{}
}

// IsUnifiedGateNode reports whether the current node runs in unified gate-fill mode — the single
// predicate lane B's per-node gates (input_source_selector, input_price_ceiling) call to switch a
// node to MARGIN-BLIND, solvency-bounded buying (sp-vh1s CONTRACT #2, §5.2, Admiral
// sign-off 2026-07-14). It is true exactly when the run delivers to a construction site; a
// resale-sink (profit-factory) run is never a gate node — so those keep today's price ceiling and
// chain-margin gates unchanged.
//
// This was once ALSO gated on a separate unified_gate_fill toggle, but that toggle had no config
// key and no struct field behind it: the sole stamper passed a literal true alongside the
// construction-site target, so the conjunct was always redundant with the target check. It was
// removed in sp-k87tl rather than left as a knob that could never be turned (RULINGS: no flags).
func IsUnifiedGateNode(ctx context.Context) bool {
	return DeliveryTargetFromContext(ctx).IsConstructionSite()
}

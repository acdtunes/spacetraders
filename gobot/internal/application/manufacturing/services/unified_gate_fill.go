package services

import (
	"context"
	"time"
)

// sp-vh1s — unified gate-fill: the run-context surface that turns a generic goods-factory run
// into a gate fill. A gate fill IS a goods-factory run differing in exactly one thing — what
// happens to the finished root output: a profit factory SELLS it at a resale sink; a gate fill
// DELIVERS it to a construction site. That one difference is carried here as a DeliveryTarget on
// the run context, alongside the UnifiedGateFill toggle.
//
// Everything rides on ctx (not a struct field) for the SAME singleton-executor race reason as the
// working-capital reserve / input price ceiling / fabricate depth cap (WithConfiguredReserve,
// WithInputPriceCeiling, WithFabricateDepthCap): the ProductionExecutor and SupplyChainResolver are
// boot SINGLETONS shared across every concurrent factory container, so per-run config would race
// between sibling factories if it lived on a struct — ctx is per-Handle and race-free. context.Context
// threads BY VALUE through the recursive production chain, so ONE stamp in the coordinator's
// executeCoordination reaches every child node.
//
// A caller that never stamps these (every profit-factory run, every test that predates this bead,
// the demand/siting estimators) reads the zero value: UnifiedGateFill off, DeliveryTarget = resale
// sink — the no-op, byte-identical-to-today path. The whole feature is dark until the toggle is on
// AND a construction-site target is stamped (IsUnifiedGateNode).

// DeliveryTargetKind distinguishes the two terminals a produced root output can take. The zero value
// is DeliverySink so an unstamped run is a resale sink (unchanged behavior).
type DeliveryTargetKind int

const (
	// DeliverySink (zero value) sells the fabricated root output at the chain-margin guard's resale
	// sink — the profit-factory terminal, unchanged by this bead.
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
type unifiedGateFillCtxKey struct{}

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

// WithUnifiedGateFill stamps the unified_gate_fill toggle onto ctx (sp-vh1s CONTRACT #1). Fed from
// ManufacturingConfig.UnifiedGateFill via the coordinator's command. false (the default) leaves the
// whole feature dark — every gate node behaves exactly as today.
func WithUnifiedGateFill(ctx context.Context, enabled bool) context.Context {
	return context.WithValue(ctx, unifiedGateFillCtxKey{}, enabled)
}

// unifiedGateFillFromContext reads the toggle, defaulting to false (off) when unstamped.
func unifiedGateFillFromContext(ctx context.Context) bool {
	enabled, _ := ctx.Value(unifiedGateFillCtxKey{}).(bool)
	return enabled
}

// --- THROUGHPUT-PACING (sp-vh1s §5.2 / analyst adjustment 1; Admiral sign-off 2026-07-14) ---
//
// The gate output-buy is MARGIN-BLIND (the price ceiling was dropped for gate nodes), so its ONLY
// safety limit is physical production throughput: buy no faster than the source factory produces, or
// the draw re-depletes supply and re-trips the exact ceiling this bead removed (a stall/recover
// oscillation — the sp-iv65 −6.6M mechanism, real even when margin-indifferent). The pace is keyed to
// the factory's export trade volume (tv), the observable throughput proxy: buy-rate ≤ k × tv per hour
// with each lot ≤ tv. k=2.0 is the analyst-validated default (MEDIUM confidence — tuned live against
// observed F48/D42 production vs draw), so both coefficients are named, ctx-carried config params.

const (
	// defaultThroughputBuyRateMultiple is k: the trailing-hour output-buy ceiling as a multiple of the
	// factory's export trade volume (k × tv units per hour). 2.0 per the analyst's ruling. A 0/absent
	// config value resolves here at the point of use — a protective default that turns the pacing gate
	// ON (it only STOPS an over-fast buy, never moves money), so a live-by-default is correct (RULINGS
	// #5). Operator-tunable via WithThroughputPacing.
	defaultThroughputBuyRateMultiple = 2.0
	// defaultThroughputPerLotMultiple caps a SINGLE output-buy lot at this multiple of tv (1.0 == one
	// trade volume). Keeps any one lot within a single market tranche so a burst cannot front-load the
	// hourly budget in one oversized buy. 0/absent resolves to 1.0 at the point of use (RULINGS #5).
	defaultThroughputPerLotMultiple = 1.0
	// throughputPacingWindow is the trailing window the k×tv rate ceiling is measured over — one hour,
	// matching the "per hour" the coefficient is expressed in.
	throughputPacingWindow = time.Hour
)

type throughputPacingCtxKey struct{}

type throughputPacingConfig struct {
	buyRateMultiple float64 // k — trailing-hour ceiling = buyRateMultiple × tv
	perLotMultiple  float64 // single-lot ceiling = perLotMultiple × tv
	disabled        bool
}

// WithThroughputPacing stamps the per-run gate output-buy pacing config onto ctx (sp-vh1s). A 0
// buyRateMultiple resolves to defaultThroughputBuyRateMultiple (2.0) and a 0 perLotMultiple to
// defaultThroughputPerLotMultiple (1.0) at the point of use; disabled=true is the emergency
// off-switch that reverts a gate output-buy to the plain min(cargo space, tv) cap (RULINGS #5). A run
// that never stamps it (every profit factory, every pre-bead test) keeps the pacing at its defaults —
// but the pacing only ENGAGES for a gate node (IsUnifiedGateNode), so an OFF fleet is unaffected.
func WithThroughputPacing(ctx context.Context, buyRateMultiple, perLotMultiple float64, disabled bool) context.Context {
	return context.WithValue(ctx, throughputPacingCtxKey{}, throughputPacingConfig{
		buyRateMultiple: buyRateMultiple, perLotMultiple: perLotMultiple, disabled: disabled,
	})
}

// throughputPacingConfigFromContext reads the pacing config, resolving absent/zero coefficients to
// their live-by-default values.
func throughputPacingConfigFromContext(ctx context.Context) throughputPacingConfig {
	cfg, _ := ctx.Value(throughputPacingCtxKey{}).(throughputPacingConfig)
	if cfg.buyRateMultiple <= 0 {
		cfg.buyRateMultiple = defaultThroughputBuyRateMultiple
	}
	if cfg.perLotMultiple <= 0 {
		cfg.perLotMultiple = defaultThroughputPerLotMultiple
	}
	return cfg
}

// IsUnifiedGateNode reports whether the current node runs in unified gate-fill mode — the single
// predicate lane B's per-node gates (input_source_selector, input_price_ceiling) call to switch a
// node to MARGIN-BLIND, solvency-and-throughput-paced buying (sp-vh1s CONTRACT #2, §5.2, Admiral
// sign-off 2026-07-14). It is true ONLY when the toggle is ON *and* the run delivers to a construction
// site; a toggle-off run, or a resale-sink (profit-factory) run, is never a gate node — so those keep
// today's price ceiling and chain-margin gates unchanged (OFF = byte-identical).
func IsUnifiedGateNode(ctx context.Context) bool {
	return unifiedGateFillFromContext(ctx) && DeliveryTargetFromContext(ctx).IsConstructionSite()
}

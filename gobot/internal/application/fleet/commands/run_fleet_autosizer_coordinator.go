package commands

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

const (
	// Config defaults (RULINGS #5: every operational value is a config key, filled here only
	// when the launch config leaves it unset — the Analyst/Admiral own the numbers). Documented
	// on config.FleetAutosizerConfig.
	defaultAutosizerTickSeconds = 900 // 15min — sizing is strategic, not per-second
	defaultPurchaseCapPerTick   = 1

	// Protective fleet ceilings (the HARD API-request-budget bound — each hull adds request
	// load). Deliberately conservative: an auto-buyer that surprises the treasury is worse than
	// one that stops early, and the captain raises these from evidence.
	defaultFleetCeilingTotal     = 50
	defaultFleetCeilingLights    = 35
	defaultFleetCeilingHeavies   = 15
	defaultFleetCeilingWarehouse = 8

	defaultPurchaseMarginOverFloor     = 200000
	defaultLightRotationSlots          = 3.5
	defaultHeavyMarginalRateFloor      = 0.7
	defaultHeavyUnservedLanesMin       = 3
	defaultHeavyTreasuryPctPerPurchase = 25
	// defaultDecliningRateUnservedFloor (sp-zbe6) is the near-zero unserved-lane count at/below
	// which a DECLINING aggregate realized-rate is a genuine heavy stop-buy; above it the decline is
	// hull concentration and the buy proceeds. "A couple of lanes" — the resolver never lets it reach
	// 0, so the declining stop-buy can never be silently disabled (the demand guard forces Shortfall>0).
	defaultDecliningRateUnservedFloor  = 2
	defaultAPIUtilCeilingPct           = 85
	defaultPaybackSafetyFactor         = 0.5
	defaultPurchaseCutoffEraMinusHours = 3.0
	defaultMaxPremiumOverCheapestPct   = 50
	defaultZeroEffectAlarmTicks        = 4

	// Default shipyard ship-type symbols per class (RULINGS #5: even the asset is a knob).
	defaultShipTypeLights  = "SHIP_LIGHT_HAULER"
	defaultShipTypeHeavies = "SHIP_HEAVY_FREIGHTER"

	defaultWarehouseMinChainTickPersistence = 2
	defaultWarehouseCapacityTargetHours     = 2.0
	defaultWarehouseFrameClassCeiling       = "light"
)

// DemandParams carries the live-resolved config the demand providers need each tick (rotation
// slots, etc.). The coordinator fills it from its runConfig so the providers, constructed once at
// boot, still see the current config.yaml value (the sp-ts82 live-config discipline) without
// holding config themselves.
type DemandParams struct {
	// LightRotationSlots is the C3 rotation divisor inverted: K chains need K × this workers.
	LightRotationSlots float64
	// WarehouseMinTickPersistence is the hysteresis: a chain must sit in the running portfolio this
	// many consecutive ticks before a warehouse follows it (sp-1j3f).
	WarehouseMinTickPersistence int
	// WarehouseMinRealizedPerHour is the pay gate: only a chain earning at least this realized $/hr
	// (rh2z chain_pnl) pulls a warehouse.
	WarehouseMinRealizedPerHour float64
	// MaxWarehouseHulls caps the warehouse demand regardless of how many durable chains exist.
	MaxWarehouseHulls int
}

// ClassDemandProvider reads one hull class's demand each tick (the pluggable-provider seam,
// vdld's idiom). The coordinator holds a slice of these; a provider whose class is disabled by
// config is skipped. Concrete impls (light / heavy / warehouse) live in their own files and are
// wired by the daemon; tests inject fakes.
type ClassDemandProvider interface {
	// Class is the hull class this provider sizes — the coordinator uses it to apply the
	// per-class disable flag and the per-class guard knobs.
	Class() HullClass
	// Demand reads the class's (demand, current, marginal-rate) for the player this tick, given
	// the live-resolved params. An unreadable input must be surfaced as
	// ClassDemand{Readable:false, Reason:...}, NOT an error, so a transient read miss fails closed
	// (no buy) without aborting the whole tick. A returned error is an infra fault the coordinator
	// logs and skips the class on.
	Demand(ctx context.Context, playerID int, params DemandParams) (ClassDemand, error)
}

// RunFleetAutosizerCoordinatorCommand launches the standing autosizer for a player (sp-1txd).
// Like the siting / trade-fleet coordinators it runs an infinite reconcile loop inside a single
// Handle() call; the container wraps it. All knobs are launch-config keys (RULINGS #5); the zero
// value falls back to the documented default, so the CLI/daemon passes only what it overrides.
type RunFleetAutosizerCoordinatorCommand struct {
	PlayerID    int
	ContainerID string
	AgentSymbol string

	// Master + per-class escapes. Disabled is the negation of autosizer_disabled so an absent
	// key reads as enabled (LIVE BY DEFAULT — Admiral no-dark-shipping).
	Disabled              bool
	DryRun                bool
	LightsDisabled        bool
	HeaviesDisabled       bool
	WarehouseHullsEnabled bool

	TickIntervalSecs   int
	PurchaseCapPerTick int

	FleetCeilingTotal     int
	FleetCeilingLights    int
	FleetCeilingHeavies   int
	FleetCeilingWarehouse int

	PurchaseMarginOverFloor int64
	Reserve                 int64
	ReserveTreasuryPct      int

	LightRotationSlots float64

	HeavyMarginalRateFloor      float64
	HeavyUnservedLanesMin       int
	HeavyTreasuryPctPerPurchase int
	DecliningRateUnservedFloor  int

	APIUtilizationCeilingPct int

	PaybackSafetyFactor           float64
	PurchaseCutoffAtEraMinusHours float64

	MaxPriceLights            int64
	MaxPriceHeavies           int64
	MaxPremiumOverCheapestPct int
	PreferDemandProximalYard  *bool

	ShipTypeLights  string
	ShipTypeHeavies string

	ZeroEffectAlarmTicks int

	// Warehouse class (sp-1txd M7/M8).
	WarehouseMinChainRealizedPerHour float64
	WarehouseMinChainTickPersistence int
	MaxWarehouseHulls                int
	StockerHullsPerWarehouseGroup    int
	WarehouseCapacityTargetHours     float64
	MaxModuleSpendPerHull            int64
	WarehouseFrameClassCeiling       string
}

// RunFleetAutosizerCoordinatorResponse reports reconcile progress. Because the loop is infinite
// it is only observed on context cancellation (shutdown).
type RunFleetAutosizerCoordinatorResponse struct {
	Ticks  int
	Errors []string
}

// RunFleetAutosizerCoordinatorHandler reconciles the desired hull pool against the live one each
// tick and buys the shortfall behind the guard stack. Registered singleton (one instance serves
// every player's ticks), so all decision inputs are read fresh from the ports each pass; the only
// in-memory state is edge-trigger bookkeeping (the zero-effect alarm streak + the heavy
// consecutive-shortfall streak), keyed by container ID so it stays per-coordinator.
type RunFleetAutosizerCoordinatorHandler struct {
	providers []ClassDemandProvider
	clock     shared.Clock

	// Buy-path collaborators (M5), wired by setters at boot. Every one is nil-safe: a nil reader
	// yields an unreadable input, which the guard stack fails CLOSED on (no buy) — the API-utilization
	// reader included, since sp-a5dq (an absent/unreadable utilization now holds concurrency growth).
	treasury  TreasuryReader
	era       EraClockReader
	apiUtil   APIUtilizationReader
	fleetSize FleetSizeReader
	yardPrice YardPriceReader
	purchaser Purchaser
	notifier  PurchaseNotifier
	metrics   MetricsSink

	// warehouse is the typed handle to the warehouse provider (sp-1j3f). Held in addition to its
	// slot in providers so the reconcile loop can invoke its DISPATCH step (place idle/stranded hulls
	// on durable chains) after the buy pass. nil until wired, in which case dispatch is skipped.
	warehouse *WarehouseDemandProvider

	mu    sync.Mutex
	state map[string]*autosizerState // keyed by container ID
}

// autosizerState is the per-coordinator in-memory edge-trigger bookkeeping.
type autosizerState struct {
	// heavyShortfallStreak counts consecutive ticks the heavy class has shown unmet demand
	// (the heavy_unserved_lanes_min anti-thrash gate). Reset when the shortfall clears.
	heavyShortfallStreak int
	// noEffectStreak counts consecutive ticks with demand-but-zero-purchase (blocked every
	// tick, or silent dry-run); noEffectPaged marks the one WARN already emitted this episode.
	noEffectStreak int
	noEffectPaged  bool
}

// NewRunFleetAutosizerCoordinatorHandler wires the coordinator. clock defaults to the real clock
// when nil (production). Demand providers are added with AddDemandProvider; the guard stack,
// purchaser, and notifier are wired with their setters (M2/M5).
func NewRunFleetAutosizerCoordinatorHandler(clock shared.Clock) *RunFleetAutosizerCoordinatorHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &RunFleetAutosizerCoordinatorHandler{
		clock: clock,
		state: make(map[string]*autosizerState),
	}
}

// AddDemandProvider registers a class demand provider. Registration order is evaluation order.
func (h *RunFleetAutosizerCoordinatorHandler) AddDemandProvider(p ClassDemandProvider) {
	h.providers = append(h.providers, p)
}

// SetWarehouseProvider wires the warehouse provider (sp-1j3f). It registers the provider in the
// evaluation loop (its Demand() feeds the buy path like any class) AND keeps a typed handle so the
// reconcile loop can run its DISPATCH step each tick — placing idle/stranded warehouse hulls on the
// durable chains, which the generic buy path cannot do. Call this INSTEAD of AddDemandProvider for
// the warehouse class.
func (h *RunFleetAutosizerCoordinatorHandler) SetWarehouseProvider(p *WarehouseDemandProvider) {
	h.warehouse = p
	h.providers = append(h.providers, p)
}

// SetTreasuryReader wires the live-treasury source (M5). Unset → treasury unreadable → the
// treasury guards fail closed (no buy).
func (h *RunFleetAutosizerCoordinatorHandler) SetTreasuryReader(r TreasuryReader) { h.treasury = r }

// SetEraClockReader wires the era-clock source (M5). Unset → era unreadable → the era-payback
// guard fails closed.
func (h *RunFleetAutosizerCoordinatorHandler) SetEraClockReader(r EraClockReader) { h.era = r }

// SetAPIUtilizationReader wires the API-utilization source (M5). Unset → utilization unreadable →
// the API-util guard fails CLOSED (sp-a5dq): a mis-wired coordinator holds concurrency growth rather
// than silently permitting unbounded growth into a saturated API.
func (h *RunFleetAutosizerCoordinatorHandler) SetAPIUtilizationReader(r APIUtilizationReader) {
	h.apiUtil = r
}

// SetFleetSizeReader wires the total-hull-count source for the absolute fleet ceiling (M5). Unset
// or erroring → total unreadable → no buy (fail closed).
func (h *RunFleetAutosizerCoordinatorHandler) SetFleetSizeReader(r FleetSizeReader) { h.fleetSize = r }

// SetYardPriceReader wires the shipyard price source (M5). Unset → price unreadable → the price
// guards fail closed.
func (h *RunFleetAutosizerCoordinatorHandler) SetYardPriceReader(r YardPriceReader) { h.yardPrice = r }

// SetPurchaser wires the buy+dedicate collaborator (M5). Unset → the coordinator evaluates and
// logs but never spends (an implicit dry-run, surfaced loudly and by the zero-effect alarm).
func (h *RunFleetAutosizerCoordinatorHandler) SetPurchaser(p Purchaser) { h.purchaser = p }

// SetPurchaseNotifier wires the captain purchase-notice channel (M5). Optional.
func (h *RunFleetAutosizerCoordinatorHandler) SetPurchaseNotifier(n PurchaseNotifier) { h.notifier = n }

// SetMetricsSink wires the metrics recorder (M5). Optional and nil-safe (pure observation).
func (h *RunFleetAutosizerCoordinatorHandler) SetMetricsSink(m MetricsSink) { h.metrics = m }

// Handle runs the reconcile loop until the context is cancelled.
func (h *RunFleetAutosizerCoordinatorHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	logger := common.LoggerFromContext(ctx)

	cmd, ok := request.(*RunFleetAutosizerCoordinatorCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	cfg := resolveFleetAutosizerConfig(cmd)
	logger.Log("INFO", fmt.Sprintf("Fleet autosizer starting (tick %s, dry_run=%v, disabled=%v, lights_disabled=%v, heavies_disabled=%v)", cfg.Tick, cfg.DryRun, cfg.Disabled, cfg.LightsDisabled, cfg.HeaviesDisabled), map[string]interface{}{
		"action":           "autosizer_start",
		"container_id":     cmd.ContainerID,
		"dry_run":          cfg.DryRun,
		"disabled":         cfg.Disabled,
		"lights_disabled":  cfg.LightsDisabled,
		"heavies_disabled": cfg.HeaviesDisabled,
	})

	result := &RunFleetAutosizerCoordinatorResponse{Errors: []string{}}

	for {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

		if _, err := h.reconcileOnce(ctx, cmd); err != nil {
			result.Errors = append(result.Errors, err.Error())
			logger.Log("ERROR", fmt.Sprintf("Autosizer reconcile failed: %v", err), nil)
		}
		result.Ticks++

		select {
		case <-time.After(cfg.Tick):
		case <-ctx.Done():
			return result, ctx.Err()
		}
	}
}

// coordinatorState returns (creating if needed) the per-container edge-trigger bookkeeping.
func (h *RunFleetAutosizerCoordinatorHandler) coordinatorState(containerID string) *autosizerState {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := h.state[containerID]
	if st == nil {
		st = &autosizerState{}
		h.state[containerID] = st
	}
	return st
}

// classDisabled reports whether a class is frozen by config. Lights/heavies are LIVE BY DEFAULT
// (only an explicit *_disabled freezes them); warehouse is opt-IN (only runs when enabled).
func (c autosizerRunConfig) classDisabled(class HullClass) bool {
	switch class {
	case HullClassLight:
		return c.LightsDisabled
	case HullClassHeavy:
		return c.HeaviesDisabled
	case HullClassWarehouse:
		return !c.WarehouseHullsEnabled
	default:
		return true // unknown class: never act
	}
}

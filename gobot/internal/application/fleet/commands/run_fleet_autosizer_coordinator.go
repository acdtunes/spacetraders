package commands

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/health"
	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

const (
	// Config defaults (RULINGS #5: every operational value is a config key, filled here only
	// when the launch config leaves it unset — the Analyst/Admiral own the numbers). Documented
	// on config.FleetAutosizerConfig.
	defaultAutosizerTickSeconds = 900 // 15min — sizing is strategic, not per-second
	defaultPurchaseCapPerTick   = 1

	// sp-r7eiu: defaultFleetCeiling{Lights,Heavies} were removed with the class_ceiling guard —
	// a flat per-class pool cap refused a purchase for being the Nth hull without asking whether
	// the fleet could afford it or had work for it, and had to be re-raised by hand every time the
	// fleet legitimately grew. See fleet_autosizer_guards.go for what bounds each class now.
	// defaultFleetCeilingExplorer SURVIVES below: it is the explorer's demand-side hard cap, not a
	// guard input.

	defaultPurchaseMarginOverFloor     = 200000
	defaultLightRotationSlots          = 3.5
	defaultHeavyUnservedLanesMin       = 3
	defaultHeavyTreasuryPctPerPurchase = 25
	// defaultHeavyCap bounds CAPITAL EXPOSURE in heavy hulls, counted fleet-wide regardless of
	// dedicated_fleet tag. Since sp-r7eiu removed the pool ceilings it is the ONLY count-based
	// bound on any class. 5 per the Admiral. An explicit 0 in config.yaml is a legitimate operator
	// HOLD, which is why the config field is a *int (see config.FleetAutosizerConfig.HeavyCap).
	defaultHeavyCap                  = 5
	defaultAPIUtilCeilingPct         = 85
	defaultMaxPremiumOverCheapestPct = 50
	defaultZeroEffectAlarmTicks      = 4

	// Default shipyard ship-type symbols per class (RULINGS #5: even the asset is a knob).
	defaultShipTypeLights  = "SHIP_LIGHT_HAULER"
	defaultShipTypeHeavies = "SHIP_HEAVY_FREIGHTER"

	// Explorer class. Opt-IN (default OFF, arming knob). The HARD CAP is 1; the
	// PRICE CEILING defaults to ~819k SHIP_EXPLORER + a premium (a REAL default, never 0=off — the
	// explorer's price ceiling is a required guard); the 25%-treasury big-ticket affordability rule
	// applies (the explorer buys an ~819k hull).
	defaultFleetCeilingExplorer           = 1
	defaultExplorerTreasuryPctPerPurchase = 25
	defaultMaxPriceExplorer               = 900000
	defaultShipTypeExplorer               = "SHIP_EXPLORER"

	// sp-y2ptq: the autosizer's contract-delivery class defaults were removed with the class (the
	// dedicated scaler owns contract-fleet capacity). The HullClassContractDelivery enum + the
	// "contract" dedication mapping the scaler reuses stay.
)

// DemandParams carries the live-resolved config the demand providers need each tick (rotation
// slots, etc.). The coordinator fills it from its runConfig so the providers, constructed once at
// boot, still see the current config.yaml value (the live-config discipline) without
// holding config themselves.
type DemandParams struct {
	// LightRotationSlots is the C3 rotation divisor inverted: K chains need K × this workers.
	LightRotationSlots float64
	// ExplorerHullsEnabled ARMS the explorer class. Default false (opt-in): when false the
	// explorer provider emits ZERO demand unconditionally, so a bare deploy buys no explorer.
	ExplorerHullsEnabled bool
	// MaxExplorerHulls is the explorer HARD CAP (the class fleet ceiling, default 1): the provider
	// never wants more than this regardless of the off-gate signal's count.
	MaxExplorerHulls int
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

// RunFleetAutosizerCoordinatorCommand launches the standing autosizer for a player.
// Like the siting / trade-fleet coordinators it runs an infinite reconcile loop inside a single
// Handle() call; the container wraps it. All knobs are launch-config keys (RULINGS #5); the zero
// value falls back to the documented default, so the CLI/daemon passes only what it overrides.
type RunFleetAutosizerCoordinatorCommand struct {
	PlayerID    int
	ContainerID string
	AgentSymbol string

	TickIntervalSecs   int
	PurchaseCapPerTick int

	PurchaseMarginOverFloor int64

	LightRotationSlots float64

	HeavyUnservedLanesMin       int
	HeavyTreasuryPctPerPurchase int
	// HeavyCap is the heavy-HULL cap. *int so an explicit 0 (operator hold) is told from
	// unset; nil ⇒ defaultHeavyCap.
	HeavyCap *int

	APIUtilizationCeilingPct int

	MaxPriceLights            int64
	MaxPriceHeavies           int64
	MaxPremiumOverCheapestPct int
	PreferDemandProximalYard  *bool

	ShipTypeLights  string
	ShipTypeHeavies string

	ZeroEffectAlarmTicks int

	// Explorer class. ExplorerHullsEnabled is the opt-IN arming knob (default OFF
	// — nothing boot-arms it). FleetCeilingExplorer is the HARD CAP (default 1); MaxPriceExplorer is
	// the price ceiling (default ~819k+premium); ExplorerTreasuryPctPerPurchase is the 25% rule.
	ExplorerHullsEnabled           bool
	FleetCeilingExplorer           int
	ExplorerTreasuryPctPerPurchase int
	MaxPriceExplorer               int64
	ShipTypeExplorer               string

	// sp-y2ptq: the contract-delivery class knobs were removed with the autosizer's demand-driven
	// contract scaling (the dedicated scaler owns it now).
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

	// Buy-path collaborators, wired by setters at boot. Every one is nil-safe: a nil reader
	// yields an unreadable input, which the guard stack fails CLOSED on (no buy) — the API-utilization
	// reader included (an absent/unreadable utilization holds concurrency growth rather than permitting it).
	treasury  TreasuryReader
	apiUtil   APIUtilizationReader
	yardPrice YardPriceReader
	// heavyCensus counts owned heavy HULLS (tag-independent); heavyYard reports the cheapest
	// known priced heavy ask. Together they derive the heavy reservation each tick.
	heavyCensus HeavyCensusReader
	heavyYard   HeavyYardReader
	// heavyYardCatalog and heavyErrand drive the heavy-yard PRICING ERRAND: a shipyard prices
	// its hulls only while a ship stands there, so a known-but-unpriced heavy yard needs a hull
	// sent to it before any reservation can ever form. Both nil-safe: unwired ⇒ no errand, and
	// the coordinator behaves exactly as it did before the errand existed.
	heavyYardCatalog HeavyYardCatalogReader
	heavyErrand      HeavyPricingErrandPort
	// heavyCapCfg is the per-tick live-config snapshot for heavy_cap (the autosizer's only
	// live-tunable knob). nil ⇒ launch-frozen behaviour.
	heavyCapCfg liveconfig.Reader
	purchaser   Purchaser
	notifier    PurchaseNotifier
	metrics     MetricsSink
	// stall is the WRITE-ONLY stall-escalation seam (health.StallObserver): each class's tick
	// reports PROGRESS / IDLE / BLOCKED(first failing guard) so a coordinator that refuses every
	// tick stops looking identical to one with nothing to do. Its single method returns nothing,
	// so no sizing decision can read the streak it accumulates (RULINGS #2 — see stall.go).
	stall health.StallObserver

	mu    sync.Mutex
	state map[string]*autosizerState // keyed by container ID
}

// autosizerState is the per-coordinator in-memory edge-trigger bookkeeping.
type autosizerState struct {
	// heavyShortfallStreak counts consecutive ticks the heavy class has shown unmet demand
	// (the heavy_unserved_lanes_min anti-thrash gate). Reset when the shortfall clears.
	heavyShortfallStreak int
	// noEffectStreak counts consecutive ticks with demand-but-zero-purchase (a guard blocking
	// every tick); noEffectPaged marks the one WARN already emitted this episode.
	noEffectStreak int
	noEffectPaged  bool
}

// NewRunFleetAutosizerCoordinatorHandler wires the coordinator. clock defaults to the real clock
// when nil (production). Demand providers are added with AddDemandProvider; the guard stack,
// purchaser, and notifier are wired with their setters.
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

// SetTreasuryReader wires the live-treasury source. Unset → treasury unreadable → the
// treasury guards fail closed (no buy).
func (h *RunFleetAutosizerCoordinatorHandler) SetTreasuryReader(r TreasuryReader) { h.treasury = r }

// SetAPIUtilizationReader wires the API-utilization source. Unset → utilization unreadable →
// the API-util guard fails CLOSED: a mis-wired coordinator holds concurrency growth rather
// than silently permitting unbounded growth into a saturated API.
func (h *RunFleetAutosizerCoordinatorHandler) SetAPIUtilizationReader(r APIUtilizationReader) {
	h.apiUtil = r
}

// SetYardPriceReader wires the shipyard price source. Unset → price unreadable → the price
// guards fail closed.
func (h *RunFleetAutosizerCoordinatorHandler) SetYardPriceReader(r YardPriceReader) { h.yardPrice = r }

// SetHeavyCensusReader wires the tag-independent owned-heavy census (heavy_cap's input).
func (h *RunFleetAutosizerCoordinatorHandler) SetHeavyCensusReader(r HeavyCensusReader) {
	h.heavyCensus = r
}

// SetHeavyYardReader wires the cheapest-known-heavy-price read (the reservation's input).
func (h *RunFleetAutosizerCoordinatorHandler) SetHeavyYardReader(r HeavyYardReader) { h.heavyYard = r }

// SetHeavyYardCatalogReader wires the known-heavy-yard catalogue — the ONE read that can see
// availability-only (unpriced) rows, which is what the pricing errand acts on. Unset ⇒ no errand.
func (h *RunFleetAutosizerCoordinatorHandler) SetHeavyYardCatalogReader(r HeavyYardCatalogReader) {
	h.heavyYardCatalog = r
}

// SetHeavyPricingErrandPort wires the pricing errand's fleet read and dispatch. Unset ⇒ no errand,
// so an unpriced heavy yard simply stays unpriced (the pre-errand behaviour).
func (h *RunFleetAutosizerCoordinatorHandler) SetHeavyPricingErrandPort(p HeavyPricingErrandPort) {
	h.heavyErrand = p
}

// SetPurchaser wires the buy+dedicate collaborator. Unset → the coordinator evaluates and
// logs but never spends, which is a MIS-WIRE and is surfaced loudly and by the zero-effect alarm.
func (h *RunFleetAutosizerCoordinatorHandler) SetPurchaser(p Purchaser) { h.purchaser = p }

// SetPurchaseNotifier wires the captain purchase-notice channel. Optional.
func (h *RunFleetAutosizerCoordinatorHandler) SetPurchaseNotifier(n PurchaseNotifier) { h.notifier = n }

// SetMetricsSink wires the metrics recorder. Optional and nil-safe (pure observation).
func (h *RunFleetAutosizerCoordinatorHandler) SetMetricsSink(m MetricsSink) { h.metrics = m }

// SetStallObserver wires the coordinator-stall escalation seam. Optional and nil-safe: an
// unwired observer simply reports nothing, because observability is never a precondition for
// sizing the fleet. The seam is write-only by type (its one method returns nothing), so wiring it
// cannot give any sizing decision something new to branch on.
func (h *RunFleetAutosizerCoordinatorHandler) SetStallObserver(o health.StallObserver) { h.stall = o }

// Handle runs the reconcile loop until the context is cancelled.
func (h *RunFleetAutosizerCoordinatorHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	logger := common.LoggerFromContext(ctx)

	cmd, ok := request.(*RunFleetAutosizerCoordinatorCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	cfg := resolveFleetAutosizerConfig(cmd)
	logger.Log("INFO", fmt.Sprintf("Fleet autosizer starting (tick %s)", cfg.Tick), map[string]interface{}{
		"action":       "autosizer_start",
		"container_id": cmd.ContainerID,
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
// and unconditionally run; explorer is opt-IN (only runs when armed).
func (c autosizerRunConfig) classDisabled(class HullClass) bool {
	switch class {
	case HullClassLight, HullClassHeavy:
		return false
	case HullClassExplorer:
		// Opt-IN arming: the explorer class runs ONLY when explicitly armed, so a bare
		// deploy skips it entirely and buys no ~819k ROI-exempt hull.
		return !c.ExplorerHullsEnabled
	default:
		// sp-y2ptq: HullClassContractDelivery (the autosizer's removed contract-scaling class) now
		// falls here — never sized by the autosizer (the dedicated scaler owns it). "unknown class: never act".
		return true
	}
}

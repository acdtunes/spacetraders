package commands

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/health"
	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
	"github.com/andrescamacho/spacetraders-go/internal/domain/hullbuy"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

const (
	// Config defaults (RULINGS #5: every operational value is a config key, filled here only
	// when the launch config leaves it unset — the Analyst/Admiral own the numbers). Documented
	// on config.FleetAutosizerConfig.
	defaultAutosizerTickSeconds = 900 // 15min — sizing is strategic, not per-second
	defaultPurchaseCapPerTick   = 1

	// There are no per-class pool ceilings — see purchase_guards.go for what bounds each
	// class.

	defaultPurchaseMarginOverFloor     = 200000
	defaultLightRotationSlots          = 3.5
	defaultHeavyUnservedLanesMin       = 3
	defaultHeavyTreasuryPctPerPurchase = 25
	// defaultHeavyCap is the ONLY count-based bound on any class. It is the SHARED constant
	// (hullbuy.DefaultHeavyCap), not a copy: sensing's heavy reservation falls back to the same
	// value, and two hand-copied literals is how the withholder and the spender end up saving
	// toward different caps. An explicit 0 in config.yaml is a legitimate operator HOLD, which is
	// why the config field is a *int (see config.FleetAutosizerConfig.HeavyCap).
	defaultHeavyCap                  = hullbuy.DefaultHeavyCap
	defaultAPIUtilCeilingPct         = 85
	defaultMaxPremiumOverCheapestPct = 50
	defaultZeroEffectAlarmTicks      = 4

	// Default shipyard ship-type symbols per class (RULINGS #5: even the asset is a knob).
	defaultShipTypeLights  = "SHIP_LIGHT_HAULER"
	defaultShipTypeHeavies = "SHIP_HEAVY_FREIGHTER"

	// The autosizer carries no contract-delivery class defaults — the dedicated scaler owns
	// contract-fleet capacity. The HullClassContractDelivery enum + the
	// "contract" dedication mapping the scaler reuses stay.
)

// DemandParams carries the live-resolved config the demand providers need each tick (rotation
// slots, etc.). The coordinator fills it from its runConfig so the providers, constructed once at
// boot, still see the current config.yaml value (the live-config discipline) without
// holding config themselves.
type DemandParams struct {
	// LightRotationSlots is the C3 rotation divisor inverted: K chains need K × this workers.
	LightRotationSlots float64
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

	// No contract-delivery class knobs here: the dedicated scaler owns contract capacity.
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

// classDisabled reports whether a class is frozen by config. The LIGHT class is the only one this
// coordinator still sizes.
//
// THE HEAVY CLASS IS DISABLED HERE: the fleet-growth coordinator owns trade capacity, and two
// coordinators buying into one pool against one treasury would each judge affordability without
// seeing the other's spend. It is disabled in the same change that gives the growth coordinator its
// heavy buy path and moves the cap declaration to it, because any gap between those leaves either
// two heavy buyers or none.
//
// HullClassContractDelivery falls to the same arm — never sized here, the dedicated scaler owns it.
// "unknown class: never act".
func (c autosizerRunConfig) classDisabled(class HullClass) bool {
	switch class {
	case HullClassLight:
		return false
	default:
		return true
	}
}

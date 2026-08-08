package commands

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/health"
	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
	"github.com/andrescamacho/spacetraders-go/internal/domain/fleetgrowth"
	"github.com/andrescamacho/spacetraders-go/internal/domain/hullbuy"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

const (
	defaultGrowthTickSeconds = 900
	// defaultGrowthUnservedLanesMin is the ANTI-THRASH streak on the heavy BUY: the shortfall must
	// persist this many ticks before a large hull is bought. On the purchase and NOT on the wave —
	// a spurious wave costs one paused probe tick, a spurious purchase costs a hull.
	defaultGrowthUnservedLanesMin = 3
	// defaultGrowthRunwayMilliHours is how many MILLI-hours of the trading fleet's cargo runway a
	// heavy purchase holds back above the immutable reserve. Milli, not float: sub-hour runway is the
	// operating range and a float would put NaN inside a money guard.
	defaultGrowthRunwayMilliHours = 2000

	defaultGrowthPurchaseMarginOverFloor   = 200000
	defaultGrowthAPIUtilCeilingPct         = 85
	defaultGrowthMaxPremiumOverCheapestPct = 50
	defaultGrowthZeroEffectAlarmTicks      = 4
	defaultGrowthShipTypeHeavies           = "SHIP_HEAVY_FREIGHTER"
	// defaultGrowthHeavyCap is the SHARED constant, not a copy: two hand-copied literals is how the
	// withholder and the spender end up saving toward different caps.
	defaultGrowthHeavyCap = hullbuy.DefaultHeavyCap
	// growthPurchaseCapPerTick bounds the coordinator to one hull per tick: a tick may commit at most
	// one large purchase before the next re-reads the treasury it just spent from.
	growthPurchaseCapPerTick = 1
)

// RunFleetGrowthCoordinatorCommand launches the standing fleet-growth coordinator for a player.
// Like the autosizer/siting coordinators it runs an infinite reconcile loop inside a single
// Handle() call; the container wraps it. Every knob is a launch-config key (RULINGS #5) and the
// zero value falls back to the documented default, so the daemon passes only what it overrides.
type RunFleetGrowthCoordinatorCommand struct {
	PlayerID    int
	ContainerID string
	AgentSymbol string

	TickIntervalSecs int
	// HeavyCap is the heavy-HULL cap. *int so an explicit 0 (operator hold) is told from unset.
	HeavyCap                  *int
	UnservedLanesMin          int
	RunwayMilliHours          int
	PurchaseMarginOverFloor   int64
	TreasuryPctPerPurchase    int
	APIUtilizationCeilingPct  int
	MaxPriceHeavies           int64
	MaxPremiumOverCheapestPct int
	PreferDemandProximalYard  *bool
	ShipTypeHeavies           string
	ZeroEffectAlarmTicks      int
}

// RunFleetGrowthCoordinatorResponse reports reconcile progress. Because the loop is infinite it is
// only observed on context cancellation (shutdown).
type RunFleetGrowthCoordinatorResponse struct {
	Ticks  int
	Errors []string
}

// UnservedLaneReader is the trade surface in the two dimensions the regime judges: the lane COUNT
// beyond the trade pool, and the DEPTH verdict answering it. readable=false ⇒ PROBE and no heavy
// bought (fail-closed on the buy, release on the wave — both spend nothing). Returning both together
// is what stops a consumer judging a regime the drain, sharing the read, never saw.
type UnservedLaneReader interface {
	LaneDemand(ctx context.Context, playerID int) (demand fleetgrowth.LaneDemand, readable bool, err error)
}

// CargoOutflowReader reports ONE trailing window of the trading fleet's cargo ledger — spend,
// recovery, largest purchase, seen-whole — because spend alone measures turnover, not commitment.
type CargoOutflowReader interface {
	CargoOutflowSince(ctx context.Context, playerID int, since time.Time) (fleetgrowth.CargoOutflow, error)
}

// TreasuryHighWaterReader reports the fleet's demonstrated capacity: the highest balance held across
// a full trade cycle. readable=false yields PROBE, never a zero.
//
// IT MUST BE A PEAK OVER THE WINDOW, NEVER A POINT READ. The ruled property — that the regime is a
// function of the fleet, not of where in a trade cycle the tick landed — is carried by nothing but
// which port lands here, and a live balance wired in here passes every test of the predicate.
type TreasuryHighWaterReader interface {
	TreasuryHighWaterSince(ctx context.Context, playerID shared.PlayerID, since time.Time) (int64, bool, error)
}

// TradeHullCounter is the trade-DEDICATED pool count — the multiplier on the hold-fill term and the
// current side of heavy demand. Deliberately tag-scoped, not the broad heavy census: the term asks
// what the TRADING fleet is about to spend, and a heavy tagged elsewhere is not flying a lane.
type TradeHullCounter interface {
	TradeHulls(ctx context.Context, playerID int) (int, error)
}

// GrowthMetricsSink records the coordinator's observation series (pure observation; nil-safe).
type GrowthMetricsSink interface {
	// RecordWave publishes the regime and, on PROBE, which clause forced it. Emitted EVERY tick on
	// EVERY path including the paused one: a HEAVY wave and a stalled coordinator both look like
	// "no probes bought", and only an always-present series tells them apart.
	RecordWave(playerID string, wave common.Wave, reason common.WaveProbeReason)
	RecordGrowthEnabled(playerID string, enabled bool)
	RecordHeavyReserve(playerID string, reserve, target int64, owned, capacity int)
	// RecordWorkingCapital publishes the reserve AND both arms: a total cannot name the measure.
	RecordWorkingCapital(playerID string, terms fleetgrowth.WorkingCapitalTerms)
	RecordDemand(class HullClass, demand, current int)
	RecordPurchase(class HullClass)
	RecordBlocked(class HullClass, guard GuardName)
	RecordZeroEffectAlarm()
	ObserveHeavyPricePremium(playerID string, paid, cheapestKnown int64)
}

// RunFleetGrowthCoordinatorHandler owns the fleet's heavy buying. Each tick it derives the wave
// from durable facts through common.DeriveWave — the ONE definition the sensing drain also reads
// — and, on a HEAVY wave only, runs one heavy candidate through the package's fail-CLOSED
// purchase guard stack.
//
// Registered singleton (one instance serves every player's ticks), so every decision input is
// read fresh from the ports each pass; the only in-memory state is edge-trigger bookkeeping,
// keyed by container ID so it stays per-coordinator.
type RunFleetGrowthCoordinatorHandler struct {
	clock shared.Clock

	// Buy-path collaborators, wired by setters at boot. Every one is nil-safe: a nil reader
	// yields an unreadable input, and the guard stack fails CLOSED on that.
	treasury    TreasuryReader
	apiUtil     APIUtilizationReader
	yardPrice   YardPriceReader
	heavyCensus HeavyCensusReader
	heavyYard   HeavyYardReader
	// heavyYardCatalog and heavyErrand drive the heavy-yard PRICING ERRAND: a shipyard prices its
	// hulls only while a ship stands there, so a known-but-unpriced heavy yard needs a hull sent
	// to it before any reservation — or any wave — can ever form. Both nil-safe: unwired ⇒ no
	// errand.
	heavyYardCatalog HeavyYardCatalogReader
	heavyErrand      HeavyPricingErrandPort
	// lanes is the capacity-short signal; outflow, tradeHulls and highWater are the three reads
	// the wave and the working-capital term are derived from.
	lanes      UnservedLaneReader
	outflow    CargoOutflowReader
	tradeHulls TradeHullCounter
	highWater  TreasuryHighWaterReader
	// growthCfg is the per-tick live-config snapshot for the live-tunable knobs. nil ⇒
	// launch-frozen behaviour.
	growthCfg liveconfig.Reader
	purchaser Purchaser
	notifier  PurchaseNotifier
	metrics   GrowthMetricsSink
	// stall is the WRITE-ONLY stall-escalation seam: a coordinator that refuses every tick must
	// not look identical to one with nothing to do. Its single method returns nothing, so no
	// decision here can read the streak it accumulates (RULINGS #2).
	stall health.StallObserver

	mu    sync.Mutex
	state map[string]*growthState
}

// growthState is the per-coordinator in-memory edge-trigger bookkeeping.
type growthState struct {
	// heavyShortfallStreak counts consecutive ticks the unserved-lane shortfall has persisted.
	// In-memory and per-container: it is edge-trigger bookkeeping, not operational state, and it
	// is deliberately NOT on the wave predicate, which two containers must agree on and
	// therefore cannot hold a streak for.
	heavyShortfallStreak int
	noEffectStreak       int
	noEffectPaged        bool
}

// NewRunFleetGrowthCoordinatorHandler wires the coordinator. clock defaults to the real clock when
// nil (production); every collaborator is added with its setter.
func NewRunFleetGrowthCoordinatorHandler(clock shared.Clock) *RunFleetGrowthCoordinatorHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &RunFleetGrowthCoordinatorHandler{
		clock: clock,
		state: make(map[string]*growthState),
	}
}

// SetTreasuryReader wires the live-treasury source. Unset → treasury unreadable → the treasury
// guards fail closed (no buy).
func (h *RunFleetGrowthCoordinatorHandler) SetTreasuryReader(r TreasuryReader) { h.treasury = r }

// SetAPIUtilizationReader wires the API-utilization source. Unset → unreadable → the API-util
// guard fails CLOSED: a mis-wired coordinator holds concurrency growth rather than permitting it.
func (h *RunFleetGrowthCoordinatorHandler) SetAPIUtilizationReader(r APIUtilizationReader) {
	h.apiUtil = r
}

// SetYardPriceReader wires the shipyard price source. Unset → price unreadable → the price guard
// fails closed.
func (h *RunFleetGrowthCoordinatorHandler) SetYardPriceReader(r YardPriceReader) { h.yardPrice = r }

// SetHeavyCensusReader wires the tag-independent owned-heavy census (heavy_cap's input).
func (h *RunFleetGrowthCoordinatorHandler) SetHeavyCensusReader(r HeavyCensusReader) {
	h.heavyCensus = r
}

// SetHeavyYardReader wires the heavy-target read behind the reservation's price term.
func (h *RunFleetGrowthCoordinatorHandler) SetHeavyYardReader(r HeavyYardReader) { h.heavyYard = r }

// SetHeavyYardCatalogReader wires the known-heavy-yard catalogue — the ONE read that can see
// availability-only (unpriced) rows, which is what the pricing errand acts on. Unset ⇒ no errand.
func (h *RunFleetGrowthCoordinatorHandler) SetHeavyYardCatalogReader(r HeavyYardCatalogReader) {
	h.heavyYardCatalog = r
}

// SetHeavyPricingErrandPort wires the pricing errand's fleet read and dispatch. Unset ⇒ no errand,
// so an unpriced heavy yard simply stays unpriced and no wave can ever turn HEAVY from it.
func (h *RunFleetGrowthCoordinatorHandler) SetHeavyPricingErrandPort(p HeavyPricingErrandPort) {
	h.heavyErrand = p
}

// SetUnservedLaneReader wires the capacity-short signal. Unset → unreadable → PROBE and no buy.
func (h *RunFleetGrowthCoordinatorHandler) SetUnservedLaneReader(r UnservedLaneReader) {
	h.lanes = r
}

// SetCargoOutflowReader wires the ledger-derived cargo-outflow read behind the working-capital
// term. Unset → the term is unreadable → no buy.
func (h *RunFleetGrowthCoordinatorHandler) SetCargoOutflowReader(r CargoOutflowReader) {
	h.outflow = r
}

// SetTradeHullCounter wires the trade-dedicated pool count.
func (h *RunFleetGrowthCoordinatorHandler) SetTradeHullCounter(c TradeHullCounter) {
	h.tradeHulls = c
}

// SetTreasuryHighWaterReader wires the demonstrated-capacity read the reachability clause judges.
// Unset → unreadable → PROBE, never a zero peak (which would be the strong claim "this fleet has
// never held money").
func (h *RunFleetGrowthCoordinatorHandler) SetTreasuryHighWaterReader(r TreasuryHighWaterReader) {
	h.highWater = r
}

// SetPurchaser wires the buy+dedicate collaborator. Unset → the coordinator evaluates and logs but
// never spends, which is a MIS-WIRE and is surfaced loudly and by the zero-effect alarm.
func (h *RunFleetGrowthCoordinatorHandler) SetPurchaser(p Purchaser) { h.purchaser = p }

// SetPurchaseNotifier wires the captain purchase-notice channel. Optional.
func (h *RunFleetGrowthCoordinatorHandler) SetPurchaseNotifier(n PurchaseNotifier) { h.notifier = n }

// SetMetricsSink wires the metrics recorder. Optional and nil-safe (pure observation).
func (h *RunFleetGrowthCoordinatorHandler) SetMetricsSink(m GrowthMetricsSink) { h.metrics = m }

// SetStallObserver wires the coordinator-stall escalation seam. Optional and nil-safe:
// observability is never a precondition for growing the fleet.
func (h *RunFleetGrowthCoordinatorHandler) SetStallObserver(o health.StallObserver) { h.stall = o }

// Handle runs the reconcile loop until the context is cancelled.
func (h *RunFleetGrowthCoordinatorHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	logger := common.LoggerFromContext(ctx)

	cmd, ok := request.(*RunFleetGrowthCoordinatorCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	cfg := resolveFleetGrowthConfig(cmd)
	logger.Log("INFO", fmt.Sprintf("Fleet growth starting (tick %s)", cfg.Tick), map[string]interface{}{
		"action":       "growth_start",
		"container_id": cmd.ContainerID,
	})

	result := &RunFleetGrowthCoordinatorResponse{Errors: []string{}}

	for {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

		if _, err := h.reconcileOnce(ctx, cmd); err != nil {
			result.Errors = append(result.Errors, err.Error())
			logger.Log("ERROR", fmt.Sprintf("Fleet growth reconcile failed: %v", err), nil)
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
func (h *RunFleetGrowthCoordinatorHandler) coordinatorState(containerID string) *growthState {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := h.state[containerID]
	if st == nil {
		st = &growthState{}
		h.state[containerID] = st
	}
	return st
}

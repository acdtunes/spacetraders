package commands

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	tradingsvc "github.com/andrescamacho/spacetraders-go/internal/application/trading/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// warehouseCapState is one warehouse's carried optimizer state between passes: the EWMA
// smoothed demand and the last-selected per-good targets (the incumbent set the hysteresis
// dead-band protects).
type warehouseCapState struct {
	smoothed map[string]float64
	targets  map[string]int
}

// NewRunStockerCoordinatorHandler wires the stocker with the same driven ports as the
// trade-route circuit (so buys/navigation resolve to the daemon's exact command
// handlers) plus the storage subsystem (deposit protocol + warehouse reads), the
// warehouse-op finder, and the Lane A demand miner. cfg carries the pre-positioning
// economics (min-recurrence/min-savings/allow-block, from cfg.Contract.PrePositioning);
// ceilingPct is the capital-ceiling percent of live treasury (default 10). A nil clock
// defaults to RealClock inside the delegated handler.
func NewRunStockerCoordinatorHandler(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	marketRepo market.MarketRepository,
	marketRefresher MarketRefresher,
	clock shared.Clock,
	apiClient domainPorts.APIClient,
	storageCoordinator storage.StorageCoordinator,
	warehouseFinder tradingsvc.WarehouseOperationFinder,
	demandMiner tradingsvc.DepositDemandMiner,
	cfg tradingsvc.DepositCandidateConfig,
	ceilingPct int,
	waypointRepo system.WaypointRepository,
) *RunStockerCoordinatorHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &RunStockerCoordinatorHandler{
		legs:               NewRunTradeRouteCoordinatorHandler(mediator, shipRepo, marketRepo, marketRefresher, clock, apiClient),
		mediator:           mediator,
		marketRepo:         marketRepo,
		apiClient:          apiClient,
		clock:              clock,
		storageCoordinator: storageCoordinator,
		warehouseFinder:    warehouseFinder,
		demandMiner:        demandMiner,
		config:             cfg,
		ceilingPct:         ceilingPct,
		waypointRepo:       waypointRepo,
		noReachableSource:  make(map[string]string),
		capState:           make(map[string]*warehouseCapState),
	}
}

// SetWarehouseCapParams injects the analyst-owned auto-cap tunables (EWMA half-life,
// value-formula weights, hysteresis margin, cold-start threshold, cross-system residual —
// RULINGS #5). The daemon calls this at wiring time; unset, the optimizer uses its documented
// defaults. Mirrors SetGateGraph/SetEventSubscriber's optional-injection shape.
func (h *RunStockerCoordinatorHandler) SetWarehouseCapParams(p tradingsvc.WarehouseCapParams) {
	h.capParams = p
}

// resolveWarehouseCaps runs the auto-cap knapsack for the co-located warehouse group and
// returns the per-good target_units to enforce this pass, or nil to defer to the pre-existing
// per-good target (measured demand / TargetPerGood override). It STANDS ASIDE (nil) on a cold
// start — too little demand history to trust computed caps — so a thin-data run degrades to
// the proven behavior rather than churning on noise; the warehouse's own cold-start caps
// (populated at StartWarehouse) still bound it. Capacity is Σ REAL hull cargo_capacity across
// the group (never assume-80). EWMA + hysteresis state is carried per warehouse waypoint.
func (h *RunStockerCoordinatorHandler) resolveWarehouseCaps(ctx context.Context, homeSystem, waypoint string, group []*storage.StorageOperation, rows []persistence.DemandCandidate) map[string]int {
	capacity := tradingsvc.TotalCapacity(h.storageCoordinator, group)
	if capacity <= 0 {
		return nil
	}

	h.capStateMu.Lock()
	st := h.capState[waypoint]
	if st == nil {
		st = &warehouseCapState{}
	}
	prior, current := st.smoothed, st.targets
	h.capStateMu.Unlock()

	plan := tradingsvc.PlanWarehouseCaps(rows, capacity, homeSystem, waypoint, h.waypointCoords(ctx), prior, current, h.capParams)

	// Persist the advanced EWMA + selection for the next pass's stickiness. Only adopt the
	// computed targets as the incumbent set when it was a real (non-cold-start) solve.
	h.capStateMu.Lock()
	next := &warehouseCapState{smoothed: plan.Smoothed, targets: st.targets}
	if !plan.ColdStart {
		next.targets = plan.Targets
	}
	h.capState[waypoint] = next
	h.capStateMu.Unlock()

	if plan.ColdStart {
		return nil // defer to the pre-existing per-good target on thin history
	}
	return plan.Targets
}

// waypointCoords builds the coordinate lookup the auto-cap knapsack uses to turn the
// residual buy-leg into a real dist(warehouse, source). It is a CACHE-ONLY read (waypointRepo,
// never an API fetch-through) so the per-pass re-solve costs no API spend; a nil repo, an
// unresolvable waypoint, or a TTL-expired cache row returns ok=false and the optimizer FAILS OPEN
// to the coarse in/cross-system residual (RULINGS #1). A nil repo yields a nil lookup, degrading
// the solve to the previous binary proxy byte-for-byte.
func (h *RunStockerCoordinatorHandler) waypointCoords(ctx context.Context) tradingsvc.WaypointCoordsLookup {
	if h.waypointRepo == nil {
		return nil
	}
	return func(waypoint string) (float64, float64, bool) {
		wp, err := h.waypointRepo.FindBySymbol(ctx, waypoint, shared.ExtractSystemSymbol(waypoint))
		if err != nil || wp == nil {
			return 0, 0, false
		}
		return wp.X, wp.Y, true
	}
}

// SetGateGraph wires the multi-jump gate-graph resolver into the delegated movement
// handler (so travel crosses multi-hop gaps to reach a foreign market and haul home).
// Mirrors the arb/tour coordinator's injection.
func (h *RunStockerCoordinatorHandler) SetGateGraph(g GateGraph) {
	h.legs.SetGateGraph(g)
}

// SetChartGateOnArrival propagates the chart-on-gate-arrival knob to the movement
// legs, so this coordinator's cross-system stock-haul arrivals chart the gate they land on
// too. Mirrors the SetGateGraph delegation.
func (h *RunStockerCoordinatorHandler) SetChartGateOnArrival(enabled bool) {
	h.legs.SetChartGateOnArrival(enabled)
}

// SetTreasuryReader wires the shared ledger-backed treasury reader into this
// coordinator's capital ceiling AND into its MOVEMENT LEGS, which run the buy-time
// working-capital floor. The legs are this handler's OWN RunTradeRouteCoordinatorHandler
// instance (the constructor builds one; the daemon passes nil), NOT the daemon's separately
// wired circuit handler — so a setter that stopped here would leave half the stocker path
// still calling Get Agent. Same delegation the tour coordinator's setter makes.
func (h *RunStockerCoordinatorHandler) SetTreasuryReader(r TreasuryReader) {
	h.treasury = r
	h.legs.SetTreasuryReader(r)
}

// SetEventSubscriber wires the ship-arrival event bus into the delegated movement
// handler so the resume path waits out a hull re-adopted mid-transit before moving
// (sp-8l3o) instead of 4214'ing and burning the restart budget. Mirrors arb/tour.
func (h *RunStockerCoordinatorHandler) SetEventSubscriber(subscriber navigation.ShipEventSubscriber) {
	h.legs.SetEventSubscriber(subscriber)
}

// SetJumpTollRecorder forwards the measured-hop recorder into the delegated movement handler,
// so a stocker's cross-gate legs count toward the fleet's per-hop travel estimate. Nil is a
// no-op. Mirrors arb/tour.
func (h *RunStockerCoordinatorHandler) SetJumpTollRecorder(r trading.JumpTollRepository) {
	h.legs.SetJumpTollRecorder(r)
}

// SetStockingRecorder wires the stock-IN deposit-event recorder: on each CONFIRMED
// stocker→warehouse deposit the handler emits a structured storage.StockingEvent (good,
// units, warehouse, source market, hauler, player, timestamp) so downstream analysis can
// measure depot stock-IN throughput/coverage. A nil recorder is a no-op, so the daemon may
// forward the wiring unconditionally. Mirrors SetGateGraph/SetWarehouseCapParams's
// optional-injection shape; the event is stamped with the handler's own clock.
func (h *RunStockerCoordinatorHandler) SetStockingRecorder(recorder storage.StockingRecorder) {
	h.stockingRecorder = recorder
}

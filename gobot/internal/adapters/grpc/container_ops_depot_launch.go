package grpc

import (
	"context"
	"fmt"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	tradingsvc "github.com/andrescamacho/spacetraders-go/internal/application/trading/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/depot"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// This file is the destination-side STOCKING half of contract-depot warehousing (bead
// sp-cftm, completing sp-u9xa). sp-u9xa made a depot ROUTE a contract to its destination
// warehouse (RouteContract prefers the depot warehouse as a withdrawal source), but left the
// depot's declared stockers/warehouses INERT — nothing read .Stockers()/.Warehouses() to
// LAUNCH the coordinators that FILL the warehouse. So the warehouse never stocked and routing
// always fell through to the byte-identical fresh-source fallback: zero cycle-time compression.
//
// This is that missing half. Reading a loaded registry, it launches — per declared, crewed
// element — a warehouse coordinator on the warehouse hull and a stocker coordinator pointed at
// the depot's destination warehouse waypoint (the deposit anchor). The depot warehouse's
// cold-start cargo caps are gated on RECEIPT demand (PlanReceiptCaps — what the DESTINATION
// receives, valuing the far source→destination haul-leg the buffer moves onto parallel
// stockers) rather than the source-side PlanWarehouseCaps. It reuses the existing
// StartStocker / StartWarehouse launch path (no parallel channel) — the topology merely DRIVES
// which coordinators start and where they point.

// depotLaunchIntent is one coordinator to (idempotently) start for one crewed depot
// element. Every position is data carried from the topology — nothing here is hardcoded
// (sp-u9xa parametrization principle).
type depotLaunchIntent struct {
	depotID string
	role    depot.Role // RoleWarehouse | RoleStocker
	// shipSymbol is the crewing hull to fly (a warehouse hull, or a stocker hull).
	shipSymbol string
	// warehouseWaypoint is where the coordinator points: a warehouse parks at its OWN waypoint;
	// a stocker deposits into the depot's destination warehouse ANCHOR (warehouses[0]).
	warehouseWaypoint string
	// coLocatedWarehouseShips is the CO-LOCATED warehouse group this warehouse belongs to: every
	// crewed warehouse hull of the SAME depot parked at the SAME waypoint, including this one
	// (sp-5q2c/sp-64se). The receipt knapsack solves over the group's AGGREGATE cargo capacity
	// (Σ hull capacity) so the shared buffer spreads across the whitelist breadth, instead of
	// each hull independently deep-filling the same top goods over its own single-hull capacity.
	// Empty/nil for a stocker intent (only warehouses carry a co-located group).
	coLocatedWarehouseShips []string
}

// planDepotLaunches reads a registry and returns the coordinators to start: one warehouse
// per crewed warehouse element (parked at its own waypoint) and one stocker per crewed stocker
// element (all pointed at the depot's destination warehouse anchor as their deposit target).
// It is PURE — no I/O — so the launch DECISION is unit-tested without any container
// infrastructure. A declared-but-uncrewed slot (empty ShipSymbol — sized before a hull is
// pinned) yields no launch: there is no hull to fly yet. A nil/empty registry yields nothing
// (destination warehousing OFF — the regression-safe default).
func planDepotLaunches(reg *depot.Registry) []depotLaunchIntent {
	if reg == nil {
		return nil
	}
	var intents []depotLaunchIntent
	for _, c := range reg.Depots() {
		warehouses := c.Warehouses()
		if len(warehouses) == 0 {
			continue // NewContractDepot guarantees >=1, but never trust a mutated registry
		}
		anchor := warehouses[0].Waypoint // the routing anchor + shared deposit target
		// Co-located warehouse groups (sp-64se/sp-5q2c): every crewed warehouse hull sharing a
		// waypoint is one logical buffer whose receipt knapsack solves over their AGGREGATE
		// capacity. Group by waypoint so a warehouse carries its whole same-waypoint group;
		// warehouses at different waypoints stay separate (co-location is by waypoint).
		coLocatedByWaypoint := map[string][]string{}
		for _, w := range warehouses {
			if w.ShipSymbol == "" {
				continue
			}
			coLocatedByWaypoint[w.Waypoint] = append(coLocatedByWaypoint[w.Waypoint], w.ShipSymbol)
		}
		for _, w := range warehouses {
			if w.ShipSymbol == "" {
				continue // declared-but-uncrewed slot: no hull to fly yet
			}
			intents = append(intents, depotLaunchIntent{
				depotID:                 c.ID(),
				role:                    depot.RoleWarehouse,
				shipSymbol:              w.ShipSymbol,
				warehouseWaypoint:       w.Waypoint, // a warehouse parks at its own waypoint
				coLocatedWarehouseShips: coLocatedByWaypoint[w.Waypoint],
			})
		}
		for _, st := range c.Stockers() {
			if st.ShipSymbol == "" {
				continue
			}
			intents = append(intents, depotLaunchIntent{
				depotID:           c.ID(),
				role:              depot.RoleStocker,
				shipSymbol:        st.ShipSymbol,
				warehouseWaypoint: anchor, // every depot stocker deposits into the anchor
			})
		}
	}
	return intents
}

// depotCoordinatorSink is the driven-port boundary to the container-launch infrastructure:
// the two primitives the depot orchestrator dispatches to. *DaemonServer satisfies it by
// delegating to its existing StartWarehouse/StartStocker path (no parallel channel). Kept
// narrow + injectable so the orchestration is unit-tested against a spy without spawning
// container goroutines or requiring idle hulls in a DB.
type depotCoordinatorSink interface {
	launchDepotWarehouse(ctx context.Context, shipSymbol, warehouseWaypoint string, coLocatedWarehouseShips []string, playerID int) error
	launchDepotStocker(ctx context.Context, shipSymbol, warehouseWaypoint string, playerID int) error
}

// launchDepotCoordinators starts every coordinator a loaded registry declares, dispatching
// each planned intent to the sink. It is FAIL-OPEN and safely re-runnable: a per-element launch
// failure (most commonly a hull that is already flying its coordinator — a benign
// already-launched skip the sink returns as nil) is logged and stepped over so one bad element
// never blocks the rest, and a reboot re-runs it harmlessly (the sink's idle-gap discipline
// refuses a double-launch). It is the same shape as ensureBootStandingCoordinators.
func launchDepotCoordinators(ctx context.Context, reg *depot.Registry, playerID int, sink depotCoordinatorSink) {
	for _, intent := range planDepotLaunches(reg) {
		var err error
		switch intent.role {
		case depot.RoleWarehouse:
			err = sink.launchDepotWarehouse(ctx, intent.shipSymbol, intent.warehouseWaypoint, intent.coLocatedWarehouseShips, playerID)
		case depot.RoleStocker:
			err = sink.launchDepotStocker(ctx, intent.shipSymbol, intent.warehouseWaypoint, playerID)
		default:
			continue
		}
		if err != nil {
			fmt.Printf("Warning: depot %q %s launch for ship %s skipped: %v\n",
				intent.depotID, intent.role, intent.shipSymbol, err)
		}
	}
}

// depotWarehouseTargetUnits computes a DESTINATION-side depot warehouse's cold-start
// per-good cargo caps from RECEIPT demand (bead sp-cftm/sp-u9xa) — the sibling of the
// source-side warehouseTargetUnits, but routed through PlanReceiptCaps. It mines demand scoped
// to the DESTINATION system (what the depot's contracts RECEIVE), maps each ranked good to a
// ReceiptCandidate, and solves the receipt knapsack anchored on the destination warehouse
// waypoint: among received goods it buffers the ones whose SOURCE is far (the long
// source→destination haul-leg the buffer relocates onto parallel stockers, off the serialized
// contract critical path) over the near-sourced ones, subject to the real hull capacity.
//
// A nil miner or a mining error degrades to the empty candidate set (PlanReceiptCaps then
// returns the static cold-start caps clipped to capacity), so a depot warehouse always starts
// with a sane, capacity-respecting plan. Coordinates unavailable FAIL OPEN to the coarse
// in/cross-system residual (RULINGS #1).
func depotWarehouseTargetUnits(
	ctx context.Context,
	miner tradingsvc.DepositDemandMiner,
	capacity int,
	destinationSystem string,
	warehouseWaypoint string,
	coords tradingsvc.WaypointCoordsLookup,
	playerID int,
	params *tradingsvc.WarehouseCapParams,
) map[string]int {
	var p tradingsvc.WarehouseCapParams
	if params != nil {
		p = *params
	}

	var candidates []persistence.DemandCandidate
	if miner != nil {
		// Mine what the DESTINATION system RECEIVES (deliverySystem == destinationSystem): the
		// receipt-demand signal, not the source buy-leg.
		if rows, err := miner.Mine(ctx, destinationSystem, playerID, nil, persistence.DemandMinerOptions{}); err == nil {
			candidates = rows
		}
	}

	receipts := make([]tradingsvc.ReceiptCandidate, 0, len(candidates))
	for _, c := range candidates {
		receipts = append(receipts, tradingsvc.ReceiptCandidate{
			Good:             c.Good,
			ContractCount:    c.ContractCount,
			Payment:          depotReceiptPayment(c),
			SourceWaypoint:   c.ForeignMarket, // where the good is sourced (the far end of the haul-leg)
			SourceSystem:     c.ForeignSystem,
			MaxContractUnits: c.MaxContractUnits,
			DemandUnits:      c.DemandUnits,
		})
	}

	return tradingsvc.PlanReceiptCaps(receipts, capacity, destinationSystem, warehouseWaypoint, coords, nil, nil, p).Targets
}

// depotColocatedWarehouseTargets computes a co-located warehouse GROUP's shared receipt-demand
// caps over the group's AGGREGATE cargo capacity (bead sp-64se / sp-5q2c) — Σ CargoCapacity
// across every co-located warehouse hull, resolved through capacityOf. Solving the receipt
// knapsack once over the aggregate (e.g. 2×80 = 160) rather than once per hull over a single
// 80 is what lets the shared buffer COVER THE WHITELIST BREADTH: at 160 the 0/1-at-one-contract
// knapsack fits four ~40-unit goods instead of deep-filling the same top-two over 80. A single
// warehouse depot has a one-hull group, so its capacity is unchanged (no regression). capacityOf
// resolves each hull's real cargo capacity (never assume-80 — a heavy frame or cargo module
// simply raises the aggregate); an unresolvable hull contributes 0, so the group is at least
// this warehouse's own capacity (fail-open).
func depotColocatedWarehouseTargets(
	ctx context.Context,
	miner tradingsvc.DepositDemandMiner,
	coLocatedWarehouseShips []string,
	capacityOf func(shipSymbol string) int,
	destinationSystem string,
	warehouseWaypoint string,
	coords tradingsvc.WaypointCoordsLookup,
	playerID int,
	params *tradingsvc.WarehouseCapParams,
) map[string]int {
	capacity := 0
	for _, shipSymbol := range coLocatedWarehouseShips {
		capacity += capacityOf(shipSymbol)
	}
	return depotWarehouseTargetUnits(ctx, miner, capacity, destinationSystem, warehouseWaypoint, coords, playerID, params)
}

// depotReceiptPayment is the per-unit value signal for a received good in the receipt-demand
// knapsack (sp-64se). It ranks by the good's TRUE CONTRACT REWARD — what the destination's
// contracts actually PAY per delivered unit (ContractRewardPerUnit) — so the buffer pre-stages
// the high-contract-value goods, NOT the ones that merely resell dear. A market ask is only a
// RESALE proxy and MIS-RANKS import-only goods (a destination imports a good precisely because
// it does not produce it, yet may resell it high); it is kept solely as a FALLBACK for when no
// contract reward is known — the home ask first, else the source ask — so such a good still
// carries a non-zero value rather than being dropped.
func depotReceiptPayment(c persistence.DemandCandidate) float64 {
	if c.ContractRewardPerUnit > 0 {
		return c.ContractRewardPerUnit
	}
	if c.HomeAsk > 0 {
		return float64(c.HomeAsk) // fallback: resale proxy when contract reward is unavailable
	}
	return float64(c.ForeignAsk) // last-resort fallback: source ask keeps an import-only good rankable
}

// sortedGoods returns the goods in a caps map in deterministic order — the depot warehouse's
// supported-stock whitelist derived from its receipt-demand caps.
func sortedGoods(targetUnits map[string]int) []string {
	goods := make([]string, 0, len(targetUnits))
	for g := range targetUnits {
		goods = append(goods, g)
	}
	sort.Strings(goods)
	return goods
}

// depotReceiptMiner resolves the receipt-demand miner the depot warehouse cap (re-)solve
// runs on: the DB-backed miner in production, or a test override when one is injected
// (sp-94du). A nil DB (degraded/test) yields a nil miner, which PlanReceiptCaps degrades to
// the static cold-start caps clipped to capacity — the same fail-open as before.
func (s *DaemonServer) depotReceiptMiner() tradingsvc.DepositDemandMiner {
	if s.depotReceiptMinerOverride != nil {
		return s.depotReceiptMinerOverride
	}
	if s.db != nil {
		return persistence.NewDemandMiner(s.db)
	}
	return nil
}

// launchDepotWarehouse (depotCoordinatorSink) starts a destination-side depot warehouse
// on shipSymbol parked at warehouseWaypoint, with its cold-start cargo caps gated on RECEIPT
// demand (depotWarehouseTargetUnits -> PlanReceiptCaps) rather than the source-side
// selector. Its supported-stock whitelist is the receipt caps' goods. A hull that is not idle
// is already flying its coordinator — a benign already-launched skip (nil), never an error, so
// the boot re-run is quiet. It reuses persistAndRunWarehouse, so the container's persistence /
// claim / recovery path is byte-identical to a captain-launched warehouse.
func (s *DaemonServer) launchDepotWarehouse(ctx context.Context, shipSymbol, warehouseWaypoint string, coLocatedWarehouseShips []string, playerID int) error {
	if shipSymbol == "" || warehouseWaypoint == "" {
		return fmt.Errorf("depot warehouse launch requires a ship symbol and warehouse waypoint")
	}
	ship, err := s.shipRepo.FindBySymbol(ctx, shipSymbol, shared.MustNewPlayerID(playerID))
	if err != nil {
		return fmt.Errorf("failed to load depot warehouse hull %s: %w", shipSymbol, err)
	}
	if ship == nil {
		return fmt.Errorf("depot warehouse hull %s not found", shipSymbol)
	}
	// Re-solve the receipt caps with the CURRENT selector on EVERY reload (sp-94du) — BEFORE the
	// idle gate, so the gate governs only the coordinator LAUNCH, never the cap re-solve. A
	// redeployed selector must reach the buffer whether the hull is idle (fresh launch below) or
	// already flying its coordinator (running warehouse, refreshed in place). The prior code
	// returned here for a non-idle hull, so a cap change never reached a running warehouse: on
	// boot the hull is re-adopted by recovery (non-idle) and this recompute was skipped.
	miner := s.depotReceiptMiner()
	// Solve the receipt knapsack over the co-located warehouse GROUP's aggregate capacity
	// (sp-64se). capacityOf reads this already-loaded hull directly (no re-fetch) and resolves
	// any sibling hull's real cargo capacity through the ship repo; an unresolvable sibling
	// contributes 0, so the aggregate is at least this hull's own capacity (fail-open).
	group := coLocatedWarehouseShips
	if len(group) == 0 {
		group = []string{shipSymbol}
	}
	capacityOf := func(sym string) int {
		if sym == shipSymbol {
			return ship.CargoCapacity()
		}
		sibling, ferr := s.shipRepo.FindBySymbol(ctx, sym, shared.MustNewPlayerID(playerID))
		if ferr != nil || sibling == nil {
			return 0
		}
		return sibling.CargoCapacity()
	}
	targetUnits := depotColocatedWarehouseTargets(
		ctx, miner, group, capacityOf,
		shared.ExtractSystemSymbol(warehouseWaypoint), warehouseWaypoint,
		s.waypointCoords(ctx), playerID, nil,
	)
	supportedGoods := sortedGoods(targetUnits)
	if len(supportedGoods) == 0 {
		return fmt.Errorf("depot warehouse %s at %s: no receipt-demand goods to stock", shipSymbol, warehouseWaypoint)
	}

	if !ship.IsIdle() {
		// Already flying its coordinator: DON'T double-launch — re-apply the freshly recomputed
		// whitelist to the running warehouse's persisted row instead, so the redeployed selector
		// reaches it (the stocker re-reads supported_goods from the store each pass). The IsIdle
		// gate still refuses a second coordinator LAUNCH; only the cap re-solve is ungated.
		return s.refreshRunningDepotWarehouseCaps(ctx, shipSymbol, warehouseWaypoint, supportedGoods, playerID)
	}

	_, err = s.persistAndRunWarehouse(ctx, shipSymbol, warehouseWaypoint, supportedGoods, targetUnits, playerID)
	return err
}

// refreshRunningDepotWarehouseCaps re-applies a freshly-recomputed receipt whitelist to an
// ALREADY-RUNNING depot warehouse (sp-94du) WITHOUT launching a second coordinator. On boot,
// container recovery re-adopts the warehouse hull (now non-idle) and RESUMES its persisted
// storage_operations row with whatever whitelist it last carried; launchDepotWarehouse's idle
// gate then skips the (re)launch. But a redeployed cap selector must still reach the running
// buffer — and the stocker re-reads each warehouse's supported_goods from the store every pass
// (warehousesAt -> FindRunning), so persisting the fresh whitelist onto that row makes the
// redeploy live on the stocker's next tick, no container restart needed. It matches the running
// warehouse operation by waypoint + crewing hull (the container id carries a random UUID and is
// not reconstructible) and updates ONLY the supported_goods column, so the live status / ship
// registration are untouched. A hull with no running warehouse row yet (recovery still in
// flight) is a benign no-op — the next reload catches it. Fail-open on a nil DB (degraded/test):
// the idle skip simply stands.
func (s *DaemonServer) refreshRunningDepotWarehouseCaps(ctx context.Context, shipSymbol, warehouseWaypoint string, supportedGoods []string, playerID int) error {
	if s.db == nil {
		return nil
	}
	repo := persistence.NewStorageOperationRepository(s.db, s.clock)
	ops, err := repo.FindAllRunningByWaypoint(ctx, playerID, warehouseWaypoint)
	if err != nil {
		return fmt.Errorf("depot warehouse %s at %s: failed to load running warehouse for cap refresh: %w", shipSymbol, warehouseWaypoint, err)
	}
	for _, op := range ops {
		if op.OperationType() != storage.OperationTypeWarehouse {
			continue
		}
		if !hullCrewsOperation(op.StorageShips(), shipSymbol) {
			continue
		}
		if err := repo.UpdateSupportedGoods(ctx, op.ID(), supportedGoods); err != nil {
			return fmt.Errorf("depot warehouse %s at %s: failed to persist recomputed caps: %w", shipSymbol, warehouseWaypoint, err)
		}
		return nil
	}
	return nil // no running warehouse row for this hull yet — recovery in flight; the next reload catches it
}

// hullCrewsOperation reports whether shipSymbol is one of a warehouse operation's storage hulls
// — the join that pairs a reload intent's hull to its running storage_operations row.
func hullCrewsOperation(storageShips []string, shipSymbol string) bool {
	for _, s := range storageShips {
		if s == shipSymbol {
			return true
		}
	}
	return false
}

// launchDepotStocker (depotCoordinatorSink) starts a STANDING, continuous stocker on
// shipSymbol that fills the depot's destination warehouse (warehouseWaypoint) and re-stages
// the moment contracts drain the buffer, surviving restart. It leaves every money/freshness
// knob at the coordinator's own default (targetPerGood 0 → the warehouse's receipt caps drive
// the fill). A hull that is not idle is already flying its coordinator — a benign
// already-launched skip (nil), never an error. It reuses StartStocker (no parallel channel).
func (s *DaemonServer) launchDepotStocker(ctx context.Context, shipSymbol, warehouseWaypoint string, playerID int) error {
	if shipSymbol == "" || warehouseWaypoint == "" {
		return fmt.Errorf("depot stocker launch requires a ship symbol and warehouse waypoint")
	}
	ship, err := s.shipRepo.FindBySymbol(ctx, shipSymbol, shared.MustNewPlayerID(playerID))
	if err != nil {
		return fmt.Errorf("failed to load depot stocker hull %s: %w", shipSymbol, err)
	}
	if ship == nil {
		return fmt.Errorf("depot stocker hull %s not found", shipSymbol)
	}
	if !ship.IsIdle() {
		return nil // already flying its coordinator — benign already-launched skip
	}

	_, err = s.StartStocker(
		ctx, shipSymbol, warehouseWaypoint,
		0,    // budgetPerLeg → coordinator default (capital ceiling + reserve still bind)
		0,    // workingCapitalReserve → 50k default
		-1,   // iterations: CONTINUOUS
		0,    // maxMarketAgeMinutes → 75 default
		0,    // targetPerGood → the warehouse's receipt caps drive the fill
		true, // standing: re-stage on drain, survive restart
		0,    // tickSeconds → 30s default
		0,    // refillHysteresis → default
		"",   // agentSymbol resolved by the coordinator
		playerID,
	)
	return err
}

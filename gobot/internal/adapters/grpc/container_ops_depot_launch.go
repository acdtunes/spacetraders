package grpc

import (
	"context"
	"fmt"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	tradingsvc "github.com/andrescamacho/spacetraders-go/internal/application/trading/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/depot"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
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
	role    depot.Role // RoleWarehouse | RoleStocker | RoleDeliveryHull | RoleSourceHub
	// shipSymbol is the crewing hull to fly (a warehouse hull, a stocker hull, a delivery hull, or a source-hub hull).
	shipSymbol string
	// targetWaypoint is where the element is anchored: a warehouse parks at its OWN waypoint; a
	// stocker deposits into the depot's destination warehouse ANCHOR (warehouses[0]); a delivery
	// hull parks at its OWN hub waypoint (sp-9j9c) to wait for the contract coordinator's dispatch;
	// a source hub parks its crewing hull at its OWN market waypoint (sp-3l64).
	targetWaypoint string
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
				targetWaypoint:          w.Waypoint, // a warehouse parks at its own waypoint
				coLocatedWarehouseShips: coLocatedByWaypoint[w.Waypoint],
			})
		}
		for _, st := range c.Stockers() {
			if st.ShipSymbol == "" {
				continue
			}
			intents = append(intents, depotLaunchIntent{
				depotID:        c.ID(),
				role:           depot.RoleStocker,
				shipSymbol:     st.ShipSymbol,
				targetWaypoint: anchor, // every depot stocker deposits into the anchor
			})
		}
		// sp-9j9c #2: place each crewed delivery hull at its OWN hub waypoint. This is what makes
		// the topology's multi-hub delivery fleet no longer inert — the hulls are positioned across
		// hubs so the nearest-selection router (#1) can route each cluster's contract to its local
		// hull. A declared-but-uncrewed slot yields no launch (no hull to fly yet), matching the
		// warehouse/stocker discipline.
		for _, dh := range c.DeliveryHulls() {
			if dh.ShipSymbol == "" {
				continue
			}
			intents = append(intents, depotLaunchIntent{
				depotID:        c.ID(),
				role:           depot.RoleDeliveryHull,
				shipSymbol:     dh.ShipSymbol,
				targetWaypoint: dh.Waypoint, // a delivery hull parks at its own hub waypoint
			})
		}
		// sp-3l64 (role-agnostic positioning): position each crewed source-hub hull at its OWN
		// market waypoint. A source hub has no standing coordinator (it feeds the stockers as a
		// buy anchor), so — like a delivery hull — its assignment is a one-shot free+exclude+park:
		// the crewing hull is freed from any prior fleet, excluded from the contract grab, and
		// navigated to the hub, instead of drifting off-config. An uncrewed slot yields no launch.
		for _, sh := range c.SourceHubs() {
			if sh.ShipSymbol == "" {
				continue // declared-but-uncrewed slot: no hull to fly yet
			}
			intents = append(intents, depotLaunchIntent{
				depotID:        c.ID(),
				role:           depot.RoleSourceHub,
				shipSymbol:     sh.ShipSymbol,
				targetWaypoint: sh.Waypoint, // a source hub parks its hull at its own market waypoint
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
	// launchDepotDelivery POSITIONS a delivery hull at its hub waypoint (sp-9j9c). Unlike the
	// standing warehouse/stocker coordinators it is a one-shot reposition — the hull waits idle
	// at its hub for the contract coordinator to dispatch it on demand.
	launchDepotDelivery(ctx context.Context, shipSymbol, hubWaypoint string, playerID int) error
	// launchDepotSourceHub POSITIONS a source-hub hull at its market waypoint (sp-3l64). Like the
	// delivery hull it is a one-shot free+exclude+park (a source hub has no standing coordinator);
	// unlike it, the parked hull is not dispatched — it holds the buy anchor for the stockers.
	launchDepotSourceHub(ctx context.Context, shipSymbol, hubWaypoint string, playerID int) error
	// homeContractWorkerReserve is the reserve-floor census the delivery-hull launch consults so it
	// never pins the LAST undedicated home general haulers the contract coordinator needs to source
	// an UNBUFFERED-good contract (bead sp-mzdk): the current pool size, the configured floor to
	// keep, and which ships are in the pool right now. *DaemonServer computes it from the ship repo
	// + the live min_home_contract_workers knob; a spy returns a canned budget. A zero value reserves
	// nothing (regression-safe: the pre-sp-mzdk pin-everything behavior).
	homeContractWorkerReserve(ctx context.Context, reg *depot.Registry, playerID int) deliveryPinBudget
}

// depotSourceHubFleet is the DedicatedFleet tag a depot source-hub hull carries (sp-3l64). Like
// depot.DeliveryHullFleet it is DISTINCT from the contract coordinator's "contract" fleet, so a
// parked source-hub hull is invisible to BOTH pools the coordinator draws from and can never be
// re-grabbed off its market anchor. A source hub has no coordinator of its own, so — unlike
// warehouse/stocker, which re-dedicate to their coordinator's own tag — it uses this depot-owned tag.
const depotSourceHubFleet = "depot-source-hub"

// launchDepotCoordinators starts every coordinator a loaded registry declares, dispatching
// each planned intent to the sink. It is FAIL-OPEN and safely re-runnable: a per-element launch
// failure (most commonly a hull that is already flying its coordinator — a benign
// already-launched skip the sink returns as nil) is logged and stepped over so one bad element
// never blocks the rest, and a reboot re-runs it harmlessly (the sink's idle-gap discipline
// refuses a double-launch). It is the same shape as ensureBootStandingCoordinators.
func launchDepotCoordinators(ctx context.Context, reg *depot.Registry, playerID int, sink depotCoordinatorSink) {
	intents := planDepotLaunches(reg)
	// Reserve floor (sp-mzdk): before pinning, consult the census of undedicated home general
	// haulers and RESERVE min_home_contract_workers of them so an unbuffered-good contract always
	// has a general sourcing worker. A reserved delivery hull is left undedicated + home + available
	// to the contract grab — not pinned to its hub.
	reserved := reserveHomeContractWorkers(intents, sink.homeContractWorkerReserve(ctx, reg, playerID))
	for _, intent := range intents {
		if intent.role == depot.RoleDeliveryHull && reserved[intent.shipSymbol] {
			fmt.Printf("Reserve floor (sp-mzdk): keeping home general hauler %s undedicated as a contract-worker reserve instead of pinning it to hub %s\n",
				intent.shipSymbol, intent.targetWaypoint)
			continue
		}
		dispatchDepotLaunch(ctx, sink, intent, playerID)
	}
}

// dispatchDepotLaunch routes ONE planned intent to the sink's per-role launch (sp-3l64). Extracted
// so BOTH the whole-registry boot/reload path (launchDepotCoordinators) and the granular
// element-add path (positionAddedDepotElement) dispatch through ONE role→launch mapping — a new
// role is wired in exactly one place. Fail-open: a per-element launch failure (most commonly a hull
// already flying its coordinator — the benign already-launched skip the sink returns as nil) is
// logged and stepped over so one bad element never blocks the rest.
func dispatchDepotLaunch(ctx context.Context, sink depotCoordinatorSink, intent depotLaunchIntent, playerID int) {
	var err error
	switch intent.role {
	case depot.RoleWarehouse:
		err = sink.launchDepotWarehouse(ctx, intent.shipSymbol, intent.targetWaypoint, intent.coLocatedWarehouseShips, playerID)
	case depot.RoleStocker:
		err = sink.launchDepotStocker(ctx, intent.shipSymbol, intent.targetWaypoint, playerID)
	case depot.RoleDeliveryHull:
		err = sink.launchDepotDelivery(ctx, intent.shipSymbol, intent.targetWaypoint, playerID)
	case depot.RoleSourceHub:
		err = sink.launchDepotSourceHub(ctx, intent.shipSymbol, intent.targetWaypoint, playerID)
	default:
		return
	}
	if err != nil {
		fmt.Printf("Warning: depot %q %s launch for ship %s skipped: %v\n",
			intent.depotID, intent.role, intent.shipSymbol, err)
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
	gateCtx bufferGateContext,
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
		// receipt-demand signal, not the source buy-leg. RankByContractReward (sp-wxf2) makes the
		// candidate selection + TopN cull rank by contract-reward value — the SAME axis the receipt
		// knapsack below optimizes — so a high-reward/low-savings good (MEDICINE/CLOTHING-like) is
		// not dropped by the source-side savings cull before PlanReceiptCaps ever weighs it.
		if rows, err := miner.Mine(ctx, destinationSystem, playerID, nil, persistence.DemandMinerOptions{RankBy: persistence.RankByContractReward}); err == nil {
			candidates = rows
		}
	}

	// sp-rxrg: gate the candidates on hub-contract-membership + local-production + source-distance
	// BEFORE the reward knapsack, so a good never contracted to THIS hub, produced locally, or sourced
	// too near is never even weighed — freeing that capacity for the goods the buffer actually exists
	// to pre-stage (the far/orphan hub contract goods).
	candidates = applyBufferGates(candidates, destinationSystem, warehouseWaypoint, gateCtx, coords)

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
	gateCtx bufferGateContext,
	playerID int,
	params *tradingsvc.WarehouseCapParams,
) map[string]int {
	capacity := 0
	for _, shipSymbol := range coLocatedWarehouseShips {
		capacity += capacityOf(shipSymbol)
	}
	return depotWarehouseTargetUnits(ctx, miner, capacity, destinationSystem, warehouseWaypoint, coords, gateCtx, playerID, params)
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
//
// sp-3l64 (role-agnostic): FIRST free+re-dedicate the hull to its OWN "warehouse" fleet via the
// shared positionDepotElementHull (navigateOnAssign=false — the warehouse COORDINATOR parks the
// hull, run_warehouse navigates it to the waypoint). This is what unblocks a hull added from a
// FOREIGN fleet: a "contract"/"manufacturing"-tagged hull can't be claimed under operation
// "warehouse" (ClaimShip rejects a foreign dedication) and a busy one isn't idle — so before this,
// an added warehouse hull sat docked, un-adopted. Re-dedicating to "warehouse" both excludes it
// from the contract grab AND satisfies the coordinator's operation-checked claim (same tag).
func (s *DaemonServer) launchDepotWarehouse(ctx context.Context, shipSymbol, warehouseWaypoint string, coLocatedWarehouseShips []string, playerID int) error {
	if shipSymbol == "" || warehouseWaypoint == "" {
		return fmt.Errorf("depot warehouse launch requires a ship symbol and warehouse waypoint")
	}
	ship, err := s.positionDepotElementHull(ctx, shipSymbol, warehouseWaypoint, operationWarehouse, false, playerID)
	if err != nil {
		return err
	}
	if ship == nil {
		return fmt.Errorf("depot warehouse hull %s not found", shipSymbol)
	}
	// Buffer-authority HANDOFF (sp-j4mc, prereq to arming epic st-7zk): when an ARMED capacity
	// reconciler owns this player's buffers, STAND DOWN the depot's buffer re-solve for an
	// ALREADY-RUNNING warehouse — the reconciler is then the sole writer of supported_goods, and
	// re-solving here would thrash the live buffer every reload (the depot ranks receipt goods by
	// VALUE, the reconciler by FREQUENCY). Two invariants keep this safe:
	//   - Scoped to the NON-IDLE (already-running) hull ONLY. A FRESH launch (idle hull) falls
	//     through to persistAndRunWarehouse below and STILL establishes the warehouse's initial
	//     whitelist — the reconciler only adjusts it via deltas thereafter, so there is no gap
	//     where nobody writes supported_goods.
	//   - The hull positioning above (positionDepotElementHull: free / re-dedicate / park) already
	//     ran, so unrelated warehouse bookkeeping is untouched — only the supported_goods re-solve
	//     and overwrite stand down.
	// armedCapacityReconcilerOwnsBuffers fails toward depot-owns on ANY repo/parse error, so a
	// query failure can never strand the buffer (default = depot owns; INERT while the live
	// reconciler is DryRun).
	if !ship.IsIdle() && s.armedCapacityReconcilerOwnsBuffers(ctx, playerID) {
		fmt.Printf("depot buffer re-solve deferred to armed reconciler (player %d): warehouse %s at %s left to the reconciler's supported_goods\n",
			playerID, shipSymbol, warehouseWaypoint)
		return nil
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
		s.waypointCoords(ctx), s.depotBufferGateContext(ctx, warehouseWaypoint, playerID), playerID, nil,
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

// positionDepotElementHull makes a depot element's hull assignment ATOMIC and ROLE-AGNOSTIC
// (bead sp-3l64) — the shared spine every role's launch routes through, so a warehouse / stocker /
// source-hub hull is freed + excluded + positioned by the SAME machinery that shipped for the
// delivery hull, instead of being persisted-but-left-docked. Parameterized by the role's
// DedicatedFleet tag (fleetTag) and whether THIS call parks the hull itself (navigateOnAssign). It
// performs, in order:
//
//  1. CLAIM-RELEASE + RE-DEDICATE (free from prior fleet): re-dedicate the hull to fleetTag and
//     sever any prior fleet's LIVE work-claim, reusing the SAME sp-w3yd machinery `fleet unassign`
//     uses (AssignFleet + ReleaseContainerClaim). Re-dedicate FIRST so the instant the claim breaks
//     the tag already prevents the old coordinator from re-grabbing it; then break the claim so a
//     hull that was MID-TASK at assign time becomes free. It fires only when the hull is not ALREADY
//     the role's own (see depotHullNeedsFreeing) — so a hull mid-role is never yanked on a reload.
//  2. EXCLUDE from the contract coordinator's grab: emergent from the fleetTag written in step 1
//     (FindIdleLightHaulers excludes any DedicatedFleet != ""; the coordinator's own
//     FindIdleShipsByFleet("contract") returns only "contract"-tagged hulls) — no separate write.
//     A delivery hull uses the DISTINCT depot.DeliveryHullFleet (dispatched only via
//     routeContractViaDepot under that identity); a warehouse/stocker re-dedicates to its OWN
//     coordinator's tag ("warehouse"/"stocker") so the SAME tag both excludes it from the grab AND
//     lets its coordinator's operation-checked ClaimShip take it (never fighting its dedication).
//  3. (RE)NAVIGATE to the waypoint — only when navigateOnAssign is set, for a role with NO standing
//     coordinator to park its hull (delivery hull + source hub). warehouse + stocker pass false:
//     their OWN coordinator parks the hull (run_warehouse navigates to the waypoint; the stocker
//     shuttles), so navigating here would only fight the coordinator's idle-gate and defer its start.
//
// IDEMPOTENT + fail-open, preserving the shipped delivery behavior: a hull already the role's own
// skips the claim-release (never yanked mid-role); a hull still flying is a benign skip; a hull
// already at its waypoint is a no-op. Returns the reloaded ship so a caller (warehouse/stocker
// launch) can gate its coordinator start on the post-release state.
func (s *DaemonServer) positionDepotElementHull(
	ctx context.Context, shipSymbol, targetWaypoint, fleetTag string, navigateOnAssign bool, playerID int,
) (*navigation.Ship, error) {
	pid := shared.MustNewPlayerID(playerID)
	ship, err := s.shipRepo.FindBySymbol(ctx, shipSymbol, pid)
	if err != nil {
		return nil, fmt.Errorf("failed to load depot %s hull %s: %w", fleetTag, shipSymbol, err)
	}
	if ship == nil {
		return nil, fmt.Errorf("depot %s hull %s not found", fleetTag, shipSymbol)
	}

	if depotHullNeedsFreeing(ship, fleetTag) {
		if err := s.shipRepo.AssignFleet(ctx, shipSymbol, fleetTag, pid); err != nil {
			return nil, fmt.Errorf("failed to re-dedicate depot hull %s to %q: %w", shipSymbol, fleetTag, err)
		}
		if _, err := s.shipRepo.ReleaseContainerClaim(ctx, shipSymbol, pid,
			fmt.Sprintf("re-dedicated as depot %s hull for %s (sp-3l64)", fleetTag, targetWaypoint)); err != nil {
			return nil, fmt.Errorf("failed to release prior work-claim on depot hull %s: %w", shipSymbol, err)
		}
		// Reload so the idle / location gates below observe the post-release state.
		ship, err = s.shipRepo.FindBySymbol(ctx, shipSymbol, pid)
		if err != nil {
			return nil, fmt.Errorf("failed to reload depot hull %s after re-dedication: %w", shipSymbol, err)
		}
		if ship == nil {
			return nil, fmt.Errorf("depot hull %s not found after re-dedication", shipSymbol)
		}
	}

	if !navigateOnAssign {
		return ship, nil // warehouse/stocker: their own coordinator parks the hull, not this call
	}
	if !ship.IsIdle() {
		return ship, nil // still flying (dispatched, or mid-reposition) — benign skip, never yanked
	}
	if loc := ship.CurrentLocation(); loc != nil && loc.Symbol == targetWaypoint {
		return ship, nil // already parked at its waypoint — nothing to reposition
	}
	navigate := s.NavigateShip
	if s.depotNavigateOverride != nil {
		navigate = s.depotNavigateOverride
	}
	if _, err := navigate(ctx, shipSymbol, targetWaypoint, playerID); err != nil {
		return ship, err
	}
	return ship, nil
}

// depotHullNeedsFreeing reports whether a depot element's hull must be claim-released + re-dedicated
// to fleetTag (sp-3l64). It fires when the hull is not already the role's own (DedicatedFleet !=
// fleetTag) AND it is safe to break its current occupancy — the hull is idle, or it is tagged to a
// FOREIGN fleet (the exact re-grab / mid-task vector: a "contract"/"manufacturing"-tagged hull).
//
// The subtle case it must NOT fire on is a hull that is non-idle AND untagged: it is already flying
// a coordinator that claimed it while untagged — a warehouse/stocker RESUMING on reload (the boot
// path re-runs this against a recovered, non-idle buffer hull that StartWarehouse/StartStocker
// never tagged) — and yanking it would strand the running buffer. That is the untagged-running
// idempotency that pairs with the tagged-running skip, and it keeps the reload re-run quiet.
func depotHullNeedsFreeing(ship *navigation.Ship, fleetTag string) bool {
	if ship.DedicatedFleet() == fleetTag {
		return false // already the role's own — idempotent skip (never yank a hull mid-role)
	}
	return ship.IsIdle() || ship.DedicatedFleet() != ""
}

// launchDepotDelivery (depotCoordinatorSink) makes depot delivery-hull assignment ATOMIC
// (bead sp-3l64, extending sp-9j9c) so a multi-hub delivery fleet is PRESENT at its hubs for the
// nearest-selection router (SelectDeliveryHull) to route each cluster's contract to its LOCAL hull —
// and STAYS there. It is the free+exclude+park path through the shared positionDepotElementHull:
// re-dedicated to the DISTINCT depot.DeliveryHullFleet (invisible to both pools the contract
// coordinator draws from — dispatched only via routeContractViaDepot), and — having no standing
// coordinator of its own — (re)navigated to its hub on assign and reload (navigateOnAssign=true).
func (s *DaemonServer) launchDepotDelivery(ctx context.Context, shipSymbol, hubWaypoint string, playerID int) error {
	if shipSymbol == "" || hubWaypoint == "" {
		return fmt.Errorf("depot delivery hull launch requires a ship symbol and hub waypoint")
	}
	_, err := s.positionDepotElementHull(ctx, shipSymbol, hubWaypoint, depot.DeliveryHullFleet, true, playerID)
	return err
}

// launchDepotSourceHub (depotCoordinatorSink) makes depot source-hub assignment ATOMIC and
// role-agnostic (sp-3l64): like the delivery hull it has no standing coordinator, so its crewing
// hull is freed from any prior fleet, excluded from the contract grab via the DISTINCT
// depotSourceHubFleet tag, and (re)navigated to its market waypoint on assign and reload — instead
// of being persisted-but-left-docked. It holds the buy anchor for the depot's stockers; it is not
// dispatched.
func (s *DaemonServer) launchDepotSourceHub(ctx context.Context, shipSymbol, hubWaypoint string, playerID int) error {
	if shipSymbol == "" || hubWaypoint == "" {
		return fmt.Errorf("depot source-hub launch requires a ship symbol and waypoint")
	}
	_, err := s.positionDepotElementHull(ctx, shipSymbol, hubWaypoint, depotSourceHubFleet, true, playerID)
	return err
}

// depotSink resolves the depotCoordinatorSink the element-add / reload positioning dispatches each
// launch through: the injected spy in tests (depotSinkOverride), else *DaemonServer itself (the
// real StartWarehouse / StartStocker / navigate path). Mirrors the depotReceiptMiner override seam.
func (s *DaemonServer) depotSink() depotCoordinatorSink {
	if s.depotSinkOverride != nil {
		return s.depotSinkOverride
	}
	return s
}

// launchDepotStocker (depotCoordinatorSink) starts a STANDING, continuous stocker on
// shipSymbol that fills the depot's destination warehouse (warehouseWaypoint) and re-stages
// the moment contracts drain the buffer, surviving restart. It leaves every money/freshness
// knob at the coordinator's own default (targetPerGood 0 → the warehouse's receipt caps drive
// the fill). A hull that is not idle is already flying its coordinator — a benign
// already-launched skip (nil), never an error. It reuses StartStocker (no parallel channel).
//
// sp-3l64 (role-agnostic): FIRST free+re-dedicate the hull to its OWN "stocker" fleet via the
// shared positionDepotElementHull (navigateOnAssign=false — the stocker COORDINATOR moves the hull:
// it shuttles buy→home→deposit, so there is no park leg to fire here). Same unblock as the
// warehouse: a hull added from a foreign fleet (or busy) is severed + re-dedicated to "stocker" so
// the coordinator's operation-checked claim can take it, instead of being persisted-but-left-docked.
func (s *DaemonServer) launchDepotStocker(ctx context.Context, shipSymbol, warehouseWaypoint string, playerID int) error {
	if shipSymbol == "" || warehouseWaypoint == "" {
		return fmt.Errorf("depot stocker launch requires a ship symbol and warehouse waypoint")
	}
	ship, err := s.positionDepotElementHull(ctx, shipSymbol, warehouseWaypoint, operationStocker, false, playerID)
	if err != nil {
		return err
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

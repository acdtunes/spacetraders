package grpc

import (
	"context"
	"fmt"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	tradingsvc "github.com/andrescamacho/spacetraders-go/internal/application/trading/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/depot"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
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
		for _, w := range warehouses {
			if w.ShipSymbol == "" {
				continue // declared-but-uncrewed slot: no hull to fly yet
			}
			intents = append(intents, depotLaunchIntent{
				depotID:           c.ID(),
				role:              depot.RoleWarehouse,
				shipSymbol:        w.ShipSymbol,
				warehouseWaypoint: w.Waypoint, // a warehouse parks at its own waypoint
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
	launchDepotWarehouse(ctx context.Context, shipSymbol, warehouseWaypoint string, playerID int) error
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
			err = sink.launchDepotWarehouse(ctx, intent.shipSymbol, intent.warehouseWaypoint, playerID)
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

// depotReceiptPayment is the per-unit value signal for a received good in the receipt-demand
// knapsack. It uses the good's market value — the home (destination) ask when known, else the
// source ask — so a good the destination IMPORTS (and therefore does not itself sell, no home
// ask) still carries a non-zero value rather than being dropped. A follow-on can thread the
// true contract reward here once the demand miner surfaces it.
func depotReceiptPayment(c persistence.DemandCandidate) float64 {
	if c.HomeAsk > 0 {
		return float64(c.HomeAsk)
	}
	return float64(c.ForeignAsk)
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

// launchDepotWarehouse (depotCoordinatorSink) starts a destination-side depot warehouse
// on shipSymbol parked at warehouseWaypoint, with its cold-start cargo caps gated on RECEIPT
// demand (depotWarehouseTargetUnits -> PlanReceiptCaps) rather than the source-side
// selector. Its supported-stock whitelist is the receipt caps' goods. A hull that is not idle
// is already flying its coordinator — a benign already-launched skip (nil), never an error, so
// the boot re-run is quiet. It reuses persistAndRunWarehouse, so the container's persistence /
// claim / recovery path is byte-identical to a captain-launched warehouse.
func (s *DaemonServer) launchDepotWarehouse(ctx context.Context, shipSymbol, warehouseWaypoint string, playerID int) error {
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
	if !ship.IsIdle() {
		return nil // already flying its coordinator — benign already-launched skip
	}

	var miner tradingsvc.DepositDemandMiner
	if s.db != nil {
		miner = persistence.NewDemandMiner(s.db)
	}
	targetUnits := depotWarehouseTargetUnits(
		ctx, miner, ship.CargoCapacity(),
		shared.ExtractSystemSymbol(warehouseWaypoint), warehouseWaypoint,
		s.waypointCoords(ctx), playerID, nil,
	)
	supportedGoods := sortedGoods(targetUnits)
	if len(supportedGoods) == 0 {
		return fmt.Errorf("depot warehouse %s at %s: no receipt-demand goods to stock", shipSymbol, warehouseWaypoint)
	}

	_, err = s.persistAndRunWarehouse(ctx, shipSymbol, warehouseWaypoint, supportedGoods, targetUnits, playerID)
	return err
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

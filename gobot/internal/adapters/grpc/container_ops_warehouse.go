package grpc

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	tradingsvc "github.com/andrescamacho/spacetraders-go/internal/application/trading/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// WarehouseOperationResult reports the container started for a warehouse.
type WarehouseOperationResult struct {
	ContainerID    string
	ShipSymbol     string
	WaypointSymbol string
}

// operationWarehouse is the warehouse's fleet identity for the atomic ClaimShip
// dedication check (sp-l7h2 Phase 2, RULINGS #7): a hull the captain pins
// `fleet assign --fleet warehouse` is claimable by its own warehouse container
// and rejected for every other operation, so the dedicated buffer hull is never
// poached by a gas/manufacturing/contract coordinator.
const operationWarehouse = "warehouse"

// StartWarehouse launches a passive inventory warehouse (sp-dchv Lane B) as a
// recovery-safe daemon container on ONE dedicated storage hull parked at a home
// waypoint. Templated on the gas/trade coordinator start path (RULINGS #2/#3):
//
//   - Idle-gap discipline: it refuses any hull that is not genuinely idle BEFORE
//     persisting anything, so a refused start has no side effects and never
//     steals a hull the daemon is actively flying.
//   - Single-writer + release-on-death: the ContainerRunner claims the hull
//     through the normal lifecycle (createShipAssignments via the ship_symbol +
//     operation="warehouse" metadata — the atomic, operation-checked ClaimShip)
//     and force-releases it on every terminal path.
//   - Recovery-safe: the row is created RUNNING (runner.Start transitions
//     PENDING->RUNNING), and "warehouse" is registered in the command factory, so
//     a daemon restart rebuilds the warehouse from its launch config; the hull's
//     cargo is separately rebuilt by the StorageRecoveryService from live ship
//     state (no new table — the storage_operations row is the source of truth).
//
// The warehouse does no work of its own: deposits arrive from tour/trade deposit
// legs and withdrawals from the manufacturing STORAGE_ACQUIRE_DELIVER executor,
// both against the shared StorageCoordinator the RunWarehouse handler registers
// the hull into.
func (s *DaemonServer) StartWarehouse(
	ctx context.Context,
	shipSymbol string,
	waypointSymbol string,
	supportedGoods []string,
	playerID int,
) (*WarehouseOperationResult, error) {
	if shipSymbol == "" {
		return nil, fmt.Errorf("ship symbol is required")
	}
	if waypointSymbol == "" {
		return nil, fmt.Errorf("waypoint symbol is required")
	}
	if len(supportedGoods) == 0 {
		return nil, fmt.Errorf("at least one supported good is required")
	}

	// Idle-gap discipline: only dedicate a genuinely idle hull, never steal one
	// mid-task (the atomic ClaimShip below is the secondary guard for the narrow
	// check->claim race).
	ship, err := s.shipRepo.FindBySymbol(ctx, shipSymbol, shared.MustNewPlayerID(playerID))
	if err != nil {
		return nil, fmt.Errorf("failed to load ship %s: %w", shipSymbol, err)
	}
	if ship == nil {
		return nil, fmt.Errorf("ship %s not found", shipSymbol)
	}
	if !ship.IsIdle() {
		return nil, fmt.Errorf("ship %s is not idle (assigned to %q) - warehouse only takes idle hulls", shipSymbol, ship.ContainerID())
	}

	// Auto-cap knapsack (sp-5n7v): compute per-good target_units from live contract demand ×
	// residual-buy-leg over the REAL hull cargo_capacity (never assume-80) and persist them in
	// the config, so the caps survive + reload with the container (RULINGS #2) and the captain
	// can inspect the buffer plan. A thin/absent demand history degrades to the static
	// cold-start caps clipped to the real capacity. The continuous re-solve — and the actual
	// per-good enforcement — lives in the stocker loop, which re-derives these caps live each
	// pass from the same optimizer.
	var miner tradingsvc.DepositDemandMiner
	if s.db != nil {
		miner = persistence.NewDemandMiner(s.db)
	}
	targetUnits := warehouseTargetUnits(ctx, miner, ship.CargoCapacity(), shared.ExtractSystemSymbol(waypointSymbol), waypointSymbol, s.waypointCoords(ctx), playerID, nil)

	return s.persistAndRunWarehouse(ctx, shipSymbol, waypointSymbol, supportedGoods, targetUnits, playerID)
}

// persistAndRunWarehouse builds the recovery-visible warehouse container from precomputed
// per-good caps, persists it, and starts the claiming runner. Extracted (sp-cftm) so the
// source-side StartWarehouse and the destination-side cluster launch share ONE container
// lifecycle: the caps come from different selectors (source-side PlanWarehouseCaps vs
// destination-receipt PlanReceiptCaps) but the persistence / claim / recovery path is
// identical — no parallel channel.
func (s *DaemonServer) persistAndRunWarehouse(
	ctx context.Context,
	shipSymbol string,
	waypointSymbol string,
	supportedGoods []string,
	targetUnits map[string]int,
	playerID int,
) (*WarehouseOperationResult, error) {
	containerID := utils.GenerateContainerID("warehouse", shipSymbol)

	supportedGoodsInterface := make([]interface{}, len(supportedGoods))
	for i, g := range supportedGoods {
		supportedGoodsInterface[i] = g
	}

	targetUnitsInterface := make(map[string]interface{}, len(targetUnits))
	for g, u := range targetUnits {
		targetUnitsInterface[g] = u
	}

	config := map[string]interface{}{
		"ship_symbol":     shipSymbol,
		"waypoint_symbol": waypointSymbol,
		"operation_id":    containerID,
		"supported_goods": supportedGoodsInterface,
		"container_id":    containerID,
		// Auto-computed per-good buffer caps (sp-5n7v). Persisted so the plan reloads with the
		// container; the stocker enforces the live-re-derived equivalents.
		"target_units": targetUnitsInterface,
		// The runner claims the hull through the atomic operation-checked
		// ClaimShip when this key is present (RULINGS #7). Persisted so a
		// recovery rebuild claims under the same fleet identity.
		"operation": operationWarehouse,
	}

	// Build the command through the same factory recovery uses, so the launch
	// config and the recovery rebuild can never drift.
	cmd, err := s.buildCommandForType("warehouse", config, playerID, containerID)
	if err != nil {
		return nil, fmt.Errorf("failed to create warehouse command: %w", err)
	}

	// A warehouse is a passive standing operation: it holds until stopped, so it
	// runs with infinite iterations like the gas coordinator. Its Handle blocks
	// on shutdown rather than looping, so the iteration model only governs the
	// (never-reached) re-entry after a clean return.
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeWarehouse,
		playerID,
		-1,  // infinite: run until stopped
		nil, // no parent — top-level coordinator, recovered independently
		config,
		nil, // default RealClock
	)

	if err := s.containerRepo.Add(ctx, containerEntity, "warehouse"); err != nil {
		return nil, fmt.Errorf("failed to persist warehouse container: %w", err)
	}

	// The runner claims the hull (ship_symbol metadata), flips the row to
	// RUNNING, and owns release-on-death.
	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipRepo, s.clock)
	s.registerContainer(containerID, runner)

	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("Warehouse container %s failed: %v\n", containerID, err)
		}
	}()

	return &WarehouseOperationResult{
		ContainerID:    containerID,
		ShipSymbol:     shipSymbol,
		WaypointSymbol: waypointSymbol,
	}, nil
}

// warehouseTargetUnits computes the per-good buffer caps for a warehouse hull (sp-5n7v):
// the auto-cap knapsack over live contract demand × residual-buy-leg subject to the REAL
// hull cargo_capacity (capacity — never assume-80). A nil miner, a mining error, or thin
// demand history degrades to the static cold-start caps clipped to the real capacity, so a
// warehouse always starts with a sane, capacity-respecting plan. Kept as a small seam so the
// glue is unit-tested without a live daemon.
func warehouseTargetUnits(
	ctx context.Context,
	miner tradingsvc.DepositDemandMiner,
	capacity int,
	homeSystem string,
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
		if rows, err := miner.Mine(ctx, homeSystem, playerID, nil, persistence.DemandMinerOptions{}); err == nil {
			candidates = rows
		}
	}
	return tradingsvc.PlanWarehouseCaps(candidates, capacity, homeSystem, warehouseWaypoint, coords, nil, nil, p).Targets
}

// waypointCoords builds the sp-9274 cache-only coordinate lookup for the warehouse-launch
// auto-cap plan (mirrors the stocker's live-loop lookup). Reads the waypoint repository only
// (no API fetch-through); a nil repo or an unresolvable/TTL-expired waypoint returns ok=false and
// the optimizer FAILS OPEN to the coarse in/cross-system residual (RULINGS #1).
func (s *DaemonServer) waypointCoords(ctx context.Context) tradingsvc.WaypointCoordsLookup {
	if s.waypointRepo == nil {
		return nil
	}
	return func(waypoint string) (float64, float64, bool) {
		wp, err := s.waypointRepo.FindBySymbol(ctx, waypoint, shared.ExtractSystemSymbol(waypoint))
		if err != nil || wp == nil {
			return 0, 0, false
		}
		return wp.X, wp.Y, true
	}
}

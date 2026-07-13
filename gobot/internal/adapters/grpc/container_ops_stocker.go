package grpc

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// StockerOperationResult reports the container started for a stocker loop.
type StockerOperationResult struct {
	ContainerID       string
	ShipSymbol        string
	WarehouseWaypoint string
}

// operationStocker is the stocker's fleet identity for the atomic ClaimShip
// dedication check (sp-m92a, mirroring operationWarehouse / RULINGS #7). It is
// BOTH the operation the stocker container claims under AND the DedicatedFleet
// tag the captain pins with `fleet assign --fleet stocker`: the ClaimShip guard
// (ship_repository.go) permits a claim only when operation == DedicatedFleet, so
// a hull dedicated to "stocker" is claimable by its own stocker container and
// rejected for every other operation (factory "manufacturing", contract
// "contract", ...). That — plus the FindIdleLightHaulers exclude filter that
// hides any dedicated hull from the general pool — is what makes continuous
// stocking survive the stocker container ending: the dedication persists across
// crash/restart/idle-gap, no coordinator can poach the hull, and the next
// stocker relaunch re-claims its own.
const operationStocker = "stocker"

// StartStocker launches the STOCKER LOOP (sp-zdwg) as a recovery-safe daemon container:
// a dedicated hull that fills a home warehouse the tours rationally won't (sp-dchv proved
// deposit legs lose to direct sells at every re-plan — correct economics; the stocker
// dedicates capacity instead of distorting tour objectives). Each round-trip it need-ranks
// the most-needed supported stock good, buys it at the cheapest foreign market
// (live-verified, fail-closed), hauls home, and deposits into the warehouse.
//
// It reuses trade-route/arb-run/tour-run's exact start machinery so it inherits the same
// safety properties without re-deriving them:
//
//   - Idle-gap discipline: it refuses any hull that is not genuinely idle BEFORE
//     persisting anything, so a refused start has no side effects and never steals a hull.
//   - Single-writer + release-on-death: the ContainerRunner claims the hull through the
//     atomic operation-checked ClaimShip (ship_symbol + operation="stocker" metadata) and
//     force-releases it on every terminal path — releasing only the container claim, never
//     the durable "stocker" dedication, so the hull stays the stocker's across restarts.
//   - Recovery-safe: the row is created RUNNING and "stocker" is registered in the command
//     factory, so a daemon restart rebuilds the run from its launch config (a laden hull
//     resumes deposit-first, RULINGS #2) or cleanly releases the hull.
//
// The buy/navigation legs go through the daemon mediator's handlers (the RouteExecutor-
// backed NavigateRouteCommand for travel), identical to tour-run; deposits use the shared
// storage coordinator's Lane B protocol.
func (s *DaemonServer) StartStocker(
	ctx context.Context,
	shipSymbol string,
	warehouseWaypoint string,
	budgetPerLeg int,
	workingCapitalReserve int64,
	iterations int,
	maxMarketAgeMinutes int,
	targetPerGood int,
	standing bool,
	tickSeconds int,
	refillHysteresis int,
	agentSymbol string,
	playerID int,
) (*StockerOperationResult, error) {
	if shipSymbol == "" {
		return nil, fmt.Errorf("ship symbol is required")
	}
	if warehouseWaypoint == "" {
		return nil, fmt.Errorf("warehouse waypoint is required")
	}

	// Idle-gap discipline: only fly a genuinely idle hull, never steal one mid-task.
	ship, err := s.shipRepo.FindBySymbol(ctx, shipSymbol, shared.MustNewPlayerID(playerID))
	if err != nil {
		return nil, fmt.Errorf("failed to load ship %s: %w", shipSymbol, err)
	}
	if ship == nil {
		return nil, fmt.Errorf("ship %s not found", shipSymbol)
	}
	if !ship.IsIdle() {
		return nil, fmt.Errorf("ship %s is not idle (assigned to %q) - stocker only takes idle-gap hulls", shipSymbol, ship.ContainerID())
	}

	containerID := utils.GenerateContainerID("stocker", shipSymbol)
	config := map[string]interface{}{
		"ship_symbol":             shipSymbol,
		"warehouse_waypoint":      warehouseWaypoint,
		"container_id":            containerID,
		"agent_symbol":            agentSymbol,
		"budget_per_leg":          budgetPerLeg,
		"working_capital_reserve": workingCapitalReserve,
		"iterations":              iterations,
		"max_market_age_minutes":  maxMarketAgeMinutes,
		"target_per_good":         targetPerGood,
		// sp-k1ka: the STANDING intent + its cadence/hysteresis knobs are PERSISTED here so a
		// daemon restart re-adopts the stocker STANDING from this exact config (RULINGS #2) —
		// the recovery rebuild (buildCommandForType) resumes the park-and-re-stage loop with
		// no manual relaunch. A non-standing launch persists standing=false (a no-op).
		"standing":          standing,
		"tick_seconds":      tickSeconds,
		"refill_hysteresis": refillHysteresis,
		// The runner claims the hull through the atomic operation-checked
		// ClaimShip when this key is present (sp-m92a, mirroring the warehouse):
		// operation="stocker" matches the hull's "stocker" DedicatedFleet tag, so
		// the stocker takes its own dedicated hull while every other coordinator
		// is rejected. Persisted so a recovery rebuild re-claims under the same
		// fleet identity after a crash/restart.
		"operation": operationStocker,
	}

	// Build the stocker command through the same factory recovery uses, so the launch
	// config and the recovery rebuild can never drift.
	cmd, err := s.buildCommandForType("stocker", config, playerID, containerID)
	if err != nil {
		return nil, fmt.Errorf("failed to create stocker command: %w", err)
	}

	// The stocker owns its own round-trip loop internally (CoordinatorOwnsIterations); the
	// container runs Handle() once — iterations=1 at the container layer, never a loop.
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeTrading,
		playerID,
		1,   // coordinator owns the loop
		nil, // no parent — top-level, recovered independently
		config,
		nil, // default RealClock
	)

	if err := s.containerRepo.Add(ctx, containerEntity, "stocker"); err != nil {
		return nil, fmt.Errorf("failed to persist stocker container: %w", err)
	}

	// The runner claims the hull through the operation-checked ClaimShip (ship_symbol +
	// operation="stocker" metadata), flips the row to RUNNING, and owns release-on-death.
	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipRepo, s.clock)
	s.registerContainer(containerID, runner)

	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("Stocker container %s failed: %v\n", containerID, err)
		}
	}()

	return &StockerOperationResult{
		ContainerID:       containerID,
		ShipSymbol:        shipSymbol,
		WarehouseWaypoint: warehouseWaypoint,
	}, nil
}

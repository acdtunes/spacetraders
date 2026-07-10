package grpc

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// TourRunOperationResult reports the container started for a one-shot guarded trade tour.
type TourRunOperationResult struct {
	ContainerID string
	ShipSymbol  string
}

// StartTourRun launches a captain-directed, guarded multi-hop trade tour (sp-1ek0) as
// a recovery-safe daemon container — arb-run's twin. Unlike arb-run it does not name a
// lane: it asks the depth-aware planner for a tour, flies it leg by leg with prices
// re-verified live at every dock, and re-plans on drift.
//
// iterations (sp-m5kv) makes it CONTINUOUS: -1 = tour, re-plan from the new position,
// tour again until margins die/starvation/stop (engine-cadence capital velocity);
// N>0 = exactly N tours; 0/unset = one tour (the original one-shot). The coordinator
// owns this loop (CoordinatorOwnsIterations), so the container still runs one iteration.
//
// It reuses arb-run's exact start machinery so it inherits the same safety properties:
//
//   - Idle-gap discipline: it refuses any hull that is not genuinely idle BEFORE
//     persisting anything, so a refused start has no side effects and never steals a
//     hull the daemon is actively flying.
//   - Single-writer + release-on-death: the ContainerRunner claims the hull through the
//     normal lifecycle (ship_symbol metadata) and force-releases it on every terminal
//     path, so the hull is never stranded.
//   - Recovery-safe: the row is created RUNNING and "tour_run" is registered in the
//     command factory (sp-7yej invariant 4), so a daemon restart rebuilds the run from
//     its launch config (a cargo-aware re-plan from current state — a persisted -1
//     resumes continuous) or cleanly releases the hull.
//
// max_spend=0 is persisted as-is; the coordinator resolves the 25%-of-treasury default
// at launch (RULINGS #6) with the working-capital floor guarding every buy regardless.
func (s *DaemonServer) StartTourRun(
	ctx context.Context,
	shipSymbol string,
	maxHops int,
	maxSpend int64,
	minMargin int,
	replanLimit int,
	workingCapitalReserve int64,
	agentSymbol string,
	iterations int,
	playerID int,
) (*TourRunOperationResult, error) {
	if shipSymbol == "" {
		return nil, fmt.Errorf("ship symbol is required")
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
		return nil, fmt.Errorf("ship %s is not idle (assigned to %q) - tour-run only takes idle-gap hulls", shipSymbol, ship.ContainerID())
	}

	containerID := utils.GenerateContainerID("tour-run", shipSymbol)
	config := map[string]interface{}{
		"ship_symbol":             shipSymbol,
		"container_id":            containerID,
		"agent_symbol":            agentSymbol,
		"max_hops":                maxHops,
		"max_spend":               maxSpend,
		"min_margin":              minMargin,
		"replan_limit":            replanLimit,
		"working_capital_reserve": workingCapitalReserve,
		"iterations":              iterations,
	}

	// Build the tour command through the same factory recovery uses, so the launch
	// config and the recovery rebuild can never drift.
	cmd, err := s.buildCommandForType("tour_run", config, playerID, containerID)
	if err != nil {
		return nil, fmt.Errorf("failed to create tour-run command: %w", err)
	}

	// The coordinator owns the tour loop (CoordinatorOwnsIterations, sp-m5kv): whether
	// this is one tour or a continuous --iterations -1 run, the container runs Handle()
	// exactly ONCE and the coordinator loops internally, so the container's own
	// iteration budget stays 1 (re-entering it would double-loop the run). The
	// persisted "iterations" config drives the coordinator's loop and survives a
	// recovery rebuild, so a -1 run resumes continuous after a restart.
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeTrading,
		playerID,
		1,   // coordinator owns iterations; the runner invokes Handle() once
		nil, // no parent — top-level, recovered independently
		config,
		nil, // default RealClock
	)

	if err := s.containerRepo.Add(ctx, containerEntity, "tour_run"); err != nil {
		return nil, fmt.Errorf("failed to persist tour-run container: %w", err)
	}

	// The runner claims the hull (ship_symbol metadata), flips the row to RUNNING, and
	// owns release-on-death.
	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipRepo, s.clock)
	s.registerContainer(containerID, runner)

	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("Tour-run container %s failed: %v\n", containerID, err)
		}
	}()

	return &TourRunOperationResult{
		ContainerID: containerID,
		ShipSymbol:  shipSymbol,
	}, nil
}

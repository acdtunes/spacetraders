package grpc

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// ArbRunOperationResult reports the container started for a one-shot guarded arb run.
type ArbRunOperationResult struct {
	ContainerID string
	ShipSymbol  string
	Good        string
	BuyAt       string
	SellAt      string
}

// StartArbRun launches a ONE-SHOT, captain-directed, guarded arbitrage run (sp-p4ua) as
// a recovery-safe daemon container — the deliberate middle between hand-flying an arb
// leg and the autonomous trade-route circuit. Unlike trade-route it does not scan or
// loop: the captain names the good, the source, and the destination, and the run buys
// once, routes (cross-gate) to the destination, sells, and stops.
//
// It reuses trade-route's exact start machinery so it inherits the same safety
// properties without re-deriving them:
//
//   - Idle-gap discipline: it refuses any hull that is not genuinely idle BEFORE
//     persisting anything, so a refused start has no side effects and never steals a
//     hull the daemon is actively flying.
//   - Single-writer + release-on-death: the ContainerRunner claims the hull through the
//     normal lifecycle (ship_symbol metadata) and force-releases it on every terminal
//     path (completion, crash, cancel), so the hull is never stranded.
//   - Recovery-safe: the row is created RUNNING and "arb_run" is registered in the
//     command factory, so a daemon restart rebuilds the run from its launch config or
//     cleanly releases the hull.
//
// The buy/sell/navigation legs go through the daemon mediator's handlers (the
// RouteExecutor-backed NavigateRouteCommand for travel), identical to trade-route.
func (s *DaemonServer) StartArbRun(
	ctx context.Context,
	shipSymbol string,
	good string,
	buyAt string,
	sellAt string,
	maxUnits int,
	maxSpend int,
	minMargin int,
	workingCapitalReserve int,
	playerID int,
) (*ArbRunOperationResult, error) {
	if shipSymbol == "" {
		return nil, fmt.Errorf("ship symbol is required")
	}
	if good == "" {
		return nil, fmt.Errorf("good is required")
	}
	if buyAt == "" {
		return nil, fmt.Errorf("buy-at waypoint is required")
	}
	if sellAt == "" {
		return nil, fmt.Errorf("sell-at waypoint is required")
	}
	if buyAt == sellAt {
		return nil, fmt.Errorf("buy-at and sell-at must differ (both %s)", buyAt)
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
		return nil, fmt.Errorf("ship %s is not idle (assigned to %q) - arb-run only takes idle-gap hulls", shipSymbol, ship.ContainerID())
	}

	containerID := utils.GenerateContainerID("arb-run", shipSymbol)
	config := map[string]interface{}{
		"ship_symbol":             shipSymbol,
		"good":                    good,
		"buy_at":                  buyAt,
		"sell_at":                 sellAt,
		"container_id":            containerID,
		"max_units":               maxUnits,
		"max_spend":               maxSpend,
		"min_margin":              minMargin,
		"working_capital_reserve": workingCapitalReserve,
	}

	// Build the arb command through the same factory recovery uses, so the launch
	// config and the recovery rebuild can never drift.
	cmd, err := s.buildCommandForType("arb_run", config, playerID, containerID)
	if err != nil {
		return nil, fmt.Errorf("failed to create arb-run command: %w", err)
	}

	// A one-shot arb runs a single buy→travel→sell and completes (the runner then
	// releases the hull): iterations=1, never a loop.
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeTrading,
		playerID,
		1,   // single one-shot run
		nil, // no parent — top-level, recovered independently
		config,
		nil, // default RealClock
	)

	if err := s.containerRepo.Add(ctx, containerEntity, "arb_run"); err != nil {
		return nil, fmt.Errorf("failed to persist arb-run container: %w", err)
	}

	// The runner claims the hull (ship_symbol metadata), flips the row to RUNNING, and
	// owns release-on-death.
	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipRepo, s.clock)
	s.registerContainer(containerID, runner)

	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("Arb-run container %s failed: %v\n", containerID, err)
		}
	}()

	return &ArbRunOperationResult{
		ContainerID: containerID,
		ShipSymbol:  shipSymbol,
		Good:        good,
		BuyAt:       buyAt,
		SellAt:      sellAt,
	}, nil
}

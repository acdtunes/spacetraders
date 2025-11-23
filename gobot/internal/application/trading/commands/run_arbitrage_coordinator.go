package commands

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/contract"
	"github.com/andrescamacho/spacetraders-go/internal/application/trading/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
	"github.com/google/uuid"
)

// ContainerRepository defines persistence operations for containers
type ContainerRepository interface {
	Add(ctx context.Context, containerEntity *container.Container, commandType string) error
}

// RunArbitrageCoordinatorCommand coordinates parallel arbitrage trading operations
type RunArbitrageCoordinatorCommand struct {
	SystemSymbol string  // System to scan for opportunities
	PlayerID     int     // Player identifier
	ContainerID  string  // Container ID for this coordinator
	MinMargin    float64 // Minimum profit margin threshold (default 10.0%)
	MaxWorkers   int     // Maximum parallel workers (default 10)
}

// RunArbitrageCoordinatorResponse is never returned (infinite loop)
type RunArbitrageCoordinatorResponse struct {
	// Never returns
}

// RunArbitrageCoordinatorHandler orchestrates parallel arbitrage operations.
//
// Pattern: Fleet Coordinator with parallel execution
//
// Workflow:
//  1. Scan for opportunities (every 2 minutes)
//  2. Discover idle ships (every 30 seconds)
//  3. Spawn workers for each ship/opportunity pair
//  4. Workers execute in parallel (goroutines)
//  5. Wait for batch completion (sync.WaitGroup)
//  6. Repeat
//
// Unlike contract coordinator, this allows MULTIPLE workers in parallel.
type RunArbitrageCoordinatorHandler struct {
	opportunityFinder  *services.ArbitrageOpportunityFinder
	shipRepo           navigation.ShipRepository
	shipAssignmentRepo container.ShipAssignmentRepository
	containerRepo      ContainerRepository
	daemonClient       daemon.DaemonClient
	mediator           common.Mediator
	clock              shared.Clock
}

// NewRunArbitrageCoordinatorHandler creates a new coordinator handler
func NewRunArbitrageCoordinatorHandler(
	opportunityFinder *services.ArbitrageOpportunityFinder,
	shipRepo navigation.ShipRepository,
	shipAssignmentRepo container.ShipAssignmentRepository,
	containerRepo ContainerRepository,
	daemonClient daemon.DaemonClient,
	mediator common.Mediator,
	clock shared.Clock,
) *RunArbitrageCoordinatorHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}

	return &RunArbitrageCoordinatorHandler{
		opportunityFinder:  opportunityFinder,
		shipRepo:           shipRepo,
		shipAssignmentRepo: shipAssignmentRepo,
		containerRepo:      containerRepo,
		daemonClient:       daemonClient,
		mediator:           mediator,
		clock:              clock,
	}
}

// Handle executes the coordinator command
func (h *RunArbitrageCoordinatorHandler) Handle(
	ctx context.Context,
	request common.Request,
) (common.Response, error) {
	cmd, ok := request.(*RunArbitrageCoordinatorCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	logger := common.LoggerFromContext(ctx)

	// Apply defaults
	minMargin := cmd.MinMargin
	if minMargin <= 0 {
		minMargin = 10.0 // Default 10% margin
	}

	maxWorkers := cmd.MaxWorkers
	if maxWorkers <= 0 {
		maxWorkers = 10 // Default max 10 parallel workers
	}

	logger.Log("INFO", "Starting arbitrage coordinator", map[string]interface{}{
		"system":      cmd.SystemSymbol,
		"min_margin":  minMargin,
		"max_workers": maxWorkers,
	})

	// Main coordination loop (infinite)
	opportunityScanInterval := 2 * time.Minute
	shipDiscoveryInterval := 30 * time.Second

	opportunityTicker := time.NewTicker(opportunityScanInterval)
	shipDiscoveryTicker := time.NewTicker(shipDiscoveryInterval)
	defer opportunityTicker.Stop()
	defer shipDiscoveryTicker.Stop()

	var opportunities []*trading.ArbitrageOpportunity
	var idleShips []string

	// Initial scan
	h.scanOpportunities(ctx, cmd, minMargin, &opportunities)

	for {
		select {
		case <-opportunityTicker.C:
			// Scan for opportunities
			h.scanOpportunities(ctx, cmd, minMargin, &opportunities)

		case <-shipDiscoveryTicker.C:
			// Discover idle ships
			h.discoverIdleShips(ctx, cmd, &idleShips)

			// Spawn workers if we have both ships and opportunities
			if len(idleShips) > 0 && len(opportunities) > 0 {
				h.spawnWorkers(ctx, cmd, idleShips, opportunities, maxWorkers)
				// Clear idle ships after spawning (they're now assigned)
				idleShips = nil
			}

		case <-ctx.Done():
			// Graceful shutdown
			logger.Log("INFO", "Arbitrage coordinator shutting down", nil)
			return &RunArbitrageCoordinatorResponse{}, nil
		}
	}
}

// scanOpportunities scans markets for arbitrage opportunities
func (h *RunArbitrageCoordinatorHandler) scanOpportunities(
	ctx context.Context,
	cmd *RunArbitrageCoordinatorCommand,
	minMargin float64,
	opportunities *[]*trading.ArbitrageOpportunity,
) {
	logger := common.LoggerFromContext(ctx)

	logger.Log("INFO", "Scanning for arbitrage opportunities", map[string]interface{}{
		"system": cmd.SystemSymbol,
	})

	opps, err := h.opportunityFinder.FindOpportunities(
		ctx,
		cmd.SystemSymbol,
		cmd.PlayerID,
		40, // Assume light hauler capacity
		minMargin,
		20, // Top 20 opportunities
	)
	if err != nil {
		logger.Log("ERROR", fmt.Sprintf("Failed to scan opportunities: %v", err), nil)
		return
	}

	*opportunities = opps
	logger.Log("INFO", fmt.Sprintf("Found %d arbitrage opportunities", len(opps)), map[string]interface{}{
		"system": cmd.SystemSymbol,
		"count":  len(opps),
	})
}

// discoverIdleShips discovers available idle ships
func (h *RunArbitrageCoordinatorHandler) discoverIdleShips(
	ctx context.Context,
	cmd *RunArbitrageCoordinatorCommand,
	idleShips *[]string,
) {
	logger := common.LoggerFromContext(ctx)

	playerID := shared.MustNewPlayerID(cmd.PlayerID)
	_, ships, err := contract.FindIdleLightHaulers(
		ctx,
		playerID,
		h.shipRepo,
		h.shipAssignmentRepo,
	)
	if err != nil {
		logger.Log("ERROR", fmt.Sprintf("Failed to find idle ships: %v", err), nil)
		return
	}

	*idleShips = ships
	if len(ships) > 0 {
		logger.Log("INFO", fmt.Sprintf("Discovered %d idle ships", len(ships)), map[string]interface{}{
			"count": len(ships),
		})
	}
}

// spawnWorkers spawns parallel workers for available ships and opportunities
func (h *RunArbitrageCoordinatorHandler) spawnWorkers(
	ctx context.Context,
	cmd *RunArbitrageCoordinatorCommand,
	idleShips []string,
	opportunities []*trading.ArbitrageOpportunity,
	maxWorkers int,
) {
	logger := common.LoggerFromContext(ctx)

	// Spawn workers in parallel (goroutines)
	var wg sync.WaitGroup

	// Limit to min(idle ships, opportunities, maxWorkers)
	numWorkers := len(idleShips)
	if numWorkers > len(opportunities) {
		numWorkers = len(opportunities)
	}
	if numWorkers > maxWorkers {
		numWorkers = maxWorkers
	}

	logger.Log("INFO", fmt.Sprintf("Spawning %d arbitrage workers", numWorkers), map[string]interface{}{
		"workers": numWorkers,
	})

	for i := 0; i < numWorkers; i++ {
		ship := idleShips[i]
		opp := opportunities[i]

		wg.Add(1)
		go func(shipSymbol string, opportunity *trading.ArbitrageOpportunity) {
			defer wg.Done()

			// Create worker container ID
			workerID := fmt.Sprintf("arbitrage-worker-%s-%s", shipSymbol, uuid.New().String()[:8])

			// Create worker command
			workerCmd := &RunArbitrageWorkerCommand{
				ShipSymbol:  shipSymbol,
				Opportunity: opportunity,
				PlayerID:    cmd.PlayerID,
				ContainerID: workerID,
			}

			// Create worker container entity
			workerContainer := container.NewContainer(
				workerID,
				container.ContainerTypeArbitrageWorker,
				cmd.PlayerID,
				1, // Single iteration (execute one trade)
				map[string]interface{}{
					"ship_symbol":  shipSymbol,
					"good":         opportunity.Good(),
					"buy_market":   opportunity.BuyMarket(),
					"sell_market":  opportunity.SellMarket(),
					"profit":       opportunity.EstimatedProfit(),
					"margin":       opportunity.ProfitMargin(),
					"container_id": workerID,
				},
				h.clock,
			)

			// Persist worker container BEFORE ship assignment
			err := h.containerRepo.Add(ctx, workerContainer, "arbitrage_worker")
			if err != nil {
				logger.Log("ERROR", fmt.Sprintf("Failed to persist worker container %s: %v", workerID, err), nil)
				return
			}

			// Assign ship to worker (atomic)
			assignment := container.NewShipAssignment(
				shipSymbol,
				cmd.PlayerID,
				workerID,
				h.clock,
			)
			err = h.shipAssignmentRepo.Assign(ctx, assignment)
			if err != nil {
				logger.Log("ERROR", fmt.Sprintf("Failed to assign ship %s: %v", shipSymbol, err), nil)
				return
			}

			// Execute worker directly (synchronous execution in this goroutine)
			// This is different from contract coordinator which uses daemon client
			logger.Log("INFO", fmt.Sprintf("Starting arbitrage worker: ship=%s good=%s margin=%.1f%%",
				shipSymbol, opportunity.Good(), opportunity.ProfitMargin()), nil)

			_, err = h.mediator.Send(ctx, workerCmd)

			// Release ship assignment when done
			releaseErr := h.shipAssignmentRepo.Release(ctx, shipSymbol, cmd.PlayerID, "worker_completed")
			if releaseErr != nil {
				logger.Log("ERROR", fmt.Sprintf("Failed to release ship %s: %v", shipSymbol, releaseErr), nil)
			}

			if err != nil {
				logger.Log("ERROR", fmt.Sprintf("Worker failed for ship %s: %v", shipSymbol, err), nil)
				return
			}

			logger.Log("INFO", fmt.Sprintf("Arbitrage worker completed: ship=%s good=%s",
				shipSymbol, opportunity.Good()), nil)

		}(ship, opp)
	}

	// Wait for batch completion
	wg.Wait()

	logger.Log("INFO", fmt.Sprintf("Batch of %d workers completed", numWorkers), nil)
}

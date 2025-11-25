package commands

import (
	"context"
	"fmt"
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

// RunManufacturingCoordinatorCommand coordinates parallel manufacturing arbitrage operations
type RunManufacturingCoordinatorCommand struct {
	SystemSymbol     string // System to scan for opportunities
	PlayerID         int    // Player identifier
	ContainerID      string // Container ID for this coordinator
	MinPurchasePrice int    // Minimum purchase price threshold (default 1000)
	MaxWorkers       int    // Maximum parallel workers (default 5)
	MaxOpportunities int    // Maximum opportunities to track (default 10)
	MinBalance       int    // Minimum credit balance to maintain (default 0 = no limit)
}

// RunManufacturingCoordinatorResponse is never returned (infinite loop)
type RunManufacturingCoordinatorResponse struct {
	// Never returns
}

// RunManufacturingCoordinatorHandler orchestrates parallel manufacturing arbitrage operations.
//
// Pattern: Fleet Coordinator with parallel execution
//
// Workflow:
//  1. Scan for high-demand manufacturing opportunities (every 2 minutes)
//  2. Discover idle ships (every 30 seconds)
//  3. Spawn workers for each ship/opportunity pair
//  4. Workers execute in parallel (goroutines)
//  5. Wait for worker completion
//  6. Repeat
//
// Key difference from arbitrage: Manufacturing takes longer (30-60+ min vs 5-15 min),
// so we scan less frequently and allow fewer parallel workers to avoid resource exhaustion.
type RunManufacturingCoordinatorHandler struct {
	demandFinder       *services.ManufacturingDemandFinder
	shipRepo           navigation.ShipRepository
	shipAssignmentRepo container.ShipAssignmentRepository
	containerRepo      ContainerRepository
	daemonClient       daemon.DaemonClient
	mediator           common.Mediator
	clock              shared.Clock
}

// NewRunManufacturingCoordinatorHandler creates a new coordinator handler
func NewRunManufacturingCoordinatorHandler(
	demandFinder *services.ManufacturingDemandFinder,
	shipRepo navigation.ShipRepository,
	shipAssignmentRepo container.ShipAssignmentRepository,
	containerRepo ContainerRepository,
	daemonClient daemon.DaemonClient,
	mediator common.Mediator,
	clock shared.Clock,
) *RunManufacturingCoordinatorHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}

	return &RunManufacturingCoordinatorHandler{
		demandFinder:       demandFinder,
		shipRepo:           shipRepo,
		shipAssignmentRepo: shipAssignmentRepo,
		containerRepo:      containerRepo,
		daemonClient:       daemonClient,
		mediator:           mediator,
		clock:              clock,
	}
}

// Handle executes the coordinator command
func (h *RunManufacturingCoordinatorHandler) Handle(
	ctx context.Context,
	request common.Request,
) (common.Response, error) {
	cmd, ok := request.(*RunManufacturingCoordinatorCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	logger := common.LoggerFromContext(ctx)

	// Apply defaults
	minPurchasePrice := cmd.MinPurchasePrice
	if minPurchasePrice <= 0 {
		minPurchasePrice = 1000 // Default minimum purchase price
	}

	maxWorkers := cmd.MaxWorkers
	if maxWorkers <= 0 {
		maxWorkers = 5 // Default max 5 parallel workers (manufacturing is slow)
	}

	maxOpportunities := cmd.MaxOpportunities
	if maxOpportunities <= 0 {
		maxOpportunities = 10 // Default top 10 opportunities
	}

	logger.Log("INFO", "Starting manufacturing coordinator", map[string]interface{}{
		"system":             cmd.SystemSymbol,
		"min_purchase_price": minPurchasePrice,
		"max_workers":        maxWorkers,
		"max_opportunities":  maxOpportunities,
	})

	// Main coordination loop (infinite)
	// Manufacturing takes longer than arbitrage, so we scan less frequently
	opportunityScanInterval := 2 * time.Minute
	shipDiscoveryInterval := 30 * time.Second

	opportunityTicker := time.NewTicker(opportunityScanInterval)
	shipDiscoveryTicker := time.NewTicker(shipDiscoveryInterval)
	defer opportunityTicker.Stop()
	defer shipDiscoveryTicker.Stop()

	var opportunities []*trading.ManufacturingOpportunity
	var idleShips []string

	// Track active workers in memory
	// Initialize from ship assignments (handles daemon restart recovery)
	activeWorkers := h.countActiveShipAssignments(ctx, cmd.ContainerID, cmd.PlayerID)
	workerCompletionChan := make(chan string, maxWorkers*2) // Buffer for completions

	logger.Log("INFO", "Initialized active workers count", map[string]interface{}{
		"active_workers": activeWorkers,
		"max_workers":    maxWorkers,
	})

	// Initial scan
	h.scanOpportunities(ctx, cmd, minPurchasePrice, maxOpportunities, &opportunities)

	for {
		select {
		case <-opportunityTicker.C:
			// Scan for opportunities
			h.scanOpportunities(ctx, cmd, minPurchasePrice, maxOpportunities, &opportunities)

		case <-shipDiscoveryTicker.C:
			// Discover idle ships
			h.discoverIdleShips(ctx, cmd, &idleShips)

			// Spawn workers if we have both ships and opportunities
			if len(idleShips) > 0 && len(opportunities) > 0 {
				spawned := h.spawnWorkers(ctx, cmd, idleShips, opportunities, maxWorkers, activeWorkers, workerCompletionChan)
				activeWorkers += spawned
				// Clear idle ships after spawning (they're now assigned)
				idleShips = nil
			}

		case workerID := <-workerCompletionChan:
			// Worker completed - decrement counter
			activeWorkers--
			logger.Log("INFO", "Manufacturing worker completed, slot freed", map[string]interface{}{
				"worker_id":      workerID,
				"active_workers": activeWorkers,
				"max_workers":    maxWorkers,
			})

		case <-ctx.Done():
			// Graceful shutdown
			logger.Log("INFO", "Manufacturing coordinator shutting down", nil)
			return &RunManufacturingCoordinatorResponse{}, nil
		}
	}
}

// scanOpportunities scans markets for manufacturing opportunities
func (h *RunManufacturingCoordinatorHandler) scanOpportunities(
	ctx context.Context,
	cmd *RunManufacturingCoordinatorCommand,
	minPurchasePrice int,
	maxOpportunities int,
	opportunities *[]*trading.ManufacturingOpportunity,
) {
	logger := common.LoggerFromContext(ctx)

	logger.Log("INFO", "Scanning for manufacturing opportunities", map[string]interface{}{
		"system": cmd.SystemSymbol,
	})

	config := services.DemandFinderConfig{
		MinPurchasePrice: minPurchasePrice,
		MaxOpportunities: maxOpportunities,
	}

	opps, err := h.demandFinder.FindHighDemandManufacturables(
		ctx,
		cmd.SystemSymbol,
		cmd.PlayerID,
		config,
	)
	if err != nil {
		logger.Log("ERROR", fmt.Sprintf("Failed to scan opportunities: %v", err), nil)
		return
	}

	*opportunities = opps
	logger.Log("INFO", fmt.Sprintf("Found %d manufacturing opportunities", len(opps)), map[string]interface{}{
		"system": cmd.SystemSymbol,
		"count":  len(opps),
	})

	// Log top opportunities for visibility
	for i, opp := range opps {
		if i >= 3 {
			break // Only show top 3
		}
		logger.Log("INFO", fmt.Sprintf("Opportunity #%d: %s at %s (price=%d, score=%.1f, depth=%d)",
			i+1, opp.Good(), opp.SellMarket().Symbol, opp.PurchasePrice(), opp.Score(), opp.TreeDepth()), nil)
	}
}

// discoverIdleShips discovers available idle ships
func (h *RunManufacturingCoordinatorHandler) discoverIdleShips(
	ctx context.Context,
	cmd *RunManufacturingCoordinatorCommand,
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

// spawnWorkers spawns parallel workers for available ships and opportunities.
// Uses score-first assignment: for each opportunity (sorted by score),
// assigns the closest available ship.
// Returns the number of workers successfully spawned.
func (h *RunManufacturingCoordinatorHandler) spawnWorkers(
	ctx context.Context,
	cmd *RunManufacturingCoordinatorCommand,
	idleShips []string,
	opportunities []*trading.ManufacturingOpportunity,
	maxWorkers int,
	activeWorkers int,
	completionChan chan<- string,
) int {
	logger := common.LoggerFromContext(ctx)

	// Enforce global maxWorkers limit using in-memory counter
	availableSlots := maxWorkers - activeWorkers
	if availableSlots <= 0 {
		logger.Log("INFO", "Max workers limit reached, skipping spawn", map[string]interface{}{
			"max_workers":    maxWorkers,
			"active_workers": activeWorkers,
		})
		return 0
	}

	// Load full ship entities to get their locations
	playerID := shared.MustNewPlayerID(cmd.PlayerID)
	shipEntities := make(map[string]*navigation.Ship)
	for _, shipSymbol := range idleShips {
		ship, err := h.shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
		if err != nil {
			logger.Log("ERROR", fmt.Sprintf("Failed to load ship %s: %v", shipSymbol, err), nil)
			continue
		}
		shipEntities[shipSymbol] = ship
	}

	// Assign ships to opportunities using score-first algorithm:
	// For each opportunity (sorted by score), find the closest ship
	type assignment struct {
		shipSymbol  string
		opportunity *trading.ManufacturingOpportunity
		distance    float64
	}

	var assignments []assignment
	availableShips := make(map[string]*navigation.Ship)
	for k, v := range shipEntities {
		availableShips[k] = v
	}

	// Limit to min(ships, opportunities, availableSlots)
	maxAssignments := len(availableShips)
	if maxAssignments > len(opportunities) {
		maxAssignments = len(opportunities)
	}
	if maxAssignments > availableSlots {
		maxAssignments = availableSlots
	}

	// For each opportunity (in order of score), assign closest ship
	// Note: We use the dependency tree root's market as the starting point
	for i := 0; i < len(opportunities) && len(assignments) < maxAssignments; i++ {
		opp := opportunities[i]

		// For manufacturing, the ship starts by gathering inputs
		// Use the sell market as a reference point for distance calculation
		sellMarket := opp.SellMarket()

		// Find closest available ship to the sell market
		var closestShip string
		var closestDistance float64 = -1

		for shipSymbol, ship := range availableShips {
			distance := ship.CurrentLocation().DistanceTo(sellMarket)
			if closestDistance < 0 || distance < closestDistance {
				closestDistance = distance
				closestShip = shipSymbol
			}
		}

		if closestShip == "" {
			break // No more ships available
		}

		// Assign ship to this opportunity
		assignments = append(assignments, assignment{
			shipSymbol:  closestShip,
			opportunity: opp,
			distance:    closestDistance,
		})

		// Remove ship from available pool
		delete(availableShips, closestShip)

		logger.Log("INFO", fmt.Sprintf("Assigned ship %s to manufacturing opportunity %s (distance: %.1f, score: %.1f)",
			closestShip, opp.Good(), closestDistance, opp.Score()), map[string]interface{}{
			"ship":        closestShip,
			"good":        opp.Good(),
			"sell_market": opp.SellMarket().Symbol,
			"distance":    closestDistance,
			"score":       opp.Score(),
		})
	}

	logger.Log("INFO", fmt.Sprintf("Spawning %d manufacturing workers with optimal assignments", len(assignments)), map[string]interface{}{
		"workers": len(assignments),
	})

	// Sequential container lifecycle: Persist → Assign → Start
	spawnedCount := 0

	for _, assign := range assignments {
		shipSymbol := assign.shipSymbol
		opportunity := assign.opportunity

		// Create worker container ID
		workerID := fmt.Sprintf("manufacturing-worker-%s-%s", shipSymbol, uuid.New().String()[:8])

		// Create worker command
		workerCmd := &RunManufacturingWorkerCommand{
			ShipSymbol:    shipSymbol,
			Opportunity:   opportunity,
			PlayerID:      cmd.PlayerID,
			ContainerID:   workerID,
			CoordinatorID: cmd.ContainerID,
			SystemSymbol:  cmd.SystemSymbol,
		}

		// Step 1: Persist worker container (via DaemonClient)
		err := h.daemonClient.PersistManufacturingWorkerContainer(ctx, workerID, uint(cmd.PlayerID), workerCmd)
		if err != nil {
			logger.Log("ERROR", fmt.Sprintf("Failed to persist worker container %s: %v", workerID, err), nil)
			continue // Skip this ship
		}

		// Step 2: Assign ship to worker (synchronous - prevents race condition)
		assignment := container.NewShipAssignment(
			shipSymbol,
			cmd.PlayerID,
			workerID,
			h.clock,
		)
		err = h.shipAssignmentRepo.Assign(ctx, assignment)
		if err != nil {
			logger.Log("ERROR", fmt.Sprintf("Failed to assign ship %s: %v", shipSymbol, err), nil)
			continue // Skip this ship (may already be assigned by another coordinator)
		}

		// Step 3: Start container with completion callback
		err = h.daemonClient.StartManufacturingWorkerContainer(ctx, workerID, completionChan)
		if err != nil {
			logger.Log("ERROR", fmt.Sprintf("Failed to start worker container %s: %v", workerID, err), nil)
			// Release assignment on failure (ship returns to idle pool)
			_ = h.shipAssignmentRepo.Release(ctx, shipSymbol, cmd.PlayerID, "worker_start_failed")
			continue
		}

		spawnedCount++
		logger.Log("INFO", fmt.Sprintf("Started manufacturing worker: ship=%s good=%s containerID=%s score=%.1f",
			shipSymbol, opportunity.Good(), workerID, opportunity.Score()), nil)
	}

	logger.Log("INFO", fmt.Sprintf("Started %d manufacturing workers via ContainerRunner", spawnedCount), map[string]interface{}{
		"spawned":        spawnedCount,
		"active_workers": activeWorkers + spawnedCount,
		"max_workers":    maxWorkers,
	})

	return spawnedCount
}

// countActiveShipAssignments counts ships assigned to manufacturing workers
// Used to initialize active worker count after daemon restart
func (h *RunManufacturingCoordinatorHandler) countActiveShipAssignments(
	ctx context.Context,
	coordinatorID string,
	playerID int,
) int {
	// Count all ships assigned to containers with "manufacturing-worker-" prefix
	count, err := h.shipAssignmentRepo.CountByContainerPrefix(ctx, "manufacturing-worker-", playerID)
	if err != nil {
		return 0
	}
	return count
}

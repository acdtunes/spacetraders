package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/contract"
	playerQueries "github.com/andrescamacho/spacetraders-go/internal/application/player/queries"
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
	UpdateStatus(ctx context.Context, containerID string, playerID int, status container.ContainerStatus, stoppedAt *time.Time, exitCode *int, exitReason string) error
}

// RunArbitrageCoordinatorCommand coordinates parallel arbitrage trading operations
type RunArbitrageCoordinatorCommand struct {
	SystemSymbol string  // System to scan for opportunities
	PlayerID     int     // Player identifier
	ContainerID  string  // Container ID for this coordinator
	MinMargin    float64 // Minimum profit margin threshold (default 10.0%)
	MaxWorkers   int     // Maximum parallel workers (default 10)
	MinBalance   int     // Minimum credit balance to maintain (default 0 = no limit)
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

	// NOTE: Cargo recovery is now handled by individual workers at startup.
	// Workers check for existing cargo and either sell (if value >= 10K) or jettison it.

	// Main coordination loop (infinite)
	opportunityScanInterval := 2 * time.Minute
	shipDiscoveryInterval := 30 * time.Second

	opportunityTicker := time.NewTicker(opportunityScanInterval)
	shipDiscoveryTicker := time.NewTicker(shipDiscoveryInterval)
	defer opportunityTicker.Stop()
	defer shipDiscoveryTicker.Stop()

	var opportunities []*trading.ArbitrageOpportunity
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
				spawned := h.spawnWorkers(ctx, cmd, idleShips, opportunities, maxWorkers, activeWorkers, workerCompletionChan)
				activeWorkers += spawned
				// Clear idle ships after spawning (they're now assigned)
				idleShips = nil
			}

		case workerID := <-workerCompletionChan:
			// Worker completed - decrement counter
			activeWorkers--
			logger.Log("INFO", "Worker completed, slot freed", map[string]interface{}{
				"worker_id":      workerID,
				"active_workers": activeWorkers,
				"max_workers":    maxWorkers,
			})

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

// spawnWorkers spawns parallel workers for available ships and opportunities.
// Uses profit-first assignment: for each opportunity (sorted by profitability),
// assigns the closest available ship to minimize travel costs.
// Returns the number of workers successfully spawned.
func (h *RunArbitrageCoordinatorHandler) spawnWorkers(
	ctx context.Context,
	cmd *RunArbitrageCoordinatorCommand,
	idleShips []string,
	opportunities []*trading.ArbitrageOpportunity,
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

	// Check credit balance guardrail
	if cmd.MinBalance > 0 {
		playerID := cmd.PlayerID
		getPlayerQuery := &playerQueries.GetPlayerQuery{
			PlayerID: &playerID,
		}

		resp, err := h.mediator.Send(ctx, getPlayerQuery)
		if err != nil {
			logger.Log("ERROR", fmt.Sprintf("Failed to query player balance: %v", err), nil)
			// Skip spawning workers if we can't check balance
			return 0
		}

		playerResp, ok := resp.(*playerQueries.GetPlayerResponse)
		if !ok {
			logger.Log("ERROR", "Invalid response from GetPlayerQuery", nil)
			return 0
		}

		currentBalance := playerResp.Player.Credits

		// Check if balance is below minimum threshold
		if currentBalance < cmd.MinBalance {
			logger.Log("WARN", fmt.Sprintf("Credit balance %d below minimum threshold %d - skipping worker spawn to preserve funds",
				currentBalance, cmd.MinBalance), map[string]interface{}{
				"current_balance": currentBalance,
				"min_balance":     cmd.MinBalance,
				"deficit":         cmd.MinBalance - currentBalance,
			})
			return 0
		}

		// Log if approaching threshold (within 20%)
		threshold := float64(cmd.MinBalance) * 1.2
		if float64(currentBalance) < threshold {
			logger.Log("WARN", fmt.Sprintf("Credit balance %d approaching minimum threshold %d",
				currentBalance, cmd.MinBalance), map[string]interface{}{
				"current_balance": currentBalance,
				"min_balance":     cmd.MinBalance,
				"margin":          currentBalance - cmd.MinBalance,
			})
		}
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

	// Assign ships to opportunities using profit-first algorithm:
	// For each opportunity (sorted by profitability), find the closest ship
	type assignment struct {
		shipSymbol string
		opportunity *trading.ArbitrageOpportunity
		distance   float64
	}

	var assignments []assignment
	availableShips := make(map[string]*navigation.Ship)
	for k, v := range shipEntities {
		availableShips[k] = v
	}

	// Limit to min(ships, opportunities, availableSlots)
	// availableSlots = maxWorkers - currentlyRunning (enforces global limit)
	maxAssignments := len(availableShips)
	if maxAssignments > len(opportunities) {
		maxAssignments = len(opportunities)
	}
	if maxAssignments > availableSlots {
		maxAssignments = availableSlots
	}

	// For each opportunity (in order of profitability), assign closest ship
	for i := 0; i < len(opportunities) && len(assignments) < maxAssignments; i++ {
		opp := opportunities[i]
		buyMarket := opp.BuyMarket()

		// Find closest available ship to the buy market
		var closestShip string
		var closestDistance float64 = -1

		for shipSymbol, ship := range availableShips {
			distance := ship.CurrentLocation().DistanceTo(buyMarket)
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
			shipSymbol: closestShip,
			opportunity: opp,
			distance:   closestDistance,
		})

		// Remove ship from available pool
		delete(availableShips, closestShip)

		logger.Log("INFO", fmt.Sprintf("Assigned ship %s to opportunity %s (distance: %.1f, margin: %.1f%%)",
			closestShip, opp.Good(), closestDistance, opp.ProfitMargin()), map[string]interface{}{
			"ship": closestShip,
			"good": opp.Good(),
			"buy_market": opp.BuyMarket().Symbol,
			"distance": closestDistance,
			"margin": opp.ProfitMargin(),
		})
	}

	logger.Log("INFO", fmt.Sprintf("Spawning %d arbitrage workers with optimal assignments", len(assignments)), map[string]interface{}{
		"workers": len(assignments),
	})

	// Sequential container lifecycle: Persist → Assign → Start
	// Track successfully spawned workers
	spawnedCount := 0

	for _, assign := range assignments {
		shipSymbol := assign.shipSymbol
		opportunity := assign.opportunity

		// Create worker container ID
		workerID := fmt.Sprintf("arbitrage-worker-%s-%s", shipSymbol, uuid.New().String()[:8])

		// Create worker command
		workerCmd := &RunArbitrageWorkerCommand{
			ShipSymbol:    shipSymbol,
			Opportunity:   opportunity,
			PlayerID:      cmd.PlayerID,
			ContainerID:   workerID,
			CoordinatorID: cmd.ContainerID, // Link worker to parent coordinator
			MinBalance:    cmd.MinBalance,
			SystemSymbol:  cmd.SystemSymbol, // For cargo recovery market lookups
		}

		// Step 1: Persist worker container (via DaemonClient)
		err := h.daemonClient.PersistArbitrageWorkerContainer(ctx, workerID, uint(cmd.PlayerID), workerCmd)
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
		err = h.daemonClient.StartArbitrageWorkerContainer(ctx, workerID, completionChan)
		if err != nil {
			logger.Log("ERROR", fmt.Sprintf("Failed to start worker container %s: %v", workerID, err), nil)
			// Release assignment on failure (ship returns to idle pool)
			_ = h.shipAssignmentRepo.Release(ctx, shipSymbol, cmd.PlayerID, "worker_start_failed")
			continue
		}

		spawnedCount++
		logger.Log("INFO", fmt.Sprintf("Started arbitrage worker: ship=%s good=%s containerID=%s margin=%.1f%%",
			shipSymbol, opportunity.Good(), workerID, opportunity.ProfitMargin()), nil)
	}

	logger.Log("INFO", fmt.Sprintf("Started %d workers via ContainerRunner", spawnedCount), map[string]interface{}{
		"spawned":        spawnedCount,
		"active_workers": activeWorkers + spawnedCount,
		"max_workers":    maxWorkers,
	})

	return spawnedCount
}

// countActiveShipAssignments counts ships assigned to this coordinator's workers
// Used to initialize active worker count after daemon restart
func (h *RunArbitrageCoordinatorHandler) countActiveShipAssignments(
	ctx context.Context,
	coordinatorID string,
	playerID int,
) int {
	assignments, err := h.shipAssignmentRepo.FindByContainer(ctx, coordinatorID, playerID)
	if err != nil {
		return 0
	}

	count := 0
	for _, a := range assignments {
		if a.Status() != "idle" {
			count++
		}
	}
	return count
}


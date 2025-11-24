package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/contract"
	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	playerQueries "github.com/andrescamacho/spacetraders-go/internal/application/player/queries"
	shipCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	"github.com/andrescamacho/spacetraders-go/internal/application/trading/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
	"github.com/google/uuid"
)

// dbLogger writes logs to both stdout and the container log database
// Implements the logging.ContainerLogger interface
type dbLogger struct {
	containerID string
	playerID    int
	logRepo     persistence.ContainerLogRepository
}

// Log implements the ContainerLogger interface
// Writes to stdout immediately and persists to database asynchronously
func (d *dbLogger) Log(level, message string, metadata map[string]interface{}) {
	timestamp := time.Now().Format(time.RFC3339)

	// Print to stdout immediately (for real-time monitoring)
	fmt.Printf("[%s] [%s] %s: %s\n", timestamp, d.containerID, level, message)
	if metadata != nil && len(metadata) > 0 {
		fmt.Printf("[%s] [%s]   metadata: %+v\n", timestamp, d.containerID, metadata)
	}

	// Persist to database asynchronously (matches ContainerRunner pattern)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := d.logRepo.Log(ctx, d.containerID, d.playerID, message, level, metadata); err != nil {
			// Log error to stdout if DB write fails (but don't block execution)
			fmt.Printf("[%s] [%s] ERROR: Failed to persist log to DB: %v\n",
				time.Now().Format(time.RFC3339),
				d.containerID,
				err,
			)
		}
	}()
}

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
	logRepo            persistence.ContainerLogRepository
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
	logRepo persistence.ContainerLogRepository,
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
		logRepo:            logRepo,
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

	// Resume any interrupted trades (ships with cargo from interrupted workers)
	h.resumeInterruptedTrades(ctx, cmd)

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

// spawnWorkers spawns parallel workers for available ships and opportunities.
// Uses profit-first assignment: for each opportunity (sorted by profitability),
// assigns the closest available ship to minimize travel costs.
func (h *RunArbitrageCoordinatorHandler) spawnWorkers(
	ctx context.Context,
	cmd *RunArbitrageCoordinatorCommand,
	idleShips []string,
	opportunities []*trading.ArbitrageOpportunity,
	maxWorkers int,
) {
	logger := common.LoggerFromContext(ctx)

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
			return
		}

		playerResp, ok := resp.(*playerQueries.GetPlayerResponse)
		if !ok {
			logger.Log("ERROR", "Invalid response from GetPlayerQuery", nil)
			return
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
			return
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

	// Limit to min(ships, opportunities, maxWorkers)
	maxAssignments := len(availableShips)
	if maxAssignments > len(opportunities) {
		maxAssignments = len(opportunities)
	}
	if maxAssignments > maxWorkers {
		maxAssignments = maxWorkers
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

	// Pre-assign ships synchronously BEFORE spawning goroutines to prevent race condition
	// where next discovery cycle picks up same ships before workers mark them as assigned
	type workerSetup struct {
		shipSymbol string
		workerID   string
		workerCmd  *RunArbitrageWorkerCommand
	}
	var readyWorkers []workerSetup

	for _, assign := range assignments {
		shipSymbol := assign.shipSymbol
		opportunity := assign.opportunity

		// Create worker container ID
		workerID := fmt.Sprintf("arbitrage-worker-%s-%s", shipSymbol, uuid.New().String()[:8])

		// Create worker container entity with parent coordinator ID
		parentContainerID := cmd.ContainerID
		workerContainer := container.NewContainer(
			workerID,
			container.ContainerTypeArbitrageWorker,
			cmd.PlayerID,
			1,                  // Single iteration (execute one trade)
			&parentContainerID, // Link to parent coordinator for cascading stop
			map[string]interface{}{
				"ship_symbol": shipSymbol,
				"good":        opportunity.Good(),
				"buy_market":  opportunity.BuyMarket(),
				"sell_market": opportunity.SellMarket(),
				"profit":      opportunity.EstimatedProfit(),
				"margin":      opportunity.ProfitMargin(),
			},
			h.clock,
		)

		// Persist worker container
		err := h.containerRepo.Add(ctx, workerContainer, "arbitrage_worker")
		if err != nil {
			logger.Log("ERROR", fmt.Sprintf("Failed to persist worker container %s: %v", workerID, err), nil)
			continue // Skip this ship
		}

		// Assign ship to worker (synchronous - prevents race condition)
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

		// Create worker command
		workerCmd := &RunArbitrageWorkerCommand{
			ShipSymbol:  shipSymbol,
			Opportunity: opportunity,
			PlayerID:    cmd.PlayerID,
			ContainerID: workerID,
			MinBalance:  cmd.MinBalance,
		}

		// Ship is now assigned - safe to spawn worker
		readyWorkers = append(readyWorkers, workerSetup{
			shipSymbol: shipSymbol,
			workerID:   workerID,
			workerCmd:  workerCmd,
		})

		logger.Log("INFO", fmt.Sprintf("Assigned ship %s to worker %s (good=%s margin=%.1f%%)",
			shipSymbol, workerID, opportunity.Good(), opportunity.ProfitMargin()), nil)
	}

	// Now spawn workers in parallel - all ships are already marked as assigned
	for _, setup := range readyWorkers {
		go func(shipSymbol string, workerID string, workerCmd *RunArbitrageWorkerCommand) {

			// Ensure ship assignment is ALWAYS released, even on early exit or interruption
			defer func() {
				releaseErr := h.shipAssignmentRepo.Release(ctx, shipSymbol, cmd.PlayerID, "worker_completed")
				if releaseErr != nil {
					logger.Log("ERROR", fmt.Sprintf("Failed to release ship %s: %v", shipSymbol, releaseErr), nil)
				}
			}()

			// Execute worker directly (synchronous execution in this goroutine)
			logger.Log("INFO", fmt.Sprintf("Starting arbitrage worker: ship=%s good=%s",
				shipSymbol, workerCmd.Opportunity.Good()), nil)

			// Mark container as RUNNING
			if err := h.containerRepo.UpdateStatus(ctx, workerID, cmd.PlayerID, container.ContainerStatusRunning, nil, nil, ""); err != nil {
				logger.Log("ERROR", fmt.Sprintf("Failed to update container status to RUNNING: %v", err), nil)
			}

			// Create worker-specific context with database-backed logger
			workerLogger := &dbLogger{
				containerID: workerID,
				playerID:    cmd.PlayerID,
				logRepo:     h.logRepo,
			}
			workerCtx := logging.WithLogger(ctx, workerLogger)

			_, err := h.mediator.Send(workerCtx, workerCmd)

			// Mark container as COMPLETED or FAILED
			completedAt := h.clock.Now()
			if err != nil {
				logger.Log("ERROR", fmt.Sprintf("Worker failed for ship %s: %v", shipSymbol, err), nil)
				exitCode := 1
				if updateErr := h.containerRepo.UpdateStatus(ctx, workerID, cmd.PlayerID, container.ContainerStatusFailed, &completedAt, &exitCode, err.Error()); updateErr != nil {
					logger.Log("ERROR", fmt.Sprintf("Failed to update container status to FAILED: %v", updateErr), nil)
				}
				return
			}

			logger.Log("INFO", fmt.Sprintf("Arbitrage worker completed: ship=%s good=%s",
				shipSymbol, workerCmd.Opportunity.Good()), nil)

			exitCode := 0
			if updateErr := h.containerRepo.UpdateStatus(ctx, workerID, cmd.PlayerID, container.ContainerStatusCompleted, &completedAt, &exitCode, "success"); updateErr != nil {
				logger.Log("ERROR", fmt.Sprintf("Failed to update container status to COMPLETED: %v", updateErr), nil)
			}

		}(setup.shipSymbol, setup.workerID, setup.workerCmd)
	}

	logger.Log("INFO", fmt.Sprintf("Spawned %d workers asynchronously (all ships pre-assigned)", len(readyWorkers)), nil)
}

// resumeInterruptedTrades finds ships with cargo from interrupted workers and completes their trades
func (h *RunArbitrageCoordinatorHandler) resumeInterruptedTrades(
	ctx context.Context,
	cmd *RunArbitrageCoordinatorCommand,
) {
	logger := common.LoggerFromContext(ctx)
	playerIDValue := shared.MustNewPlayerID(cmd.PlayerID)

	logger.Log("INFO", "Checking for interrupted trades to resume", nil)

	// Get all light hauler ships
	_, allShips, err := contract.FindIdleLightHaulers(ctx, playerIDValue, h.shipRepo, h.shipAssignmentRepo)
	if err != nil {
		logger.Log("ERROR", fmt.Sprintf("Failed to find ships for recovery: %v", err), nil)
		return
	}

	var shipsWithCargo []string

	// Find ships with cargo
	for _, shipSymbol := range allShips {
		ship, err := h.shipRepo.FindBySymbol(ctx, shipSymbol, playerIDValue)
		if err != nil {
			logger.Log("ERROR", fmt.Sprintf("Failed to load ship %s: %v", shipSymbol, err), nil)
			continue
		}

		if ship.CargoUnits() > 0 {
			shipsWithCargo = append(shipsWithCargo, shipSymbol)
			logger.Log("INFO", fmt.Sprintf("Found interrupted trade: ship=%s cargo=%d/%d",
				shipSymbol, ship.CargoUnits(), ship.CargoCapacity()), map[string]interface{}{
				"ship": shipSymbol,
				"cargo_units": ship.CargoUnits(),
			})
		}
	}

	if len(shipsWithCargo) == 0 {
		logger.Log("INFO", "No interrupted trades found", nil)
		return
	}

	logger.Log("INFO", fmt.Sprintf("Resuming %d interrupted trades", len(shipsWithCargo)), map[string]interface{}{
		"count": len(shipsWithCargo),
	})

	// Resume trades sequentially to avoid overwhelming the system
	for _, shipSymbol := range shipsWithCargo {
		h.resumeTrade(ctx, cmd, shipSymbol, playerIDValue)
	}

	logger.Log("INFO", "Finished resuming interrupted trades", nil)
}

// resumeTrade completes an interrupted arbitrage trade by selling cargo
func (h *RunArbitrageCoordinatorHandler) resumeTrade(
	ctx context.Context,
	cmd *RunArbitrageCoordinatorCommand,
	shipSymbol string,
	playerID shared.PlayerID,
) {
	logger := common.LoggerFromContext(ctx)

	// Load ship to get cargo details
	ship, err := h.shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
	if err != nil {
		logger.Log("ERROR", fmt.Sprintf("Failed to load ship %s for recovery: %v", shipSymbol, err), nil)
		return
	}

	if ship.CargoUnits() == 0 {
		return // No cargo to sell
	}

	// Get cargo item (assume single item for arbitrage trades)
	cargoItems := ship.Cargo().Inventory
	if len(cargoItems) == 0 {
		return
	}

	good := cargoItems[0].Symbol
	units := cargoItems[0].Units

	logger.Log("INFO", fmt.Sprintf("Resuming trade for ship %s: selling %d units of %s",
		shipSymbol, units, good), map[string]interface{}{
		"ship":  shipSymbol,
		"good":  good,
		"units": units,
	})

	// Find best sell market in the system for this good
	// Use opportunity finder to get current market data
	opps, err := h.opportunityFinder.FindOpportunities(
		ctx,
		cmd.SystemSymbol,
		cmd.PlayerID,
		ship.CargoCapacity(),
		0.01, // Very low minimum margin for recovery (1%)
		50,   // Get top 50 to find best sell price
	)
	if err != nil {
		logger.Log("ERROR", fmt.Sprintf("Failed to find markets for recovery: %v", err), nil)
		return
	}

	// Find opportunity with this good to get sell market
	var sellMarket string
	var sellPrice int
	for _, opp := range opps {
		if opp.Good() == good {
			sellMarket = opp.SellMarket().Symbol
			sellPrice = opp.SellPrice()
			break
		}
	}

	if sellMarket == "" {
		logger.Log("WARN", fmt.Sprintf("No sell market found for %s, will sell at current location", good), nil)
		// Dock and sell at current location as fallback
		_, err := h.mediator.Send(ctx, &shipTypes.DockShipCommand{
			ShipSymbol: shipSymbol,
			PlayerID:   playerID,
		})
		if err != nil {
			logger.Log("ERROR", fmt.Sprintf("Failed to dock for recovery sell: %v", err), nil)
			return
		}

		_, err = h.mediator.Send(ctx, &shipCmd.SellCargoCommand{
			ShipSymbol: shipSymbol,
			GoodSymbol: good,
			Units:      units,
			PlayerID:   playerID,
		})
		if err != nil {
			logger.Log("ERROR", fmt.Sprintf("Failed to sell cargo for recovery: %v", err), nil)
		} else {
			logger.Log("INFO", fmt.Sprintf("Recovered trade completed at current location: ship=%s good=%s units=%d",
				shipSymbol, good, units), nil)
		}
		return
	}

	logger.Log("INFO", fmt.Sprintf("Navigating to sell market %s (price: %d)", sellMarket, sellPrice), map[string]interface{}{
		"sell_market": sellMarket,
		"sell_price":  sellPrice,
	})

	// Navigate to sell market
	_, err = h.mediator.Send(ctx, &shipCmd.NavigateRouteCommand{
		ShipSymbol:   shipSymbol,
		Destination:  sellMarket,
		PlayerID:     playerID,
		PreferCruise: false,
	})
	if err != nil {
		logger.Log("ERROR", fmt.Sprintf("Failed to navigate to sell market: %v", err), nil)
		return
	}

	// Dock at sell market
	_, err = h.mediator.Send(ctx, &shipTypes.DockShipCommand{
		ShipSymbol: shipSymbol,
		PlayerID:   playerID,
	})
	if err != nil {
		logger.Log("ERROR", fmt.Sprintf("Failed to dock at sell market: %v", err), nil)
		return
	}

	// Sell cargo
	sellResp, err := h.mediator.Send(ctx, &shipCmd.SellCargoCommand{
		ShipSymbol: shipSymbol,
		GoodSymbol: good,
		Units:      units,
		PlayerID:   playerID,
	})
	if err != nil {
		logger.Log("ERROR", fmt.Sprintf("Failed to sell cargo: %v", err), nil)
		return
	}

	if resp, ok := sellResp.(*shipCmd.SellCargoResponse); ok {
		logger.Log("INFO", fmt.Sprintf("Recovered trade completed: ship=%s good=%s units=%d revenue=%d",
			shipSymbol, good, resp.UnitsSold, resp.TotalRevenue), map[string]interface{}{
			"ship":     shipSymbol,
			"good":     good,
			"units":    resp.UnitsSold,
			"revenue":  resp.TotalRevenue,
		})
	}
}

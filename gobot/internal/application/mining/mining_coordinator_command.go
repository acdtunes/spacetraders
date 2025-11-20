package mining

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	appShip "github.com/andrescamacho/spacetraders-go/internal/application/ship"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	domainMining "github.com/andrescamacho/spacetraders-go/internal/domain/mining"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	domainShared "github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// MiningCoordinatorCommand manages a fleet of mining and transport ships
// using the Transport-as-Sink pattern
type MiningCoordinatorCommand struct {
	MiningOperationID string
	PlayerID          int
	AsteroidField     string   // Waypoint symbol (may be empty if MiningType is set)
	MinerShips        []string // Ships for mining
	TransportShips    []string // Ships for transport
	TopNOres          int      // Number of ore types to keep
	ContainerID       string   // Coordinator's own container ID
	MiningType        string   // For auto-selection: common_metals, precious_metals, etc.
	Force             bool     // Override fuel validation warnings
	DryRun            bool     // If true, only plan routes without starting workers
	MaxLegTime        int      // Max time per leg in minutes (0 = no limit)
}

// MiningCoordinatorResponse contains the coordinator execution results
type MiningCoordinatorResponse struct {
	TotalTransfers int
	TotalRevenue   int
	Errors         []string
	// Dry-run results
	AsteroidField string               // Selected asteroid (dry-run)
	MarketSymbol  string               // Market for transport loop (dry-run)
	ShipRoutes    []common.ShipRouteDTO // Planned routes for all ships (dry-run)
}

// TransportRoutePlan contains the planned routes for a transport ship
// This is the shared route planning result used by both dry-run and real mode
type TransportRoutePlan struct {
	ToMarket    *navigation.Route // Current position -> Market (BURN)
	ToAsteroid  *navigation.Route // Market -> Asteroid (CRUISE)
	ToMarketRet *navigation.Route // Asteroid -> Market (CRUISE)
}

// PlanTransportRoute plans the initial route for a transport ship
// Route: current -> market (BURN) -> asteroid (CRUISE) -> market (CRUISE)
// This function is used by both dry-run mode and actual transport workers
func PlanTransportRoute(
	ctx context.Context,
	routePlanner *appShip.RoutePlanner,
	ship *navigation.Ship,
	marketSymbol string,
	asteroidField string,
	waypoints []*system.WaypointData,
	systemSymbol string,
) (*TransportRoutePlan, error) {
	// Convert waypoints to map[string]*shared.Waypoint for RoutePlanner
	waypointMap := make(map[string]*domainShared.Waypoint)
	for _, wp := range waypoints {
		waypointMap[wp.Symbol] = &domainShared.Waypoint{
			Symbol:       wp.Symbol,
			SystemSymbol: systemSymbol,
			X:            wp.X,
			Y:            wp.Y,
			HasFuel:      wp.HasFuel,
		}
	}

	// Route 1: current -> market (BURN for speed, will refuel there)
	// Uses ship's current fuel - RoutePlanner handles refuel stops automatically
	toMarket, err := routePlanner.PlanRoute(ctx, ship, marketSymbol, waypointMap, false)
	if err != nil {
		return nil, fmt.Errorf("failed to plan route to market: %w", err)
	}

	// Route 2: market -> asteroid (CRUISE to preserve fuel for return)
	// Create temporary ship state at market with full fuel
	marketWp := waypointMap[marketSymbol]
	if marketWp == nil {
		return nil, fmt.Errorf("market waypoint %s not found in waypoint map", marketSymbol)
	}
	shipAtMarket := ship.CloneAtLocation(marketWp, ship.Fuel().Capacity)
	toAsteroid, err := routePlanner.PlanRoute(ctx, shipAtMarket, asteroidField, waypointMap, true)
	if err != nil {
		return nil, fmt.Errorf("failed to plan route to asteroid: %w", err)
	}

	// Route 3: asteroid -> market (CRUISE to make it back)
	// Create temporary ship state at asteroid with full fuel
	asteroidWp := waypointMap[asteroidField]
	if asteroidWp == nil {
		return nil, fmt.Errorf("asteroid waypoint %s not found in waypoint map", asteroidField)
	}
	shipAtAsteroid := ship.CloneAtLocation(asteroidWp, ship.Fuel().Capacity)
	toMarketRet, err := routePlanner.PlanRoute(ctx, shipAtAsteroid, marketSymbol, waypointMap, true)
	if err != nil {
		return nil, fmt.Errorf("failed to plan route back to market: %w", err)
	}

	return &TransportRoutePlan{
		ToMarket:    toMarket,
		ToAsteroid:  toAsteroid,
		ToMarketRet: toMarketRet,
	}, nil
}

// MiningMarketRepository defines the market operations needed for mining coordinator
type MiningMarketRepository interface {
	ListMarketsInSystem(ctx context.Context, playerID uint, systemSymbol string, maxAgeMinutes int) ([]market.Market, error)
}

// MiningCoordinatorHandler implements the mining fleet coordinator logic
type MiningCoordinatorHandler struct {
	mediator           common.Mediator
	shipRepo           navigation.ShipRepository
	operationRepo      domainMining.MiningOperationRepository
	shipAssignmentRepo daemon.ShipAssignmentRepository
	daemonClient       daemon.DaemonClient
	routingClient      routing.RoutingClient
	routePlanner       *appShip.RoutePlanner
	graphProvider      system.ISystemGraphProvider
	marketRepo         MiningMarketRepository
	waypointRepo       system.WaypointRepository
}

// NewMiningCoordinatorHandler creates a new mining coordinator handler
func NewMiningCoordinatorHandler(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	operationRepo domainMining.MiningOperationRepository,
	shipAssignmentRepo daemon.ShipAssignmentRepository,
	daemonClient daemon.DaemonClient,
	routingClient routing.RoutingClient,
	routePlanner *appShip.RoutePlanner,
	graphProvider system.ISystemGraphProvider,
	marketRepo MiningMarketRepository,
	waypointRepo system.WaypointRepository,
) *MiningCoordinatorHandler {
	return &MiningCoordinatorHandler{
		mediator:           mediator,
		shipRepo:           shipRepo,
		operationRepo:      operationRepo,
		shipAssignmentRepo: shipAssignmentRepo,
		daemonClient:       daemonClient,
		routingClient:      routingClient,
		routePlanner:       routePlanner,
		graphProvider:      graphProvider,
		marketRepo:         marketRepo,
		waypointRepo:       waypointRepo,
	}
}

// Handle executes the mining coordinator command
func (h *MiningCoordinatorHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	logger := common.LoggerFromContext(ctx)

	cmd, ok := request.(*MiningCoordinatorCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	result := &MiningCoordinatorResponse{
		TotalTransfers: 0,
		TotalRevenue:   0,
		Errors:         []string{},
	}

	// Step 0: Determine asteroid and market
	var marketSymbol string
	if cmd.AsteroidField == "" && cmd.MiningType != "" {
		// Auto-select asteroid AND market together using optimization
		logger.Log("INFO", fmt.Sprintf("Auto-selecting asteroid for mining type: %s", cmd.MiningType), nil)
		selectedAsteroid, selectedMarket, err := h.selectAsteroidAndMarket(
			ctx,
			cmd.MiningType,
			cmd.TransportShips,
			cmd.Force,
			cmd.PlayerID,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to auto-select asteroid: %w", err)
		}
		cmd.AsteroidField = selectedAsteroid
		marketSymbol = selectedMarket
		logger.Log("INFO", fmt.Sprintf("Selected asteroid: %s, market: %s", selectedAsteroid, selectedMarket), nil)
	} else if cmd.AsteroidField != "" {
		// User specified asteroid - just find the closest market
		parts := strings.Split(cmd.AsteroidField, "-")
		if len(parts) < 2 {
			return nil, fmt.Errorf("invalid asteroid waypoint format: %s", cmd.AsteroidField)
		}
		systemSymbol := parts[0] + "-" + parts[1]

		var err error
		marketSymbol, err = h.findClosestMarketWithFuel(ctx, cmd.PlayerID, cmd.AsteroidField, systemSymbol)
		if err != nil {
			return nil, fmt.Errorf("failed to find market for transport loop: %w", err)
		}
		logger.Log("INFO", fmt.Sprintf("Found market for transport loop: %s", marketSymbol), nil)
	} else {
		return nil, fmt.Errorf("no asteroid field specified and no mining type provided for auto-selection")
	}

	// Dry-run mode: plan routes for all ships and return without starting workers
	if cmd.DryRun {
		return h.planDryRunRoutes(ctx, cmd, logger)
	}

	// Step 1: Create or load mining operation
	operation, err := h.getOrCreateOperation(ctx, cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to get/create operation: %w", err)
	}

	// Step 2: Create ship pool assignments
	logger.Log("INFO", fmt.Sprintf("Creating ship pool: %d miners, %d transports",
		len(cmd.MinerShips), len(cmd.TransportShips)), nil)

	allShips := append(cmd.MinerShips, cmd.TransportShips...)
	if err := h.createPoolAssignments(ctx, allShips, cmd.ContainerID, cmd.PlayerID); err != nil {
		return nil, fmt.Errorf("failed to create pool assignments: %w", err)
	}

	// Step 3: Create coordination channels
	transportAvailabilityChan := make(chan string, len(cmd.TransportShips))
	minerRequestChan := make(chan string, len(cmd.MinerShips))
	transferCompleteChan := make(chan TransferComplete, len(cmd.MinerShips))

	// Per-miner channels for receiving assigned transport
	minerAssignChans := make(map[string]chan string)
	for _, miner := range cmd.MinerShips {
		minerAssignChans[miner] = make(chan string)
	}

	// Per-transport channels for receiving cargo notification
	transportCargoReceivedChans := make(map[string]chan struct{})
	for _, transport := range cmd.TransportShips {
		transportCargoReceivedChans[transport] = make(chan struct{})
	}

	// Track all spawned worker container IDs for cleanup on shutdown
	var workerContainerIDs []string

	// Step 4: Spawn transport workers (they run continuously)
	logger.Log("INFO", "Spawning transport workers...", nil)
	for _, transportShip := range cmd.TransportShips {
		containerID, err := h.spawnTransportWorker(ctx, cmd, transportShip, marketSymbol,
			transportAvailabilityChan, transportCargoReceivedChans[transportShip])
		if err != nil {
			errMsg := fmt.Sprintf("Failed to spawn transport worker for %s: %v", transportShip, err)
			logger.Log("ERROR", errMsg, nil)
			result.Errors = append(result.Errors, errMsg)
		} else {
			workerContainerIDs = append(workerContainerIDs, containerID)
		}
	}

	// Step 5: Spawn mining workers (they run continuously)
	logger.Log("INFO", "Spawning mining workers...", nil)
	for _, minerShip := range cmd.MinerShips {
		containerID, err := h.spawnMiningWorker(ctx, cmd, minerShip,
			minerRequestChan, minerAssignChans[minerShip], transferCompleteChan)
		if err != nil {
			errMsg := fmt.Sprintf("Failed to spawn mining worker for %s: %v", minerShip, err)
			logger.Log("ERROR", errMsg, nil)
			result.Errors = append(result.Errors, errMsg)
		} else {
			workerContainerIDs = append(workerContainerIDs, containerID)
		}
	}

	// Step 6: Main coordination loop
	logger.Log("INFO", "Starting coordination loop", nil)

	// Available transport pool
	availableTransports := []string{}

	// Track cargo levels of each transport (to fill one completely before next)
	transportCargoLevels := make(map[string]int)

	// Queue of miners waiting for transport
	waitingMiners := []string{}

	for {
		select {
		case <-ctx.Done():
			// Context cancelled, stop all workers and cleanup
			logger.Log("INFO", "Coordinator shutdown requested", nil)
			// Stop all worker containers
			for _, containerID := range workerContainerIDs {
				logger.Log("INFO", fmt.Sprintf("Stopping worker container: %s", containerID), nil)
				_ = h.daemonClient.StopContainer(ctx, containerID)
			}
			h.releasePoolAssignments(ctx, cmd.ContainerID, cmd.PlayerID)
			operation.Stop()
			h.operationRepo.Update(ctx, operation)
			return result, ctx.Err()

		case transportSymbol := <-transportAvailabilityChan:
			// Transport signaled availability - fetch its current cargo level
			if transportShip, err := h.shipRepo.FindBySymbol(ctx, transportSymbol, cmd.PlayerID); err == nil {
				transportCargoLevels[transportSymbol] = transportShip.Cargo().Units
			}
			logger.Log("DEBUG", fmt.Sprintf("Transport %s available (pool size: %d, cargo: %d)",
				transportSymbol, len(availableTransports)+1, transportCargoLevels[transportSymbol]), nil)

			// Check if there are waiting miners
			if len(waitingMiners) > 0 {
				// Match immediately with first waiting miner
				minerSymbol := waitingMiners[0]
				waitingMiners = waitingMiners[1:]

				// Send transport to miner
				select {
				case minerAssignChans[minerSymbol] <- transportSymbol:
					logger.Log("INFO", fmt.Sprintf("Assigned transport %s to waiting miner %s", transportSymbol, minerSymbol), nil)
				case <-ctx.Done():
					return result, ctx.Err()
				}
			} else {
				// Add to available pool
				availableTransports = append(availableTransports, transportSymbol)
			}

		case minerSymbol := <-minerRequestChan:
			// Miner requesting transport
			logger.Log("DEBUG", fmt.Sprintf("Miner %s requesting transport (available: %d)", minerSymbol, len(availableTransports)), nil)

			if len(availableTransports) > 0 {
			// Select transport with most cargo to fill it first
			bestIdx := 0
			bestCargo := transportCargoLevels[availableTransports[0]]
			for i, ts := range availableTransports {
				if transportCargoLevels[ts] > bestCargo {
					bestIdx = i
					bestCargo = transportCargoLevels[ts]
				}
			}
			transportSymbol := availableTransports[bestIdx]
			availableTransports = append(availableTransports[:bestIdx], availableTransports[bestIdx+1:]...)

				// Send transport to miner
				select {
				case minerAssignChans[minerSymbol] <- transportSymbol:
					logger.Log("INFO", fmt.Sprintf("Assigned transport %s to miner %s (cargo: %d)", transportSymbol, minerSymbol, bestCargo), nil)
				case <-ctx.Done():
					return result, ctx.Err()
				}
			} else {
				// No transport available, add to waiting queue
				waitingMiners = append(waitingMiners, minerSymbol)
				logger.Log("INFO", fmt.Sprintf("Miner %s waiting for transport (queue: %d)", minerSymbol, len(waitingMiners)), nil)
			}

		case transfer := <-transferCompleteChan:
			// Miner completed transferring cargo to transport
			result.TotalTransfers++
			logger.Log("INFO", fmt.Sprintf("Transfer %d complete: %s -> %s",
				result.TotalTransfers, transfer.MinerSymbol, transfer.TransportSymbol), nil)

			// Notify the transport that cargo was received
			select {
			case transportCargoReceivedChans[transfer.TransportSymbol] <- struct{}{}:
				logger.Log("DEBUG", fmt.Sprintf("Notified transport %s of cargo receipt", transfer.TransportSymbol), nil)
			case <-ctx.Done():
				return result, ctx.Err()
			}
		}
	}
}

// getOrCreateOperation loads or creates the mining operation
func (h *MiningCoordinatorHandler) getOrCreateOperation(
	ctx context.Context,
	cmd *MiningCoordinatorCommand,
) (*domainMining.MiningOperation, error) {
	// Try to load existing operation
	operation, err := h.operationRepo.FindByID(ctx, cmd.MiningOperationID, cmd.PlayerID)
	if err == nil && operation != nil {
		// Resume existing operation
		if operation.IsPending() {
			operation.Start()
			h.operationRepo.Update(ctx, operation)
		}
		return operation, nil
	}

	// Create new operation
	operation = domainMining.NewMiningOperation(
		cmd.MiningOperationID,
		cmd.PlayerID,
		cmd.AsteroidField,
		cmd.MinerShips,
		cmd.TransportShips,
		cmd.TopNOres,
		0, // BatchThreshold not used in Transport-as-Sink
		0, // BatchTimeout not used in Transport-as-Sink
		-1, // Infinite iterations
		nil,
	)

	if err := h.operationRepo.Insert(ctx, operation); err != nil {
		return nil, fmt.Errorf("failed to insert operation: %w", err)
	}

	operation.Start()
	h.operationRepo.Update(ctx, operation)

	return operation, nil
}

// createPoolAssignments creates ship assignments for all ships
func (h *MiningCoordinatorHandler) createPoolAssignments(
	ctx context.Context,
	ships []string,
	containerID string,
	playerID int,
) error {
	for _, ship := range ships {
		assignment := daemon.NewShipAssignment(ship, playerID, containerID, nil)
		if err := h.shipAssignmentRepo.Assign(ctx, assignment); err != nil {
			return fmt.Errorf("failed to assign %s: %w", ship, err)
		}
	}
	return nil
}

// releasePoolAssignments releases all ship assignments
func (h *MiningCoordinatorHandler) releasePoolAssignments(
	ctx context.Context,
	containerID string,
	playerID int,
) error {
	assignments, err := h.shipAssignmentRepo.FindByContainer(ctx, containerID, playerID)
	if err != nil {
		return err
	}

	for _, assignment := range assignments {
		h.shipAssignmentRepo.Release(ctx, assignment.ShipSymbol(), playerID, "coordinator shutdown")
	}

	return nil
}

// spawnMiningWorker creates and starts a mining worker for a ship
func (h *MiningCoordinatorHandler) spawnMiningWorker(
	ctx context.Context,
	cmd *MiningCoordinatorCommand,
	shipSymbol string,
	requestChan chan<- string,
	assignChan <-chan string,
	transferCompleteChan chan<- TransferComplete,
) (string, error) {
	logger := common.LoggerFromContext(ctx)

	workerContainerID := fmt.Sprintf("mining-worker-%s-%d", shipSymbol, time.Now().Unix())

	workerCmd := &MiningWorkerCommand{
		ShipSymbol:           shipSymbol,
		PlayerID:             cmd.PlayerID,
		AsteroidField:        cmd.AsteroidField,
		TopNOres:             cmd.TopNOres,
		CoordinatorID:        cmd.ContainerID,
		TransportRequestChan: requestChan,
		TransportAssignChan:  assignChan,
		TransferCompleteChan: transferCompleteChan,
	}

	// Step 1: Persist worker container
	logger.Log("INFO", fmt.Sprintf("Persisting mining worker %s", workerContainerID), nil)
	if err := h.daemonClient.PersistMiningWorkerContainer(ctx, workerContainerID, uint(cmd.PlayerID), workerCmd); err != nil {
		return "", fmt.Errorf("failed to persist worker: %w", err)
	}

	// Step 2: Transfer ship to worker
	if err := h.shipAssignmentRepo.Transfer(ctx, shipSymbol, cmd.ContainerID, workerContainerID); err != nil {
		_ = h.daemonClient.StopContainer(ctx, workerContainerID)
		return "", fmt.Errorf("failed to transfer ship: %w", err)
	}

	// Step 3: Start worker (no completion callback needed - workers run continuously)
	if err := h.daemonClient.StartMiningWorkerContainer(ctx, workerContainerID, nil); err != nil {
		_ = h.shipAssignmentRepo.Transfer(ctx, shipSymbol, workerContainerID, cmd.ContainerID)
		return "", fmt.Errorf("failed to start worker: %w", err)
	}

	logger.Log("INFO", fmt.Sprintf("Mining worker started for %s", shipSymbol), nil)
	return workerContainerID, nil
}

// spawnTransportWorker creates and starts a transport worker for a ship
func (h *MiningCoordinatorHandler) spawnTransportWorker(
	ctx context.Context,
	cmd *MiningCoordinatorCommand,
	shipSymbol string,
	marketSymbol string,
	availabilityChan chan<- string,
	cargoReceivedChan <-chan struct{},
) (string, error) {
	logger := common.LoggerFromContext(ctx)

	workerContainerID := fmt.Sprintf("transport-worker-%s-%d", shipSymbol, time.Now().Unix())

	workerCmd := &TransportWorkerCommand{
		ShipSymbol:        shipSymbol,
		PlayerID:          cmd.PlayerID,
		AsteroidField:     cmd.AsteroidField,
		MarketSymbol:      marketSymbol,
		CoordinatorID:     cmd.ContainerID,
		AvailabilityChan:  availabilityChan,
		CargoReceivedChan: cargoReceivedChan,
	}

	// Step 1: Persist worker container
	logger.Log("INFO", fmt.Sprintf("Persisting transport worker %s", workerContainerID), nil)
	if err := h.daemonClient.PersistTransportWorkerContainer(ctx, workerContainerID, uint(cmd.PlayerID), workerCmd); err != nil {
		return "", fmt.Errorf("failed to persist worker: %w", err)
	}

	// Step 2: Transfer ship to worker
	if err := h.shipAssignmentRepo.Transfer(ctx, shipSymbol, cmd.ContainerID, workerContainerID); err != nil {
		_ = h.daemonClient.StopContainer(ctx, workerContainerID)
		return "", fmt.Errorf("failed to transfer ship: %w", err)
	}

	// Step 3: Start worker (no completion callback needed - workers run continuously)
	if err := h.daemonClient.StartTransportWorkerContainer(ctx, workerContainerID, nil); err != nil {
		_ = h.shipAssignmentRepo.Transfer(ctx, shipSymbol, workerContainerID, cmd.ContainerID)
		return "", fmt.Errorf("failed to start worker: %w", err)
	}

	logger.Log("INFO", fmt.Sprintf("Transport worker started for %s", shipSymbol), nil)
	return workerContainerID, nil
}

// planDryRunRoutes plans routes for all ships without starting workers
func (h *MiningCoordinatorHandler) planDryRunRoutes(
	ctx context.Context,
	cmd *MiningCoordinatorCommand,
	logger common.ContainerLogger,
) (*MiningCoordinatorResponse, error) {
	logger.Log("INFO", "Dry-run mode: planning routes for all ships", nil)

	result := &MiningCoordinatorResponse{
		AsteroidField: cmd.AsteroidField,
		ShipRoutes:    []common.ShipRouteDTO{},
		Errors:        []string{},
	}

	// Extract system symbol from asteroid waypoint (e.g., "X1-AU21-J63" -> "X1-AU21")
	parts := strings.Split(cmd.AsteroidField, "-")
	if len(parts) < 2 {
		return nil, fmt.Errorf("invalid asteroid waypoint format: %s", cmd.AsteroidField)
	}
	systemSymbol := parts[0] + "-" + parts[1]

	// Get waypoint data for routing
	graphResult, err := h.graphProvider.GetGraph(ctx, systemSymbol, false, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get graph for system %s: %w", systemSymbol, err)
	}
	waypoints, err := extractWaypointData(graphResult.Graph)
	if err != nil {
		return nil, fmt.Errorf("failed to extract waypoints from graph: %w", err)
	}

	// Find the closest market with fuel to the asteroid
	marketSymbol, err := h.findClosestMarketWithFuel(ctx, cmd.PlayerID, cmd.AsteroidField, systemSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to find market: %w", err)
	}
	result.MarketSymbol = marketSymbol
	logger.Log("INFO", fmt.Sprintf("Found market for transport loop: %s", marketSymbol), nil)

	// Plan routes for each miner: current position -> asteroid
	for _, minerSymbol := range cmd.MinerShips {
		ship, err := h.shipRepo.FindBySymbol(ctx, minerSymbol, cmd.PlayerID)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("failed to fetch miner %s: %v", minerSymbol, err))
			continue
		}

		// Plan route to asteroid
		routeResp, err := h.routingClient.PlanRoute(ctx, &routing.RouteRequest{
			SystemSymbol:  systemSymbol,
			StartWaypoint: ship.CurrentLocation().Symbol,
			GoalWaypoint:  cmd.AsteroidField,
			CurrentFuel:   ship.Fuel().Current,
			FuelCapacity:  ship.Fuel().Capacity,
			EngineSpeed:   ship.EngineSpeed(),
			Waypoints:     waypoints,
			FuelEfficient: false,
			PreferCruise:  true,
		})
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("failed to plan route for miner %s: %v", minerSymbol, err))
			continue
		}

		// Convert to ShipRoute
		shipRoute := h.convertRouteToShipRoute(ship.CurrentLocation().Symbol, minerSymbol, "miner", routeResp)
		result.ShipRoutes = append(result.ShipRoutes, shipRoute)
	}

	// Plan routes for each transport using shared routing logic
	for _, transportSymbol := range cmd.TransportShips {
		ship, err := h.shipRepo.FindBySymbol(ctx, transportSymbol, cmd.PlayerID)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("failed to fetch transport %s: %v", transportSymbol, err))
			continue
		}

		// Use shared route planning function
		routePlan, err := PlanTransportRoute(
			ctx,
			h.routePlanner,
			ship,
			marketSymbol,
			cmd.AsteroidField,
			waypoints,
			systemSymbol,
		)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("failed to plan route for transport %s: %v", transportSymbol, err))
			continue
		}

		// Combine all routes into a single ShipRoute
		shipRoute := h.combineTransportRoutes(ship.CurrentLocation().Symbol, transportSymbol, routePlan.ToMarket, routePlan.ToAsteroid, routePlan.ToMarketRet)
		result.ShipRoutes = append(result.ShipRoutes, shipRoute)
	}

	// Log detailed route information
	logger.Log("INFO", fmt.Sprintf("Dry-run complete: planned routes for %d ships", len(result.ShipRoutes)), nil)
	logger.Log("INFO", fmt.Sprintf("Selected asteroid: %s", result.AsteroidField), nil)
	logger.Log("INFO", fmt.Sprintf("Selected market: %s", result.MarketSymbol), nil)

	for _, route := range result.ShipRoutes {
		totalMins := route.TotalTime / 60
		logger.Log("INFO", fmt.Sprintf("Route for %s (%s): %d segments, %d min total, %d fuel",
			route.ShipSymbol, route.ShipType, len(route.Segments), totalMins, route.TotalFuel), nil)
		for i, seg := range route.Segments {
			segMins := seg.TravelTime / 60
			logger.Log("INFO", fmt.Sprintf("  %d. %s → %s [%s, %d min, %d fuel]",
				i+1, seg.From, seg.To, seg.FlightMode, segMins, seg.FuelCost), nil)
		}
	}

	return result, nil
}

// selectAsteroidAndMarket selects the best asteroid field based on mining type and travel time
// Returns both the asteroid symbol and the nearest market symbol
func (h *MiningCoordinatorHandler) selectAsteroidAndMarket(
	ctx context.Context,
	miningType string,
	transportShips []string,
	force bool,
	playerID int,
) (asteroidSymbol, marketSymbol string, err error) {
	// Validate mining type
	traitMap := map[string]string{
		"common_metals":   "COMMON_METAL_DEPOSITS",
		"precious_metals": "PRECIOUS_METAL_DEPOSITS",
		"rare_metals":     "RARE_METAL_DEPOSITS",
		"minerals":        "MINERAL_DEPOSITS",
		"ice":             "ICE_CRYSTALS",
		"gas":             "EXPLOSIVE_GASES",
	}

	trait, ok := traitMap[miningType]
	if !ok {
		return "", "", fmt.Errorf("unknown mining type: %s (valid types: common_metals, precious_metals, rare_metals, minerals, ice, gas)", miningType)
	}

	// Need at least one transport ship to determine system and fuel constraints
	if len(transportShips) == 0 {
		return "", "", fmt.Errorf("at least one transport ship is required for auto-selection")
	}

	// Get first transport ship to determine system and fuel capacity
	transportShip, err := h.shipRepo.FindBySymbol(ctx, transportShips[0], playerID)
	if err != nil {
		return "", "", fmt.Errorf("failed to get transport ship %s: %w", transportShips[0], err)
	}

	systemSymbol := domainShared.ExtractSystemSymbol(transportShip.CurrentLocation().Symbol)
	fuelCapacity := transportShip.FuelCapacity()
	engineSpeed := transportShip.EngineSpeed()

	// Query asteroids with the mining trait in the system
	asteroids, err := h.waypointRepo.ListBySystemWithTrait(ctx, systemSymbol, trait)
	if err != nil {
		return "", "", fmt.Errorf("failed to query asteroids with trait %s: %w", trait, err)
	}

	if len(asteroids) == 0 {
		return "", "", fmt.Errorf("no asteroid fields found with trait %s in system %s", trait, systemSymbol)
	}

	// Query markets in the system (waypoints with MARKETPLACE trait)
	markets, err := h.waypointRepo.ListBySystemWithTrait(ctx, systemSymbol, "MARKETPLACE")
	if err != nil {
		return "", "", fmt.Errorf("failed to query markets: %w", err)
	}

	if len(markets) == 0 {
		return "", "", fmt.Errorf("no markets found in system %s", systemSymbol)
	}

	// Get all waypoints in the system for routing
	allWaypoints, err := h.waypointRepo.ListBySystem(ctx, systemSymbol)
	if err != nil {
		return "", "", fmt.Errorf("failed to query waypoints: %w", err)
	}

	// Convert to routing WaypointData
	waypointData := make([]*system.WaypointData, len(allWaypoints))
	for i, wp := range allWaypoints {
		waypointData[i] = &system.WaypointData{
			Symbol:  wp.Symbol,
			X:       wp.X,
			Y:       wp.Y,
			HasFuel: wp.HasFuel,
		}
	}

	// OPTIMIZATION: Pre-filter and sort by distance for early termination
	type asteroidMarketPair struct {
		asteroid *domainShared.Waypoint
		market   *domainShared.Waypoint
		distance float64
	}

	var pairs []asteroidMarketPair
	for _, asteroid := range asteroids {
		// Calculate distance to all markets
		marketDistances := make([]asteroidMarketPair, 0, len(markets))
		for _, market := range markets {
			dist := asteroid.DistanceTo(market)
			marketDistances = append(marketDistances, asteroidMarketPair{
				asteroid: asteroid,
				market:   market,
				distance: dist,
			})
		}

		// Sort markets by distance and keep only top 5 nearest
		sort.Slice(marketDistances, func(i, j int) bool {
			return marketDistances[i].distance < marketDistances[j].distance
		})

		// Keep top 5 nearest markets per asteroid
		limit := 5
		if len(marketDistances) < limit {
			limit = len(marketDistances)
		}
		pairs = append(pairs, marketDistances[:limit]...)
	}

	// Sort all pairs by distance (process nearest first for early termination)
	sort.Slice(pairs, func(i, j int) bool {
		return pairs[i].distance < pairs[j].distance
	})

	fmt.Printf("Asteroid selection: %d asteroids × top 5 markets = %d pairs to evaluate\n", len(asteroids), len(pairs))

	// Route pairs in order with early termination
	type routeResult struct {
		asteroid      *domainShared.Waypoint
		market        *domainShared.Waypoint
		roundTripTime int
		roundTripFuel int
		distance      float64
	}

	// Use cancellable context for early termination
	routeCtx, cancelRouting := context.WithCancel(ctx)
	defer cancelRouting()

	// Create channels for worker pool
	const numWorkers = 15
	type routeJob struct {
		asteroid *domainShared.Waypoint
		market   *domainShared.Waypoint
		distance float64
	}
	jobs := make(chan routeJob, len(pairs))
	results := make(chan *routeResult, len(pairs))

	// Start worker goroutines
	var wg sync.WaitGroup
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				// Check if cancelled
				select {
				case <-routeCtx.Done():
					return
				default:
				}

				// Fuel feasibility pre-check: CRUISE mode estimation
				cruiseRoundTripFuel := int(job.distance * 2 * 1.0)
				if cruiseRoundTripFuel > fuelCapacity {
					continue
				}

				// Plan route from market to asteroid
				toAsteroidReq := &routing.RouteRequest{
					SystemSymbol:  systemSymbol,
					StartWaypoint: job.market.Symbol,
					GoalWaypoint:  job.asteroid.Symbol,
					CurrentFuel:   fuelCapacity,
					FuelCapacity:  fuelCapacity,
					EngineSpeed:   engineSpeed,
					Waypoints:     waypointData,
					FuelEfficient: false,
					PreferCruise:  true,
				}

				toAsteroidResp, err := h.routingClient.PlanRoute(routeCtx, toAsteroidReq)
				if err != nil {
					continue
				}

				// Plan route from asteroid back to market
				toMarketReq := &routing.RouteRequest{
					SystemSymbol:  systemSymbol,
					StartWaypoint: job.asteroid.Symbol,
					GoalWaypoint:  job.market.Symbol,
					CurrentFuel:   fuelCapacity,
					FuelCapacity:  fuelCapacity,
					EngineSpeed:   engineSpeed,
					Waypoints:     waypointData,
					FuelEfficient: false,
					PreferCruise:  true,
				}

				toMarketResp, err := h.routingClient.PlanRoute(routeCtx, toMarketReq)
				if err != nil {
					continue
				}

				// Calculate totals
				roundTripTime := toAsteroidResp.TotalTimeSeconds + toMarketResp.TotalTimeSeconds
				roundTripFuel := toAsteroidResp.TotalFuelCost + toMarketResp.TotalFuelCost

				// Filter: round-trip fuel must fit in tank
				if roundTripFuel > fuelCapacity {
					continue
				}

				// Valid result!
				results <- &routeResult{
					asteroid:      job.asteroid,
					market:        job.market,
					roundTripTime: roundTripTime,
					roundTripFuel: roundTripFuel,
					distance:      job.distance,
				}
			}
		}()
	}

	// Send jobs in distance order
	go func() {
		for _, pair := range pairs {
			select {
			case <-routeCtx.Done():
				break
			case jobs <- routeJob{asteroid: pair.asteroid, market: pair.market, distance: pair.distance}:
			}
		}
		close(jobs)
	}()

	// Wait for workers in background and close results
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect all results and select the one with shortest distance
	var allResults []*routeResult
	for result := range results {
		allResults = append(allResults, result)
	}

	fmt.Printf("Evaluated %d valid asteroid-market pairs\n", len(allResults))

	// If no valid results found
	if len(allResults) == 0 {
		return "", "", fmt.Errorf("no valid asteroid candidates found (requires: round-trip fuel ≤%d)", fuelCapacity)
	}

	// Sort by distance (shortest first) to get the optimal result
	sort.Slice(allResults, func(i, j int) bool {
		return allResults[i].distance < allResults[j].distance
	})

	selectedResult := allResults[0]

	// Handle force flag for fuel capacity warnings
	if selectedResult.roundTripFuel > fuelCapacity {
		if force {
			fmt.Printf("WARNING: Selected asteroid %s requires %d fuel for round trip but transport capacity is %d. Proceeding with --force.\n",
				selectedResult.asteroid.Symbol, selectedResult.roundTripFuel, fuelCapacity)
		} else {
			return "", "", fmt.Errorf("asteroid %s requires %d fuel but capacity is %d. Use --force to override",
				selectedResult.asteroid.Symbol, selectedResult.roundTripFuel, fuelCapacity)
		}
	}

	travelMins := selectedResult.roundTripTime / 60
	fmt.Printf("Auto-selected asteroid %s (%s) - %d min round trip via market %s (fuel: %d/%d)\n",
		selectedResult.asteroid.Symbol, trait,
		travelMins, selectedResult.market.Symbol,
		selectedResult.roundTripFuel, fuelCapacity)

	return selectedResult.asteroid.Symbol, selectedResult.market.Symbol, nil
}

// findClosestMarketWithFuel finds the closest market with fuel near the asteroid
func (h *MiningCoordinatorHandler) findClosestMarketWithFuel(
	ctx context.Context,
	playerID int,
	asteroidSymbol string,
	systemSymbol string,
) (string, error) {
	// Get all markets in the system
	markets, err := h.marketRepo.ListMarketsInSystem(ctx, uint(playerID), systemSymbol, 60)
	if err != nil {
		return "", err
	}

	// Get waypoint data for distance calculation
	graphResult, err := h.graphProvider.GetGraph(ctx, systemSymbol, false, playerID)
	if err != nil {
		return "", err
	}

	waypointData, err := extractWaypointData(graphResult.Graph)
	if err != nil {
		return "", err
	}

	// Build waypoint map for quick lookup
	waypointMap := make(map[string]*system.WaypointData)
	for _, wp := range waypointData {
		waypointMap[wp.Symbol] = wp
	}

	asteroidWp, ok := waypointMap[asteroidSymbol]
	if !ok {
		return "", fmt.Errorf("asteroid waypoint not found: %s", asteroidSymbol)
	}

	// Find closest market with fuel
	var closestMarket string
	minDistance := float64(999999)

	for _, mkt := range markets {
		// Check if market has fuel
		if !mkt.HasGood("FUEL") {
			continue
		}

		// Calculate distance
		marketWp, ok := waypointMap[mkt.WaypointSymbol()]
		if !ok {
			continue
		}

		// Calculate Euclidean distance
		dx := asteroidWp.X - marketWp.X
		dy := asteroidWp.Y - marketWp.Y
		distance := dx*dx + dy*dy // Using squared distance for comparison
		if distance < minDistance {
			minDistance = distance
			closestMarket = mkt.WaypointSymbol()
		}
	}

	if closestMarket == "" {
		return "", fmt.Errorf("no market with fuel found in system %s", systemSymbol)
	}

	return closestMarket, nil
}

// convertRouteToShipRoute converts a routing response to ShipRoute format
func (h *MiningCoordinatorHandler) convertRouteToShipRoute(
	startWaypoint string,
	shipSymbol string,
	shipType string,
	routeResp *routing.RouteResponse,
) common.ShipRouteDTO {
	segments := []common.RouteSegmentDTO{}
	prevWaypoint := startWaypoint

	for _, step := range routeResp.Steps {
		if step.Action == routing.RouteActionTravel {
			segments = append(segments, common.RouteSegmentDTO{
				From:       prevWaypoint,
				To:         step.Waypoint,
				FlightMode: step.Mode,
				FuelCost:   step.FuelCost,
				TravelTime: step.TimeSeconds,
			})
			prevWaypoint = step.Waypoint
		}
	}

	return common.ShipRouteDTO{
		ShipSymbol: shipSymbol,
		ShipType:   shipType,
		Segments:   segments,
		TotalFuel:  routeResp.TotalFuelCost,
		TotalTime:  routeResp.TotalTimeSeconds,
	}
}

// combineTransportRoutes combines multiple routes into a single ShipRoute for transports
func (h *MiningCoordinatorHandler) combineTransportRoutes(
	startWaypoint string,
	shipSymbol string,
	route1, route2, route3 *navigation.Route,
) common.ShipRouteDTO {
	segments := []common.RouteSegmentDTO{}
	totalFuel := 0
	totalTime := 0

	// Helper to convert segments from a Route
	addRouteSegments := func(route *navigation.Route) {
		for _, seg := range route.Segments() {
			segments = append(segments, common.RouteSegmentToDTO(seg))
		}
		totalFuel += route.TotalFuelRequired()
		totalTime += route.TotalTravelTime()
	}

	// Add all three route legs
	addRouteSegments(route1) // current -> market
	addRouteSegments(route2) // market -> asteroid
	addRouteSegments(route3) // asteroid -> market

	return common.ShipRouteDTO{
		ShipSymbol: shipSymbol,
		ShipType:   "transport",
		Segments:   segments,
		TotalFuel:  totalFuel,
		TotalTime:  totalTime,
	}
}

// extractWaypointData converts graph format to routing waypoint data
func extractWaypointData(graph map[string]interface{}) ([]*system.WaypointData, error) {
	waypoints, ok := graph["waypoints"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid graph format: missing waypoints")
	}

	waypointData := make([]*system.WaypointData, 0, len(waypoints))
	for symbol, data := range waypoints {
		wpMap, ok := data.(map[string]interface{})
		if !ok {
			continue
		}

		wp := &system.WaypointData{
			Symbol: symbol,
		}

		if x, ok := wpMap["x"].(float64); ok {
			wp.X = x
		}
		if y, ok := wpMap["y"].(float64); ok {
			wp.Y = y
		}
		// Check for has_fuel as bool
		if hasFuel, ok := wpMap["has_fuel"].(bool); ok {
			wp.HasFuel = hasFuel
			// Debug: log key waypoints
			if symbol == "X1-AU21-H51" || symbol == "X1-AU21-I56" || symbol == "X1-AU21-J58" {
				fmt.Printf("[DEBUG] extractWaypointData: %s has_fuel=%v\n", symbol, hasFuel)
			}
		} else if hasFuelRaw, exists := wpMap["has_fuel"]; exists {
			// Debug: log if has_fuel exists but wrong type
			fmt.Printf("[DEBUG] Waypoint %s has_fuel is type %T, value %v\n", symbol, hasFuelRaw, hasFuelRaw)
		} else {
			// Debug: log if has_fuel doesn't exist
			if symbol == "X1-AU21-H51" || symbol == "X1-AU21-I56" || symbol == "X1-AU21-J58" {
				fmt.Printf("[DEBUG] extractWaypointData: %s has_fuel MISSING\n", symbol)
			}
		}

		waypointData = append(waypointData, wp)
	}

	return waypointData, nil
}

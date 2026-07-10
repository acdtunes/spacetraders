package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	appContract "github.com/andrescamacho/spacetraders-go/internal/application/contract"
	contractServices "github.com/andrescamacho/spacetraders-go/internal/application/contract/services"
	contractTypes "github.com/andrescamacho/spacetraders-go/internal/application/contract/types"
	shipAssignment "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/assignment"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// Type aliases for convenience
type RunFleetCoordinatorCommand = contractTypes.RunFleetCoordinatorCommand
type RunFleetCoordinatorResponse = contractTypes.RunFleetCoordinatorResponse

// RunFleetCoordinatorHandler implements the fleet coordinator logic
type RunFleetCoordinatorHandler struct {
	fleetPoolManager       *contractServices.FleetPoolManager
	workerLifecycleManager *contractServices.WorkerLifecycleManager
	contractMarketService  *contractServices.ContractMarketService
	marketRepo             market.MarketRepository
	shipRepo               navigation.ShipRepository
	daemonClient           daemon.DaemonClient
	graphProvider          system.ISystemGraphProvider
	converter              system.IWaypointConverter
	clock                  shared.Clock
	captainEvents          captain.EventRecorder

	// Event bus for inter-container communication
	eventSubscriber navigation.ShipEventSubscriber

	// idleArbLauncher (sp-1z2h) starts recovery-safe one-shot arb containers
	// for the idle-gap dispatcher. Wired at daemon startup like the event
	// subscriber; nil (e.g. in tests) leaves the harvest off entirely.
	idleArbLauncher appContract.IdleArbLauncher
}

// NewRunFleetCoordinatorHandler creates a new fleet coordinator handler
// The clock parameter is optional - if nil, defaults to RealClock for production use
func NewRunFleetCoordinatorHandler(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	contractRepo domainContract.ContractRepository,
	marketRepo market.MarketRepository,
	daemonClient daemon.DaemonClient,
	graphProvider system.ISystemGraphProvider,
	converter system.IWaypointConverter,
	containerRepo contractServices.ContainerRepository,
	clock shared.Clock,
	captainEvents captain.EventRecorder,
) *RunFleetCoordinatorHandler {
	// Default to RealClock if not provided
	if clock == nil {
		clock = shared.NewRealClock()
	}

	fleetPoolManager := contractServices.NewFleetPoolManager(mediator)
	workerLifecycleManager := contractServices.NewWorkerLifecycleManager(daemonClient, containerRepo, shipRepo)
	contractMarketService := contractServices.NewContractMarketService(mediator, contractRepo)

	return &RunFleetCoordinatorHandler{
		fleetPoolManager:       fleetPoolManager,
		workerLifecycleManager: workerLifecycleManager,
		contractMarketService:  contractMarketService,
		marketRepo:             marketRepo,
		shipRepo:               shipRepo,
		daemonClient:           daemonClient,
		graphProvider:          graphProvider,
		converter:              converter,
		clock:                  clock,
		captainEvents:          captainEvents,
	}
}

// SetEventSubscriber sets the event subscriber for inter-container communication.
// This enables event-driven notifications when workers complete.
func (h *RunFleetCoordinatorHandler) SetEventSubscriber(subscriber navigation.ShipEventSubscriber) {
	h.eventSubscriber = subscriber
}

// SetIdleArbLauncher wires the daemon-server launcher the idle-gap arb
// dispatcher (sp-1z2h) spawns its one-shot legs through. Optional: without it
// the coordinator runs exactly as before, no harvest.
func (h *RunFleetCoordinatorHandler) SetIdleArbLauncher(launcher appContract.IdleArbLauncher) {
	h.idleArbLauncher = launcher
}

// Handle executes the fleet coordinator command
func (h *RunFleetCoordinatorHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	logger := common.LoggerFromContext(ctx)

	cmd, ok := request.(*RunFleetCoordinatorCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	result := &RunFleetCoordinatorResponse{
		ContractsCompleted: 0,
		Errors:             []string{},
	}

	// Reconcile the operator's --dedicated-ships list into the DedicatedFleet
	// claim-filter (sp-snmb), routed through AssignShipFleetCommand — the
	// single write path for the tag (sp-l7h2). Best-effort and additive-only:
	// a ship symbol dropped from a later --dedicated-ships list on restart is
	// NOT un-dedicated here, only newly-configured symbols are marked. The
	// empty default must not touch anything, mediator lookup included.
	if len(cmd.DedicatedShips) > 0 {
		reconcileDedicatedFleet(ctx, logger, h.fleetPoolManager.GetMediator(), cmd.PlayerID, cmd.DedicatedShips, dedicatedFleetContract)
	}

	// No pool initialization - ships are discovered dynamically

	// Subscribe to WorkerCompletedEvent for this coordinator
	// Events are published by ContainerRunner when worker containers complete
	if h.eventSubscriber == nil {
		// Wiring gap must fail the container, not panic the daemon.
		return nil, fmt.Errorf("fleet coordinator: no event subscriber wired (call SetEventSubscriber at startup)")
	}
	workerCompletedCh := h.eventSubscriber.SubscribeWorkerCompleted(cmd.ContainerID)
	defer h.eventSubscriber.UnsubscribeWorkerCompleted(cmd.ContainerID, workerCompletedCh)

	// IDLE-GAP ARB (sp-1z2h): harvest the dedicated fleet's 89% idle time with
	// hub-local one-shot guarded arb legs. The dispatcher's reserve rule keeps
	// contract claims instant (see contract.IdleArbDispatcher); its life is
	// bounded by this coordinator's ctx, and it is inert when no dedicated
	// fleet exists or no launcher is wired.
	if h.idleArbLauncher != nil && !cmd.IdleArbDisabled {
		dispatcher := appContract.NewIdleArbDispatcher(
			h.shipRepo,
			h.marketRepo,
			h.graphProvider,
			h.idleArbLauncher,
			h.clock,
			cmd.PlayerID,
			dedicatedFleetContract,
			appContract.IdleArbConfig{
				ReserveHulls:     cmd.IdleArbReserveHulls,
				HubRadius:        cmd.IdleArbHubRadius,
				MaxSpendPerLeg:   cmd.IdleArbMaxSpend,
				MinMarginPerUnit: cmd.IdleArbMinMargin,
				Interval:         time.Duration(cmd.IdleArbIntervalSecs) * time.Second,
			},
		)
		go dispatcher.Run(ctx)
	}

	if err := h.workerLifecycleManager.StopExistingWorkers(ctx, cmd.PlayerID.Value()); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed during existing worker cleanup: %v", err), nil)
	}

	// Track current active worker container ID for cleanup on shutdown
	var activeWorkerContainerID string

	// Re-adopt any in-flight contract delivery a daemon restart orphaned
	// (sp-tgp5), BEFORE the main loop's reclaim/discovery can force-release the
	// cargo-laden ship and restart the workflow from scratch. Sets the active
	// worker so shutdown cleanup and the one-worker guard both see it.
	if readoptedWorkerID := h.readoptInterruptedDeliveries(ctx, cmd); readoptedWorkerID != "" {
		activeWorkerContainerID = readoptedWorkerID
	}

	// Track previous ship for balancing logic
	var previousShipSymbol string

	// errMon watches each retry checkpoint below for a long streak of the
	// identical error (sp-e2l1): the 2026-07-05 negotiate-nil incident ran
	// for 18h retrying the same failure every 60s and never emitted a
	// single event, so nothing outside the container's own logs could see
	// it was stuck. errMon makes that observable — edge-triggered, once per
	// streak crossing, not once per iteration.
	errMon := newCoordinatorErrorMonitor(coordinatorErrorStreakThreshold)

	// Step 4: Main coordinator loop (infinite)
	// Execute one contract at a time (game constraint: one active contract per player)
	for {
		select {
		case <-ctx.Done():
			// Context cancelled, exit
			h.stopActiveWorker(ctx, activeWorkerContainerID)
			return result, ctx.Err()
		default:
			// Continue with contract assignment
		}

		// Reclaim ships orphaned by a restart-killed worker (sp-tgp5) on every
		// pass, unconditionally - not only when the whole fleet is starved.
		// markWorkerInterrupted deliberately preserves ship assignment when a
		// worker container is marked FAILED during restart recovery, so an
		// orphaned ship stays IsAssigned()==true forever unless something
		// proactively releases it. Gating this behind "len(availableShips) ==
		// 0" (as st-anu's original fix did) meant the reclaim never fired
		// whenever the rest of the fleet had even one other idle hull -
		// exactly the common case, not the rare one. Running it here, first,
		// every iteration, means an orphan is freed on the coordinator's very
		// next pass regardless of what the rest of the fleet is doing.
		if reclaimed, reclaimErr := h.workerLifecycleManager.ReclaimShipsFromInterruptedWorkers(ctx, cmd.PlayerID.Value(), h.clock); reclaimErr != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to reclaim ships from interrupted workers: %v", reclaimErr), nil)
		} else if reclaimed > 0 {
			logger.Log("INFO", fmt.Sprintf("Reclaimed %d ship(s) from interrupted workers", reclaimed), nil)
			continue // Re-check ctx.Done() and re-discover candidates with the freed ship(s)
		}

		// Dynamically discover idle haul candidates. Contracts include the
		// command ship as a first-class candidate (sp-4a4e): it hauls contract
		// legs fine and is often the fastest, largest hull owned, so it competes
		// on distance like any hauler instead of sitting benched until zero
		// haulers remain.
		_, generalShips, err := appContract.FindIdleLightHaulers(ctx, cmd.PlayerID, h.shipRepo, appContract.IncludeCommandShip)
		if err != nil {
			errMsg := fmt.Sprintf("Failed to find idle haulers: %v", err)
			logger.Log("ERROR", errMsg, nil)
			result.Errors = append(result.Errors, errMsg)
			if streak, crossed := errMon.Note("find_idle_haulers", err.Error()); crossed {
				h.recordErrorLoopEvent(ctx, cmd, "find_idle_haulers", err, streak)
			}
			h.clock.Sleep(10 * time.Second)
			continue
		}
		errMon.Note("find_idle_haulers", "")

		// The coordinator's own dedicated fleet (sp-snmb) is invisible to
		// FindIdleLightHaulers via the claim-filter, so it is looked up
		// separately here. Looked up by fleet NAME from the persisted tag
		// (sp-l7h2), not the remembered --dedicated-ships list: a `fleet
		// assign`/`unassign` while this coordinator runs takes effect on the
		// very next pass, no restart needed.
		_, dedicatedIdleShips, err := appContract.FindIdleShipsByFleet(ctx, cmd.PlayerID, h.shipRepo, dedicatedFleetContract)
		if err != nil {
			errMsg := fmt.Sprintf("Failed to find idle dedicated ships: %v", err)
			logger.Log("ERROR", errMsg, nil)
			result.Errors = append(result.Errors, errMsg)
			h.clock.Sleep(10 * time.Second)
			continue
		}

		// EXCLUSIVE MODE (sp-wq7r): a dedicated fleet, once tagged (via
		// --dedicated-ships at startup or a live `fleet assign` with no
		// restart, sp-l7h2), is sealed - the coordinator draws ONLY from its
		// own idle members, even when that set is empty because every member
		// is busy. Before this fix the two pools were unconditionally
		// combined regardless of dedication state, so a coordinator with a
		// dedicated fleet still drafted idle non-dedicated pool hulls by
		// distance and could displace whatever cargo they were
		// mid-liquidating - "dedicated" was never actually exclusive.
		dedicatedFleetActive, err := appContract.FleetHasMembers(ctx, cmd.PlayerID, h.shipRepo, dedicatedFleetContract)
		if err != nil {
			errMsg := fmt.Sprintf("Failed to check dedicated fleet membership: %v", err)
			logger.Log("ERROR", errMsg, nil)
			result.Errors = append(result.Errors, errMsg)
			h.clock.Sleep(10 * time.Second)
			continue
		}
		availableShips := appContract.SelectAvailableShips(generalShips, dedicatedIdleShips, dedicatedFleetActive)

		// If no ships available, wait for completion signal. (The interrupted-
		// worker reclaim pass already ran unconditionally at the top of this
		// loop iteration - see above - so by this point any orphan has
		// already been freed if one existed.)
		if len(availableShips) == 0 {
			logger.Log("INFO", "No ships available, waiting for completion...", nil)
			select {
			case event := <-workerCompletedCh:
				recordWorkerCompletion(logger, event, fmt.Sprintf("Ship %s completed, back in pool", event.ShipSymbol))
				activeWorkerContainerID = "" // Worker completed
				// Loop immediately to assign next contract
			case <-time.After(30 * time.Second):
				// Timeout, check again
			case <-ctx.Done():
				h.stopActiveWorker(ctx, activeWorkerContainerID)
				return result, ctx.Err()
			}
			continue // Loop back to check for available ships
		}

		// CRITICAL CHECK: Prevent multiple workers by checking if any worker is already running
		// This prevents race conditions when negotiation fails early in the loop
		existingActiveWorkers, err := h.workerLifecycleManager.FindExistingWorkers(ctx, cmd.PlayerID.Value())
		if err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to check for active workers: %v", err), nil)
		} else if len(existingActiveWorkers) > 0 {
			logger.Log("WARNING", fmt.Sprintf("Found %d active CONTRACT_WORKFLOW workers - waiting instead of creating new worker", len(existingActiveWorkers)), nil)
			select {
			case event := <-workerCompletedCh:
				recordWorkerCompletion(logger, event, fmt.Sprintf("Active worker completed for ship %s", event.ShipSymbol))
				activeWorkerContainerID = "" // Worker completed
				// Loop back to create new worker
			case <-time.After(1 * time.Minute):
				logger.Log("WARNING", "Timeout waiting for active worker, will check again", nil)
			case <-ctx.Done():
				h.stopActiveWorker(ctx, activeWorkerContainerID)
				return result, ctx.Err()
			}
			continue
		}

		// Negotiate contract (use any ship from pool for negotiation)
		logger.Log("INFO", "Negotiating new contract...", nil)
		contract, err := h.contractMarketService.NegotiateContract(ctx, availableShips[0], cmd.PlayerID.Value())
		if err != nil {
			errMsg := fmt.Sprintf("Failed to negotiate contract: %v", err)
			logger.Log("ERROR", errMsg, nil)
			result.Errors = append(result.Errors, errMsg)
			if streak, crossed := errMon.Note("negotiate_contract", err.Error()); crossed {
				h.recordErrorLoopEvent(ctx, cmd, "negotiate_contract", err, streak)
			}
			h.clock.Sleep(30 * time.Second)
			continue
		}
		errMon.Note("negotiate_contract", "")

		// Check if contract is already complete (all deliveries fulfilled)
		allDeliveriesFulfilled := true
		for _, delivery := range contract.Terms().Deliveries {
			if delivery.UnitsRequired > delivery.UnitsFulfilled {
				allDeliveriesFulfilled = false
				break
			}
		}
		if allDeliveriesFulfilled {
			logger.Log("INFO", "Contract deliveries complete - fulfilling contract to get reward", map[string]interface{}{
				"contract_id": contract.ContractID(),
			})
			// Try to fulfill the contract via API to claim rewards
			err := h.contractMarketService.FulfillContract(ctx, contract, cmd.PlayerID)
			if err != nil {
				logger.Log("ERROR", fmt.Sprintf("Failed to fulfill contract: %v", err), nil)
			} else {
				logger.Log("INFO", "Contract fulfilled successfully - will negotiate new contract", nil)
				result.ContractsCompleted++
			}
			h.clock.Sleep(5 * time.Second) // Brief pause before negotiating new contract
			continue
		}

		// Find the cheapest WORKER-REACHABLE purchase market for the contract
		// (sp-1z2h sourcing cost-optimizer). nil reachability = in-system only:
		// contract sourcing is single-system by ruling (RULINGS #14), and the
		// worker's trip is in-system NavigateAndDock with zero jump capability, so
		// a cross-system source it can't reach must be excluded, not
		// selected-then-crashed ('waypoint not found in cache' — sp-9hu8).
		logger.Log("INFO", "Planning sourcing (cheapest in-system market)...", nil)
		plan, err := appContract.PlanSourcing(ctx, contract, h.marketRepo, cmd.PlayerID.Value(), nil)
		if err != nil {
			// Market data not yet available - this is expected while scouts are scanning
			logger.Log("INFO", "Purchase market not yet available - waiting for scouts to scan market data", map[string]interface{}{
				"contract_id": contract.ContractID(),
				"error":       err.Error(),
			})
			// Sleep and retry - scouts will eventually scan the required market
			h.clock.Sleep(30 * time.Second)
			continue
		}
		purchaseMarket := plan.Market

		// SOURCING DEFER GATE (sp-1z2h): never EXECUTE a sourcing run whose
		// projected net is worse than −20% of payout while deadline runway
		// remains — park and re-project next pass instead. The contract is
		// ACCEPTED before parking (accept-now-sequence-smart): acceptance is
		// what makes NegotiateContract resume it on the next pass, and an
		// unaccepted contract parked past its accept-by deadline would be a
		// skip — the exact RULINGS #1 violation this gate must never commit.
		decision := appContract.EvaluateSourcingDefer(plan, contract, h.clock.Now())
		if decision.Defer {
			accepted, acceptErr := h.contractMarketService.EnsureAccepted(ctx, contract, cmd.PlayerID)
			if acceptErr != nil {
				// Without acceptance a defer is not safe (see above) — source
				// this pass instead of parking, and say why.
				logger.Log("WARNING", fmt.Sprintf(
					"Sourcing defer wanted but contract %s could not be accepted first (%v) - sourcing this pass instead of parking (never-skip)",
					contract.ContractID(), acceptErr), nil)
			} else {
				contract = accepted
				logger.Log("WARNING", decision.DeferMessage(plan), map[string]interface{}{
					"action":        "sourcing_deferred",
					"contract_id":   contract.ContractID(),
					"projected_net": decision.ProjectedNet,
					"payout":        decision.Payout,
					"threshold":     decision.Threshold,
					"unit_ask":      plan.UnitAsk,
					"market":        plan.Market,
					"trade_symbol":  plan.Good,
				})
				h.clock.Sleep(appContract.SourcingDeferRecheckInterval)
				continue
			}
		} else if decision.Overridden {
			logger.Log("WARNING", decision.OverrideMessage(plan), map[string]interface{}{
				"action":        "sourcing_defer_overridden",
				"contract_id":   contract.ContractID(),
				"projected_net": decision.ProjectedNet,
				"payout":        decision.Payout,
				"threshold":     decision.Threshold,
				"unit_ask":      plan.UnitAsk,
				"market":        plan.Market,
				"trade_symbol":  plan.Good,
			})
		}

		// Extract required cargo for delivery (for ship selection prioritization)
		var requiredCargo string
		var unitsNeeded int
		for _, delivery := range contract.Terms().Deliveries {
			if delivery.UnitsRequired > delivery.UnitsFulfilled {
				requiredCargo = delivery.TradeSymbol
				unitsNeeded = delivery.UnitsRequired - delivery.UnitsFulfilled
				break
			}
		}

		// Check for in-flight cargo from active workers (prevent duplicate purchases on restart)
		inFlightCargo, err := h.calculateInFlightCargo(ctx, requiredCargo, cmd.PlayerID.Value())
		if err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to calculate in-flight cargo: %v", err), nil)
			// Continue anyway - better to risk duplication than block indefinitely
		}

		// If there's already enough in-flight cargo, wait for delivery instead of assigning new work
		if inFlightCargo >= unitsNeeded {
			logger.Log("INFO", fmt.Sprintf("Contract already has %d units of %s in-flight (needed: %d) - waiting for delivery instead of assigning new ship",
				inFlightCargo, requiredCargo, unitsNeeded), nil)
			// Wait for worker completion
			select {
			case event := <-workerCompletedCh:
				recordWorkerCompletion(logger, event, fmt.Sprintf("Active worker completed for ship %s", event.ShipSymbol))
				activeWorkerContainerID = "" // Worker completed
				// Loop back to check contract status
			case <-time.After(1 * time.Minute):
				logger.Log("INFO", "Timeout waiting for delivery, will re-check", nil)
			case <-ctx.Done():
				h.stopActiveWorker(ctx, activeWorkerContainerID)
				return result, ctx.Err()
			}
			continue
		}

		// Log remaining units needed after accounting for in-flight cargo
		if inFlightCargo > 0 {
			logger.Log("INFO", fmt.Sprintf("Contract needs %d more units of %s (%d in-flight, %d required, %d fulfilled)",
				unitsNeeded-inFlightCargo, requiredCargo, inFlightCargo, unitsNeeded+contract.Terms().Deliveries[0].UnitsFulfilled, contract.Terms().Deliveries[0].UnitsFulfilled), nil)
		}

		// NO-CARGO-DUMP CLAIM GUARD (sp-wq7r): a candidate hull already
		// holding cargo unrelated to this contract's delivery good must be
		// parked, never claimed - claiming it would let downstream cargo
		// handling jettison whatever it was carrying (e.g. mid-liquidation
		// EQUIPMENT dumped to make room for LIQUID_NITROGEN) to satisfy a
		// contract that has nothing to do with that cargo. Filter BEFORE
		// SelectClosestShip so an unrelated-cargo hull is never even
		// distance-ranked as the winner.
		claimableShips, parkedShips, err := appContract.FilterUnrelatedCargo(ctx, cmd.PlayerID, h.shipRepo, availableShips, requiredCargo)
		if err != nil {
			errMsg := fmt.Sprintf("Failed to filter candidates by cargo: %v", err)
			logger.Log("ERROR", errMsg, nil)
			result.Errors = append(result.Errors, errMsg)
			h.clock.Sleep(10 * time.Second)
			continue
		}
		if len(claimableShips) == 0 {
			logger.Log("INFO", fmt.Sprintf("No claimable ships - %d candidate(s) hold unrelated cargo, waiting for completion...", len(parkedShips)), nil)
			select {
			case event := <-workerCompletedCh:
				recordWorkerCompletion(logger, event, fmt.Sprintf("Ship %s completed, back in pool", event.ShipSymbol))
				activeWorkerContainerID = "" // Worker completed
			case <-time.After(30 * time.Second):
				// Timeout, check again
			case <-ctx.Done():
				h.stopActiveWorker(ctx, activeWorkerContainerID)
				return result, ctx.Err()
			}
			continue
		}

		// Select closest ship to purchase market (prioritizes ships with required cargo)
		logger.Log("INFO", fmt.Sprintf("Selecting closest ship (required cargo: %s)...", requiredCargo), nil)
		selectedShip, distance, err := appContract.SelectClosestShip(
			ctx,
			claimableShips,
			h.shipRepo,
			h.graphProvider,
			h.converter,
			purchaseMarket,
			requiredCargo,
			unitsNeeded,
			cmd.PlayerID.Value(),
		)
		if err != nil {
			errMsg := fmt.Sprintf("Failed to select ship: %v", err)
			logger.Log("ERROR", errMsg, nil)
			result.Errors = append(result.Errors, errMsg)
			if streak, crossed := errMon.Note("select_closest_ship", err.Error()); crossed {
				h.recordErrorLoopEvent(ctx, cmd, "select_closest_ship", err, streak)
			}
			h.clock.Sleep(10 * time.Second)
			continue
		}
		errMon.Note("select_closest_ship", "")

		logger.Log("INFO", fmt.Sprintf("Selected %s (distance: %.2f units)", selectedShip, distance), nil)

		// If selected ship is different from previous ship, reposition the
		// previous ship: a dedicated ship (sp-snmb) homes to a balanced
		// operator-configured standby station (fewest fleet peers, distance
		// tie-break - l7h2 Phase 3) instead of the normal market-balancing
		// treatment, since it's exclusively reserved for this coordinator and
		// has no reason to loiter at a general market.
		if previousShipSymbol != "" && previousShipSymbol != selectedShip {
			if isDedicatedShip(previousShipSymbol, cmd.DedicatedShips) {
				logger.Log("INFO", fmt.Sprintf("Selected ship changed from %s to %s - homing dedicated ship %s to standby station", previousShipSymbol, selectedShip, previousShipSymbol), nil)

				// Launch homing command asynchronously (fire-and-forget)
				go func(shipSymbol string, playerID shared.PlayerID, standbyStations []string, fleetShips []string) {
					homeCmd := &HomeShipCommand{
						ShipSymbol:      shipSymbol,
						PlayerID:        playerID,
						StandbyStations: standbyStations,
						FleetShips:      fleetShips,
					}
					// Create background context since parent context may be cancelled
					homeCtx := context.Background()
					homeCtx = common.WithLogger(homeCtx, common.LoggerFromContext(ctx))

					_, err := h.fleetPoolManager.GetMediator().Send(homeCtx, homeCmd)
					if err != nil {
						logger.Log("WARNING", fmt.Sprintf("Failed to home dedicated ship %s: %v", shipSymbol, err), nil)
					}
				}(previousShipSymbol, cmd.PlayerID, cmd.StandbyStations, cmd.DedicatedShips)
			} else {
				logger.Log("INFO", fmt.Sprintf("Selected ship changed from %s to %s - balancing previous ship position", previousShipSymbol, selectedShip), nil)

				// Launch balancing command asynchronously (fire-and-forget)
				go func(shipSymbol string, playerID shared.PlayerID, coordinatorID string) {
					balanceCmd := &BalanceShipPositionCommand{
						ShipSymbol:    shipSymbol,
						PlayerID:      playerID,
						CoordinatorID: coordinatorID,
					}
					// Create background context since parent context may be cancelled
					balanceCtx := context.Background()
					balanceCtx = common.WithLogger(balanceCtx, common.LoggerFromContext(ctx))

					_, err := h.fleetPoolManager.GetMediator().Send(balanceCtx, balanceCmd)
					if err != nil {
						logger.Log("WARNING", fmt.Sprintf("Failed to balance ship %s position: %v", shipSymbol, err), nil)
					}
				}(previousShipSymbol, cmd.PlayerID, cmd.ContainerID)
			}
		}

		workerContainerID, err := h.spawnContractWorker(ctx, cmd, selectedShip)
		if err != nil {
			logger.Log("ERROR", err.Error(), nil)
			result.Errors = append(result.Errors, err.Error())
			if streak, crossed := errMon.Note("spawn_contract_worker", err.Error()); crossed {
				h.recordErrorLoopEvent(ctx, cmd, "spawn_contract_worker", err, streak)
			}
			h.clock.Sleep(10 * time.Second)
			continue
		}
		errMon.Note("spawn_contract_worker", "")

		activeWorkerContainerID = workerContainerID

		// Block waiting for worker completion
		logger.Log("INFO", fmt.Sprintf("Waiting for %s to complete contract...", selectedShip), nil)
		select {
		case event := <-workerCompletedCh:
			if recordWorkerCompletion(logger, event, fmt.Sprintf("Contract completed by %s", event.ShipSymbol)) {
				result.ContractsCompleted++
			}
			activeWorkerContainerID = ""

			// Ship will no longer be transferred back to coordinator - it's automatically available
			// since we're using dynamic discovery instead of pool assignments

			// Store completed ship as previous ship for potential balancing in next iteration
			previousShipSymbol = event.ShipSymbol

			continue

		case <-time.After(30 * time.Minute):
			// Timeout waiting for worker
			logger.Log("ERROR", fmt.Sprintf("Timeout waiting for worker %s", selectedShip), nil)
			errMsg := fmt.Sprintf("Worker timeout for ship %s", selectedShip)
			result.Errors = append(result.Errors, errMsg)
			// Loop back to try again
			continue

		case <-ctx.Done():
			logger.Log("INFO", "Context cancelled, exiting coordinator", nil)
			h.stopActiveWorker(ctx, activeWorkerContainerID)
			return result, ctx.Err()
		}
	}
}

// dedicatedFleetContract is the Ship.DedicatedFleet() value this coordinator
// reconciles its --dedicated-ships list into (sp-snmb).
const dedicatedFleetContract = "contract"

// reconcileDedicatedFleet marks every operator-configured --dedicated-ships
// entry into fleetName so the DedicatedFleet claim-filter in
// FindIdleLightHaulers actually takes effect. Routed through
// AssignShipFleetCommand — the single write path for the dedication tag
// (sp-l7h2) — rather than mutating ships directly, so reconciliation and
// `fleet assign` can never drift apart. Additive-only: a symbol removed
// from a later --dedicated-ships list on restart is NOT un-dedicated by this
// pass - only configured symbols are marked (deferred symmetric-removal gap,
// sp-snmb). Still idempotent: the repository write behind the command skips
// the DB write when the tag is already fleetName, so a restart with an
// unchanged list performs zero DB writes. Per-ship failures are logged at
// WARNING and skipped rather than aborting the whole pass, since one bad
// symbol (e.g. a ship sold since the operator last updated the flag) must
// not block reconciling the rest.
func reconcileDedicatedFleet(
	ctx context.Context,
	logger common.ContainerLogger,
	med common.Mediator,
	playerID shared.PlayerID,
	dedicatedShips []string,
	fleetName string,
) {
	for _, symbol := range dedicatedShips {
		pid := playerID.Value()
		_, err := med.Send(ctx, &shipAssignment.AssignShipFleetCommand{
			ShipSymbol: symbol,
			Fleet:      fleetName,
			PlayerID:   &pid,
		})
		if err != nil {
			logger.Log("WARNING", fmt.Sprintf("dedicated fleet reconciliation: failed to assign ship %s: %v", symbol, err), nil)
			continue
		}
		logger.Log("INFO", fmt.Sprintf("Ship %s reconciled into dedicated %s fleet", symbol, fleetName), nil)
	}
}

// isDedicatedShip reports whether shipSymbol is one of the operator's
// configured --dedicated-ships (sp-snmb). Used at the "previous ship" hook to
// decide whether an idle ship should home to a standby station instead of
// being balanced to a market.
func isDedicatedShip(shipSymbol string, dedicatedShips []string) bool {
	for _, symbol := range dedicatedShips {
		if symbol == shipSymbol {
			return true
		}
	}
	return false
}

// recordWorkerCompletion logs the outcome of a worker-completion event honestly
// and reports whether it should count toward the completed-contracts metric.
// A successful worker is logged at INFO with successMsg and counts; a crashed
// worker is logged at ERROR carrying event.Error and does NOT count — so the
// logs and the ContractsCompleted metric never treat a failure as a completion
// (sp-2q2w). Every worker-completion receive site funnels through here.
func recordWorkerCompletion(logger common.ContainerLogger, event navigation.WorkerCompletedEvent, successMsg string) (succeeded bool) {
	if event.Success {
		logger.Log("INFO", successMsg, nil)
		return true
	}
	logger.Log("ERROR", fmt.Sprintf("Worker for ship %s failed: %s", event.ShipSymbol, event.Error), nil)
	return false
}

// readoptInterruptedDeliveries resumes a contract delivery that a daemon restart
// orphaned mid-flight (sp-tgp5). A restart marks the in-flight worker container
// FAILED (markWorkerInterrupted) but deliberately leaves its ship holding the
// contract cargo. The existing "ship has cargo -> resume delivery" path
// (StopExistingWorkers) only inspects RUNNING workers, so it never fires for a
// restart-interrupted (FAILED) worker; without this pass the coordinator would
// instead ForceRelease that ship (ReclaimShipsFromInterruptedWorkers) and restart
// the whole workflow from the top — negotiate -> find-purchase-market -> select —
// stalling the fully-loaded ship behind a purchase-market gate it does not need
// while scouts repopulate market data after the restart (the 15-30min throughput
// hole in the captain ledger).
//
// Re-adopting spawns a fresh worker directly for the cargo-laden ship; the
// worker's already-idempotent workflow (FindOrNegotiate finds the accepted
// contract, ProcessAllDeliveries delivers the aboard cargo) resumes at the
// delivery leg with no re-negotiation and no re-purchase. At most one ship is
// re-adopted per startup: contracts run one worker at a time (game constraint:
// one active contract per player), so only one ship is ever mid-delivery. Empty
// interrupted ships are left untouched here for ReclaimShipsFromInterruptedWorkers
// to free into normal discovery. Any failure falls back to that reclaim path, so
// a transient error here can never strand the ship — it just forgoes the fast
// resume. Returns the re-adopted worker's container ID, or "" if nothing was
// re-adopted.
func (h *RunFleetCoordinatorHandler) readoptInterruptedDeliveries(
	ctx context.Context,
	cmd *RunFleetCoordinatorCommand,
) string {
	logger := common.LoggerFromContext(ctx)

	ships, err := h.workerLifecycleManager.FindInterruptedWorkerShipsWithCargo(ctx, cmd.PlayerID.Value())
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed to find interrupted deliveries to re-adopt: %v", err), nil)
		return ""
	}
	if len(ships) == 0 {
		return ""
	}

	ship := ships[0]
	shipSymbol := ship.ShipSymbol()

	// Detach from the dead worker container so spawnContractWorker can re-assign
	// the ship to the fresh one. Mirrors ReclaimShipsFromInterruptedWorkers' own
	// detach, but here we immediately re-adopt instead of returning to discovery.
	ship.ForceRelease("worker_readopt", h.clock)
	if err := h.shipRepo.Save(ctx, ship); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed to release ship %s for re-adoption (falling back to reclaim/discovery): %v", shipSymbol, err), nil)
		return ""
	}

	workerContainerID, err := h.spawnContractWorker(ctx, cmd, shipSymbol)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed to re-adopt in-flight delivery for ship %s (falling back to discovery): %v", shipSymbol, err), nil)
		return ""
	}

	logger.Log("INFO", fmt.Sprintf("Re-adopted in-flight contract delivery: ship %s resuming in worker %s (cargo aboard, no re-negotiation)", shipSymbol, workerContainerID), map[string]interface{}{
		"ship_symbol":  shipSymbol,
		"container_id": workerContainerID,
		"action":       "readopt_delivery",
	})
	return workerContainerID
}

func (h *RunFleetCoordinatorHandler) spawnContractWorker(
	ctx context.Context,
	cmd *RunFleetCoordinatorCommand,
	selectedShip string,
) (string, error) {
	logger := common.LoggerFromContext(ctx)

	workerContainerID := utils.GenerateContainerID("contract-work", selectedShip)

	workerCmd := &RunWorkflowCommand{
		ShipSymbol:    selectedShip,
		PlayerID:      cmd.PlayerID,
		ContainerID:   workerContainerID,
		CoordinatorID: cmd.ContainerID,
	}

	logger.Log("INFO", fmt.Sprintf("Persisting worker container %s for %s", workerContainerID, selectedShip), nil)
	if err := h.daemonClient.PersistContainer(ctx, daemon.ContainerKindContractWorkflow, workerContainerID, uint(cmd.PlayerID.Value()), workerCmd); err != nil {
		return "", fmt.Errorf("Failed to persist worker container: %v", err)
	}

	logger.Log("INFO", fmt.Sprintf("Assigning %s to worker container", selectedShip), nil)
	ship, err := h.shipRepo.FindBySymbol(ctx, selectedShip, cmd.PlayerID)
	if err != nil {
		_ = h.workerLifecycleManager.StopWorkerContainer(ctx, workerContainerID)
		return "", fmt.Errorf("Failed to load ship %s: %v", selectedShip, err)
	}

	// Atomic claim (sp-lprs, l7h2 Phase 2.5): assignment AND fleet dedication are
	// re-checked inside ClaimShip's row-locked transaction, replacing the old
	// FindBySymbol+AssignToContainer+Save read-modify-write whose TOCTOU let a
	// `fleet assign` racing discovery slip a foreign-pinned hull — including the
	// command frigate under its "command" pin — into a contract worker. A hull
	// pinned to a fleet other than dedicatedFleetContract ("contract") is
	// rejected at the DB, not clobbered; a contract-pinned or unpinned hull
	// claims normally. Both callers hand this an idle ship: the main loop selects
	// from idle candidates, and readoptInterruptedDeliveries force-releases the
	// dead worker's hull to idle before re-adopting it.
	if err := h.shipRepo.ClaimShip(ctx, selectedShip, workerContainerID, cmd.PlayerID, dedicatedFleetContract); err != nil {
		_ = h.workerLifecycleManager.StopWorkerContainer(ctx, workerContainerID)
		// %w so callers (and the poach-vector test) can distinguish a fleet-
		// dedication rejection from a transient failure; the string is identical.
		return "", fmt.Errorf("Failed to claim ship %s: %w", selectedShip, err)
	}

	// Mirror the committed claim into the in-memory entity so the start-failure
	// rollback below (and any later read of `ship`) sees the assignment. A sync
	// failure here is a WARN, not an unclaim: the DB claim already holds the
	// ship, so returning an error would orphan a committed claim with no holder
	// to release it (matches the factory/gas Phase 2 migration).
	if err := ship.AssignToContainer(workerContainerID, h.clock); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Ship %s claimed in DB but in-memory assign failed (claim stands): %v", selectedShip, err), nil)
	}

	logger.Log("INFO", fmt.Sprintf("Starting worker container for %s", selectedShip), nil)
	if err := h.daemonClient.StartContainer(ctx, daemon.ContainerKindContractWorkflow, workerContainerID); err != nil {
		ship.ForceRelease("worker_start_failed", h.clock)
		_ = h.shipRepo.Save(ctx, ship)
		_ = h.workerLifecycleManager.StopWorkerContainer(ctx, workerContainerID)
		return "", fmt.Errorf("Failed to start worker container: %v", err)
	}

	return workerContainerID, nil
}

// calculateInFlightCargo calculates the total cargo of a specific trade symbol
// that is currently held by ships working on active contract workflows, plus
// cargo still aboard ships whose contract worker was interrupted (marked
// FAILED) but hasn't been reclaimed to idle yet (sp-u20w). Without the second
// source, a partially-laden hull orphaned by a dead worker read as 0 in-flight
// the moment its worker died, letting the coordinator purchase units
// redundant with what that hull is still physically holding.
//
// Ordering rules out double-counting: readoptInterruptedDeliveries (sp-tgp5)
// runs once, before the main loop starts, and moves any successfully
// re-adopted ship onto a fresh RUNNING container before this function is ever
// called from inside the loop. That ship is therefore picked up exactly once,
// by the RUNNING-workers pass below, and no longer matches
// FindInterruptedWorkerShipsWithCargo's query (it queries by container ID,
// and the ship has moved off the dead one — the dead container's own row can
// still be sitting in the FAILED list, but nothing on it matches anymore). A
// ship that is NOT re-adopted (readoption only re-adopts one hull per
// startup) stays attached to its FAILED container - a transient state the
// loop's unconditional ReclaimShipsFromInterruptedWorkers pass forces closed
// on its very next iteration - so counting it here can delay, but never
// permanently stall, the coordinator, unlike counting arbitrary idle-ship
// cargo would.
//
// This is used during restart recovery to prevent duplicate cargo purchases.
func (h *RunFleetCoordinatorHandler) calculateInFlightCargo(
	ctx context.Context,
	tradeSymbol string,
	playerID int,
) (int, error) {
	logger := common.LoggerFromContext(ctx)

	// Find all active CONTRACT_WORKFLOW containers
	activeWorkers, err := h.workerLifecycleManager.FindExistingWorkers(ctx, playerID)
	if err != nil {
		return 0, fmt.Errorf("failed to find existing workers: %w", err)
	}

	totalInFlight := 0

	// For each active worker, find its assigned ships and check their cargo
	for _, worker := range activeWorkers {
		ships, err := h.shipRepo.FindByContainer(ctx, worker.ID, shared.MustNewPlayerID(playerID))
		if err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to get ships for container %s: %v", worker.ID, err), nil)
			continue
		}

		for _, ship := range ships {
			// Count cargo of the required trade symbol
			for _, item := range ship.Cargo().Inventory {
				if item.Symbol == tradeSymbol {
					totalInFlight += item.Units
					logger.Log("INFO", fmt.Sprintf("Found %d units of %s in ship %s cargo (worker %s)",
						item.Units, tradeSymbol, ship.ShipSymbol(), worker.ID), nil)
				}
			}
		}
	}

	// Also count cargo still aboard ships whose contract worker was
	// interrupted (marked FAILED) but hasn't been reclaimed to idle yet
	// (sp-u20w). Reuses FindInterruptedWorkerShipsWithCargo (sp-tgp5) rather
	// than a new query, so this always agrees with readoptInterruptedDeliveries
	// about which ships are "interrupted with cargo to salvage." A failure
	// here is logged and swallowed, matching this function's existing
	// "better to risk duplication than block indefinitely" contract with its
	// caller — the RUNNING-workers total above is still valid on its own.
	interruptedShips, err := h.workerLifecycleManager.FindInterruptedWorkerShipsWithCargo(ctx, playerID)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed to find interrupted-worker ships for in-flight cargo count: %v", err), nil)
	} else {
		for _, ship := range interruptedShips {
			for _, item := range ship.Cargo().Inventory {
				if item.Symbol == tradeSymbol {
					totalInFlight += item.Units
					logger.Log("INFO", fmt.Sprintf("Found %d units of %s in interrupted ship %s cargo (worker dead, not yet reclaimed)",
						item.Units, tradeSymbol, ship.ShipSymbol()), nil)
				}
			}
		}
	}

	if totalInFlight > 0 {
		logger.Log("INFO", fmt.Sprintf("Total in-flight cargo: %d units of %s", totalInFlight, tradeSymbol), nil)
	}

	return totalInFlight, nil
}

// recordErrorLoopEvent emits the captain outbox event for a checkpoint's
// error streak crossing (sp-e2l1). Fire-and-forget with its own short
// timeout, mirroring internal/adapters/grpc/captain_recorder.go's idiom: an
// outbox failure must never break the coordinator's retry loop, so errors
// are logged at WARNING and swallowed. A nil captainEvents (not wired —
// tests, or a daemon boot before main finishes DI) silently disables
// recording rather than panicking.
func (h *RunFleetCoordinatorHandler) recordErrorLoopEvent(ctx context.Context, cmd *RunFleetCoordinatorCommand, checkpoint string, cause error, streak int) {
	if h.captainEvents == nil {
		return
	}
	logger := common.LoggerFromContext(ctx)
	event := buildErrorLoopEvent(cmd.ContainerID, cmd.PlayerID.Value(), checkpoint, cause, streak)
	recordCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.captainEvents.Record(recordCtx, event); err != nil {
		logger.Log("WARNING", fmt.Sprintf("captain outbox: failed to record %s for checkpoint %s: %v", captain.EventCoordinatorErrorLoop, checkpoint, err), nil)
	}
}

func (h *RunFleetCoordinatorHandler) stopActiveWorker(ctx context.Context, activeWorkerContainerID string) {
	if activeWorkerContainerID == "" {
		return
	}
	logger := common.LoggerFromContext(ctx)
	logger.Log("INFO", fmt.Sprintf("Stopping active worker container: %s", activeWorkerContainerID), nil)
	_ = h.workerLifecycleManager.StopWorkerContainer(ctx, activeWorkerContainerID)
}

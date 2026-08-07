package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	appContract "github.com/andrescamacho/spacetraders-go/internal/application/contract"
	contractServices "github.com/andrescamacho/spacetraders-go/internal/application/contract/services"
	contractTypes "github.com/andrescamacho/spacetraders-go/internal/application/contract/types"
	"github.com/andrescamacho/spacetraders-go/internal/application/health"
	"github.com/andrescamacho/spacetraders-go/internal/domain/absorption"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// Type aliases for convenience
type RunFleetCoordinatorCommand = contractTypes.RunFleetCoordinatorCommand
type RunFleetCoordinatorResponse = contractTypes.RunFleetCoordinatorResponse

// Pacing for the reconcile loop's retry backoffs, taken on the INJECTED clock
// (h.clock.Sleep), which a MockClock makes instant in tests.
//
// These are deliberately kept separate from the select-timeout constants below:
// the two families run on DIFFERENT clocks and must never be merged behind one
// helper — see the note on that block.
const (
	// stepRetryBackoff paces the retry after a transient discovery, filtering or
	// dispatch step fails, so a failing pass cannot spin the loop hot.
	stepRetryBackoff = 10 * time.Second

	// negotiateRetryBackoff paces the retry after negotiation fails — longer than
	// stepRetryBackoff because the cause is usually server-side or funding.
	negotiateRetryBackoff = 30 * time.Second

	// awaitMarketDataBackoff paces re-checks while scouts scan. Not an error path: the
	// contract is parked and retried every pass (RULINGS #1), never skipped.
	awaitMarketDataBackoff = 30 * time.Second

	// postFulfillmentPause is a brief settle after a contract is fulfilled,
	// before negotiating the next one.
	postFulfillmentPause = 5 * time.Second
)

// Bounds on the loop's `select` waits for a worker-completion event, taken with
// time.After.
//
// HAZARD: time.After is the REAL wall clock even under a MockClock, unlike the
// backoff constants above which ride the injected clock. Naming both families is
// safe; routing them through one shared sleep helper is NOT — it would hang the
// test suite on these timeouts.
const (
	// idlePoolRecheckInterval bounds one wait when no hull is currently
	// dispatchable, so the pool is re-examined even if no completion event lands.
	idlePoolRecheckInterval = 30 * time.Second

	// activeWorkerWaitTimeout bounds one wait for the single active hull to
	// finish before the loop re-checks contract state.
	activeWorkerWaitTimeout = 1 * time.Minute

	// workerCompletionTimeout is the watchdog on a dispatched worker: crossing it
	// records an error and re-enters the loop rather than waiting forever.
	workerCompletionTimeout = 30 * time.Minute
)

// RunFleetCoordinatorHandler implements the fleet coordinator logic
type RunFleetCoordinatorHandler struct {
	fleetPoolManager       *contractServices.FleetPoolManager
	workerLifecycleManager *contractServices.WorkerLifecycleManager
	contractMarketService  *contractServices.ContractMarketService
	marketRepo             market.MarketRepository
	shipRepo               navigation.ShipRepository
	contractRepo           domainContract.ContractRepository
	daemonClient           daemon.DaemonClient
	graphProvider          system.ISystemGraphProvider
	converter              system.IWaypointConverter
	clock                  shared.Clock
	captainEvents          captain.EventRecorder

	// Event bus for inter-container communication
	eventSubscriber navigation.ShipEventSubscriber

	// seedMarker durably records that this coordinator has applied its
	// --dedicated-ships launch seed once, so a daemon restart does NOT replay the
	// stale seed over live fleet state. Optional; nil leaves the seed
	// un-persisted (a restart re-seeds — fail-open, matching the sibling
	// optional-port contract). The daemon injects a container-config-backed
	// marker via SetDedicatedFleetSeedMarker, mirroring the arb cost persister.
	seedMarker DedicatedFleetSeedMarker

	// idleArbLauncher starts recovery-safe one-shot arb containers for the
	// idle-gap dispatcher. Wired at daemon startup like the event subscriber;
	// nil (e.g. in tests) leaves the harvest off entirely.
	idleArbLauncher appContract.IdleArbLauncher

	// standbyProvider resolves the LIVE standby-station set from this
	// coordinator's OWN container config each discovery pass, so a hub added or
	// removed via `fleet hub add|remove` on the running coordinator is honored
	// with NO restart — the operation-level analogue of the live dedicated-fleet
	// tag read. Nil leaves homing on the frozen launch --standby-stations
	// snapshot.
	standbyProvider appContract.StandbyStationProvider

	// invFinder lets the sourcing plan see in-system warehouse stock as a
	// zero-ask source, so the defer gate does not park a contract inventory can
	// fulfill for free. Nil => market-only projection. The worker's executor
	// withdraws independently, so this only affects the coordinator's
	// park/proceed math.
	invFinder appContract.InventorySourceFinder

	// absorptionLedger is wired into the idle-arb dispatcher so it consults the
	// cross-engine ledger (skip:reserved) and records each launched leg's
	// absorption. Nil leaves the dispatcher's ledger integration inert.
	// plannedTTLSlack is the ledger knob the daemon injects alongside it
	// (RULINGS #5).
	absorptionLedger          absorption.Ledger
	absorptionPlannedTTLSlack time.Duration

	// depotRegistryProvider resolves the LIVE contract-depot routing registry
	// each pass, so an active contract whose destination is owned by a
	// configured depot is delivered via that depot's config-assigned,
	// co-located delivery hull (withdraw-local + deliver-local) instead of the
	// default distance-based pool selection + cheapest-market sourcing. Nil or
	// an empty/unavailable registry degrades to the default long-haul path
	// BYTE-IDENTICALLY — the natural off-switch, no config flag. The daemon
	// injects a store-backed provider via SetDepotRegistryProvider, mirroring
	// the invFinder / standbyProvider optional-injection idiom.
	depotRegistryProvider appContract.DepotRegistryProvider

	// standbyPlacementProvider resolves the LIVE FIXED placement slots each homing pass, replacing
	// a demand-ranked spread whose concurrent-homing timing piles idle hulls on the top-demand hub.
	// Between-legs homing zips each hull to its own slot by symbol, and the same set auto-resolves
	// the standby SET when the `fleet hub` set is empty. Backed by the SAME home-system role lookup
	// and TopDeliverySlots selection the contract auto-scaler buys against, so both positioning
	// consumers agree on ONE slot set. Nil leaves homing on the launch set; it is a READ, never a
	// config write (RULINGS #3).
	standbyPlacementProvider appContract.StandbyPlacementProvider
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
		contractRepo:           contractRepo,
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

// SetDedicatedFleetSeedMarker wires the durable first-boot marker so the
// coordinator persists "the --dedicated-ships seed has been applied" after its
// first boot and skips replaying that seed on every later restart. Left unset
// (nil), the seed is re-applied on every boot (fail-open).
func (h *RunFleetCoordinatorHandler) SetDedicatedFleetSeedMarker(marker DedicatedFleetSeedMarker) {
	h.seedMarker = marker
}

// SetIdleArbLauncher wires the daemon-server launcher the idle-gap arb
// dispatcher spawns its one-shot legs through. Optional: without it the
// coordinator runs with no harvest.
func (h *RunFleetCoordinatorHandler) SetIdleArbLauncher(launcher appContract.IdleArbLauncher) {
	h.idleArbLauncher = launcher
}

// SetStandbyStationProvider wires the live standby-station reader so the
// coordinator resolves its hub set from its own container config each pass
// instead of the frozen launch snapshot. Optional and nil-safe: without it
// homing stays on the launch --standby-stations list.
func (h *RunFleetCoordinatorHandler) SetStandbyStationProvider(provider appContract.StandbyStationProvider) {
	h.standbyProvider = provider
}

// SetAbsorptionLedger wires the cross-engine absorption ledger into the
// idle-arb dispatcher this coordinator spawns, with the PLANNED-hold TTL
// slack knob. Nil leaves the dispatcher's ledger integration inert.
func (h *RunFleetCoordinatorHandler) SetAbsorptionLedger(ledger absorption.Ledger, plannedTTLSlack time.Duration) {
	h.absorptionLedger = ledger
	h.absorptionPlannedTTLSlack = plannedTTLSlack
}

// SetInventoryFinder wires the in-system warehouse finder into the sourcing
// plan so the defer gate treats stocked goods as zero-ask. Optional and
// nil-safe: without it the coordinator plans market-only.
func (h *RunFleetCoordinatorHandler) SetInventoryFinder(finder appContract.InventorySourceFinder) {
	h.invFinder = finder
}

// SetDepotRegistryProvider wires the live contract-depot routing registry
// source so an active contract whose destination is owned by a configured
// depot routes to that depot's config-assigned delivery hull and prefers its
// co-located destination warehouse as the withdrawal source. Optional and
// nil-safe: without it — or with an empty/unavailable registry — the
// coordinator runs its default long-haul routing byte-identically.
func (h *RunFleetCoordinatorHandler) SetDepotRegistryProvider(provider appContract.DepotRegistryProvider) {
	h.depotRegistryProvider = provider
}

// SetStandbyPlacementProvider wires the live FIXED-placement reader so between-legs homing sends
// each idle hull to its permanent slot, and auto-resolves the standby set from the fixed placement
// slots when the `fleet hub` set is empty. Optional and nil-safe: without it homing stays on the
// launch set. It is the SAME slot set the contract auto-scaler homes new buys onto, so both
// positioning consumers place hulls on one set.
func (h *RunFleetCoordinatorHandler) SetStandbyPlacementProvider(provider appContract.StandbyPlacementProvider) {
	h.standbyPlacementProvider = provider
}

// Handle executes the fleet coordinator command
// fleetCoordinatorOperationType files this coordinator's spend alongside the contract work it
// exists to keep flying, rather than in a category of its own nobody would think to sum.
const fleetCoordinatorOperationType = "contract_workflow"

func (h *RunFleetCoordinatorHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	logger := common.LoggerFromContext(ctx)

	cmd, ok := request.(*RunFleetCoordinatorCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	// Everything this tick drives spends fuel that reads its attribution from here. The homing
	// command carries no identity of its own, so without this the flight books as though nobody
	// had asked for it.
	ctx = shared.WithOperationContext(ctx, shared.NewOperationContext(cmd.ContainerID, fleetCoordinatorOperationType))

	result := &RunFleetCoordinatorResponse{
		ContractsCompleted: 0,
		Errors:             []string{},
	}

	// Seed --dedicated-ships into the DedicatedFleet claim-filter exactly once,
	// on genuine first boot: replaying it on every restart would re-stamp a hull
	// deliberately `fleet remove`d, resurrecting the removal. Routed through
	// AssignShipFleetCommand, the single write path for the tag.
	if len(cmd.DedicatedShips) > 0 {
		dedicationSeed{
			logger:         logger,
			med:            h.fleetPoolManager.GetMediator(),
			playerID:       cmd.PlayerID,
			dedicatedShips: cmd.DedicatedShips,
			fleetName:      dedicatedFleetContract,
			assigner:       fmt.Sprintf("contract-coordinator-reconcile:%s", cmd.ContainerID),
		}.seedIfFirstBoot(ctx, h.seedMarker, cmd.ContainerID, cmd.DedicatedShipsSeeded)
	}

	// No pool initialization - ships are discovered dynamically

	// Events are published by ContainerRunner when worker containers complete.
	if h.eventSubscriber == nil {
		// Wiring gap must fail the container, not panic the daemon.
		return nil, fmt.Errorf("fleet coordinator: no event subscriber wired (call SetEventSubscriber at startup)")
	}
	workerCompletedCh := h.eventSubscriber.SubscribeWorkerCompleted(cmd.ContainerID)
	defer h.eventSubscriber.UnsubscribeWorkerCompleted(cmd.ContainerID, workerCompletedCh)

	// Harvests the dedicated fleet's idle time with hub-local one-shot guarded
	// arb legs. The dispatcher's reserve rule keeps contract claims instant (see
	// contract.IdleArbDispatcher); its life is bounded by this coordinator's
	// ctx, and it is inert when no dedicated fleet exists or no launcher is
	// wired.
	if h.idleArbLauncher != nil && !cmd.IdleArbDisabled {
		dispatcher := h.newIdleArbDispatcher(cmd)
		go dispatcher.Run(ctx)
	}

	if err := h.workerLifecycleManager.StopExistingWorkers(ctx, cmd.PlayerID.Value()); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed during existing worker cleanup: %v", err), nil)
	}

	// Track current active worker container ID for cleanup on shutdown
	var activeWorkerContainerID string

	// Re-adopt any in-flight contract delivery a daemon restart orphaned, BEFORE
	// the main loop's reclaim/discovery can force-release the cargo-laden ship
	// and restart the workflow from scratch. Sets the active worker so shutdown
	// cleanup and the one-worker guard both see it.
	if readoptedWorkerID := h.readoptInterruptedDeliveries(ctx, cmd); readoptedWorkerID != "" {
		activeWorkerContainerID = readoptedWorkerID
	}

	// Track previous ship for balancing logic
	var previousShipSymbol string

	// errMon makes a long streak of identical retry errors observable (a stuck
	// loop must not run silently) — edge-triggered, once per streak crossing,
	// not once per iteration.
	errMon := health.NewMonitor(health.DefaultStreakThreshold)

	// gov is the per-hull spawn-storm guard: escalating backoff after each
	// instant worker death, plus quarantine after N deaths within a window, so a
	// poison hull is skipped while the coordinator moves on to a healthy one
	// (RULINGS #1). In-memory for this run only — a restart clears it
	// deliberately, since the hull may have been fixed meanwhile.
	gov := newSpawnGovernor(h.clock)

	// liquidationCooldown is the per-hull auto-liquidation re-dispatch guard,
	// in-memory for this run only (like gov): after a parked hull is handed a
	// cargo_liquidation worker, it stays off the re-dispatch list for
	// liquidationDispatchCooldown so an unsellable hold cannot storm. A restart
	// clears it deliberately — a re-dispatch is safe (the worker no-ops an
	// already-cleared hold).
	liquidationCooldown := make(map[string]time.Time)

	// deliverHeldAttempted bounds the cycle split to ONE zero-travel deliver-held run per
	// (contract, hull), in-memory for this run only, like gov and liquidationCooldown. If a hull
	// comes back still holding its load, the next pass runs the FULL source+deliver leg rather than
	// re-dispatching the same no-op forever. A restart clears it deliberately: the split is cheap
	// and re-earning it once is safer than persisting a stale suppression.
	deliverHeldAttempted := make(map[string]bool)

	idlePoolWait := workerWait{
		ch:           workerCompletedCh,
		recheck:      idlePoolRecheckInterval,
		completedMsg: "Ship %s completed, back in pool",
	}
	activeHullWait := func(timeoutLevel, timeoutMsg string) workerWait {
		return workerWait{
			ch:           workerCompletedCh,
			recheck:      activeWorkerWaitTimeout,
			completedMsg: "Active worker completed for ship %s",
			timeoutLevel: timeoutLevel,
			timeoutMsg:   timeoutMsg,
		}
	}

	pass := &coordinatorPass{h: h, cmd: cmd, result: result, errMon: errMon}

	// Executes one contract at a time (game constraint: one active contract per player).
	for {
		select {
		case <-ctx.Done():
			h.stopActiveWorker(ctx, activeWorkerContainerID)
			return result, ctx.Err()
		default:
		}

		// Reclaim ships orphaned by a restart-killed worker, unconditionally on
		// every pass — not only when the whole fleet is starved. An orphaned ship
		// stays IsAssigned()==true forever unless something proactively releases
		// it, so gating this on fleet starvation would leave it stuck whenever
		// any other hull is idle.
		if reclaimed, reclaimErr := h.workerLifecycleManager.ReclaimShipsFromInterruptedWorkers(ctx, cmd.PlayerID.Value(), h.clock); reclaimErr != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to reclaim ships from interrupted workers: %v", reclaimErr), nil)
		} else if reclaimed > 0 {
			logger.Log("INFO", fmt.Sprintf("Reclaimed %d ship(s) from interrupted workers", reclaimed), nil)
			continue // Re-check ctx.Done() and re-discover candidates with the freed ship(s)
		}

		pool, ok := pass.discoverShipPool(ctx)
		if !ok {
			continue
		}

		// The interrupted-worker reclaim pass already ran unconditionally at the
		// top of this iteration, so by this point any orphan has already been
		// freed if one existed.
		if len(pool.available) == 0 {
			logger.Log("INFO", "No ships available, waiting for completion...", nil)
			if h.awaitWorkerSlot(ctx, idlePoolWait, &activeWorkerContainerID) {
				return result, ctx.Err()
			}
			continue
		}

		// DETERMINISTIC SINGLE-HULL GATE. activeWorkerContainerID is this coordinator's OWN
		// synchronous record of the worker it spawned or re-adopted this lifetime, set the instant
		// readoptInterruptedDeliveries re-adopts a cargo-laden hull, BEFORE that worker's async
		// StartContainer surfaces it as RUNNING. During that window EVERY durable defense is blind:
		// the RUNNING-status FindExistingWorkers query sees zero workers, calculateInFlightCargo
		// sees the hull on neither a RUNNING nor the dead FAILED container, and
		// idleReclaimedContractCargoHeld sees it as assigned — so the loop would dispatch a SECOND
		// hull onto the same contract and buy a duplicate load. The contract runs exactly ONE hull
		// at a time: while this coordinator holds an active hull it WAITS for that hull rather than
		// dispatching another, and only once the completion event clears the id does the loop fall
		// through to distance-based selection of the NEXT hull.
		if activeWorkerContainerID != "" {
			logger.Log("INFO", fmt.Sprintf("Contract already has an active hull (worker %s) - waiting for it to complete before dispatching another (sp-zve2q deterministic single-hull)", activeWorkerContainerID), nil)
			if h.awaitWorkerSlot(ctx, activeHullWait("INFO", "Timeout waiting for active hull, will re-check"), &activeWorkerContainerID) {
				return result, ctx.Err()
			}
			continue
		}

		// CRITICAL CHECK: Prevent multiple workers by checking if any worker is already running
		// This prevents race conditions when negotiation fails early in the loop
		existingActiveWorkers, err := h.workerLifecycleManager.FindExistingWorkers(ctx, cmd.PlayerID.Value())
		if err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to check for active workers: %v", err), nil)
		} else if len(existingActiveWorkers) > 0 {
			logger.Log("WARNING", fmt.Sprintf("Found %d active CONTRACT_WORKFLOW workers - waiting instead of creating new worker", len(existingActiveWorkers)), nil)
			if h.awaitWorkerSlot(ctx, activeHullWait("WARNING", "Timeout waiting for active worker, will check again"), &activeWorkerContainerID) {
				return result, ctx.Err()
			}
			continue
		}

		// Negotiate contract (use any ship from pool for negotiation)
		logger.Log("INFO", "Negotiating new contract...", nil)
		contract, err := h.contractMarketService.NegotiateContract(ctx, pool.available[0], cmd.PlayerID.Value())
		if err != nil {
			pass.recordStepFailure(ctx, "negotiate_contract", fmt.Sprintf("Failed to negotiate contract: %v", err), err)
			h.clock.Sleep(negotiateRetryBackoff)
			continue
		}
		errMon.Note("negotiate_contract", "")

		if _, stillOwed := outstandingDelivery(contract); !stillOwed {
			logger.Log("INFO", "Contract deliveries complete - fulfilling contract to get reward", map[string]interface{}{
				"contract_id": contract.ContractID(),
			})
			if err := h.contractMarketService.FulfillContract(ctx, contract, cmd.PlayerID); err != nil {
				logger.Log("ERROR", fmt.Sprintf("Failed to fulfill contract: %v", err), nil)
			} else {
				logger.Log("INFO", "Contract fulfilled successfully - will negotiate new contract", nil)
				result.ContractsCompleted++
			}
			h.clock.Sleep(postFulfillmentPause)
			continue
		}

		// Contract sourcing is HOME-system only (RULINGS #14): the worker's trip
		// is in-system NavigateAndDock with zero jump capability, so a
		// cross-system source it can't reach is never a candidate.
		logger.Log("INFO", "Planning sourcing (cheapest home-system market)...", nil)
		plan, err := appContract.PlanSourcing(ctx, contract, h.marketRepo, cmd.PlayerID.Value(), appContract.WithInventoryFinder(h.invFinder))
		if err != nil {
			// Market data not yet available - this is expected while scouts are scanning
			logger.Log("INFO", "Purchase market not yet available - waiting for scouts to scan market data", map[string]interface{}{
				"contract_id": contract.ContractID(),
				"error":       err.Error(),
			})
			// Sleep and retry - scouts will eventually scan the required market
			h.clock.Sleep(awaitMarketDataBackoff)
			continue
		}
		purchaseMarket := plan.Market

		contract, deferSourcing := h.applySourcingDefer(ctx, cmd, contract, plan)
		if deferSourcing {
			h.clock.Sleep(appContract.SourcingDeferRecheckInterval)
			continue
		}

		// deliveryDestination is the active delivery's waypoint; its system is the
		// contract's HOME system, the same scope PlanSourcing derives sourcing
		// from above, used to keep the worker grab home-local.
		var requiredCargo string
		var unitsNeeded int
		var deliveryDestination string
		if delivery, stillOwed := outstandingDelivery(contract); stillOwed {
			requiredCargo = delivery.TradeSymbol
			unitsNeeded = delivery.UnitsRequired - delivery.UnitsFulfilled
			deliveryDestination = delivery.DestinationSymbol
		}

		// Check for in-flight cargo from active workers (prevent duplicate purchases on restart)
		inFlightCargo, err := h.calculateInFlightCargo(ctx, requiredCargo, cmd.PlayerID.Value())
		if err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to calculate in-flight cargo: %v", err), nil)
			// Continue anyway - better to risk duplication than block indefinitely
		}

		h.noteIdleHullsCoverShortfall(ctx, contract.ContractID(), requiredCargo, unitsNeeded, inFlightCargo, cmd.PlayerID.Value())

		// If there's already enough in-flight cargo, wait for delivery instead of assigning new work
		if inFlightCargo >= unitsNeeded {
			logger.Log("INFO", fmt.Sprintf("Contract already has %d units of %s in-flight (needed: %d) - waiting for delivery instead of assigning new ship",
				inFlightCargo, requiredCargo, unitsNeeded), nil)
			if h.awaitWorkerSlot(ctx, activeHullWait("INFO", "Timeout waiting for delivery, will re-check"), &activeWorkerContainerID) {
				return result, ctx.Err()
			}
			continue
		}

		// Log remaining units needed after accounting for in-flight cargo
		if inFlightCargo > 0 {
			logger.Log("INFO", fmt.Sprintf("Contract needs %d more units of %s (%d in-flight, %d required, %d fulfilled)",
				unitsNeeded-inFlightCargo, requiredCargo, inFlightCargo, unitsNeeded+contract.Terms().Deliveries[0].UnitsFulfilled, contract.Terms().Deliveries[0].UnitsFulfilled), nil)
		}

		// HOME-SYSTEM LOCALITY: the contract worker sources AND delivers in the
		// delivery's HOME system with ZERO jump capability (RULINGS #14). A hull
		// idle in a FOREIGN system can reach neither the source market nor the
		// delivery, so it must be UNSELECTABLE here rather than claimed-then-
		// stalled. Scoped to the GENERAL grab pool only; a dedicated fleet
		// (EXCLUSIVE MODE) passes through untouched. An empty home-scoped pool
		// falls through to the "no spawnable ships" wait branch below.
		pool.available, err = h.scopeCandidatesToContractHome(ctx, cmd.PlayerID, pool.available, deliveryDestination, pool.dedicatedFleetActive)
		if err != nil {
			pass.retryAfterStepFailure(ctx, "", fmt.Sprintf("Failed to scope candidates to contract home system: %v", err), err)
			continue
		}

		// NO-CARGO-DUMP CLAIM GUARD: a candidate hull already holding cargo
		// unrelated to this contract's delivery good must be parked, never
		// claimed — claiming it would let downstream cargo handling jettison
		// whatever it was carrying to satisfy a contract that has nothing to do
		// with that cargo. Filter BEFORE SelectClosestShip so an unrelated-cargo
		// hull is never even distance-ranked as the winner.
		claimableShips, parkedShips, err := appContract.FilterUnrelatedCargo(ctx, cmd.PlayerID, h.shipRepo, pool.available, requiredCargo)
		if err != nil {
			pass.retryAfterStepFailure(ctx, "", fmt.Sprintf("Failed to filter candidates by cargo: %v", err), err)
			continue
		}

		// AUTO-LIQUIDATION: hulls just parked for holding cargo unrelated to this
		// contract are handed a one-shot cargo_liquidation worker that sells the
		// leftover at the best in-system bid so the hull re-enters candidacy,
		// instead of staying filtered out of candidacy every pass. Never blocks
		// the contract work below — claimableShips (which excludes parkedShips)
		// proceeds regardless.
		h.dispatchLiquidationForParked(ctx, cmd, parkedShips, requiredCargo, liquidationCooldown)

		// Spawn-governor exclusion: drop hulls in post-instant-death backoff or
		// quarantined for repeated instant deaths / repeated identical failures,
		// so the coordinator spawns a worker on a HEALTHY hull instead of
		// hot-respawning a poison one. Both holds are TEMPORARY — a backoff'd hull
		// re-enters selection when its interval expires, a quarantined one when its
		// cooldown does (and is then re-probed with a single worker), so a hull
		// fixed upstream returns to service unattended. Only when EVERY candidate
		// is held does the coordinator park and wait (RULINGS #1: a deferral, never
		// a skip).
		spawnableShips, heldShips := gov.FilterEligible(claimableShips)
		if len(spawnableShips) == 0 {
			logger.Log("INFO", fmt.Sprintf(
				"No spawnable ships - %d hold unrelated cargo, %d in spawn-backoff/quarantine; waiting for completion...",
				len(parkedShips), len(heldShips)), nil)
			if h.awaitWorkerSlot(ctx, idlePoolWait, &activeWorkerContainerID) {
				return result, ctx.Err()
			}
			continue
		}

		// Depot routing seam: BEFORE the default distance-based hull selection,
		// consult the LIVE depot registry (resolved fail-safe each pass from the
		// boot-loaded durable store). An owning depot with a config-assigned
		// delivery hull may divert THIS contract onto that pinned, co-located
		// hull (withdraw-local + deliver-local). A nil/empty/unavailable
		// registry, or a destination no depot owns, returns routeMatched=false
		// and the default SelectClosestShip path runs BYTE-IDENTICALLY.
		route, routeMatched := routeContractViaDepot(
			appContract.ResolveDepotRegistry(ctx, logger, h.depotRegistryProvider, cmd.PlayerID.Value()),
			contract,
			// Ranks the depot's delivery hulls by the SAME in-system distance the
			// default SelectClosestShip path uses, so a MULTI-hub delivery fleet
			// routes each contract to its cluster's nearest hull. A single-hull
			// depot never invokes this (byte-identical).
			newDepotDeliveryDistance(ctx, h.graphProvider, cmd.PlayerID.Value()),
		)

		// The depot delivery hull is destination-pinned, so it is the right hull
		// ONLY when the good is already BUFFERED at the hub (deliver from stock,
		// ~0 source travel). When UNBUFFERED (must be bought at a remote source
		// market), routing to the pinned hull would fly it empty to the far
		// source and back (~2x) and leave its hub uncovered; resolveContractHullRoute
		// then declines the depot hull so the coordinator falls through to
		// source-nearest idle-hull selection below.
		var selectedShip string
		var distance float64
		// DELIVER-HELD dispatch (sp-5jce2): set only when the holder short-circuit
		// below fires on a badly-placed PARTIAL holder standing on the delivery.
		deliverHeldOnly := false

		// DETERMINISTIC HOLDER SHORT-CIRCUIT. An IDLE hull already holding the contract good
		// delivers that EXISTING load, so it MUST win over sourcing a duplicate onto the closest
		// empty hull. idleReclaimedContractCargoHeld DETECTS such a holder above, but the
		// candidate-discovery filters drop it when it is in transit or undedicated while a
		// dedicated fleet is active — so SelectClosestShip's own cargo-priority never sees it and
		// the closest EMPTY hull double-sources. This binds detection to selection: name the pure
		// idle holder and select it directly, deterministically the SAME hull on a restart. A pure
		// holder cannot trip the NO-CARGO-DUMP guard, so the claim is safe; the normal no-holder
		// pass returns "" and falls through to the unchanged depot/closest selection.
		holder, holderErr := h.idleContractCargoHolder(ctx, requiredCargo, cmd.PlayerID.Value())
		if holderErr != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to resolve idle contract-cargo holder (falling back to distance selection): %v", holderErr), nil)
		}
		if holder != "" {
			selectedShip = holder

			// WEIGHED, NOT ABSOLUTE. The short-circuit above is unconditional, and a hull ends
			// every cycle AT THE DELIVERY — the point maximally far from the source — so "any
			// holder wins" re-picks the worst-placed hull for the next source run, every cycle.
			// When the held load is only a PARTIAL and a spawnable hull sits decisively closer to
			// the source, split the cycle instead: this hull still runs, but only to register what
			// it is standing on at zero travel, and the next pass sources the remainder with the
			// near hull. The held load is neither stranded nor re-bought, so the duplicate-sourcing
			// defense is intact.
			deliverHeldOnly = h.decideDeliverHeldFirst(ctx, contract.ContractID(), holderRun{
				Holder:         holder,
				Candidates:     spawnableShips,
				SourceWaypoint: purchaseMarket,
				Destination:    deliveryDestination,
				Good:           requiredCargo,
				UnitsNeeded:    unitsNeeded,
				PlayerID:       cmd.PlayerID.Value(),
			}, deliverHeldAttempted)

			logHolderSelection(logger, contract.ContractID(), holder, requiredCargo, deliverHeldOnly)
		} else if hullRoute := resolveContractHullRoute(route, routeMatched, plan); hullRoute.UseDepotHull {
			selectedShip = hullRoute.DepotHull
			logDepotRouteBuffered(logger, contract.ContractID(), route, purchaseMarket)
		} else {
			// A depot owns the destination but the good is UNBUFFERED — DECOUPLE
			// sourcing from delivery rather than pull the pinned depot hull off
			// its hub to fly ~2x; source with the idle hull nearest the SOURCE
			// market instead (the default path below).
			if routeMatched {
				logDepotRouteUnbuffered(logger, contract.ContractID(), route, requiredCargo, purchaseMarket)
			}
			// DEFAULT PATH: select closest ship to purchase market (prioritizes
			// ships with required cargo).
			logger.Log("INFO", fmt.Sprintf("Selecting closest ship (required cargo: %s)...", requiredCargo), nil)
			selectedShip, distance, err = appContract.SelectClosestShip(
				ctx,
				spawnableShips,
				h.shipRepo,
				h.graphProvider,
				h.converter,
				purchaseMarket,
				requiredCargo,
				unitsNeeded,
				cmd.PlayerID.Value(),
			)
			if err != nil {
				pass.retryAfterStepFailure(ctx, "select_closest_ship", fmt.Sprintf("Failed to select ship: %v", err), err)
				continue
			}
			errMon.Note("select_closest_ship", "")
		}

		logger.Log("INFO", fmt.Sprintf("Selected %s (distance: %.2f units)", selectedShip, distance), nil)

		h.repositionPreviousShip(ctx, cmd, previousShipSymbol, selectedShip)

		// Last-resort verdict for the claim-side guard: the command frigate may be
		// drafted for this leg only when NO regular hauler was an idle candidate
		// this pass (RULINGS #7 — the frigate hauls only as a last resort). A cargo
		// HOLDER completing its EXISTING load is never a fresh last-resort draft
		// (like a readopt) — so a command frigate already holding contract cargo is
		// authorized here rather than benched, matching readoptInterruptedDeliveries.
		commandDraftAllowed := holder != "" || !hasRegularHaulerCandidate(pool.generalEntities)

		workerContainerID, err := h.spawnContractWorker(ctx, cmd, selectedShip, commandDraftAllowed, deliverHeldOnly)
		if err != nil {
			pass.retryAfterStepFailure(ctx, "spawn_contract_worker", err.Error(), err)
			continue
		}
		errMon.Note("spawn_contract_worker", "")

		activeWorkerContainerID = workerContainerID
		// Timestamp the spawn so an instant death of this worker is measurable
		// against the spawn-governor's instant-death threshold.
		gov.NoteSpawn(selectedShip)

		logger.Log("INFO", fmt.Sprintf("Waiting for %s to complete contract...", selectedShip), nil)
		select {
		case event := <-workerCompletedCh:
			// Feed the completion to the spawn governor FIRST, error text and all: a worker that
			// died instantly extends this hull's backoff, and either the Nth instant death within
			// the window OR the Nth consecutive IDENTICAL error — the breaker for a hull that takes
			// minutes to die the same way — crosses it into an expiring quarantine. On that
			// crossing emit the loud line, captain event and counter; thereafter the hull is
			// skipped until its cooldown elapses and the next pass selects a healthy hull
			// (RULINGS #1: the contract keeps being worked).
			if outcome := gov.NoteCompletion(event.ShipSymbol, event.Success, event.Error); outcome.JustQuarantined {
				logger.Log("ERROR", hullQuarantineMessage(event.ShipSymbol, outcome), map[string]interface{}{
					"action":           "hull_quarantined",
					"ship_symbol":      event.ShipSymbol,
					"cause":            outcome.Cause,
					"instant_deaths":   outcome.InstantDeaths,
					"identical_errors": outcome.IdenticalErrors,
					"cooldown_seconds": outcome.Cooldown.Seconds(),
				})
				metrics.RecordHullQuarantine(outcome.Cause)
				h.recordHullQuarantineEvent(ctx, cmd, event.ShipSymbol, outcome)
			}
			if recordWorkerCompletion(logger, event, fmt.Sprintf("Contract completed by %s", event.ShipSymbol)) {
				result.ContractsCompleted++
				// Home the just-delivered hull to a standby sink IMMEDIATELY
				// instead of leaving it loitering at the delivery waypoint until the next
				// between-legs selection change or the ~90s idle-arb sweep. Scoped to a
				// successful delivery (the hull is free/idle at the delivery waypoint here);
				// reuses the coordinator's ONE homing path — frigate-excluded, no-thrash,
				// best-effort.
				h.homeCompletedHullToStandby(ctx, cmd, event.ShipSymbol)
			}
			activeWorkerContainerID = ""
			previousShipSymbol = event.ShipSymbol

			continue

		case <-time.After(workerCompletionTimeout):
			logger.Log("ERROR", fmt.Sprintf("Timeout waiting for worker %s", selectedShip), nil)
			errMsg := fmt.Sprintf("Worker timeout for ship %s", selectedShip)
			result.Errors = append(result.Errors, errMsg)
			continue

		case <-ctx.Done():
			logger.Log("INFO", "Context cancelled, exiting coordinator", nil)
			h.stopActiveWorker(ctx, activeWorkerContainerID)
			return result, ctx.Err()
		}
	}
}

// applySourcingDefer parks the pass when projected net is worse than the defer
// threshold. RULINGS #1 never-skip means Defer never fires here; it surfaces as Overridden.
func (h *RunFleetCoordinatorHandler) applySourcingDefer(
	ctx context.Context,
	cmd *RunFleetCoordinatorCommand,
	contract *domainContract.Contract,
	plan *appContract.SourcingPlan,
) (*domainContract.Contract, bool) {
	logger := common.LoggerFromContext(ctx)
	decision := appContract.EvaluateSourcingDefer(plan, contract, h.clock.Now())

	if !decision.Defer {
		if decision.Overridden {
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
		return contract, false
	}

	// Acceptance is what makes NegotiateContract resume the parked contract; an
	// unaccepted one parked past its accept-by deadline would be a skip.
	accepted, acceptErr := h.contractMarketService.EnsureAccepted(ctx, contract, cmd.PlayerID)
	if acceptErr != nil {
		logger.Log("WARNING", fmt.Sprintf(
			"Sourcing defer wanted but contract %s could not be accepted first (%v) - sourcing this pass instead of parking (never-skip)",
			contract.ContractID(), acceptErr), nil)
		return contract, false
	}

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
	return contract, true
}

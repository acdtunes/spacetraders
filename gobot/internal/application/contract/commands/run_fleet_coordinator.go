package commands

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	appContract "github.com/andrescamacho/spacetraders-go/internal/application/contract"
	contractServices "github.com/andrescamacho/spacetraders-go/internal/application/contract/services"
	contractTypes "github.com/andrescamacho/spacetraders-go/internal/application/contract/types"
	"github.com/andrescamacho/spacetraders-go/internal/application/health"
	"github.com/andrescamacho/spacetraders-go/internal/application/liquidation"
	shipAssignment "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/assignment"
	"github.com/andrescamacho/spacetraders-go/internal/domain/absorption"
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
	contractRepo           domainContract.ContractRepository
	daemonClient           daemon.DaemonClient
	graphProvider          system.ISystemGraphProvider
	converter              system.IWaypointConverter
	clock                  shared.Clock
	captainEvents          captain.EventRecorder

	// Event bus for inter-container communication
	eventSubscriber navigation.ShipEventSubscriber

	// seedMarker (sp-86vb) durably records that this coordinator has applied its
	// --dedicated-ships launch seed once, so a daemon restart does NOT replay the
	// stale seed over live fleet state. Optional; nil leaves the seed un-persisted
	// (a restart then re-seeds exactly as before this fix — fail-open, matching the
	// sibling optional-port contract). The daemon injects a container-config-backed
	// marker via SetDedicatedFleetSeedMarker, mirroring the arb cost persister.
	seedMarker DedicatedFleetSeedMarker

	// idleArbLauncher (sp-1z2h) starts recovery-safe one-shot arb containers
	// for the idle-gap dispatcher. Wired at daemon startup like the event
	// subscriber; nil (e.g. in tests) leaves the harvest off entirely.
	idleArbLauncher appContract.IdleArbLauncher

	// standbyProvider (sp-jcke) resolves the LIVE standby-station set from this
	// coordinator's OWN container config each discovery pass, so a hub added or
	// removed via `fleet hub add|remove` on the running coordinator is honored
	// with NO restart — the operation-level analogue of the live dedicated-fleet
	// tag read (sp-cmwc). Nil (tests / daemon predating the wiring) leaves homing
	// on the frozen launch --standby-stations snapshot, exactly as before.
	standbyProvider appContract.StandbyStationProvider

	// invFinder (sp-dchv Lane D) lets the sourcing plan see in-system warehouse
	// stock as a zero-ask source, so the defer gate does not park a contract
	// inventory can fulfill for free. Nil (tests / feature off) => market-only
	// projection, byte-identical to before. The worker's executor withdraws
	// independently, so this only affects the coordinator's park/proceed math.
	invFinder appContract.InventorySourceFinder

	// absorptionLedger (sp-78ai L2) is wired into the idle-arb dispatcher so it
	// consults the cross-engine ledger (skip:reserved) and records each launched
	// leg's absorption. Nil (tests / feature off) leaves the dispatcher's ledger
	// integration inert. consultDisabled + plannedTTLSlack are the ledger knobs the
	// daemon injects alongside it (RULINGS #5).
	absorptionLedger          absorption.Ledger
	absorptionConsultOff      bool
	absorptionPlannedTTLSlack time.Duration

	// depotRegistryProvider (sp-u9xa, the final seam) resolves the LIVE contract-
	// depot routing registry each pass, so an active contract whose destination is
	// owned by a configured depot is delivered via that depot's config-assigned,
	// co-located delivery hull (withdraw-local + deliver-local) instead of the default
	// distance-based pool selection + cheapest-market sourcing. Nil (tests / feature
	// off) or an empty/unavailable registry degrades to the default long-haul path
	// BYTE-IDENTICALLY — the natural off-switch, no config flag. The daemon injects a
	// store-backed provider via SetDepotRegistryProvider, mirroring the invFinder /
	// standbyProvider optional-injection idiom.
	depotRegistryProvider appContract.DepotRegistryProvider
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

// SetDedicatedFleetSeedMarker wires the durable first-boot marker (sp-86vb) so
// the coordinator persists "the --dedicated-ships seed has been applied" after
// its first boot and skips replaying that seed on every later restart. Left unset
// (nil), the seed is re-applied on every boot exactly as before this fix
// (fail-open). Mirrors the SetIdleArbLauncher optional-injection idiom.
func (h *RunFleetCoordinatorHandler) SetDedicatedFleetSeedMarker(marker DedicatedFleetSeedMarker) {
	h.seedMarker = marker
}

// SetIdleArbLauncher wires the daemon-server launcher the idle-gap arb
// dispatcher (sp-1z2h) spawns its one-shot legs through. Optional: without it
// the coordinator runs exactly as before, no harvest.
func (h *RunFleetCoordinatorHandler) SetIdleArbLauncher(launcher appContract.IdleArbLauncher) {
	h.idleArbLauncher = launcher
}

// SetStandbyStationProvider wires the live standby-station reader (sp-jcke) so
// the coordinator resolves its hub set from its own container config each pass
// instead of the frozen launch snapshot — the operation-level mirror of the live
// dedicated-fleet tag read (sp-cmwc). Optional and nil-safe: without it homing
// stays on the launch --standby-stations list, exactly as before. The daemon
// injects a container-config-backed provider at startup, mirroring the seed
// marker's optional-injection idiom.
func (h *RunFleetCoordinatorHandler) SetStandbyStationProvider(provider appContract.StandbyStationProvider) {
	h.standbyProvider = provider
}

// SetAbsorptionLedger wires the cross-engine absorption ledger (sp-78ai L2) into the
// idle-arb dispatcher this coordinator spawns, with the two ledger knobs (consult
// kill-switch, PLANNED-hold TTL slack). Nil leaves the dispatcher's ledger
// integration inert. Mirrors the SetIdleArbLauncher startup-injection idiom.
func (h *RunFleetCoordinatorHandler) SetAbsorptionLedger(ledger absorption.Ledger, consultDisabled bool, plannedTTLSlack time.Duration) {
	h.absorptionLedger = ledger
	h.absorptionConsultOff = consultDisabled
	h.absorptionPlannedTTLSlack = plannedTTLSlack
}

// SetInventoryFinder wires the in-system warehouse finder (sp-dchv Lane D) into
// the sourcing plan so the defer gate treats stocked goods as zero-ask. Optional
// and nil-safe: without it the coordinator plans market-only, exactly as before.
func (h *RunFleetCoordinatorHandler) SetInventoryFinder(finder appContract.InventorySourceFinder) {
	h.invFinder = finder
}

// SetDepotRegistryProvider wires the live contract-depot routing registry source
// (sp-u9xa, the final seam) so an active contract whose destination is owned by a
// configured depot routes to that depot's config-assigned delivery hull and prefers
// its co-located destination warehouse as the withdrawal source. Optional and nil-safe:
// without it — or with an empty/unavailable registry — the coordinator runs its default
// long-haul routing byte-identically (empty registry == today's behavior). Mirrors the
// SetInventoryFinder / SetStandbyStationProvider optional-injection idiom.
func (h *RunFleetCoordinatorHandler) SetDepotRegistryProvider(provider appContract.DepotRegistryProvider) {
	h.depotRegistryProvider = provider
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

	// Seed the operator's --dedicated-ships list into the DedicatedFleet
	// claim-filter (sp-snmb) — but ONCE, on genuine first boot only (sp-86vb).
	// Routed through AssignShipFleetCommand, the single write path for the tag
	// (sp-l7h2). On a daemon restart the launch seed is a STALE snapshot: a hull
	// deliberately `fleet remove`d while the coordinator ran had its live tag
	// cleared, and replaying the immutable seed would re-stamp "contract" onto it,
	// resurrecting the removal. seedDedicatedFleetIfFirstBoot gates the replay on
	// the persisted DedicatedShipsSeeded marker so the live tag stays authoritative
	// across restarts. The len>0 guard keeps the empty default from touching the
	// mediator at all (the seed function is a no-op then anyway).
	if len(cmd.DedicatedShips) > 0 {
		seedDedicatedFleetIfFirstBoot(ctx, logger, h.fleetPoolManager.GetMediator(), h.seedMarker,
			cmd.PlayerID, cmd.ContainerID, cmd.DedicatedShips, cmd.DedicatedShipsSeeded, dedicatedFleetContract,
			fmt.Sprintf("contract-coordinator-reconcile:%s", cmd.ContainerID))
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
		// Post-leg re-homing (sp-8bpr): the dispatcher sends an arb hull that
		// finished off-station back to standby through the coordinator's OWN
		// balanced-standby homing (HomeShipCommand, l7h2 Phase 3) — the same
		// stations and fleet-peer list the contract-handoff homing below uses,
		// never a parallel homing path (RULINGS #7). Empty StandbyStations leaves
		// re-homing off, exactly as it leaves the contract-handoff homing off.
		homer := &mediatorShipHomer{
			mediator: h.fleetPoolManager.GetMediator(),
			shipRepo: h.shipRepo,
			playerID: cmd.PlayerID,
			fleet:    dedicatedFleetContract,
		}
		dispatcher := appContract.NewIdleArbDispatcher(
			h.shipRepo,
			h.marketRepo,
			h.graphProvider,
			h.idleArbLauncher,
			homer,
			appContract.NewActiveContractGoods(h.contractRepo),
			h.clock,
			cmd.PlayerID,
			dedicatedFleetContract,
			appContract.IdleArbConfig{
				ReserveHulls:     cmd.IdleArbReserveHulls,
				HubRadius:        cmd.IdleArbHubRadius,
				LeashRadius:      cmd.IdleArbLeashRadius,
				MaxLegDuration:   time.Duration(cmd.IdleArbMaxLegSecs) * time.Second,
				MaxSpendPerLeg:   cmd.IdleArbMaxSpend,
				MinMarginPerUnit: cmd.IdleArbMinMargin,
				// Percent → fraction (0 → WithDefaults applies the 0.80 default).
				MarginVerifyFraction: float64(cmd.IdleArbMarginVerifyPct) / 100.0,
				Blacklist:            cmd.IdleArbBlacklist,
				StandbyStations:      cmd.StandbyStations,
				Interval:             time.Duration(cmd.IdleArbIntervalSecs) * time.Second,
				// sp-lbbm lane mutex recovery hold (0 → WithDefaults applies 20min).
				RecoveryHold: time.Duration(cmd.IdleArbRecoveryHoldSecs) * time.Second,
				// sp-u4tv per-trip profitability floor. Percent → fraction (0 →
				// WithDefaults applies 100/u, 0.20, 35/u fuel).
				MinNetProfitPerUnit: cmd.IdleArbMinNetProfit,
				NetProfitFraction:   float64(cmd.IdleArbNetProfitPct) / 100.0,
				FuelCostPerUnit:     cmd.IdleArbFuelCostPerUnit,
			},
		)
		// sp-78ai L2: wire the cross-engine absorption ledger so the dispatcher
		// consults it (skip:reserved) and records launched legs. Inert when unwired.
		dispatcher.SetAbsorptionLedger(h.absorptionLedger, h.absorptionConsultOff, h.absorptionPlannedTTLSlack)
		// LIVE hub set (sp-jcke): the dispatcher's post-leg re-homing resolves the
		// CURRENT standby set each pass from this coordinator's container config, so a
		// `fleet hub add|remove` re-homes idle hulls across the new set with no
		// restart. Falls back to cmd.StandbyStations on a read failure / no provider.
		dispatcher.SetStandbyResolver(func(resolveCtx context.Context) []string {
			return appContract.ResolveStandbyStations(resolveCtx, common.LoggerFromContext(resolveCtx), h.standbyProvider, cmd.ContainerID, cmd.PlayerID.Value(), cmd.StandbyStations)
		})
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
	errMon := health.NewMonitor(health.DefaultStreakThreshold)

	// gov (sp-lybx) is the per-hull spawn-storm guard: an escalating backoff
	// after each instant worker death, plus a quarantine after N instant deaths
	// within a window, so a poison hull is skipped for the rest of this run while
	// the coordinator moves on to a healthy hull (the CONTRACT keeps being
	// worked — RULINGS #1). Clock-injected; its state is in-memory for this run
	// only — a coordinator recreate/restart clears it deliberately, since the
	// hull may have been fixed (reclassified, repaired, unpinned) meanwhile.
	gov := newSpawnGovernor(h.clock)

	// liquidationCooldown (sp-39oi) is the per-hull auto-liquidation re-dispatch guard,
	// in-memory for this run only (like gov): after a parked-with-cargo hull is handed a
	// cargo_liquidation worker, it stays off the re-dispatch list for
	// liquidationDispatchCooldown so an unsellable hold cannot storm. A restart clears it
	// deliberately — a re-dispatch after restart is safe (the worker reconciles the hull
	// and no-ops an already-cleared hold).
	liquidationCooldown := make(map[string]time.Time)

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
		generalShipEntities, generalShips, err := appContract.FindIdleLightHaulers(ctx, cmd.PlayerID, h.shipRepo, "", appContract.IncludeCommandShip)
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

		// Command-cargo baseline (sp-uj6a): FindIdleLightHaulers' generic cargo
		// check only screens out probes (CargoCapacity() == 0) - it does not stop
		// a stock command frigate from competing for legs a light hauler would
		// single-trip. A command hull below baseline double-trips a load a
		// dedicated hauler moves in one pass, spending its whole speed advantage
		// on the extra leg for a net loss versus just dispatching the hauler, so
		// it is filtered back out here, after discovery and before any candidate
		// is ranked or claimed - the ranking ladder (SelectHullForCargo) never
		// sees a hull this gate already removed.
		generalShips = appContract.FilterCommandCargoBaseline(ctx, generalShipEntities, cmd.CommandCargoBaseline)

		// The coordinator's own dedicated fleet (sp-snmb) is invisible to
		// FindIdleLightHaulers via the claim-filter, so it is looked up
		// separately here. Looked up by fleet NAME from the persisted tag
		// (sp-l7h2), not the remembered --dedicated-ships list: a `fleet
		// assign`/`unassign` while this coordinator runs takes effect on the
		// very next pass, no restart needed.
		// RequireCargoCapacity (sp-lybx): a 0-cargo hull mispinned into the
		// contract fleet is UNSELECTABLE here — it can never carry a delivery, so
		// claiming it just spawns a worker that dies instantly. The idle-arb
		// dispatcher's own FindIdleShipsByFleet calls omit the policy and keep
		// every tagged member, so its reserve accounting is unchanged.
		_, dedicatedIdleShips, err := appContract.FindIdleShipsByFleet(ctx, cmd.PlayerID, h.shipRepo, dedicatedFleetContract, appContract.RequireCargoCapacity)
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

		// Find the cheapest HOME-system purchase market for the contract (sp-1z2h
		// sourcing cost-optimizer). Contract sourcing is HOME-system only by ruling
		// (RULINGS #14): the worker's trip is in-system NavigateAndDock with zero
		// jump capability, so a cross-system source it can't reach is never a
		// candidate — no selected-then-crashed ('waypoint not found in cache' —
		// sp-9hu8).
		logger.Log("INFO", "Planning sourcing (cheapest home-system market)...", nil)
		plan, err := appContract.PlanSourcing(ctx, contract, h.marketRepo, cmd.PlayerID.Value(), appContract.WithInventoryFinder(h.invFinder))
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

		// AUTO-LIQUIDATION (sp-39oi): the hulls just parked for holding cargo unrelated
		// to this contract are the strand class that jams the pool. Instead of leaving
		// them filtered out of candidacy every pass (the fleet-scale zero-fulfillment
		// jam), hand each a one-shot cargo_liquidation worker that sells the leftover at
		// the best in-system bid so the hull re-enters candidacy. It runs even when
		// spawnable hulls remain, clearing strands as they appear rather than only once
		// the whole pool has jammed, and never blocks the contract work below —
		// claimableShips (which excludes parkedShips) proceeds regardless.
		h.dispatchLiquidationForParked(ctx, cmd, parkedShips, liquidationCooldown)

		// Spawn-governor exclusion (sp-lybx): drop hulls in post-instant-death
		// backoff or quarantined for repeated instant worker deaths (ANY cause,
		// the generic net beyond Fix A's 0-cargo exclusion), so the coordinator
		// spawns a worker on a HEALTHY hull instead of hot-respawning a poison
		// one. A backoff'd hull re-enters selection when its interval expires; a
		// quarantined one stays out for the rest of this run. Whenever a healthy
		// hull remains the contract keeps being worked by it (RULINGS #1); only
		// when EVERY candidate is held does the coordinator park and wait — a
		// deferral, never a skip.
		spawnableShips, heldShips := gov.FilterEligible(claimableShips)
		if len(spawnableShips) == 0 {
			logger.Log("INFO", fmt.Sprintf(
				"No spawnable ships - %d hold unrelated cargo, %d in spawn-backoff/quarantine; waiting for completion...",
				len(parkedShips), len(heldShips)), nil)
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

		// sp-u9xa depot routing seam (the FINAL integration): BEFORE the default
		// distance-based hull selection, consult the LIVE depot registry (resolved
		// fail-safe each pass from the boot-loaded durable store). An owning depot with
		// a config-assigned delivery hull may divert THIS contract onto that pinned,
		// co-located hull (withdraw-local + deliver-local). A nil/empty/unavailable
		// registry, or a destination no depot owns, returns routeMatched=false and the
		// default SelectClosestShip path runs BYTE-IDENTICALLY — the natural off-switch
		// (no config flag; empty registry == today's behavior).
		route, routeMatched := routeContractViaDepot(
			appContract.ResolveDepotRegistry(ctx, logger, h.depotRegistryProvider, cmd.PlayerID.Value()),
			contract,
			// sp-9j9c: rank the depot's delivery hulls by the SAME in-system distance the default
			// SelectClosestShip path uses, so a MULTI-hub delivery fleet routes each contract to its
			// cluster's nearest hull. A single-hull depot never invokes this (byte-identical).
			newDepotDeliveryDistance(ctx, h.graphProvider, cmd.PlayerID.Value()),
		)

		// sp-obtr: the depot delivery hull is destination-pinned, so it is the right hull ONLY
		// when the good is already BUFFERED at the hub (SourceInventory — deliver from stock,
		// ~0 source travel). When the good is UNBUFFERED (SourceMarket — must be bought at a
		// remote source market), routing to the destination-pinned depot hull makes it fly empty
		// to the far source then back (~2x) and leaves its hub uncovered; resolveContractHullRoute
		// then declines the depot hull so the coordinator falls through to source-nearest idle-hull
		// selection below (SelectClosestShip toward purchaseMarket — the source market — over the
		// idle pool, which already excludes the depot hull per sp-3l64).
		var selectedShip string
		var distance float64
		if hullRoute := resolveContractHullRoute(route, routeMatched, plan); hullRoute.UseDepotHull {
			selectedShip = hullRoute.DepotHull
			logger.Log("INFO", fmt.Sprintf(
				"Contract %s destination owned by depot %s - good BUFFERED at hub, routing to co-located delivery hull %s (withdraw-local+deliver-local via warehouse %s)",
				contract.ContractID(), route.DepotID, route.DeliveryHull, route.Warehouse),
				map[string]interface{}{
					"action":        "depot_route_contract",
					"contract_id":   contract.ContractID(),
					"depot_id":      route.DepotID,
					"delivery_hull": route.DeliveryHull,
					"warehouse":     route.Warehouse,
					"source":        purchaseMarket,
					"buffered":      true,
				})
		} else {
			// sp-obtr: a depot owns the destination but the good is UNBUFFERED — DECOUPLE
			// sourcing from delivery. Do NOT pull the destination-pinned depot hull off its hub
			// to fly ~2x; source with the idle hull nearest the SOURCE market instead (the default
			// path below). Logged so the divergence from the depot hull is auditable.
			if routeMatched {
				logger.Log("INFO", fmt.Sprintf(
					"Contract %s destination owned by depot %s but good %s is UNBUFFERED (source %s) - decoupling sourcing from delivery: selecting the idle hull nearest the source market instead of the destination-pinned depot hull %s (sp-obtr)",
					contract.ContractID(), route.DepotID, requiredCargo, purchaseMarket, route.DeliveryHull),
					map[string]interface{}{
						"action":        "depot_route_unbuffered_source",
						"contract_id":   contract.ContractID(),
						"depot_id":      route.DepotID,
						"delivery_hull": route.DeliveryHull,
						"source":        purchaseMarket,
						"trade_symbol":  requiredCargo,
						"buffered":      false,
					})
			}
			// DEFAULT PATH (byte-identical to pre-sp-u9xa): select closest ship to
			// purchase market (prioritizes ships with required cargo).
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
		}

		logger.Log("INFO", fmt.Sprintf("Selected %s (distance: %.2f units)", selectedShip, distance), nil)

		// If selected ship is different from previous ship, reposition the
		// previous ship: a dedicated ship (sp-snmb) homes to a balanced
		// operator-configured standby station (fewest fleet peers, distance
		// tie-break - l7h2 Phase 3) instead of the normal market-balancing
		// treatment, since it's exclusively reserved for this coordinator and
		// has no reason to loiter at a general market.
		if previousShipSymbol != "" && previousShipSymbol != selectedShip {
			// LIVE dedicated-fleet membership (sp-cmwc), not the frozen launch list:
			// a hull `fleet add`ed after launch homes between legs like any dedicated
			// member, and this same list is the standby-station occupancy peer set.
			dedicatedMembers := resolveDedicatedMembersForHoming(ctx, logger, h.shipRepo, cmd.PlayerID, dedicatedFleetContract, cmd.DedicatedShips)
			if isDedicatedShip(previousShipSymbol, dedicatedMembers) {
				logger.Log("INFO", fmt.Sprintf("Selected ship changed from %s to %s - homing dedicated ship %s to standby station", previousShipSymbol, selectedShip, previousShipSymbol), nil)

				// LIVE standby-station set (sp-jcke), not the frozen launch snapshot: a
				// hub `fleet hub add`ed after launch draws idle hulls toward it and a
				// removed one re-homes its hulls to the remaining set, all with no
				// restart — the operation-level mirror of the live dedicated-fleet read
				// above. Resolved per repositioning; falls back to cmd.StandbyStations
				// on a read failure or when no provider is wired.
				liveStandby := appContract.ResolveStandbyStations(ctx, logger, h.standbyProvider, cmd.ContainerID, cmd.PlayerID.Value(), cmd.StandbyStations)

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
				}(previousShipSymbol, cmd.PlayerID, liveStandby, dedicatedMembers)
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

		// sp-sqq5 last-resort verdict for the claim-side guard: the command
		// frigate may be drafted for this leg only when NO regular hauler was an
		// idle candidate this pass. Discovery (FindIdleLightHaulers) already holds
		// an undedicated/retired frigate out of the pool while any regular hauler
		// is idle, so in normal operation this is true exactly when the frigate is
		// the sole candidate; if a future change ever re-admits it alongside a
		// hauler, the verdict is false and spawnContractWorker refuses the draft
		// rather than re-sweeping the retired frigate onto contracts (RULINGS #7).
		commandDraftAllowed := !hasRegularHaulerCandidate(generalShipEntities)

		workerContainerID, err := h.spawnContractWorker(ctx, cmd, selectedShip, commandDraftAllowed)
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
		// Timestamp the spawn so an instant death of this worker is measurable
		// against the spawn-governor's instant-death threshold (sp-lybx).
		gov.NoteSpawn(selectedShip)

		// Block waiting for worker completion
		logger.Log("INFO", fmt.Sprintf("Waiting for %s to complete contract...", selectedShip), nil)
		select {
		case event := <-workerCompletedCh:
			// Feed the completion to the spawn governor FIRST: a worker that just
			// died instantly extends this hull's backoff, and the Nth instant
			// death within the window crosses it into quarantine. On that exact
			// crossing, emit the ONE loud line + captain event; thereafter the
			// hull is skipped for the rest of the run and the next pass selects a
			// healthy hull (the CONTRACT keeps being worked — RULINGS #1).
			if outcome := gov.NoteCompletion(event.ShipSymbol, event.Success); outcome.JustQuarantined {
				logger.Log("ERROR", hullQuarantineMessage(event.ShipSymbol, outcome.InstantDeaths), map[string]interface{}{
					"action":         "hull_quarantined",
					"ship_symbol":    event.ShipSymbol,
					"instant_deaths": outcome.InstantDeaths,
				})
				h.recordHullQuarantineEvent(ctx, cmd, event.ShipSymbol, outcome.InstantDeaths)
			}
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

// ErrCommandFrigateNotLastResort is returned by spawnContractWorker when it
// refuses to draft an UNDEDICATED command frigate for a contract haul because a
// regular hauler is available (RULINGS #7: the command frigate hauls only as a
// last resort, sp-sqq5). A sentinel so callers/tests can distinguish this
// deliberate policy refusal from a transient spawn failure via errors.Is.
var ErrCommandFrigateNotLastResort = errors.New("command frigate not drafted for contract haul: regular haulers available (last-resort only)")

// hasRegularHaulerCandidate reports whether any candidate is a non-command hull
// (a regular hauler). The main loop uses it to compute the command frigate's
// last-resort verdict for the claim-side guard (sp-sqq5): a regular hauler among
// the discovered candidates means the frigate is NOT the last resort.
func hasRegularHaulerCandidate(candidates []*navigation.Ship) bool {
	for _, ship := range candidates {
		if !domainContract.IsCommandHull(ship) {
			return true
		}
	}
	return false
}

// DedicatedFleetSeedMarker durably records that this coordinator has applied its
// --dedicated-ships launch seed ONCE (sp-86vb), so a later daemon-restart rebuild
// reads the marker back and does NOT replay the stale launch seed over live fleet
// state — a hull deliberately `fleet remove`d stays removed across the restart
// (RULINGS #2). The daemon backs it with the coordinator's OWN container config
// (the same map the recovery rebuild reads its command from), mirroring the arb
// cost persister (sp-dkj7). Reporting/gating only — no ship state is written here.
type DedicatedFleetSeedMarker interface {
	// MarkDedicatedShipsSeeded records that containerID's --dedicated-ships seed
	// has been applied, so a later restart rebuild reads DedicatedShipsSeeded=true.
	// A returned error is advisory: the seed has already been applied, so the caller
	// logs and continues (a persistence failure degrades restart-resilience of the
	// removal, it never fails the coordinator).
	MarkDedicatedShipsSeeded(ctx context.Context, containerID string, playerID int) error
}

// seedDedicatedFleetIfFirstBoot applies the --dedicated-ships launch seed EXACTLY
// once per coordinator lifetime (sp-86vb). On genuine first boot (seeded=false) it
// reconciles the seed into the dedication tag and then persists a durable "seeded"
// marker into the coordinator's own container config; on every subsequent daemon
// restart (seeded=true, reloaded from that marker) it does NOTHING, leaving the
// live dedicated_fleet tag authoritative.
//
// This is the fix for the restart-resurrection defect: fleet add/remove mutate the
// live tag atomically, but the launch seed is a frozen snapshot the coordinator
// used to replay ADDITIVELY on every boot. A hull removed via `fleet remove` (tag
// cleared) that was in the original seed got its "contract" tag re-stamped on the
// next restart — resurrecting a deliberate removal. Gating the replay on a
// persisted first-boot marker stops that while still seeding a genuine first boot.
//
// An empty seed still touches nothing (mediator lookup included). A nil marker
// leaves the seed un-persisted and warns: the seed still applies, but a restart
// would re-seed exactly as before this fix (fail-open; production always wires it).
func seedDedicatedFleetIfFirstBoot(
	ctx context.Context,
	logger common.ContainerLogger,
	med common.Mediator,
	marker DedicatedFleetSeedMarker,
	playerID shared.PlayerID,
	containerID string,
	dedicatedShips []string,
	seeded bool,
	fleetName string,
	assigner string,
) {
	if len(dedicatedShips) == 0 {
		return
	}
	// Already seeded on a previous boot — the live dedicated_fleet tag is now
	// authoritative. Do NOT replay the stale launch snapshot, or a hull removed
	// via `fleet remove` (tag cleared) that is still listed in the seed would be
	// re-stamped "contract", resurrecting a deliberate removal (sp-86vb).
	if seeded {
		return
	}

	reconcileDedicatedFleet(ctx, logger, med, playerID, dedicatedShips, fleetName, assigner)

	// Persist the first-boot marker so a later restart reloads seeded=true and skips
	// the replay above. Fail-open: the seed has already been applied, so a marker
	// failure is a WARNING, never a coordinator abort (RULINGS #1 never-skip).
	if marker == nil {
		logger.Log("WARNING", fmt.Sprintf(
			"dedicated fleet seed applied for %d ship(s) but no seed marker is wired - a daemon restart may replay the launch seed over live fleet state (sp-86vb)",
			len(dedicatedShips)), nil)
		return
	}
	if err := marker.MarkDedicatedShipsSeeded(ctx, containerID, playerID.Value()); err != nil {
		logger.Log("WARNING", fmt.Sprintf(
			"dedicated fleet seed applied but failed to persist the seeded marker (a restart may replay the launch seed): %v", err), nil)
	}
}

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
	assigner string,
) {
	for _, symbol := range dedicatedShips {
		pid := playerID.Value()
		// Automated path (Manual: false): the assign handler BLOCKS a 0-cargo
		// hull from being pinned into a hauling fleet (sp-r6f1). This is what
		// stopped the reconcile from silently re-pinning the 0-cargo satellites
		// TORWIND-24/25 into "contract" on every restart. A blocked symbol
		// surfaces as the WARNING below and is skipped, exactly like any other
		// per-ship failure — the rest of the list still reconciles.
		_, err := med.Send(ctx, &shipAssignment.AssignShipFleetCommand{
			ShipSymbol: symbol,
			Fleet:      fleetName,
			PlayerID:   &pid,
			Assigner:   assigner,
			Manual:     false,
		})
		if err != nil {
			logger.Log("WARNING", fmt.Sprintf("dedicated fleet reconciliation: failed to assign ship %s: %v", symbol, err), nil)
			continue
		}
		logger.Log("INFO", fmt.Sprintf("Ship %s reconciled into dedicated %s fleet", symbol, fleetName), nil)
	}
}

// mediatorShipHomer implements appContract.ShipHomer (sp-8bpr) by dispatching
// the EXISTING balanced-standby HomeShipCommand (l7h2 Phase 3) through the
// mediator — the idle-arb dispatcher's post-leg re-home reuses the coordinator's
// own homing machinery verbatim, with the same standby-station set and
// fleet-peer list the contract-handoff homing uses (RULINGS #7: no parallel
// homing algorithm).
//
// Both membership inputs are LIVE, not frozen launch snapshots. The standby set
// is passed in per re-home (sp-jcke) — the dispatcher resolves the CURRENT hub
// set from the coordinator's container config each pass, so a `fleet hub
// add|remove` re-homes across the new set with no restart. The fleet-peer list is
// resolved LIVE per re-home from the dedicated_fleet tag (sp-cmwc), so a hull
// `fleet add`ed after launch is counted in standby-station occupancy and a `fleet
// remove`d one is not — both matching the contract-handoff homing gate.
//
// Navigation runs FIRE-AND-FORGET, mirroring the coordinator's own
// `go func(){ Send(homeCmd) }` at the contract-handoff hook: HomeShipCommand
// blocks until the hull arrives (navigate_route executes the whole route), so a
// synchronous call would stall the dispatcher tick for the full flight. HomeShip
// returns as soon as the home is DISPATCHED; the detached goroutine carries the
// container logger on a background context that outlives the request ctx,
// exactly as the coordinator's homing goroutine does, and logs a homing failure
// at WARNING rather than surfacing it (re-homing is best-effort).
type mediatorShipHomer struct {
	mediator common.Mediator
	shipRepo navigation.ShipRepository
	playerID shared.PlayerID
	fleet    string
}

var _ appContract.ShipHomer = (*mediatorShipHomer)(nil)

// HomeShip re-homes the hull to the LIVE standby set the dispatcher resolved this
// pass (sp-jcke), passed in rather than frozen on the homer, so an idle-arb
// re-home tracks a `fleet hub add|remove` with no restart — the same live set the
// coordinator's between-legs homing uses.
func (m *mediatorShipHomer) HomeShip(ctx context.Context, shipSymbol string, standbyStations []string) error {
	logger := common.LoggerFromContext(ctx)
	homeCmd := &HomeShipCommand{
		ShipSymbol:      shipSymbol,
		PlayerID:        m.playerID,
		StandbyStations: standbyStations,
		FleetShips:      resolveDedicatedMembersForHoming(ctx, logger, m.shipRepo, m.playerID, m.fleet, nil),
	}
	go func() {
		// Background context (the dispatch ctx may be cancelled when the
		// coordinator stops) carrying the container logger, mirroring the
		// contract-handoff homing goroutine.
		homeCtx := common.WithLogger(context.Background(), logger)
		if _, err := m.mediator.Send(homeCtx, homeCmd); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Idle-arb re-home: homing %s failed: %v", shipSymbol, err), nil)
		}
	}()
	return nil
}

// isDedicatedShip reports whether shipSymbol is present in the given
// dedicated-membership list. Used at the "previous ship" hook to decide whether
// an idle ship homes to a standby station instead of being balanced to a market.
// The list is the LIVE dedicated-fleet membership (sp-cmwc), not the immutable
// --dedicated-ships launch snapshot — see resolveDedicatedMembersForHoming.
func isDedicatedShip(shipSymbol string, dedicatedShips []string) bool {
	for _, symbol := range dedicatedShips {
		if symbol == shipSymbol {
			return true
		}
	}
	return false
}

// resolveDedicatedMembersForHoming returns the LIVE dedicated-fleet membership the
// between-legs homing gate keys off (sp-cmwc). Before this, the gate read the frozen
// --dedicated-ships launch list, so a hull added via `fleet add --operation contract`
// after launch (tag set, absent from that list) failed the gate and was market-balanced
// like a general-pool hull instead of homed to a standby station between legs — while a
// `fleet remove`d hull still counted. Reading the live dedicated_fleet tag makes the
// gate and the standby-occupancy peer list track actual membership, matching the live
// authority FindIdleShipsByFleet / FleetHasMembers already give the selection side.
//
// On a membership read error it falls back to launchList (the pre-fix source), so a
// transient repo failure is never WORSE than the old behavior — it just forgoes the
// live view for that one repositioning.
func resolveDedicatedMembersForHoming(
	ctx context.Context,
	logger common.ContainerLogger,
	shipRepo navigation.ShipRepository,
	playerID shared.PlayerID,
	fleet string,
	launchList []string,
) []string {
	members, err := appContract.FindFleetMemberSymbols(ctx, playerID, shipRepo, fleet)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf(
			"failed to read live %s-fleet membership for homing (falling back to launch --dedicated-ships list): %v", fleet, err), nil)
		return launchList
	}
	return members
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
	// Under CAS-retry (sp-wa7c): re-apply ForceRelease on the FRESH row so a
	// concurrent writer's cargo/nav update on the same hull survives instead of
	// being last-write-wins clobbered, and skip unless the hull is still on its
	// dead worker (already released / re-claimed elsewhere -> changed=false).
	deadWorkerContainer := ship.ContainerID()
	if _, _, err := h.shipRepo.SaveWithRetry(ctx, shipSymbol, cmd.PlayerID,
		func(sh *navigation.Ship) (bool, error) {
			if !sh.IsAssigned() || sh.ContainerID() != deadWorkerContainer {
				return false, nil
			}
			sh.ForceRelease("worker_readopt", h.clock)
			return true, nil
		}); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed to release ship %s for re-adoption (falling back to reclaim/discovery): %v", shipSymbol, err), nil)
		return ""
	}

	// A resume is never a fresh last-resort decision: the frigate (if this hull
	// is the command frigate) was already mid-contract, so re-orphaning it is the
	// strand sp-sqq5 fixes. Always authorize the command draft here.
	workerContainerID, err := h.spawnContractWorker(ctx, cmd, shipSymbol, true)
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

// spawnContractWorker persists, claims, and starts a contract-workflow worker on
// selectedShip. commandDraftAllowed authorizes drafting the command frigate for
// this leg (sp-sqq5): a FRESH draft from the main loop passes the last-resort
// verdict (true only when no regular hauler is an idle candidate), while a
// RESUME of an interrupted delivery (readoptInterruptedDeliveries) always passes
// true — re-orphaning a mid-delivery frigate is the exact strand this bead
// closes. The value governs only an UNDEDICATED command frigate; every regular
// hull and every contract-dedicated command hull is unaffected.
func (h *RunFleetCoordinatorHandler) spawnContractWorker(
	ctx context.Context,
	cmd *RunFleetCoordinatorCommand,
	selectedShip string,
	commandDraftAllowed bool,
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

	// sp-sqq5 (claim-side last-resort backstop, RULINGS #7): refuse to draft an
	// UNDEDICATED command frigate for a contract haul unless it is a genuine last
	// resort. Discovery (FindIdleLightHaulers) already holds a retired/`fleet
	// unassign`'d frigate out of the pool while any regular hauler is idle; this
	// is the single-writer backstop at the claim itself, so even a discovery
	// regression cannot silently re-sweep the frigate onto contracts (the sp-sqq5
	// defect: a re-claim that stranded a mid-delivery contract). A
	// contract-DEDICATED command hull (tag "contract") is a legitimate fleet
	// member and passes untouched; a resume (readopt) passes commandDraftAllowed
	// =true so a mid-delivery frigate is never re-orphaned. Rolled back exactly
	// like a rejected claim — no ship write, no worker started.
	if !commandDraftAllowed && ship.DedicatedFleet() == "" && domainContract.IsCommandHull(ship) {
		logger.Log("INFO", fmt.Sprintf(
			"Refusing to draft undedicated command frigate %s for a contract haul while a regular hauler is available — command frigate hauls only as last resort (RULINGS #7)", selectedShip),
			map[string]interface{}{
				"action":      "skipped:command_frigate_not_last_resort",
				"ship_symbol": selectedShip,
			})
		_ = h.workerLifecycleManager.StopWorkerContainer(ctx, workerContainerID)
		return "", fmt.Errorf("refusing to draft undedicated command frigate %s for a contract haul: %w", selectedShip, ErrCommandFrigateNotLastResort)
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
	//
	// sp-3l64: a DEPOT DELIVERY hull carries the distinct depot.DeliveryHullFleet
	// dedication (so discovery can never re-grab it) and reaches this claim ONLY via
	// routeContractViaDepot or a mid-delivery readopt. contractClaimFleet keys the
	// claim on the hull's own dedication so that depot-routed dispatch passes the
	// dedication guard, while every other hull still claims under "contract".
	if err := h.shipRepo.ClaimShip(ctx, selectedShip, workerContainerID, cmd.PlayerID, contractClaimFleet(ship.DedicatedFleet())); err != nil {
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
		// Release the just-claimed hull under CAS-retry (sp-wa7c): re-apply
		// ForceRelease on the FRESH row so a concurrent writer's cargo/nav update
		// survives instead of being last-write-wins clobbered, and skip unless the
		// hull is still this worker's claim (RULINGS #7 — never release out from
		// under a new owner).
		_, _, _ = h.shipRepo.SaveWithRetry(ctx, selectedShip, cmd.PlayerID,
			func(sh *navigation.Ship) (bool, error) {
				if !sh.IsAssigned() || sh.ContainerID() != workerContainerID {
					return false, nil
				}
				sh.ForceRelease("worker_start_failed", h.clock)
				return true, nil
			})
		_ = h.workerLifecycleManager.StopWorkerContainer(ctx, workerContainerID)
		return "", fmt.Errorf("Failed to start worker container: %v", err)
	}

	return workerContainerID, nil
}

// liquidationDispatchCooldown is how long a hull stays off the auto-liquidation
// re-dispatch list after one attempt (sp-39oi). It bounds a spawn-storm on a genuinely
// stuck hull — no in-system market bids its cargo and jettison is off, so each pass
// would otherwise re-park then re-dispatch it. A sellable hull clears on the first
// attempt and never comes back to the parked list, so the cooldown only governs the
// unsellable-hold tail; the hull is retried after it, since a market may appear
// (scouts scan) — a deferral, never a permanent skip (RULINGS #1).
const liquidationDispatchCooldown = 5 * time.Minute

// dispatchLiquidationForParked self-clears the hulls FilterUnrelatedCargo parked for
// holding cargo unrelated to the active contract (sp-39oi): each gets a one-shot
// cargo_liquidation worker that sells the strand at the best in-system bid (jettison
// only as a last resort below the configured floor), so the hull re-enters candidacy
// on a later pass instead of sitting filtered out of the pool forever — the fleet-scale
// jam this closes. It is a STANDING mechanism (runs every discovery pass), gated by the
// AutoLiquidationDisabled escape hatch and a per-hull cooldown so an unsellable hold
// never storms. Best-effort: a spawn failure is logged and the hull is put on cooldown
// so a persistent failure cannot spin; contract work on the spawnable hulls is never
// blocked by it.
func (h *RunFleetCoordinatorHandler) dispatchLiquidationForParked(
	ctx context.Context,
	cmd *RunFleetCoordinatorCommand,
	parkedShips []string,
	cooldown map[string]time.Time,
) {
	if cmd.AutoLiquidationDisabled || len(parkedShips) == 0 {
		return
	}
	logger := common.LoggerFromContext(ctx)
	now := h.clock.Now()
	for _, shipSymbol := range parkedShips {
		if until, ok := cooldown[shipSymbol]; ok && now.Before(until) {
			continue // recently dispatched — don't re-storm a stuck hull
		}
		cooldown[shipSymbol] = now.Add(liquidationDispatchCooldown)
		workerID, err := h.spawnLiquidationWorker(ctx, cmd, shipSymbol)
		if err != nil {
			logger.Log("WARNING", fmt.Sprintf("Auto-liquidation dispatch for parked hull %s failed: %v - will retry after cooldown", shipSymbol, err), map[string]interface{}{
				"action":      "liquidation_dispatch_failed",
				"ship_symbol": shipSymbol,
			})
			continue
		}
		logger.Log("INFO", fmt.Sprintf("Auto-liquidation dispatched for parked hull %s (worker %s) - self-clearing stranded cargo so it re-enters candidacy", shipSymbol, workerID), map[string]interface{}{
			"action":       "liquidation_dispatched",
			"ship_symbol":  shipSymbol,
			"worker_id":    workerID,
			"min_jettison": cmd.LiquidationMinJettisonValue,
		})
	}
}

// spawnLiquidationWorker persists, claims, and starts a one-shot cargo_liquidation
// worker on a parked hull (sp-39oi), mirroring spawnContractWorker's atomic-claim +
// rollback lifecycle. The claim goes through ClaimShip under the contract fleet
// identity (operation "contract"), so an unpinned or contract-pinned hull claims
// cleanly while a hull pinned to another fleet is rejected at the DB rather than
// poached — the liquidation only ever clears hulls the contract coordinator legitimately
// draws from. On a start failure the assignment is released so the hull returns to the
// pool.
func (h *RunFleetCoordinatorHandler) spawnLiquidationWorker(
	ctx context.Context,
	cmd *RunFleetCoordinatorCommand,
	shipSymbol string,
) (string, error) {
	logger := common.LoggerFromContext(ctx)

	workerContainerID := utils.GenerateContainerID("cargo-liquidation", shipSymbol)
	workerCmd := &liquidation.LiquidateCargoCommand{
		PlayerID:         cmd.PlayerID,
		ShipSymbol:       shipSymbol,
		MinJettisonValue: cmd.LiquidationMinJettisonValue,
		CoordinatorID:    cmd.ContainerID,
	}

	if err := h.daemonClient.PersistContainer(ctx, daemon.ContainerKindCargoLiquidation, workerContainerID, uint(cmd.PlayerID.Value()), workerCmd); err != nil {
		return "", fmt.Errorf("failed to persist liquidation worker: %w", err)
	}

	ship, err := h.shipRepo.FindBySymbol(ctx, shipSymbol, cmd.PlayerID)
	if err != nil {
		_ = h.workerLifecycleManager.StopWorkerContainer(ctx, workerContainerID)
		return "", fmt.Errorf("failed to load ship %s: %w", shipSymbol, err)
	}

	// Atomic operation-checked claim (sp-lprs), same identity as spawnContractWorker:
	// a foreign-pinned hull is rejected at the DB, not clobbered.
	if err := h.shipRepo.ClaimShip(ctx, shipSymbol, workerContainerID, cmd.PlayerID, dedicatedFleetContract); err != nil {
		_ = h.workerLifecycleManager.StopWorkerContainer(ctx, workerContainerID)
		return "", fmt.Errorf("failed to claim ship %s: %w", shipSymbol, err)
	}

	// Mirror the committed claim into the in-memory entity so the start-failure rollback
	// below sees the assignment; a sync failure is a WARN, not an unclaim (the DB claim
	// stands).
	if err := ship.AssignToContainer(workerContainerID, h.clock); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Ship %s claimed in DB but in-memory assign failed (claim stands): %v", shipSymbol, err), nil)
	}

	if err := h.daemonClient.StartContainer(ctx, daemon.ContainerKindCargoLiquidation, workerContainerID); err != nil {
		// Release the just-claimed hull under CAS-retry (sp-wa7c): re-apply
		// ForceRelease on the FRESH row so a concurrent writer's cargo/nav update
		// survives instead of being last-write-wins clobbered, and skip unless the
		// hull is still this worker's claim (RULINGS #7).
		_, _, _ = h.shipRepo.SaveWithRetry(ctx, shipSymbol, cmd.PlayerID,
			func(sh *navigation.Ship) (bool, error) {
				if !sh.IsAssigned() || sh.ContainerID() != workerContainerID {
					return false, nil
				}
				sh.ForceRelease("liquidation_start_failed", h.clock)
				return true, nil
			})
		_ = h.workerLifecycleManager.StopWorkerContainer(ctx, workerContainerID)
		return "", fmt.Errorf("failed to start liquidation worker: %w", err)
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
	event := health.NewErrorLoopEvent(cmd.ContainerID, cmd.PlayerID.Value(), checkpoint, cause, streak)
	recordCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.captainEvents.Record(recordCtx, event); err != nil {
		logger.Log("WARNING", fmt.Sprintf("captain outbox: failed to record %s for checkpoint %s: %v", captain.EventCoordinatorErrorLoop, checkpoint, err), nil)
	}
}

// recordHullQuarantineEvent emits the single captain outbox event for a hull
// crossing into spawn quarantine (sp-lybx). Fire-and-forget with its own short
// timeout, mirroring recordErrorLoopEvent exactly: an outbox failure must never
// break the coordinator's loop, so it is logged at WARNING and swallowed, and a
// nil captainEvents (not wired — tests, or a daemon boot before DI completes)
// silently disables recording rather than panicking.
func (h *RunFleetCoordinatorHandler) recordHullQuarantineEvent(ctx context.Context, cmd *RunFleetCoordinatorCommand, hull string, instantDeaths int) {
	if h.captainEvents == nil {
		return
	}
	logger := common.LoggerFromContext(ctx)
	event := buildHullQuarantineEvent(cmd.ContainerID, cmd.PlayerID.Value(), hull, instantDeaths)
	recordCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.captainEvents.Record(recordCtx, event); err != nil {
		logger.Log("WARNING", fmt.Sprintf("captain outbox: failed to record hull quarantine for %s: %v", hull, err), nil)
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

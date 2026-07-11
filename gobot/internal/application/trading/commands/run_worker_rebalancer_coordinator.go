package commands

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	appContract "github.com/andrescamacho/spacetraders-go/internal/application/contract"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

const (
	// defaultRebalancerTickSeconds is the reconcile cadence when the launch config leaves
	// it unset (RULINGS #5: parametrized, not hardcoded at the call site). One tick is at
	// most one minute of a vacancy sitting unaddressed before the next ferry pass.
	defaultRebalancerTickSeconds = 60

	// defaultVacancyMinMinutes is how long the OLDEST factory container in a system must
	// have been RUNNING before that system counts as a hub-vacancy (RULINGS #5). This is
	// the restart-safe clock: it is derived from the container row's persisted StartedAt,
	// so a fresh handler makes the same call and a just-launched / just-restarted factory
	// mid-first-cycle is exempt (RULINGS #2) — never ferried to on a transient no-worker
	// blip.
	defaultVacancyMinMinutes = 15

	// defaultSourceMinIdle is the minimum idle undedicated light-haulers a system must
	// hold to donate one to a vacancy (RULINGS #5). At >= 2, ferrying exactly one per
	// vacancy per tick (and re-deriving each tick) never strips a source below one idle.
	defaultSourceMinIdle = 2

	// defaultFerryCooldownSeconds suppresses a NEW ferry to a system within this window of
	// the most-recent ferry that targeted it (RULINGS #5). DB-derived from the ferry
	// container rows' StartedAt, so it survives a restart with zero new state (RULINGS #2)
	// — the anti-thrash guard that stops a still-flying (or just-completed) ferry from
	// being piled onto while the vacancy naturally resolves.
	defaultFerryCooldownSeconds = 600

	// defaultMaxConcurrentFerries caps simultaneously-running ferries (RULINGS #5),
	// bounding concurrent cross-system capital movement / API load. Counted from the
	// RUNNING worker_ferry container rows, so a fresh handler counts identically.
	defaultMaxConcurrentFerries = 2

	// defaultMaxLightsPerSystem is the per-system light-hauler cap (0 = uncapped, the
	// default). When positive, no ferry is dispatched to a system whose in-system lights
	// plus in-flight inbound ferries already meet the cap.
	defaultMaxLightsPerSystem = 0

	// sourceKeepMin is the hard floor of idle lights a source must retain: ferrying is
	// only ever taken from a source that keeps at least one idle hull behind, regardless
	// of how low source_min_idle is tuned (defends the never-strip-below-1 invariant).
	sourceKeepMin = 1

	// roleHauler is the registration role a light-hauler carries — the same predicate
	// FindIdleLightHaulers keys candidacy on. Named locally so the anti-hub supply count
	// classifies exactly the same hull class without reaching into the contract package.
	roleHauler = "HAULER"

	// marketplaceTrait selects the destination waypoint the ferry lands the hull on — a
	// marketplace in the vacancy system (any one serves; the factory mans it in-system).
	marketplaceTrait = "MARKETPLACE"

	// workerFerryContainerPrefix is the GenerateContainerID("worker_ferry", …) prefix. A
	// hull whose active claim's container ID carries this prefix is ferry-claimed — the
	// key to the restart-safe reclaim (below): the coordinator identifies a ferry-owned
	// hull from SHIP STATE ALONE, so an interrupted ferry of ANY age (older than any query
	// window) is still reclaimed with zero persisted coordinator state (RULINGS #2).
	workerFerryContainerPrefix = "worker_ferry-"

	// workerFerryOperation is the ClaimShip operation/fleet name a ferried hull is claimed
	// under (RULINGS #7 poach guard). The claim is OCCUPANCY, not a dedication — the hull
	// is never AssignFleet'd, so on release (arrival or interruption) it returns to the
	// general pool for the destination factory to man.
	workerFerryOperation = "worker_ferry"
)

// factoryCommandTypes are the CommandTypes whose RUNNING containers signal active factory
// work in a system (sp-f5pr). Both persist system_symbol in their container config; the
// container-query adapter parses it. A system with any such container is a factory system.
var factoryCommandTypes = map[string]bool{
	"goods_factory_coordinator": true,
	"manufacturing_coordinator": true,
}

// ActiveFactoryContainer is the container-query DTO for one RUNNING factory container
// (sp-f5pr): the system it runs in and when it started. StartedAt is the restart-safe
// vacancy clock (the persisted container row survives a daemon restart). Kept as a small
// DTO so the GORM model never crosses into the application layer.
type ActiveFactoryContainer struct {
	SystemSymbol string
	StartedAt    time.Time
}

// FerryContainer is the container-query DTO for one worker_ferry container (sp-f5pr):
// enough to derive the concurrency cap (RUNNING count), the per-vacancy cooldown
// (StartedAt vs the destination system), and the in-flight inbound count for
// max_lights_per_system. Every operational clock/cap is derived from these rows, so a
// fresh handler reads them identically (RULINGS #2 — zero new persisted state).
type FerryContainer struct {
	ID                  string
	DestinationWaypoint string
	Status              string
	StartedAt           time.Time
}

// RebalancerContainerQuery reads the DB-derived inputs the coordinator's vacancy
// detection and ferry caps depend on (sp-f5pr). Both methods fail the tick closed on
// error (a guard that cannot read state never ferries). Satisfied by a thin grpc adapter
// over the GORM container repository.
type RebalancerContainerQuery interface {
	// ActiveFactoryContainers returns every RUNNING factory container for the player
	// (goods_factory_coordinator / manufacturing_coordinator), with its system and
	// StartedAt.
	ActiveFactoryContainers(ctx context.Context, playerID int) ([]ActiveFactoryContainer, error)
	// RecentFerries returns the player's worker_ferry containers that are either RUNNING
	// (regardless of age — so the concurrency cap and the reclaim RUNNING-set are exact)
	// or started at/after `since` (so the per-vacancy cooldown sees recently-completed
	// ferries). The coordinator filters within this set; the adapter guarantees the
	// RUNNING-any-age inclusion.
	RecentFerries(ctx context.Context, playerID int, since time.Time) ([]FerryContainer, error)
}

// MarketWaypointProvider lists the marketplace waypoints in a system (sp-f5pr) — the
// coordinator picks one (smallest symbol, deterministic) as the ferry's destination.
// Satisfied by the GORM waypoint repository (ListBySystemWithTrait).
type MarketWaypointProvider interface {
	ListBySystemWithTrait(ctx context.Context, systemSymbol, trait string) ([]*shared.Waypoint, error)
}

// RunWorkerRebalancerCoordinatorCommand launches the standing worker-rebalancer
// coordinator for a player (sp-f5pr). Like the trade-fleet / scout-post coordinators it
// runs an infinite reconcile loop inside a single Handle() call; the container wraps that
// one loop (created with iterations=-1, so it is NOT a CoordinatorOwnsIterations type).
type RunWorkerRebalancerCoordinatorCommand struct {
	PlayerID    shared.PlayerID
	ContainerID string
	// AgentSymbol is threaded through for parity with the sibling coordinators' launch
	// identity (the ferry worker itself needs no agent — movement only).
	AgentSymbol string

	// Enabled is the captain's config off-switch (RULINGS #5). When false the reconcile
	// pass is inert — the container stays resident, so flipping worker_rebalancer.enabled
	// back on in config.yaml and restarting the daemon re-arms it with no manual relaunch.
	// The default (true) is applied at config-resolution time.
	Enabled bool

	// DryRun decides + logs the ferry it WOULD dispatch but persists/claims/starts nothing
	// (set once at launch, like the frontier coordinator's — NOT a config.yaml knob).
	DryRun bool

	// Operational knobs; <= 0 uses the coordinator's own documented default (RULINGS #5).
	TickIntervalSecs     int
	VacancyMinMinutes    int
	SourceMinIdle        int
	FerryCooldownSecs    int
	MaxConcurrentFerries int
	MaxLightsPerSystem   int
}

// RunWorkerRebalancerCoordinatorResponse reports reconcile progress. Because the loop is
// infinite it is only observed on context cancellation (shutdown).
type RunWorkerRebalancerCoordinatorResponse struct {
	Ticks   int
	Ferried int
	Errors  []string
}

// RunWorkerRebalancerCoordinatorHandler ferries idle undedicated light-haulers
// cross-system to worker-starved factory systems (sp-f5pr). Every reconcile pass derives
// its ENTIRE state from the DB — ship rows and container rows — so it holds no in-memory
// maps and a fresh handler (post-restart) makes byte-identical suppress/allow/ferry
// decisions (RULINGS #2). It claims a hull only through the atomic, poach-guarded
// ClaimShip (RULINGS #7), reuses the trade-route coordinator's multi-jump travel() for
// movement (via the spawned ferry worker, RULINGS: reuse), and fails every guard closed
// (any unreadable state ⇒ no ferry this tick).
type RunWorkerRebalancerCoordinatorHandler struct {
	shipRepo       navigation.ShipRepository
	daemonClient   daemon.DaemonClient
	containerQuery RebalancerContainerQuery
	marketProvider MarketWaypointProvider
	clock          shared.Clock

	// gateGraph resolves jump-hop distances for nearest-source selection (sp-f5pr). nil
	// disables ferrying entirely (fail-closed park), so it is wired via SetGateGraph
	// rather than the constructor — mirroring the trade-route/scout coordinators.
	gateGraph GateGraph
}

// NewRunWorkerRebalancerCoordinatorHandler wires the coordinator. clock defaults to the
// real clock when nil (production). The gate-graph resolver is optional and injected
// separately (SetGateGraph); nil leaves ferrying disabled (fail-closed).
func NewRunWorkerRebalancerCoordinatorHandler(
	shipRepo navigation.ShipRepository,
	daemonClient daemon.DaemonClient,
	containerQuery RebalancerContainerQuery,
	marketProvider MarketWaypointProvider,
	clock shared.Clock,
) *RunWorkerRebalancerCoordinatorHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &RunWorkerRebalancerCoordinatorHandler{
		shipRepo:       shipRepo,
		daemonClient:   daemonClient,
		containerQuery: containerQuery,
		marketProvider: marketProvider,
		clock:          clock,
	}
}

// SetGateGraph wires the multi-jump gate-graph resolver (sp-f5pr). The daemon injects the
// same persisted, fetch-through gategraph.Service the trade circuit uses, so the
// nearest-source BFS and the ferry's travel() share one cache/graph. Optional-injection
// like the sibling coordinators; nil leaves ferrying disabled (fail-closed park).
func (h *RunWorkerRebalancerCoordinatorHandler) SetGateGraph(g GateGraph) {
	h.gateGraph = g
}

// Handle runs the reconcile loop until the context is cancelled.
func (h *RunWorkerRebalancerCoordinatorHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	logger := common.LoggerFromContext(ctx)

	cmd, ok := request.(*RunWorkerRebalancerCoordinatorCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	tick := time.Duration(cmd.TickIntervalSecs) * time.Second
	if tick <= 0 {
		tick = defaultRebalancerTickSeconds * time.Second
	}

	result := &RunWorkerRebalancerCoordinatorResponse{Errors: []string{}}
	logger.Log("INFO", fmt.Sprintf("Worker rebalancer coordinator starting (tick %s, vacancy_min %dm, source_min_idle %d, cooldown %ds, max_concurrent %d, max_lights %d, enabled %t, dry_run %t)",
		tick, cmd.vacancyMin(), cmd.sourceMinIdle(), int(cmd.cooldown().Seconds()), cmd.maxConcurrentFerries(), cmd.MaxLightsPerSystem, cmd.Enabled, cmd.DryRun), map[string]interface{}{
		"action":       "worker_rebalancer_coordinator_start",
		"container_id": cmd.ContainerID,
		"enabled":      cmd.Enabled,
		"dry_run":      cmd.DryRun,
	})

	for {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

		ferried, err := h.reconcileOnce(ctx, cmd)
		result.Ferried += ferried
		result.Ticks++
		if err != nil {
			result.Errors = append(result.Errors, err.Error())
			logger.Log("ERROR", fmt.Sprintf("Worker rebalancer reconcile failed: %v", err), nil)
		}

		select {
		case <-time.After(tick):
		case <-ctx.Done():
			return result, ctx.Err()
		}
	}
}

// reconcileOnce is one reconcile pass. It is the unit the tests drive directly (the
// Handle loop just calls it on a timer).
//
// Order (each step fails CLOSED — any unreadable state ⇒ no ferry this tick):
//  1. Load the fleet + the factory/ferry container rows (the DB-derived inputs).
//  2. RECLAIM ended ferries FIRST (arrival AND interruption, one path): a hull still
//     claimed to a worker_ferry container that is not RUNNING is released. On arrival the
//     hull becomes idle in the vacancy system → the destination factory (and this pass's
//     own vacancy detection) sees it, self-healing the vacancy. On interruption the hull
//     is released wherever it sits and re-evaluated — never stranded.
//  3. Detect hub-vacancies (active factory ≥ vacancy_min old, no in-system idle light,
//     demand > supply).
//  4. For each vacancy, honoring the concurrency cap / per-vacancy cooldown /
//     max_lights_per_system caps, ferry ONE nearest-by-hops idle light from a source with
//     >= source_min_idle idle lights (never stripping it below one).
func (h *RunWorkerRebalancerCoordinatorHandler) reconcileOnce(ctx context.Context, cmd *RunWorkerRebalancerCoordinatorCommand) (int, error) {
	logger := common.LoggerFromContext(ctx)

	// Config off-switch (RULINGS #5): inert when disabled, so the container can stay
	// resident and be re-armed by a config flip + restart with no manual relaunch.
	if !cmd.Enabled {
		return 0, nil
	}

	// (1) DB-derived inputs. Any read failure fails the whole tick closed.
	allShips, err := h.shipRepo.FindAllByPlayer(ctx, cmd.PlayerID)
	if err != nil {
		return 0, fmt.Errorf("failed to list ships for worker rebalancer reconcile: %w", err)
	}
	factories, err := h.containerQuery.ActiveFactoryContainers(ctx, cmd.PlayerID.Value())
	if err != nil {
		return 0, fmt.Errorf("failed to list active factory containers: %w", err)
	}
	now := h.clock.Now()
	cooldown := cmd.cooldown()
	ferries, err := h.containerQuery.RecentFerries(ctx, cmd.PlayerID.Value(), now.Add(-cooldown))
	if err != nil {
		return 0, fmt.Errorf("failed to list recent worker_ferry containers: %w", err)
	}

	runningFerryIDs := make(map[string]bool)
	for _, f := range ferries {
		if f.Status == runningStatus {
			runningFerryIDs[f.ID] = true
		}
	}
	ferriesByID := make(map[string]FerryContainer, len(ferries))
	for _, f := range ferries {
		ferriesByID[f.ID] = f
	}

	// (2) Reclaim ended ferries FIRST, so a just-arrived hull is idle for this same pass.
	h.reclaimEndedFerries(ctx, cmd, allShips, runningFerryIDs, ferriesByID)

	// (3) Detect hub-vacancies from the (post-reclaim) fleet + factory rows.
	vacancies := h.detectVacancies(ctx, cmd, allShips, factories, now)

	// (4) Ferry pass.
	concurrent := len(runningFerryIDs)
	ferried := h.dispatchFerries(ctx, cmd, allShips, vacancies, ferries, runningFerryIDs, now, concurrent)

	logger.Log("INFO", fmt.Sprintf("Worker rebalancer tick: vacancies=%v, running_ferries=%d, ferried=%d%s",
		vacancies, concurrent, ferried, dryRunSuffix(cmd.DryRun)), map[string]interface{}{
		"action":          "worker_rebalancer_tick",
		"vacancies":       vacancies,
		"running_ferries": concurrent,
		"ferried":         ferried,
		"dry_run":         cmd.DryRun,
	})

	return ferried, nil
}

// reclaimEndedFerries releases any hull still claimed to a worker_ferry container that is
// NOT running — the single arrival-and-interruption path (sp-f5pr). A ferry-claimed hull
// is identified from SHIP STATE ALONE (its active claim's container ID carries the
// worker_ferry prefix), so an interrupted ferry of ANY age is reclaimed with zero
// persisted coordinator state — the restart-safety backbone (RULINGS #2). A RUNNING
// ferry's hull is left untouched (un-poachable). Best-effort per hull: a save failure is
// logged and skipped, never aborting the tick.
func (h *RunWorkerRebalancerCoordinatorHandler) reclaimEndedFerries(
	ctx context.Context,
	cmd *RunWorkerRebalancerCoordinatorCommand,
	allShips []*navigation.Ship,
	runningFerryIDs map[string]bool,
	ferriesByID map[string]FerryContainer,
) {
	logger := common.LoggerFromContext(ctx)
	for _, ship := range allShips {
		if !ship.IsAssigned() {
			continue
		}
		containerID := ship.ContainerID()
		if !strings.HasPrefix(containerID, workerFerryContainerPrefix) {
			continue // not ferry-claimed
		}
		if runningFerryIDs[containerID] {
			continue // ferry airborne — leave it, un-poachable (RULINGS #7)
		}

		reason := ferryEndReason(ferriesByID, containerID)
		ship.ForceRelease("worker_ferry_"+reason, h.clock)
		if err := h.shipRepo.Save(ctx, ship); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to reclaim ferried hull %s from ended ferry %s: %v", ship.ShipSymbol(), containerID, err), nil)
			continue
		}
		logger.Log("INFO", fmt.Sprintf("Reclaimed ferried hull %s from %s ferry %s — %s", ship.ShipSymbol(), reason, containerID, reclaimNote(reason)), map[string]interface{}{
			"action":       "worker_ferry_reclaim",
			"ship_symbol":  ship.ShipSymbol(),
			"container_id": containerID,
			"reason":       reason,
		})
	}
}

// detectVacancies returns the systems that are hub-vacancies THIS tick (sp-f5pr),
// deterministically sorted. A system S qualifies iff ALL hold:
//  1. active factory work: >= 1 RUNNING factory container in S;
//  2. persisted duration: the OLDEST such container started >= vacancy_min ago (the
//     restart-safe clock — a just-launched/restarted factory is exempt, RULINGS #2);
//  3. no self-heal: zero idle in-system light-haulers (FindIdleLightHaulers(S) is empty);
//  4. demand > supply (the anti-hub guard): the count of undedicated lights physically in
//     S is BELOW the factory-container count in S. A well-supplied system (lights >=
//     factories, e.g. KA42) is adequately manned and NOT a vacancy.
//
// Fails closed: a FindIdleLightHaulers read error on a candidate system drops that system
// (no ferry to a system whose self-heal state cannot be confirmed).
func (h *RunWorkerRebalancerCoordinatorHandler) detectVacancies(
	ctx context.Context,
	cmd *RunWorkerRebalancerCoordinatorCommand,
	allShips []*navigation.Ship,
	factories []ActiveFactoryContainer,
	now time.Time,
) []string {
	logger := common.LoggerFromContext(ctx)

	// Group factory containers by system: count + oldest StartedAt.
	type factoryAgg struct {
		count  int
		oldest time.Time
	}
	bySystem := make(map[string]*factoryAgg)
	for _, f := range factories {
		if f.SystemSymbol == "" {
			continue // unparseable system — cannot attribute; skip (fail-closed)
		}
		agg, ok := bySystem[f.SystemSymbol]
		if !ok {
			bySystem[f.SystemSymbol] = &factoryAgg{count: 1, oldest: f.StartedAt}
			continue
		}
		agg.count++
		if f.StartedAt.Before(agg.oldest) {
			agg.oldest = f.StartedAt
		}
	}

	vacancyMin := cmd.vacancyMin()
	var vacancies []string
	for system, agg := range bySystem {
		// (2) persisted duration.
		if now.Sub(agg.oldest) < vacancyMin {
			continue
		}
		// (3) no self-heal: an idle in-system light already mans it.
		idle, _, err := appContract.FindIdleLightHaulers(ctx, cmd.PlayerID, h.shipRepo, system)
		if err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to check in-system idle haulers for %s: %v — skipping (fail-closed)", system, err), nil)
			continue
		}
		if len(idle) > 0 {
			continue
		}
		// (4) demand > supply (anti-hub): a system already holding >= factories lights is
		// adequately supplied, even if none is idle RIGHT NOW.
		supply := undedicatedLightsInSystem(allShips, system)
		if supply >= agg.count {
			continue
		}
		vacancies = append(vacancies, system)
	}
	sort.Strings(vacancies)
	return vacancies
}

// dispatchFerries ferries at most one nearest-by-hops idle light to each vacancy, honoring
// the concurrency cap, the per-vacancy cooldown, and max_lights_per_system (sp-f5pr).
// Returns the number of ferries actually launched (0 in dry-run). A per-vacancy failure is
// logged and skipped — the rest of the vacancy list is still serviced.
func (h *RunWorkerRebalancerCoordinatorHandler) dispatchFerries(
	ctx context.Context,
	cmd *RunWorkerRebalancerCoordinatorCommand,
	allShips []*navigation.Ship,
	vacancies []string,
	ferries []FerryContainer,
	runningFerryIDs map[string]bool,
	now time.Time,
	concurrent int,
) int {
	logger := common.LoggerFromContext(ctx)
	if len(vacancies) == 0 {
		return 0
	}

	// Source pool: idle undedicated unreserved lights, grouped by current system.
	sources := h.idleSourcesBySystem(ctx, cmd)

	maxConcurrent := cmd.maxConcurrentFerries()
	cooldown := cmd.cooldown()
	maxLights := cmd.MaxLightsPerSystem
	ferried := 0

	for _, system := range vacancies {
		// Concurrency cap (DB-derived; a fresh handler counts identically).
		if concurrent >= maxConcurrent {
			logger.Log("INFO", fmt.Sprintf("Worker rebalancer at max concurrent ferries (%d) — holding remaining vacancies this tick", maxConcurrent), map[string]interface{}{
				"action":         "worker_rebalancer_max_concurrent",
				"max_concurrent": maxConcurrent,
			})
			break
		}

		// Per-vacancy cooldown: suppress if any ferry targeting S started within the window.
		if h.systemInCooldown(ferries, system, now, cooldown) {
			logger.Log("INFO", fmt.Sprintf("Vacancy %s cooling down — a ferry targeted it within %ds; skipping this tick", system, int(cooldown.Seconds())), map[string]interface{}{
				"action":        "worker_rebalancer_cooldown_hold",
				"system_symbol": system,
			})
			continue
		}

		// max_lights_per_system: in-system lights + in-flight inbound ferries.
		if maxLights > 0 {
			lights := undedicatedLightsInSystem(allShips, system) + h.inFlightFerriesToSystem(ferries, runningFerryIDs, system)
			if lights >= maxLights {
				logger.Log("INFO", fmt.Sprintf("Vacancy %s already at max_lights_per_system (%d lights incl. in-flight >= cap %d) — not ferrying", system, lights, maxLights), map[string]interface{}{
					"action":        "worker_rebalancer_max_lights",
					"system_symbol": system,
				})
				continue
			}
		}

		// Destination waypoint: a marketplace in the vacancy system (deterministic).
		destWaypoint, ok := h.pickDestination(ctx, system)
		if !ok {
			logger.Log("INFO", fmt.Sprintf("Vacancy %s has no known marketplace waypoint — cannot pick a ferry destination, skipping (fail-closed)", system), map[string]interface{}{
				"action":        "worker_rebalancer_no_destination",
				"system_symbol": system,
			})
			continue
		}

		// Nearest source by gate-graph hops (fail-closed: no gate graph, no eligible
		// source, or an unroutable pool parks the vacancy honest).
		hull, srcSystem, hops, ok := h.selectNearestSource(ctx, cmd, sources, system)
		if !ok {
			logger.Log("INFO", fmt.Sprintf("Vacancy %s: no eligible jump-routable source (>= %d idle lights) in the fleet — parked (fail-closed)", system, cmd.sourceMinIdle()), map[string]interface{}{
				"action":        "worker_rebalancer_no_source",
				"system_symbol": system,
			})
			continue
		}

		if cmd.DryRun {
			logger.Log("INFO", fmt.Sprintf("[dry-run] Would ferry %s from %s → %s (%d hop(s), dest %s)", hull, srcSystem, system, hops, destWaypoint), map[string]interface{}{
				"action":        "worker_rebalancer_ferry_dryrun",
				"ship_symbol":   hull,
				"source_system": srcSystem,
				"system_symbol": system,
				"jumps":         hops,
				"destination":   destWaypoint,
			})
			removeSourceHull(sources, srcSystem, hull) // don't "reuse" it for the next vacancy in the same dry-run pass
			continue
		}

		ferryID, err := h.spawnFerry(ctx, cmd, hull, destWaypoint)
		if err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to dispatch ferry of %s to vacancy %s: %v", hull, system, err), nil)
			continue
		}

		removeSourceHull(sources, srcSystem, hull) // in-flight now — not a candidate for another vacancy this tick
		concurrent++
		ferried++
		logger.Log("INFO", fmt.Sprintf("Ferrying %s from %s → %s (%d hop(s), ferry %s, dest %s)", hull, srcSystem, system, hops, ferryID, destWaypoint), map[string]interface{}{
			"action":        "worker_rebalancer_ferry",
			"ship_symbol":   hull,
			"source_system": srcSystem,
			"system_symbol": system,
			"jumps":         hops,
			"container_id":  ferryID,
			"destination":   destWaypoint,
		})
	}

	return ferried
}

// idleSourcesBySystem returns the idle, undedicated, UNRESERVED light-haulers grouped by
// their current system (sp-f5pr). It reuses the audited FindIdleLightHaulers predicate for
// candidacy (role/cargo/command/dedication/idle) and ADDITIONALLY drops captain-reserved
// hulls (defense-in-depth: the captain hand-reserves hulls in real time; a reservation is
// already an active assignment so FindIdleLightHaulers excludes it, but the explicit guard
// keeps the poach-safety local and obvious, RULINGS #7). Each group is symbol-sorted so
// hull selection within a source is deterministic.
func (h *RunWorkerRebalancerCoordinatorHandler) idleSourcesBySystem(ctx context.Context, cmd *RunWorkerRebalancerCoordinatorCommand) map[string][]string {
	idle, _, err := appContract.FindIdleLightHaulers(ctx, cmd.PlayerID, h.shipRepo, "")
	if err != nil {
		// Fail closed: no source pool this tick (the caller then finds no source per
		// vacancy and parks). Logged by FindIdleLightHaulers itself.
		return map[string][]string{}
	}
	sources := make(map[string][]string)
	for _, ship := range idle {
		if ship.IsReservedByCaptain() {
			continue // captain reserved — never poach (defense-in-depth)
		}
		system := shipSystem(ship)
		if system == "" {
			continue // unknown location — cannot route a source we can't place
		}
		sources[system] = append(sources[system], ship.ShipSymbol())
	}
	for system := range sources {
		sort.Strings(sources[system])
	}
	return sources
}

// selectNearestSource picks the fleet-wide nearest ELIGIBLE source system to vacancySystem
// by gate-graph hops and returns one hull from it (sp-f5pr). A source is eligible iff it
// holds >= source_min_idle idle lights AND would keep at least one behind after donating
// (never strip below one). A source whose gate-graph Path errors is skipped (fail-closed).
// Deterministic: sources are considered in sorted system order and the comparison is
// strict (< bestHops), so the lowest-symbol source wins an equal-hops tie; the chosen hull
// is the source group's lowest symbol. ok=false when no eligible, routable source exists,
// or no gate graph is wired.
func (h *RunWorkerRebalancerCoordinatorHandler) selectNearestSource(
	ctx context.Context,
	cmd *RunWorkerRebalancerCoordinatorCommand,
	sources map[string][]string,
	vacancySystem string,
) (hull, srcSystem string, hops int, ok bool) {
	if h.gateGraph == nil {
		return "", "", 0, false
	}
	logger := common.LoggerFromContext(ctx)
	minIdle := cmd.sourceMinIdle()

	srcSystems := make([]string, 0, len(sources))
	for system := range sources {
		srcSystems = append(srcSystems, system)
	}
	sort.Strings(srcSystems)

	bestSystem, bestHops := "", 0
	for _, system := range srcSystems {
		hulls := sources[system]
		if len(hulls) < minIdle || len(hulls) <= sourceKeepMin {
			continue // below the source floor, or would strip the source below one
		}
		path, err := h.gateGraph.Path(ctx, system, vacancySystem, cmd.PlayerID.Value())
		if err != nil {
			logger.Log("INFO", fmt.Sprintf("Ferry source candidate %s → %s unroutable this tick: %v", system, vacancySystem, err), map[string]interface{}{
				"action": "worker_rebalancer_source_unroutable",
				"from":   system,
				"to":     vacancySystem,
			})
			continue
		}
		candidateHops := len(path) - 1
		if bestSystem == "" || candidateHops < bestHops {
			bestSystem, bestHops = system, candidateHops
		}
	}
	if bestSystem == "" {
		return "", "", 0, false
	}
	return sources[bestSystem][0], bestSystem, bestHops, true
}

// spawnFerry persists a coordinator-managed worker_ferry worker for hull, atomically
// claims the hull to it (operation worker_ferry — the poach guard, RULINGS #7), and starts
// it (sp-f5pr). Mirrors spawnReposition exactly (persist → claim → start, with rollback on
// each failure) so the ferry inherits the same restart-recovery semantics. The claim is
// occupancy, NOT a dedication — the hull is never AssignFleet'd.
func (h *RunWorkerRebalancerCoordinatorHandler) spawnFerry(
	ctx context.Context,
	cmd *RunWorkerRebalancerCoordinatorCommand,
	hull string,
	destinationWaypoint string,
) (string, error) {
	ferryID := utils.GenerateContainerID(workerFerryOperation, hull)
	ferryCmd := &WorkerFerryCommand{
		PlayerID:            cmd.PlayerID,
		ShipSymbol:          hull,
		DestinationWaypoint: destinationWaypoint,
		CoordinatorID:       cmd.ContainerID,
	}

	if err := h.daemonClient.PersistContainer(ctx, daemon.ContainerKindWorkerFerry, ferryID, uint(cmd.PlayerID.Value()), ferryCmd); err != nil {
		return "", fmt.Errorf("failed to persist worker ferry worker: %w", err)
	}

	if err := h.shipRepo.ClaimShip(ctx, hull, ferryID, cmd.PlayerID, workerFerryOperation); err != nil {
		_ = h.daemonClient.StopContainer(ctx, ferryID)
		return "", fmt.Errorf("failed to claim light-hauler %s for ferry: %w", hull, err)
	}

	if err := h.daemonClient.StartContainer(ctx, daemon.ContainerKindWorkerFerry, ferryID); err != nil {
		h.releaseHull(ctx, cmd, hull, "worker_ferry_start_failed")
		_ = h.daemonClient.StopContainer(ctx, ferryID)
		return "", fmt.Errorf("failed to start worker ferry worker: %w", err)
	}

	return ferryID, nil
}

// pickDestination returns a deterministic marketplace waypoint in system (the smallest
// symbol), or ok=false when none is known or the lookup errors (fail-closed).
func (h *RunWorkerRebalancerCoordinatorHandler) pickDestination(ctx context.Context, system string) (string, bool) {
	waypoints, err := h.marketProvider.ListBySystemWithTrait(ctx, system, marketplaceTrait)
	if err != nil || len(waypoints) == 0 {
		return "", false
	}
	best := waypoints[0].Symbol
	for _, wp := range waypoints[1:] {
		if wp.Symbol < best {
			best = wp.Symbol
		}
	}
	return best, true
}

// systemInCooldown reports whether any ferry targeting system started within the cooldown
// window (sp-f5pr) — DB-derived from the container rows' StartedAt, so it is restart-safe.
func (h *RunWorkerRebalancerCoordinatorHandler) systemInCooldown(ferries []FerryContainer, system string, now time.Time, cooldown time.Duration) bool {
	if cooldown <= 0 {
		return false
	}
	cutoff := now.Add(-cooldown)
	for _, f := range ferries {
		if shared.ExtractSystemSymbol(f.DestinationWaypoint) != system {
			continue
		}
		if f.StartedAt.After(cutoff) {
			return true
		}
	}
	return false
}

// inFlightFerriesToSystem counts RUNNING ferries whose destination is system — the
// in-flight inbound light-haulers, for the max_lights_per_system cap (sp-f5pr).
func (h *RunWorkerRebalancerCoordinatorHandler) inFlightFerriesToSystem(ferries []FerryContainer, runningFerryIDs map[string]bool, system string) int {
	n := 0
	for _, f := range ferries {
		if !runningFerryIDs[f.ID] {
			continue
		}
		if shared.ExtractSystemSymbol(f.DestinationWaypoint) == system {
			n++
		}
	}
	return n
}

// releaseHull frees a specific hull by symbol (ferry start-failure rollback). Best-effort.
func (h *RunWorkerRebalancerCoordinatorHandler) releaseHull(ctx context.Context, cmd *RunWorkerRebalancerCoordinatorCommand, hull, reason string) {
	logger := common.LoggerFromContext(ctx)
	ship, err := h.shipRepo.FindBySymbol(ctx, hull, cmd.PlayerID)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed to load hull %s to release (%s): %v", hull, reason, err), nil)
		return
	}
	if !ship.IsAssigned() {
		return
	}
	ship.ForceRelease(reason, h.clock)
	if err := h.shipRepo.Save(ctx, ship); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed to release hull %s (%s): %v", hull, reason, err), nil)
	}
}

// ---- pure helpers ----------------------------------------------------------

// runningStatus is the container status string that counts a ferry as airborne.
const runningStatus = "RUNNING"

// undedicatedLightsInSystem counts the undedicated light-haulers physically in system,
// REGARDLESS of assignment or current waypoint within the system (sp-f5pr) — the anti-hub
// supply measure. It classifies with the same predicate FindIdleLightHaulers uses (role
// HAULER, cargo capacity > 0, not the command hull, no dedication), so a system's supply
// and its ferry-source pool see the same hull class.
func undedicatedLightsInSystem(allShips []*navigation.Ship, system string) int {
	n := 0
	for _, ship := range allShips {
		if !isUndedicatedLightHauler(ship) {
			continue
		}
		if shipSystem(ship) == system {
			n++
		}
	}
	return n
}

// isUndedicatedLightHauler is the shared candidacy predicate (sp-f5pr): a haul-capable,
// undedicated, non-command hull. Mirrors FindIdleLightHaulers' role/cargo/command/
// dedication filter WITHOUT the idle/in-transit/location checks, so it classifies a hull's
// CLASS independent of its momentary availability (the anti-hub supply count needs busy
// lights to count too).
func isUndedicatedLightHauler(ship *navigation.Ship) bool {
	if domainContract.IsCommandHull(ship) {
		return false
	}
	if ship.Role() != roleHauler {
		return false
	}
	if ship.CargoCapacity() == 0 {
		return false
	}
	return ship.DedicatedFleet() == ""
}

// shipSystem returns the system a ship is currently in, or "" when its location is unknown
// (fail-closed: an unplaceable hull is neither counted as supply nor used as a source).
func shipSystem(ship *navigation.Ship) string {
	loc := ship.CurrentLocation()
	if loc == nil {
		return ""
	}
	return shared.ExtractSystemSymbol(loc.Symbol)
}

// removeSourceHull drops hull from its source group so it is not offered to another vacancy
// in the same tick (it is now in-flight, or dry-run "spent").
func removeSourceHull(sources map[string][]string, srcSystem, hull string) {
	hulls := sources[srcSystem]
	kept := hulls[:0]
	for _, s := range hulls {
		if s != hull {
			kept = append(kept, s)
		}
	}
	sources[srcSystem] = kept
}

// ferryEndReason names why a ferry-claimed hull is being reclaimed, from the ferry's
// terminal status: COMPLETED ⇒ "arrival" (the hull reached the vacancy system and was
// released there), anything else (FAILED/INTERRUPTED/STOPPED, or a row too old to be in
// the query window) ⇒ "interrupted".
func ferryEndReason(ferriesByID map[string]FerryContainer, containerID string) string {
	if f, ok := ferriesByID[containerID]; ok && f.Status == "COMPLETED" {
		return "arrival"
	}
	return "interrupted"
}

// reclaimNote is the human tail of the reclaim log line, per end reason.
func reclaimNote(reason string) string {
	if reason == "arrival" {
		return "released idle in-system for the factory to man"
	}
	return "released for re-evaluation next tick (never stranded)"
}

// dryRunSuffix annotates the tick summary when nothing was actually dispatched.
func dryRunSuffix(dryRun bool) string {
	if dryRun {
		return " (dry-run: nothing dispatched)"
	}
	return ""
}

// ---- knob resolution (RULINGS #5: 0/unset ⇒ documented default) ------------

func (c *RunWorkerRebalancerCoordinatorCommand) vacancyMin() time.Duration {
	m := c.VacancyMinMinutes
	if m <= 0 {
		m = defaultVacancyMinMinutes
	}
	return time.Duration(m) * time.Minute
}

func (c *RunWorkerRebalancerCoordinatorCommand) sourceMinIdle() int {
	if c.SourceMinIdle <= 0 {
		return defaultSourceMinIdle
	}
	return c.SourceMinIdle
}

func (c *RunWorkerRebalancerCoordinatorCommand) cooldown() time.Duration {
	s := c.FerryCooldownSecs
	if s <= 0 {
		s = defaultFerryCooldownSeconds
	}
	return time.Duration(s) * time.Second
}

func (c *RunWorkerRebalancerCoordinatorCommand) maxConcurrentFerries() int {
	if c.MaxConcurrentFerries <= 0 {
		return defaultMaxConcurrentFerries
	}
	return c.MaxConcurrentFerries
}

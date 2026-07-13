package commands

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/contract"
	mfgServices "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services"
	mfgTypes "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/types"
	// aliased: this file's executeLevelParallel goroutine already binds a
	// local variable named "ship" (ship := <-exec.shipPool), which would
	// shadow the bare package name — same alias siphon_resources.go uses for
	// the identical reason.
	shipapp "github.com/andrescamacho/spacetraders-go/internal/application/ship"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/google/uuid"
)

// Type aliases for convenience
type RunFactoryCoordinatorCommand = mfgTypes.RunFactoryCoordinatorCommand
type RunFactoryCoordinatorResponse = mfgTypes.RunFactoryCoordinatorResponse

const (
	shipDiscoveryInterval  = 30 * time.Second
	shipPoolCapacityFactor = 2

	// noWorkIterationDelay throttles a -1 (infinite) coordinator's next
	// iteration after one that performed no work at all — the sp-2dv4
	// chain-margin guard parked pre-spend, or every claimable node parked
	// (sp-vsfn catch-all) for lack of a claimable in-system hull. Handle()
	// otherwise returns clean and instant, and the container runner
	// (container_runner.go) re-invokes it immediately: a starved factory was
	// clocked at ~280 no-op iterations/sec (8,377 in 30s, sp-2q2o), fast
	// enough to rotate the guard's own park verdict out of the per-container
	// log ring before an operator could read it. 45s sits inside the bead's
	// mandated 30-60s band, deliberately off the 30s shipDiscoveryInterval
	// cadence above so the two polls don't beat together.
	noWorkIterationDelay = 45 * time.Second

	// noWorkHeartbeatInterval re-logs the current no-work reason on a slow
	// cadence so a long-parked factory still proves it's alive (and why) in
	// the log window, without repeating the line on every ~45s iteration.
	noWorkHeartbeatInterval = 10 * time.Minute
)

// RunFactoryCoordinatorHandler orchestrates fleet-based goods production.
// Pattern: Fleet Coordinator (like ContractFleetCoordinator)
//
// Workflow:
// 1. Build dependency tree using SupplyChainResolver
// 2. Analyze dependencies to identify parallel execution levels
// 3. Discover idle ships for parallel execution
// 4. Execute production in parallel levels (bottom-up: leaves to root)
//   - Spawn goroutines for independent nodes at each level
//   - Use channels for completion signaling
//   - Wait for level completion before proceeding to next level
//
// 5. Return aggregated results
//
// Error Handling:
// - Worker failure → propagate error, cancel remaining workers
// - No idle ships → return error
// - Market unavailable → backoff and retry (handled by ProductionExecutor)
type RunFactoryCoordinatorHandler struct {
	mediator           common.Mediator
	shipRepo           navigation.ShipRepository
	marketRepo         market.MarketRepository
	resolver           *mfgServices.SupplyChainResolver
	marketLocator      *mfgServices.MarketLocator
	productionExecutor *mfgServices.ProductionExecutor
	dependencyAnalyzer *mfgServices.DependencyAnalyzer
	chainMarginGuard   *mfgServices.ChainMarginGuard
	clock              shared.Clock

	// chainPnLReader is the optional DB-backed realized-P&L ledger the kill-switch judges
	// (sp-rh2z). nil disables the kill-switch (fail-OPEN) — the optional-port contract the
	// test fixtures rely on, identical to the executor's priceHistory/spendLedger. The daemon
	// wires the real reader via SetChainPnLReader.
	chainPnLReader mfgServices.ChainPnLReader

	// noWorkMu guards noWorkState. This handler is a singleton shared across
	// every concurrent goods_factory container (main.go constructs it once),
	// so the no-work log-dedup state is keyed by ContainerID to keep sibling
	// factories from contaminating each other's state (sp-2q2o).
	noWorkMu    sync.Mutex
	noWorkState map[string]*noWorkTracker

	// chainPnLKillMu guards chainPnLKillState, the per-container "currently auto-paused"
	// flag the kill-switch uses for episode dedup (sp-rh2z): the kill counter/WARN emit
	// once on running→paused and the resume INFO once on paused→running, not on every
	// re-check. Keyed by ContainerID for the same singleton-across-containers reason as
	// noWorkState (one container = one chain).
	chainPnLKillMu    sync.Mutex
	chainPnLKillState map[string]bool

	// inputPauseMu guards inputPauseState, the per-container input-poison anti-cycle state
	// (sp-r5a6): a paused chain's recovery clock (when it may re-attempt) plus the cached pause
	// line to re-report while it sleeps. Drives once-per-episode dedup (pause counter/WARN emit
	// once on running→paused, resume INFO once on paused→running) AND the recovery-clock backoff
	// (a paused chain sleeps until reattemptAt instead of re-polling every 45s). Keyed by
	// ContainerID for the same singleton-across-containers reason as noWorkState.
	inputPauseMu    sync.Mutex
	inputPauseState map[string]*inputPauseEntry

	// exportRestMu guards exportRestState, the per-container export-ask-subsidy rest state (sp-xdk6):
	// a resting chain's recovery-window clock (when it may lift again) plus the cached rest line to
	// re-report while it rests. Drives once-per-episode dedup (rest counter/WARN emit once on
	// lifting→resting, resume INFO once on resting→lifting) AND the recovery-window backoff (a
	// resting chain sleeps until liftAllowedAt instead of re-polling every 45s). The OUTPUT-LADDER
	// sibling of inputPauseState. Keyed by ContainerID (one container = one chain).
	exportRestMu    sync.Mutex
	exportRestState map[string]*exportRestEntry

	// plannerStock deposits harvested root output into a co-located warehouse at
	// cost basis instead of selling it at market (C1, sp-64je). LIVE BY DEFAULT:
	// the daemon wires it unconditionally and it runs unless the run's
	// planner_stock_disabled escape hatch is set. nil only in tests / a build that
	// omits the wiring, in which case output sells as before.
	plannerStock plannerStockDepositor

	// workerCapProvider resolves the LIVE per-op worker/hull cap from this
	// container's OWN config each pass (sp-ev0n) — the store the `goods factory
	// workers` daemon RPC mutates. It is the factory analogue of the contract
	// coordinator's StandbyStationProvider (sp-jcke): a live cap change is honored
	// on the very next pass with no restart. nil (tests, or a build that omits the
	// wiring) leaves the coordinator on the launch-resolved cmd.WorkerCap, which is
	// never worse than the pre-fix behavior. The daemon wires the real
	// container-config-backed reader via SetWorkerCapProvider.
	workerCapProvider FactoryWorkerCapProvider
}

// FactoryWorkerCapProvider resolves the LIVE per-op worker/hull cap for a
// goods_factory container each production pass (sp-ev0n), the operation-level
// analogue of the contract coordinator's live standby-station read (sp-jcke). It
// is backed by the coordinator's OWN container config — the store the `goods
// factory workers` daemon RPC mutates — so a cap set live is visible to the
// fan-out on the very next pass with no restart. A nil provider or a read error
// leaves the coordinator on the launch-resolved cap (never worse than the pre-fix
// behavior); ok=false means no live per-op override is set, so the launch cap
// stands.
type FactoryWorkerCapProvider interface {
	// WorkerCap returns the container's live per-op cap and whether a positive
	// live override is currently set. ok=false (absent, non-positive, or an
	// unreadable key) tells the caller to keep the launch-resolved cap.
	WorkerCap(ctx context.Context, containerID string, playerID int) (cap int, ok bool, err error)
}

// plannerStockDepositor is the factory's view of the planner-visible-stock
// deposit path (C1, sp-64je). Satisfied by *mfgServices.PlannerStockDepositor.
type plannerStockDepositor interface {
	DepositOutput(ctx context.Context, playerID int, shipSymbol, waypoint, good string, units, unitBasis int) (bool, error)
}

// noWorkTracker remembers one container's last-logged no-work reason and
// when it was logged, so backoffNoWork can log a state change once (plus a
// slow heartbeat) instead of on every throttled iteration.
type noWorkTracker struct {
	reason   string
	loggedAt time.Time
}

// NewRunFactoryCoordinatorHandler creates a new factory coordinator handler
func NewRunFactoryCoordinatorHandler(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	marketRepo market.MarketRepository,
	resolver *mfgServices.SupplyChainResolver,
	marketLocator *mfgServices.MarketLocator,
	clock shared.Clock,
	apiClient domainPorts.APIClient, // live treasury reads for the factory input-buy spend floor (sp-9aoc); nil disables it (tests)
) *RunFactoryCoordinatorHandler {
	// Honour the "nil = use RealClock" wiring convention (main.go) that every
	// sibling coordinator follows (run_parallel_manufacturing_coordinator.go,
	// assign_scouting_fleet.go, ...). Omitting this left h.clock nil and
	// SIGSEGV'd the daemon when the parallel claim path called clock.Now()
	// (sp-bt6o).
	if clock == nil {
		clock = shared.NewRealClock()
	}

	productionExecutor := mfgServices.NewProductionExecutor(
		mediator,
		shipRepo,
		marketRepo,
		marketLocator,
		clock,
		apiClient, // sp-9aoc: threads the live client to buyGood's working-capital spend floor
	)

	dependencyAnalyzer := mfgServices.NewDependencyAnalyzer()

	// sp-2dv4: pre-spend chain-margin + absorption guard, built from the same
	// market accessors the coordinator already holds — no wiring change upstream.
	chainMarginGuard := mfgServices.NewChainMarginGuard(marketLocator, marketRepo)

	return &RunFactoryCoordinatorHandler{
		mediator:           mediator,
		shipRepo:           shipRepo,
		marketRepo:         marketRepo,
		resolver:           resolver,
		marketLocator:      marketLocator,
		productionExecutor: productionExecutor,
		dependencyAnalyzer: dependencyAnalyzer,
		chainMarginGuard:   chainMarginGuard,
		clock:              clock,
		noWorkState:        make(map[string]*noWorkTracker),
		chainPnLKillState:  make(map[string]bool),
		inputPauseState:    make(map[string]*inputPauseEntry),
		exportRestState:    make(map[string]*exportRestEntry),
	}
}

// SetSpendLedger wires the cross-container concurrent factory-input spend cap (sp-w3he)
// into the production executor. The daemon calls this after construction (main.go), the
// same setter-injection pattern as SetEventSubscriber on the contract coordinator; left
// unset the cap is fail-open, which is exactly what every test caller wants.
func (h *RunFactoryCoordinatorHandler) SetSpendLedger(ledger mfgServices.SpendReservationLedger) {
	h.productionExecutor.SetSpendLedger(ledger)
}

// SetPlannerStockDepositor wires the planner-visible-stock deposit path (C1,
// sp-64je): harvested root output deposits into a co-located warehouse at cost
// basis instead of selling at market. The feature is LIVE BY DEFAULT — the daemon
// calls this unconditionally and it runs unless a run's planner_stock_disabled
// escape hatch is set. Left unset (tests) the output sells at the resale sink.
func (h *RunFactoryCoordinatorHandler) SetPlannerStockDepositor(dep plannerStockDepositor) {
	h.plannerStock = dep
}

// SetPriceHistoryReader wires the trailing-median source for the factory input price
// ceiling (sp-iv65) into the production executor. The daemon calls this after construction
// with the DB-backed price history repo (main.go), the same setter-injection pattern as
// SetSpendLedger; left unset the ceiling is fail-open, which is what every test caller wants.
func (h *RunFactoryCoordinatorHandler) SetPriceHistoryReader(reader mfgServices.InputPriceHistoryReader) {
	h.productionExecutor.SetPriceHistoryReader(reader)
}

// SetWorkerCapProvider wires the live per-op worker-cap reader (sp-ev0n) so the
// coordinator re-reads its concurrent-hull cap from its own container config each
// pass — the factory mirror of SetStandbyStationProvider (sp-jcke). The daemon
// calls this after construction with the container-config-backed provider
// (main.go); left unset the coordinator uses the launch-resolved cmd.WorkerCap,
// which is exactly what every test caller wants.
func (h *RunFactoryCoordinatorHandler) SetWorkerCapProvider(provider FactoryWorkerCapProvider) {
	h.workerCapProvider = provider
}

// resolveEffectiveWorkerCap returns the concurrent-hull cap this pass must honor,
// mirroring ResolveStandbyStations (sp-jcke): the live per-op override read from
// the container config is authoritative; a nil provider, a read error, or no live
// override falls back to the launch-resolved cmd.WorkerCap so a transient failure
// is never worse than the frozen-launch behavior. A returned <=0 means unbounded
// (the pre-sp-ev0n emergent fan-out). Because worker_cap is persisted in the
// container config and is NOT re-injected from config.yaml on rebuild, the
// launch value already reflects a prior live change across a restart (RULINGS #2).
func (h *RunFactoryCoordinatorHandler) resolveEffectiveWorkerCap(ctx context.Context, cmd *RunFactoryCoordinatorCommand) int {
	if h.workerCapProvider == nil {
		return cmd.WorkerCap
	}
	live, ok, err := h.workerCapProvider.WorkerCap(ctx, cmd.ContainerID, cmd.PlayerID)
	if err != nil {
		if logger := common.LoggerFromContext(ctx); logger != nil {
			logger.Log("WARNING", fmt.Sprintf(
				"failed to read live worker cap for factory %s (falling back to launch cap %d): %v",
				cmd.ContainerID, cmd.WorkerCap, err), nil)
		}
		return cmd.WorkerCap
	}
	if !ok {
		return cmd.WorkerCap
	}
	return live
}

// Handle executes the factory coordinator command
func (h *RunFactoryCoordinatorHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*RunFactoryCoordinatorCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	response := &RunFactoryCoordinatorResponse{
		FactoryID:        generateFactoryID(),
		TargetGood:       cmd.TargetGood,
		QuantityAcquired: 0,
		TotalCost:        0,
		NodesCompleted:   0,
		NodesTotal:       0,
		ShipsUsed:        0,
		Completed:        false,
		Error:            "",
	}

	// Execute coordination workflow
	if err := h.executeCoordination(ctx, cmd, response); err != nil {
		response.Error = err.Error()
		return response, err
	}

	response.Completed = true

	// sp-2q2o: a -1 (infinite) container's runner re-invokes Handle() the
	// instant it returns, so an iteration that did no work must not return
	// instantly or the container spins hundreds of times a second. A bounded
	// run (MaxIterations >= 0 - a one-shot CLI invocation, a fixed count, or
	// a test) is left alone: it terminates on its own and returning fast is
	// exactly what it should do.
	if response.NoWorkReason != "" && cmd.MaxIterations == -1 {
		h.backoffNoWork(ctx, cmd.ContainerID, response.NoWorkReason)
	}

	return response, nil
}

// backoffNoWork throttles a -1 container's next iteration after one that did
// no work, logging the reason once per state change (plus a slow heartbeat)
// rather than on every iteration (sp-2q2o). This handler is a singleton
// shared across every concurrent goods_factory container, so dedup state is
// keyed by containerID and mutex-guarded.
func (h *RunFactoryCoordinatorHandler) backoffNoWork(ctx context.Context, containerID, reason string) {
	now := h.clock.Now()

	h.noWorkMu.Lock()
	tracker, exists := h.noWorkState[containerID]
	stateChanged := !exists || tracker.reason != reason
	heartbeatDue := exists && !stateChanged && now.Sub(tracker.loggedAt) >= noWorkHeartbeatInterval
	shouldLog := stateChanged || heartbeatDue
	if shouldLog {
		h.noWorkState[containerID] = &noWorkTracker{reason: reason, loggedAt: now}
	}
	h.noWorkMu.Unlock()

	// sp-r5a6: an input-poison pause rests the chain for its recovery half-life, not the 45s
	// no-work poll — a just-flickering-marginal well re-poisons under early-recovery buys, so a
	// paused chain sleeps until its re-attempt is due (zero polling during recovery). Any other
	// no-work reason (a margin park, a no-hull park) keeps the normal short backoff.
	//
	// sp-xdk6: an export-ask-subsidy rest sleeps for its recovery WINDOW (same reasoning — re-lifting
	// an over-drawn output market during early recovery re-ladders it). The input-pause delay is
	// checked FIRST so it wins precedence; the two are mutually exclusive in practice (an
	// input-paused chain returns before the rest is ever armed), so this only ever fires for a chain
	// resting purely on the output-ladder signal.
	delay := noWorkIterationDelay
	if reattemptDelay, paused := h.inputPauseReattemptDelay(containerID); paused {
		delay = reattemptDelay
	} else if restDelay, resting := h.exportRestReattemptDelay(containerID); resting {
		delay = restDelay
	}

	if shouldLog {
		logger := common.LoggerFromContext(ctx)
		logger.Log("INFO", "Factory idle - waiting for workers", map[string]interface{}{
			"container_id": containerID,
			"reason":       reason,
			"backoff":      delay.String(),
		})
	}

	h.sleepInterruptibly(ctx, delay)
}

// sleepInterruptibly blocks for d via the handler's injected clock (so tests
// can fake it with a MockClock) while still honouring context cancellation.
// shared.Clock has no cancellation of its own - RealClock.Sleep is a plain
// blocking time.Sleep - so the actual sleep runs on a background goroutine
// and this races it against ctx.Done(). A cancelled container therefore
// returns immediately instead of hanging for up to d - whether d is the
// sp-2q2o no-work backoff (noWorkIterationDelay) or the sp-l709 idle-hauler
// park-poll wait (shipDiscoveryInterval); the abandoned goroutine finishes
// sleeping in the background and exits cleanly on its own, holding no
// resources worth cancelling.
func (h *RunFactoryCoordinatorHandler) sleepInterruptibly(ctx context.Context, d time.Duration) {
	done := make(chan struct{})
	go func() {
		h.clock.Sleep(d)
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
	}
}

// executeCoordination orchestrates the complete fleet-based production workflow
func (h *RunFactoryCoordinatorHandler) executeCoordination(
	ctx context.Context,
	cmd *RunFactoryCoordinatorCommand,
	response *RunFactoryCoordinatorResponse,
) error {
	logger := common.LoggerFromContext(ctx)

	// Create operation context for transaction linking and add to context
	if cmd.ContainerID != "" {
		opContext := shared.NewOperationContext(cmd.ContainerID, "factory_workflow")
		ctx = shared.WithOperationContext(ctx, opContext)
	}

	// sp-agzj: thread the per-run working-capital reserve to the point of spend so the
	// factory input-buy floor tracks the fleet reserve (effective floor = max(50k,
	// configured)) instead of the stale hardcoded 50k. Stamped ONCE here — the
	// ProductionExecutor is a singleton shared across concurrent factory containers, so
	// ctx (per-Handle) carries it race-free to every parallel worker's buyGood.
	ctx = mfgServices.WithConfiguredReserve(ctx, cmd.WorkingCapitalReserve)
	// sp-yqx4: stamp the treasury-percent so each input buy's floor resolves the
	// counter-cyclical max(50k, min(reserve, pct% × live treasury)) instead of the flat
	// absolute reserve — a factory is no longer deadlocked by a reserve above the treasury.
	// Only when set (the goods_factory build resolves 0/absent → 40); a directly-built
	// command leaves it 0, keeping the absolute floor the sp-agzj/kk61 suites assert.
	if cmd.WorkingCapitalReserveTreasuryPct > 0 {
		ctx = common.WithReserveTreasuryPct(ctx, cmd.WorkingCapitalReserveTreasuryPct)
	}
	// sp-iv65: stamp the ladder-chase input price ceiling config so each parallel worker's
	// buyGood resolves it race-free off ctx (same singleton-executor reasoning as the reserve
	// above). A 0 multiplier resolves to the 1.5 default at the point of use; disabled=true is
	// the emergency off-switch. Stamped unconditionally so a directly-built command (0/false)
	// still runs the guard at its default when a price-history reader is wired.
	ctx = mfgServices.WithInputPriceCeiling(ctx, cmd.InputPriceCeilingMultiplier, cmd.InputPriceCeilingDisabled)
	// sp-a5j7 Phase 2: stamp the supply-first sourcing config the same way (same singleton-executor
	// race reasoning). Rescue multiplier 0 resolves to the 1.2 default; era-end flips to
	// price-first < T-6h; disabled reverts to pure price-first. Stamped unconditionally so a
	// directly-built command (0, false, false) still runs supply-first at its default.
	ctx = mfgServices.WithInputSourcing(ctx, cmd.InputRescueMultiplier, cmd.InputEraEndPriceFirst, cmd.InputSourcingDisabled)
	// sp-jav2 / FACTORY_DOCTRINE X1: stamp the fabricate depth cap so the SupplyChainResolver (a
	// boot singleton shared across sibling factory containers) reads it race-free off ctx. maxDepth
	// 0 resolves to the depth-1 default at the point of use — fabricate the output, buy its inputs;
	// disabled=true restores the original unbounded recursion. Stamped unconditionally so a
	// directly-built command (0/false) still caps at depth-1.
	ctx = mfgServices.WithFabricateDepthCap(ctx, cmd.FabricateMaxDepth, cmd.FabricateDepthCapDisabled)
	// sp-sdyo: stamp the per-good buy-gating overrides the same way (same singleton-executor race
	// reasoning — the resolver and executor are boot singletons shared across sibling factories, so
	// per-run overrides ride ctx, not a struct field). The resolver reads the per-good strategy and
	// the executor's ceiling reads the per-good priceCeilingMult (hard-capped, RULINGS #4). A nil map
	// (a directly-built command, or a launch with no overrides) leaves every good on the global gates.
	ctx = mfgServices.WithGoodGatingOverrides(ctx, cmd.GoodGatingOverrides)
	// sp-yfzi: stamp the per-run PRODUCTION acquisition strategy so the shared singleton resolver
	// resolves this factory's tree on Smart (fabricate a SCARCE intermediate that has a factory, buy
	// an abundant one) instead of its prefer-buy estimation default. Same ctx-not-struct-field race
	// reasoning as the depth cap above. Empty (a directly-built command) is a no-op — the resolver
	// keeps prefer-buy, byte-identical to today; the goods_factory launch build defaults it to smart.
	ctx = mfgServices.WithProductionStrategy(ctx, cmd.ProductionStrategy)

	logger.Log("INFO", "Starting factory coordinator", map[string]interface{}{
		"factory_id":    response.FactoryID,
		"target_good":   cmd.TargetGood,
		"system_symbol": cmd.SystemSymbol,
	})

	// Step 0.5: Input-poison anti-cycle recovery window (sp-r5a6). If this chain is already
	// input-paused and its recovery clock has NOT elapsed, do ZERO work this tick — skip the
	// tree build, every market read, and all buying pressure — and let Handle's backoff sleep
	// the container to the re-attempt. This is the "held off the market during early recovery"
	// half of the anti-cycle: re-polling a just-flickering-marginal well re-poisons it (the T1
	// finding), so a paused chain reads nothing until the half-life is up. Scoped to resale runs
	// like the guards below; when the anti-cycle is disabled the window is ignored (Step 2.4
	// clears any stale pause).
	if !cmd.InputsOnly && !cmd.AntiCycleDisabled {
		if pausedMsg, withinWindow := h.inputPauseWithinWindow(cmd.ContainerID); withinWindow {
			response.NoWorkReason = pausedMsg
			return nil
		}
	}

	// Step 0.6: Export-ask-subsidy REST recovery window (sp-xdk6). If this chain is mid-rest (its
	// own output market laddered above the eligible median last tick) and the recovery window has
	// NOT elapsed, do ZERO work this tick — skip the tree build and every market read — and let
	// Handle's backoff sleep the container to the window. This is the "held off the lift during
	// recovery" half of the signal: re-lifting an over-drawn market during early recovery just
	// re-ladders it, so a resting chain reads nothing until the window is up. Checked AFTER the
	// input-pause window above so an input-paused chain (the upstream cause) wins precedence. Scoped
	// to resale runs; when the signal is disabled the window is ignored (Step 2.45 clears any stale
	// rest).
	if !cmd.InputsOnly && !cmd.RestSignalDisabled {
		if restMsg, withinWindow := h.exportRestWithinWindow(cmd.ContainerID); withinWindow {
			response.NoWorkReason = restMsg
			return nil
		}
	}

	// Step 1: Build dependency tree
	dependencyTree, err := h.buildDependencyTree(ctx, cmd)
	if err != nil {
		return fmt.Errorf("failed to build dependency tree: %w", err)
	}

	// Step 2: Flatten tree to get production nodes
	nodes := dependencyTree.FlattenToList()
	response.NodesTotal = len(nodes)

	// sp-c07v: every good anywhere in this factory's own tree (target output
	// plus every input at every level) counts as "related" cargo for the
	// claim-time guard below (Step 3) - see filterUnrelatedCargo.
	relatedGoods := treeGoodsList(nodes)

	logger.Log("INFO", "Dependency tree built", map[string]interface{}{
		"factory_id":      response.FactoryID,
		"target_good":     cmd.TargetGood,
		"total_nodes":     response.NodesTotal,
		"tree_depth":      dependencyTree.TotalDepth(),
		"buy_nodes":       countNodesByMethod(nodes, goods.AcquisitionBuy),
		"fabricate_nodes": countNodesByMethod(nodes, goods.AcquisitionFabricate),
	})

	// Step 2.4: Input-poison anti-cycle detection (sp-r5a6). BEFORE the margin guard and the C2
	// kill-switch, ask whether this chain's market-sourced input layer has gone ineligible — no
	// MODERATE+ in-system supply source for a required input (the a5j7 leading indicator, read
	// off the same eligibility the supply-first selector picks from). When it has, PAUSE the
	// chain onto the recovery clock: emit the pause (once per episode), set the NoWorkReason, and
	// return pre-spend (zero credits, worker freed) — Handle's backoff then sleeps the container
	// for the recovery half-life before the one-iteration re-attempt. Placed first so an
	// input-poisoned chain gets the long recovery pause rather than the margin guard's short park
	// or a C2 realized-P&L kill (precedence: this is cheaper and is the upstream cause). Scoped to
	// resale runs (!InputsOnly) like the guards below. On a CLEARED (eligible) layer it lifts any
	// standing pause and falls through to the margin guard + production.
	if !cmd.InputsOnly {
		pause := h.evaluateInputLayerPause(ctx, cmd, nodes)
		if pause.Paused {
			h.recordInputLayerPause(ctx, cmd, pause)
			response.NoWorkReason = pause.PauseMessage()
			return nil
		}
		h.clearInputLayerPause(ctx, cmd)
	}

	// Step 2.45: Export-ask-subsidy REST signal (sp-xdk6, analyst redesign C4). AFTER the input-pause
	// (which wins precedence — an input-poisoned chain isn't lifting to over-draw anything) and
	// BEFORE the margin guard + C2 kill, ask whether this chain's OWN output market's ask has
	// laddered above the eligible cross-source median (EligibleSourceMedianAsk, the a5j7 baseline
	// reused). When it has, REST the chain onto the recovery-window clock: emit the rest (once per
	// episode), set the NoWorkReason, and return pre-spend (zero credits, worker freed) — Handle's
	// backoff then sleeps the container for the rest window before the one-iteration re-attempt.
	// Placed here so an over-lifted chain gets the proper recovery-window rest rather than the margin
	// guard's short 45s park or a lagging C2 P&L kill (the ask ladder is the LEADING symptom of the
	// same phenomenon C2 catches late). Scoped to resale runs (!InputsOnly). On a recovered market
	// (own ask ≤ median) it lifts any standing rest and falls through to the margin guard + production.
	if !cmd.InputsOnly {
		rest := h.evaluateExportRest(ctx, cmd, dependencyTree)
		if rest.Rested {
			h.recordExportRest(ctx, cmd, rest)
			response.NoWorkReason = rest.RestMessage()
			return nil
		}
		h.clearExportRest(ctx, cmd)
	}

	// Step 2.5: Pre-spend chain-margin + absorption guard (sp-2dv4, money-integrity
	// #3). Project the whole chain's LIVE P&L and the final sink's absorption BEFORE
	// committing a single feed buy. A factory started against crushed feed import
	// bids (negative projected margin) — or one whose resale sink is too small to
	// ever absorb the feed spend — PARKS pre-spend (zero credits committed) instead
	// of bleeding, mirroring the 9aoc solvency floor's fail-closed discipline and
	// reusing the clean partial-success contract (return nil, no error). Scoped to
	// resale runs: inputs-only construction supply has no resale sink and is left to
	// the construction pipeline's own economics + the bp6f #3 harvest guard. This
	// guard is additive and touches none of those existing park/floor paths.
	if !cmd.InputsOnly {
		proj := h.chainMarginGuard.Evaluate(ctx, dependencyTree, cmd.SystemSymbol, cmd.PlayerID)
		if !proj.Proceed {
			logger.Log("WARNING", proj.ParkMessage(), proj.LogFields(response.FactoryID))
			response.NoWorkReason = proj.ParkMessage()
			return nil
		}
		logger.Log("INFO", proj.ProceedMessage(), proj.LogFields(response.FactoryID))
	}

	// Step 2.6: Chain P&L kill-switch (sp-rh2z, analyst redesign C2). AFTER the pre-spend
	// margin guard, project this chain's REALIZED P&L/hr over the rolling window (factory local
	// sells + tour realized net − input cost − lift). When it falls below the kill threshold,
	// auto-PAUSE the chain pre-spend (zero credits committed, same clean partial-success
	// contract as the margin guard) and let the -1 container's re-invocation loop RESUME it
	// automatically once the window recovers — the portfolio becomes self-pruning. Scoped to
	// resale runs (!InputsOnly) like the margin guard: an inputs-only construction feeder
	// realizes its value later through the construction pipeline, not through sells, so realized
	// P&L is not its success signal. FAILS OPEN (RULINGS #4, unlike the pre-spend guards which
	// fail closed): every blind/disabled/unreadable/pre-realization path PROCEEDS — the switch
	// can only stop spend, and an accounting outage must not halt production.
	if !cmd.InputsOnly {
		verdict := h.evaluateChainPnLKill(ctx, cmd)
		if verdict.Killed {
			h.recordChainPnLKill(ctx, cmd, verdict)
			response.NoWorkReason = verdict.KillMessage()
			return nil
		}
		h.clearChainPnLKill(ctx, cmd, verdict)
	}

	// Step 3: Wait for an idle hauler.
	//
	// At launch the factory may momentarily find every hauler
	// coordinator-assigned. Rather than crashing on that transient gap (sp-vmrj —
	// the "impatience crash"), we poll for the next idle hauler the same way the
	// long-lived fleet coordinator waits for ships (run_fleet_coordinator.go):
	// keep re-discovering until a hauler frees or the container's context is
	// cancelled. A factory that holds a market at MODERATE+ is long-lived, so an
	// indefinite wait for the next idle gap is the correct bound.
	playerID := shared.MustNewPlayerID(cmd.PlayerID)
	idleShips, idleShipSymbols, err := h.waitForIdleHaulers(ctx, playerID, cmd.SystemSymbol, relatedGoods, response.FactoryID)
	if err != nil {
		return err
	}

	logger.Log("INFO", "Discovered idle ships for production", map[string]interface{}{
		"factory_id":   response.FactoryID,
		"ship_count":   len(idleShips),
		"ship_symbols": idleShipSymbols,
	})

	// Step 4: Analyze dependencies for parallel execution
	parallelLevels := h.dependencyAnalyzer.IdentifyParallelLevels(dependencyTree)
	speedup := h.dependencyAnalyzer.EstimateParallelSpeedup(parallelLevels)

	logger.Log("INFO", "Parallel execution plan created", map[string]interface{}{
		"factory_id":        response.FactoryID,
		"parallel_levels":   len(parallelLevels),
		"estimated_speedup": speedup,
	})

	// Live per-op worker cap (sp-ev0n): resolve the concurrent-hull bound FRESH this
	// pass from the container config (via the injected provider), so a `goods factory
	// workers` change converges the fan-out to the new N on the very next pass with no
	// restart. <=0 = unbounded (the pre-fix emergent fan-out). Resolved here — after the
	// pre-spend guards, right before the fan-out — so the read is skipped entirely on a
	// parked/no-work pass.
	workerCap := h.resolveEffectiveWorkerCap(ctx, cmd)
	if workerCap > 0 {
		logger.Log("INFO", fmt.Sprintf("Factory worker cap: at most %d concurrent hull(s) this pass", workerCap), map[string]interface{}{
			"factory_id":   response.FactoryID,
			"container_id": cmd.ContainerID,
			"worker_cap":   workerCap,
		})
	}

	// Step 5: Execute production in parallel levels
	if err := h.executeParallelProduction(ctx, cmd, parallelLevels, idleShips, response, relatedGoods, workerCap); err != nil {
		// Release all ship assignments on error
		h.releaseAllShipAssignments(ctx, cmd.ContainerID, cmd.PlayerID, "production_failed")
		return fmt.Errorf("parallel production failed: %w", err)
	}

	// sp-2q2o: executeLevelParallel's sp-vsfn catch-all parks (excludes from
	// results, doesn't abort the run) every node whose worker failed -
	// including "no claimable ship for %s %s" when the in-system fleet has
	// nothing free. If EVERY node parked this way, the run just completed
	// clean, fast, and having done nothing; flag it so Handle can back off
	// instead of the runner re-invoking instantly.
	if response.NodesTotal > 0 && response.NodesCompleted == 0 {
		response.NoWorkReason = "no nodes completed - every claimable node parked (no claimable in-system hull or worker failure)"
	}

	// Step 6: Release all ship assignments on successful completion
	if err := h.releaseAllShipAssignments(ctx, cmd.ContainerID, cmd.PlayerID, "factory_completed"); err != nil {
		logger.Log("WARNING", "Failed to release ship assignments", map[string]interface{}{
			"error": err.Error(),
		})
	}

	logger.Log("INFO", "Factory coordinator completed", map[string]interface{}{
		"factory_id":      response.FactoryID,
		"target_good":     cmd.TargetGood,
		"quantity":        response.QuantityAcquired,
		"total_cost":      response.TotalCost,
		"nodes_completed": response.NodesCompleted,
	})

	return nil
}

// treeGoodsList returns the good symbol for every node in a flattened supply
// chain tree - the target output plus every input at every level. It scopes
// the claim-time unrelated-cargo guard (filterUnrelatedCargo) to this
// factory's own production tree rather than just its top-level target good.
func treeGoodsList(nodes []*goods.SupplyChainNode) []string {
	list := make([]string, len(nodes))
	for i, node := range nodes {
		list[i] = node.Good
	}
	return list
}

// filterUnrelatedCargo excludes idle candidates from discovery whenever they
// hold cargo that has nothing to do with this factory's own production tree
// (sp-c07v). A hull left idle by some unrelated crashed/aborted worker - e.g.
// a stocker crash that leaves FOOD aboard - must never be claimed here:
// claiming it only spins zero-unit "hold full, could not unload existing
// cargo" BUY-task no-ops, since the factory has nowhere to put the input
// goods it buys and, by design, never jettisons a stranger's cargo to make
// room. The hull is simply left unclaimed - a fail-safe skip, never a dump.
//
// This mirrors the contract coordinator's own claim-time guard
// (contract.FilterUnrelatedCargo, sp-wq7r) rather than calling it directly:
// that helper's contract is built around a single required good (one
// contract run delivers one good), but a factory's dependency tree spans
// MULTIPLE goods at once - e.g. FAB_PLATE (the target) fed by IRON (an input
// two levels down) - and a hull pre-loaded with any one tree good is
// legitimate, already-useful cargo (see
// TestFactoryCoordinator_ParallelFabrication_DoesNotRepurchaseDeliveredInputs,
// which relies on exactly this: an idle hauler pre-loaded with an INPUT good
// must still be claimed normally). Narrowing the check to a single target
// good would wrongly park a hull mid-flight on a feed leg. relatedGoods is
// every good anywhere in the tree (treeGoodsList), so a hull is skipped only
// when its hold contains something genuinely foreign to this factory's own
// supply chain.
//
// Candidates already arrive as live *navigation.Ship values from
// FindIdleLightHaulers, so - unlike contract.FilterUnrelatedCargo - no
// second repository fetch is needed here; the cargo already in hand is
// checked directly.
func filterUnrelatedCargo(
	ctx context.Context,
	ships []*navigation.Ship,
	relatedGoods []string,
) ([]*navigation.Ship, []string) {
	if len(ships) == 0 {
		return ships, nil
	}

	related := make(map[string]bool, len(relatedGoods))
	for _, good := range relatedGoods {
		related[good] = true
	}

	logger := common.LoggerFromContext(ctx)
	claimable := make([]*navigation.Ship, 0, len(ships))
	claimableSymbols := make([]string, 0, len(ships))

	for _, ship := range ships {
		if held := unrelatedCargoItems(ship.Cargo(), related); len(held) > 0 {
			// sp-iqyq convention: the container-log renderer prints only
			// level+message and drops the metadata map, so ship/held-goods/reason
			// must be verbatim in the message itself, not just in metadata, for an
			// operator watching the log stream to see why the hull was skipped.
			logger.Log("INFO", fmt.Sprintf(
				"Skipped idle hull holding unrelated cargo - not claimed: ship=%s held_goods=%v reason=unrelated_cargo",
				ship.ShipSymbol(), held,
			), map[string]interface{}{
				"ship":       ship.ShipSymbol(),
				"held_goods": held,
				"reason":     "unrelated_cargo",
			})
			continue
		}
		claimable = append(claimable, ship)
		claimableSymbols = append(claimableSymbols, ship.ShipSymbol())
	}

	return claimable, claimableSymbols
}

// unrelatedCargoItems returns a "SYMBOL:UNITS" summary of every cargo item
// that is not in related. Empty cargo, or cargo made up entirely of related
// goods, returns nil (nothing foreign found).
func unrelatedCargoItems(cargo *shared.Cargo, related map[string]bool) []string {
	var held []string
	for _, item := range cargo.Inventory {
		if item.Units > 0 && !related[item.Symbol] {
			held = append(held, fmt.Sprintf("%s:%d", item.Symbol, item.Units))
		}
	}
	return held
}

// waitForIdleHaulers polls for idle light haulers, blocking until at least one
// is available or the context is cancelled.
//
// This is the fix for the "impatience crash" (sp-vmrj): the factory used to
// return a fatal "no idle hauler ships available for production" the instant it
// found every hauler busy at launch, killing an acceleration play on the pad. A
// factory meant to hold a market at MODERATE+ is long-lived, so waiting for the
// next idle gap — exactly what the fleet coordinator does when its pool is
// momentarily empty (run_fleet_coordinator.go) — is the correct behaviour. The
// wait is bounded only by context cancellation (container shutdown), never by a
// timeout, so a slow-to-free fleet can never re-introduce the crash.
//
// sp-c07v: candidates holding cargo unrelated to this factory's own
// production tree (relatedGoods) are filtered out before the idle-count
// check below, so they are never claimed in the first place - see
// filterUnrelatedCargo.
func (h *RunFactoryCoordinatorHandler) waitForIdleHaulers(
	ctx context.Context,
	playerID shared.PlayerID,
	systemSymbol string,
	relatedGoods []string,
	factoryID string,
) ([]*navigation.Ship, []string, error) {
	logger := common.LoggerFromContext(ctx)
	waited := false

	for {
		// Honour container shutdown between polls (mirrors the fleet
		// coordinator's top-of-loop cancellation check).
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		default:
		}

		// sp-qr3v: restrict the pool to hulls currently in the factory's own
		// system. Manufacturing never jumps cross-system, so an out-of-system
		// hull must be unselectable here (never claimed-then-failed).
		idleShips, idleShipSymbols, err := contract.FindIdleLightHaulers(ctx, playerID, h.shipRepo, systemSymbol)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to discover idle ships: %w", err)
		}

		// sp-c07v: NO-CARGO-DUMP claim guard, ported from the contract
		// coordinator (sp-wq7r) - skip, never claim, any hull holding cargo
		// foreign to this factory's tree.
		idleShips, idleShipSymbols = filterUnrelatedCargo(ctx, idleShips, relatedGoods)

		if len(idleShips) > 0 {
			if waited {
				logger.Log("INFO", "Idle hauler became available - resuming production", map[string]interface{}{
					"factory_id":   factoryID,
					"ship_count":   len(idleShips),
					"ship_symbols": idleShipSymbols,
				})
			}
			return idleShips, idleShipSymbols, nil
		}

		waited = true
		// Park-with-reason (sp-qr3v): the substance goes in the MESSAGE TEXT, not
		// only the metadata map - the CLI container-log renderer drops the map, so
		// naming the system there is what an operator actually sees. This is the
		// honest reason the factory is idle: no in-system hauler exists yet, NOT a
		// claim that keeps failing. It self-heals the moment the captain routes a
		// hauler into this system (the next poll claims it, zero operator action).
		logger.Log("INFO", fmt.Sprintf("No in-system worker (system=%s) - waiting for an idle in-system hauler before production", systemSymbol), map[string]interface{}{
			"factory_id":    factoryID,
			"system_symbol": systemSymbol,
			"poll_interval": shipDiscoveryInterval.String(),
		})
		// sp-l709: sleepInterruptibly (not a bare h.clock.Sleep) so a container
		// shutdown mid-poll is noticed the instant ctx is cancelled instead of up
		// to shipDiscoveryInterval (30s) late - the top-of-loop cancellation check
		// above then returns ctx.Err() on the very next iteration.
		h.sleepInterruptibly(ctx, shipDiscoveryInterval)
	}
}

// executeParallelProduction executes production levels in parallel
// Operation context is retrieved from ctx using shared.OperationContextFromContext if present
func (h *RunFactoryCoordinatorHandler) executeParallelProduction(
	ctx context.Context,
	cmd *RunFactoryCoordinatorCommand,
	levels []mfgServices.ParallelLevel,
	idleShips []*navigation.Ship,
	response *RunFactoryCoordinatorResponse,
	relatedGoods []string,
	workerCap int, // sp-ev0n: <=0 = unbounded; >0 bounds concurrent hulls to N this pass
) error {
	// Get operation context from context
	opContext := shared.OperationContextFromContext(ctx)
	logger := common.LoggerFromContext(ctx)
	shipsUsed := make(map[string]bool)
	var shipsUsedMutex sync.Mutex // Protect concurrent map access
	totalCost := 0
	nodesCompleted := 0

	// Capacity: 2× initial ships to accommodate dynamic discovery. Under a live worker
	// cap (sp-ev0n) the fan-out never runs more than workerCap node workers at once
	// (runWorkersBounded's semaphore), so only workerCap ships are ever in flight
	// concurrently — the surplus idle hulls stay unclaimed for the gate-drain/contract
	// fleets. The pool itself keeps its full 2× capacity so the background refresher can
	// still stage a replacement the instant an in-flight hull is released.
	shipPool := make(chan *navigation.Ship, len(idleShips)*shipPoolCapacityFactor)
	for _, ship := range idleShips {
		shipPool <- ship
	}

	// Launch background ship discoverer
	discoveryCtx, cancelDiscovery := context.WithCancel(ctx)
	defer cancelDiscovery()

	go h.shipPoolRefresher(discoveryCtx, cmd.PlayerID, cmd.SystemSymbol, relatedGoods, shipPool, shipsUsed, &shipsUsedMutex)

	logger.Log("INFO", "Starting parallel production", map[string]interface{}{
		"factory_id":         response.FactoryID,
		"levels":             len(levels),
		"available_ships":    len(idleShips),
		"discovery_enabled":  true,
		"discovery_interval": shipDiscoveryInterval.String(),
	})

	// Execute each level sequentially, but nodes within each level in parallel
	for levelIdx, level := range levels {
		logger.Log("INFO", "Starting parallel level", map[string]interface{}{
			"factory_id":  response.FactoryID,
			"level":       levelIdx,
			"depth":       level.Depth,
			"nodes_count": len(level.Nodes),
		})

		// Only the root level (the last, top-of-tree level) leaves its output in
		// factory stock under inputs-only; every lower level's output is an input to
		// the level above and must still be harvested + delivered.
		isRootLevel := levelIdx == len(levels)-1
		exec := levelExecution{
			cmd:                  cmd,
			shipPool:             shipPool,
			shipsUsed:            shipsUsed,
			shipsUsedMutex:       &shipsUsedMutex,
			deliveryDestinations: h.mapDeliveryDestinations(ctx, cmd, levels, levelIdx),
			opContext:            opContext,
			inputsOnly:           cmd.InputsOnly && isRootLevel,
			isRootLevel:          isRootLevel,
			workerCap:            workerCap,
		}

		// Execute all nodes in this level in parallel
		results, err := h.executeLevelParallel(ctx, exec, level.Nodes)
		if err != nil {
			return fmt.Errorf("level %d execution failed: %w", levelIdx, err)
		}

		// Aggregate results
		for _, result := range results {
			totalCost += result.TotalCost
			nodesCompleted++
		}

		logger.Log("INFO", "Parallel level completed", map[string]interface{}{
			"factory_id":      response.FactoryID,
			"level":           levelIdx,
			"nodes_completed": len(results),
			"level_cost":      sumCosts(results),
		})
	}

	// Update response
	response.TotalCost = totalCost
	response.NodesCompleted = nodesCompleted
	response.ShipsUsed = len(shipsUsed)

	// Find the root node's quantity
	if len(levels) > 0 {
		rootLevel := levels[len(levels)-1]
		if len(rootLevel.Nodes) > 0 {
			response.QuantityAcquired = rootLevel.Nodes[0].QuantityAcquired
		}
	}

	logger.Log("INFO", "Parallel production completed", map[string]interface{}{
		"factory_id":       response.FactoryID,
		"total_cost":       totalCost,
		"ships_used":       len(shipsUsed),
		"ships_discovered": len(shipsUsed) - len(idleShips),
		"nodes_completed":  nodesCompleted,
	})

	return nil
}

func (h *RunFactoryCoordinatorHandler) mapDeliveryDestinations(
	ctx context.Context,
	cmd *RunFactoryCoordinatorCommand,
	levels []mfgServices.ParallelLevel,
	levelIdx int,
) map[string]string {
	logger := common.LoggerFromContext(ctx)
	deliveryDestinations := make(map[string]string) // good -> import waypoint
	if levelIdx+1 >= len(levels) {
		return deliveryDestinations
	}

	nextLevel := levels[levelIdx+1]
	for _, fabricNode := range nextLevel.Nodes {
		if fabricNode.AcquisitionMethod != goods.AcquisitionFabricate {
			continue
		}
		exportMarket, err := h.marketLocator.FindExportMarket(ctx, fabricNode.Good, cmd.SystemSymbol, cmd.PlayerID)
		if err != nil {
			logger.Log("WARNING", "Could not find export market for fabrication destination", map[string]interface{}{
				"good":  fabricNode.Good,
				"error": err.Error(),
			})
			continue
		}
		for _, child := range fabricNode.Children {
			deliveryDestinations[child.Good] = exportMarket.WaypointSymbol
			logger.Log("INFO", "Mapped delivery destination for input", map[string]interface{}{
				"input_good":         child.Good,
				"destination":        exportMarket.WaypointSymbol,
				"fabrication_target": fabricNode.Good,
			})
		}
	}
	return deliveryDestinations
}

// shipPoolRefresher runs a background goroutine that periodically discovers new idle ships
// and adds them to the ship pool, allowing blocked workers to acquire ships mid-execution.
//
// Discovery process:
// 1. Poll every 30 seconds for newly idle ships
// 2. Filter out ships already in the pool (via shipsUsed map)
// 3. Attempt non-blocking send to shipPool (skip if full)
// 4. Log newly added ships for observability
// 5. Exit gracefully on context cancellation
//
// Thread safety:
// - shipsUsed map: protected by mutex for concurrent access
// - shipPool channel: Go channels are concurrency-safe
func (h *RunFactoryCoordinatorHandler) shipPoolRefresher(
	ctx context.Context,
	playerID int,
	systemSymbol string,
	relatedGoods []string,
	shipPool chan *navigation.Ship,
	shipsUsed map[string]bool,
	shipsUsedMutex *sync.Mutex,
) {
	logger := common.LoggerFromContext(ctx)
	ticker := time.NewTicker(shipDiscoveryInterval)
	defer ticker.Stop()

	playerIDValue := shared.MustNewPlayerID(playerID)
	discoveryCount := 0

	logger.Log("INFO", "Ship pool refresher started", map[string]interface{}{
		"interval": shipDiscoveryInterval.String(),
	})

	for {
		select {
		case <-ctx.Done():
			logger.Log("INFO", "Ship pool refresher stopped", map[string]interface{}{
				"total_discoveries": discoveryCount,
			})
			return

		case <-ticker.C:
			discoveryCount = h.refreshShipPoolOnce(ctx, playerIDValue, systemSymbol, relatedGoods, shipPool, shipsUsed, shipsUsedMutex, discoveryCount)
		}
	}
}

// refreshShipPoolOnce runs a single ship-pool discovery tick: the body of
// shipPoolRefresher's ticker case, extracted so it can be driven directly by
// tests without waiting on the real 30-second ticker. Returns the updated
// discoveryCount (unchanged if the discovery call itself failed or found
// nothing to add).
//
// sp-npyr: contract.FindIdleLightHaulers logs "Idle light haulers discovered"
// unconditionally on every call, fleet-wide, whether or not any of those
// ships are new capacity for THIS factory run. Once a run's initial ships are
// claimed, every subsequent tick re-finds the same already-tracked idle
// haulers (they stay DB-idle until a worker actually pulls them off shipPool
// and claims them — see claimShipForFactory) and silently adds nothing,
// reading from the outside as unexplained 30s-cadence noise with zero
// followup. The `else if` branch below names that steady state explicitly.
func (h *RunFactoryCoordinatorHandler) refreshShipPoolOnce(
	ctx context.Context,
	playerIDValue shared.PlayerID,
	systemSymbol string,
	relatedGoods []string,
	shipPool chan *navigation.Ship,
	shipsUsed map[string]bool,
	shipsUsedMutex *sync.Mutex,
	discoveryCount int,
) int {
	logger := common.LoggerFromContext(ctx)

	// Re-discover idle ships. sp-qr3v: the mid-run refresh filters to the
	// factory's own system too, so a hull that drifts idle in another system is
	// never added to the pool and then claimed-then-failed.
	newIdleShips, _, err := contract.FindIdleLightHaulers(
		ctx,
		playerIDValue,
		h.shipRepo,
		systemSymbol,
	)
	if err != nil {
		logger.Log("WARNING", "Failed to refresh ship pool", map[string]interface{}{
			"error": err.Error(),
		})
		return discoveryCount
	}

	// sp-c07v: NO-CARGO-DUMP claim guard - a hull holding cargo foreign to
	// this factory's tree is skipped here too, so a mid-run discovery tick
	// can't add it to the pool any more than the initial discovery could.
	newIdleShips, _ = filterUnrelatedCargo(ctx, newIdleShips, relatedGoods)

	// Add newly discovered ships to pool (non-blocking)
	addedCount := 0
	addedShips := make([]string, 0)

	for _, ship := range newIdleShips {
		// Skip if ship already in use by this factory (check with lock)
		shipsUsedMutex.Lock()
		alreadyUsed := shipsUsed[ship.ShipSymbol()]
		shipsUsedMutex.Unlock()

		if alreadyUsed {
			continue
		}

		// Attempt non-blocking send to pool
		select {
		case shipPool <- ship:
			shipsUsedMutex.Lock()
			shipsUsed[ship.ShipSymbol()] = true
			shipsUsedMutex.Unlock()
			addedShips = append(addedShips, ship.ShipSymbol())
			addedCount++
		default:
			// Channel full, skip this ship
			// Will retry on next tick if ship still idle
		}
	}

	if addedCount > 0 {
		discoveryCount += addedCount
		logger.Log("INFO", "Added new ships to pool", map[string]interface{}{
			"added_count":        addedCount,
			"added_ships":        addedShips,
			"total_discoveries":  discoveryCount,
			"pool_capacity_used": fmt.Sprintf("%d/%d", len(shipsUsed), cap(shipPool)),
		})
	} else if len(newIdleShips) > 0 {
		logger.Log("INFO", "Idle haulers found but already claimed for this run - no new capacity added", map[string]interface{}{
			"idle_count": len(newIdleShips),
		})
	}

	return discoveryCount
}

type levelExecution struct {
	cmd                  *RunFactoryCoordinatorCommand
	shipPool             chan *navigation.Ship
	shipsUsed            map[string]bool
	shipsUsedMutex       *sync.Mutex
	deliveryDestinations map[string]string
	opContext            *shared.OperationContext
	// inputsOnly is set only for the root level (the target good). An intermediate
	// fabricated good must still be harvested so it can be delivered to the level
	// above it — only the terminal output is left in factory stock (sp-q02m).
	inputsOnly bool
	// isRootLevel marks the top-of-tree level whose node is the terminal product.
	// On a resale run (not inputsOnly) that product's harvested output is flown to
	// the guard's resale sink and sold there (sp-rqwm) — intermediate levels' output
	// is a feed for the level above and is never resold at an import sink.
	isRootLevel bool
	// workerCap bounds concurrent node workers in this level (sp-ev0n): runWorkersBounded
	// never runs more than workerCap goroutines at once, so at most workerCap hulls are in
	// flight. <=0 = unbounded (one worker per node, the pre-fix fan-out). Levels run
	// sequentially, so bounding each level bounds the instantaneous concurrent-hull count.
	workerCap int
}

// executeLevelParallel executes all nodes in a level in parallel using goroutines
func (h *RunFactoryCoordinatorHandler) executeLevelParallel(
	ctx context.Context,
	exec levelExecution,
	nodes []*goods.SupplyChainNode,
) ([]*mfgServices.ProductionResult, error) {
	// The per-node worker: pull a hull from the shared pool (blocking until one is
	// free), run the node, and return the hull. runWorkersBounded gates how many of
	// these run at once (exec.workerCap, sp-ev0n) — the ship-pull stays inside the
	// gated region so a hull is held only while a concurrency slot is.
	return h.runWorkersBounded(ctx, exec.workerCap, nodes, func(ctx context.Context, n *goods.SupplyChainNode) (*mfgServices.ProductionResult, error) {
		ship := <-exec.shipPool
		defer func() { exec.shipPool <- ship }() // Return ship to pool
		return h.runNodeWorker(ctx, exec, n, ship)
	})
}

// nodeWorkerResult carries one node worker's outcome back to the collector in
// runWorkersBounded.
type nodeWorkerResult struct {
	result *mfgServices.ProductionResult
	err    error
	node   *goods.SupplyChainNode
}

// nodeWorker produces one node using one hull. executeLevelParallel supplies the
// real worker (pull a ship, run the node); tests supply a fake so the concurrency
// bound can be exercised in isolation without the full production stack.
type nodeWorker func(ctx context.Context, n *goods.SupplyChainNode) (*mfgServices.ProductionResult, error)

// runWorkersBounded runs one worker goroutine per node while never letting more
// than workerCap run CONCURRENTLY — the sp-ev0n concurrent-hull bound. workerCap<=0
// is unbounded (one worker per node, the pre-fix emergent fan-out of
// min(nodes, idle hulls)); workerCap>0 caps in-flight workers — and therefore
// in-flight hulls — at N via a buffered semaphore acquired before the worker runs
// and released after. Because the coordinator re-resolves workerCap fresh each pass
// (resolveEffectiveWorkerCap), a lowered cap converges the fan-out to the new N on
// the very next pass (a hull already mid-node finishes first, never force-killed);
// a raised cap scales the fan-out back up the next pass.
//
// The sp-vsfn park-vs-abort policy is preserved verbatim from the pre-extraction
// executeLevelParallel: a worker error PARKS that node (excluded from results, run
// continues) UNLESS it is a container-shutdown signal (context cancel/deadline), the
// one case that aborts the run rather than misreporting a killed run as a clean
// partial success. A ctx cancellation while a worker waits for a semaphore slot
// yields exactly that shutdown-signal result, so a live shutdown aborts cleanly
// instead of hanging on a full semaphore.
func (h *RunFactoryCoordinatorHandler) runWorkersBounded(
	ctx context.Context,
	workerCap int,
	nodes []*goods.SupplyChainNode,
	worker nodeWorker,
) ([]*mfgServices.ProductionResult, error) {
	logger := common.LoggerFromContext(ctx)
	resultChan := make(chan nodeWorkerResult, len(nodes))

	// sp-ev0n: a buffered semaphore of workerCap slots bounds concurrency. nil (cap
	// <=0) disables the bound entirely, so the unbounded path is byte-for-byte the
	// old fan-out.
	var sem chan struct{}
	if workerCap > 0 {
		sem = make(chan struct{}, workerCap)
	}

	var wg sync.WaitGroup
	for _, node := range nodes {
		wg.Add(1)
		go func(n *goods.SupplyChainNode) {
			defer wg.Done()

			if sem != nil {
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					// Shutdown before a slot freed: report it as a shutdown signal so
					// the collector aborts (never a silent park), and never pull a hull.
					resultChan <- nodeWorkerResult{err: ctx.Err(), node: n}
					return
				}
				defer func() { <-sem }()
			}

			result, err := worker(ctx, n)

			resultChan <- nodeWorkerResult{
				result: result,
				err:    err,
				node:   n,
			}

			if err != nil {
				var refuelErr *shipapp.ErrRefuelUnrecoverable
				var arrivalErr *shipapp.ErrArrivalWaitExhausted
				var cargoErr *goods.ErrInsufficientCargo
				switch {
				case errors.As(err, &refuelErr):
					// sp-vsfn: this ship's refuel retry/reroute budget is
					// exhausted. The collection loop below parks this node
					// (does not abort the level/run) rather than crashing —
					// this branch only makes that specific cause visible in
					// the log message text instead of the opaque generic
					// "Worker failed", so a transient refuel exhaustion
					// (self-resolves once the ship is re-claimed on a later
					// run) is distinguishable at a glance from a genuine
					// production bug (sp-npyr).
					logger.Log("WARNING", fmt.Sprintf("Worker parked on unrecoverable refuel failure: ship %s at %s after %d attempt(s)", refuelErr.ShipSymbol, refuelErr.Waypoint, refuelErr.Attempts), map[string]interface{}{
						"good":        n.Good,
						"ship_symbol": refuelErr.ShipSymbol,
						"waypoint":    refuelErr.Waypoint,
						"attempts":    refuelErr.Attempts,
						"error":       err.Error(),
					})
				case errors.As(err, &arrivalErr):
					// sp-vsfn: the ship's arrival wait gave up — the ARRIVED
					// event never arrived and repeated resyncs kept showing
					// IN_TRANSIT. Parked, not crashed: a later run re-syncs
					// against the ship repository and retries.
					logger.Log("WARNING", fmt.Sprintf("Worker parked on arrival wait exhaustion: ship %s after %d attempt(s)", arrivalErr.ShipSymbol, arrivalErr.Attempts), map[string]interface{}{
						"good":        n.Good,
						"ship_symbol": arrivalErr.ShipSymbol,
						"attempts":    arrivalErr.Attempts,
						"error":       err.Error(),
					})
				case errors.As(err, &cargoErr):
					// sp-vsfn: the ship couldn't hold the required goods.
					// Parked, not crashed: a later run may claim a hull with
					// more free space, or this hull's hold may have cleared.
					logger.Log("WARNING", fmt.Sprintf("Worker parked on insufficient cargo space: ship %s needs %d, has %d", cargoErr.ShipSymbol, cargoErr.RequiredSpace, cargoErr.AvailableSpace), map[string]interface{}{
						"good":            n.Good,
						"ship_symbol":     cargoErr.ShipSymbol,
						"required_space":  cargoErr.RequiredSpace,
						"available_space": cargoErr.AvailableSpace,
						"error":           err.Error(),
					})
				default:
					// sp-vsfn catch-all: any OTHER worker error — including
					// novel, not-yet-classified transients such as the
					// orbit-while-in-transit 400/4214 race that reopened this
					// issue — still PARKS this node rather than crashing the
					// whole run (see the collection loop below). It keeps the
					// generic ERROR level/message so it stays visible,
					// greppable and alertable as a candidate for its own
					// classified branch later, instead of silently blending
					// into the WARNING-level "known and expected" causes
					// above.
					logger.Log("ERROR", "Worker failed", map[string]interface{}{
						"good":  n.Good,
						"error": err.Error(),
					})
				}
			} else {
				logger.Log("INFO", fmt.Sprintf("Worker completed: %s %s (%d units, %d credits)", n.AcquisitionMethod, n.Good, result.QuantityAcquired, result.TotalCost), map[string]interface{}{
					"good":     n.Good,
					"quantity": result.QuantityAcquired,
					"cost":     result.TotalCost,
				})
			}
		}(node)
	}

	// Wait for all workers to complete
	go func() {
		wg.Wait()
		close(resultChan)
	}()

	// Collect results. sp-vsfn (catch-all park+resume): a worker error PARKS
	// that node — excluded from results, but does NOT abort the level or the
	// run — UNLESS the error is a container shutdown signal (context
	// cancellation/deadline), the one case where silently continuing would
	// misreport a killed run as a clean, partially-completed success. This
	// treats "everything except shutdown" as parkable rather than matching a
	// fixed allow-list of known transient error types: the prior allow-list
	// -style fix (refuel only) was reopened the moment a new, unclassified
	// trigger (orbit-while-in-transit 400/4214) appeared, so a deny-list of
	// exactly the one case that must NOT be parked is the design that doesn't
	// need a follow-up patch for the next new trigger.
	results := make([]*mfgServices.ProductionResult, 0, len(nodes))
	var firstError error

	for wr := range resultChan {
		if wr.err != nil {
			if isContainerShutdownSignal(wr.err) && firstError == nil {
				firstError = wr.err
			}
		}
		if wr.result != nil {
			results = append(results, wr.result)
			// Mark node as completed
			wr.node.MarkCompleted(wr.result.QuantityAcquired)
		}
	}

	if firstError != nil {
		return nil, firstError
	}

	return results, nil
}

// isContainerShutdownSignal reports whether err is a context
// cancellation/deadline signal — i.e. the container itself is shutting down
// — as opposed to a transient worker failure. executeLevelParallel parks
// (does not abort the run for) every worker error EXCEPT this one:
// continuing past a genuine shutdown signal would misreport a killed run as
// a clean, partially-completed success (sp-vsfn).
func isContainerShutdownSignal(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (h *RunFactoryCoordinatorHandler) runNodeWorker(
	ctx context.Context,
	exec levelExecution,
	n *goods.SupplyChainNode,
	ship *navigation.Ship,
) (*mfgServices.ProductionResult, error) {
	logger := common.LoggerFromContext(ctx)

	// A worker goroutine must never SIGSEGV the daemon. Guard a nil hull before
	// any deref (detectOnboardCargo/logging touch ship immediately) so a
	// degenerate pool entry fails this node cleanly instead of panicking the
	// whole fleet (sp-bt6o).
	if ship == nil {
		logger.Log("WARNING", fmt.Sprintf("Skipping %s %s - nil ship from pool", n.AcquisitionMethod, n.Good), map[string]interface{}{
			"good":   n.Good,
			"method": n.AcquisitionMethod,
		})
		return nil, fmt.Errorf("nil ship for %s %s", n.AcquisitionMethod, n.Good)
	}

	hasNeededCargo := h.detectOnboardCargo(ctx, n, ship)

	if !hasNeededCargo {
		logger.Log("INFO", fmt.Sprintf("Worker starting: %s %s using ship %s", n.AcquisitionMethod, n.Good, ship.ShipSymbol()), map[string]interface{}{
			"good":        n.Good,
			"ship_symbol": ship.ShipSymbol(),
			"method":      n.AcquisitionMethod,
		})
	} else {
		logger.Log("INFO", fmt.Sprintf("Worker starting: DELIVER %s using ship %s (cargo already onboard)", n.Good, ship.ShipSymbol()), map[string]interface{}{
			"good":        n.Good,
			"ship_symbol": ship.ShipSymbol(),
			"method":      "DELIVER",
		})
	}

	if !h.claimShipForFactory(ctx, exec.cmd.ContainerID, ship, exec.shipsUsed, exec.shipsUsedMutex) {
		// The pulled hull was nil or no longer claimable (grabbed by another
		// coordinator since discovery). Fail this node cleanly rather than
		// operating an unclaimed ship; the factory degrades to a relaunchable
		// failure, never a daemon panic (sp-bt6o).
		return nil, fmt.Errorf("no claimable ship for %s %s", n.AcquisitionMethod, n.Good)
	}

	deliveryDest := exec.deliveryDestinations[n.Good]

	if hasNeededCargo && deliveryDest != "" {
		return h.deliverExistingCargo(ctx, ship, n.Good, deliveryDest, exec.cmd.PlayerID)
	}
	return h.produceNodeOnly(ctx, ship, n, exec.cmd.SystemSymbol, exec.cmd.PlayerID, deliveryDest, exec.opContext, exec.inputsOnly, exec.isRootLevel, exec.cmd.PlannerStockDisabled)
}

func (h *RunFactoryCoordinatorHandler) detectOnboardCargo(ctx context.Context, n *goods.SupplyChainNode, ship *navigation.Ship) bool {
	if !n.IsLeaf() || n.AcquisitionMethod != goods.AcquisitionBuy {
		return false
	}
	logger := common.LoggerFromContext(ctx)
	for _, item := range ship.Cargo().Inventory {
		if item.Symbol == n.Good && item.Units > 0 {
			logger.Log("INFO", fmt.Sprintf("Ship %s already has %d units of %s - will deliver directly", ship.ShipSymbol(), item.Units, n.Good), map[string]interface{}{
				"ship_symbol": ship.ShipSymbol(),
				"good":        n.Good,
				"units":       item.Units,
			})
			return true
		}
	}
	return false
}

// operationManufacturing is the factory coordinator's fleet identity for the
// atomic ClaimShip dedication check (sp-l7h2 Phase 2). The goods factory is
// part of the manufacturing family, so it claims under the same operation
// name the manufacturing task workers use (worker_lifecycle_manager.go's
// package-local constant of the same name and value): a hull the captain
// pins with `fleet assign --fleet manufacturing` is claimable by both, and
// by nothing else.
const operationManufacturing = "manufacturing"

// claimShipForFactory attempts to claim a ship for the factory container and
// reports whether the ship is usable by the caller.
//
// It must NEVER panic the daemon. A nil ship, or one that is no longer idle at
// claim time (another coordinator grabbed it since discovery — a stale-snapshot
// TOCTOU), is skipped with a WARNING and reported unclaimable, so a bad claim
// degrades to a skipped node instead of SIGSEGV'ing the whole fleet (sp-bt6o).
//
// The claim write itself is ShipRepository.ClaimShip (sp-l7h2 Phase 2): a
// row-locked transaction that re-checks assignment AND fleet dedication
// atomically, so a hull pinned to another fleet — or grabbed by another
// coordinator after the in-memory checks below — is rejected at the DB, not
// clobbered. The IsIdle check below stays as the cheap layer-1 pre-filter;
// ClaimShip is the layer-2 guarantee.
func (h *RunFactoryCoordinatorHandler) claimShipForFactory(
	ctx context.Context,
	containerID string,
	ship *navigation.Ship,
	shipsUsed map[string]bool,
	shipsUsedMutex *sync.Mutex,
) bool {
	logger := common.LoggerFromContext(ctx)

	// Defense-in-depth: a nil ship must never reach AssignToContainer.
	if ship == nil {
		logger.Log("WARNING", "Skipping nil ship in factory claim path", nil)
		return false
	}

	shipsUsedMutex.Lock()
	alreadyClaimed := shipsUsed[ship.ShipSymbol()]
	shipsUsedMutex.Unlock()

	// Idempotent across parallel levels: a ship this factory already claimed is
	// still ours to use.
	if alreadyClaimed {
		return true
	}

	// Re-validate claimability at claim time rather than trusting the discovery
	// snapshot. A ship now owned by another container must be skipped, not
	// clobbered (mirrors how the fleet/mfg coordinators reject a non-idle ship).
	if !ship.IsIdle() {
		logger.Log("WARNING", "Skipping ship no longer idle at claim time", map[string]interface{}{
			"ship_symbol":  ship.ShipSymbol(),
			"container_id": ship.ContainerID(),
		})
		return false
	}

	// Atomic claim (sp-l7h2 Phase 2): assignment + dedication are re-checked
	// inside ClaimShip's row-locked transaction, replacing the old read-modify-
	// write AssignToContainer+Save whose TOCTOU let the factory clobber claims
	// and poach fleet-dedicated hulls.
	if err := h.shipRepo.ClaimShip(ctx, ship.ShipSymbol(), containerID, ship.PlayerID(), operationManufacturing); err != nil {
		logger.Log("WARNING", "Failed to claim ship for factory", map[string]interface{}{
			"ship_symbol": ship.ShipSymbol(),
			"error":       err.Error(),
		})
		return false
	}

	// Update the Ship domain entity for in-memory consistency (the DB claim is
	// already committed). Mirrors the manufacturing worker path: a sync failure
	// here is a WARN, not an unclaim — returning false would orphan the DB
	// claim with no holder ever releasing it.
	if err := ship.AssignToContainer(containerID, h.clock); err != nil {
		logger.Log("WARNING", "Failed to update ship domain entity (DB claim already committed)", map[string]interface{}{
			"ship_symbol": ship.ShipSymbol(),
			"error":       err.Error(),
		})
	}

	shipsUsedMutex.Lock()
	shipsUsed[ship.ShipSymbol()] = true
	shipsUsedMutex.Unlock()

	logger.Log("INFO", "Ship assigned to factory", map[string]interface{}{
		"ship_symbol":  ship.ShipSymbol(),
		"container_id": containerID,
	})
	return true
}

// produceNodeOnly produces a single node without recursing into children
// This is used for parallel execution where children are already produced
func (h *RunFactoryCoordinatorHandler) produceNodeOnly(
	ctx context.Context,
	ship *navigation.Ship,
	node *goods.SupplyChainNode,
	systemSymbol string,
	playerID int,
	deliveryDest string,
	opContext *shared.OperationContext, // Operation context for transaction linking
	inputsOnly bool, // when true (root level only), the fabricated output is left in factory stock
	isRootLevel bool, // when true, the terminal product's output is sold at the guard's resale sink (sp-rqwm)
	plannerStockDisabled bool, // C1 (sp-64je) escape hatch: when true, force the pre-C1 sell-at-market path; LIVE (deposit) by default
) (*mfgServices.ProductionResult, error) {
	logger := common.LoggerFromContext(ctx)

	// For leaf nodes (BUY), purchase and optionally deliver. A leaf is an input buy,
	// so it is always harvested (inputs-only never suppresses input acquisition).
	if node.IsLeaf() {
		// First, buy the goods
		result, err := h.productionExecutor.ProduceGood(ctx, ship, node, systemSymbol, playerID, opContext, false)
		if err != nil {
			return nil, err
		}

		// If there's a delivery destination, deliver the cargo there — but only if the
		// buy actually acquired units. An empty-tranche skip (sp-q02m crash #4) yields a
		// 0-unit result; attempting to deliver nothing errors in deliverCargo ("no X in
		// cargo") and would re-crash the very run we just kept alive, so we skip delivery
		// and let the node complete with zero units.
		if deliveryDest != "" && result.QuantityAcquired > 0 {
			// sp-9mkf (Bug 1) same-waypoint guard: a feed whose buy waypoint equals its
			// delivery destination is a guaranteed round-trip loss — we would sell it back
			// at the same market's (lower) bid we just bought it from at the (higher) ask.
			// The root cause (sourcing a feed from an IMPORT market, i.e. the factory
			// itself) is fixed in FindExportMarket, so this can no longer arise for the
			// importer case; this is the fail-closed backstop. It REFUSES the same-waypoint
			// delivery sell and holds the cargo rather than dump it at a loss, logged loudly
			// so any recurrence surfaces immediately.
			if deliveryDest == result.WaypointSymbol {
				logger.Log("WARNING", fmt.Sprintf("Refusing same-waypoint delivery of %d units of %s at %s — feed was bought here; selling it back is a round-trip loss. Holding cargo.", result.QuantityAcquired, node.Good, deliveryDest), map[string]interface{}{
					"good":     node.Good,
					"waypoint": deliveryDest,
					"units":    result.QuantityAcquired,
					"action":   "same_waypoint_delivery_refused",
				})
				return result, nil
			}

			logger.Log("INFO", fmt.Sprintf("Delivering %d units of %s to %s", result.QuantityAcquired, node.Good, deliveryDest), map[string]interface{}{
				"good":        node.Good,
				"quantity":    result.QuantityAcquired,
				"destination": deliveryDest,
			})

			deliveryResult, err := h.deliverCargo(ctx, ship.ShipSymbol(), node.Good, deliveryDest, playerID)
			if err != nil {
				return nil, fmt.Errorf("failed to deliver %s to %s: %w", node.Good, deliveryDest, err)
			}

			// Update result with delivery revenue (negative cost)
			result.TotalCost -= deliveryResult.TotalRevenue
			result.WaypointSymbol = deliveryDest

			logger.Log("INFO", fmt.Sprintf("Delivered %d units of %s for %d credits revenue", deliveryResult.UnitsSold, node.Good, deliveryResult.TotalRevenue), map[string]interface{}{
				"good":     node.Good,
				"units":    deliveryResult.UnitsSold,
				"revenue":  deliveryResult.TotalRevenue,
				"location": deliveryDest,
			})
		}

		return result, nil
	}

	// For fabrication nodes, we assume children are already completed
	// Just deliver inputs and purchase output
	playerIDValue := shared.MustNewPlayerID(playerID)

	// Find manufacturing waypoint (market that exports/produces this good)
	exportMarket, err := h.marketLocator.FindExportMarket(ctx, node.Good, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find export market for %s: %w", node.Good, err)
	}

	logger.Log("INFO", "Found manufacturing waypoint for parallel node", map[string]interface{}{
		"good":     node.Good,
		"waypoint": exportMarket.WaypointSymbol,
	})

	// Navigate and dock at manufacturing waypoint
	updatedShip, err := h.productionExecutor.NavigateAndDock(ctx, ship.ShipSymbol(), exportMarket.WaypointSymbol, playerIDValue)
	if err != nil {
		return nil, fmt.Errorf("failed to navigate to manufacturing waypoint: %w", err)
	}

	// Inputs were already bought and delivered to the factory by the child
	// workers of the previous parallel level - only verify completion here.
	totalCost := 0
	for _, child := range node.Children {
		if !child.Completed {
			return nil, fmt.Errorf("child %s not completed before fabrication", child.Good)
		}
	}

	// Poll for production and purchase output. In inputs-only mode (root level only)
	// the poll confirms the output was fabricated but the harvest is skipped, leaving
	// the good in factory stock for a construction pipeline to source (sp-q02m).
	quantity, cost, err := h.productionExecutor.PollForProduction(ctx, node.Good, exportMarket.WaypointSymbol, updatedShip.ShipSymbol(), playerIDValue, opContext, inputsOnly, systemSymbol)
	if err != nil {
		return nil, err
	}

	totalCost += cost

	// sp-rqwm: BIND the output sale to the guard's resale sink. Only the terminal
	// product (root level, resale run) is flown to the import sink the chain-margin
	// guard priced and sold THERE — never dumped at the factory/buy market. The basis
	// for the bid>=basis loss floor is the factory ask we paid to harvest
	// (exportMarket.Price). Intermediate feeds are delivered to their parent fab and
	// inputs-only leaves output in factory stock, so both skip this leg. A sink below
	// the floor (or none) HOLDS the output onboard rather than dumping it.
	if isRootLevel && !inputsOnly {
		// C1 (sp-64je): planner-visible stock, LIVE BY DEFAULT. Unless the escape hatch
		// is set (and when wired), deposit the harvested output into a co-located
		// warehouse at cost basis (exportMarket.Price, the factory ask we paid to harvest)
		// so the tour solver withdraws it at basis instead of buying our own output at
		// laddered asks. Fails SAFE — any decline or error falls through to the resale
		// sell below (unchanged behavior).
		if !plannerStockDisabled && h.plannerStock != nil {
			deposited, depErr := h.plannerStock.DepositOutput(
				ctx, playerID, updatedShip.ShipSymbol(), exportMarket.WaypointSymbol, node.Good, quantity, exportMarket.Price,
			)
			if depErr != nil {
				logger.Log("WARN", fmt.Sprintf("planner-stock deposit of %s failed, selling at resale sink instead: %v", node.Good, depErr), map[string]interface{}{
					"good": node.Good, "quantity": quantity, "basis": exportMarket.Price,
				})
			} else if deposited {
				// Stocked at basis: capital is carried as stock (no market sale), realized
				// later when a tour withdraws. totalCost keeps the harvest cost (no revenue
				// offset), so chain P&L still sees the outlay.
				return &mfgServices.ProductionResult{
					QuantityAcquired: quantity,
					TotalCost:        totalCost,
					WaypointSymbol:   exportMarket.WaypointSymbol,
				}, nil
			}
		}

		revenue, sellErr := h.productionExecutor.SellFabricatedOutputAtSink(
			ctx, updatedShip.ShipSymbol(), node.Good, exportMarket.Price, systemSymbol, playerIDValue, opContext,
		)
		if sellErr != nil {
			return nil, fmt.Errorf("failed to sell fabricated %s at resale sink: %w", node.Good, sellErr)
		}
		totalCost -= revenue
	}

	return &mfgServices.ProductionResult{
		QuantityAcquired: quantity,
		TotalCost:        totalCost,
		WaypointSymbol:   exportMarket.WaypointSymbol,
	}, nil
}

// sumCosts sums the total cost from a list of production results
func sumCosts(results []*mfgServices.ProductionResult) int {
	total := 0
	for _, r := range results {
		total += r.TotalCost
	}
	return total
}

// buildDependencyTree builds the supply chain dependency tree
func (h *RunFactoryCoordinatorHandler) buildDependencyTree(
	ctx context.Context,
	cmd *RunFactoryCoordinatorCommand,
) (*goods.SupplyChainNode, error) {
	playerID := cmd.PlayerID
	systemSymbol := cmd.SystemSymbol

	// Use SupplyChainResolver to build tree
	tree, err := h.resolver.BuildDependencyTree(
		ctx,
		cmd.TargetGood,
		systemSymbol,
		playerID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve supply chain: %w", err)
	}

	return tree, nil
}

// generateFactoryID generates a unique factory ID using UUID
func generateFactoryID() string {
	return "factory-" + uuid.New().String()
}

// countNodesByMethod counts nodes by acquisition method
func countNodesByMethod(nodes []*goods.SupplyChainNode, method goods.AcquisitionMethod) int {
	count := 0
	for _, node := range nodes {
		if node.AcquisitionMethod == method {
			count++
		}
	}
	return count
}

// releaseAllShipAssignments releases all ship assignments for the factory container
func (h *RunFactoryCoordinatorHandler) releaseAllShipAssignments(
	ctx context.Context,
	containerID string,
	playerID int,
	reason string,
) error {
	logger := common.LoggerFromContext(ctx)

	// Find all ships assigned to this container
	playerIDValue := shared.MustNewPlayerID(playerID)
	assignedShips, err := h.shipRepo.FindByContainer(ctx, containerID, playerIDValue)
	if err != nil {
		logger.Log("ERROR", "Failed to find ship assignments", map[string]interface{}{
			"container_id": containerID,
			"error":        err.Error(),
		})
		return err
	}

	// Release each ship using Ship aggregate pattern
	for _, ship := range assignedShips {
		ship.ForceRelease(reason, h.clock)
		if err := h.shipRepo.Save(ctx, ship); err != nil {
			logger.Log("WARNING", "Failed to release ship assignment", map[string]interface{}{
				"ship_symbol":  ship.ShipSymbol(),
				"container_id": containerID,
				"error":        err.Error(),
			})
		}
	}

	logger.Log("INFO", "Released all ship assignments", map[string]interface{}{
		"container_id": containerID,
		"ships_count":  len(assignedShips),
		"reason":       reason,
	})

	return nil
}

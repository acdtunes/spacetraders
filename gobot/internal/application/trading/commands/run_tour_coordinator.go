package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	gasCmd "github.com/andrescamacho/spacetraders-go/internal/application/gas/commands"
	"github.com/andrescamacho/spacetraders-go/internal/application/system/gategraph"
	tradingsvc "github.com/andrescamacho/spacetraders-go/internal/application/trading/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/absorption"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

const (
	// tourPriceTolerancePct is the live-vs-planned price gate: a trade whose live
	// price has moved more than this from the planner's projection is skipped and
	// triggers a re-plan (matches the graduation-gate ±15% metric).
	tourPriceTolerancePct = 15
	// tourMaxReplansDefault bounds re-plans per tour when the captain leaves
	// --replan-limit at 0.
	tourMaxReplansDefault = 2
	// maxTourHops / maxTourSystems bound the planner's search (spec: ≤6 hops,
	// ≤2 gate-adjacent systems). The planner enforces the system cap; the executor
	// caps hops in the constraint it sends.
	maxTourHops    = 6
	maxTourSystems = 2
	// unreachableLaneReason labels the sp-mtvg drop counter: a good with a cheap source
	// IN the tour graph but its best sink in a system OUTSIDE it (>1 gate hop away), so
	// source and sink never co-occur in one snapshot and the solver can never plan the
	// lane. This is the "exotic good-level blind spot" the diagnostic makes loud.
	unreachableLaneReason = "counterparty_system_unreachable"
	// unreachableLaneMinSpreadPerUnit gates the diagnostic to materially profitable
	// lanes: the observed exotic misses run 14k–37k/u (LASER_RIFLES 14,078; HOLOGRAPHICS
	// ~19,800; QUANTUM_DRIVES ~37,000), so a 5k floor captures that class while filtering
	// routine sub-5k cross-map spreads that would only add noise. A tuning knob, not a
	// trade gate — it only decides what the observation counts.
	unreachableLaneMinSpreadPerUnit = 5000
	// unreachableLaneLogTopN caps how many of the richest dropped lanes are named in the
	// log line per plan (the counter still aggregates ALL of them); mirrors the solver's
	// TOP_REJECTED_N observability parity so the log can't spam.
	unreachableLaneLogTopN = 3
	// defaultModelArtifactPath is where the checked-in market-model artifact lives
	// relative to the daemon's working directory (repo root). The executor reads
	// fit_version + era from it at launch to bind the planner to the exact model —
	// an unreadable artifact fails OPEN to single-lane (RULINGS #4: never guess a
	// version), never a phantom trade.
	defaultModelArtifactPath = "gobot/services/routing-service/model_artifacts/market_model.json"
	// tourDefaultMaxSpendTreasuryPct sizes the default cumulative spend cap when the
	// captain leaves --max-spend at 0: 25% of live treasury (RULINGS #6). With
	// --iterations -1 this is re-resolved against LIVE treasury at EACH tour's plan
	// (an explicit --max-spend stays constant per tour); see execute's loop.
	tourDefaultMaxSpendTreasuryPct = 25
	// tourStarvationLimit bounds how many CONSECUTIVE no-progress tours (planner
	// returns no profitable tour, or a feasible plan executes zero trades) a
	// continuous run (--iterations -1) tolerates before it calls margins dead and
	// exits HONESTLY (the container completes). Mirrors the trade-route circuit
	// loop's noProgressStarvationLimit: one no-plan can be a transient live-recheck
	// miss, several in a row means the system has nothing left worth touring. A
	// no-plan on the VERY FIRST tour (nothing earned yet) is the existing fail-open
	// "tour unavailable" instead, so the single-lane fallback stands.
	tourStarvationLimit = 3
	// defaultDepositCeilingPct is the pre-positioning capital ceiling as a percent
	// of live treasury when the captain leaves capital_ceiling_pct at 0 (sp-dchv
	// Lane C). Junior to the working-capital reserve; an unreadable balance yields
	// ZERO candidates (fail closed, RULINGS #4).
	defaultDepositCeilingPct = 10
	// tourTreasuryRetryBackoff is the interruptible pause a CONTINUOUS (--iterations
	// -1) dynamic-cap (--max-spend 0) tour waits before RE-TRYING when the live
	// treasury read fails at re-resolution time (sp-7z7j). RULINGS #4: an unreadable
	// balance fails CLOSED (never spend, never fall back to unlimited/stale) — but
	// failing closed must PAUSE and RETRY, not silently end the -1 loop. A transient
	// GetAgent blip (a global rate-limit burst fails every hull's shared-agent read at
	// once) was resolving to a 0 budget the planner refused (spend_cap 0 → infeasible),
	// which the loop misread as "tour unavailable" and COMPLETED the container after one
	// iteration. Mirrors liquidationRetryBackoff's cadence; clock-injected so tests are
	// instant and a Stop/shutdown never waits it out.
	tourTreasuryRetryBackoff = 20 * time.Second
)

// exitReason* enumerates why the continuous tour loop stopped, surfaced on the
// response for observability (mirrors the trade-route coordinator's ExitReason).
const (
	// tourExitIterations: a finite --iterations budget was consumed (every tour flew).
	tourExitIterations = "iterations_exhausted"
	// tourExitStarvation: tourStarvationLimit consecutive tours found no profitable
	// tour (or flew zero trades) — margins died. An HONEST completion.
	tourExitStarvation = "starvation"
	// tourExitUnavailable: the very first tour found no plan and nothing was earned —
	// the fail-open no-op (single-lane fallback stands).
	tourExitUnavailable = "tour_unavailable"
)

// RunTourCoordinatorCommand is a captain-directed, guarded multi-hop trade-tour run
// (sp-1ek0): plan a depth-aware tour for THIS hull, fly it leg by leg with prices
// re-verified live at every dock, re-plan at most ReplanLimit times when reality
// drifts past tolerance. The route is dynamically planned, so honest completion is a
// response VETO (not a Go error) — a re-run cannot resume a planner-chosen route.
//
// Iterations makes it a CONTINUOUS engine (sp-m5kv): on manifest completion it
// re-plans from the hull's CURRENT position + live market and flies the next tour
// with no captain in the loop, turning capital velocity from captain-cadence into
// engine-cadence. See Iterations for the loop semantics.
type RunTourCoordinatorCommand struct {
	ShipSymbol  string
	PlayerID    int
	MaxHops     int   // 0 → maxTourHops
	MaxSpend    int64 // 0 → 25% of live treasury (re-resolved per tour when Iterations != 0/1)
	MinMargin   int
	ReplanLimit int // 0 → tourMaxReplansDefault (PER TOUR)
	// Iterations is the tour count (sp-m5kv), unifying the container iteration
	// semantics (registry invariant 3): -1 = CONTINUOUS (tour, re-plan from the new
	// position, tour again — until margins die/starvation/stop), N>0 = exactly N
	// tours, 0 = the one-tour default (the original one-shot behavior, so every
	// pre-sp-m5kv caller and test is byte-for-byte unchanged). The coordinator owns
	// this loop internally (CoordinatorOwnsIterations); the container runs Handle()
	// once.
	Iterations            int
	AgentSymbol           string
	ContainerID           string // the tour id; groups this run's telemetry legs
	WorkingCapitalReserve int64  // 0 → defaultWorkingCapitalReserve
	// ModelArtifactPath overrides defaultModelArtifactPath (tests point it at a temp
	// artifact); empty → the default repo-relative path.
	ModelArtifactPath string

	// --- Reposition-on-margins-death (sp-zhii) ---
	// When a CONTINUOUS (--iterations -1) tour's margins die (tourStarvationLimit
	// consecutive no-plans after >=1 productive tour), the coordinator RANKS
	// jump-reachable systems by expected tour margin and JUMPS to the best one before
	// exiting — a hull stranded on its own freshly-sold-out ground rotates to a fresh
	// renewable one instead of dying on it and burning a captain relaunch. Bounded to
	// ONE reposition per margins-death episode (no infinite hop-scotching).

	// RepositionDisabled is the kill-switch. false (the zero value / absent config) →
	// reposition is ON for continuous runs (the captain filed sp-zhii to END the
	// whack-a-mole, so ON is the default); true disables it and a margins-died tour
	// exits exactly as pre-sp-zhii.
	RepositionDisabled bool
	// RepositionMinMargin is the fresh-profit floor (RULINGS #5) a candidate's planned
	// tour must clear to justify the jump: a jump costs antimatter + fuel + a one-way
	// hop the hull spends not trading, so a marginal destination isn't worth relocating
	// for. 0 → repositionMinMarginDefault.
	RepositionMinMargin int
	// RepositionMaxCandidates bounds the solver fan-out: at most K pre-ranked candidate
	// systems get a real planner call per margins-death episode. 0 →
	// repositionMaxCandidatesDefault.
	RepositionMaxCandidates int
	// RepositionInProgress / RepositionTargetSystem / RepositionTargetWaypoint are the
	// restart-resume state (RULINGS #2): persisted into the container config the instant
	// a reposition jump is committed and cleared once it lands, so a daemon restart
	// mid-jump resumes toward the SAME ground through the shared cooldown-riding travel
	// machinery (sp-wc5h) rather than re-planning at whatever intermediate hop it was
	// re-adopted on. Set by the recovery rebuild from the persisted config; a fresh
	// launch leaves them zero.
	RepositionInProgress     bool
	RepositionTargetSystem   string
	RepositionTargetWaypoint string
}

// RunTourCoordinatorResponse reports the realised tour economics and — via
// CompletionOutcome — whether the run honestly completed. Three terminal shapes:
// a completed tour (Completed), a fail-open no-op (TourUnavailable, a clean
// completion — planner down/infeasible or model artifact unreadable, single-lane
// fallback stands), and a stranded-cargo veto (CargoStranded → the runner
// terminalizes FAILED via the honest-completion contract).
type RunTourCoordinatorResponse struct {
	ShipSymbol   string
	TourID       string
	LegsPlanned  int
	LegsExecuted int
	Replans      int
	TotalSpent   int64
	TotalRevenue int64
	NetProfit    int64
	ModelVersion string
	Completed    bool

	// ToursCompleted counts how many tours flew >=1 trade this run (sp-m5kv). 1 for
	// the one-shot default; >1 for a continuous (--iterations) run. TradesExecuted is
	// the run's total executed buy+sell tranches (the per-tour progress signal the
	// starvation guard reads). ExitReason (a tourExit* constant) explains why a
	// continuous loop stopped; empty on the one-shot path.
	ToursCompleted int
	TradesExecuted int
	ExitReason     string

	// Repositions counts how many times this run rotated the hull to a fresh ground on
	// margins-death (sp-zhii). ExitDetail is the human-readable exit explanation the
	// ExitReason constant abbreviates — on a reposition-then-death it NAMES BOTH the
	// origin and the destination system ("repositioned X -> Y ... margins died there
	// too"), so a captain reading a completed continuous tour sees the full rotation
	// story, not just the machine-readable "starvation".
	Repositions int
	ExitDetail  string

	// TourUnavailable marks a fail-open exit: no trading happened, the single-lane
	// fallback remains. A CLEAN completion (not a failure), never a phantom trade.
	TourUnavailable       bool
	TourUnavailableReason string

	// CargoStranded is the honest-completion veto (sp-7yej invariant 2): the tour
	// ended holding cargo it bought this run. Threaded through CompletionOutcome
	// (nil Go error), NOT arb's non-nil-error shape — a dynamically-planned tour
	// cannot be resumed by a re-run, which would trade AROUND the strand.
	CargoStranded       bool
	CargoStrandedReason string

	Error string
}

// CompletionOutcome implements common.CompletionReporter: a stranded tour vetoes
// the runner's success=true (terminalized FAILED with the strand as its signature).
// A fail-open "tour unavailable" is an honest clean completion (nothing half-done).
func (r *RunTourCoordinatorResponse) CompletionOutcome() (bool, string) {
	if r.CargoStranded {
		return false, r.CargoStrandedReason
	}
	return true, ""
}

// Compile-time pin: the tour response participates in the honest-completion contract.
var _ common.CompletionReporter = (*RunTourCoordinatorResponse)(nil)

// RunTourCoordinatorHandler runs the one-shot guarded tour. It composes the proven
// RunTradeRouteCoordinatorHandler primitives (travel — multi-jump, dock, purchase,
// sell, observeGood, loadShip, spendFloorBreached) rather than re-implementing them,
// so it inherits every fix those legs carry, and adds the planner call, per-leg live
// re-verification, bounded re-planning, telemetry, and the stranded-cargo veto.
type RunTourCoordinatorHandler struct {
	legs         *RunTradeRouteCoordinatorHandler
	marketRepo   market.MarketRepository
	waypointRepo system.WaypointRepository
	telemetry    trading.TourTelemetryRepository
	planner      routing.RoutingClient
	clock        shared.Clock
	// apiClient live-reads treasury for the default 25% max-spend; nil → no default
	// cap (the per-buy working-capital floor still guards).
	apiClient domainPorts.APIClient
	// modelArtifactPath is the daemon-configured (absolute) path to the market-model
	// artifact this coordinator reads at launch, injected from cfg.Routing.ModelArtifactPath
	// (sp-wj0h). Empty → the repo-relative defaultModelArtifactPath fallback. A per-run
	// cmd.ModelArtifactPath (tests) still wins over this.
	modelArtifactPath string

	// repositionPersister durably records an in-flight margins-death reposition (its
	// target system+waypoint) into the container config so a daemon restart mid-jump
	// resumes toward the SAME ground (sp-zhii, RULINGS #2). Optional; nil disables
	// persistence (a restart mid-jump then re-plans at the hull's current position
	// rather than resuming the reposition — fail-open, matching the sibling optional-port
	// contract). The daemon injects a container-config-backed persister via
	// SetRepositionPersister.
	repositionPersister RepositionStatePersister

	// mediator dispatches the cargo TransferCargoCommand for haul-to-storage deposit
	// legs (sp-dchv Lane C). Same mediator the delegated legs use.
	mediator common.Mediator
	// Pre-positioning deposit dependencies (sp-dchv Lane C), all optional and
	// injected via SetPrePositioning AFTER the storage subsystem is wired (main.go).
	// When any is nil or prePositioning.Enabled is false, no deposit legs are
	// offered or executed and the tour behaves exactly as pre-sp-dchv.
	storageCoordinator storage.StorageCoordinator
	warehouseFinder    tradingsvc.WarehouseOperationFinder
	demandMiner        tradingsvc.DepositDemandMiner
	prePositioning     tradingsvc.DepositCandidateConfig
	depositCeilingPct  int

	// --- Cross-engine absorption coordination (sp-78ai L3) ---
	// absorptionLedger, when wired via SetAbsorptionLedger, makes the tour a ledger
	// WRITER (reserve planned tranches at plan-accept, convert to recovery shadows at
	// sale, release on re-plan/exit) AND a READER (net outstanding depth into each plan
	// so the solver plans AROUND sinks other containers occupy). Nil (the pre-sp-78ai
	// shape / tests that don't wire it) → no netting, no reservations: the tour plans
	// and flies exactly as before.
	absorptionLedger absorption.Ledger
	// tourConsultDisabled is the operator escape hatch (RULINGS #5). true → the tour
	// STOPS netting and STOPS conditionally gating (never rejects/re-plans on a
	// reservation breach), but still RECORDS each plan's occupancy so other engines keep
	// consulting it — the same "kill the consult, keep the record" posture idle-arb's
	// IdleArbConsultDisabled takes. Convert + release still run. Default false.
	tourConsultDisabled bool
	// tourPlannedTTLSlack pads a plan's projected round-trip TTL (backstop to the sweep +
	// dead-container reclaim). 0 → defaultTourPlannedTTLSlack.
	tourPlannedTTLSlack time.Duration
	// recoveryHalfLives caches the fitted per-tier recovery half-lives (minutes) read
	// from the model artifact ONCE, for the report-only projected_recovery_burden metric
	// (Q3). Immutable after the first load; the handler is shared across concurrent tour
	// runs, so it is loaded under recoveryOnce and never mutated per-run.
	recoveryHalfLives map[string]float64
	recoveryOnce      sync.Once

	// sinkScanner backs the out-of-horizon lane diagnostic (sp-mtvg): after building the
	// in-scope snapshot, the coordinator asks it for each in-scope-sourced good's best sink
	// ACROSS ALL SYSTEMS, and counts+logs the lanes whose best sink lies beyond the
	// 1-gate-hop tour graph — the "exotic good-level blind spot" made loud. Optional and
	// nil-safe: unset (tests, or metrics-disabled builds) → the diagnostic no-ops and the
	// tour plans exactly as before (RULINGS #4 — observation never gates the trade path).
	sinkScanner outOfHorizonSinkScanner
}

// outOfHorizonSinkScanner reads the global best sell destination per good (across ALL
// systems), the seam the tour coordinator uses to SEE sinks its 1-gate-hop snapshot
// cannot (sp-mtvg). The concrete *persistence.MarketRepositoryGORM satisfies it; the
// daemon injects it via SetOutOfHorizonSinkScanner. Kept as a narrow local port (not a
// method on the wide market.MarketRepository interface) so no mock/test double outside
// this diagnostic is disturbed.
type outOfHorizonSinkScanner interface {
	BestSinksAcrossSystems(ctx context.Context, goods []string, playerID int, maxAge time.Duration, now time.Time) (map[string]market.GlobalSinkResult, error)
}

// NewRunTourCoordinatorHandler wires the tour coordinator with the same driven ports
// as the trade-route circuit (so buys/sells/navigation resolve to the daemon's exact
// command handlers) plus the market-model planner, waypoint repository (era-scoped
// coordinates), and telemetry repository. A nil clock defaults to RealClock.
func NewRunTourCoordinatorHandler(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	marketRepo market.MarketRepository,
	waypointRepo system.WaypointRepository,
	telemetry trading.TourTelemetryRepository,
	planner routing.RoutingClient,
	marketRefresher MarketRefresher,
	clock shared.Clock,
	apiClient domainPorts.APIClient,
) *RunTourCoordinatorHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &RunTourCoordinatorHandler{
		legs:         NewRunTradeRouteCoordinatorHandler(mediator, shipRepo, marketRepo, marketRefresher, clock, apiClient),
		marketRepo:   marketRepo,
		waypointRepo: waypointRepo,
		telemetry:    telemetry,
		planner:      planner,
		clock:        clock,
		apiClient:    apiClient,
		mediator:     mediator,
	}
}

// SetPrePositioning wires the optional haul-to-storage deposit subsystem (sp-dchv
// Lane C): the shared storage coordinator (deposit protocol + warehouse space
// reads), the warehouse-op finder, the Lane A demand miner, the resolved config,
// and the capital-ceiling percent. Called from main.go AFTER the storage subsystem
// is constructed (the tour coordinator is wired earlier). Left unset, no deposit
// legs are ever offered or executed — the tour plans and flies pure arb.
func (h *RunTourCoordinatorHandler) SetPrePositioning(
	coordinator storage.StorageCoordinator,
	warehouses tradingsvc.WarehouseOperationFinder,
	miner tradingsvc.DepositDemandMiner,
	cfg tradingsvc.DepositCandidateConfig,
	capitalCeilingPct int,
) {
	h.storageCoordinator = coordinator
	h.warehouseFinder = warehouses
	h.demandMiner = miner
	h.prePositioning = cfg
	h.depositCeilingPct = capitalCeilingPct
}

// SetOutOfHorizonSinkScanner wires the global best-sink reader that backs the
// out-of-horizon lane diagnostic (sp-mtvg). The daemon injects the concrete market repo;
// left unset the diagnostic no-ops (RULINGS #4). Optional-port pattern, like the setters
// below.
func (h *RunTourCoordinatorHandler) SetOutOfHorizonSinkScanner(s outOfHorizonSinkScanner) {
	h.sinkScanner = s
}

// SetGateGraph wires the multi-jump gate-graph resolver into the delegated movement
// handler (so travel crosses multi-hop gaps and cross-gate tours fly). Mirrors the
// arb coordinator's injection.
func (h *RunTourCoordinatorHandler) SetGateGraph(g GateGraph) {
	h.legs.SetGateGraph(g)
}

// SetModelArtifactPath injects the daemon-configured (absolute) market-model artifact
// path this coordinator reads at launch (sp-wj0h: resolved from cfg.Routing.ModelArtifactPath
// so it is cwd-independent). Left unset, the coordinator falls back to the repo-relative
// defaultModelArtifactPath. Mirrors the SetGateGraph optional-injection idiom.
func (h *RunTourCoordinatorHandler) SetModelArtifactPath(path string) {
	h.modelArtifactPath = path
}

// RepositionEpisode is the durable slice of a margins-death reposition (sp-zhii): the
// destination the hull is jumping to. It is persisted into the container config the
// instant the jump is committed and cleared (InProgress=false) once it lands, so a daemon
// restart mid-jump (RULINGS #2) resumes toward the SAME ground through the shared
// cooldown-riding travel machinery (sp-wc5h) rather than re-planning at whatever
// intermediate hop it was re-adopted on.
type RepositionEpisode struct {
	InProgress     bool
	TargetSystem   string
	TargetWaypoint string
}

// RepositionStatePersister durably records an in-flight reposition's destination (keyed
// by container) so a restart-rebuilt run resumes the jump instead of re-planning at an
// intermediate position (sp-zhii, RULINGS #2). The daemon backs this with the container
// config — the same map the recovery rebuild reads (buildTourCoordinatorCommand's
// reposition_* keys). Mirrors the arb ArbCostPersister contract: a returned error is
// advisory (persistence durability, never a spend/movement guard), so the caller logs and
// continues.
type RepositionStatePersister interface {
	PersistRepositionState(ctx context.Context, containerID string, playerID int, episode RepositionEpisode) error
}

// SetRepositionPersister wires the durable reposition-state store (sp-zhii) so a margins-
// death reposition survives a daemon restart mid-jump (RULINGS #2). Left unset (nil), a
// restart mid-jump re-plans at the hull's current position rather than resuming the
// reposition, exactly as if the feature carried no persistence (fail-open). Mirrors the
// arb SetCostPersister optional-injection idiom.
func (h *RunTourCoordinatorHandler) SetRepositionPersister(p RepositionStatePersister) {
	h.repositionPersister = p
}

// Handle executes the one-shot tour. A fail-open no-op and a stranded-cargo veto both
// return a nil Go error (the veto is threaded through CompletionOutcome); an
// operational failure mid-tour returns the underlying error so the runner can retry
// (a retry re-plans from current position/cargo — cargo-aware, never a blind re-buy).
func (h *RunTourCoordinatorHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*RunTourCoordinatorCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}
	response := &RunTourCoordinatorResponse{ShipSymbol: cmd.ShipSymbol, TourID: cmd.ContainerID}
	if err := h.execute(ctx, cmd, response); err != nil {
		response.Error = err.Error()
		return response, err
	}
	if !response.TourUnavailable && !response.CargoStranded {
		response.Completed = true
	}
	return response, nil
}

func (h *RunTourCoordinatorHandler) execute(ctx context.Context, cmd *RunTourCoordinatorCommand, response *RunTourCoordinatorResponse) (err error) {
	logger := common.LoggerFromContext(ctx)

	// sp-fbih P11/P12: observe the tour_run's terminal outcome exactly once, and ONLY on an
	// HONEST completion (err == nil). A resumable exit — ctx-cancel on shutdown, the 7z7j
	// fail-closed treasury pause, a travel error mid-reposition — returns non-nil and sets
	// no ExitReason; the container is re-adopted and runs again, so counting an exit or
	// observing a truncated duration there would double-count one logical run. Every
	// err==nil return sets ExitReason first (unavailable/starvation/iterations), so the
	// counter and the histogram move together. Pure observation after the loop has already
	// decided (RULINGS #4); a metrics miss cannot alter the outcome.
	tourStart := h.clock.Now()
	defer func() {
		if err != nil || response.ExitReason == "" {
			return
		}
		metrics.RecordTourExit(cmd.PlayerID, response.ExitReason)
		metrics.ObserveTourDuration(cmd.PlayerID, h.clock.Now().Sub(tourStart).Seconds())
	}()

	// Stamp every ledger row this run's buy/sell legs write with operation_type=
	// "tour" (sp-lgnh). The delegated cargo-tx path reads this operation context
	// off ctx and persists opCtx.NormalizedOperationType() ("tour_run" → "tour");
	// without it, tour trades land under the default and contaminate the very
	// single-lane baseline the graduation gate measures the tour against (the
	// baseline filters operation_type <> 'tour'). Mirrors how every coordinator
	// tags its writes at the boundary (run_trade_route_coordinator.go's "trade_route").
	ctx = shared.WithOperationContext(ctx, shared.NewOperationContext(cmd.ContainerID, "tour_run"))

	// sp-78ai L3: release this container's PLANNED reservations on EVERY exit path
	// (clean completion, error, ctx-cancel) so a finished tour stops occupying sink/ask
	// depth other engines net against. Converted EXECUTED shadows are LEFT (real recovery
	// still decaying); a ctx-cancelled exit that cannot run the delete leaves the rows to
	// the ledger's TTL sweep + dead-container reclaim (the belt-and-suspenders cleanup).
	defer h.releaseTourReservations(ctx, cmd)

	// Bind the model version from the checked-in artifact (RULINGS #4: unreadable →
	// fail OPEN to single-lane, never guess a version). Path precedence (sp-wj0h): an
	// explicit per-run cmd.ModelArtifactPath (tests) → the daemon-configured absolute
	// path (production, cwd-independent) → the repo-relative constant (pure-env fallback).
	artifactPath := cmd.ModelArtifactPath
	if artifactPath == "" {
		artifactPath = h.modelArtifactPath
	}
	if artifactPath == "" {
		artifactPath = defaultModelArtifactPath
	}
	modelVersion, err := readTourModelVersion(artifactPath)
	if err != nil {
		response.TourUnavailable = true
		response.TourUnavailableReason = fmt.Sprintf("tour unavailable: model artifact unreadable (%s): %v", artifactPath, err)
		response.ExitReason = tourExitUnavailable
		logger.Log("WARNING", response.TourUnavailableReason, map[string]interface{}{
			"artifact": artifactPath, "error": err.Error(),
		})
		return nil
	}
	response.ModelVersion = modelVersion

	reserve := cmd.WorkingCapitalReserve
	if reserve == 0 {
		reserve = int64(defaultWorkingCapitalReserve)
		// sp-ggk2 RULINGS #4: never resolve the reserve to a default SILENTLY. Tonight's
		// regression — a coordinator-launched tour buying under the 50k floor while its
		// launch config carried a 1M reserve — was invisible precisely because this
		// fallback logged nothing. A built command reaching here with reserve==0 means the
		// launch config carried no reserve (a captain CLI tour with no --reserve, or a
		// fleet whose [trade_fleet] reserve is unset); surfacing it makes a fleet
		// accidentally running on the floor visible in the log, not only in the P&L. The
		// present-but-unparseable case can no longer reach here — it fails the build closed
		// (PresentOrFailInt in buildTourCoordinatorCommand).
		logger.Log("WARNING", fmt.Sprintf(
			"Tour %s: working-capital reserve resolved to the %d default (launch config carried no reserve) - every buy is floored at %d, not a fleet reserve",
			cmd.ShipSymbol, defaultWorkingCapitalReserve, defaultWorkingCapitalReserve), map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol, "container_id": cmd.ContainerID, "resolved_reserve": defaultWorkingCapitalReserve,
		})
	}
	maxHops := cmd.MaxHops
	if maxHops <= 0 || maxHops > maxTourHops {
		maxHops = maxTourHops
	}
	replanLimit := cmd.ReplanLimit
	if replanLimit <= 0 {
		replanLimit = tourMaxReplansDefault
	}

	// Iteration budget (sp-m5kv): 0 → the one-tour default (the original one-shot,
	// so every pre-sp-m5kv caller/test is unchanged); -1 → continuous until margins
	// die; N>0 → exactly N tours.
	iterations := cmd.Iterations
	if iterations == 0 {
		iterations = 1
	}
	continuous := iterations < 0

	// netBought is CUMULATIVE across every tour this run: the honest-completion
	// stranded veto (sp-7yej invariant 2) is checked ONCE, at the final exit. A tour
	// ending with held cargo is NOT stranded mid-run — the next tour re-plans from the
	// hull's current cargo and (sp-m5kv part 2) the solver sells it as launch
	// inventory. Only cargo BOUGHT this run and never sold survives to veto the final
	// completion; pre-held cargo (never in netBought) drives it negative, so
	// liquidating the captain's pre-existing load is a bonus, never a false veto.
	netBought := map[string]int{}

	// The budget counts PRODUCTIVE tours (ToursCompleted), not attempts: "N tours"
	// means N tours actually flown, so a transient no-plan mid-run is retried (bounded
	// by the starvation streak) rather than silently burning a tour slot.
	noProgressStreak := 0

	// episode tracks the current margins-death reposition (sp-zhii): whether this run has
	// already spent its ONE reposition since the last productive tour, and the systems
	// involved (for the honest "margins died at X, repositioned to Y, died there too"
	// exit). A productive tour clears it — a fresh ground earned means a LATER death may
	// rotate again (grounds are renewable flows), which is the whole point; the
	// one-per-episode bound only stops hop-scotching WITHOUT trading in between.
	var episode repositionEpisode

	// RULINGS #2 restart-resume: a continuous run re-adopted mid-jump (the reposition was
	// in flight when the daemon restarted) resumes toward the SAME destination through the
	// shared cooldown-riding travel machinery (sp-wc5h rides any leftover jump cooldown),
	// then clears the persisted flag — so the hull lands on the ground it was rotating to
	// rather than re-planning at whatever intermediate hop it was re-adopted on. It counts
	// as the episode's spent reposition so a fresh 3-strike at the destination exits
	// honestly instead of hop-scotching across the restart boundary.
	if continuous && cmd.RepositionInProgress && cmd.RepositionTargetWaypoint != "" {
		logger.Log("INFO", fmt.Sprintf("Reposition resume: re-adopted mid-jump toward %s (%s) after a restart - completing the jump before re-planning (RULINGS #2)", cmd.RepositionTargetSystem, cmd.RepositionTargetWaypoint), map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol, "target_system": cmd.RepositionTargetSystem, "target_waypoint": cmd.RepositionTargetWaypoint,
		})
		if rerr := h.legs.RepositionToWaypoint(ctx, cmd.ShipSymbol, cmd.RepositionTargetWaypoint, cmd.PlayerID); rerr != nil {
			return rerr // resumable — the persisted in-progress flag stays set so a re-restart retries the resume
		}
		h.persistReposition(ctx, cmd, RepositionEpisode{InProgress: false})
		episode = repositionEpisode{repositioned: true, toSystem: cmd.RepositionTargetSystem}
	}

	for continuous || response.ToursCompleted < iterations {
		// A stop/shutdown cancels ctx (interruptAllContainers escalates the STOPPING
		// flag to a ctx cancel). Exit RESUMABLE at the tour boundary by returning the
		// ctx error, which the runner routes through its ctx.Err() path (re-adopted at
		// next boot) — never let a cancel be misread as a swallowed planner no-plan and,
		// via the starvation streak, COMPLETE a -1 container (the sp-ovkn trap: a
		// COMPLETED row is dropped from the recovery set and the hull is lost).
		if err := ctx.Err(); err != nil {
			return err
		}

		// RULINGS #6: an explicit --max-spend is a constant per-tour cap; --max-spend
		// 0/omitted re-resolves 25% of LIVE treasury at EACH tour's plan, so a
		// continuous run sizes each tour to the treasury it has grown into. The per-buy
		// working-capital floor guards every spend regardless.
		tourMaxSpend := cmd.MaxSpend
		if tourMaxSpend == 0 {
			resolved, unreadable := h.defaultMaxSpend(ctx)
			if unreadable {
				// sp-7z7j: the dynamic budget could NOT be re-resolved — a treasury
				// SOURCE is wired but the live read failed (transient GetAgent blip /
				// token gone). RULINGS #4 fail-CLOSED: do NOT spend this iteration and
				// NEVER fall back to unlimited or a stale budget. But failing closed
				// must PAUSE and RETRY, not end the loop: proceeding here with a 0
				// budget is exactly what the planner refused (spend_cap 0 → infeasible),
				// which — nothing earned yet on a relaunch — the loop below misread as
				// "tour unavailable" and COMPLETED a -1 container after one iteration
				// (the 5/5 field repro). Skip the tour, wait an interruptible backoff,
				// and re-resolve next pass; a Stop/shutdown during the wait exits
				// RESUMABLE (ctx error), the same as the boundary check above. The
				// no-progress starvation streak is left UNTOUCHED — an unreadable
				// treasury is a transient guard trip, not margin-death.
				logger.Log("WARNING", "Dynamic tour budget unresolved (live treasury unreadable) - failing closed: not spending, pausing before retry (loop stays alive)", map[string]interface{}{
					"ship_symbol": cmd.ShipSymbol, "tours_completed": response.ToursCompleted,
					"backoff_seconds": int(tourTreasuryRetryBackoff.Seconds()),
				})
				if werr := h.legs.sleepInterruptibly(ctx, tourTreasuryRetryBackoff); werr != nil {
					return werr
				}
				continue
			}
			tourMaxSpend = resolved
			// sp-fbih P13: record the RESOLVED dynamic cap (25% of live treasury) the exact
			// value nj2b's Guards panel proxies with a treasury x 0.25 line. Only on the dynamic
			// path — an explicit --max-spend constant has nothing dynamic to track.
			metrics.SetTourResolvedMaxSpend(cmd.PlayerID, resolved)
		}

		tradesBefore := response.TradesExecuted
		feasible, reason, terr := h.runOneTour(ctx, cmd, response, netBought, maxHops, tourMaxSpend, reserve, replanLimit, modelVersion)
		if terr != nil {
			return terr
		}

		// A PRODUCTIVE tour (feasible AND flew >=1 trade) resets the starvation streak and
		// ENDS any reposition episode: a fresh ground earned, so a later death may rotate
		// again (sp-zhii — the one-per-episode bound only prevents hop-scotching WITHOUT
		// trading in between).
		if feasible && response.TradesExecuted > tradesBefore {
			noProgressStreak = 0
			response.ToursCompleted++
			episode = repositionEpisode{}
			continue
		}

		// No progress this tour. On the VERY FIRST tour with no plan, nothing was earned.
		// A finite/one-shot run (iterations != -1) fails open here — the single-lane
		// fallback stands, the original one-shot behavior preserved exactly. A CONTINUOUS
		// (-1) run does NOT: a recovered engine re-enters at ToursCompleted==0 having LOST
		// its pre-restart productive standing across the daemon boundary, and dying on ONE
		// drained-ground plan (bypassing sp-zhii's rank-and-reposition) is the sp-m9co
		// restart-boundary death — hulls productive before the restart lost to a single bad
		// post-restart plan on ground the pre-restart cohort had drained. So a continuous run
		// falls THROUGH to the streak, letting iteration-1 infeasibility accumulate toward the
		// SAME reposition rescue as margins-death rather than completing the container; a
		// genuinely dead neighbourhood still exits honestly below (no candidate clears the
		// floor). The 7z7j unreadable-treasury PAUSE never reaches here (it `continue`s above,
		// before runOneTour), so it is untouched.
		if !feasible && response.ToursCompleted == 0 && !continuous {
			response.TourUnavailable = true
			response.TourUnavailableReason = reason
			response.ExitReason = tourExitUnavailable
			logger.Log("INFO", reason, map[string]interface{}{"ship_symbol": cmd.ShipSymbol, "model": modelVersion})
			return nil
		}

		// Already earned but this tour made no progress (no plan, or a feasible plan that
		// flew zero trades — every leg degraded, re-plans exhausted). Bound how many in a
		// row a -1 loop tolerates so a transient miss is retried but a genuinely dead
		// ground is caught (mirrors the trade-route zero-visit starvation).
		noProgressStreak++
		starvationDetail := fmt.Sprintf("margins died (%d consecutive tours found no profitable plan) after %d productive tour(s)", noProgressStreak, response.ToursCompleted)
		if feasible {
			starvationDetail = fmt.Sprintf("%d consecutive tours flew zero trades after %d productive tour(s)", noProgressStreak, response.ToursCompleted)
		}
		if noProgressStreak < tourStarvationLimit {
			continue
		}

		// sp-fbih P4: the ground just tapped out (3-strike confirmed). Counted HERE, before the
		// reposition attempt, so it measures the ground rich->tapped cadence whether or not a
		// reposition then rescues the run — distinct from tour_exit_total{reason=starvation},
		// which fires only when a tap-out becomes the final honest exit. A productive tour
		// resets the streak, so this counts once per margins-death episode.
		metrics.RecordTourMarginsDeath(cmd.PlayerID)

		// Margins confirmed dead. Before exiting, try to ROTATE the hull to a fresh
		// renewable ground (sp-zhii): rank jump-reachable systems by expected tour margin,
		// jump to the best one that clears the reposition floor, and let the loop re-plan
		// there. Scoped to CONTINUOUS (-1) runs — a finite/one-shot run already fail-opened
		// above on iteration-1 infeasibility and never reaches here with no plan. sp-m9co:
		// this now fires at ToursCompleted==0 too, so a recovered continuous engine that
		// re-entered with a lost productive count and 3-struck on iteration-1 infeasibility
		// rotates off the drained ground instead of dying on it (the fail-open above no
		// longer intercepts continuous runs).
		if continuous {
			repositioned, rerr := h.maybeReposition(ctx, cmd, response, &episode, netBought, maxHops, tourMaxSpend, reserve, modelVersion)
			if rerr != nil {
				return rerr
			}
			if repositioned {
				noProgressStreak = 0
				continue
			}
		}

		// No ground was worth the jump (or reposition is off/already spent this episode) —
		// exit HONEST (the container completes). The detail NAMES BOTH systems when a
		// reposition was already spent this episode (RULINGS: name origin and destination).
		response.ExitReason = tourExitStarvation
		response.ExitDetail = starvationExitDetail(episode, starvationDetail)
		logger.Log("INFO", "Continuous tour stopping - "+response.ExitDetail, map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol, "tours_completed": response.ToursCompleted,
			"repositions": response.Repositions, "reason": reason,
		})
		break
	}
	if response.ExitReason == "" {
		response.ExitReason = tourExitIterations
	}

	// Honest-completion check (FINAL exit only, sp-m5kv boundary): any cargo bought
	// this run and still aboard after the whole loop is a stranded veto — the
	// container is terminalized FAILED (sp-7yej invariant 2). A mid-run held load is
	// deliberately NOT checked here; it was carried forward to the next tour's plan.
	if reason, stranded := h.strandedReason(ctx, cmd, netBought); stranded {
		response.CargoStranded = true
		response.CargoStrandedReason = reason
		logger.Log("ERROR", reason, map[string]interface{}{"ship_symbol": cmd.ShipSymbol})
		return nil
	}

	response.NetProfit = response.TotalRevenue - response.TotalSpent
	logger.Log("INFO", "Tour run complete", map[string]interface{}{
		"ship_symbol": cmd.ShipSymbol, "tours_completed": response.ToursCompleted, "exit_reason": response.ExitReason,
		"legs_executed": response.LegsExecuted, "trades_executed": response.TradesExecuted, "replans": response.Replans,
		"repositions": response.Repositions, "spent": response.TotalSpent, "revenue": response.TotalRevenue, "net": response.NetProfit,
	})
	return nil
}

// runOneTour plans and flies ONE tour from the hull's CURRENT position and cargo,
// accumulating economics into response and cargo bought into netBought (cumulative
// across the run). It returns feasible=false with a fail-open reason when the planner
// found no profitable tour (the caller decides fail-open vs margin-death), and a
// non-nil error only on an operational failure the runner should retry (a retry
// re-plans from current position/cargo — cargo-aware, never a blind re-buy). This is
// the per-tour body the continuous loop repeats; the original one-shot run is exactly
// one call of it.
func (h *RunTourCoordinatorHandler) runOneTour(
	ctx context.Context,
	cmd *RunTourCoordinatorCommand,
	response *RunTourCoordinatorResponse,
	netBought map[string]int,
	maxHops int,
	maxSpend, reserve int64,
	replanLimit int,
	modelVersion string,
) (bool, string, error) {
	logger := common.LoggerFromContext(ctx)

	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return false, "", err
	}

	// sp-78ai L3: plan a depth-netted tour AND conditionally reserve its tranches
	// all-or-nothing. A reservation breach (another container claimed a sink between the
	// netting snapshot and the reserve) is a normal re-plan, NOT a failure — planAndReserve
	// retries against fresh ledger state, and only a persistent contention exits infeasible.
	plan, shadowSinks, reason, feasible, err := h.planAndReserve(ctx, cmd, ship, maxHops, maxSpend, reserve, modelVersion)
	if err != nil {
		return false, "", err
	}
	if !feasible {
		return false, reason, nil
	}
	response.LegsPlanned += len(plan.Legs)
	// Honest projection split (sp-bc27 + sp-dchv Lane C): projected profit is the
	// TOTAL that ranked this tour; fresh cash profit, held-cargo liquidation revenue,
	// and synthetic haul-to-storage DEPOSIT value are reported apart so a laden-hull
	// or pre-positioning plan's margin is not read as pure fresh-trade profit.
	// Fresh cash = total - liquidation - deposit_value (liquidation has no
	// acquisition cost; a deposit books no cash — its value is future contract
	// savings, not revenue).
	freshProfit := plan.ProjectedProfit - plan.HeldLiquidation - plan.DepositValue
	logger.Log("INFO", fmt.Sprintf("Tour planned: %d legs, projected profit %d (fresh %d, liquidation %d, deposit %d) (model %s)", len(plan.Legs), plan.ProjectedProfit, freshProfit, plan.HeldLiquidation, plan.DepositValue, modelVersion), map[string]interface{}{
		"legs": len(plan.Legs), "projected_profit": plan.ProjectedProfit,
		"projected_fresh_profit": freshProfit, "projected_held_liquidation": plan.HeldLiquidation,
		"projected_deposit_value": plan.DepositValue,
		"cph":                     plan.ProjectedCreditsPerHour, "model": modelVersion,
	})

	// Execute plan legs; on degradation, re-plan from current position/cargo (bounded
	// by replanLimit PER TOUR).
	replansLeft := replanLimit
	var cumulativeSpend int64
	for {
		degraded, execErr := h.executePlan(ctx, cmd, plan, shadowSinks, response, netBought, &cumulativeSpend, maxSpend, reserve)
		if execErr != nil {
			return false, "", execErr
		}
		if !degraded {
			break
		}
		if replansLeft <= 0 {
			logger.Log("INFO", "Tour re-plan budget exhausted - stopping (any unsold tour cargo will report as stranded)", map[string]interface{}{
				"ship_symbol": cmd.ShipSymbol,
			})
			break
		}
		replansLeft--
		response.Replans++
		ship, err = h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
		if err != nil {
			return false, "", err
		}
		budget := remainingSpend(maxSpend, cumulativeSpend)
		// Re-plan releases this container's prior PLANNED rows and reserves the new plan
		// fresh (planAndReserve), so the replacement plan never double-counts the old
		// one's holds and converted recovery shadows persist (sp-78ai L3).
		var replanFeasible bool
		plan, shadowSinks, _, replanFeasible, err = h.planAndReserve(ctx, cmd, ship, maxHops, budget, reserve, modelVersion)
		if err != nil {
			return false, "", err
		}
		if !replanFeasible {
			logger.Log("INFO", "Re-plan produced no feasible tour - stopping", map[string]interface{}{
				"ship_symbol": cmd.ShipSymbol,
			})
			break
		}
	}
	return true, "", nil
}

// executePlan flies the legs of a single plan. It returns degraded=true when a
// leg's live prices moved past tolerance (the caller re-plans), and a non-nil error
// only on an operational failure the runner should retry. An unroutable leg (gate
// graph drift) is treated as degradation, not a hard failure.
func (h *RunTourCoordinatorHandler) executePlan(
	ctx context.Context,
	cmd *RunTourCoordinatorCommand,
	plan *routing.TourPlan,
	shadowSinks map[shadowSinkKey]bool,
	response *RunTourCoordinatorResponse,
	netBought map[string]int,
	cumulativeSpend *int64,
	maxSpend, reserve int64,
) (bool, error) {
	logger := common.LoggerFromContext(ctx)

	for legIdx, leg := range plan.Legs {
		ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
		if err != nil {
			return false, err
		}
		ship, err = h.legs.travel(ctx, ship, leg.Waypoint, cmd.PlayerID)
		if err != nil {
			if errors.Is(err, gategraph.ErrUnroutable) {
				logger.Log("WARNING", fmt.Sprintf("Leg %d to %s unroutable (gate-graph drift) - degrading to re-plan: %v", legIdx, leg.Waypoint, err), map[string]interface{}{
					"leg": legIdx, "waypoint": leg.Waypoint, "error": err.Error(),
				})
				return true, nil
			}
			return false, fmt.Errorf("travel to leg %d (%s) failed: %w", legIdx, leg.Waypoint, err)
		}
		if err := h.legs.dock(ctx, ship, cmd.PlayerID); err != nil {
			return false, fmt.Errorf("dock at leg %d (%s) failed: %w", legIdx, leg.Waypoint, err)
		}

		legDegraded := false
		// sp-78ai L3: accumulate realized units sold per good at THIS leg, so the sink's
		// recovery shadow is converted ONCE with the full crush (across its price-tiered
		// tranches), not once per tranche. Nil when no ledger is wired.
		legSells := h.newLegSells()
		// Sells before buys (errata): a leg that fills the hold both ways must free
		// space before spending it, and sell tranches are ordered price-ascending.
		for _, trade := range sellsBeforeBuys(leg.Trades) {
			executed, terr := h.executeTrade(ctx, cmd, leg, legIdx, trade, shadowSinks, response, netBought, cumulativeSpend, maxSpend, reserve, legSells)
			if terr != nil {
				return false, terr
			}
			if !executed {
				legDegraded = true // a skipped trade degrades the leg but a still-good sibling trade may proceed
			}
		}
		// Convert this leg's sinks to recovery shadows (per sink as legs complete, design
		// §2) — even on a degraded leg, so the tranches that DID sell shadow their crush.
		h.convertLegShadows(ctx, cmd, leg.Waypoint, legSells)
		response.LegsExecuted++
		if legDegraded {
			return true, nil
		}
	}
	return false, nil
}

// executeTrade live-re-verifies one trade against the plan and, if within tolerance,
// dispatches it. Returns executed=false (a skip) when the live price has degraded past
// tourPriceTolerancePct or cannot be read — the caller degrades the leg and re-plans.
func (h *RunTourCoordinatorHandler) executeTrade(
	ctx context.Context,
	cmd *RunTourCoordinatorCommand,
	leg routing.TourLeg,
	legIdx int,
	trade routing.TourTrade,
	shadowSinks map[shadowSinkKey]bool,
	response *RunTourCoordinatorResponse,
	netBought map[string]int,
	cumulativeSpend *int64,
	maxSpend, reserve int64,
	legSells map[string]*tourSinkSale,
) (bool, error) {
	logger := common.LoggerFromContext(ctx)

	// sp-dchv Lane C: a DEPOSIT tranche is a haul-to-storage transfer, not a market
	// trade — there is no live market bid to re-verify (its value is the synthetic
	// bid). Route it straight to the warehouse deposit path, BYPASSING the
	// live-price observe + tolerance gate the market trades below run.
	if trade.IsDeposit {
		return h.executeDeposit(ctx, cmd, leg, legIdx, trade, response, netBought)
	}

	live, oerr := h.legs.observeGood(ctx, leg.Waypoint, trade.Good, cmd.PlayerID)
	if oerr != nil {
		logger.Log("WARNING", fmt.Sprintf("No live price for %s at %s - skipping (will re-plan): %v", trade.Good, leg.Waypoint, oerr), map[string]interface{}{
			"good": trade.Good, "waypoint": leg.Waypoint, "error": oerr.Error(),
		})
		return false, nil
	}
	planned := trade.ExpectedUnitPrice
	if planned <= 0 {
		return false, nil
	}
	livePrice := live.PurchasePrice() // sell: what the market pays us
	if trade.IsBuy {
		livePrice = live.SellPrice() // buy: what we pay
	}
	degradationPct := math.Abs(float64(livePrice-planned)) / float64(planned) * 100
	if degradationPct > tourPriceTolerancePct {
		logger.Log("WARNING", fmt.Sprintf("Leg %d %s %s: live %d vs planned %d = %.1f%% moved (> %d%%) - skipping, will re-plan",
			legIdx, tradeSide(trade), trade.Good, livePrice, planned, degradationPct, tourPriceTolerancePct), map[string]interface{}{
			"leg": legIdx, "good": trade.Good, "live": livePrice, "planned": planned, "degradation_pct": degradationPct,
		})
		return false, nil
	}

	if trade.IsBuy {
		return h.executeBuy(ctx, cmd, leg, legIdx, trade, shadowSinks, live, response, netBought, cumulativeSpend, maxSpend, reserve)
	}
	return h.executeSell(ctx, cmd, leg, legIdx, trade, live, response, netBought, legSells)
}

func (h *RunTourCoordinatorHandler) executeBuy(
	ctx context.Context,
	cmd *RunTourCoordinatorCommand,
	leg routing.TourLeg,
	legIdx int,
	trade routing.TourTrade,
	shadowSinks map[shadowSinkKey]bool,
	live *market.TradeGood,
	response *RunTourCoordinatorResponse,
	netBought map[string]int,
	cumulativeSpend *int64,
	maxSpend, reserve int64,
) (bool, error) {
	logger := common.LoggerFromContext(ctx)

	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return false, err
	}
	liveAsk := live.SellPrice()
	if liveAsk <= 0 {
		return false, nil
	}
	units := trade.Units
	if space := ship.AvailableCargoSpace(); space < units {
		units = space
	}
	if tv := live.TradeVolume(); tv > 0 && tv < units {
		units = tv // each transaction ≤ tradeVolume
	}
	if maxSpend > 0 {
		remaining := maxSpend - *cumulativeSpend
		if remaining <= 0 {
			logger.Log("WARNING", "Cumulative tour spend cap reached - skipping buy", map[string]interface{}{
				"good": trade.Good, "cap": maxSpend, "spent": *cumulativeSpend,
			})
			return false, nil
		}
		if affordable := int(remaining / int64(liveAsk)); affordable < units {
			units = affordable
		}
	}
	if units <= 0 {
		return false, nil
	}

	// Working-capital spend floor at BUY time (sp-agzj / RULINGS #4). Re-read the
	// LIVE balance immediately before the purchase and SHRINK this tranche to the
	// units the reserve can still afford, rather than the old all-or-nothing skip —
	// a floor that binds should still buy what fits beneath it. Skip only if even
	// one unit pierces the floor; fail CLOSED (no spend, re-plan) if the balance
	// can't be read; proceed unconstrained when no live client is wired (the guard's
	// optional-port contract, which every nil-apiClient test relies on). This shares
	// the circuit's live-treasury seam (reserveHeadroom) rather than forking a
	// parallel read. NOTE: the read is live but not atomic with the purchase, so
	// concurrent hulls draining the shared treasury in the read→buy window remain a
	// residual (sp-78ai); this binds the floor at execution, it does not lock it.
	headroom, liveBalance, guardOn, readable := h.legs.reserveHeadroom(ctx, int(reserve))
	if guardOn && !readable {
		logger.Log("WARNING", fmt.Sprintf("Tour leg %d: live balance unreadable at buy time for %d %s @ %d (reserve %d) - not spending, will re-plan (fail-closed)",
			legIdx, units, trade.Good, liveAsk, reserve), map[string]interface{}{
			"leg": legIdx, "good": trade.Good, "planned_units": units, "ask": liveAsk, "reserve": reserve,
		})
		return false, nil
	}
	if guardOn {
		floorMaxUnits := headroom / liveAsk // floor-respecting max; headroom may be <= 0 (skip)
		if floorMaxUnits <= 0 {
			metrics.RecordTourReserveFloorEngagement(cmd.PlayerID, "skip") // sp-fbih P5: floor bound the whole tranche
			logger.Log("WARNING", fmt.Sprintf("Tour leg %d: buy of %d %s @ %d would breach working-capital floor - live balance %d, reserve %d, even 1 unit pierces - skipping, will re-plan",
				legIdx, units, trade.Good, liveAsk, liveBalance, reserve), map[string]interface{}{
				"leg": legIdx, "good": trade.Good, "planned_units": units, "ask": liveAsk, "live_balance": liveBalance, "reserve": reserve,
			})
			return false, nil
		}
		if floorMaxUnits < units {
			metrics.RecordTourReserveFloorEngagement(cmd.PlayerID, "shrink") // sp-fbih P5: floor cut the tranche to fit
			logger.Log("WARNING", fmt.Sprintf("Tour leg %d: shrinking buy of %s from %d to %d units @ %d to respect working-capital floor (live balance %d, reserve %d)",
				legIdx, trade.Good, units, floorMaxUnits, liveAsk, liveBalance, reserve), map[string]interface{}{
				"leg": legIdx, "good": trade.Good, "planned_units": units, "floor_max_units": floorMaxUnits, "ask": liveAsk, "live_balance": liveBalance, "reserve": reserve,
			})
			units = floorMaxUnits
		}
	}

	plannedAt := h.clock.Now()
	// sp-9mkf: arm the per-tranche buy ceiling at the plan's tolerated ask — the planned
	// basis plus the same tourPriceTolerancePct the leg-level gate above applied. That
	// gate checked only the first live read; this bounds the intra-buy ladder a
	// multi-tranche purchase walks up itself (the D39 stale-ask class), aborting the
	// remainder once a sub-tranche prices past the plan's tolerance.
	planned := trade.ExpectedUnitPrice
	maxAskPerUnit := planned + planned*tourPriceTolerancePct/100
	buyResp, err := h.legs.purchaseWithCeiling(ctx, cmd.ShipSymbol, trade.Good, units, cmd.PlayerID, maxAskPerUnit)
	if err != nil {
		return false, fmt.Errorf("purchase of %d %s at %s failed: %w", units, trade.Good, leg.Waypoint, err)
	}
	if buyResp.UnitsAdded == 0 && buyResp.CeilingAborted {
		logger.Log("WARNING", fmt.Sprintf("Tour leg %d: buy ceiling aborted %s at %s (live ask %d > ceiling %d) - skipping, will re-plan",
			legIdx, trade.Good, leg.Waypoint, buyResp.CeilingObservedAsk, maxAskPerUnit), map[string]interface{}{
			"leg": legIdx, "good": trade.Good, "live_ask": buyResp.CeilingObservedAsk, "ceiling": maxAskPerUnit,
		})
		return false, nil
	}
	*cumulativeSpend += int64(buyResp.TotalCost)
	response.TotalSpent += int64(buyResp.TotalCost)
	response.TradesExecuted++
	netBought[trade.Good] += buyResp.UnitsAdded
	h.recordLeg(ctx, cmd, leg, legIdx, trade, buyResp.UnitsAdded, realizedUnitPrice(buyResp.TotalCost, buyResp.UnitsAdded), plannedAt)
	logger.Log("INFO", fmt.Sprintf("Tour leg %d: bought %d %s at %s (cost %d)", legIdx, buyResp.UnitsAdded, trade.Good, leg.Waypoint, buyResp.TotalCost), nil)
	// sp-8cz9 P2: a buy that LANDED on ground carrying an outstanding EXECUTED recovery
	// shadow is a cross-plan ladder incident — the fleet re-buying into a market still
	// recovering from its own dump. Pure observation off the plan-time probe set; a
	// nil-map read is false, so this is inert when no shadows were netted.
	if buyResp.UnitsAdded > 0 && shadowSinks[shadowSinkKey{leg.Waypoint, trade.Good}] {
		metrics.RecordAbsorptionLadderIncident(cmd.PlayerID, trade.Good)
	}
	return true, nil
}

func (h *RunTourCoordinatorHandler) executeSell(
	ctx context.Context,
	cmd *RunTourCoordinatorCommand,
	leg routing.TourLeg,
	legIdx int,
	trade routing.TourTrade,
	live *market.TradeGood,
	response *RunTourCoordinatorResponse,
	netBought map[string]int,
	legSells map[string]*tourSinkSale,
) (bool, error) {
	logger := common.LoggerFromContext(ctx)

	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return false, err
	}

	// sp-1vhv fail-closed: never sell cargo the hull has reserved as do-not-sell
	// (a staged outfitting module, or an operator-protected good). Skip the leg with
	// a reason=reserved line rather than liquidate a module a coordinator wrongly
	// treated as manifest. tourShipState already keeps reserved cargo out of the
	// planner, so this only fires on a planning leak — the executor refuses
	// independently so a leak can never realize the loss. Returning a skip degrades
	// the leg, and the re-plan (with reserved cargo excluded) drops the doomed sell.
	if ship.IsCargoReserved(trade.Good) {
		logger.Log("INFO", fmt.Sprintf("Tour leg %d: skipped selling %s at %s - cargo is reserved (do-not-sell), held aboard", legIdx, trade.Good, leg.Waypoint), map[string]interface{}{
			"action": "reserved_cargo_skip", "ship_symbol": cmd.ShipSymbol, "good": trade.Good, "waypoint": leg.Waypoint, "reason": "reserved", "leg": legIdx,
		})
		return false, nil
	}

	held := 0
	if c := ship.Cargo(); c != nil {
		held = c.GetItemUnits(trade.Good)
	}
	units := trade.Units
	if held < units {
		units = held
	}
	if tv := live.TradeVolume(); tv > 0 && tv < units {
		units = tv
	}
	if units <= 0 {
		return false, nil // nothing to sell here (cargo already gone) — not a degrade
	}

	plannedAt := h.clock.Now()
	sellResp, err := h.legs.sell(ctx, cmd.ShipSymbol, trade.Good, units, cmd.PlayerID)
	if err != nil {
		return false, fmt.Errorf("sell of %d %s at %s failed: %w", units, trade.Good, leg.Waypoint, err)
	}
	response.TotalRevenue += int64(sellResp.TotalRevenue)
	response.TradesExecuted++
	netBought[trade.Good] -= sellResp.UnitsSold
	// sp-78ai L3: accumulate the realized units sold into this sink for the per-sink
	// conversion at leg completion. The solver splits a sink's A-cap depth into SEPARATE
	// price-tiered tranches (distinct trades), so a single sink can sell across several
	// executeSell calls in one leg; converting per tranche would record only the first,
	// under-stating the very multi-tranche co-dump crush this ledger exists to shadow. The
	// live re-verify tier + trade_volume (stable across a sink's tranches) size the shadow.
	h.noteSinkSale(legSells, trade.Good, sellResp.UnitsSold, live)
	h.recordLeg(ctx, cmd, leg, legIdx, trade, sellResp.UnitsSold, realizedUnitPrice(sellResp.TotalRevenue, sellResp.UnitsSold), plannedAt)
	logger.Log("INFO", fmt.Sprintf("Tour leg %d: sold %d %s at %s (revenue %d)", legIdx, sellResp.UnitsSold, trade.Good, leg.Waypoint, sellResp.TotalRevenue), nil)
	return true, nil
}

// executeDeposit deposits a haul-to-storage tranche into the home warehouse
// (sp-dchv Lane C) using the gas-proven protocol: ReserveSpaceForDeposit →
// TransferCargo (API) → ConfirmDeposit, releasing the reservation on transfer
// failure. It runs NO live-price re-verify (the value is the synthetic bid, not a
// market price) and books ZERO revenue — a deposit is an inventory transfer, not a
// sale, so no ledger transaction row is written (recordLeg is deliberately NOT
// called) and realized P&L is not inflated; the synthetic savings value is logged
// for observability only.
//
// Honest-completion composure (RULINGS #1 / sp-7yej): a successful deposit
// decrements netBought (the good LEFT the hull into inventory — not stranded). A
// deposit that cannot complete (no warehouse, warehouse full/gone) returns a SKIP
// (executed=false) so the leg degrades and the tour re-plans; the un-deposited
// cargo is then carried as held cargo and the next plan liquidates it at market
// (m5kv) rather than stranding it. An API transfer failure returns an error the
// runner retries (it re-plans cargo-aware from the current hold).
func (h *RunTourCoordinatorHandler) executeDeposit(
	ctx context.Context,
	cmd *RunTourCoordinatorCommand,
	leg routing.TourLeg,
	legIdx int,
	trade routing.TourTrade,
	response *RunTourCoordinatorResponse,
	netBought map[string]int,
) (bool, error) {
	logger := common.LoggerFromContext(ctx)

	if h.storageCoordinator == nil || h.warehouseFinder == nil || h.mediator == nil {
		logger.Log("WARNING", fmt.Sprintf("Tour leg %d: deposit of %s planned but storage subsystem unwired - degrading to re-plan (held cargo will liquidate)", legIdx, trade.Good), map[string]interface{}{
			"leg": legIdx, "good": trade.Good, "waypoint": leg.Waypoint,
		})
		return false, nil
	}

	// The deposit sink is the CO-LOCATED warehouse group at the leg's waypoint (sp-5q2c:
	// the anchor plus any additive-capacity siblings). None running → degrade.
	group := h.warehousesAt(ctx, cmd.PlayerID, leg.Waypoint)
	if len(group) == 0 {
		logger.Log("WARNING", fmt.Sprintf("Tour leg %d: no running warehouse at %s for %s deposit - degrading to re-plan (held cargo will liquidate)", legIdx, leg.Waypoint, trade.Good), map[string]interface{}{
			"leg": legIdx, "good": trade.Good, "waypoint": leg.Waypoint,
		})
		return false, nil
	}

	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return false, err
	}
	held := 0
	if c := ship.Cargo(); c != nil {
		held = c.GetItemUnits(trade.Good)
	}
	units := trade.Units
	if held < units {
		units = held
	}
	if units <= 0 {
		return false, nil // nothing to deposit (cargo already gone) — not a degrade
	}

	// Deposit across the group, spilling from the newest member with space into the
	// next as each fills (additive capacity). Each member: reserve atomically → transfer
	// → confirm (Lane B / siphon protocol). "Full" — and the degrade — is reached only
	// when the WHOLE group is saturated.
	deposited := 0
	for deposited < units {
		remaining := units - deposited
		dst := tradingsvc.SelectDepositWarehouse(h.storageCoordinator, group, trade.Good)
		if dst == nil {
			break // every co-located member full or unsupported
		}
		storageShip, reserved, ok := h.storageCoordinator.ReserveSpaceForDeposit(dst.ID(), remaining)
		if !ok || storageShip == nil {
			break // race: space vanished between select and reserve
		}
		move := reserved
		if move > remaining {
			move = remaining
		}
		if _, terr := h.mediator.Send(ctx, &gasCmd.TransferCargoCommand{
			FromShip:   cmd.ShipSymbol,
			ToShip:     storageShip.ShipSymbol(),
			GoodSymbol: trade.Good,
			Units:      move,
			PlayerID:   shared.MustNewPlayerID(cmd.PlayerID),
		}); terr != nil {
			h.storageCoordinator.ReleaseReservedSpace(storageShip.ShipSymbol(), reserved)
			return false, fmt.Errorf("deposit transfer of %d %s to warehouse hull %s failed: %w", move, trade.Good, storageShip.ShipSymbol(), terr)
		}
		h.storageCoordinator.ConfirmDeposit(storageShip.ShipSymbol(), trade.Good, move)
		logger.Log("INFO", fmt.Sprintf("Tour leg %d: deposited %d %s into warehouse %s (savings value %d, no revenue)", legIdx, move, trade.Good, storageShip.WaypointSymbol(), move*trade.ExpectedUnitPrice), map[string]interface{}{
			"leg": legIdx, "good": trade.Good, "units": move, "warehouse": dst.ID(),
			"storage_ship": storageShip.ShipSymbol(), "savings_value": move * trade.ExpectedUnitPrice,
			"operation_type": "warehouse_deposit",
		})
		deposited += move
	}

	if deposited <= 0 {
		logger.Log("WARNING", fmt.Sprintf("Tour leg %d: warehouse group at %s has no space for %d %s (all %d co-located op(s) full) - degrading to re-plan (held cargo will liquidate at market)", legIdx, leg.Waypoint, units, trade.Good, len(group)), map[string]interface{}{
			"leg": legIdx, "good": trade.Good, "units": units, "waypoint": leg.Waypoint, "group_size": len(group),
		})
		return false, nil // full → degrade → next plan liquidates the held cargo (m5kv)
	}

	response.TradesExecuted++
	netBought[trade.Good] -= deposited // left the hull into inventory — not stranded
	return true, nil
}

// warehousesAt returns ALL RUNNING warehouse operations parked at waypoint — the
// co-located additive-capacity group (sp-5q2c: e.g. light-12 + heavy-4B at E42, whose
// slots sum). Empty when none is running there or the finder is unwired (fail closed —
// the caller degrades to pure arb for that leg). A stale sp-3lj5 zombie row is included
// but contributes 0 free space and is never chosen as a deposit target, so aggregation
// composes with the newest-wins zombie fix.
func (h *RunTourCoordinatorHandler) warehousesAt(ctx context.Context, playerID int, waypoint string) []*storage.StorageOperation {
	if h.warehouseFinder == nil {
		return nil
	}
	ops, err := h.warehouseFinder.FindRunning(ctx, playerID)
	if err != nil {
		return nil
	}
	return tradingsvc.RunningWarehousesAtWaypoint(ops, waypoint)
}

// warehouseAt returns the newest RUNNING warehouse operation at waypoint (the group's
// deposit anchor), or nil if none is running there. The deposit path aggregates the
// whole co-located group (warehousesAt); this anchor pick is retained for the sp-3lj5
// regression, where a stale zombie row sits alongside its live replacement at the same
// waypoint — newest-wins ensures the anchor is the live op, and the group aggregation
// independently ensures the zombie's 0-capacity never makes the warehouse look full.
func (h *RunTourCoordinatorHandler) warehouseAt(ctx context.Context, playerID int, waypoint string) *storage.StorageOperation {
	return tradingsvc.SelectNewestRunningWarehouse(h.warehousesAt(ctx, playerID, waypoint))
}

// plan assembles the market snapshot + era-scoped coordinates over the tour graph
// (home system + fresh gate neighbors) and calls the depth-aware planner. The
// constraint carries the resolved model version so the solver fails closed on a
// mismatch rather than silently using a stale model.
func (h *RunTourCoordinatorHandler) plan(
	ctx context.Context,
	ship *navigation.Ship,
	maxHops int,
	maxSpend, reserve int64,
	cmd *RunTourCoordinatorCommand,
	modelVersion string,
) (*routing.TourPlan, []routing.TourGoodSnapshot, []routing.TourMarketAbsorption, error) {
	allowedSystems := h.tourSystems(ctx, ship, cmd.PlayerID)
	return h.planForState(ctx, h.tourShipState(ship), allowedSystems, maxHops, maxSpend, reserve, cmd, modelVersion)
}

// planForState assembles the market snapshot + era-scoped coordinates over allowedSystems
// and calls the depth-aware planner for the given ship state. It is the plan core shared
// by the live tour (plan, above — ship state + tour graph derived from the hull's real
// position) and the sp-zhii reposition pre-flight (planAtCandidate — a SYNTHETIC ship
// state positioned at a candidate system, over that candidate's tour graph, to price the
// tour the hull WOULD fly there without moving it first).
func (h *RunTourCoordinatorHandler) planForState(
	ctx context.Context,
	shipState routing.TourShipState,
	allowedSystems []string,
	maxHops int,
	maxSpend, reserve int64,
	cmd *RunTourCoordinatorCommand,
	modelVersion string,
) (*routing.TourPlan, []routing.TourGoodSnapshot, []routing.TourMarketAbsorption, error) {
	snapshot, waypoints, err := tradingsvc.BuildTourSnapshot(ctx, h.marketRepo, h.waypointRepo, allowedSystems, cmd.PlayerID, h.clock.Now(), maxListingAge)
	if err != nil {
		return nil, nil, nil, err
	}
	// sp-mtvg: make the 1-gate-hop horizon's dropped exotic lanes LOUD. Best-effort and
	// read-only — it never touches snapshot/plan and any error is swallowed (RULINGS #4).
	h.recordUnreachableLanes(ctx, allowedSystems, snapshot, cmd.PlayerID)
	// sp-dchv Lane C: assemble haul-to-storage deposit candidates for the planner to
	// price against arb sells. Empty when pre-positioning is off, no warehouse is in
	// the tour graph, or the capital ceiling is unreadable (fail closed) — the tour
	// then plans pure arb, unchanged.
	deposits := h.depositCandidates(ctx, allowedSystems, cmd.PlayerID, reserve)
	// sp-78ai L3: assemble the outstanding cross-container absorption the solver nets
	// out of available depth so it plans AROUND sinks other containers occupy. Empty
	// when the ledger is unwired / the consult is killed / the read fails (fail-OPEN —
	// the conditional Reserve is the hard backstop), leaving the plan against full depth.
	absorptionView := h.assembleAbsorption(ctx, cmd.PlayerID)
	cons := routing.TourConstraints{
		MaxHops:               maxHops,
		MinMarginPerUnit:      cmd.MinMargin,
		MaxSnapshotAgeMinutes: int(maxListingAge.Minutes()),
		MaxSpend:              maxSpend,
		WorkingCapitalReserve: reserve,
		AllowedSystems:        allowedSystems,
		ExpectedModelVersion:  modelVersion,
	}
	plan, err := h.planner.OptimizeTradeTour(ctx, snapshot, waypoints, shipState, cons, deposits, absorptionView)
	if err != nil {
		return nil, nil, nil, err
	}
	// absorptionView is returned so the accept path can score cap-binding + ladder
	// incidents (sp-8cz9) off the SAME netted depth the solver planned against — no
	// re-read of the ledger. Nil when the ledger is unwired / consult killed, which
	// simply yields no burn-in samples.
	return plan, snapshot, absorptionView, nil
}

// recordUnreachableLanes is the sp-mtvg out-of-horizon lane diagnostic. Given the
// just-built in-scope snapshot, it finds each good the hull can SOURCE cheaply within the
// tour graph whose best sink (across ALL systems) lies OUTSIDE it — a profitable lane the
// 1-gate-hop horizon structurally hides from the solver (the source and its sink never
// co-occur in one snapshot, so no filter ever "rejects" the good; it simply never has a
// sell destination present). It counts every such lane on
// tour_candidates_dropped_total{reason=counterparty_system_unreachable} and names the
// richest few by spread in one log line, converting the silent leak into a legible signal
// so the class can never again be misdiagnosed as a price/volume filter.
//
// Read-only, best-effort, nil-safe: an unset scanner (tests / metrics-disabled), an empty
// snapshot, or any read error yields no diagnostic and never touches the plan (RULINGS #4).
// The guarded 1-hop horizon itself is unchanged — this only makes what it drops visible.
func (h *RunTourCoordinatorHandler) recordUnreachableLanes(
	ctx context.Context,
	allowedSystems []string,
	snapshot []routing.TourGoodSnapshot,
	playerID int,
) {
	if h.sinkScanner == nil || len(snapshot) == 0 {
		return
	}
	goods := inScopeSourcedGoods(snapshot)
	if len(goods) == 0 {
		return
	}
	sinks, err := h.sinkScanner.BestSinksAcrossSystems(ctx, goods, playerID, maxListingAge, h.clock.Now())
	if err != nil || len(sinks) == 0 {
		return
	}
	dropped := computeUnreachableLanes(allowedSystems, snapshot, sinks)
	if len(dropped) == 0 {
		return
	}
	metrics.RecordTourCandidateDropped(playerID, unreachableLaneReason, len(dropped))
	// Name the richest lanes by spread (bounded) so the counter's rate carries exemplars.
	top := dropped
	if len(top) > unreachableLaneLogTopN {
		top = top[:unreachableLaneLogTopN]
	}
	parts := make([]string, 0, len(top))
	for _, l := range top {
		parts = append(parts, fmt.Sprintf("%s %s(%d)->%s@%s(%d) spread %d/u",
			l.Good, l.SourceWaypoint, l.Ask, l.SinkWaypoint, l.SinkSystem, l.Bid, l.Spread))
	}
	common.LoggerFromContext(ctx).Log("INFO", fmt.Sprintf(
		"Tour horizon dropped %d profitable lane(s) whose best sink is beyond the gate-neighbor graph (sp-mtvg): %s",
		len(dropped), strings.Join(parts, "; ")),
		map[string]interface{}{
			"action":          "tour_candidates_dropped",
			"reason":          unreachableLaneReason,
			"count":           len(dropped),
			"allowed_systems": strings.Join(allowedSystems, ","),
		})
}

// unreachableLane is one profitable lane the tour horizon hides: a good sourceable in the
// tour graph whose best sink sits in an out-of-graph system (sp-mtvg).
type unreachableLane struct {
	Good           string
	SourceWaypoint string
	SinkWaypoint   string
	SinkSystem     string
	Ask            int
	Bid            int
	Spread         int
}

// inScopeSourcedGoods returns the goods with a positive in-scope BUY quote (Ask>0) in the
// snapshot — the goods the hull can actually source within the tour graph, the only ones
// whose out-of-graph sinks are a genuine missed lane rather than noise.
func inScopeSourcedGoods(snapshot []routing.TourGoodSnapshot) []string {
	seen := map[string]bool{}
	var goods []string
	for _, r := range snapshot {
		if r.Ask > 0 && !seen[r.Good] {
			seen[r.Good] = true
			goods = append(goods, r.Good)
		}
	}
	return goods
}

// computeUnreachableLanes is the pure detection core of the sp-mtvg diagnostic. For each
// good with a cheap in-scope source (min Ask>0 in the snapshot), it flags the good when
// its best sink (from `sinks`, the global cross-system scan) lies OUTSIDE allowedSystems
// and clears the materiality floor. Returned richest-spread-first. Pure — no clock, no
// metrics, no IO — so the flagging rules are unit-tested directly.
func computeUnreachableLanes(
	allowedSystems []string,
	snapshot []routing.TourGoodSnapshot,
	sinks map[string]market.GlobalSinkResult,
) []unreachableLane {
	cheapestAsk := map[string]int{}
	sourceWp := map[string]string{}
	for _, r := range snapshot {
		if r.Ask <= 0 {
			continue
		}
		if cur, ok := cheapestAsk[r.Good]; !ok || r.Ask < cur {
			cheapestAsk[r.Good] = r.Ask
			sourceWp[r.Good] = r.Waypoint
		}
	}
	allowed := map[string]bool{}
	for _, s := range allowedSystems {
		allowed[s] = true
	}
	var dropped []unreachableLane
	for good, ask := range cheapestAsk {
		sink, ok := sinks[good]
		if !ok || allowed[sink.SystemSymbol] {
			continue // no known sink, or the sink is already reachable in the tour graph
		}
		spread := sink.Bid - ask
		if spread < unreachableLaneMinSpreadPerUnit {
			continue
		}
		dropped = append(dropped, unreachableLane{
			Good: good, SourceWaypoint: sourceWp[good], SinkWaypoint: sink.WaypointSymbol,
			SinkSystem: sink.SystemSymbol, Ask: ask, Bid: sink.Bid, Spread: spread,
		})
	}
	sort.Slice(dropped, func(i, j int) bool { return dropped[i].Spread > dropped[j].Spread })
	return dropped
}

// depositCandidates assembles the haul-to-storage deposit sinks for the planner
// (sp-dchv Lane C), resolving the pre-positioning capital ceiling from live
// treasury first. Returns nil (no deposit legs) when pre-positioning is disabled,
// the storage subsystem is unwired, or the live balance is unreadable — all of
// which leave the tour to plan pure arb.
func (h *RunTourCoordinatorHandler) depositCandidates(ctx context.Context, allowedSystems []string, playerID int, reserve int64) []routing.TourDepositCandidate {
	if !h.prePositioning.Enabled {
		return nil // deliberate off-switch — not a silent zero, no verdict
	}
	if h.storageCoordinator == nil || h.warehouseFinder == nil || h.demandMiner == nil {
		// Enabled but a dependency is unwired: a WIRING BUG, not an off-switch —
		// make it LOUD (sp-dchv observability) rather than silently planning pure arb.
		common.LoggerFromContext(ctx).Log("WARNING",
			"Pre-positioning verdict: 0 deposit candidate(s) — enabled but subsystem unwired (storageCoordinator/warehouseFinder/demandMiner nil)",
			map[string]interface{}{
				"storage_coordinator_wired": h.storageCoordinator != nil,
				"warehouse_finder_wired":    h.warehouseFinder != nil,
				"demand_miner_wired":        h.demandMiner != nil,
			})
		return nil
	}
	ceiling, known := h.depositCapitalCeiling(ctx, reserve)
	return tradingsvc.BuildDepositCandidates(
		ctx, h.demandMiner, h.warehouseFinder, h.storageCoordinator,
		allowedSystems, playerID, ceiling, known, h.prePositioning,
	)
}

// depositCapitalCeiling resolves the pre-positioning capital ceiling:
// depositCeilingPct (default 10) percent of LIVE treasury, held JUNIOR to the
// working-capital reserve (never tie up capital that would breach it). Returns
// known=false when the live balance is UNREADABLE — the assembler then offers no
// candidates (fail closed, RULINGS #4: money guards never spend on an unreadable
// balance). The foreign buys the deposits fund still pass the per-buy
// working-capital floor and the cumulative max-spend cap at execution; this
// ceiling is the pre-positioning-specific budget layered on top.
func (h *RunTourCoordinatorHandler) depositCapitalCeiling(ctx context.Context, reserve int64) (int64, bool) {
	if h.apiClient == nil {
		return 0, false
	}
	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		return 0, false
	}
	agent, err := h.apiClient.GetAgent(ctx, token)
	if err != nil {
		return 0, false
	}
	pct := int64(h.depositCeilingPct)
	if pct <= 0 {
		pct = defaultDepositCeilingPct
	}
	ceiling := int64(agent.Credits) * pct / 100
	if avail := int64(agent.Credits) - reserve; avail < ceiling {
		ceiling = avail // junior to the working-capital reserve
	}
	if ceiling < 0 {
		ceiling = 0
	}
	return ceiling, true
}

// tourSystems is the default tour graph: the hull's current system plus every system
// one gate hop away with fresh market data (the planner scopes each tour to
// maxTourSystems=2 within this allowed set). Neighbor discovery fails open to
// home-only.
func (h *RunTourCoordinatorHandler) tourSystems(ctx context.Context, ship *navigation.Ship, playerID int) []string {
	return h.tourSystemsFrom(ctx, ship.CurrentLocation().SystemSymbol, playerID)
}

// tourSystemsFrom is tourSystems generalized to an arbitrary home system: the given
// system plus every system one gate hop away with fresh market data. The live tour
// centers it on the hull's current system; the sp-zhii reposition pre-flight centers it
// on a candidate system to build that candidate's tour graph. Neighbor discovery fails
// open to home-only.
func (h *RunTourCoordinatorHandler) tourSystemsFrom(ctx context.Context, home string, playerID int) []string {
	systems := []string{home}
	seen := map[string]bool{home: true}
	for _, n := range h.legs.neighborSystems(ctx, home, playerID) {
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		systems = append(systems, n)
	}
	return systems
}

func (h *RunTourCoordinatorHandler) tourShipState(ship *navigation.Ship) routing.TourShipState {
	cargo := map[string]int{}
	if c := ship.Cargo(); c != nil {
		for _, item := range c.Inventory {
			// sp-1vhv: never offer reserved cargo (staged outfitting modules, or an
			// operator-protected good) to the planner as sellable/liquidatable
			// inventory — the tour must not PLAN to sell what the executor will
			// refuse to sell, and its projected profit must not book phantom
			// module-liquidation revenue. Non-reserved held cargo is still carried
			// forward and liquidated as launch inventory (sp-m5kv).
			if ship.IsCargoReserved(item.Symbol) {
				continue
			}
			cargo[item.Symbol] = item.Units
		}
	}
	fuelCurrent, fuelCapacity := 0, ship.FuelCapacity()
	if f := ship.Fuel(); f != nil {
		fuelCurrent, fuelCapacity = f.Current, f.Capacity
	}
	return routing.TourShipState{
		ShipSymbol:      ship.ShipSymbol(),
		CurrentWaypoint: ship.CurrentLocation().Symbol,
		CurrentSystem:   ship.CurrentLocation().SystemSymbol,
		HoldCapacity:    ship.CargoCapacity(),
		FuelCurrent:     fuelCurrent,
		FuelCapacity:    fuelCapacity,
		EngineSpeed:     ship.EngineSpeed(),
		Cargo:           cargo,
	}
}

// defaultMaxSpend resolves the 25%-of-treasury cap (RULINGS #6) when --max-spend is 0.
// It returns (cap, unreadable) so the caller can tell "no treasury source, plan
// uncapped" apart from "have a source but the read FAILED, fail closed" (sp-7z7j) —
// the pre-fix single int64(0) conflated the two, letting a transient read failure
// masquerade as a 0 budget:
//
//   - unreadable=false, cap>0  → live treasury read; size the tour to 25% of it.
//   - unreadable=false, cap=0  → NO apiClient wired at all (structural; the daemon
//     always wires one, so this is the test-harness / pure-env path). 0 is "no explicit
//     cumulative cap" — the per-buy working-capital floor still guards every spend.
//   - unreadable=true,  cap=0  → a treasury SOURCE is wired but the live read FAILED
//     (no player token, or GetAgent errored). The caller MUST fail closed: never spend
//     on this, never fall back to unlimited or a stale budget — pause and retry so a
//     continuous (--iterations -1) loop survives the transient (the exact 5/5 field
//     repro where a shared-agent GetAgent blip completed every hull after one iteration).
func (h *RunTourCoordinatorHandler) defaultMaxSpend(ctx context.Context) (int64, bool) {
	logger := common.LoggerFromContext(ctx)
	if h.apiClient == nil {
		return 0, false // no treasury source wired — 0 = no explicit cap (floor guards)
	}
	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		logger.Log("WARNING", "Cannot re-resolve dynamic tour max-spend: player token unavailable - failing closed (will not spend uncapped)", map[string]interface{}{
			"error": err.Error(),
		})
		return 0, true // source exists but UNREADABLE (no token) — fail closed
	}
	agent, err := h.apiClient.GetAgent(ctx, token)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Cannot re-resolve dynamic tour max-spend: live treasury read failed (%v) - failing closed (will not spend uncapped)", err), map[string]interface{}{
			"error": err.Error(),
		})
		return 0, true // source exists but UNREADABLE (read failed) — fail closed
	}
	spendCap := int64(agent.Credits) * tourDefaultMaxSpendTreasuryPct / 100
	logger.Log("INFO", fmt.Sprintf("Default tour max-spend = %d (25%% of live treasury %d)", spendCap, agent.Credits), map[string]interface{}{
		"max_spend": spendCap, "treasury": agent.Credits,
	})
	return spendCap, false
}

// strandedReason reports whether any good the tour bought is still aboard (net
// bought minus sold > 0) — an honest-completion veto. The message names each good,
// its stranded units, and the hull's current location so the strand is greppable
// and hand-recoverable.
func (h *RunTourCoordinatorHandler) strandedReason(ctx context.Context, cmd *RunTourCoordinatorCommand, netBought map[string]int) (string, bool) {
	var parts []string
	for good, net := range netBought {
		if net > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", net, good))
		}
	}
	if len(parts) == 0 {
		return "", false
	}
	sort.Strings(parts)
	loc := "unknown"
	if ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID); err == nil {
		loc = ship.CurrentLocation().Symbol
	}
	return fmt.Sprintf("stranded cargo: %s still aboard at %s (tour-bought, unsold) - reporting failure", strings.Join(parts, ", "), loc), true
}

func (h *RunTourCoordinatorHandler) recordLeg(
	ctx context.Context,
	cmd *RunTourCoordinatorCommand,
	leg routing.TourLeg,
	legIdx int,
	trade routing.TourTrade,
	realizedUnits, realizedUnitPrice int,
	plannedAt time.Time,
) {
	if h.telemetry == nil {
		return
	}
	err := h.telemetry.RecordLeg(ctx, trading.TourLegTelemetry{
		TourID:            cmd.ContainerID,
		ShipSymbol:        cmd.ShipSymbol,
		LegIndex:          legIdx,
		Waypoint:          leg.Waypoint,
		Good:              trade.Good,
		IsBuy:             trade.IsBuy,
		PlannedUnits:      trade.Units,
		RealizedUnits:     realizedUnits,
		PlannedUnitPrice:  trade.ExpectedUnitPrice,
		RealizedUnitPrice: realizedUnitPrice,
		PlannedAt:         plannedAt,
		RealizedAt:        h.clock.Now(),
		PlayerID:          cmd.PlayerID,
	})
	if err != nil {
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf("Failed to record tour leg telemetry: %v", err), map[string]interface{}{
			"tour": cmd.ContainerID, "leg": legIdx, "good": trade.Good, "error": err.Error(),
		})
	}
}

// readTourModelVersion reads "<fit_version>@<era>" from the checked-in artifact so the
// constraint binds the planner to the exact fitted model (spec: mismatch → the solver
// fails closed). Any read/parse failure surfaces as an error the caller fails open on.
func readTourModelVersion(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read model artifact: %w", err)
	}
	var art struct {
		FitVersion int    `json:"fit_version"`
		Era        string `json:"era"`
	}
	if err := json.Unmarshal(data, &art); err != nil {
		return "", fmt.Errorf("parse model artifact: %w", err)
	}
	if art.Era == "" {
		return "", fmt.Errorf("model artifact missing era")
	}
	return fmt.Sprintf("%d@%s", art.FitVersion, art.Era), nil
}

// sellsBeforeBuys reorders a leg's trades so every sell precedes every buy, preserving
// relative order within each side (the planner emits them this way; the executor
// enforces it so the hold is freed before it is refilled).
func sellsBeforeBuys(trades []routing.TourTrade) []routing.TourTrade {
	out := make([]routing.TourTrade, 0, len(trades))
	for _, t := range trades {
		if !t.IsBuy {
			out = append(out, t)
		}
	}
	for _, t := range trades {
		if t.IsBuy {
			out = append(out, t)
		}
	}
	return out
}

func remainingSpend(maxSpend, spent int64) int64 {
	if maxSpend <= 0 {
		return 0 // no explicit cap
	}
	if r := maxSpend - spent; r > 0 {
		return r
	}
	return 0
}

func realizedUnitPrice(total, units int) int {
	if units <= 0 {
		return 0
	}
	return total / units
}

func tradeSide(t routing.TourTrade) string {
	if t.IsBuy {
		return "buy"
	}
	return "sell"
}

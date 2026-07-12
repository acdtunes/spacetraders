package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

const (
	// Config defaults (RULINGS #5: every operational value is a config key, filled here only when
	// the launch config leaves it unset — the Analyst/Admiral own the numbers). Documented on
	// config.BootstrapConfig.
	defaultBootstrapTickSeconds = 300 // 5min — cold-start is a slow, deliberate ramp
	defaultProbeTarget          = 3   // DATA target: 3 probes scouting so market data flows ASAP
	defaultCoverageBar          = 0.9 // DATA→exit: 90% of home-system marketplaces fresh
	defaultReserveMargin        = 0.5 // spend ≤ 50% of treasury per decision (guardrail + pacer)
	// defaultProbeShipType is the shipyard ship-type symbol bought for a probe (RULINGS #5: even
	// the asset is a knob).
	defaultProbeShipType = "SHIP_PROBE"

	// INCOME-phase defaults (Slice 2).
	defaultHaulerTarget = 4 // INCOME hull cap: one hauler per viable contract hub, up to 4 (spec 4–5)
	// defaultIncomeBar is the INCOME→GATE exit: realized NET credits/hour the contract fleet must
	// clear before the arc drives gate construction. Deliberately CONSERVATIVE (a clearly-earning but
	// not-huge bar): the Phase-1 objective is building the gate, so the worse failure is a bar set so
	// HIGH the arc never reaches GATE — a lower bar only risks starting GATE with a still-warming
	// fleet. This is the primary field-calibration knob (spec Slice-2 open question).
	defaultIncomeBar = 10000.0
	// defaultMinContractEarners is how many haulers stay on contracts through GATE to keep funding
	// material acquisition (consumed by the GATE phase in Slice 3; plumbed here with the INCOME ramp).
	defaultMinContractEarners = 1
	// defaultHaulerShipType is the shipyard ship-type bought for a contract hauler (RULINGS #5: the
	// asset is a knob). A light hauler is the cold-start contract workhorse (cheap, adequate cargo).
	defaultHaulerShipType = "SHIP_LIGHT_HAULER"

	// GATE-phase defaults (Slice 3).
	// defaultGateWorkerTarget caps gate-construction workers (actual = ~one per active gate-material
	// chain + a delivery hauler, up to this). 6 covers a typical jump-gate material shape (a handful of
	// producing chains + delivery) without letting a wide pipeline drain the treasury; the Analyst tunes
	// it. The worker pool is mostly REPURPOSED idle contract haulers, so this rarely drives a buy.
	defaultGateWorkerTarget = 6
	// gateDeliveryHaulers is the small fixed delivery allowance added to the per-chain worker target
	// (spec §Fleet scaling: "~one worker per active gate-material chain + 1–2 delivery haulers"). Kept a
	// call-site constant (not a knob) — it is a shape detail of the sizing formula, bounded by
	// gate_worker_target which IS the operator-reachable cap.
	gateDeliveryHaulers = 1
)

// ShipRefresher forces a live re-read of the player's hulls before any role/assignment decision —
// the phantom-cache guard (captain L47): the ship cache desyncs (a phantom-idle hull misread as
// busy, or vice-versa), so the reconciler refreshes the pool at the top of every tick. An error
// fails the tick CLOSED (no action) rather than acting on stale state.
type ShipRefresher interface {
	RefreshFleet(ctx context.Context, playerID int) error
}

// WorldObserver reads the live-world Observation for a tick (the game is the source of truth). An
// unreadable input must be surfaced as Observation{Readable:false, Reason:...}, NOT an error, so a
// transient read miss fails closed (no action) without aborting the loop; a returned error is an
// infra fault the coordinator logs and skips the tick on.
type WorldObserver interface {
	Observe(ctx context.Context, playerID int) (Observation, error)
}

// ProbeAcquirer price-checks and buys probes (reuses shipyard list + shipyard purchase). PriceCheck
// reads the cheapest reachable yard's ask for shipType; readable=false ⇒ the capital gate fails
// closed (no buy). Buy purchases exactly one shipType at yard.
type ProbeAcquirer interface {
	PriceCheck(ctx context.Context, playerID int, shipType string) (price int64, yard string, readable bool, err error)
	Buy(ctx context.Context, playerID int, shipType, yard string) (BuyResult, error)
}

// ScoutAssigner assigns every probe/satellite in a system to scout all its markets (reuses
// workflow scout-all-markets' VRP fleet assignment). It is idempotent — re-running re-optimizes
// across the current probe set — so the reconciler can call it whenever a probe is not yet scouting.
type ScoutAssigner interface {
	AssignAllMarkets(ctx context.Context, playerID int, system string) error
}

// MetricsSink records the bootstrap's observation series (spec §Observability). Pure observation:
// nil-safe and best-effort, a recording miss never touches a decision.
type MetricsSink interface {
	// RecordPhase sets the derived-phase gauge (spacetraders_bootstrap_phase{phase}).
	RecordPhase(phase string)
	// RecordProbePurchased increments the probes-bought counter (once per executed DATA buy).
	RecordProbePurchased()
	// RecordHaulerPurchased increments the haulers-bought counter (once per executed INCOME buy).
	RecordHaulerPurchased()
	// RecordConstructionPct sets the gate construction-progress gauge [0,100] (GATE phase).
	RecordConstructionPct(pct float64)
}

// FrigateRetirer clears the command frigate's contract-fleet dedication (reuses fleet unassign —
// AssignShipFleetCommand with Fleet=""). It is the "retire the frigate from contract work" action: a
// frigate is a poor contract worker (low fuel/cargo), so it must not sit in the contract coordinator's
// dedicated pool. Idempotent at the adapter (a clear on an untagged hull is a no-op); the reconciler
// still guards on the observation so a stale tag is cleared exactly once.
type FrigateRetirer interface {
	RetireFromContract(ctx context.Context, playerID int, shipSymbol string) error
}

// HaulerAcquirer price-checks and buys ONE light hauler, then dedicates it to the contract fleet and
// places it on its hub (reuses shipyard list/purchase + fleet assign + navigate). Mirrors
// ProbeAcquirer but folds the dedicate+placement into the buy, because a contract hauler is a
// dedicated, positioned hull — not a free scout. PriceCheck reads the cheapest reachable yard's ask
// for shipType (readable=false ⇒ the capital gate fails closed).
type HaulerAcquirer interface {
	PriceCheck(ctx context.Context, playerID int, shipType string) (price int64, yard string, readable bool, err error)
	BuyAndPlace(ctx context.Context, playerID int, shipType, yard, hubWaypoint string) (BuyResult, error)
}

// ContractRunner launches the contract fleet coordinator (workflow batch-contract) for a player
// (reuses the existing ContractFleetCoordinator launch). The reconciler calls Start only when the
// observation reports it is not already running, so the launch is idempotent; Start is best-effort and
// its error is logged, not fatal.
type ContractRunner interface {
	StartBatchContract(ctx context.Context, playerID int) error
}

// --- GATE-phase collaborators (Slice 3). Each is nil-safe (a nil collaborator degrades the GATE
// action it drives to a logged skip surfaced as a blocker, never a panic). ---

// ConstructionManager starts the jump-gate construction pipeline (reuses `construction start`). Start
// is idempotent at the adapter (it RESUMES when a pipeline already exists), and the reconciler also
// guards on obs.ConstructionStarted, so the pipeline is created exactly once even across a restart.
type ConstructionManager interface {
	Start(ctx context.Context, playerID int, site string) error
}

// ManufacturingController manages the construction EXECUTOR — the manufacturing coordinator that claims
// worker hulls and runs produce/deliver for the pipeline's tasks. EnsureRunning launches it if down (a
// fresh start ADOPTS existing pipelines). BounceForAdoption restarts a running-but-unadopted executor so
// it adopts a freshly-created pipeline (captain L57: a new pipeline is INERT until the executor adopts it
// at startup). Both are idempotent at the adapter and guarded on the observation, so neither double-acts.
type ManufacturingController interface {
	EnsureRunning(ctx context.Context, playerID int) error
	BounceForAdoption(ctx context.Context, playerID int) error
}

// WorkerRepurposer releases a contract-dedicated income hauler back to the idle pool (reuses fleet
// unassign — the same tag-clear as retiring the frigate) so the manufacturing coordinator claims it as
// a gate-construction worker. This is the "repurpose idle INCOME haulers FIRST" seed (spec §Fleet
// scaling): the income fleet becomes the seed construction workforce before any hull is bought.
type WorkerRepurposer interface {
	RepurposeToConstruction(ctx context.Context, playerID int, shipSymbol string) error
}

// GateWorkerAcquirer price-checks and buys ONE gate-construction worker hull and dedicates it to
// construction (reuses shipyard purchase + fleet assign). The staged top-up when repurposed haulers
// don't cover the pipeline's shape. Mirrors HaulerAcquirer but does not place on a hub (the executor
// claims the worker); PriceCheck readable=false ⇒ the capital gate fails closed (no buy).
type GateWorkerAcquirer interface {
	PriceCheck(ctx context.Context, playerID int, shipType string) (price int64, yard string, readable bool, err error)
	BuyForConstruction(ctx context.Context, playerID int, shipType, yard string) (BuyResult, error)
}

// AcquisitionTracker reports whether an acquisition the coordinator ALREADY dispatched for a
// (player, shipType) is still IN FLIGHT — a batch-purchase still navigating its buyer to the yard and
// buying, whose new hull has not yet surfaced in the observation. The reconciler consults it before
// every staged buy (probe / hauler / gate worker), so a read-after-write lag — or a compressed tick
// firing before the prior buy's hull appears in the fleet read — never re-dispatches a purchase that is
// already on its way. The invariant (st-drm.6): at most ONE in-flight acquisition per (player, shipType);
// a tick that observes need>0 while one is active dispatches NOTHING, and the first tick after it lands
// re-derives need from the world and proceeds. nil-safe: unset ⇒ no tracking (the legacy count-only
// staging), so a coordinator wired without it behaves exactly as before.
type AcquisitionTracker interface {
	InFlight(ctx context.Context, playerID int, shipType string) (bool, error)
}

// HandoffLauncher performs the COMPLETE hand-off: it launches the standing fleet-autosizer (OFF the whole
// bootstrap run so the two never issue conflicting purchases against one treasury) and the other standing
// coordinators, turning the fleet over to the mature demand-driven economy. Guarded on obs.AutosizerRunning
// so a restart post-COMPLETE re-observes the autosizer running and never re-launches.
type HandoffLauncher interface {
	LaunchAutosizer(ctx context.Context, playerID int, agentSymbol string) error
	LaunchStandingCoordinators(ctx context.Context, playerID int, agentSymbol string) error
}

// RunBootstrapCoordinatorCommand launches the standing bootstrap coordinator for a player (sp-3nbe).
// Like the fleet-autosizer / siting coordinators it runs an infinite reconcile loop inside a single
// Handle() call; the container wraps it. All knobs are launch-config keys (RULINGS #5); the zero
// value falls back to the documented default, so the CLI/daemon passes only what it overrides.
type RunBootstrapCoordinatorCommand struct {
	PlayerID    int
	ContainerID string
	AgentSymbol string

	// Disabled is the master boot-gate (negation of bootstrap_disabled so an absent key reads as
	// ENABLED — LIVE BY DEFAULT, Admiral no-dark-shipping). The container stays resident when
	// disabled so a config flip + restart re-arms it, but it takes no action while stood down.
	Disabled bool
	// DryRun observes + logs the decisions it WOULD take and takes none. It WARNs every tick — not
	// a silent no-op (the f5pr silent-dry-run lesson).
	DryRun bool

	TickIntervalSecs int
	ProbeTarget      int
	CoverageBar      float64
	ReserveMargin    float64
	ProbeShipType    string

	// INCOME-phase knobs (Slice 2, RULINGS #5; the zero value defers to the documented default).
	HaulerTarget       int     // INCOME hull cap — actual = one per viable contract hub, up to this.
	IncomeBar          float64 // INCOME→GATE exit: realized net credits/hour the fleet must clear.
	MinContractEarners int     // haulers kept on contracts through GATE (consumed in Slice 3).
	HaulerShipType     string  // the shipyard ship-type bought for a contract hauler.

	// GATE-phase knob (Slice 3, RULINGS #5; the zero value defers to the documented default).
	GateWorkerTarget int // GATE worker cap — actual = ~one per active gate-material chain + delivery.
}

// RunBootstrapCoordinatorResponse reports reconcile progress. Because the loop is infinite it is
// only observed on context cancellation (shutdown).
type RunBootstrapCoordinatorResponse struct {
	Ticks  int
	Errors []string
}

// RunBootstrapCoordinatorHandler reconciles a cold agent toward the jump gate. It holds NO
// in-memory progress state: progress is ALWAYS re-derived from the live observation each tick
// (spec §Minimal persisted state), so a mid-flight crash is a non-event — a restart re-observes and
// resumes at real state. Collaborators are wired by setters at boot; each is nil-safe (a nil
// collaborator degrades to a logged skip, never a panic).
type RunBootstrapCoordinatorHandler struct {
	clock shared.Clock

	refresher ShipRefresher
	observer  WorldObserver
	acquirer  ProbeAcquirer
	scouter   ScoutAssigner
	metrics   MetricsSink

	// acquisition guards every staged buy against re-dispatching while a same-type acquisition it
	// already launched is still in flight (st-drm.6). nil-safe: unset ⇒ the legacy count-only staging.
	acquisition AcquisitionTracker

	// INCOME-phase collaborators (Slice 2). Each is nil-safe: a nil collaborator degrades the INCOME
	// action it drives to a logged skip (surfaced as a blocker), never a panic.
	retirer      FrigateRetirer
	haulAcquirer HaulerAcquirer
	contractRun  ContractRunner

	// GATE-phase collaborators (Slice 3). Same nil-safe contract.
	construction  ConstructionManager
	manufacturing ManufacturingController
	repurposer    WorkerRepurposer
	gateAcquirer  GateWorkerAcquirer
	handoff       HandoffLauncher
}

// NewRunBootstrapCoordinatorHandler wires the coordinator. clock defaults to the real clock when
// nil (production). The observer/acquirer/scouter/refresher/metrics are wired with their setters.
func NewRunBootstrapCoordinatorHandler(clock shared.Clock) *RunBootstrapCoordinatorHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &RunBootstrapCoordinatorHandler{clock: clock}
}

// SetShipRefresher wires the phantom-cache-guard fleet refresh (captain L47). Unset → the guard is
// skipped (logged), which the tests pin against.
func (h *RunBootstrapCoordinatorHandler) SetShipRefresher(r ShipRefresher) { h.refresher = r }

// SetWorldObserver wires the live-world observation source. Unset → the tick cannot observe and is
// a logged no-op.
func (h *RunBootstrapCoordinatorHandler) SetWorldObserver(o WorldObserver) { h.observer = o }

// SetProbeAcquirer wires the price-check + buy path (reuses shipyard list/purchase). Unset → the
// coordinator evaluates and logs but never spends (an implicit dry-run, surfaced loudly).
func (h *RunBootstrapCoordinatorHandler) SetProbeAcquirer(a ProbeAcquirer) { h.acquirer = a }

// SetScoutAssigner wires the scout-all-markets assignment (reuses the VRP fleet assignment). Unset
// → probes are bought but not assigned (surfaced loudly).
func (h *RunBootstrapCoordinatorHandler) SetScoutAssigner(s ScoutAssigner) { h.scouter = s }

// SetAcquisitionTracker wires the in-flight-acquisition guard (st-drm.6): a same-type buy is not
// re-dispatched while a prior one is still on its way to landing in the observation. Unset ⇒ no
// tracking (the legacy count-only staging), so existing wiring/tests are unaffected.
func (h *RunBootstrapCoordinatorHandler) SetAcquisitionTracker(t AcquisitionTracker) { h.acquisition = t }

// SetMetricsSink wires the metrics recorder. Optional and nil-safe (pure observation).
func (h *RunBootstrapCoordinatorHandler) SetMetricsSink(m MetricsSink) { h.metrics = m }

// SetFrigateRetirer wires the "retire the frigate from contract work" action (reuses fleet unassign).
// Unset → the retire is a logged skip.
func (h *RunBootstrapCoordinatorHandler) SetFrigateRetirer(r FrigateRetirer) { h.retirer = r }

// SetHaulerAcquirer wires the price-check + buy-and-place-on-hub path (reuses shipyard purchase +
// fleet assign + navigate). Unset → INCOME evaluates and logs but never buys a hauler.
func (h *RunBootstrapCoordinatorHandler) SetHaulerAcquirer(a HaulerAcquirer) { h.haulAcquirer = a }

// SetContractRunner wires the batch-contract launch (reuses the contract fleet coordinator). Unset →
// haulers are placed but batch-contract is not driven (surfaced loudly).
func (h *RunBootstrapCoordinatorHandler) SetContractRunner(c ContractRunner) { h.contractRun = c }

// SetConstructionManager wires `construction start` (reuses the construction pipeline planner). Unset →
// GATE evaluates and logs but never starts the pipeline (surfaced loudly).
func (h *RunBootstrapCoordinatorHandler) SetConstructionManager(c ConstructionManager) {
	h.construction = c
}

// SetManufacturingController wires the construction-executor ensure/bounce (the manufacturing
// coordinator). Unset → GATE cannot ensure the executor or perform the L57 adoption bounce (surfaced loudly).
func (h *RunBootstrapCoordinatorHandler) SetManufacturingController(m ManufacturingController) {
	h.manufacturing = m
}

// SetWorkerRepurposer wires the "release an income hauler to construction" action (reuses fleet
// unassign). Unset → GATE cannot repurpose haulers and top-up buys carry the whole worker load (surfaced loudly).
func (h *RunBootstrapCoordinatorHandler) SetWorkerRepurposer(r WorkerRepurposer) { h.repurposer = r }

// SetGateWorkerAcquirer wires the price-check + buy-for-construction path (reuses shipyard purchase +
// fleet assign). Unset → GATE repurposes but never buys the top-up delta (surfaced loudly).
func (h *RunBootstrapCoordinatorHandler) SetGateWorkerAcquirer(a GateWorkerAcquirer) {
	h.gateAcquirer = a
}

// SetHandoffLauncher wires the COMPLETE hand-off (launch the autosizer + standing coordinators). Unset →
// the gate completes but the hand-off is a logged skip, so the mature economy is not launched (surfaced loudly).
func (h *RunBootstrapCoordinatorHandler) SetHandoffLauncher(l HandoffLauncher) { h.handoff = l }

// Handle runs the reconcile loop until the context is cancelled.
func (h *RunBootstrapCoordinatorHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	logger := common.LoggerFromContext(ctx)

	cmd, ok := request.(*RunBootstrapCoordinatorCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	cfg := resolveBootstrapConfig(cmd)
	logger.Log("INFO", fmt.Sprintf("Bootstrap coordinator starting (tick %s, dry_run=%v, disabled=%v, probe_target=%d, coverage_bar=%.2f, reserve_margin=%.2f, hauler_target=%d, income_bar=%.0f, min_contract_earners=%d)", cfg.Tick, cfg.DryRun, cfg.Disabled, cfg.ProbeTarget, cfg.CoverageBar, cfg.ReserveMargin, cfg.HaulerTarget, cfg.IncomeBar, cfg.MinContractEarners), map[string]interface{}{
		"action":       "bootstrap_start",
		"container_id": cmd.ContainerID,
		"dry_run":      cfg.DryRun,
		"disabled":     cfg.Disabled,
	})

	result := &RunBootstrapCoordinatorResponse{Errors: []string{}}

	for {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

		res, err := h.reconcileOnce(ctx, cmd)
		if err != nil {
			result.Errors = append(result.Errors, err.Error())
			logger.Log("ERROR", fmt.Sprintf("Bootstrap reconcile failed: %v", err), nil)
		}
		result.Ticks++

		// Terminal COMPLETE: the gate is built and the standing economy is handed off, so the coordinator
		// has finished its job and exits cleanly (spec §Architecture: "then exits COMPLETE"). A restart
		// post-COMPLETE re-derives COMPLETE, re-observes the hand-off done, and exits again — idempotent.
		if res.Done {
			logger.Log("INFO", "Bootstrap coordinator exiting: COMPLETE reached and handed off", map[string]interface{}{
				"action":       "bootstrap_exit_complete",
				"container_id": cmd.ContainerID,
			})
			return result, nil
		}

		select {
		case <-time.After(cfg.Tick):
		case <-ctx.Done():
			return result, ctx.Err()
		}
	}
}

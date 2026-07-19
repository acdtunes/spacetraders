package commands

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

const (
	// Config defaults (RULINGS #5: every operational value is a config key, filled here only when
	// the launch config leaves it unset — the Analyst/Admiral own the numbers). Documented on
	// config.BootstrapConfig.
	// defaultBootstrapTickSeconds is the cold-start reconcile cadence. SHORT on purpose (sp-lgo3):
	// bootstrap runs ONLY during cold start — 1 frigate + 1-3 probes make <0.1 req/s vs the 2 req/s
	// ACCOUNT limit (20x+ headroom) and it exits at COMPLETE before the fleet is ever large, so a fast
	// tick carries zero API-pacing concern for its whole lifetime. The old 300s injected up to 5min of
	// dead time between a real event (frigate docks, scan/arrival completes) and the coordinator
	// reacting — almost all of the observed ~11min probe-buy was poll latency, not travel. 45s cuts
	// time-to-gate (→ more Phase-2 time → higher rank) with ample headroom. Made SAFE against the
	// fresh-buy over-buy the short tick would otherwise expose by the sp-lgo3 count-sync bridge (PART 1).
	// Live-tunable via the tick_secs knob (bounds 10..86400) with no restart. Event-driven reaction
	// (react to arrival/scan-complete via the wake/watch model instead of a fixed poll) is a future
	// follow-up — the tick drop is the scoped fix.
	defaultBootstrapTickSeconds = 45
	defaultProbeTarget          = 3   // DATA target: 3 probes scouting so market data flows ASAP
	defaultCoverageBar          = 0.9 // DATA→exit: 90% of home-system marketplaces fresh
	defaultReserveMargin        = 0.5 // spend ≤ 50% of treasury per decision (guardrail + pacer)
	// defaultProbeShipType is the shipyard ship-type symbol bought for a probe (RULINGS #5: even
	// the asset is a knob).
	defaultProbeShipType = "SHIP_PROBE"

	// INCOME-phase defaults.
	defaultHaulerTarget = 4 // INCOME hull cap: one hauler per viable contract hub, up to 4 (spec 4–5)
	// defaultIncomeBar is the INCOME→GATE exit: realized NET credits/hour the contract fleet must
	// clear before the arc drives gate construction. Deliberately CONSERVATIVE (a clearly-earning but
	// not-huge bar): the Phase-1 objective is building the gate, so the worse failure is a bar set so
	// HIGH the arc never reaches GATE — a lower bar only risks starting GATE with a still-warming
	// fleet. This is the primary field-calibration knob (an open tuning question).
	defaultIncomeBar = 10000.0
	// defaultMinContractEarners is how many haulers stay on contracts through GATE to keep funding
	// material acquisition (consumed by the GATE phase; plumbed here with the INCOME ramp).
	defaultMinContractEarners = 1
	// defaultHaulerShipType is the shipyard ship-type bought for a contract hauler (RULINGS #5: the
	// asset is a knob). A light hauler is the cold-start contract workhorse (cheap, adequate cargo).
	defaultHaulerShipType = "SHIP_LIGHT_HAULER"

	// defaultContractWorkingCapitalFloor is the ABSOLUTE cash cushion (whole credits) the treasury must
	// still clear AFTER a staged INCOME hauler buy: the buy is affordable when treasury−price ≥ this
	// floor (sp-acv5, PLAYBOOK §3). It replaces the old PROPORTIONAL reserve_margin×treasury hauler gate,
	// which only bought once treasury grew past ~2× the hauler price and so delayed the cash-flow scaling
	// the hauler exists to provide. 50k ≈ one light-hauler contract cycle's goods+fuel working capital (a
	// ~full cargo of contract goods at typical commodity prices — tens of k — plus fuel and a safety
	// buffer), so a permitted buy always leaves the contract operation funded. It is the Admiral's
	// IMMUTABLE working-capital floor (RULINGS #5 + 2026-07-18 Amendment: "the immutable 50k
	// working-capital floor … deliberately non-tunable per-run"): a documented hard constant, NOT a
	// live-tunable / config.yaml knob, and NOT the shared reserve_margin (which still paces the DATA probe
	// buy). Its own dedicated parameter so the broader treasury-floor work (sp-ktio) builds on it.
	defaultContractWorkingCapitalFloor int64 = 50_000

	// GATE-phase defaults.
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

	// defaultDeferProbeToFreshsizer is the sp-tsn2 single-buyer-arbitration flag default: 0 = OFF
	// (byte-identical — bootstrap and freshsizer each buy behind their own guards). Armed to 1 via
	// `tune --operation bootstrap defer_probe_to_freshsizer 1`, bootstrap hands probe acquisition to
	// the freshsizer once the first market is covered (coverage>0) and a freshsizer coordinator runs.
	defaultDeferProbeToFreshsizer = 0
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

// ShipyardScanner positions an idle hull AT a home-system shipyard so the NEXT tick's live PriceCheck
// returns priced listings (sp-hh0h). The cold-start deadlock it breaks: on a fresh universe nothing
// has ever visited the home shipyard, its live ship listing is presence-gated (empty unless a hull is
// there), so PriceCheck reads unreadable and every probe buy fails closed FOREVER — DATA never leaves
// the ground without a captain. This does NOT weaken the price guard (RULINGS #4): a genuinely
// unreadable price still buys nothing this tick; the scanner makes the price READABLE by getting a hull
// to the yard, so the guard clears on evidence. EnsureHomeShipyardReadable is idempotent and
// best-effort — dispatched=false when a hull is already present/en route at a shipyard (just wait) or no
// idle hull is free to go — so re-evaluation each unreadable tick never re-navigates or thrashes. Unset
// (nil) → the reconciler preserves the pre-hh0h fail-closed behavior (byte-identical).
type ShipyardScanner interface {
	EnsureHomeShipyardReadable(ctx context.Context, playerID int, homeSystem string) (dispatched bool, err error)
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

// FrigateContractLoopStarter starts the command frigate's OWN continuous single-hull contract loop
// (sp-rype), reusing the sp-ehg9 batch-contract --loop primitive (DaemonServer.BatchContractWorkflow
// with iterations=-1). This is the pre-hauler frigate EARNER: after the frigate finishes its hour-0
// shipyard run + probe buy it must run contracts as the sole earner rather than park idle at the yard
// (the sp-rype stall — the contract_fleet_coordinator does not keep the frigate earning: sp-ehg9). The
// reconciler calls StartLoop only when provisioning is done AND no loop is already running
// (obs.FrigateContractLoopRunning), so the start is idempotent; the daemon's per-player
// single-CONTRACT_WORKFLOW guard is the atomic backstop, so a duplicate start is a benign no-op. Unset
// (nil) ⇒ the frigate-earner action is a logged skip (byte-identical to pre-sp-rype).
type FrigateContractLoopStarter interface {
	StartLoop(ctx context.Context, playerID int, frigateSymbol string) error
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

// HandoffLauncher performs the COMPLETE hand-off: it launches the standing fleet-autosizer (OFF the whole
// bootstrap run so the two never issue conflicting purchases against one treasury) and the other standing
// coordinators, turning the fleet over to the mature demand-driven economy. Guarded on obs.AutosizerRunning
// so a restart post-COMPLETE re-observes the autosizer running and never re-launches.
type HandoffLauncher interface {
	LaunchAutosizer(ctx context.Context, playerID int, agentSymbol string) error
	LaunchStandingCoordinators(ctx context.Context, playerID int, agentSymbol string) error
}

// RunBootstrapCoordinatorCommand launches the standing bootstrap coordinator for a player.
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

	// INCOME-phase knobs (RULINGS #5; the zero value defers to the documented default).
	HaulerTarget       int     // INCOME hull cap — actual = one per viable contract hub, up to this.
	IncomeBar          float64 // INCOME→GATE exit: realized net credits/hour the fleet must clear.
	MinContractEarners int     // haulers kept on contracts through GATE.
	HaulerShipType     string  // the shipyard ship-type bought for a contract hauler.

	// GATE-phase knob (RULINGS #5; the zero value defers to the documented default).
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
	scanner   ShipyardScanner // sp-hh0h: positions a hull at the home yard so the cold price reads
	metrics   MetricsSink

	// INCOME-phase collaborators. Each is nil-safe: a nil collaborator degrades the INCOME
	// action it drives to a logged skip (surfaced as a blocker), never a panic.
	retirer      FrigateRetirer
	haulAcquirer HaulerAcquirer
	contractRun  ContractRunner
	frigateLoop  FrigateContractLoopStarter // sp-rype: the pre-hauler frigate sole-earner contract loop

	// GATE-phase collaborators. Same nil-safe contract.
	construction  ConstructionManager
	manufacturing ManufacturingController
	repurposer    WorkerRepurposer
	gateAcquirer  GateWorkerAcquirer
	handoff       HandoffLauncher

	// liveConfig snapshots the container's OWN persisted config at each tick start (sp-r6yq),
	// so a `spacetraders tune --operation bootstrap` of a knob takes effect on the NEXT tick with
	// no restart. Optional-injection: nil keeps the launch-frozen behavior byte-identical.
	liveConfig liveconfig.Reader

	// buyBridges holds the per-container fresh-buy count-sync bridge (sp-lgo3): it folds probes the
	// coordinator has bought but the ship-count observation has not yet reflected into the count the
	// DATA buy gate reads, so a SHORT reconcile tick never re-buys toward a target already reached
	// (the over-buy the sync lag would otherwise cause). Keyed by ContainerID because this handler is
	// a REGISTERED SINGLETON serving every bootstrap container — a bare field would be shared/raced
	// across concurrent players; buyBridgeMu guards the MAP only (see probeBridge). It is NOT a
	// progress cursor: it DECAYS to zero as the observation catches up and is dropped on restart (by
	// which point the buys have long synced), so phase/progress stays derived purely from observation.
	buyBridgeMu sync.Mutex
	buyBridges  map[string]*probeBuyBridge
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

// SetShipyardScanner wires the cold-start shipyard-readability positioner (sp-hh0h): when the home
// shipyard price is unreadable, it flies an idle hull to the yard so the next tick's live price read
// succeeds. Unset → the coordinator keeps the pre-hh0h fail-closed behavior (byte-identical): an
// unreadable price simply blocks the buy each tick with no repositioning.
func (h *RunBootstrapCoordinatorHandler) SetShipyardScanner(s ShipyardScanner) { h.scanner = s }

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

// SetFrigateContractLoopStarter wires the pre-hauler frigate sole-earner contract loop (sp-rype;
// reuses the sp-ehg9 batch-contract --loop primitive). Unset → the frigate is provisioned but never put
// on its earning loop, so it would park idle after the probe buy (surfaced loudly as a logged skip).
func (h *RunBootstrapCoordinatorHandler) SetFrigateContractLoopStarter(s FrigateContractLoopStarter) {
	h.frigateLoop = s
}

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

// SetLiveConfigReader wires the per-tick live-config snapshot source (sp-r6yq), making the
// tunable knobs (BootstrapTunableDefaults) honor `spacetraders tune --operation bootstrap` on
// the next tick. Leaving it unset keeps every knob launch-frozen (byte-identical to pre-sp-r6yq).
func (h *RunBootstrapCoordinatorHandler) SetLiveConfigReader(r liveconfig.Reader) { h.liveConfig = r }

// liveConfigSnapshot takes the tick's live-config snapshot (sp-r6yq). A nil reader (not wired —
// tests, minimal boots) or a read error yields nil, which resolveBootstrapConfig treats as "run
// this tick entirely on the launch command" — the fail-safe launch behavior, never a
// half-applied config. The read is logged, not fatal: a transient DB gap must not kill the loop.
func (h *RunBootstrapCoordinatorHandler) liveConfigSnapshot(ctx context.Context, cmd *RunBootstrapCoordinatorCommand) liveconfig.Snapshot {
	if h.liveConfig == nil {
		return nil
	}
	snap, err := h.liveConfig.Snapshot(ctx, cmd.ContainerID, cmd.PlayerID)
	if err != nil {
		common.LoggerFromContext(ctx).Log("WARN", fmt.Sprintf("Bootstrap live config unreadable — this tick runs on launch values: %v", err), map[string]interface{}{
			"action":       "bootstrap_live_config_unreadable",
			"container_id": cmd.ContainerID,
		})
		return nil
	}
	return snap
}

// Handle runs the reconcile loop until the context is cancelled.
func (h *RunBootstrapCoordinatorHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	logger := common.LoggerFromContext(ctx)

	cmd, ok := request.(*RunBootstrapCoordinatorCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	// Startup log only — resolve from the launch command alone (nil live). Per-tick reconcile
	// re-resolves WITH the live snapshot (sp-r6yq), so a later tune is reflected from that tick on.
	cfg := resolveBootstrapConfig(cmd, nil)
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

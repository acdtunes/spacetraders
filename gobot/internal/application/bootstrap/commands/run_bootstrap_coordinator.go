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
	// defaultBootstrapTickSeconds is the cold-start reconcile cadence — the ONE live-tunable knob
	// (tick_secs, bounds 10..86400, no restart). SHORT on purpose (sp-lgo3): bootstrap runs ONLY during
	// cold start — 1 frigate + 1-3 probes make <0.1 req/s vs the 2 req/s ACCOUNT limit (20x+ headroom)
	// and it exits at EXPANSION (gate built) before the fleet is ever large, so a fast tick carries zero
	// API-pacing concern for its whole lifetime. A slow tick instead injects minutes of dead time between
	// a real event (frigate docks, scan/arrival completes) and the coordinator reacting — almost all of
	// the observed ~11min probe-buy was poll latency, not travel. 45s cuts time-to-gate (→ more Phase-2
	// time → higher rank) with ample headroom, made SAFE against the fresh-buy over-buy a short tick
	// would otherwise expose by the sp-lgo3 count-sync bridge.
	defaultBootstrapTickSeconds = 45

	// The cold-start SIZES. Bootstrap seeds a fixed, known-good shape and the standing coordinators own
	// everything above it, so these are the shape itself — not per-run knobs.
	//
	// probeTarget is the scouting seed: 3 probes so market data flows ASAP.
	probeTarget = 3
	// probeShipType is the shipyard ship-type symbol bought for a probe.
	probeShipType = "SHIP_PROBE"
	// haulerTarget caps the contract hull ramp: one hauler per viable contract hub, up to 4 (spec 4–5).
	haulerTarget = 4
	// haulerShipType is the ship-type bought for a contract hauler (and, reused, a gate worker). A light
	// hauler is the cold-start workhorse: cheap, adequate cargo.
	haulerShipType = "SHIP_LIGHT_HAULER"
	// gateWorkerTarget is the gate-construction workforce: the size GATE ramps to, one hull per tick,
	// from the moment the pipeline exists. The gate BUYS its own workers (the contract fleet is exclusive
	// and never repurposed), so this is also the direct bound on the construction-worker spend — 4 keeps
	// that spend small enough that the contract operation still funds the material bill alongside it.
	gateWorkerTarget = 4

	// contractWorkingCapitalFloor is the ABSOLUTE cash cushion (whole credits) the treasury must still
	// clear AFTER a staged bootstrap contract-op spend — the hauler buy (incl. the sp-7r7w first-hauler
	// pivot) AND the GATE-phase gate-worker/construction spend (sp-bpdf): the spend is affordable when
	// treasury−price ≥ this floor (sp-acv5, PLAYBOOK §3).
	//
	// 150k is the contract operation's OPERATING capital: a light-hauler contract cycle's goods+fuel plus
	// enough headroom to keep several concurrent contract cycles funded through a treasury dip — a
	// deliberately conservative operating floor for the whole contract op, NOT a bare per-buy minimum.
	//
	// DISTINCT from the immutable anti-stall bound (Admiral RULINGS #5, 2026-07-18 amendment split): this
	// contract cushion is the derived CONTRACT-OPERATING tier common.ContractReserveCushion (150k =
	// common.ImmutableReserveFloor + 100k; sp-zq635 re-homed it into the ONE floor source so the base and
	// every cushion move together and can never drift). The base common.ImmutableReserveFloor (50k) remains
	// the SEPARATE immutable anti-stall backstop: the outer-max clamp that keeps mature tour/factory trade
	// able to trade its way out of a low-treasury crunch, and the line the fleet autosizer clamps to
	// directly. Both are hard constants — never config keys, never live-tunable. The contract cushion is
	// RAISED above the immutable bound (stricter), so no money guard is weakened (RULINGS #4/#5): a
	// permitted contract-op buy leaves the op funded at 150k.
	contractWorkingCapitalFloor int64 = common.ContractReserveCushion

	// GATE-entry gate — UNCONDITIONALLY ON (sp-1cbxz): GATE entry requires a genuinely SCALED contract op
	// (the FULL fleet at the auto-scaler's live target) — closing the ktio deadlock where one contract
	// payout spiked realized income and drove GATE with ZERO haulers, latching on ConstructionStarted.
	//
	// gateMinHaulers is the escape hatch's STARVED-EARNER floor (sp-gm7r repurposed it): a sticky GATE
	// holding fewer than this many contract haulers reads as under-scaled and (with low progress, for the
	// hysteresis streak) re-derives COLDSTART. GATE ENTRY no longer uses it — the full scaler-target bar is
	// the entry gate — so it scopes only the release of a stuck latch: 2 clearly marks a starved op (a lone
	// frigate spike latched GATE with ZERO haulers).
	gateMinHaulers = 2

	// The fleet autosizer and the dedicated contract auto-scaler are both LAUNCHED EARLY during the
	// cold-start scaling window (sp-1cbxz): the autosizer so the capacity reconciler's emitted
	// contract-delivery demand has a buyer, the scaler so it ramps the exclusive contract fleet behind the
	// 200000 cushion. The ktio-A absolute treasury floor (sp-bpdf) is the load-bearing safety for running
	// the autosizer during cold start.

	// Death-spiral cure (UNCONDITIONALLY ON, sp-gm7r removed the master flag). It replaces the premature
	// GATE-entry gate with a three-part cure: (1) GATE entry requires the FULL contract fleet (delivery +
	// depot) to have reached the auto-scaler's live achievable target AND a treasury
	// surplus war chest (gateFunded); (2) GATE keeps the WHOLE contract fleet earning and never repurposes it
	// to construction (sp-cdxy2: the contract fleet is EXCLUSIVE — the gate BUYS its own workers instead of
	// cannibalizing contracts, which had churned buy→repurpose→buy against the scaler); (3) a sticky GATE that
	// latched under-scaled with ~no construction re-derives COLDSTART (so the op re-scales) after an
	// anti-thrash hysteresis streak. Gate entry only ever tightens, never loosens (RULINGS #4). Its
	// calibration bars follow.
	//
	// gateSurplusFloor is the treasury SURPLUS — over common.ImmutableReserveFloor (50k) — the op must
	// hold to enter GATE: a war chest for the jump-gate material bill (~1600 FAB_MATS + 400 ADVANCED_CIRCUITRY)
	// so GATE is earned from contract surplus, never raced on a thin treasury its own material spend then
	// crashes. 500k (⇒ treasury ≥ 550k to gate) is sized against the freshly-read gate bill. It is a
	// PHASE-entry threshold, NOT a spend guard (RULINGS #5); the buy-time 150k working-capital floor is
	// untouched. Base choice: the immutable 50k anti-stall bound; the 150k contract cushion is the stricter
	// alternative if contract working capital should not count as surplus.
	gateSurplusFloor int64 = 500_000
	// gateReentryConstructionPct is the construction-progress ceiling (whole percent, 0..100) below
	// which an under-scaled sticky GATE may re-derive COLDSTART (the escape hatch). 5% scopes the escape to a
	// GATE that latched but never really built — past it real materials are flowing (the manufacturing
	// executor keeps delivering regardless of bootstrap's phase) and GATE is permanent.
	gateReentryConstructionPct = 5.0
	// gateReentryStreakTicks is how many CONSECUTIVE under-scaled + low-progress ticks must hold before
	// the GATE→COLDSTART re-derive fires — anti-thrash hysteresis: a single dip never flips the phase, and the
	// direction is asymmetric (SLOW to leave GATE over N ticks, immediate to resume it once the op re-scales),
	// so the phase strongly prefers GATE and only escapes a genuinely, persistently starved latch. The streak
	// is in-memory per-container and fails SAFE on restart: a dropped streak just re-accrues from 0 (delays the
	// re-derive one window), never double-acts (the re-derive is a pure phase relabel — no spend, no
	// assignment). 3 ticks ≈ 2.25 min at the 45s cold-start cadence.
	gateReentryStreakTicks = 3

	// expansionHandoffRetryTicks bounds how many CONSECUTIVE ticks the terminal EXPANSION phase
	// re-attempts an UNCONFIRMED hand-off before exiting anyway. EXPANSION is terminal on the WORLD
	// signal — the home jump gate is BUILT — so the only open question is where an unconfirmable
	// hand-off gets retried: in-process every tick, or at the next daemon boot. Bootstrap is
	// boot-standing and every hand-off launch is idempotent, so the bounded exit still retries — once
	// per boot instead of forever.
	//
	// The bound is what keeps BOTH failure modes covered. A TRANSIENT launcher fault is fully inside the
	// window (it succeeds on a later tick and exits with the hand-off confirmed, so a fleet that has just
	// finished its gate never exits half-handed-off). A PERSISTENT one — a launcher that is down, absent,
	// or observe-only — can no longer pin the coordinator in a per-tick full-fleet re-read on a mature
	// fleet, where that loop costs a double-digit share of the account-wide (unraisable) request budget
	// while achieving nothing. In-memory per container and fails SAFE on restart: a dropped streak just
	// re-accrues from 0, and the exit is a pure loop-exit (no spend, no assignment), so it never
	// double-acts. 3 ticks ≈ 2.25 min at the 45s cadence.
	expansionHandoffRetryTicks = 3
)

// DefaultTickInterval is defaultBootstrapTickSeconds as a wall-clock duration. The container
// runner reads it as the defence-in-depth pacing floor between runner re-entries of an infinite
// bootstrap container — single-sourced here so the floor can never drift from the tick it mirrors.
const DefaultTickInterval = defaultBootstrapTickSeconds * time.Second

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

// ScoutPostDeclarer declares the STANDING scout-post COVERAGE target for a system — the
// desired-state post the boot-standing scout-post coordinator (sp-9ujl) mans by claiming an idle
// probe. It is a coverage declaration, NOT a probe assignment: bootstrap declares the home post and
// leaves its probes IDLE, and the coordinator does the manning (claim → VRP-partition → scan),
// which seeds the initial home scan → census → the freshsizer takes over declaring the rest.
// Idempotent — a post already declared for the system is preserved, not re-touched — so bootstrap
// can call it every tick. This REPLACES the old probe-holding scout-all-markets sweep, which
// held the probes and starved the now-boot-standing coordinator (sp-pt7d, Admiral intent: bootstrap
// buys probes but assigns them to nothing; the coordinator mans them). minHulls is the permanent
// manning FLOOR (probeTarget, sp-2ci9y) stamped on the home post so the freshsizer never sizes it
// below the probes bootstrap bought — passed through and applied idempotently.
type ScoutPostDeclarer interface {
	DeclareHomeScoutPost(ctx context.Context, playerID int, system string, minHulls int) error
}

// ShipyardScanner positions an idle hull AT a home-system shipyard so the NEXT tick's live PriceCheck
// returns priced listings (sp-hh0h). The cold-start deadlock it breaks: on a fresh universe nothing
// has ever visited the home shipyard, its live ship listing is presence-gated (empty unless a hull is
// there), so PriceCheck reads unreadable and every probe buy fails closed FOREVER — cold start never leaves
// the ground without a captain. This does NOT weaken the price guard (RULINGS #4): a genuinely
// unreadable price still buys nothing this tick; the scanner makes the price READABLE by getting a hull
// to the yard, so the guard clears on evidence. EnsureHomeShipyardReadable is idempotent and
// best-effort — dispatched=false when a hull is already present/en route at a shipyard (just wait) or no
// idle hull is free to go — so re-evaluation each unreadable tick never re-navigates or thrashes. Unset
// (nil) → the reconciler preserves the pre-hh0h fail-closed behavior (byte-identical).
type ShipyardScanner interface {
	EnsureHomeShipyardReadable(ctx context.Context, playerID int, homeSystem string) (dispatched bool, err error)
	// PositionPurchaserAtShipyard navigates the NAMED hull (the freed+dedicated command frigate at the
	// sp-5nd2 first-hauler pivot) to a home-system shipyard so the NEXT tick's presence-gated hauler
	// PriceCheck reads. It differs from EnsureHomeShipyardReadable in two load-bearing ways: it targets a
	// SPECIFIC hull by symbol (so it does not depend on the frigate reading idle the instant after its
	// loop-claim is released), and it positions the PURCHASING-dedicated frigate that EnsureHomeShipyardReadable
	// deliberately skips (that one only repositions undedicated hulls, RULINGS #7). Idempotent + best-effort:
	// dispatched=false (no re-nav) when the hull is already present (not in transit) at a home shipyard, already
	// en route, or no home-system shipyard is known yet. It NEVER buys and NEVER weakens the price guard —
	// the reconciler still spends nothing while the price is unreadable.
	PositionPurchaserAtShipyard(ctx context.Context, playerID int, shipSymbol, homeSystem string) (dispatched bool, err error)
}

// MetricsSink records the bootstrap's observation series (spec §Observability). Pure observation:
// nil-safe and best-effort, a recording miss never touches a decision.
type MetricsSink interface {
	// RecordPhase sets the derived-phase gauge (spacetraders_bootstrap_phase{phase}).
	RecordPhase(phase string)
	// RecordProbePurchased increments the probes-bought counter (once per executed probe buy).
	RecordProbePurchased()
	// RecordHaulerPurchased increments the haulers-bought counter (once per executed hauler buy).
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
	// DedicateAsPurchaser tags the frigate as the EXCLUSIVE purchasing ship (dedicated_fleet=purchasing)
	// at the first-hauler pivot (sp-7r7w): a protected standing role — idle between buys but reserved as
	// THE buy ship for every subsequent purchase, and NEVER re-drafted into the contract op (guarded like
	// a foreign dedication in the reconciler / contract-fleet / autosizer selection paths, RULINGS #7).
	// Reuses the single fleet-assign write path (AssignShipFleet with Fleet="purchasing").
	DedicateAsPurchaser(ctx context.Context, playerID int, shipSymbol string) error
}

// HaulerAcquirer price-checks and buys ONE light hauler, then dedicates it to the contract fleet and
// places it on its hub (reuses shipyard list/purchase + fleet assign + navigate). Mirrors
// ProbeAcquirer but folds the dedicate+placement into the buy, because a contract hauler is a
// dedicated, positioned hull — not a free scout. PriceCheck reads the cheapest reachable yard's ask
// for shipType (readable=false ⇒ the capital gate fails closed).
type HaulerAcquirer interface {
	PriceCheck(ctx context.Context, playerID int, shipType string) (price int64, yard string, readable bool, err error)
	// BuyAndPlace buys ONE hauler, dedicates it to the contract fleet, and places it on its hub.
	// purchaserSymbol names the hull that executes the buy (navigate→dock→purchase): "" keeps the legacy
	// behavior (pick any idle hull), and a set value pins THE purchaser — the first-hauler pivot passes
	// the freed command frigate so the buy is deterministic, not dependent on an incidentally-idle probe.
	BuyAndPlace(ctx context.Context, playerID int, shipType, yard, hubWaypoint, purchaserSymbol string) (BuyResult, error)
	// BuyAndDedicate buys ONE hull and dedicates it to the arbitrary fleet tag `fleet` (sp-192k4), with
	// NO hub placement — the fleet-parameterized sibling of BuyAndPlace (which hardcodes the contract
	// fleet + a hub). The cold-start hull-routing trade-seed calls it with fleet="trade" to seed acquisition
	// #2 to the trade fleet. purchaserSymbol pins the buy ship exactly as BuyAndPlace does (the trade seed
	// passes the exclusive purchasing frigate). Reuses the same atomic buy+dedicate path (BatchPurchaseShips
	// + fleet assign) BuyAndPlace uses, minus the navigate.
	BuyAndDedicate(ctx context.Context, playerID int, shipType, yard, fleet, purchaserSymbol string) (BuyResult, error)
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
	// StopLoop stops the frigate's continuous contract-loop container (StopContainer), releasing the
	// frigate's work-claim so it goes idle — the first-hauler pivot (sp-7r7w) the design already
	// documents (BatchContractWorkflow: "stops the returned container at the first-hauler pivot"). The
	// freed frigate then executes the hauler buy and is retired to the exclusive purchasing role; the
	// loop-start is gated on len(Haulers)==0 so it never restarts post-pivot. Idempotent (stopping an
	// absent loop is a benign no-op).
	StopLoop(ctx context.Context, playerID int, frigateSymbol string) error
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
// a gate-construction worker. This is the "repurpose idle contract haulers FIRST" seed (spec §Fleet
// scaling): the income fleet becomes the seed construction workforce before any hull is bought.
type WorkerRepurposer interface {
	RepurposeToConstruction(ctx context.Context, playerID int, shipSymbol string) error
}

// GateSurplusReleaser un-dedicates the gate's OWN surplus IDLE manufacturing hulls back to the UNDEDICATED
// idle pool via the single-writer AssignFleet (fleet→"", RULINGS #3), from where the contract scaler's
// reclaim-before-buy tier (IdleHullReclaimer) adopts them into the contract fleet BEFORE it buys — the
// zero-buy fleet re-balance (sp-mxflh). It mirrors the contract scaler's DeliverySurplusReleaser (un-dedicate
// to the idle pool); it is the OPPOSITE direction of WorkerRepurposer (which re-dedicates TO construction) and
// never touches the exclusive contract fleet. Un-dedicate is FREE (no spend) ⇒ never cushion-gated. Nil-safe:
// unset ⇒ the surplus release is a logged skip (the gate keeps its surplus until wired), never a panic.
type GateSurplusReleaser interface {
	// ReleaseSurplusGateWorkers un-dedicates the given manufacturing-hull symbols to "" (the idle pool),
	// returning how many it actually released. It RE-GUARDS each hull at release time (still manufacturing-
	// dedicated, still idle, not in transit) so a hull that picked up a construction task since the observation
	// is never yanked mid-task; an AssignFleet error stops early and returns the partial count (a hull left
	// dedicated is safe — re-balanced a later tick). A fleet-read error surfaces (release nothing, fail-closed).
	ReleaseSurplusGateWorkers(ctx context.Context, playerID int, shipSymbols []string) (int, error)
}

// GateWorkerAcquirer price-checks and buys ONE gate-construction worker hull and dedicates it to
// construction (reuses shipyard purchase + fleet assign). The staged top-up when repurposed haulers
// don't cover the pipeline's shape. Mirrors HaulerAcquirer but does not place on a hub (the executor
// claims the worker); PriceCheck readable=false ⇒ the capital gate fails closed (no buy).
type GateWorkerAcquirer interface {
	PriceCheck(ctx context.Context, playerID int, shipType string) (price int64, yard string, readable bool, err error)
	BuyForConstruction(ctx context.Context, playerID int, shipType, yard string) (BuyResult, error)
}

// HandoffLauncher performs the EXPANSION hand-off: it launches the standing fleet-autosizer (OFF the whole
// bootstrap run so the two never issue conflicting purchases against one treasury) and the other standing
// coordinators, turning the fleet over to the mature demand-driven economy. Guarded on obs.AutosizerRunning
// so a restart post-gate re-observes the autosizer running and never re-launches.
type HandoffLauncher interface {
	LaunchAutosizer(ctx context.Context, playerID int, agentSymbol string) error
	LaunchStandingCoordinators(ctx context.Context, playerID int, agentSymbol string) error
	// LaunchContractScaler launches the standing dedicated contract auto-scaler during the cold-start
	// scaling window (unconditional in the cold-start window, sp-1cbxz). Idempotent (skips when one is
	// already RUNNING/PENDING), so a re-run never double-launches.
	LaunchContractScaler(ctx context.Context, playerID int, agentSymbol string) error
	// LaunchTradeFleetCoordinator launches the standing trade-fleet coordinator at the cold-start trade-seed
	// (sp-192k4), so the freshly-seeded trade hull is picked up and put on a continuous tour. Idempotent
	// (skips when one is already RUNNING/PENDING) — the observable trade hull is the seeded signal, so a
	// re-run never double-launches.
	LaunchTradeFleetCoordinator(ctx context.Context, playerID int, agentSymbol string) error
}

// RunBootstrapCoordinatorCommand launches the standing bootstrap coordinator for a player.
// Like the fleet-autosizer / siting coordinators it runs an infinite reconcile loop inside a single
// Handle() call; the container wraps it. The cold-start shape is fixed in code, so the launch config
// carries only the boot-gate and the cadence; a zero cadence falls back to the documented default.
type RunBootstrapCoordinatorCommand struct {
	PlayerID    int
	ContainerID string
	AgentSymbol string

	// Disabled is the master boot-gate (negation of bootstrap_disabled so an absent key reads as
	// ENABLED — LIVE BY DEFAULT, Admiral no-dark-shipping). The container stays resident when
	// disabled so a config flip + restart re-arms it, but it takes no action while stood down.
	Disabled bool

	// TickIntervalSecs is the reconcile cadence, live-overlaid each tick by the tick_secs tune.
	TickIntervalSecs int
}

// RunBootstrapCoordinatorResponse reports reconcile progress, observed on context cancellation
// (shutdown) or at the terminal EXPANSION exit.
type RunBootstrapCoordinatorResponse struct {
	Ticks  int
	Errors []string

	// Done reports the terminal EXPANSION exit: the gate is built and the standing economy is handed
	// off, so the WHOLE bootstrap run is finished. The container runner consumes it via RunTerminal —
	// the boot-standing container carries an infinite iteration budget, and without this signal the
	// runner re-enters Handle() the instant it returns, spinning a mature fleet through unpaced
	// full-fleet re-reads instead of completing.
	Done bool
}

// RunTerminal implements common.RunTerminalReporter: a done response ends the container's iteration
// loop (COMPLETED) instead of re-entering the handler.
func (r *RunBootstrapCoordinatorResponse) RunTerminal() bool { return r.Done }

// RunBootstrapCoordinatorHandler reconciles a cold agent toward the jump gate. It holds NO
// in-memory progress state: progress is ALWAYS re-derived from the live observation each tick
// (spec §Minimal persisted state), so a mid-flight crash is a non-event — a restart re-observes and
// resumes at real state. Collaborators are wired by setters at boot; each is nil-safe (a nil
// collaborator degrades to a logged skip, never a panic).
type RunBootstrapCoordinatorHandler struct {
	clock shared.Clock

	refresher    ShipRefresher
	observer     WorldObserver
	acquirer     ProbeAcquirer
	postDeclarer ScoutPostDeclarer
	scanner      ShipyardScanner // sp-hh0h: positions a hull at the home yard so the cold price reads
	metrics      MetricsSink

	// Contract-workstream collaborators. Each is nil-safe: a nil collaborator degrades the contract
	// action it drives to a logged skip (surfaced as a blocker), never a panic.
	retirer      FrigateRetirer
	haulAcquirer HaulerAcquirer
	contractRun  ContractRunner
	frigateLoop  FrigateContractLoopStarter // sp-rype: the pre-hauler frigate sole-earner contract loop

	// GATE-phase collaborators. Same nil-safe contract.
	construction  ConstructionManager
	manufacturing ManufacturingController
	repurposer    WorkerRepurposer
	gateReleaser  GateSurplusReleaser // sp-mxflh: un-dedicate surplus idle gate workers → idle pool (scaler adopts)
	gateAcquirer  GateWorkerAcquirer
	handoff       HandoffLauncher

	// liveConfig snapshots the container's OWN persisted config at each tick start (sp-r6yq),
	// so a `spacetraders tune --operation bootstrap` of a knob takes effect on the NEXT tick with
	// no restart. Optional-injection: nil keeps the launch-frozen behavior byte-identical.
	liveConfig liveconfig.Reader

	// buyBridges holds the per-container fresh-buy count-sync bridge (sp-lgo3): it folds probes the
	// coordinator has bought but the ship-count observation has not yet reflected into the count the
	// probe buy gate reads, so a SHORT reconcile tick never re-buys toward a target already reached
	// (the over-buy the sync lag would otherwise cause). Keyed by ContainerID because this handler is
	// a REGISTERED SINGLETON serving every bootstrap container — a bare field would be shared/raced
	// across concurrent players; buyBridgeMu guards the MAP only (see probeBridge). It is NOT a
	// progress cursor: it DECAYS to zero as the observation catches up and is dropped on restart (by
	// which point the buys have long synced), so phase/progress stays derived purely from observation.
	buyBridgeMu sync.Mutex
	buyBridges  map[string]*probeBuyBridge

	// underScaledStreaks holds the per-container escape-hatch hysteresis counter: consecutive ticks a
	// sticky-latched GATE has been under-scaled with ~no construction, so the GATE→COLDSTART re-derive fires only
	// after gateReentryStreakTicks in a row (anti-thrash). Keyed by ContainerID for the same singleton
	// reason as buyBridges; underScaledStreakMu guards the MAP only (one container's ticks are
	// sequential). Consulted every tick (sp-gm7r removed the flag); NOT a progress cursor — dropped on
	// restart (the re-derive just re-accrues from 0, delaying one window, never double-acting).
	underScaledStreakMu sync.Mutex
	underScaledStreaks  map[string]int

	// expansionHoldStreaks holds the per-container terminal-exit hysteresis counter: consecutive EXPANSION
	// ticks whose hand-off could not be confirmed, so the bounded exit fires only after
	// expansionHandoffRetryTicks in a row (a transient launcher fault still exits with the hand-off
	// confirmed). Keyed by ContainerID for the same singleton reason as underScaledStreaks;
	// expansionHoldStreakMu guards the MAP only (one container's ticks are sequential). NOT a progress
	// cursor — dropped on restart, where it simply re-accrues from 0 and the exit is a pure loop-exit
	// (no spend, no assignment), so it can never double-act.
	expansionHoldStreakMu sync.Mutex
	expansionHoldStreaks  map[string]int

	// haulerPrices holds the per-container LAST READABLE contract-hauler price (sp-muc5x): the most recent
	// presence-gated hauler shipyard ask this coordinator observed. It is cached so the first-hauler
	// pivot can test AFFORDABILITY *before* it frees the command frigate's earning loop to position the buyer
	// — the cold-start deadlock where the frigate was freed while the hauler was permanently unaffordable,
	// leaving no earner (treasury never grew → permanent stall). Seeded at the probe buy (a hull already at
	// the yard reads the hauler price at the same GetShipyard moment) and refreshed on every readable hauler
	// PriceCheck. Keyed by ContainerID for the same REGISTERED-SINGLETON reason as buyBridges; haulerPriceMu
	// guards the MAP only (one container's ticks are sequential). NOT a progress cursor — dropped on restart
	// (a re-seed re-populates it on a fresh cold start), so phase/progress stays derived purely from
	// observation. A 0/absent cache reads as "no evidence yet" and preserves the existing free+position path.
	haulerPriceMu sync.Mutex
	haulerPrices  map[string]int64
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

// SetScoutPostDeclarer wires the home scout-post declaration (AddScoutPost). Unset → the home
// coverage post is not declared (surfaced loudly); bootstrap still buys probes and leaves them idle.
func (h *RunBootstrapCoordinatorHandler) SetScoutPostDeclarer(s ScoutPostDeclarer) {
	h.postDeclarer = s
}

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
// fleet assign + navigate). Unset → the contract workstream evaluates and logs but never buys a hauler.
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

// SetGateSurplusReleaser wires the "un-dedicate surplus idle gate workers to the idle pool" action (sp-mxflh)
// so the contract scaler's reclaim-before-buy tier adopts them (zero buys). Unset → the gate keeps its surplus
// (a logged skip), never a panic.
func (h *RunBootstrapCoordinatorHandler) SetGateSurplusReleaser(r GateSurplusReleaser) {
	h.gateReleaser = r
}

// SetGateWorkerAcquirer wires the price-check + buy-for-construction path (reuses shipyard purchase +
// fleet assign). Unset → GATE repurposes but never buys the top-up delta (surfaced loudly).
func (h *RunBootstrapCoordinatorHandler) SetGateWorkerAcquirer(a GateWorkerAcquirer) {
	h.gateAcquirer = a
}

// SetHandoffLauncher wires the EXPANSION hand-off (launch the autosizer + standing coordinators). Unset →
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
	logger.Log("INFO", fmt.Sprintf("Bootstrap coordinator starting (tick %s, disabled=%v, probes→%d, haulers→%d, gate workers→%d)", cfg.Tick, cfg.Disabled, probeTarget, haulerTarget, gateWorkerTarget), map[string]interface{}{
		"action":       "bootstrap_start",
		"container_id": cmd.ContainerID,
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

		// Terminal EXPANSION (sp-feiy7 — formerly COMPLETE): the gate is built and the standing economy is
		// handed off, so the coordinator has finished its job and exits cleanly (spec §Architecture). A
		// restart post-gate re-derives EXPANSION, re-observes the hand-off done, and exits again — idempotent.
		// Done MUST reach the response: it is what stops the container runner's iteration loop
		// (RunTerminal) — the return alone does not, because the boot-standing container's iteration
		// budget is infinite and the runner would re-enter Handle() immediately.
		if res.Done {
			result.Done = true
			logger.Log("INFO", "Bootstrap coordinator exiting: EXPANSION reached (gate built) and handed off", map[string]interface{}{
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

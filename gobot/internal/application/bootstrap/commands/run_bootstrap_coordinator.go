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
	// defaultBootstrapTickSeconds is the cold-start reconcile cadence — a live-tunable knob
	// (tick_secs, bounds 10..86400, no restart). SHORT on purpose: bootstrap runs ONLY during
	// cold start — 1 frigate + 1-3 probes make <0.1 req/s vs the 2 req/s ACCOUNT limit (20x+ headroom)
	// and it exits at EXPANSION (gate built) before the fleet is ever large, so a fast tick carries zero
	// API-pacing concern for its whole lifetime. A slow tick instead injects minutes of dead time between
	// a real event (frigate docks, scan/arrival completes) and the coordinator reacting — almost all of
	// the observed ~11min probe-buy was poll latency, not travel. 45s cuts time-to-gate (→ more Phase-2
	// time → higher rank) with ample headroom, made SAFE against the fresh-buy over-buy a short tick
	// would otherwise expose by the count-sync bridge.
	defaultBootstrapTickSeconds = 45

	// defaultContractStartTreasuryThreshold is the treasury the fleet must hold before the CONTRACT
	// OPERATION starts (contract_start_treasury_threshold — config.yaml [bootstrap] + live-tunable).
	// Below it the command frigate trades and nothing contract-side is launched or bought. A FLAT
	// reading, deliberately NOT netted against the reserve floor — different arithmetic from
	// gateSurplusFloor's GATE-entry surplus. SEQUENCING only, never a spend guard (RULINGS #1/#4).
	defaultContractStartTreasuryThreshold int64 = 500_000

	// The cold-start SIZES. Bootstrap seeds a fixed, known-good shape and the standing coordinators own
	// everything above it, so these are the shape itself — not per-run knobs.
	//
	// probeTarget is the scouting seed: 3 probes so market data flows ASAP.
	probeTarget = 3
	// probeShipType is the shipyard ship-type symbol bought for a probe.
	probeShipType = "SHIP_PROBE"

	// YardSentinelReservationReason is the yard-sentinel's captain-reservation reason. EXPORTED so
	// observeFleetShape (internal/adapters/grpc) can recognise the SAME string to tell the sentinel
	// apart from any other captain reservation, without a second source of truth for it.
	YardSentinelReservationReason = "bootstrap_yard_sentinel"
	// haulerTarget caps the contract hull ramp: one hauler per viable contract hub, up to 4 (spec 4–5).
	haulerTarget = 4
	// haulerShipType is the ship-type bought for a contract hauler (and, reused, a gate worker). A light
	// hauler is the cold-start workhorse: cheap, adequate cargo.
	haulerShipType = "SHIP_LIGHT_HAULER"
	// gateWorkerTarget is the gate-construction workforce: the size GATE ramps to, one hull per tick,
	// from the moment the pipeline exists. The gate BUYS its own workers (the contract fleet is exclusive
	// and never repurposed), so this is also the direct bound on the construction-worker spend — it keeps
	// that spend small enough that the contract operation still funds the material bill alongside it.
	gateWorkerTarget = 3

	// contractWorkingCapitalFloor is the ABSOLUTE cash cushion (whole credits) the treasury must still
	// clear AFTER a staged bootstrap fleet-scaling spend — the hauler buy (incl. the first-hauler
	// pivot), the trade-seed buy, AND the GATE-phase gate-worker/construction spend: affordable when
	// treasury−price ≥ this floor (PLAYBOOK §3).
	//
	// Aliases the derived FLEET-SCALING tier common.FleetScalingReserveCushion (see its own doc for
	// the sizing data) rather than the lower common.ContractReserveCushion it used to point at: that
	// tier was sized to survive only ITSELF, not itself plus a large contract source-buy landing right
	// after. common.ImmutableReserveFloor (50k) remains the SEPARATE immutable anti-stall backstop: the
	// outer-max clamp mature tour/factory trade uses to trade out of a low-treasury crunch, and the
	// line the fleet autosizer clamps to directly. Both are hard constants — never config keys, never
	// live-tunable. This cushion sits ABOVE that bound (stricter), so no money guard is weakened
	// (RULINGS #4/#5).
	contractWorkingCapitalFloor int64 = common.FleetScalingReserveCushion

	// GATE-entry gate — UNCONDITIONALLY ON: GATE entry requires a genuinely SCALED contract op
	// (the FULL fleet at the auto-scaler's live target). Without that bar one contract payout
	// spiking realized income drives GATE with ZERO haulers, latching on ConstructionStarted.
	//
	// gateMinHaulers is the escape hatch's STARVED-EARNER floor: a sticky GATE
	// holding fewer than this many contract haulers reads as under-scaled and (with low progress, for the
	// hysteresis streak) re-derives COLDSTART. GATE ENTRY does not use it — the full scaler-target bar is
	// the entry gate — so it scopes only the release of a stuck latch: 2 clearly marks a starved op (a lone
	// frigate spike latched GATE with ZERO haulers).
	gateMinHaulers = 2

	// The fleet autosizer and the dedicated contract auto-scaler are both LAUNCHED EARLY during the
	// cold-start scaling window: the autosizer so the capacity reconciler's emitted
	// contract-delivery demand has a buyer, the scaler so it ramps the exclusive contract fleet behind the
	// 200000 cushion. The ktio-A absolute treasury floor is the load-bearing safety for running
	// the autosizer during cold start.

	// Death-spiral cure (UNCONDITIONALLY ON). Three parts: (1) GATE entry requires the FULL contract
	// fleet (delivery + depot) to have reached the auto-scaler's live achievable target AND a treasury
	// surplus war chest (gateFunded); (2) GATE keeps the WHOLE contract fleet earning and never repurposes it
	// to construction (the contract fleet is EXCLUSIVE — the gate BUYS its own workers instead of
	// cannibalizing contracts, which churns buy→repurpose→buy against the scaler); (3) a sticky GATE that
	// latched under-scaled with ~no construction re-derives COLDSTART (so the op re-scales) after an
	// anti-thrash hysteresis streak. Gate entry only ever tightens, never loosens (RULINGS #4). Its
	// calibration bars follow.
	//
	// gateSurplusFloor is the treasury SURPLUS — over common.ImmutableReserveFloor (50k) — the op must
	// hold to enter GATE: a war chest for the jump-gate material bill (~1600 FAB_MATS + 400 ADVANCED_CIRCUITRY)
	// so GATE is earned from contract surplus, never raced on a thin treasury its own material spend then
	// crashes. 500k (⇒ treasury ≥ 550k to gate) is sized against the freshly-read gate bill. It is a
	// PHASE-entry threshold, NOT a spend guard (RULINGS #5); the buy-time 350k working-capital floor is
	// untouched. Base choice: the immutable 50k anti-stall bound; the 350k fleet-scaling cushion is the
	// stricter alternative if contract working capital should not count as surplus.
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
	// or observe-only — can no longer pin the coordinator in a per-tick live re-read on a mature fleet,
	// where that loop spends the account-wide (unraisable) request budget while achieving nothing.
	// In-memory per container and fails SAFE on restart: a dropped streak just
	// re-accrues from 0, and the exit is a pure loop-exit (no spend, no assignment), so it never
	// double-acts. 3 ticks ≈ 2.25 min at the 45s cadence.
	expansionHandoffRetryTicks = 3
)

// DefaultTickInterval is defaultBootstrapTickSeconds as a wall-clock duration. The container
// runner reads it as the defence-in-depth pacing floor between runner re-entries of an infinite
// bootstrap container — single-sourced here so the floor can never drift from the tick it mirrors.
const DefaultTickInterval = defaultBootstrapTickSeconds * time.Second

// pendingScalingReservationTarget is the treasury a capital-blocked gate-worker or hauler buy is
// waiting to clear: price plus the SAME contractWorkingCapitalFloor the buy's own cushion/
// affordable test already enforces, so the two numbers can never drift apart (RULINGS #5).
func pendingScalingReservationTarget(price int64) int64 {
	return price + contractWorkingCapitalFloor
}

// ShipRefresher refreshes the player's hulls before any role/assignment decision — the
// phantom-cache guard against a desynced cache (a phantom-idle hull misread as busy). An error
// fails the tick CLOSED. A fleet read is priced per page, so an implementation may serve a tick
// from the projection instead: that is nil, never an error, which would stop the coordinator.
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
//
// A yard prices its hulls only while a ship is standing at it, so an unvisited yard reads unreadable.
// An unreadable read still returns the LAST ASK the yard gave for shipType (0 when none ever has),
// because it is the only evidence available while the yard is cold. It is evidence for policy — when
// to commit a ship — and never a price to spend against: every buy path gates on readable first.
type ProbeAcquirer interface {
	PriceCheck(ctx context.Context, playerID int, shipType string) (price int64, yard string, readable bool, err error)
	Buy(ctx context.Context, playerID int, shipType, yard string) (BuyResult, error)
}

// HomeTourStarter starts the cold-start market tour over the home system's markets, on the
// probes bootstrap has already bought. It is the SAME path the captain's `scout markets`
// verb invokes — one code path, two callers — so the tour an operator starts and the tour
// the DATA phase starts are the same object, subject to the same bootstrap-phase gate and
// swept by the same graduation edge. There is no desired-state post behind it and nothing
// re-mans it: bootstrap re-derives from the live world each tick, like every other action.
//
// It reports how many hulls were put on tour so the caller can log an honest delta.
type HomeTourStarter interface {
	StartHomeMarketTour(ctx context.Context, playerID int, homeSystem string) (int, error)
}

// ShipyardScanner makes a cold home shipyard readable for a specific ship type. A yard's ship listing
// is presence-gated — priced only while a hull stands at it — so on a fresh universe nothing has ever
// visited the home yard, every PriceCheck reads unreadable, and cold start never leaves the ground.
// Sending a hull is what turns the read into evidence; it does NOT weaken the price guard (RULINGS #4),
// because the tick that sends a hull still buys nothing. Unset (nil) → the reconciler simply fails closed.
type ShipyardScanner interface {
	// EnsureShipyardReadable sends a hull toward a home-system SHIPYARD waypoint that can plausibly
	// sell shipType, so the next tick's price read succeeds — weighing the persisted shipyard-inventory
	// record over the bare candidate list (prefer a confirmed seller, never resend to a confirmed
	// non-seller). purchaser names the hull to send — the committed purchasing frigate, whose
	// dedication puts it outside the free-hull search; empty means "pick a free hull", which never
	// takes one another controller owns (RULINGS #7). borrow lends a TAGGED but free hull when that
	// search is empty, re-tagging nothing and refusing one back on tour. Presence is enough to price;
	// buys dock.
	//
	// Idempotent and best-effort: dispatched=false is a WAIT — a hull at a VIABLE yard, one en route,
	// no free hull, or no home shipyard known yet. exhausted is the distinct dead end where every known
	// candidate is confirmed not to sell shipType.
	EnsureShipyardReadable(ctx context.Context, playerID int, homeSystem, shipType, purchaser, borrow string) (dispatched bool, exhausted bool, err error)
}

// MetricsSink records the bootstrap's observation series (spec §Observability). Pure observation:
// nil-safe and best-effort, a recording miss never touches a decision. Every method takes playerID so
// a shared Prometheus instance can scope a panel to one player/era instead of blending every player
// that has ever run against it.
type MetricsSink interface {
	// RecordPhase sets the derived-phase gauge (spacetraders_bootstrap_phase{phase,player_id}).
	RecordPhase(phase string, playerID string)
	// RecordProbePurchased increments the probes-bought counter (once per executed probe buy).
	RecordProbePurchased(playerID string)
	// RecordHaulerPurchased increments the haulers-bought counter (once per executed hauler buy).
	RecordHaulerPurchased(playerID string)
	// RecordConstructionPct sets the gate construction-progress gauge [0,100] (GATE phase).
	RecordConstructionPct(pct float64, playerID string)
}

// FrigateRetirer clears the command frigate's contract-fleet dedication (reuses fleet unassign —
// AssignShipFleetCommand with Fleet=""). It is the "retire the frigate from contract work" action: a
// frigate is a poor contract worker (low fuel/cargo), so it must not sit in the contract coordinator's
// dedicated pool. Idempotent at the adapter (a clear on an untagged hull is a no-op); the reconciler
// still guards on the observation so a stale tag is cleared exactly once.
type FrigateRetirer interface {
	RetireFromContract(ctx context.Context, playerID int, shipSymbol string) error
	// DedicateAsPurchaser tags the frigate as the EXCLUSIVE purchasing ship (dedicated_fleet=purchasing)
	// at the first-hauler pivot: a protected standing role — idle between buys but reserved as
	// THE buy ship for every subsequent purchase, and NEVER re-drafted into the contract op (guarded like
	// a foreign dedication in the reconciler / contract-fleet / autosizer selection paths, RULINGS #7).
	// Reuses the single fleet-assign write path (AssignShipFleet with Fleet="purchasing").
	DedicateAsPurchaser(ctx context.Context, playerID int, shipSymbol string) error
	// DedicateAsTrade tags the frigate into the TRADE fleet (dedicated_fleet=trade) — its standing home:
	// the frigate tours under the trade-fleet coordinator from tick 1 and is handed back to it after its
	// cold-start buys, so it never stands by idle-dedicated. Same single fleet-assign write path as
	// DedicateAsPurchaser with the destination tag swapped, and idempotent at the repo.
	DedicateAsTrade(ctx context.Context, playerID int, shipSymbol string) error
}

// HaulerAcquirer price-checks and buys ONE light hauler, then dedicates it to the contract fleet and
// places it on its hub (reuses shipyard list/purchase + fleet assign + navigate). Mirrors
// ProbeAcquirer but folds the dedicate+placement into the buy, because a contract hauler is a
// dedicated, positioned hull — not a free scout. PriceCheck reads the cheapest reachable yard's ask
// for shipType (readable=false ⇒ the capital gate fails closed, and the price carries the last ask
// the yard gave — see ProbeAcquirer).
type HaulerAcquirer interface {
	PriceCheck(ctx context.Context, playerID int, shipType string) (price int64, yard string, readable bool, err error)
	// BuyAndPlace buys ONE hauler, dedicates it to the contract fleet, and places it on its hub.
	// purchaserSymbol names the hull that executes the buy (navigate→dock→purchase): "" keeps the legacy
	// behavior (pick any idle hull), and a set value pins THE purchaser — the first-hauler pivot passes
	// the freed command frigate so the buy is deterministic, not dependent on an incidentally-idle probe.
	BuyAndPlace(ctx context.Context, playerID int, shipType, yard, hubWaypoint, purchaserSymbol string) (BuyResult, error)
	// BuyAndDedicate buys ONE hull and dedicates it to the arbitrary fleet tag `fleet`, with
	// NO hub placement — the fleet-parameterized sibling of BuyAndPlace (which hardcodes the contract
	// fleet + a hub). The cold-start hull-routing trade-seed calls it with fleet="trade" to seed acquisition
	// #2 to the trade fleet. purchaserSymbol pins the buy ship exactly as BuyAndPlace does (the trade seed
	// passes the exclusive purchasing frigate). Reuses the same atomic buy+dedicate path (BatchPurchaseShips
	// + fleet assign) BuyAndPlace uses, minus the navigate.
	BuyAndDedicate(ctx context.Context, playerID int, shipType, yard, fleet, purchaserSymbol string) (BuyResult, error)
}

// YardSentinelAcquirer manages the standing yard-sentinel probe: the one-shot buy+reserve, the
// idempotent navigate+dock positioning, and the EXPANSION release. Mirrors GateSurplusReleaser's own
// phase-spanning shape (one collaborator for both halves of one hull's life).
//
// PROTECTION IS A CAPTAIN RESERVATION, NEVER A DEDICATION TAG: BuyAndReserve leaves DedicatedFleet()
// "" forever and instead marks the hull IsAssigned()/!IsIdle() via ReserveForCaptain — the claim/
// assignment axis selectHomeTourHulls's own idle check already refuses, with no change to that
// function. A reservation, not a plain container claim, because it is the one assignment kind that
// survives a daemon restart (ReleaseAllActive's boot sweep excludes it) — a claim under bootstrap's
// own container would be wiped on the next deploy and fall straight back into scouting's reach.
// Release returns it to plain idle with the tag still "" — already adoptStrandedProbes's own ""
// allowlist case, so the already-running probe-sensing coordinator adopts it with no further code.
type YardSentinelAcquirer interface {
	// PriceCheck mirrors ProbeAcquirer/HaulerAcquirer (same asset, SHIP_PROBE).
	PriceCheck(ctx context.Context, playerID int, shipType string) (price int64, yard string, readable bool, err error)
	// BuyAndReserve buys ONE shipType at yard and reserves the bought hull for the captain with
	// `reason`. purchaserSymbol mirrors HaulerAcquirer.BuyAndDedicate ("" ⇒ any idle hull buys).
	BuyAndReserve(ctx context.Context, playerID int, shipType, yard, reason, purchaserSymbol string) (BuyResult, error)
	// EnsureParked flies the sentinel toward a home-system shipyard that can plausibly sell shipType,
	// idempotent and re-derived every call like ShipyardScanner.EnsureShipyardReadable (shares its
	// candidate selection): docked at a VIABLE yard ⇒ no-op; mid-flight ⇒ wait; otherwise ⇒ navigate,
	// then dock. shipType is bootstrap's CURRENT need, re-read every call so a hull docked at a yard
	// confirmed wrong for it is redirected toward one that is.
	EnsureParked(ctx context.Context, playerID int, homeSystem, shipType, shipSymbol string) (docked bool, err error)
	// Release clears the sentinel's captain reservation at the EXPANSION hand-off.
	Release(ctx context.Context, playerID int, shipSymbol, reason string) error
}

// ContractRunner launches the contract fleet coordinator (workflow batch-contract) for a player
// (reuses the existing ContractFleetCoordinator launch). The reconciler calls Start only when the
// observation reports it is not already running, so the launch is idempotent; Start is best-effort and
// its error is logged, not fatal.
type ContractRunner interface {
	StartBatchContract(ctx context.Context, playerID int) error
}

// FrigateContractLoopStarter drives the command frigate's OWN continuous single-hull contract loop
// (DaemonServer.BatchContractWorkflow with iterations=-1). The frigate EARNS IN TRADE now, so bootstrap
// never STARTS this loop; only StopLoop is driven, to clear a loop container an earlier deploy left
// running (an infinite loop never ends by itself and would hold the hull's claim forever). StartLoop
// remains the primitive's other half for any caller that needs it. Unset (nil) ⇒ a logged skip.
type FrigateContractLoopStarter interface {
	StartLoop(ctx context.Context, playerID int, frigateSymbol string) error
	// StopLoop stops the frigate's continuous contract-loop container (StopContainer), releasing the
	// frigate's work-claim so it goes idle and can take up trade. The reconciler drives it only at a
	// cargo-empty safe point, so no accepted contract's cargo is abandoned. Idempotent (stopping an
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
// zero-buy fleet re-balance. It mirrors the contract scaler's DeliverySurplusReleaser (un-dedicate
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

	// ReleaseGateWorkersToTrade re-dedicates the given manufacturing hulls to the TRADE fleet at the
	// EXPANSION hand-off, returning how many it actually re-tagged. It is the same guarded
	// write as ReleaseSurplusGateWorkers but names a DESTINATION instead of clearing to the idle pool,
	// because at EXPANSION there is no adopter to clear them to: the trade coordinator works only hulls
	// ALREADY tagged "trade", the fleet autosizer tags only hulls it BUYS, and the capacity reconciler
	// that once auto-pinned idle hulls was deleted — so an un-dedicated gate hull would sit
	// idle indefinitely unless the contract scaler happened to have a ramp deficit. It carries the same
	// re-guards (still manufacturing-dedicated, still idle, not in transit) plus a cargo-capacity guard,
	// so a hull mid-delivery is never yanked and a 0-cargo hull never lands in the trade pool.
	ReleaseGateWorkersToTrade(ctx context.Context, playerID int, shipSymbols []string) (int, error)
}

// GateWorkerAcquirer price-checks and buys ONE gate-construction worker hull and dedicates it to
// construction (reuses shipyard purchase + fleet assign). The staged top-up when repurposed haulers
// don't cover the pipeline's shape. Mirrors HaulerAcquirer but does not place on a hub (the executor
// claims the worker); PriceCheck readable=false ⇒ the capital gate fails closed (no buy).
type GateWorkerAcquirer interface {
	PriceCheck(ctx context.Context, playerID int, shipType string) (price int64, yard string, readable bool, err error)
	BuyForConstruction(ctx context.Context, playerID int, shipType, yard string) (BuyResult, error)
}

// HandoffLauncher performs the EXPANSION hand-off: it launches the standing fleet-growth coordinator,
// turning the fleet over to the mature demand-driven economy. Guarded on obs.GrowthRunning so a restart
// post-gate re-observes growth running and never re-launches.
type HandoffLauncher interface {
	LaunchStandingCoordinators(ctx context.Context, playerID int, agentSymbol string) error
	// LaunchContractScaler launches the standing dedicated contract auto-scaler during the cold-start
	// scaling window, once the contract-start gate passes. Idempotent (skips when one is
	// already RUNNING/PENDING), so a re-run never double-launches.
	LaunchContractScaler(ctx context.Context, playerID int, agentSymbol string) error
	// LaunchTradeFleetCoordinator launches the standing trade-fleet coordinator at the cold-start trade-seed
	// so the freshly-seeded trade hull is picked up and put on a continuous tour. Idempotent
	// (skips when one is already RUNNING/PENDING) — the observable trade hull is the seeded signal, so a
	// re-run never double-launches.
	LaunchTradeFleetCoordinator(ctx context.Context, playerID int, agentSymbol string) error
}

// PendingScalingReservationPublisher publishes the pending-fleet-scaling reservation
// construction's own spend guard defers to (production_executor_spend_guards.go's
// PendingScalingReservation port). Bootstrap calls Publish every tick a gate-worker or hauler buy
// remains capital-blocked; the read side ages the row out on staleness, so simply not calling
// this again is what retires it — nil-safe/unwired like every other collaborator here.
type PendingScalingReservationPublisher interface {
	Publish(ctx context.Context, playerID int, targetAmount int64) error
}

// RunBootstrapCoordinatorCommand launches the standing bootstrap coordinator for a player.
// Like the fleet-growth / siting coordinators it runs an infinite reconcile loop inside a single
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

	// ContractStartTreasuryThreshold is the flat treasury the contract operation waits for before it
	// starts, live-overlaid each tick by the contract_start_treasury_threshold tune. 0/absent ⇒ the
	// documented default.
	ContractStartTreasuryThreshold int
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
	scanner      ShipyardScanner // Positions a hull at the home yard so the cold price reads
	metrics      MetricsSink
	yardSentinel YardSentinelAcquirer // The standing yard-sentinel's whole lifecycle

	// Contract-workstream collaborators. Each is nil-safe: a nil collaborator degrades the contract
	// action it drives to a logged skip (surfaced as a blocker), never a panic.
	retirer      FrigateRetirer
	haulAcquirer HaulerAcquirer
	contractRun  ContractRunner
	frigateLoop  FrigateContractLoopStarter // the pre-hauler frigate sole-earner contract loop

	// GATE-phase collaborators. Same nil-safe contract.
	construction  ConstructionManager
	manufacturing ManufacturingController
	repurposer    WorkerRepurposer
	gateReleaser  GateSurplusReleaser // Un-dedicate surplus idle gate workers → idle pool (scaler adopts)
	gateAcquirer  GateWorkerAcquirer
	handoff       HandoffLauncher
	tourStarter   HomeTourStarter

	// pendingScaling publishes the capital-blocked-buy signal construction's spend guard reads to
	// raise its floor via MAX. Shared by maybeBuyGateWorker (GATE) and maybeBuyHauler (income).
	pendingScaling PendingScalingReservationPublisher

	// liveConfig snapshots the container's OWN persisted config at each tick start,
	// so a `spacetraders tune --operation bootstrap` of a knob takes effect on the NEXT tick with
	// no restart. Optional-injection: nil keeps the launch-frozen behavior byte-identical.
	liveConfig liveconfig.Reader

	// buyBridges holds the per-container fresh-buy count-sync bridge: it folds probes the
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
	// sequential). Consulted every tick; NOT a progress cursor — dropped on
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

// SetShipyardScanner wires the cold-start shipyard-readability positioner: when the home
// shipyard price is unreadable, it flies an idle hull to the yard so the next tick's live price read
// succeeds. Unset → the coordinator keeps the pre-hh0h fail-closed behavior (byte-identical): an
// unreadable price simply blocks the buy each tick with no repositioning.
func (h *RunBootstrapCoordinatorHandler) SetShipyardScanner(s ShipyardScanner) { h.scanner = s }

// SetMetricsSink wires the metrics recorder. Optional and nil-safe (pure observation).
func (h *RunBootstrapCoordinatorHandler) SetMetricsSink(m MetricsSink) { h.metrics = m }

// SetYardSentinelAcquirer wires the yard-sentinel's buy, positioning, and EXPANSION release. Unset →
// the sentinel is never bought (a logged skip, surfaced loudly — see buyYardSentinel).
func (h *RunBootstrapCoordinatorHandler) SetYardSentinelAcquirer(a YardSentinelAcquirer) {
	h.yardSentinel = a
}

// SetFrigateRetirer wires the "retire the frigate from contract work" action (reuses fleet unassign).
// Unset → the retire is a logged skip.
func (h *RunBootstrapCoordinatorHandler) SetFrigateRetirer(r FrigateRetirer) { h.retirer = r }

// SetHaulerAcquirer wires the price-check + buy-and-place-on-hub path (reuses shipyard purchase +
// fleet assign + navigate). Unset → the contract workstream evaluates and logs but never buys a hauler.
func (h *RunBootstrapCoordinatorHandler) SetHaulerAcquirer(a HaulerAcquirer) { h.haulAcquirer = a }

// SetContractRunner wires the batch-contract launch (reuses the contract fleet coordinator). Unset →
// haulers are placed but batch-contract is not driven (surfaced loudly).
func (h *RunBootstrapCoordinatorHandler) SetContractRunner(c ContractRunner) { h.contractRun = c }

// SetFrigateContractLoopStarter wires the frigate contract-loop primitive. The reconciler drives only
// its STOP half — clearing a loop container an earlier deploy left running. Unset → a legacy loop cannot
// be cleared and keeps its claim on the frigate (surfaced loudly as a logged skip).
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

// SetGateSurplusReleaser wires the "un-dedicate surplus idle gate workers to the idle pool" action
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

// SetHomeTourStarter wires the cold-start market tour. Unset ⇒ bootstrap buys its probes
// and leaves them idle, and says so on its own line rather than failing silently.
func (h *RunBootstrapCoordinatorHandler) SetHomeTourStarter(t HomeTourStarter) {
	h.tourStarter = t
}

// SetHandoffLauncher wires the EXPANSION hand-off (launch the autosizer + standing coordinators). Unset →
// the gate completes but the hand-off is a logged skip, so the mature economy is not launched (surfaced loudly).
func (h *RunBootstrapCoordinatorHandler) SetHandoffLauncher(l HandoffLauncher) { h.handoff = l }

// SetPendingScalingReservationPublisher wires the capital-blocked-buy signal (consumed via MAX on
// the other side). Unset → maybeBuyGateWorker/maybeBuyHauler simply do not publish, RULINGS #4.
func (h *RunBootstrapCoordinatorHandler) SetPendingScalingReservationPublisher(p PendingScalingReservationPublisher) {
	h.pendingScaling = p
}

// publishPendingScalingReservation refreshes the reservation the moment a gate-worker or hauler
// buy is capital-blocked. Best-effort: a publish failure never touches res.Blocker, which is
// already the correct, complete reason for this tick's decision.
func (h *RunBootstrapCoordinatorHandler) publishPendingScalingReservation(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, targetAmount int64, what string) {
	if h.pendingScaling == nil {
		return
	}
	if err := h.pendingScaling.Publish(ctx, cmd.PlayerID, targetAmount); err != nil {
		common.LoggerFromContext(ctx).Log("WARN", fmt.Sprintf("Bootstrap could not publish the pending fleet-scaling reservation for the capital-blocked %s buy (construction's spend guard will not see the raised target this cycle): %v", what, err), map[string]interface{}{
			"action":        "bootstrap_pending_scaling_publish_error",
			"container_id":  cmd.ContainerID,
			"target_amount": targetAmount,
			"error":         err.Error(),
		})
	}
}

// SetLiveConfigReader wires the per-tick live-config snapshot source, making the
// tunable knobs (BootstrapTunableDefaults) honor `spacetraders tune --operation bootstrap` on
// the next tick. Leaving it unset keeps every knob launch-frozen.
func (h *RunBootstrapCoordinatorHandler) SetLiveConfigReader(r liveconfig.Reader) { h.liveConfig = r }

// liveConfigSnapshot takes the tick's live-config snapshot. A nil reader (not wired —
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
	// re-resolves WITH the live snapshot, so a later tune is reflected from that tick on.
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

		// Terminal EXPANSION: the gate is built and the standing economy is
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

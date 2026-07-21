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
	// still clear AFTER a staged bootstrap contract-op spend — the INCOME hauler buy (incl. the sp-7r7w
	// first-hauler pivot) AND the GATE-phase gate-worker/construction spend (sp-bpdf): the spend is
	// affordable when treasury−price ≥ this floor (sp-acv5, PLAYBOOK §3). It replaces the old PROPORTIONAL
	// reserve_margin×treasury gate, which only bought once treasury grew past ~2× the price and so delayed
	// the cash-flow scaling the hauler exists to provide.
	//
	// 150k is the contract operation's OPERATING capital: a light-hauler contract cycle's goods+fuel plus
	// enough headroom to keep several concurrent contract cycles funded through a treasury dip — a
	// deliberately conservative operating floor for the whole contract op, NOT a bare per-buy minimum.
	//
	// DISTINCT from the immutable anti-stall bound (Admiral RULINGS #5, 2026-07-18 amendment split): this
	// contract cushion (150k) is its OWN documented hard constant — no longer common.ImmutableReserveFloor.
	// common.ImmutableReserveFloor (50k) remains the SEPARATE immutable anti-stall backstop: the outer-max
	// clamp that keeps mature tour/factory trade able to trade its way out of a low-treasury crunch, and
	// the line the fleet autosizer clamps to (common.EffectiveReserveFloor) + the capacity reconciler's
	// DefaultReserveFloorCredits equal (their compile-time lockstep guard is untouched, still 50k). Both
	// are hard constants — NOT live-tunable / config.yaml knobs, NOT the shared reserve_margin (which still
	// paces the DATA probe buy). The contract cushion is RAISED above the immutable bound (stricter), so no
	// money guard is weakened (RULINGS #4/#5): a permitted contract-op buy leaves the op funded at 150k.
	defaultContractWorkingCapitalFloor int64 = 150_000

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

	// sp-fp3y GATE-entry gate — now DEFAULT-ON (sp-5nd2): the arm lives in the config.yaml launch layer
	// (cmd.ScaledGateEntryDisabled negation), so GATE entry requires a genuinely SCALED contract op
	// (haulers + a SUSTAINED $/hr) — closing the ktio deadlock where one contract payout spiked income
	// past income_bar and drove GATE with ZERO haulers, latching on ConstructionStarted. defaultScaledGateEntry
	// is the FORCE-ON positive tune's default: 0 = not-forced (the config default carries the arm); a live
	// `tune scaled_gate_entry 1` still force-arms even if config disabled it. The live scaled_gate_entry_disabled
	// tune is the no-restart kill-switch. Arms TOGETHER WITH ktio-B (sp-sjvv) — both default-on as a pair.
	defaultScaledGateEntry = 0
	// defaultGateIncomeBar is the SUSTAINED (rolling-mean over gateIncomeWindowTicks) net credits/hour the
	// contract fleet must clear to enter GATE when scaled_gate_entry is armed. Deliberately well ABOVE
	// income_bar (10000): a single contract payout momentarily spikes instantaneous income past 10000 (the
	// ktio false trigger), but a genuinely scaled 2–4 hauler op sustains net $/hr in this range once warmed
	// while a lone spike, smoothed over the window alongside the net-negative spend ticks, nets far less.
	// The primary field-calibration knob for the armed gate (tunable via gate_income_bar). It is a
	// phase-transition threshold like income_bar, NOT a money-floor (RULINGS #5) — no spend guard reads it.
	// A bar set too HIGH only delays GATE; too low re-opens the spurious trigger.
	defaultGateIncomeBar = 50000.0
	// defaultGateMinHaulers is the hauler floor for armed GATE entry: the INCOME ramp must hold at least
	// this many contract-dedicated haulers, proving a multi-hull op (the ktio deadlock entered GATE with
	// ZERO haulers — a frigate-only contract spike). Set BELOW hauler_target (4) because viable hubs may be
	// fewer than the target, so requiring the full target could wedge a legitimately-scaled op; 2 clearly
	// separates a scaled op from the 0-hauler spike. Tunable via gate_min_haulers.
	defaultGateMinHaulers = 2
	// gateIncomeWindowTicks is how many recent reconcile ticks the armed GATE-entry $/hr is smoothed over
	// (the "sustained" window). A call-site constant, not a knob — a shape detail of the sustained metric,
	// bounded in wall-clock by tick_secs (at the 45s cold-start cadence, 5 ticks ≈ 3.75 min of sustained
	// earning). The window must be FULL before it can clear the bar, so a spike on a fresh/short history
	// (the first ticks after arming, or after a restart drops the window) can never trip GATE.
	gateIncomeWindowTicks = 5

	// defaultAutosizerEarlyScaling is the sp-sjvv cold-start-contract-scaling flag — now DEFAULT-ON
	// (sp-5nd2) via the config.yaml launch layer (cmd.AutosizerEarlyScalingDisabled negation). This const
	// is the FORCE-ON positive tune's default: 0 = not-forced (the config default carries the arm); the
	// live autosizer_early_scaling_disabled tune is the no-restart kill-switch. Armed, ONE flag arms TWO coupled behaviors so
	// the capacity reconciler's emitted contract-delivery demand finally has a buyer during cold start
	// (the ktio-B fix): (1) bootstrap LAUNCHES the fleet autosizer EARLY, during the DATA/INCOME
	// scaling window, so the reconciler's demand is consumed by the autosizer's guard-gated buy path
	// (contract_delivery armed via sp-nkqn's own contract_delivery_hulls_enabled config knob); and
	// (2) bootstrap DEFERS its own contract-hauler buys to that autosizer once it is running
	// (single-buyer arbitration — the two never bid on one treasury), which also dissolves the
	// maybeBuyHauler no_purchaser deadlock. The coupling is deliberate: bootstrap only defers to a
	// buyer it has confirmed running, so a cold start can never wedge on an absent autosizer. This
	// REVERSES the deliberate "autosizer off the whole bootstrap run" guard; the arbitration + the
	// ktio-A absolute treasury floor (sp-bpdf) are the load-bearing safety that replaces it.
	defaultAutosizerEarlyScaling = 0

	// The sp-5nd2 live kill-switches: 0 = NOT disabled (the default — both features run, armed via the
	// config.yaml launch layer). A live `tune scaled_gate_entry_disabled 1` / `autosizer_early_scaling_disabled 1`
	// stands the respective feature down on the next tick with no restart; `... 0` deletes the key → reverts
	// to the config default (armed). Inverted polarity so the zero value stays default-ON, and so the
	// mutateTuneConfigKey `0 = revert-to-default` contract is untouched (RULINGS #4 — no tune-write change).
	defaultScaledGateEntryDisabled       = 0
	defaultAutosizerEarlyScalingDisabled = 0

	// defaultContractScalerEarlyScaling is the DEFAULT-OFF arm for the dedicated contract auto-scaler:
	// 0 = OFF, so a bare deploy never launches the scaler (byte-identical). UNLIKE the autosizer arm
	// (default-ON), this is a positive tunable-only flag armed after validation
	// (`tune --operation bootstrap contract_scaler_early_scaling 1`). Armed, bootstrap launches the
	// standing scaler EARLY during the DATA/INCOME window so it ramps the exclusive contract fleet behind
	// the 200000 cushion; it is the eventual REPLACEMENT for the reconciler+autosizer contract-delivery
	// path, so the operator arms one or the other, never both.
	defaultContractScalerEarlyScaling = 0

	// Scaled-gate hardening — the DEFAULT-OFF master flag (0 = byte-identical). It EXTENDS the scaled
	// GATE-entry gate with a three-part cold-start-death-spiral cure: (1) GATE entry also requires a RAISED
	// hauler floor, a SUSTAINED $/hr, AND a treasury surplus war chest; (2) GATE keeps a higher
	// contract-earner floor so the repurpose-first seed never cannibalizes the contract op below the capacity
	// reconciler's depot-staging pool; (3) a sticky GATE that latched under-scaled with ~no construction
	// re-derives INCOME (so the op re-scales) after an anti-thrash hysteresis streak. A positive tunable flag
	// with no launch key (like defer_probe_to_freshsizer), armed live after validation. Gate entry only ever
	// tightens, never loosens (RULINGS #4).
	defaultGateSurplusHardening = 0
	// defaultGateHaulerFloor is the RAISED GATE-entry hauler floor while hardening is armed: a genuinely
	// scaled contract op, NOT the 2-hull minimum the plain scaled gate (gate_min_haulers) admits. Aligned
	// with hauler_target (4) so GATE waits for a real fleet rather than latching on a 2-hauler income blip.
	// Clamped to ≥ gate_min_haulers at read so hardening can never be LOOSER than the plain scaled gate on
	// the hauler dimension (RULINGS #4).
	defaultGateHaulerFloor = 4
	// defaultGateSurplusFloor is the treasury SURPLUS — over common.ImmutableReserveFloor (50k) — the op must
	// hold to enter GATE while hardening is armed: a war chest for the jump-gate material bill (~1600 FAB_MATS
	// + 400 ADVANCED_CIRCUITRY) so GATE is earned from contract surplus, never raced on a thin treasury its
	// own material spend then crashes. 500k (⇒ treasury ≥ 550k to gate) is a CONSERVATIVE placeholder — tuned
	// against the freshly-read gate bill. It is a PHASE-entry threshold, NOT a spend guard (RULINGS #5); the
	// buy-time 150k working-capital floor is untouched. Base choice: the immutable 50k anti-stall bound; the
	// 150k contract cushion is the stricter alternative if contract working capital should not count as surplus.
	defaultGateSurplusFloor int64 = 500_000
	// defaultGateContractFloor is how many contract-dedicated haulers stay EARNING through GATE while
	// hardening is armed — GATE repurposes only the surplus ABOVE this to construction, never below: the
	// capacity reconciler withholds the staging depot below a 2-hauler pool, so cannibalizing to 1 starves
	// sourcing and funding collapses. 2 holds the depot-staging floor. Supersedes min_contract_earners (1)
	// only while armed.
	defaultGateContractFloor = 2
	// defaultGateReentryConstructionPct is the construction-progress ceiling (whole percent, 0..100) below
	// which an under-scaled sticky GATE may re-derive INCOME (the escape hatch). 5% scopes the escape to a
	// GATE that latched but never really built — past it real materials are flowing (the manufacturing
	// executor keeps delivering regardless of bootstrap's phase) and GATE is permanent.
	defaultGateReentryConstructionPct = 5.0
	// defaultGateReentryStreakTicks is how many CONSECUTIVE under-scaled + low-progress ticks must hold before
	// the GATE→INCOME re-derive fires — anti-thrash hysteresis: a single dip never flips the phase, and the
	// direction is asymmetric (SLOW to leave GATE over N ticks, immediate to resume it once the op re-scales),
	// so the phase strongly prefers GATE and only escapes a genuinely, persistently starved latch. The streak
	// is in-memory per-container and fails SAFE on restart: a dropped streak just re-accrues from 0 (delays the
	// re-derive one window), never double-acts (the re-derive is a pure phase relabel — no spend, no
	// assignment). 3 ticks ≈ 2.25 min at the 45s cold-start cadence.
	defaultGateReentryStreakTicks = 3
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

// ScoutPostDeclarer declares the STANDING scout-post COVERAGE target for a system — the
// desired-state post the boot-standing scout-post coordinator (sp-9ujl) mans by claiming an idle
// probe. It is a coverage declaration, NOT a probe assignment: bootstrap declares the home post and
// leaves its probes IDLE, and the coordinator does the manning (claim → VRP-partition → scan),
// which seeds the initial home scan → census → the freshsizer takes over declaring the rest.
// Idempotent — a post already declared for the system is preserved, not re-touched — so bootstrap
// can call it every DATA tick. This REPLACES the old probe-holding scout-all-markets sweep, which
// held the probes and starved the now-boot-standing coordinator (sp-pt7d, Admiral intent: bootstrap
// buys probes but assigns them to nothing; the coordinator mans them).
type ScoutPostDeclarer interface {
	DeclareHomeScoutPost(ctx context.Context, playerID int, system string) error
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
	// LaunchContractScaler launches the standing dedicated contract auto-scaler during the cold-start
	// scaling window when contract_scaler_early_scaling is armed. Idempotent (skips when one is already
	// RUNNING/PENDING), so a re-run never double-launches. Default-off: nothing calls it until the flag
	// is armed, so a bare deploy never launches the scaler (byte-identical).
	LaunchContractScaler(ctx context.Context, playerID int, agentSymbol string) error
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

	// Cold-start economics arms (sp-5nd2), default-ON via the bootstrap_disabled negation idiom: an
	// absent/false disable flag reads as ARMED, so the zero value is LIVE-BY-DEFAULT and resolve needs
	// no positive fallback. Set true (config.yaml bootstrap_*_disabled) to stand the feature down at
	// launch; the live *_disabled tune is the no-restart kill-switch.
	ScaledGateEntryDisabled       bool // true ⇒ GATE entry falls back to the bare income_bar (sp-fp3y off).
	AutosizerEarlyScalingDisabled bool // true ⇒ the early autosizer + single-buyer arbitration stay off (sp-sjvv off).
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

	refresher    ShipRefresher
	observer     WorldObserver
	acquirer     ProbeAcquirer
	postDeclarer ScoutPostDeclarer
	scanner      ShipyardScanner // sp-hh0h: positions a hull at the home yard so the cold price reads
	metrics      MetricsSink

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

	// incomeWindows holds the per-container GATE-entry income smoother (sp-fp3y): the rolling window of
	// recent realized-$/hr readings whose mean is the SUSTAINED $/hr the armed scaled-gate-entry gate reads,
	// so a lone instantaneous income spike never trips GATE with an unscaled op (the ktio deadlock). Keyed by
	// ContainerID for the same singleton reason as buyBridges; incomeWindowMu guards the MAP only (one
	// container's ticks are sequential — see incomeWindowFor). Consulted only when scaled_gate_entry is armed;
	// like buyBridges it is NOT a progress cursor (dropped on restart, and GATE entry re-defers a few ticks
	// while it re-fills), so phase/progress stays derived purely from observation.
	incomeWindowMu sync.Mutex
	incomeWindows  map[string]*incomeWindow

	// underScaledStreaks holds the per-container escape-hatch hysteresis counter: consecutive ticks a
	// sticky-latched GATE has been under-scaled with ~no construction, so the GATE→INCOME re-derive fires only
	// after gate_reentry_streak_ticks in a row (anti-thrash). Keyed by ContainerID for the same singleton
	// reason as buyBridges/incomeWindows; underScaledStreakMu guards the MAP only (one container's ticks are
	// sequential). Consulted only when gate_surplus_hardening is armed; NOT a progress cursor — dropped on
	// restart (the re-derive just re-accrues from 0, delaying one window, never double-acting).
	underScaledStreakMu sync.Mutex
	underScaledStreaks  map[string]int
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

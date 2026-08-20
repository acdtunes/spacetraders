// Package commands holds the captain bootstrap coordinator: a standing per-player reconciler
// that encodes the known-good cold-start playbook and drives a fresh agent toward the jump gate
// — autonomously, idempotently, recoverably — so the captain launches it once and monitors
// rather than re-deriving the cold-start sequence every era.
//
// Shape mirrors the fleet-autosizer / siting coordinators (the daemon-container idiom): a
// registered singleton handler with optional setter-collaborators, one infinite reconcile loop
// in Handle(), resolveBootstrapConfig() resolving every <=0 knob to a documented default
// (RULINGS #5). It is a RECONCILER, not a stored-cursor script — each tick it OBSERVES the live
// world (the game is the source of truth), DERIVES the phase from that observation (never a
// persisted enum), and ACTS on the delta with each action guarded "already done / in-flight?".
package commands

import "time"

// Phase is the bootstrap arc phase. It is ALWAYS derived from the current observation (fleet,
// construction, treasury) and NEVER read back from storage — a persisted enum can desync from the
// live world, which is the whole failure mode a reconciler exists to avoid (spec §Architecture).
type Phase string

const (
	// PhaseColdStart is the cold start, and it drives TWO PARALLEL workstreams every tick: scanning
	// (buy probes to target so market data can flow) and earning (put the frigate in the trade fleet,
	// then — once treasury clears the contract-start threshold — run the contract coordinator and stage
	// the hub haulers). Each self-guards to a no-op once its own work is done, so one finishing never
	// holds the other back.
	PhaseColdStart Phase = "COLDSTART"
	// PhaseGate is jump-gate construction: start the construction pipeline, ensure the executor
	// has adopted it, and size the gate workforce until the site is complete.
	PhaseGate Phase = "GATE"
	// PhaseExpansion is the terminal: the jump-gate construction is COMPLETE — the gate is built,
	// steady-state growth begins (sp-feiy7, Admiral 2026-07-24: "we should add a new phase called
	// EXPANSION — this is the phase where we start to buy probes"). It replaces the old COMPLETE
	// label in that exact slot: bootstrap's own job here is unchanged (hand the standing economy
	// off, then exit), but the DERIVED phase now names the era the world has entered — the phase
	// growth spenders (the probe-buyer fleet) key on. Like a built gate it is monotone:
	// once derived it never regresses to a buying phase.
	PhaseExpansion Phase = "EXPANSION"
)

// Observation is one tick's read of the live world — the reconciler's entire input. Everything the
// phase derivation and the cold-start guards need is here, read fresh each tick so a restart
// resumes at real state with no persisted cursor (spec §Idempotency). A read that could not gather
// all its inputs sets Readable=false, which the reconciler treats as fail-closed: NO action this
// tick (a missing signal must never drive a spend or an assignment).
type Observation struct {
	// HomeSystem is the system the cold-start plays out in (the command ship's / HQ system) — the
	// system probes scout and where probes are bought. "" when it could not be resolved.
	HomeSystem string
	// ProbeCount is how many probe/satellite hulls exist NOW, counting every nav status: a
	// freshly-bought probe still navigating counts, so re-observation after a
	// buy never re-triggers the buy (the idempotency that makes a mid-purchase restart a non-event).
	ProbeCount int
	// ProbesScouting is how many probes are already assigned to a scout-tour — the idempotency
	// guard for the scout-assignment action (skip re-assigning when every probe is already scouting).
	ProbesScouting int
	// ProbesUntoured is how many probes sit IDLE at HOME having never been put on a scout tour —
	// what separates a fleet that GREW from a tour that ENDED, since both read ProbesScouting <
	// ProbeCount yet call for opposite answers. 0 on an unreadable read, which holds the tour.
	ProbesUntoured int
	// HasIdlePurchaser reports whether an idle hull exists to fly to a shipyard and execute a buy
	// (the batch-purchase path needs a purchasing hull). When false the buy is BLOCKED, not failed.
	HasIdlePurchaser bool
	// MarketsCovered is how many SCOUTABLE home-system marketplaces have (fresh) market data — the
	// heartbeat's scan-progress numerator (observability only; coverage gates nothing).
	MarketsCovered int
	// MarketsTotal is how many SCOUTABLE marketplaces the home system has (not FUEL_STATION) — that
	// same denominator. 0 when no waypoints are known yet (a cold agent), which reads as 0 coverage.
	MarketsTotal int
	// Treasury is live agent credits — the capital-gate input.
	Treasury int64

	// --- Contract-income workstream signals (Slice 2). Zero values are the cold-start default (no
	// income yet, no haulers, frigate untagged, no market data), so an observation that leaves them
	// unset reads as "contracts not started" and the scanning guards are unaffected. ---

	// IncomePerHour is the contract fleet's realized net credits/hour over a trailing window — reported on
	// the heartbeat so an operator can watch the contract op warm up. Realized (booked ledger income), not
	// projected. It is OBSERVABILITY ONLY: it drives no phase transition, because a single contract payout
	// swings it from net-negative to a false all-clear in one tick.
	IncomePerHour float64
	// CommandFrigateID is the command frigate's ship symbol — the hull that trades and, at the
	// first-hauler pivot, buys. "" when no command hull is resolved.
	CommandFrigateID string
	// CommandFrigateOnContract reports whether the command frigate currently carries the "contract"
	// fleet dedication (so the contract coordinator's dedicated pool would draft it). true ⇒ retire it
	// (clear the tag); false ⇒ already retired (the idempotency guard).
	CommandFrigateOnContract bool
	// CommandFrigatePurchasing reports whether the command frigate carries the "purchasing" dedication —
	// the EXCLUSIVE purchasing-ship role set at the first-hauler pivot. Once true nothing re-tags it until
	// its buys land (so an in-flight purchase survives a restart, even before a hauler is observed), and
	// it is off-limits to the contract op. It also latches the contract-start threshold against the
	// treasury dip its own buys cause.
	CommandFrigatePurchasing bool
	// CommandFrigateOnTrade reports whether the command frigate carries the "trade" fleet dedication —
	// its STANDING home in the cold-start design: the frigate tours under the trade-fleet coordinator from
	// tick 1 and returns to it after its cold-start buys. false ⇒ dedicate it (the idempotency guard).
	CommandFrigateOnTrade bool
	// CommandFrigateIdle reports whether the command frigate is genuinely free RIGHT NOW — idle (no
	// container claim, no captain reservation) and not still flying. Same expression as
	// GateWorkerSnapshot.Idle, and the same partition the trade-fleet coordinator's relaunch logic draws.
	// With CommandFrigateOnTrade it is the "idle in trade" signal the first-hauler pivot fires off
	// (PLAYBOOK §9). Defaults false on an unresolved frigate ⇒ fail-safe (no re-dedication, no pivot).
	CommandFrigateIdle bool
	// CommandFrigateLastRunStart / CommandFrigateLastRunEnd are the claim and release times of the
	// frigate's LAST container run, read straight off the persisted ship assignment (zero ⇒ no run on
	// record). They are the whole cross-tick channel for the starved-trade contract fallback (RULINGS
	// #2, nothing held in memory): a run too short to have traded that is STILL parked is a tour the
	// trade-fleet coordinator has escalated on, not one it is about to relaunch.
	CommandFrigateLastRunStart time.Time
	CommandFrigateLastRunEnd   time.Time
	// Haulers is the contract-dedicated hauler pool NOW — each with the waypoint it is placed on (or
	// heading to). Its length is the staged-buy count guard (buy while below haulerTarget); the waypoints
	// are the "slot already served" placement guard.
	Haulers []HaulerSnapshot
	// BatchContractRunning reports whether the contract fleet coordinator (workflow batch-contract) is
	// already running for this player — the idempotency guard for the batch-contract launch (never
	// relaunch a running coordinator).
	BatchContractRunning bool
	// FrigateContractLoopRunning reports whether the command frigate's OWN continuous single-hull
	// contract loop is running — a CONTRACT_WORKFLOW loop container (batch-contract --loop,
	// iterations=-1) on the frigate. The frigate earns in TRADE now, so this signals only a loop an
	// earlier deploy left running: it is stopped once, at a cargo-empty safe point, because an infinite
	// loop never ends by itself and would hold the hull's claim forever. It is DISTINCT from
	// BatchContractRunning, which detects the contract_fleet_coordinator TYPE and does NOT see this
	// per-hull loop container. false ⇒ no frigate loop (the fresh cold-start default).
	FrigateContractLoopRunning bool
	// FrigateCargoEmpty reports whether the command frigate currently carries NO cargo — the SAFE POINT
	// for the first-hauler pivot. Stopping the frigate's contract loop mid-delivery would
	// abandon in-flight contract cargo, so the pivot fires ONLY when the frigate is empty (between
	// contracts); a loaded frigate defers the pivot a tick (the loop delivers + empties). Defaults false
	// on an unresolved/unreadable frigate ⇒ the pivot is BLOCKED (fail-safe: never stop the earner on an
	// unknown state), so the buy waits rather than risk losing cargo.
	FrigateCargoEmpty bool
	// ContractPlacementSlots is this era's FIXED delivery placement set: the ≤6 central parks the contract
	// auto-scaler buys against and the contract coordinator's between-legs homing zips hulls onto — ONE
	// slot set for every positioning consumer, so the ramp's placements never drift from where the standing
	// op then homes them. It rests on STATIONARY inputs (home-system waypoint geometry + market role), so it
	// does not move with whatever contract happens to be live. Empty ⇒ the era's parks are unresolved (an
	// uncharted/unscanned home), which the ramp reads fail-closed: no placement target, no hauler buy.
	ContractPlacementSlots []string
	// ContractGraduated reports the durable per-player era-scoped contract-graduation flag:
	// the operator has retired contracts as the funding floor. When true, the contract workstream (actIncome
	// — batch-contract, the frigate sole-earner loop, staged hauler buys) does NOT run, DURABLY across
	// restarts, so a boot-standing bootstrap never re-establishes the contract earner on a graduated fleet.
	// False (the default / a fresh era / a read miss) ⇒ contracts run as today — byte-identical, fail-OPEN.
	// It gates ONLY the contract-income workstream; scanning (probes/scouting), GATE (construction), and
	// trade are untouched.
	ContractGraduated bool

	// --- GATE-phase signals. Zero values are the pre-GATE default (no gate site known, no
	// construction pipeline, the executor down, no gate workers), so a cold-start observation that leaves
	// them unset reads as "GATE not started" and the cold-start guards are unaffected. ---

	// GateSite is the home-system jump-gate construction site waypoint (an under-construction JUMP_GATE).
	// "" when it could not be resolved yet (no waypoint data) or the system has no gate to build — a
	// blocker, not an error (a later tick with data retries).
	GateSite string
	// ConstructionStarted reports whether a construction pipeline ALREADY exists for GateSite. It is
	// BOTH the idempotency guard for `construction start` (never create a second pipeline) AND the
	// STICKY-GATE signal: once a pipeline exists the arc stays in GATE even as contract income falls with
	// repurposed haulers, so derivePhase never regresses GATE→COLDSTART (which would re-buy haulers and
	// thrash). A restart mid-GATE re-observes this true → resumes in GATE.
	ConstructionStarted bool
	// ConstructionComplete reports whether the gate construction site is 100% delivered — the
	// GATE→EXPANSION exit. Terminal and monotone (a built gate stays built — the observer reports a
	// BUILT home gate as complete), so a restart post-completion re-derives EXPANSION.
	ConstructionComplete bool
	// ConstructionPercent is the site's delivery progress in [0,100] — heartbeat + metrics only (never a guard).
	ConstructionPercent float64
	// GateMaterialChains is how many active gate-material producing chains the started pipeline reveals —
	// the worker-sizing top-up target (~one worker per chain). 0 before the pipeline reveals its shape (so
	// the top-up BUY holds until the shape is known; repurposing idle haulers as the seed does not wait on it).
	GateMaterialChains int
	// ManufacturingRunning reports whether the manufacturing coordinator — the construction EXECUTOR that
	// claims worker hulls and runs produce/deliver — is running for this player. false ⇒ ensure it running.
	ManufacturingRunning bool
	// ManufacturingAdopted reports whether the running manufacturing coordinator has ADOPTED the gate
	// construction pipeline (claimed/started its tasks). A freshly-created pipeline is INERT until the
	// executor adopts it at startup (captain L57), so running-but-!adopted ⇒ BOUNCE it. true ⇒ already
	// adopted (the idempotency guard, so a restart mid-GATE never re-bounces a healthy executor).
	ManufacturingAdopted bool
	// GateWorkers is how many hulls are NOW dedicated to gate construction (claimed by the executor) — the
	// worker-sizing "have" count, so the staged top-up buy never overshoots the pipeline's shape.
	GateWorkers int
	// GateWorkerHulls is the per-hull detail of those same manufacturing-dedicated gate workers —
	// each with its idle status — the surplus-release selection input. len(GateWorkerHulls) == GateWorkers by
	// construction (the observer appends one here for every GateWorkers++). Only planGateWorkers's surplus
	// path reads it, so it is inert unless the executor is over-provisioned; mirrors how obs.Haulers carries
	// the per-hull delivery detail alongside its count-based guards.
	GateWorkerHulls []GateWorkerSnapshot
	// GrowthRunning reports whether the standing fleet-GROWTH coordinator is already running — the
	// EXPANSION launch-once hand-off guard (a restart post-gate re-observes it running ⇒ no re-launch,
	// no exit loop). It names GROWTH because growth is what the hand-off starts; a latch naming
	// anything else lets a restart re-run a hand-off that already happened.
	GrowthRunning bool

	// TradeHullCount is the number of 'trade'-fleet-dedicated hulls NOW — the observable trade-seeded
	// signal. The trade hull EXISTING is the durable "seeded" marker: idempotent by
	// construction, auto-re-derived each tick from the live fleet (no stored flag), so it is restart-safe.
	// It drives the cold-start hull-routing trade-seed: acquisition #2 → trade, held until a trade hull
	// exists. Mirrors how obs.Haulers counts contract-dedicated hulls, filtering on the "trade" tag
	// instead. 0 (the cold-start default) ⇒ not yet seeded.
	TradeHullCount int

	// ContractDepotHullCount is the DEPOT half of the contract fleet NOW: hulls whose
	// DedicatedFleet() is "warehouse" OR "stocker" — the central far-source warehouse + stocker the
	// contract auto-scaler grows (container_ops_depot_launch.go stamps these tags). len(obs.Haulers) is
	// the DELIVERY half; the two sum to the FULL contract fleet the GATE-entry bar measures against the
	// scaler's target. Mirrors how obs.Haulers/TradeHullCount count by DedicatedFleet() tag; 0 (the
	// cold-start default) ⇒ no depot yet, so the full fleet is just the delivery haulers.
	ContractDepotHullCount int

	// ContractScalerTarget is the contract auto-scaler's live ACHIEVABLE fleet target NOW —
	// min(scaler plan slots, the scaler's live contract_fleet_max_hulls ceiling) — the HARD bar
	// GATE entry measures the full contract fleet (Haulers + ContractDepotHullCount) against.
	// GATE is entered only once the contract op has genuinely reached the
	// size the scaler is driving it toward, so a lightly-scaled op can never latch GATE and cannibalize
	// itself (the death spiral). 0 (no scaler running / unread target) ⇒ FAIL-CLOSED: gateFunded
	// never gates on an unknown target, so a scaler-less op stays in cold start rather than entering GATE blind.
	ContractScalerTarget int

	// Readable reports whether the observer gathered all its inputs. false ⇒ fail-closed (no action
	// this tick), with Reason naming what could not be read.
	Readable bool
	// Reason is a human note for the decision/heartbeat log (why unreadable, or context).
	Reason string
}

// HaulerSnapshot is one contract-dedicated hauler's identity + placement, read each tick. Waypoint is
// where the hull is (idle) or heading (in transit) — the "hub already served" key for the staged
// hauler buy (a hub is served when a hauler's Waypoint is on it).
type HaulerSnapshot struct {
	Symbol   string
	Waypoint string
}

// GateWorkerSnapshot is one manufacturing-dedicated gate worker's identity + release-eligibility.
// Idle is true ONLY when the hull is genuinely free (idle and not in transit), so the surplus-release
// selection can never pick a hull mid-construction-task. It carries only what the release decision needs,
// mirroring HaulerSnapshot's minimal shape.
type GateWorkerSnapshot struct {
	Symbol string
	Idle   bool
}

// CoverageFraction is MarketsCovered / MarketsTotal (scoutable markets only), and 0 when nothing
// is known yet (total 0) so a cold agent reads as uncovered rather than dividing by zero.
func (o Observation) CoverageFraction() float64 {
	if o.MarketsTotal <= 0 {
		return 0
	}
	return float64(o.MarketsCovered) / float64(o.MarketsTotal)
}

// BuyResult is what a probe purchase returns for the decision log.
type BuyResult struct {
	ShipSymbol string
	Price      int64
}

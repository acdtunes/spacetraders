// Package commands holds the captain bootstrap coordinator (sp-3nbe): a standing per-player
// reconciler that encodes the known-good cold-start playbook and drives a fresh agent toward
// the jump gate — autonomously, idempotently, recoverably — so the captain launches it once and
// monitors rather than re-deriving the cold-start sequence every era.
//
// Shape mirrors the fleet-autosizer / siting coordinators (the daemon-container idiom): a
// registered singleton handler with optional setter-collaborators, one infinite reconcile loop
// in Handle(), resolveBootstrapConfig() resolving every <=0 knob to a documented default
// (RULINGS #5). It is a RECONCILER, not a stored-cursor script — each tick it OBSERVES the live
// world (the game is the source of truth), DERIVES the phase from that observation (never a
// persisted enum), and ACTS on the delta with each action guarded "already done / in-flight?".
//
// Slice 1 built the whole framework + the DATA phase. Slice 2 (sp-ysgb.1) adds the INCOME phase:
// retire the command frigate from contract work, select contract hubs from the scouted market data,
// staged-buy one light hauler per viable hub (capped at hauler_target), run batch-contract, and exit
// to GATE when realized $/hr clears income_bar. GATE (Slice 3) is still a terminal stub — a phase
// derived past INCOME holds at "not yet implemented, holding at INCOME-complete", not an error.
package commands

// Phase is the bootstrap arc phase. It is ALWAYS derived from the current observation (market
// coverage, fleet, ...) and NEVER read back from storage — a persisted enum can desync from the
// live world, which is the whole failure mode a reconciler exists to avoid (spec §Architecture).
type Phase string

const (
	// PhaseData is the cold-start data phase: buy probes to target and scout every market so
	// contract/hub selection has data to work from. The one LIVE phase in Slice 1.
	PhaseData Phase = "DATA"
	// PhaseIncome is the contract-income ramp: retire the frigate, staged-buy hub haulers, run
	// batch-contract, exit to GATE when realized $/hr ≥ income_bar (LIVE as of Slice 2).
	PhaseIncome Phase = "INCOME"
	// PhaseGate is jump-gate construction (Slice 3 — still a terminal stub: a phase derived past
	// INCOME holds at "not yet implemented, holding at INCOME-complete").
	PhaseGate Phase = "GATE"
	// PhaseComplete is the terminal: the gate is built, standing coordinators handed off
	// (Slice 3 — not implemented in Slice 1).
	PhaseComplete Phase = "COMPLETE"
)

// Observation is one tick's read of the live world — the reconciler's entire input. Everything the
// phase derivation and the DATA-phase guards need is here, read fresh each tick so a restart
// resumes at real state with no persisted cursor (spec §Idempotency). A read that could not gather
// all its inputs sets Readable=false, which the reconciler treats as fail-closed: NO action this
// tick (a missing signal must never drive a spend or an assignment).
type Observation struct {
	// HomeSystem is the system the cold-start plays out in (the command ship's / HQ system) — the
	// system probes scout and where probes are bought. "" when it could not be resolved.
	HomeSystem string
	// ProbeCount is how many probe/satellite hulls exist NOW, counting every nav status: a
	// freshly-bought probe still navigating to its scout post counts, so re-observation after a
	// buy never re-triggers the buy (the idempotency that makes a mid-purchase restart a non-event).
	ProbeCount int
	// ProbesScouting is how many probes are already assigned to a scout-tour — the idempotency
	// guard for the scout-assignment action (skip re-assigning when every probe is already scouting).
	ProbesScouting int
	// HasIdlePurchaser reports whether an idle hull exists to fly to a shipyard and execute a buy
	// (the batch-purchase path needs a purchasing hull). When false the buy is BLOCKED, not failed.
	HasIdlePurchaser bool
	// MarketsCovered is how many home-system marketplaces have (fresh) market data — the DATA-exit
	// numerator.
	MarketsCovered int
	// MarketsTotal is how many marketplaces the home system has — the DATA-exit denominator. 0 when
	// no waypoints are known yet (a cold agent), which reads as 0 coverage (stay in DATA).
	MarketsTotal int
	// Treasury is live agent credits — the capital-gate input.
	Treasury int64

	// FreshsizerActive reports whether a market-freshness-sizer coordinator is RUNNING for this
	// player — the sp-tsn2 probe-buyer-arbitration input. When the deferral knob is armed and the
	// first market is covered, bootstrap hands probe acquisition to the freshsizer so exactly one
	// buyer grows the shared fleet during the conflict window. false ⇒ bootstrap NEVER defers (it
	// must not defer into a vacuum — a cold start would wedge if no buyer provisions probes).
	FreshsizerActive bool

	// --- INCOME-phase signals (Slice 2). Zero values are the cold-start default (no income yet, no
	// haulers, frigate untagged, no market data), so a DATA-phase observation that leaves them unset
	// reads as "INCOME not started" and the DATA guards are unaffected. ---

	// IncomePerHour is the contract fleet's realized net credits/hour over a trailing window — the
	// INCOME→GATE exit input. Realized (booked ledger income), not projected, so the bar measures a
	// fleet that is genuinely earning. 0 on a fresh INCOME entry keeps the arc in INCOME (income_bar is
	// positive by default), so it never skips straight to GATE.
	IncomePerHour float64
	// CommandFrigateID is the command frigate's ship symbol — the hull retired from contract work (a
	// poor contract worker: low fuel/cargo). "" when no command hull is resolved.
	CommandFrigateID string
	// CommandFrigateOnContract reports whether the command frigate currently carries the "contract"
	// fleet dedication (so the contract coordinator's dedicated pool would draft it). true ⇒ retire it
	// (clear the tag); false ⇒ already retired (the idempotency guard).
	CommandFrigateOnContract bool
	// Haulers is the contract-dedicated hauler pool NOW — each with the waypoint it is placed on (or
	// heading to). Its length is the staged-buy count guard (buy while < one-per-viable-hub, capped at
	// hauler_target); the waypoints are the "hub already served" placement guard.
	Haulers []HaulerSnapshot
	// BatchContractRunning reports whether the contract fleet coordinator (workflow batch-contract) is
	// already running for this player — the idempotency guard for the batch-contract launch (never
	// relaunch a running coordinator).
	BatchContractRunning bool
	// FrigateContractLoopRunning reports whether the command frigate's OWN continuous single-hull
	// contract loop is already running (sp-rype) — a CONTRACT_WORKFLOW loop container (sp-ehg9
	// batch-contract --loop, iterations=-1) on the frigate. This is the earner-signal guard for the
	// pre-hauler frigate loop: bootstrap starts it exactly once and never re-starts a running loop.
	// It is DISTINCT from BatchContractRunning, which detects the contract_fleet_coordinator TYPE and
	// does NOT see this per-hull loop container (sp-ehg9 note): the two are separate earners, so the
	// loop needs its own signal. false ⇒ no frigate loop yet (the fresh cold-start default).
	FrigateContractLoopRunning bool
	// Markets is the scouted market data for the home system(s) — the contract-hub selector's input
	// (each marketplace's sourceable goods + purchase prices). Empty ⇒ no hubs selectable this tick
	// (fail-closed: no hauler buys), which a fresh INCOME entry before scouting completes reads as.
	Markets []MarketSnapshot
	// ContractGoods is the set of goods the player's available/active contracts demand — the selector
	// scores hubs by how cheaply they source THESE. Empty ⇒ the selector falls back to overall market
	// density + cheapness (a dense, cheap market is a sound generic contract hub), so hub selection
	// works even before the first contract is accepted.
	ContractGoods []string
	// ContractGraduated reports the durable per-player era-scoped contract-graduation flag (sp-difa.1):
	// the operator has retired contracts as the funding floor. When true, the INCOME workstream (actIncome
	// — batch-contract, the frigate sole-earner loop, staged hauler buys) does NOT run, DURABLY across
	// restarts, so a boot-standing bootstrap never re-establishes the contract earner on a graduated fleet.
	// False (the default / a fresh era / a read miss) ⇒ contracts run as today — byte-identical, fail-OPEN.
	// It gates ONLY the contract-income workstream; DATA (probes/scouting), GATE (construction), and trade
	// are untouched.
	ContractGraduated bool

	// --- GATE-phase signals (Slice 3). Zero values are the pre-GATE default (no gate site known, no
	// construction pipeline, the executor down, no gate workers), so an INCOME-phase observation that
	// leaves them unset reads as "GATE not started" and the earlier phases' guards are unaffected. ---

	// GateSite is the home-system jump-gate construction site waypoint (an under-construction JUMP_GATE).
	// "" when it could not be resolved yet (no waypoint data) or the system has no gate to build — a
	// blocker, not an error (a later tick with data retries).
	GateSite string
	// ConstructionStarted reports whether a construction pipeline ALREADY exists for GateSite. It is
	// BOTH the idempotency guard for `construction start` (never create a second pipeline) AND the
	// STICKY-GATE signal: once a pipeline exists the arc stays in GATE even as contract income falls with
	// repurposed haulers, so derivePhase never regresses GATE→INCOME (which would re-buy haulers and
	// thrash). A restart mid-GATE re-observes this true → resumes in GATE.
	ConstructionStarted bool
	// ConstructionComplete reports whether the gate construction site is 100% delivered — the GATE→COMPLETE
	// exit. Terminal and monotone (a built gate stays built), so a restart post-completion re-derives COMPLETE.
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
	// AutosizerRunning reports whether the standing fleet-autosizer is already running — the COMPLETE
	// launch-once hand-off guard (a restart post-COMPLETE re-observes it running ⇒ no re-launch, no exit loop).
	AutosizerRunning bool

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

// MarketSnapshot is one scouted marketplace's tradable goods — the unit the contract-hub selector
// ranks. It carries only what hub selection needs: the waypoint (the hauler's placement target), its
// system (intra-system clustering context), and the goods a hauler can SOURCE here with their prices.
type MarketSnapshot struct {
	Waypoint string
	System   string
	Goods    []MarketGood
}

// MarketGood is one good a market can SELL to a hauler (a sourceable good), with the price the hauler
// pays. PurchasePrice is the sourcing cost the hub selector minimizes; a good the market does not sell
// (import-only) is simply omitted, so every MarketGood present is sourceable.
type MarketGood struct {
	Symbol        string
	PurchasePrice int64
}

// CoverageFraction is MarketsCovered / MarketsTotal, and 0 when nothing is known yet (total 0) so
// a cold agent reads as uncovered and stays in DATA rather than dividing by zero.
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

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
// Slice 1 (this file's scope) is the whole framework + the DATA phase only. INCOME and GATE are
// later slices; a phase derived beyond DATA is a Slice-1 terminal ("not yet implemented, holding
// at DATA-complete"), not an error.
package commands

// Phase is the bootstrap arc phase. It is ALWAYS derived from the current observation (market
// coverage, fleet, ...) and NEVER read back from storage — a persisted enum can desync from the
// live world, which is the whole failure mode a reconciler exists to avoid (spec §Architecture).
type Phase string

const (
	// PhaseData is the cold-start data phase: buy probes to target and scout every market so
	// contract/hub selection has data to work from. The one LIVE phase in Slice 1.
	PhaseData Phase = "DATA"
	// PhaseIncome is the contract-income ramp (Slice 2 — not implemented in Slice 1).
	PhaseIncome Phase = "INCOME"
	// PhaseGate is jump-gate construction (Slice 3 — not implemented in Slice 1).
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
	// Readable reports whether the observer gathered all its inputs. false ⇒ fail-closed (no action
	// this tick), with Reason naming what could not be read.
	Readable bool
	// Reason is a human note for the decision/heartbeat log (why unreadable, or context).
	Reason string
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

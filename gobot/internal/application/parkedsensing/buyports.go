package parkedsensing

import (
	"context"
	"errors"
	"time"
)

// buyqueue.go is the spend half of the parked-probe sensing model: it turns the
// placements the screen WANTED into probes we actually own. The screen decides
// where a hull should stand; this decides whether we can afford to put one
// there, and buys it.
//
// Every money read here fails CLOSED (RULINGS #4). An unreadable treasury, an
// unknowable cargo outflow, or an uncountable probe fleet all mean the same
// thing — the guard cannot be verified — and therefore all mean "buy nothing
// this tick". None of them is ever read as a permissive zero.

// ErrSlotClaimed reports that a slot transition LOST A RACE — another writer
// moved the slot out of the expected state first. It is the application-layer
// name for the ledger's optimistic-transition conflict, declared here so the
// queue can tell "somebody else owns this placement" (skip it quietly, a normal
// concurrent-tick outcome) apart from "the ledger is not accepting writes" (an
// outage, which must stop the drain rather than be retried into). Conflating
// the two would let a database failure look like routine contention and scroll
// past unnoticed.
var ErrSlotClaimed = errors.New("sensing slot was claimed by another writer")

// SensingParkedFleetTag is the dedicated_fleet tag carried by every probe this
// engine owns. It is written the instant a hull is bought, because a
// bought-but-untagged probe is invisible to every ownership sweep that selects
// on the tag — it would look like an idle undedicated hull and be poached by
// another coordinator, or counted by none.
const SensingParkedFleetTag = "sensing_parked"

// SensingCoverageOperationType is the ledger operation_type every probe THIS
// queue buys is booked under.
//
// It is deliberately NOT "fleet expansion". Two engines buy probes — the frontier
// expansion engine, and this queue buying coverage for markets already judged
// worth watching — and while both wrote the same label the ledger could not
// answer "which engine spent this". That mattered the moment the operator
// switched expansion off: the switch worked, expansion stopped asking, this queue
// kept buying, and the ledger reported "fleet expansion" spending money against a
// switch that read off. 25 hulls and 907,545 credits went out looking exactly
// like the engine that had been stopped (sp-com1h).
//
// Spaced and lower-case to match the column's existing human-readable values
// ("fleet expansion", "fleet rebalancing"), because operation_type is read
// straight off a Grafana legend and typed by hand into ad-hoc SQL.
const SensingCoverageOperationType = "sensing coverage"

// maxDrainAttempts bounds how many purchase ATTEMPTS one drain tick may make —
// not how many succeed. Every trip through the buy path costs one, whether it
// ends in a hull, an unpriceable yard, or a counter that refused.
//
// Counting attempts rather than successes is the whole point. Each attempt
// opens with a LIVE, uncached shipyard price read, so a budget that only
// decremented on success would leave every FAILURE path unbounded: fifty
// placements whose yards cannot be priced would fire fifty live reads and fifty
// purchase attempts every tick, forever — and would do it hardest exactly when
// the API is already degraded, which is what makes yards unpriceable in the
// first place. A failure must cost the same as a success, or failure becomes
// the cheap path.
//
// The trade accepted here is that a run of failing placements at the head of the
// queue can delay the ones behind them, since the queue is depth-ordered and
// re-derived each tick. That is bounded by the failures being transient — the
// one systematic repeat-failure source, a hull we are structurally unable to
// claim, is excluded at selection time instead (see ParkedShipReader).
//
// A plain constant, deliberately not a knob: it is a rate limit on API bursts,
// not an economic lever.
const maxDrainAttempts = 6

// cargoSpendLookback is the trailing window the cargo-runway term of the floor
// measures. One hour, matching ProbeBuyFloor's per-hour units.
const cargoSpendLookback = time.Hour

// TreasuryReader live-reads the player's spendable credits. Structurally
// satisfied by the same api-backed reader every other money guard uses, whose
// 15s cache is invalidated on every credit-decreasing call — so a read taken
// straight after a purchase cannot come back stale-high.
type TreasuryReader interface {
	LiveCredits(ctx context.Context, playerID int) (int64, error)
}

// CargoSpendReader measures the trading fleet's recent cargo outflow, which is
// what makes the buy floor dynamic.
type CargoSpendReader interface {
	// AbsCargoBuySpendSince sums the ABSOLUTE value of cargo-purchase spend
	// booked since `since`. Expenses are stored negative in the ledger; the
	// adapter negates them, so a busier fleet yields a LARGER number and
	// therefore a HIGHER floor.
	AbsCargoBuySpendSince(ctx context.Context, playerID int, since time.Time) (int64, error)
}

// BoughtProbe is one completed purchase.
type BoughtProbe struct {
	ShipSymbol string
	Price      int64
	// CreditsAfter is the treasury balance the shipyard reported AFTER the
	// purchase settled. It is the authoritative post-spend balance and saves an
	// API round-trip before the next pop's floor check. Zero means the purchase
	// path could not report one, in which case the caller falls back to
	// arithmetic (see DrainBuyQueue).
	CreditsAfter int64
}

// ProbePurchaser prices and buys one probe through the existing purchase-ship
// machinery. Both halves fail the buy closed on error.
type ProbePurchaser interface {
	// Quote returns the live probe price at a yard, for the floor check that
	// runs BEFORE any money moves.
	Quote(ctx context.Context, playerID int, yardWaypoint string) (int64, error)
	// Buy purchases exactly one probe at yardWaypoint. purchasingShip is a hull
	// of ours already standing at that yard — the purchase machinery navigates
	// and docks it itself, so it must genuinely be able to reach the counter.
	//
	// claimOwnerContainerID is the DRIVING coordinator's container id, and it
	// must be a REAL one. The implementation holds an exclusive single-writer
	// claim on purchasingShip for the length of the buy, and that claim writes
	// ships.container_id — a column carrying a foreign key to containers(id).
	// A descriptive label here is not merely imprecise, it is unwritable: the
	// database rejects the row, the claim fails closed, and the purchase never
	// happens. This is why the value travels per-call rather than being bound
	// into the adapter — a coordinator relaunch mints a new container id, and a
	// value captured at construction time would go stale into the same failure.
	Buy(ctx context.Context, playerID int, purchasingShip, yardWaypoint, claimOwnerContainerID string) (BoughtProbe, error)
}

// ProbeYardCatalog lists the shipyards in a system that sell probes, cheapest
// first. Narrowed to the one method the drain needs; the screen's fuller
// WaypointCatalog satisfies it, so one adapter serves both.
type ProbeYardCatalog interface {
	ListProbeYards(ctx context.Context, system string) ([]string, error)
}

// ProbeListingMemo reports what a PREVIOUS shipyard read persisted about a yard's
// stock, so the drain can stop paying to re-learn a standing fact.
//
// OPTIONAL: a nil memo quotes everything, so an unwired deployment is unchanged.
type ProbeListingMemo interface {
	// LastListingScan reports whether the yard's STORED listings include a priced
	// probe, and when that reading was taken.
	//
	// known=false means the yard has never been read, which the caller must treat
	// as "ask once" and never as "no probe" — an absent reading is how a yard
	// enters the memo at all, so reading it as a negative would freeze the fleet's
	// knowledge permanently.
	LastListingScan(ctx context.Context, playerID int, waypoint string) (sellsProbe bool, scannedAt time.Time, known bool, err error)
}

// ParkedShipReader reads ship positions from the DATABASE, never the API.
//
// Both methods are scoped to ONE waypoint or ONE ship by construction. The
// interface deliberately exposes no fleet-listing method: the sensing engine's
// per-tick cost must scale with the placements it is working, not with how many
// hulls the player owns.
type ParkedShipReader interface {
	// DockedProbeAt returns a probe docked at waypoint that this engine may
	// actually DRIVE, if any.
	//
	// "May drive" is part of the contract, not an implementation detail:
	// implementations MUST exclude hulls dedicated to another fleet. Fleet
	// dedication is enforced when a hull is claimed, and that rejection is
	// PERMANENT rather than transient — so a foreign-dedicated probe returned
	// here would be selected as a purchasing hull, rejected at the claim, and
	// selected again on the next tick and every tick after, burning a live
	// price read each time and never filling the placement. Excluding it at
	// selection is what keeps the drain's failure paths transient.
	DockedProbeAt(ctx context.Context, playerID int, waypoint string) (string, bool, error)
	// ShipAt returns one hull's recorded position.
	ShipAt(ctx context.Context, playerID int, shipSymbol string) (ShipPos, error)
}

// FleetTagger writes the dedicated-fleet tag — the single write path for fleet
// dedication, and idempotent, so re-asserting a tag already persisted is free.
type FleetTagger interface {
	AssignFleet(ctx context.Context, playerID int, shipSymbol, fleet string) error
}

// BuyLedger is the durable placement ledger, from the buy queue's side.
//
// It is a separate, narrower interface from the screen's SlotLedger on purpose:
// the two halves of the model touch disjoint parts of the same table (the
// screen writes placements, the queue advances them), and neither should be
// able to reach the other's verbs.
type BuyLedger interface {
	// SlotsByState returns the player's slots in any of the given states.
	SlotsByState(ctx context.Context, playerID int, states ...string) ([]QueuedSlot, error)
	// SlotsBySystem returns every slot in one system, in any state.
	SlotsBySystem(ctx context.Context, playerID int, system string) ([]QueuedSlot, error)
	// SystemsByVerdict returns the systems carrying a screening verdict.
	SystemsByVerdict(ctx context.Context, playerID int, verdict string) ([]ScreenedSystem, error)
	// CountOwnedProbes reports how many probe hulls the ledger accounts for.
	// This is the probe-cap read that gates spend.
	CountOwnedProbes(ctx context.Context, playerID int) (int64, error)
	// TransitionSlot advances one slot's state, guarded on it still being in
	// From so two writers racing the same slot cannot both proceed.
	TransitionSlot(ctx context.Context, playerID int, t SlotTransition, set SlotFields) error
}

// BuyPorts is everything DrainBuyQueue needs from the outside world.
type BuyPorts struct {
	Treasury   TreasuryReader
	CargoSpend CargoSpendReader
	Purchaser  ProbePurchaser
	Ledger     BuyLedger
	Yards      ProbeYardCatalog
	Ships      ParkedShipReader
	// ListingMemo answers what a previous shipyard read learned about a yard's
	// stock, so a yard already known to sell no probe costs no live quote.
	// OPTIONAL: nil quotes everything, exactly as the drain did before it existed.
	ListingMemo ProbeListingMemo
	Fleet       FleetTagger
	// HeavyReserve reports the credits held back for the NEXT heavy purchase. OPTIONAL:
	// a nil reader means no reserve and byte-identical behaviour, so the sensing engine
	// runs unchanged before the heavy feature is wired.
	//
	// A read ERROR fails CLOSED here. That is DEFENCE IN DEPTH, not this queue's claim about
	// what an unreadable reserve means: the shipped reader answers ZERO (loudly) when it
	// cannot see its inputs, matching the fleet autosizer's direction on the same blind
	// signal, so nothing in production reaches the error branch today.
	//
	// It is kept DELIBERATELY. HeavyReserveReader is an exported interface carrying an error
	// in its contract, and this field is a swappable seam — a nil reader is already a
	// supported wiring — so the drain cannot assume which implementation it holds. Dropping
	// the branch would make the drain treat an erroring reader's zero as authoritative, which
	// is the silent-zero outcome the reserve's own rules exist to prevent. Both halves are
	// pinned: TestDrain_ErroringReserveReaderStillFailsClosed for this branch,
	// TestDrain_BlindReserveReadsZeroAndBuyingProceeds for what the shipped reader actually does.
	HeavyReserve HeavyReserveReader

	// Gates and MannedHulls serve the foothold path ONLY (foothold.go): the gate
	// topology names which systems a surplus hull could be flown from, and the
	// post reader names the hulls that are manning a scout post and are
	// therefore not this engine's to take.
	//
	// FAIL-CLOSED WHEN EITHER IS NIL. The path needs both to be safe — reach
	// without the manned set would strip the scouting fleet, and the manned set
	// without reach has nowhere to draw from — so an unwired port yields no
	// foothold rather than a partly-guarded one. Both are wired in the sensing
	// coordinator; nil is a test wiring, not a deployment one.
	Gates       GateNeighbours
	MannedHulls MannedHullReader

	// YardDemand names the shipyards the fleet cannot price, so the drain's
	// ordering can put a placement standing on one ahead of an ordinary market in
	// the same system (yardqueue.go). It is the SAME budget instance the presence
	// pass pulls from, which is the point: that pass moves a hull we already own
	// to a dark yard, this one decides which placement the fleet BUYS a hull for
	// first, and a fleet where the two ranked yards differently would spend both
	// resources on different counters.
	//
	// OPTIONAL and FAIL-OPEN. A nil reader leaves the queue's order untouched by
	// this term — the same direction reachableFills takes, and the opposite of the
	// money guards above, because this term can only reorder placements the queue
	// had already decided it wanted. It is wired in the sensing coordinator; nil is
	// a test wiring, not a deployment one.
	YardDemand YardDemandReader

	// ClaimOwnerContainerID is the driving coordinator's container id, handed to
	// Purchaser.Buy as the owner of the purchasing hull's claim. It is IDENTITY,
	// not a port: the drain never reads it, it only carries it, because the claim
	// is the adapter's to make and a real container id is the only value that can
	// legally own one (see ProbePurchaser.Buy).
	//
	// It rides here rather than as a DrainBuyQueue parameter so the existing drain
	// call sites stay untouched. The adapter fails the buy CLOSED when it arrives
	// empty, so an unset field can never reach the database as a claim.
	ClaimOwnerContainerID string
}

// HeavyReserveReader reports the derived hold-back for the next heavy purchase. The value is
// computed by common.HeavyReserve — the ONE definition, shared with the fleet autosizer. This
// port carries the answer; it must never re-derive it.
type HeavyReserveReader interface {
	Reserve(ctx context.Context, playerID int) (int64, error)
}

// BuyKnobs are the operator-set economics of the queue.
type BuyKnobs struct {
	// SpendEnabled is the operator's expansion switch, and off means NO PROBE IS
	// BOUGHT — the whole point of the field.
	//
	// IT IS THE SAME SWITCH AS ExpandKnobs.SpendEnabled (`expansion_enabled`), fed
	// from the same resolved config on the same tick. It has to reach here as well
	// as there, because the two engines spend through different doors and the
	// switch only ever covered one of them: expansion's half stops the seed wants
	// and the off-gate demand it RAISES for other engines, while this queue is the
	// engine that actually pays. Measured: with the switch off, expansion
	// correctly reported "spending paused: no seed purchase, no explorer demand"
	// while this drain bought six probes a cycle for hours — 25 hulls and 907,545
	// credits — because nothing here consulted it (sp-com1h).
	//
	// OFF STOPS PURCHASES, NOT THE DRAIN, and the distinction is what makes "off"
	// safe to leave on. A paused tick still re-tasks an idle spare into a
	// placement (reuseSpareHull) and still flies a surplus hull across a gate to
	// establish a foothold — both cost zero credits and zero API calls, and both
	// are how coverage keeps growing on hulls we have already paid for. What it
	// does not do is read a yard's live price, claim a placement for purchase, or
	// pay a counter.
	//
	// It is NOT the money guard. ProbeBuyFloor, the probe cap and the heavy
	// reserve are, and they are unchanged either way (RULINGS #4) — this only ever
	// narrows what may be bought, never widens it. The zero value is therefore the
	// closed one: a call site that forgets to set it buys nothing, which is the
	// only direction a money guard may fail.
	SpendEnabled bool
	// ProbeCap is the hard ceiling on probe hulls this engine may own.
	ProbeCap int
	// CapexReserve is the credits held back for ship capex the operation has
	// already committed to elsewhere.
	CapexReserve int64
	// KMilli is how many MILLI-hours of the trading fleet's cargo runway the
	// floor holds back on top of the immutable reserve: 2000 = 2h, 400 = 0.4h.
	// Milli rather than a float because sub-hour runway is the operating range
	// and this coordinator's tunable registry is integer end to end — see
	// domain/parkedsensing.ProbeBuyFloor for why a float would put NaN inside a
	// money guard.
	KMilli int
}

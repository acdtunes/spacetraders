package parkedsensing

import (
	"context"
	"errors"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/fleetgrowth"
)

// buyports.go declares the ports and types of the spend half of the parked-probe
// sensing model — the machinery that turns the placements the screen WANTED into
// probes we actually own. Every money read here fails CLOSED (RULINGS #4): an
// unreadable treasury, an unknowable cargo outflow and an uncountable probe fleet
// all mean the guard cannot be verified, and therefore all mean "buy nothing this
// tick". None of them is ever read as a permissive zero.

// ErrSlotClaimed reports that a slot transition LOST A RACE — another writer
// moved the slot out of the expected state first. It exists so the queue can tell
// "somebody else owns this placement" (skip it quietly, a normal concurrent-tick
// outcome) apart from "the ledger is not accepting writes" (an outage, which must
// stop the drain rather than be retried into); conflating the two would let a
// database failure look like routine contention and scroll past unnoticed.
var ErrSlotClaimed = errors.New("sensing slot was claimed by another writer")

// SensingParkedFleetTag is the dedicated_fleet tag carried by every probe this
// engine owns. It is written the instant a hull is bought, because a
// bought-but-untagged probe is invisible to every ownership sweep that selects on
// the tag — it would look like an idle undedicated hull and be poached by another
// coordinator, or counted by none.
const SensingParkedFleetTag = "sensing_parked"

// SensingCoverageOperationType is the ledger operation_type every probe THIS queue
// buys is booked under. Deliberately NOT "fleet expansion": two engines buy probes,
// and a shared label leaves the ledger unable to answer "which engine spent this",
// so an operator who stops one of them still sees its label spending. Spaced and
// lower-case to match the column's other human-readable values, because
// operation_type is read off a Grafana legend and typed by hand into ad-hoc SQL.
const SensingCoverageOperationType = "sensing coverage"

// maxDrainAttempts bounds how many purchase ATTEMPTS one drain tick may make — not
// how many succeed. Every trip through the buy path costs one, whether it ends in a
// hull, an unpriceable yard or a counter that refused, because each attempt opens
// with a LIVE, uncached shipyard price read: a budget decremented only on success
// leaves every FAILURE path unbounded, and unbounded hardest when the API is already
// degraded. The cost is that a run of failing placements at the head of the
// depth-ordered queue delays the ones behind them, bounded by those failures being
// transient — the one systematic repeat-failure source, a hull we cannot claim, is
// excluded at selection time instead (see ParkedShipReader). A plain constant,
// deliberately not a knob: a rate limit on API bursts, not an economic lever.
const maxDrainAttempts = 6

// cargoSpendLookback names the shared window for call-site readability. Two money guards measuring
// one fleet's outflow over two windows would reserve against two measurements of one quantity.
const cargoSpendLookback = fleetgrowth.TradeCycleWindow

// TreasuryReader live-reads the player's spendable credits. Structurally satisfied
// by the same api-backed reader every other money guard uses, whose cache is
// invalidated on every credit-decreasing call — so a read taken straight after a
// purchase cannot come back stale-high.
type TreasuryReader interface {
	LiveCredits(ctx context.Context, playerID int) (int64, error)
}

// CargoSpendReader measures the trading fleet's recent cargo outflow, which is
// what makes the buy floor dynamic.
type CargoSpendReader interface {
	// AbsCargoBuySpendSince sums the ABSOLUTE value of cargo-purchase spend booked
	// since `since`. Expenses are stored negative in the ledger and the adapter
	// negates them, so a busier fleet yields a LARGER number and a HIGHER floor.
	AbsCargoBuySpendSince(ctx context.Context, playerID int, since time.Time) (int64, error)
}

// BoughtProbe is one completed purchase.
type BoughtProbe struct {
	ShipSymbol string
	Price      int64
	// CreditsAfter is the treasury balance the shipyard reported AFTER the purchase
	// settled — the authoritative post-spend balance, which saves an API round-trip
	// before the next pop's floor check. Zero means the purchase path could not
	// report one, in which case the caller falls back to arithmetic.
	CreditsAfter int64
}

// ProbePurchaser prices and buys one probe through the existing purchase-ship
// machinery. Both halves fail the buy closed on error.
type ProbePurchaser interface {
	// Quote returns the live probe price at a yard, for the floor check that runs
	// BEFORE any money moves.
	Quote(ctx context.Context, playerID int, yardWaypoint string) (int64, error)
	// Buy purchases exactly one probe at yardWaypoint. purchasingShip is a hull of
	// ours already standing at that yard — the purchase machinery navigates and docks
	// it itself, so it must genuinely be able to reach the counter.
	//
	// claimOwnerContainerID must be a REAL container id: the buy holds an exclusive
	// single-writer claim on purchasingShip, and that claim writes ships.container_id,
	// a column with a foreign key to containers(id). A descriptive label is
	// unwritable — the row is rejected and the purchase never happens. It travels
	// per-call because a coordinator relaunch mints a new container id.
	Buy(ctx context.Context, playerID int, purchasingShip, yardWaypoint, claimOwnerContainerID string) (BoughtProbe, error)
}

// ProbeYardCatalog lists the shipyards in a system that sell probes, cheapest
// first. Narrowed to the one method the drain needs; the screen's fuller
// WaypointCatalog satisfies it, so one adapter serves both.
type ProbeYardCatalog interface {
	ListProbeYards(ctx context.Context, system string) ([]string, error)
}

// ProbeListingMemo reports what a PREVIOUS shipyard read persisted about a yard's
// stock, so the drain can stop paying to re-learn a standing fact. OPTIONAL: a nil
// memo quotes everything.
type ProbeListingMemo interface {
	// LastListingScan reports whether the yard's STORED listings include a priced
	// probe, and when that reading was taken. known=false means the yard has never
	// been read, which the caller must treat as "ask once" and never as "no probe":
	// an absent reading is how a yard enters the memo at all, so reading it as a
	// negative would freeze the fleet's knowledge permanently.
	LastListingScan(ctx context.Context, playerID int, waypoint string) (sellsProbe bool, scannedAt time.Time, known bool, err error)
}

// ParkedShipReader reads ship positions from the DATABASE, never the API. Every
// read is scoped to ONE waypoint, ONE ship, or a BOUNDED page by construction, and
// the interface deliberately exposes no unbounded fleet-listing method: the sensing
// engine's per-tick cost must scale with the placements it is working, not with the
// size of the fleet.
type ParkedShipReader interface {
	// DockedProbeAt returns a probe docked at waypoint that this engine may actually
	// DRIVE, if any. "May drive" is part of the contract: implementations MUST
	// exclude hulls dedicated to another fleet, because that claim rejection is
	// PERMANENT rather than transient — a foreign-dedicated probe returned here would
	// be selected as a purchasing hull, rejected at the claim, and selected again
	// every tick after, burning a live price read and never filling the placement.
	DockedProbeAt(ctx context.Context, playerID int, waypoint string) (string, bool, error)
	// DockedBuyerAt returns ANY hull of ours docked at waypoint that this engine may
	// claim to buy THROUGH — the probe DockedProbeAt already finds, or a NON-PROBE
	// hull standing at the counter on loan (see counterstaff.go).
	//
	// A SECOND READ RATHER THAN A WIDENING OF DockedProbeAt, and the split is
	// load-bearing. DockedProbeAt's answer is also read as "a probe of ours is on
	// station here"; this one answers only "somebody of ours can sign for a
	// purchase". Callers ask DockedProbeAt FIRST and fall through to this, so the
	// pair is a strict superset of the old behaviour however this one is narrowed.
	//
	// THE SAME PERMANENT-REJECTION CONTRACT APPLIES, and it is stricter here because
	// a non-probe hull has an owner. Implementations MUST exclude every hull the
	// claim path would refuse forever: one dedicated to another fleet, one a
	// container already holds, and one the captain has reserved. A hull that fails
	// its claim on every tick is a standing API drain — the buy queue would select
	// it, pay for a live shipyard price read, fail, and select it again.
	//
	// The mechanic behind it: SpaceTraders sells a hull only where a hull of ours is
	// already docked, and it does not care WHICH. Presence is the requirement, not a
	// probe.
	DockedBuyerAt(ctx context.Context, playerID int, waypoint string) (string, bool, error)
	// LendableHulls returns at most `limit` NON-PROBE hulls of ours that this engine
	// could borrow for one errand, cheapest sacrifice first.
	//
	// BOUNDED, WHICH IS WHY IT IS ADMISSIBLE HERE. The bound is the caller's and the
	// pass that uses it dispatches at most one hull per tick, so the cost is a single
	// indexed page read rather than a fleet walk — the property the note above this
	// interface protects.
	//
	// IT ADMITS ONLY WHAT THE CLAIM PATH WOULD ACCEPT, the same list DockedBuyerAt
	// excludes: undedicated (or already ours), unclaimed by any container, and
	// unreserved by the captain. Borrowing is a reachability trick, not a way to
	// take a hull off another coordinator's work.
	//
	// IN-TRANSIT HULLS ARE INCLUDED AND FLAGGED. They are not borrowable, but a hull
	// already FLYING to a counter is exactly what stops the next tick sending a
	// second one there — dropping them would make the pass double-dispatch.
	LendableHulls(ctx context.Context, playerID int, limit int) ([]LendableHull, error)
	// ShipAt returns one hull's recorded position.
	ShipAt(ctx context.Context, playerID int, shipSymbol string) (ShipPos, error)
}

// LendableHull is one hull the fleet could lend to stand at a probe counter.
//
// It carries no role, no cargo and no capacity, and that narrowness is the point:
// the borrower's only question is "can this hull stand somewhere it is not standing
// now", and a richer type would invite this engine into fleet decisions that belong
// to the coordinators that own these hulls.
type LendableHull struct {
	ShipSymbol string
	// Waypoint is where the hull STANDS — or, when InTransit, where it is flying TO,
	// which is what the ships row records for a hull under way.
	Waypoint string
	System   string
	// InTransit reports that the hull is under way and therefore NOT borrowable. It
	// is returned rather than filtered out so a counter with a hull already inbound
	// can be recognised and left alone.
	InTransit bool
}

// FleetTagger writes the dedicated-fleet tag — the single write path for fleet
// dedication, and idempotent, so re-asserting a tag already persisted is free.
type FleetTagger interface {
	AssignFleet(ctx context.Context, playerID int, shipSymbol, fleet string) error
}

// BuyLedger is the durable placement ledger, from the buy queue's side. It is a
// separate, narrower interface from the screen's SlotLedger on purpose: the two
// halves of the model touch disjoint parts of the same table (the screen writes
// placements, the queue advances them), and neither should be able to reach the
// other's verbs.
type BuyLedger interface {
	// SlotsByState returns the player's slots in any of the given states.
	SlotsByState(ctx context.Context, playerID int, states ...string) ([]QueuedSlot, error)
	// SlotsBySystem returns every slot in one system, in any state.
	SlotsBySystem(ctx context.Context, playerID int, system string) ([]QueuedSlot, error)
	// SystemsByVerdict returns the systems carrying a screening verdict.
	SystemsByVerdict(ctx context.Context, playerID int, verdict string) ([]ScreenedSystem, error)
	// CountOwnedProbes reports how many probe hulls the ledger accounts for. This is
	// the probe-cap read that gates spend.
	CountOwnedProbes(ctx context.Context, playerID int) (int64, error)
	// TransitionSlot advances one slot's state, guarded on it still being in From so
	// two writers racing the same slot cannot both proceed.
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
	// OPTIONAL: nil quotes everything.
	ListingMemo ProbeListingMemo
	Fleet       FleetTagger
	// Wave answers which regime this tick is in, and what the fleet is saving toward.
	// OPTIONAL: a nil reader means no heavy buyer, which is the PROBE wave — an
	// unwired seam must never pause probe buying. A read ERROR fails CLOSED: this is
	// a swappable seam, so an erroring implementation's zero is never authoritative.
	Wave WaveReader

	// Gates and MannedHulls serve the foothold path ONLY (foothold.go): the gate
	// topology names which systems a surplus hull could be flown from, and the post
	// reader names the hulls manning a scout post, which are not this engine's to take.
	// FAIL-CLOSED WHEN EITHER IS NIL, because the path needs both to be safe — reach
	// without the manned set would strip the scouting fleet, and the manned set
	// without reach has nowhere to draw from.
	Gates       GateNeighbours
	MannedHulls MannedHullReader

	// YardDemand names the shipyards the fleet cannot price, so the drain's ordering
	// can put a placement standing on one ahead of an ordinary market in the same
	// system (yardqueue.go). It is the SAME budget instance the presence pass pulls
	// from, because a fleet whose two passes ranked yards differently would spend
	// both resources on different counters. OPTIONAL and FAIL-OPEN — the opposite
	// direction from the money guards above, because this term can only reorder
	// placements the queue already wanted.
	YardDemand YardDemandReader

	// ClaimOwnerContainerID is the driving coordinator's container id, handed to
	// Purchaser.Buy as the owner of the purchasing hull's claim. It is IDENTITY, not a
	// port: the drain only carries it, because the claim is the adapter's to make (see
	// ProbePurchaser.Buy). The adapter fails the buy CLOSED when it arrives empty, so
	// an unset field can never reach the database as a claim.
	ClaimOwnerContainerID string
}

// WaveReader answers the regime, what the fleet is saving toward, and the spend hold — a FOURTH
// answer, never a third Wave value, which would pass every `wave != WaveHeavy` gate.
//
// IT RETURNS THE WHOLE ANSWER, not the inputs to it. The drain must not assemble a second
// WaveInputs and call common.DeriveWave itself: two assemblies of one predicate is the split-brain
// the one-definition rule exists to prevent, and this port is the seam where the growth
// coordinator's facts and the drain's meet. The target is carried for the heartbeat, not for a
// decision — since the wave owns the hold-back, no path may add it into a spend floor.
type WaveReader interface {
	Wave(ctx context.Context, playerID int) (common.Wave, common.WaveProbeReason, common.HeavyReserveTarget, common.ProbeSpendHold, error)
}

// BuyKnobs are the operator-set economics of the queue.
type BuyKnobs struct {
	// SpendEnabled is the operator's expansion switch, and off means NO PROBE IS BOUGHT.
	// IT IS ONE HALF OF THE SAME KNOB AS ExpandKnobs.SeedsEnabled (`expansion_enabled`),
	// resolved on the same tick: the halves are separately settable because a coverage
	// probe and a charting errand differ in what they earn.
	//
	// OFF STOPS PURCHASES, NOT THE DRAIN, which is what makes "off" safe to leave on:
	// a paused tick still re-tasks an idle spare into a placement (reuseSpareHull) and
	// still flies a surplus hull across a gate to establish a foothold, both at zero
	// credits and zero API calls. What it does not do is read a yard's live price,
	// claim a placement for purchase, or pay a counter.
	//
	// IT IS NOT THE WAVE: two independent gates side by side, this one the operator's
	// choice and the wave the fleet's regime. Folding them together would leave an
	// operator unable to tell "you switched this off" from "the fleet is saving for a
	// hull". Neither is the money guard — ProbeBuyFloor and the probe cap are,
	// unchanged either way (RULINGS #4). The zero value is the closed one.
	SpendEnabled bool
	// ProbeCap is the hard ceiling on probe hulls this engine may own.
	ProbeCap int
	// CapexReserve is the credits held back for ship capex the operation has
	// already committed to elsewhere.
	CapexReserve int64
	// KMilli is how many MILLI-hours of the trading fleet's cargo runway the floor
	// holds back on top of the immutable reserve: 2000 = 2h, 400 = 0.4h. Milli rather
	// than a float because sub-hour runway is the operating range and the tunable
	// registry is integer end to end — see domain/parkedsensing.ProbeBuyFloor for why
	// a float would put NaN inside a money guard.
	KMilli int
	// CoverageReserve holds back this many FILL attempts per tick for the best
	// placement in a system the fleet has never entered. Zero (the default)
	// leaves the saturate-first order in drainorder.go untouched.
	CoverageReserve int
}

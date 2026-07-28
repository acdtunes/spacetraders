package parkedsensing

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainSensing "github.com/andrescamacho/spacetraders-go/internal/domain/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
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
	Buy(ctx context.Context, playerID int, purchasingShip, yardWaypoint string) (BoughtProbe, error)
}

// ProbeYardCatalog lists the shipyards in a system that sell probes, cheapest
// first. Narrowed to the one method the drain needs; the screen's fuller
// WaypointCatalog satisfies it, so one adapter serves both.
type ProbeYardCatalog interface {
	ListProbeYards(ctx context.Context, system string) ([]string, error)
}

// ShipPos is where one hull is, read from the ships table.
type ShipPos struct {
	Waypoint  string
	NavStatus navigation.NavStatus
	// Found reports whether the ships table knows this hull at all. A hull we
	// cannot locate is never acted on.
	Found bool
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

// QueuedSlot is one placement row as the buy queue and the placement machine
// see it: the ledger's own columns, with the nullable ones flattened to empty
// strings (nothing here distinguishes NULL from empty, and treating them alike
// is what keeps every "is a hull recorded?" check a simple != "").
type QueuedSlot struct {
	Waypoint     string
	System       string
	Kind         string
	State        string
	AssignedShip string
	PurchaseYard string
	DepthCredits int64
}

// ScreenedSystem is one screened system's identity and size.
type ScreenedSystem struct {
	System       string
	DepthCredits int64
}

// SlotFields carries the field writes a transition applies ATOMICALLY with the
// state flip. A nil pointer leaves the stored value alone; a pointer to the
// empty string CLEARS the column. Clearing matters: releasing a spare hull must
// actually drop its ship reference, or the ledger keeps counting a hull that
// now belongs to another slot.
type SlotFields struct {
	AssignedShip *string
	PurchaseYard *string
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
	// fromState so two writers racing the same slot cannot both proceed.
	TransitionSlot(ctx context.Context, playerID int, waypoint, fromState, toState string, set SlotFields) error
}

// BuyPorts is everything DrainBuyQueue needs from the outside world.
type BuyPorts struct {
	Treasury   TreasuryReader
	CargoSpend CargoSpendReader
	Purchaser  ProbePurchaser
	Ledger     BuyLedger
	Yards      ProbeYardCatalog
	Ships      ParkedShipReader
	Fleet      FleetTagger
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
}

// HeavyReserveReader reports the derived hold-back for the next heavy purchase. The value is
// computed by common.HeavyReserve — the ONE definition, shared with the fleet autosizer. This
// port carries the answer; it must never re-derive it.
type HeavyReserveReader interface {
	Reserve(ctx context.Context, playerID int) (int64, error)
}

// BuyKnobs are the operator-set economics of the queue.
type BuyKnobs struct {
	// ProbeCap is the hard ceiling on probe hulls this engine may own.
	ProbeCap int
	// CapexReserve is the credits held back for ship capex the operation has
	// already committed to elsewhere.
	CapexReserve int64
	// K is how many hours of the trading fleet's cargo runway the floor holds
	// back on top of the immutable reserve.
	K int
}

// BuyReport is one drain tick's outcome, for the heartbeat.
type BuyReport struct {
	// Bought counts probes purchased; Reused counts placements filled by
	// re-tasking a spare hull instead, which costs nothing.
	Bought, Reused int
	// Queued counts placements claimed for purchase this tick.
	Queued int
	// SkippedNoYard counts placements passed over because no yard in their
	// system had a hull of ours standing at it to buy through. Expected and
	// benign — the placement waits for expansion to establish presence.
	SkippedNoYard int
	// Attempts counts every trip through the buy path, successful or not,
	// against maxDrainAttempts.
	Attempts int
	// HeavyReserve is the credits held back for the next heavy this tick. It is on
	// the report for ONE reason (spec risk 3): while a heavy accumulates, probe
	// buying stops, and on a thin treasury that looks identical to sensing having
	// died. A non-zero value here beside FloorHeld says "saving for a heavy",
	// which is the difference between an operator waiting and an operator paging.
	HeavyReserve int64
	// CapHeld and FloorHeld report which ceiling stopped the drain.
	CapHeld, FloorHeld bool
	// HaltedPriceDrift reports that a yard charged MORE than it had just
	// quoted, and the drain stopped for the tick because of it. The hull is
	// still bought and recorded — an overrun cannot un-buy it — but every
	// remaining quote this tick was taken against a market that has since moved,
	// so none of them can be trusted to gate a further purchase. The next tick
	// re-quotes from scratch and proceeds normally.
	HaltedPriceDrift bool
}

// drainState is the running position one tick mutates as it buys: the treasury
// left, the floor it must stay above, and how many hulls are now owned. It is
// threaded by pointer so every pop sees the effect of the pop before it.
type drainState struct {
	credits int64
	floor   int64
	owned   int64
	cap     int64
}

// DrainBuyQueue fills as many wanted placements as the treasury and the probe
// cap allow, in priority order.
//
// Order is deliberate and tested: FILLS first — placements in systems already
// judged IN_SCOPE, deepest system first — then SEEDS, the spare hulls that go
// out to frontier systems nobody has screened yet. A fill earns its keep
// immediately (it watches a market we know we want); a seed is speculative. So
// a tick that can only afford one probe spends it on the known-good placement.
//
// Both ceilings are re-checked on EVERY pop, not once at the top: each purchase
// moves the treasury and grows the fleet, so a batch that was affordable when
// the tick started may not be by its third buy.
func DrainBuyQueue(
	ctx context.Context,
	p BuyPorts,
	playerID int,
	k BuyKnobs,
	clock shared.Clock,
) (BuyReport, error) {
	var rep BuyReport
	if clock == nil {
		clock = shared.NewRealClock()
	}

	// The heavy reservation is read FIRST, ahead of every gate, so rep.HeavyReserve is populated
	// on EVERY return path — including the two that stop before a floor is ever built (nothing
	// queued, and the probe cap held). The heartbeat publishes this number beside the autosizer's
	// own per-tick gauge, and an operator correlating the two must never see them disagree merely
	// because this tick took a short path. The probe cap makes that concrete: it is a long-lived
	// steady state, so the heartbeat would otherwise read 0 for hours with a reserve genuinely
	// outstanding, and "the two halves disagree" is the one signal these diagnostics exist to keep
	// trustworthy.
	//
	// It does not break the cheapest-first ordering below: all three reads behind this port are
	// LOCAL DB queries (containers, ships, shipyard_inventory), never an API call.
	heavyReserve := int64(0)
	if p.HeavyReserve != nil {
		r, err := p.HeavyReserve.Reserve(ctx, playerID)
		if err != nil {
			return rep, fmt.Errorf("heavy reserve unreadable, buying nothing this tick: %w", err)
		}
		if r > 0 {
			heavyReserve = r
		}
	}
	rep.HeavyReserve = heavyReserve

	// Cheapest-first gate order: the ledger reads are local, the treasury and
	// price reads are network. A tick with nothing to buy — the overwhelmingly
	// common case once the map is placed — must not cost an API call.
	candidates, err := drainCandidates(ctx, p, playerID)
	if err != nil || len(candidates) == 0 {
		return rep, err
	}

	owned, err := p.Ledger.CountOwnedProbes(ctx, playerID)
	if err != nil {
		return rep, fmt.Errorf("probe cap unreadable, buying nothing this tick: %w", err)
	}
	if owned >= int64(k.ProbeCap) {
		rep.CapHeld = true
		return rep, nil
	}

	credits, err := p.Treasury.LiveCredits(ctx, playerID)
	if err != nil {
		return rep, fmt.Errorf("treasury unreadable, buying nothing this tick: %w", err)
	}
	spend, err := p.CargoSpend.AbsCargoBuySpendSince(ctx, playerID, clock.Now().Add(-cargoSpendLookback))
	if err != nil {
		// An unknowable cargo outflow is NOT a zero one. Reading it as zero
		// would collapse the runway term and hand back the cheapest floor
		// available exactly when we understand the least.
		return rep, fmt.Errorf("cargo spend unreadable, buying nothing this tick: %w", err)
	}
	// The heavy reservation (read at the top of the tick) is capex in the literal sense
	// CapexReserve documents: credits held back for ship capex committed elsewhere. Folding it
	// into that term is what makes probe buying stand down while a heavy accumulates, and resume
	// the moment it lands.
	st := drainState{
		credits: credits,
		owned:   owned,
		cap:     int64(k.ProbeCap),
		floor: domainSensing.ProbeBuyFloor(
			common.ImmutableReserveFloor,
			k.CapexReserve+heavyReserve,
			domainSensing.CargoSpendPerHour(spend),
			k.K,
		),
	}

	for _, slot := range candidates {
		if rep.Attempts >= maxDrainAttempts {
			break
		}
		if st.owned >= st.cap {
			rep.CapHeld = true
			break
		}

		// The system's slots serve both the spare-reuse scan and the
		// purchasing-hull lookup below, so they are read once per pop.
		inSystem, err := p.Ledger.SlotsBySystem(ctx, playerID, slot.System)
		if err != nil {
			return rep, fmt.Errorf("sensing slots in %q unreadable: %w", slot.System, err)
		}

		// A spare hull already standing in this system fills the placement for
		// free. Always preferred over buying — it spends nothing and consumes
		// no cap headroom (the hull merely changes which slot claims it).
		reused, err := reuseSpareHull(ctx, p, playerID, slot, inSystem)
		if err != nil {
			return rep, err
		}
		if reused {
			rep.Reused++
			rep.Attempts++
			continue
		}

		buys, err := resolvePurchaseCandidates(ctx, p, playerID, slot, inSystem)
		if err != nil {
			return rep, err
		}
		if len(buys) == 0 {
			// No yard in this system has a hull of ours we can buy through. Not
			// an error and not worth a log line per tick: the placement waits
			// until expansion puts a usable probe within reach. Never a blind
			// cross-map buy. Costs no attempt because it touched no API.
			rep.SkippedNoYard++
			continue
		}

		claimed, err := claimForPurchase(ctx, p, playerID, slot, buys[0].yard, &rep)
		if err != nil {
			return rep, err
		}
		if !claimed {
			continue
		}

		halt, err := fillSlot(ctx, p, playerID, slot, buys, &st, &rep)
		if err != nil {
			return rep, err
		}
		if halt {
			break
		}
	}
	return rep, nil
}

// fillSlot buys one hull for a claimed placement, trying each yard where we have
// a usable purchasing hull until one sells us a probe. It reports whether the
// whole drain should stop.
//
// Working down the candidate yards matters because a refusal is usually LOCAL to
// the counter — out of stock, a hull that moved between selection and purchase —
// while the placement itself is still perfectly fillable at the yard next door.
// Abandoning the slot on the first refusal would leave it claimed and unfilled
// for a whole tick, every tick, whenever its nearest yard is the unreliable one.
//
// Every yard tried costs an attempt, including the ones that fail. See
// maxDrainAttempts for why failure must not be the cheap path.
func fillSlot(
	ctx context.Context,
	p BuyPorts,
	playerID int,
	slot QueuedSlot,
	buys []purchaseCandidate,
	st *drainState,
	rep *BuyReport,
) (bool, error) {
	for _, candidate := range buys {
		if rep.Attempts >= maxDrainAttempts {
			return true, nil
		}
		rep.Attempts++

		quote, err := p.Purchaser.Quote(ctx, playerID, candidate.yard)
		if err != nil {
			continue // unpriceable counter: no floor check is possible, try the next
		}
		if st.credits-quote < st.floor {
			// At the floor. Stop rather than shop for a cheaper yard: the floor
			// exists to protect working capital, and a marginally cheaper probe
			// erodes it just the same.
			rep.FloorHeld = true
			return true, nil
		}

		probe, err := p.Purchaser.Buy(ctx, playerID, candidate.buyer, candidate.yard)
		if err != nil || probe.ShipSymbol == "" {
			continue // this counter refused; the placement is still fillable elsewhere
		}

		if err := recordPurchase(ctx, p, playerID, slot, candidate.yard, probe); err != nil {
			return true, err
		}
		rep.Bought++
		st.owned++

		// Account with what was actually CHARGED, not what was quoted. The
		// purchase command carries no price ceiling of its own, so the floor
		// this queue enforces is only as binding as the number it subtracts —
		// and a yard that moved between the quote and the counter would
		// otherwise leave the running treasury overstated for every later pop.
		//
		// The larger of the two is taken rather than the actual outright: a
		// purchase path that reported no price at all would otherwise read as a
		// free hull, which is the one direction a money guard must never fail.
		paid := quote
		if probe.Price > paid {
			paid = probe.Price
		}
		st.credits = postBuyCredits(st.credits, paid, probe)

		if probe.Price > quote {
			// The market moved against us mid-purchase. The hull is bought and
			// recorded — that cannot be undone, and refusing to record it would
			// orphan a real probe and undercount the cap. What CAN be stopped is
			// spending further on quotes taken before the move.
			rep.HaltedPriceDrift = true
			logging.LoggerFromContext(ctx).Log("WARN", "sensing probe cost more than quoted; halting the buy queue for this tick", map[string]interface{}{
				"action":      "parked_sensing_price_drift_halt",
				"ship_symbol": probe.ShipSymbol,
				"waypoint":    candidate.yard,
				"quoted":      quote,
				"paid":        probe.Price,
			})
			return true, nil
		}
		return false, nil
	}
	return false, nil
}

// postBuyCredits is the treasury the NEXT pop's floor check runs against. The
// shipyard's own post-settlement balance is authoritative when it reports one,
// but it is only ever ACCEPTED when it is the more conservative of the two
// numbers — a shipyard reporting a balance higher than (before − price) would
// otherwise relax the floor on the strength of a value we did not compute.
func postBuyCredits(before, price int64, probe BoughtProbe) int64 {
	arithmetic := before - price
	if probe.CreditsAfter > 0 && probe.CreditsAfter < arithmetic {
		return probe.CreditsAfter
	}
	return arithmetic
}

// drainCandidates returns the placements to work this tick, in priority order:
// every IN_SCOPE fill sorted by system depth descending, then the seeds.
//
// QUEUED slots are drained alongside WANTED ones. A slot reaches QUEUED when a
// previous tick claimed it and its purchase then failed; without re-reading
// QUEUED here that claim would be a one-way door and the placement would never
// be retried.
func drainCandidates(ctx context.Context, p BuyPorts, playerID int) ([]QueuedSlot, error) {
	slots, err := p.Ledger.SlotsByState(ctx, playerID, SlotStateWanted, SlotStateQueued)
	if err != nil {
		return nil, fmt.Errorf("failed to list unfilled sensing slots: %w", err)
	}
	if len(slots) == 0 {
		return nil, nil
	}

	systems, err := p.Ledger.SystemsByVerdict(ctx, playerID, VerdictInScope)
	if err != nil {
		return nil, fmt.Errorf("failed to list in-scope sensing systems: %w", err)
	}
	depth := make(map[string]int64, len(systems))
	inScope := make(map[string]bool, len(systems))
	for _, s := range systems {
		depth[s.System] = s.DepthCredits
		inScope[s.System] = true
	}

	var fills, seeds []QueuedSlot
	for _, slot := range slots {
		switch {
		case slot.Kind == SlotKindSpare:
			// A spare is a seed: it goes wherever the frontier needs eyes, and
			// its system carries no verdict yet, so it is never scope-filtered.
			seeds = append(seeds, slot)
		case inScope[slot.System]:
			fills = append(fills, slot)
		}
		// A MARKET/YARD placement in a system NOT judged IN_SCOPE is dropped:
		// either the screen has not reached a verdict yet, or it rejected the
		// system. Buying a hull for a placement we have not justified is the
		// one purchase this queue must never make.
	}

	// Stable, so the ledger's own waypoint ordering survives as the within-
	// system tiebreak — which makes the queue FIFO per system and its output
	// reproducible tick to tick.
	sort.SliceStable(fills, func(i, j int) bool {
		return depth[fills[i].System] > depth[fills[j].System]
	})
	return append(fills, seeds...), nil
}

// reuseSpareHull re-tasks a spare probe already parked in the target's system,
// filling the placement with no purchase at all. It reports whether one was
// found and moved.
//
// THE TWO-ROW ORDERING IS A MONEY GUARD, not a style choice. One hull is named
// by two rows for an instant, and which instant is chosen decides which way a
// crash miscounts:
//
//   - Claim the target FIRST (as here): a failure between the two writes leaves
//     both rows naming the hull, so CountOwnedProbes counts it twice. The cap
//     then reads the fleet as LARGER than it is and buys FEWER probes. Wrong,
//     recoverable, and safe.
//   - Release the spare first: a failure between the writes leaves the hull
//     named by NO row. The cap reads the fleet as smaller than it is and
//     authorises buying a replacement for a probe we already own. Wrong, and it
//     spends money — the exact direction RULINGS #4 forbids.
//
// So the transient over-count is chosen deliberately over the transient
// under-count, and a failed release is surfaced loudly rather than swallowed.
func reuseSpareHull(ctx context.Context, p BuyPorts, playerID int, target QueuedSlot, inSystem []QueuedSlot) (bool, error) {
	for _, spare := range inSystem {
		if spare.Kind != SlotKindSpare || spare.State != SlotStateParked || spare.AssignedShip == "" {
			continue
		}
		if spare.Waypoint == target.Waypoint {
			continue // the target IS the spare's own slot; nothing to move
		}

		hull := spare.AssignedShip
		// The hull is already ours, so the target goes straight to IN_TRANSIT:
		// there is nothing to buy, and no BOUGHT state to pass through.
		//
		// Note that this writes IN_TRANSIT for a hull that has NOT been told to
		// move, and usually is not even at the target waypoint. The placement
		// machine's dispatchClaim branch is what notices that and flies it. That
		// branch is load-bearing for this path, not an edge case: without it the
		// hull stands where it is forever while the slot reads as in-flight.
		err := p.Ledger.TransitionSlot(ctx, playerID, target.Waypoint, target.State, SlotStateInTransit,
			SlotFields{AssignedShip: &hull})
		switch {
		case errors.Is(err, ErrSlotClaimed):
			// Lost the race for this placement. Not ours any more — and the
			// spare is still untouched, which is exactly why the target is
			// claimed first.
			return false, nil
		case err != nil:
			return false, fmt.Errorf("failed to re-task spare hull %s to %s: %w", hull, target.Waypoint, err)
		}

		// The spare's own slot reverts to a want with no hull behind it: the
		// reserve is spent, and the row must stop counting a hull that now
		// belongs to the target.
		cleared := ""
		if err := p.Ledger.TransitionSlot(ctx, playerID, spare.Waypoint, SlotStateParked, SlotStateWanted,
			SlotFields{AssignedShip: &cleared}); err != nil {
			return true, fmt.Errorf(
				"spare hull %s re-tasked to %s but its slot %s was not released (hull now double-counted, cap reads high): %w",
				hull, target.Waypoint, spare.Waypoint, err)
		}
		return true, nil
	}
	return false, nil
}

// purchaseCandidate is one executable place to buy: a yard, and a hull of ours
// standing at it to buy through.
type purchaseCandidate struct{ yard, buyer string }

// resolvePurchaseCandidates lists every executable place to buy for a placement,
// best first.
//
// A purchase needs a hull ALREADY STANDING at the yard — the purchase machinery
// navigates and docks the buyer itself, so a buyer that cannot reach the counter
// is not a buyer. Candidates, in order:
//
//  1. the yard recorded on the slot, if a previous tick already chose one;
//  2. then each probe-selling yard in the placement's OWN system, cheapest
//     first.
//
// The recorded yard is a PREFERENCE, not a commitment — the rest of the list is
// still tried. Treating it as binding would let a claimed placement stall
// permanently: the hull that made its yard executable can be flown off by any
// other coordinator, and the placement would then be skipped every tick forever
// while a perfectly good yard sat unused next door.
//
// Presence at a yard is matched WAYPOINT-wise and never kind-wise. When a
// probe-selling yard is also a whitelisted market the screen slots it as
// MARKET, so the probe standing on that yard sits under a MARKET-kind row;
// filtering for kind == YARD would miss it and buy a second hull for a waypoint
// that already has one.
//
// Nothing here ever looks outside the placement's own system: a cross-map buy
// would strand a fresh probe several gate hops from the slot it was bought for.
func resolvePurchaseCandidates(ctx context.Context, p BuyPorts, playerID int, slot QueuedSlot, inSystem []QueuedSlot) ([]purchaseCandidate, error) {
	listed, err := p.Yards.ListProbeYards(ctx, slot.System)
	if err != nil {
		return nil, fmt.Errorf("failed to list probe yards in %q: %w", slot.System, err)
	}

	yards := make([]string, 0, len(listed)+1)
	if slot.PurchaseYard != "" {
		yards = append(yards, slot.PurchaseYard)
	}
	for _, y := range listed {
		if y != slot.PurchaseYard {
			yards = append(yards, y)
		}
	}

	candidates := make([]purchaseCandidate, 0, len(yards))
	for _, yard := range yards {
		buyer, found, err := buyerAt(ctx, p, playerID, yard, inSystem)
		if err != nil {
			return nil, err
		}
		if found {
			candidates = append(candidates, purchaseCandidate{yard: yard, buyer: buyer})
		}
	}
	return candidates, nil
}

// buyerAt finds a hull of ours standing at one waypoint: first a parked sensing
// probe the ledger already accounts for, then any probe of ours the ships table
// shows docked there.
func buyerAt(ctx context.Context, p BuyPorts, playerID int, waypoint string, inSystem []QueuedSlot) (string, bool, error) {
	for _, s := range inSystem {
		if s.Waypoint == waypoint && s.State == SlotStateParked && s.AssignedShip != "" {
			return s.AssignedShip, true, nil
		}
	}
	ship, found, err := p.Ships.DockedProbeAt(ctx, playerID, waypoint)
	if err != nil {
		return "", false, fmt.Errorf("failed to look for a docked probe at %q: %w", waypoint, err)
	}
	return ship, found, nil
}

// claimForPurchase moves a placement from WANTED to QUEUED before any money
// moves, so a second writer cannot buy a second probe for the same placement. It
// reports whether this tick owns the placement.
//
// A placement already in QUEUED was claimed by an earlier tick whose purchase
// failed; re-claiming it would be a wasted write.
func claimForPurchase(ctx context.Context, p BuyPorts, playerID int, slot QueuedSlot, yard string, rep *BuyReport) (bool, error) {
	if slot.State != SlotStateWanted {
		return true, nil
	}
	err := p.Ledger.TransitionSlot(ctx, playerID, slot.Waypoint, SlotStateWanted, SlotStateQueued,
		SlotFields{PurchaseYard: &yard})
	switch {
	case errors.Is(err, ErrSlotClaimed):
		// Another writer owns this placement now. Routine, and nothing was
		// spent — move on to the next one.
		return false, nil
	case err != nil:
		// Not contention: the ledger itself is refusing writes. A claim we
		// cannot record is a claim that cannot protect a purchase.
		return false, fmt.Errorf("failed to claim sensing slot %s for purchase: %w", slot.Waypoint, err)
	}
	rep.Queued++
	return true, nil
}

// recordPurchase writes the bought hull against its placement and tags it.
//
// The two writes are ordered by what a failure between them costs. The hull is
// recorded FIRST so the probe cap counts something we have paid for even if the
// tag write then fails; the reverse order would leave a paid-for hull uncounted
// and authorise buying it again. The tag is therefore best-effort here, and the
// placement machine re-asserts it (idempotently) on the next edge.
//
// A failed RECORD after a successful purchase is the one unrecoverable shape —
// money spent, hull unaccounted — so it surfaces as an error and stops the
// drain rather than spending further against a ledger that is not accepting
// writes.
//
// The yard is recorded alongside the hull because a retry may have fallen back
// to a different one than the claim chose. Leaving the original would leave the
// row asserting a provenance the purchase did not have.
func recordPurchase(ctx context.Context, p BuyPorts, playerID int, slot QueuedSlot, yard string, probe BoughtProbe) error {
	if err := p.Ledger.TransitionSlot(ctx, playerID, slot.Waypoint, SlotStateQueued, SlotStateBought,
		SlotFields{AssignedShip: &probe.ShipSymbol, PurchaseYard: &yard}); err != nil {
		return fmt.Errorf(
			"bought probe %s for slot %s but could not record it (hull unaccounted, drain halted): %w",
			probe.ShipSymbol, slot.Waypoint, err)
	}
	if err := p.Fleet.AssignFleet(ctx, playerID, probe.ShipSymbol, SensingParkedFleetTag); err != nil {
		// Best-effort by design (see above), but NAMED: an untagged hull looks
		// like an idle undedicated probe to every other coordinator's ownership
		// sweep and can be poached. The placement machine re-asserts the tag on
		// the next edge, so this is a one-tick exposure — but a silent one would
		// leave a poached hull with no trace of how it got away.
		logging.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf(
			"Bought probe %s is recorded against slot %s but was not tagged into the sensing fleet (poachable until the placement machine re-tags it): %v",
			probe.ShipSymbol, slot.Waypoint, err), map[string]interface{}{
			"action":      "parked_sensing_purchase_tag_failed",
			"ship_symbol": probe.ShipSymbol,
			"waypoint":    slot.Waypoint,
		})
	}
	return nil
}

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

// probeListingMemoTTL is how long a PERSISTED shipyard listing set is trusted
// before the yard is asked again.
//
// THE WASTE IT REMOVES. ListProbeYards falls back to shipyard-TRAIT waypoints
// whenever a system has no stored probe listing, and that fallback is the normal
// path — only a handful of true probe yards are known fleet-wide. Those waypoints
// are real shipyards that simply do not sell probes, so every one of them that a
// hull happens to stand at costs one live quote per drain tick, forever, and the
// answer is discarded each time. The per-tick refusalMemo already stops the
// REPEATS within a tick; nothing carried the fact ACROSS ticks.
//
// WHY SIX HOURS. At ~57 drain cycles an hour a dead yard costs ~57 calls/hour
// today; trusting a stored reading for six hours costs one call per six hours per
// yard, a ~99.7% reduction, while still re-checking each yard ~20 times inside a
// ~120-hour era. A shorter interval does not scale with the thing that creates
// these yards: charting toward the 300-system target keeps adding shipyard-trait
// waypoints, and at one call/hour each a few dozen of them would re-create the
// very cost this removes. A longer one buys little more — the second six hours
// saves a further 0.08 calls/hour per yard — while widening the window in which a
// restocked yard is wrongly written off.
//
// BEING WRONG IS CHEAP AND SELF-HEALING, which is what makes six hours defensible
// rather than merely convenient: a yard that starts selling probes is simply not
// bought from until the interval elapses. It blocks nothing else — seed staging
// tests hull PRESENCE (staffedAt), not probe stock, so expansion is unaffected —
// and any other yard in the system still serves the placement.
//
// A plain constant, deliberately not a knob, in the manner of maxDrainAttempts:
// it paces how long one local fact is trusted, and nothing downstream benefits
// from tuning it.
const probeListingMemoTTL = 6 * time.Hour

// ProbeListingMemo reports what a PREVIOUS shipyard read persisted about a yard's
// stock, so the drain can stop paying to re-learn a standing fact.
//
// OPTIONAL: a nil memo quotes everything, which is exactly the behaviour that
// existed before this port, so an unwired deployment is unchanged.
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

// probeStock is what a yard's STORED listings say about whether it sells a probe. It is the memo's
// three answers named, so the two engines that consult them cannot drift into different rules.
type probeStock int

const (
	// probeStockUnread — no listing has ever been persisted for this yard. It is how a yard ENTERS
	// the memo, so it must never be read as a negative: doing so would freeze the fleet's knowledge
	// and permanently write off every counter nothing had happened to look at yet.
	probeStockUnread probeStock = iota
	// probeStockSells — a stored, priced SHIP_PROBE listing. Evidence, not a trait guess.
	probeStockSells
	// probeStockNone — read recently, and it sells no probe. The standing fact worth acting on.
	probeStockNone
)

// readProbeStock classifies one yard from the stored listings, applying the memo's staleness rule in
// ONE place.
//
// A STALE probe-less reading degrades to UNREAD, so a restocked counter is reconsidered rather than
// written off for the era — the same rule skipKnownProbeless applies, and it is written here so the
// buy queue and seed staging cannot drift apart on it. A nil memo answers UNREAD, which is exactly
// the behaviour both callers had before the port existed.
func readProbeStock(ctx context.Context, memo ProbeListingMemo, playerID int, yard string, now time.Time) (probeStock, time.Time, error) {
	if memo == nil {
		return probeStockUnread, time.Time{}, nil
	}
	sellsProbe, scannedAt, known, err := memo.LastListingScan(ctx, playerID, yard)
	if err != nil {
		return probeStockUnread, time.Time{}, fmt.Errorf("failed to read the stored listings of %q: %w", yard, err)
	}
	switch {
	case !known:
		return probeStockUnread, scannedAt, nil
	case sellsProbe:
		return probeStockSells, scannedAt, nil
	case now.Sub(scannedAt) >= probeListingMemoTTL:
		return probeStockUnread, scannedAt, nil // gone stale; ask again
	default:
		return probeStockNone, scannedAt, nil
	}
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
	// WhitelistGoods is the whitelisted goods this placement was recorded as
	// watching. It is what lets the foothold path prove that releasing a hull
	// leaves its system's goods coverage intact (see coveredByOthers).
	//
	// EMPTY MEANS UNKNOWN, NEVER "WATCHES NOTHING". The adapter yields an empty
	// list both for a row that genuinely records no goods and for one whose
	// goods column will not decode, and every reader here must treat the two
	// alike: as an absence of evidence that can only ever make a hull LESS
	// eligible to be moved, never more.
	WhitelistGoods []string
}

// ScreenedSystem is one screened system's identity and size.
type ScreenedSystem struct {
	System       string
	DepthCredits int64
	// ScreenedAt is when this system was last looked at, or NIL for one that
	// never has been. It is what lets the sweep rotate least-recently-screened
	// first instead of re-screening a fixed alphabetical head forever.
	//
	// NIL IS MEANINGFUL AND MUST NOT COLLAPSE TO THE ZERO TIME. A never-screened
	// system is the newly-discovered frontier — the case the sweep most needs to
	// reach — and the zero time would merely make it sort first by accident,
	// leaving any reader that dereferences the pointer to panic instead. The
	// pointer keeps "never" a case a comparator has to answer for explicitly.
	ScreenedAt *time.Time
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
	TransitionSlot(ctx context.Context, playerID int, waypoint, kind, fromState, toState string, set SlotFields) error
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

// BuyStep names which half of the purchase path refused. The two are worth
// telling apart because they fail for entirely different reasons and call for
// entirely different operator responses: a QUOTE refusal is the yard (out of
// stock, unpriced listing, an API that will not answer), while a BUY refusal is
// the transaction (a hull that cannot be claimed or docked, a counter that
// declined, credits that moved between pricing and paying). Collapsing them
// into one "it did not work" is what made this queue's failures unreadable.
type BuyStep string

const (
	// BuyStepQuote is a refusal at the live price read, BEFORE any floor check
	// is possible and before any hull is engaged.
	BuyStepQuote BuyStep = "quote"
	// BuyStepBuy is a refusal at the counter, after the price cleared the floor.
	BuyStepBuy BuyStep = "buy"
	// BuyStepMemo is a yard passed over WITHOUT being asked, because a stored
	// listing read already says it sells no probe.
	//
	// It is reported separately from BuyStepQuote precisely because it costs no
	// API call: an operator reading the cycle summary must be able to tell "this
	// counter refused us today" from "we did not ask, and here is why". Conflating
	// them would hide the very saving this step exists to make, and would make the
	// next defect of this shape as invisible as this one was.
	BuyStepMemo BuyStep = "memo"
)

// BuyRefusal is one counter's refusal this tick, with the number of placements
// that met it.
//
// It is AGGREGATED rather than recorded per attempt for a reason that is
// operational, not cosmetic: this drain runs every ~30s forever, so a per-attempt
// record would turn a standing refusal into an unbounded log. One row per
// distinct refusal, carrying how many placements it blocked, says the same thing
// in a line an operator can actually read — and Count is the number that
// distinguishes "one placement is unlucky" from "this counter is blocking the
// whole queue".
type BuyRefusal struct {
	// Step is which half refused. See BuyStep.
	Step BuyStep
	// Yard is the counter that refused.
	Yard string
	// Buyer is the hull the purchase would have gone through. EMPTY on a quote
	// refusal, where no hull was engaged — an empty Buyer is therefore
	// meaningful, not missing data.
	Buyer string
	// Reason is the underlying error, verbatim. This is the field that carries
	// "out of stock" vs "hull not docked" vs "insufficient credits", so it is
	// never summarised or truncated here.
	Reason string
	// Count is how many placements this same refusal blocked this tick.
	Count int
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
	// Footholds counts SPARE placements filled by flying a surplus hull across
	// a gate — the path that establishes presence in a system that could not
	// fund one for itself. See foothold.go.
	//
	// It is reported SEPARATELY from Reused, which it otherwise resembles,
	// because the two spend different things: a reuse moves an idle spare and
	// costs nothing, while a foothold takes a hull off a working market and
	// leaves that market unwatched until it is re-bought. An operator must be
	// able to see the second happening without inferring it.
	Footholds int
	// Attempts counts every trip through the buy path, successful or not,
	// against maxDrainAttempts.
	Attempts int
	// Refusals is why the counters that refused this tick refused, one row per
	// distinct refusal. A tick with Attempts > 0 and Bought == 0 and no Refusals
	// is a contradiction — every path that burns an attempt without buying
	// records one.
	Refusals []BuyRefusal
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
			k.KMilli,
		),
	}

	// One memo per TICK, never longer. A refusal is re-learned on the next tick
	// from scratch, so a counter that was merely having a bad minute is retried
	// 30 seconds later rather than blacklisted.
	memo := newRefusalMemo()

	// One foothold broker per TICK, for the same reason and with the same
	// lifetime: it holds the surplus pool the tick allocates from, so two
	// placements cannot both be handed the same hull.
	footholds := &footholdBroker{}

	// How many attempts the FILLS may spend before standing aside for the seeds
	// queued behind them — the whole budget when no seed is outstanding. It
	// SPLITS maxDrainAttempts rather than adding to it. See seedshare.go.
	fillBudget := fillAttemptBudget(candidates)

	for _, slot := range candidates {
		if rep.Attempts >= maxDrainAttempts {
			break
		}
		if st.owned >= st.cap {
			rep.CapHeld = true
			break
		}
		// Checked BEFORE any read, so a fill standing aside costs nothing: the
		// loop runs past the remaining fills to reach the seeds behind them.
		if yieldsToSeeds(slot, rep.Attempts, fillBudget) {
			continue
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

		buys, err := resolvePurchaseCandidates(ctx, p, playerID, slot, inSystem, clock.Now(), &rep, memo)
		if err != nil {
			return rep, err
		}
		if len(buys) == 0 {
			// No yard in this system has a hull of ours we can buy through.
			//
			// For a SPARE placement this IS the buying deadlock: expansion asked
			// for a foothold in a system we have judged but never occupied, and
			// the only way to buy there is to already be there. The foothold path
			// is the one thing that breaks it — it flies a surplus hull in from
			// within gate reach, after which the system funds itself. It spends
			// no money and issues no API call, so it costs no attempt.
			foothold, ferr := footholds.fill(ctx, p, playerID, slot, &rep)
			if ferr != nil {
				return rep, ferr
			}
			if foothold {
				continue
			}

			// Everything else simply waits until expansion puts a usable probe
			// within reach. Not an error and not worth a log line per tick, and
			// never a blind cross-map buy. Costs no attempt because it touched
			// no API.
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

		halt, err := fillSlot(ctx, p, playerID, slot, buys, &st, &rep, memo)
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
	memo *refusalMemo,
) (bool, error) {
	for _, candidate := range buys {
		// A counter that already refused THIS TICK is not asked again. The
		// refusal belongs to the counter — an unpriceable yard, a hull that
		// cannot buy — not to the placement that happened to meet it first, so
		// re-asking inside one tick spends an attempt to re-learn a fact already
		// recorded, and spends it out of the budget a working yard would have
		// used. It costs no attempt for the same reason a placement with no
		// reachable yard costs none: it touches no API.
		if memo.blocks(rep, candidate.yard, candidate.buyer) {
			continue
		}
		if rep.Attempts >= maxDrainAttempts {
			return true, nil
		}
		rep.Attempts++

		quote, err := p.Purchaser.Quote(ctx, playerID, candidate.yard)
		if err != nil {
			// Unpriceable counter: no floor check is possible, try the next. The
			// buyer is deliberately not named — no hull was engaged.
			memo.record(rep, BuyStepQuote, candidate.yard, "", err.Error())
			continue
		}
		if st.credits-quote < st.floor {
			// At the floor. Stop rather than shop for a cheaper yard: the floor
			// exists to protect working capital, and a marginally cheaper probe
			// erodes it just the same.
			rep.FloorHeld = true
			return true, nil
		}

		probe, err := p.Purchaser.Buy(ctx, playerID, candidate.buyer, candidate.yard, p.ClaimOwnerContainerID)
		if err != nil || probe.ShipSymbol == "" {
			// This counter refused; the placement is still fillable elsewhere.
			// The buyer IS named here: "the yard is out of stock" and "this hull
			// cannot buy" are the two readings an operator has to choose
			// between, and the hull is what tells them apart.
			memo.record(rep, BuyStepBuy, candidate.yard, candidate.buyer, buyRefusalReason(err))
			continue
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

// refusalMemo remembers which counters have already refused this tick, and
// carries the aggregation behind BuyReport.Refusals.
//
// It exists to answer one question cheaply: has this counter already told us
// no? Before it, the drain re-asked every yard once per placement, which on the
// live fleet meant two counters were asked the same question three times each
// and the entire six-attempt budget was consumed re-confirming two facts.
type refusalMemo struct {
	// idx maps a refusal key to the BuyReport.Refusals row already recorded for
	// it, so a repeat increments a count instead of appending a duplicate row.
	idx map[string]int
}

func newRefusalMemo() *refusalMemo { return &refusalMemo{idx: make(map[string]int)} }

// quoteKey scopes a quote refusal to the YARD ALONE. An unpriceable counter is
// unpriceable no matter which hull we would have bought through, so naming the
// buyer here would let the same dead yard be re-quoted once per hull standing on
// it.
func quoteKey(yard string) string { return "quote\x00" + yard }

// buyKey scopes a buy refusal to the yard AND the hull. The pairing is the
// point: the same yard may well sell through a different hull (the refusal is
// often the hull's — an unclaimable or undocked one), so a buy refusal must not
// condemn the counter itself.
func buyKey(yard, buyer string) string { return "buy\x00" + yard + "\x00" + buyer }

// blocks reports whether this candidate has already refused this tick, counting
// the placement it just blocked.
//
// Both keys are consulted: a yard that failed to QUOTE cannot be bought from
// through any hull, so the quote refusal blocks every candidate on that yard,
// while a buy refusal blocks only the pairing that actually failed.
func (m *refusalMemo) blocks(rep *BuyReport, yard, buyer string) bool {
	for _, key := range []string{quoteKey(yard), buyKey(yard, buyer)} {
		if i, ok := m.idx[key]; ok {
			rep.Refusals[i].Count++
			return true
		}
	}
	return false
}

// record files one refusal, folding a repeat into the existing row's count.
func (m *refusalMemo) record(rep *BuyReport, step BuyStep, yard, buyer, reason string) {
	key := quoteKey(yard)
	row := BuyRefusal{Step: step, Yard: yard, Reason: reason, Count: 1}
	if step == BuyStepBuy {
		key = buyKey(yard, buyer)
		row.Buyer = buyer
	}
	if i, ok := m.idx[key]; ok {
		rep.Refusals[i].Count++
		return
	}
	m.idx[key] = len(rep.Refusals)
	rep.Refusals = append(rep.Refusals, row)
}

// buyRefusalReason names the nil-error refusal explicitly.
//
// A purchase that returns no error and no hull is the one shape that would
// otherwise record an EMPTY reason — which is precisely the silence this whole
// change exists to remove, so it is given words rather than left blank.
func buyRefusalReason(err error) string {
	if err != nil {
		return err.Error()
	}
	return "the purchase path reported no hull and no error"
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
// every IN_SCOPE fill sorted by COVERAGE ascending with depth as the tiebreak,
// then the seeds.
//
// COVERAGE FIRST, BECAUSE A BUDGET OF SIX IS SPENT ON THE HEAD OF THIS LIST.
// Sorting on depth alone let the richest system's placements occupy the whole
// head of every tick, so a poorer system never got a turn however long it
// waited — measured on the live fleet as 67% of parked probes sitting in three
// systems while covered systems held one each. Depth is still the tiebreak, so
// once coverage is even this degenerates to the old ordering exactly.
//
// EVERY SLOT CARRIES ITS OWN COVERAGE, which is the part that would otherwise be
// got wrong. Ranking purely on probes already parked would tie a 0-probe
// system's twenty-two outstanding placements at rank 0 together, and that system
// would take the whole tick — reproducing the concentration one tier down. So
// the i-th outstanding placement of a system ranks at parked + i: its first slot
// competes at 0, its second at 1, and a second system on 0 outranks the first
// system's second slot.
//
// The index is taken walking the list in the order the LEDGER returned it, before
// any sort, because that order is FIFO per system and the sort below is stable —
// which is what keeps a placement from being overtaken by a newer one in its own
// system, tick after tick.
//
// The hulls a system already holds come from a separate narrow read — see
// coverageBySystem for why they are not simply folded into the query below.
//
// QUEUED slots are drained alongside WANTED ones. A slot reaches QUEUED when a
// previous tick claimed it and its purchase then failed; without re-reading
// QUEUED here that claim would be a one-way door and the placement would never
// be retried. A QUEUED slot is a candidate AND consumes a coverage index, which
// is right both ways round: it still needs working, and a claim already made is
// a probe already spoken for.
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

	if len(fills) == 0 {
		return seeds, nil
	}
	covered, err := coverageBySystem(ctx, p, playerID)
	if err != nil {
		return nil, err
	}

	// Each fill's effective coverage, taken in the ledger's own order so the
	// per-system index is FIFO.
	ranked := make([]rankedFill, len(fills))
	outstanding := make(map[string]int, len(fills))
	for i, fill := range fills {
		ranked[i] = rankedFill{slot: fill, coverage: covered[fill.System] + outstanding[fill.System]}
		outstanding[fill.System]++
	}

	// Stable, so the ledger's own waypoint ordering survives as the last
	// tiebreak — which makes the queue FIFO per system and its output
	// reproducible tick to tick.
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].coverage != ranked[j].coverage {
			return ranked[i].coverage < ranked[j].coverage
		}
		return depth[ranked[i].slot.System] > depth[ranked[j].slot.System]
	})

	out := make([]QueuedSlot, 0, len(ranked)+len(seeds))
	for _, r := range ranked {
		out = append(out, r.slot)
	}
	return append(out, seeds...), nil
}

// rankedFill is one fill beside the coverage it competes at — the count of hulls
// its system already holds, plus its own position among that system's
// outstanding placements.
type rankedFill struct {
	slot     QueuedSlot
	coverage int
}

// coverageBySystem counts the hulls each system already has or has coming.
//
// The states are exactly the ones states.go calls hull-bearing — "from BOUGHT
// onwards a hull exists and counts against the probe cap" — because coverage
// asks the same question the cap does. Counting only PARKED would read a system
// with five probes in flight as empty and pile a sixth onto it.
//
// A SEPARATE READ RATHER THAN A WIDER CANDIDATE QUERY, and that is not a
// stylistic choice. Four stages of the tick call SlotsByState and the STATE LIST
// is the only thing that tells them apart — the reconcile ordering tests
// fingerprint each stage by it. Widening the candidate query to cover these
// states would have spelled `allSlotStates` exactly, making the drain's read
// indistinguishable from the expansion tick's and the adoption sweep's, so an
// ordering assertion would silently match the wrong stage. The narrow read keeps
// both fingerprints unique, and it keeps the filled rows structurally incapable
// of reaching the candidate list — a filled row worked as a candidate would buy
// a second probe for a waypoint that already holds one.
//
// It costs one local ledger query, taken only once the tick has fills to order,
// so a tick with nothing to buy still costs nothing. It is never an API call.
//
// FAILS CLOSED, like every other read in this queue. Reading an unavailable
// ledger as "no coverage anywhere" would not merely mis-order — it would rank
// every system at zero and hand the whole budget to whichever one sorts deepest,
// which is the concentration this ordering exists to prevent, arriving silently
// and exactly when the ledger is unwell. Nothing has been spent at this point in
// the tick, so stopping costs no requests and one cycle.
func coverageBySystem(ctx context.Context, p BuyPorts, playerID int) (map[string]int, error) {
	filled, err := p.Ledger.SlotsByState(ctx, playerID, SlotStateBought, SlotStateInTransit, SlotStateParked)
	if err != nil {
		return nil, fmt.Errorf("sensing coverage unreadable, buying nothing this tick: %w", err)
	}
	covered := make(map[string]int, len(filled))
	for _, slot := range filled {
		covered[slot.System]++
	}
	return covered, nil
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
		err := p.Ledger.TransitionSlot(ctx, playerID, target.Waypoint, target.Kind, target.State, SlotStateInTransit,
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
		if err := p.Ledger.TransitionSlot(ctx, playerID, spare.Waypoint, spare.Kind, SlotStateParked, SlotStateWanted,
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
func resolvePurchaseCandidates(
	ctx context.Context,
	p BuyPorts,
	playerID int,
	slot QueuedSlot,
	inSystem []QueuedSlot,
	now time.Time,
	rep *BuyReport,
	memo *refusalMemo,
) ([]purchaseCandidate, error) {
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
		// Asked BEFORE buyerAt so a dead yard costs neither the ships read nor the
		// live quote behind it. This is a LOCAL read; it never touches the API.
		if skipKnownProbeless(ctx, p, playerID, yard, now, rep, memo) {
			continue
		}
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

// skipKnownProbeless reports whether a yard may be passed over WITHOUT a live
// quote, because a stored listing read already says it sells no probe.
//
// THE THREE ANSWERS, and only the middle one skips:
//
//   - never read (known=false) → ASK. This is how a yard enters the memo at all;
//     treating an absent reading as a negative would freeze the fleet's knowledge
//     and permanently write off every yard nothing had happened to read yet.
//   - read recently, sells no probe → SKIP. The standing fact this change exists
//     to stop paying for.
//   - read recently, sells a probe → ASK. The memo removes candidates; it never
//     waves one through, so a yard it likes is quoted and floor-checked exactly as
//     before. NOTHING here can approve a purchase.
//
// A STALE reading is treated as never-read, so a restocked yard is re-checked
// every probeListingMemoTTL rather than written off for the era.
//
// FAILS OPEN, which inverts this queue's usual direction and is deliberate. The
// memo is an API-budget optimisation, not a money guard: the worst an open failure
// costs is the single call the drain already makes today, whereas failing closed
// would let one unhealthy local read starve probe buying across the whole fleet.
// RULINGS #4 is untouched either way — every money guard sits downstream of this
// and judges the purchase unchanged.
//
// The skip is RECORDED, through the same per-tick memo that aggregates refusals,
// so a yard that stops being queried does not also stop being reported.
func skipKnownProbeless(
	ctx context.Context,
	p BuyPorts,
	playerID int,
	yard string,
	now time.Time,
	rep *BuyReport,
	memo *refusalMemo,
) bool {
	stock, scannedAt, err := readProbeStock(ctx, p.ListingMemo, playerID, yard, now)
	if err != nil || stock != probeStockNone {
		// FAILS OPEN on the read error, unchanged: the memo is an API-budget
		// optimisation, not a money guard, and every money guard sits downstream.
		return false
	}
	memo.record(rep, BuyStepMemo, yard, "", fmt.Sprintf(
		"stored listings show no probe (read %s ago; re-checked after %s)",
		now.Sub(scannedAt).Truncate(time.Second), probeListingMemoTTL))
	return true
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
	err := p.Ledger.TransitionSlot(ctx, playerID, slot.Waypoint, slot.Kind, SlotStateWanted, SlotStateQueued,
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
	if err := p.Ledger.TransitionSlot(ctx, playerID, slot.Waypoint, slot.Kind, SlotStateQueued, SlotStateBought,
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

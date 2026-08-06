package commands

import (
	"context"
	"fmt"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
)

// THE HEAVY-YARD PRICING ERRAND.
//
// A SpaceTraders shipyard prices its hulls only while a ship stands at the waypoint. A
// presence-less read still returns the yard's ship-type CATALOGUE, which is persisted at
// purchase_price 0 — "this yard sells a heavy, at an ask nobody has read". Every money-facing
// consumer then correctly ignores that row (a zero can never feed a price guard), so a fleet can
// KNOW where to buy a heavy and still never form a reservation, never accumulate treasury, and
// never buy one. That is the state this errand exists to leave.
//
// The errand uses the idiom the bootstrap coordinator applies to the cold home yard:
// when the price cannot be read, SEND A HULL so the next scan reads it. This tick still buys
// nothing, so no money guard is weakened (RULINGS #4) — it makes the price readable, it does not
// bypass the price.
//
// Nothing is stored (RULINGS #2). Both halves of "is an errand already under way?" are DERIVED
// from durable ship rows each tick: a hull's location IS its destination while it is in transit
// (Ship.StartTransit), so a hull standing at — or flying to — an unpriced heavy yard is the errand,
// and there is no cursor to go stale, re-fire on restart, or leak across eras.

// heavyPricingErrandsInFlight bounds the errand to ONE hull at a time, fleet-wide.
//
// A const, not a knob (RULINGS #5). The bound is not an economic dial: one hull is all it takes to
// price a yard, the catalogue is walked nearest-first so the most useful yard is priced first, and
// every additional simultaneous errand would take another spare probe off station to buy
// information we are about to get anyway. Raising it could only ever trade coverage for latency.
const heavyPricingErrandsInFlight = 1

// heavyPricingErrandFleet is the dedication a hull MUST carry to be eligible to fly a pricing
// errand: the PARKED SENSING pool — the fleet's spare hulls.
//
// IT USED TO NAME THE TRADE POOL, AND THAT IS THE DEFECT THIS CONST CARRIES THE SCAR OF (sp-gmfvw).
// The errand was built to draw from the one pool that is never spare: a trade hull is either flying
// a lane or docked mid-tour, so the eligible set was empty essentially always and the errand
// declined every tick of an entire era while thirteen known heavy yards sat at purchase_price 0.
//
// A probe is the right carrier because PRICING A YARD NEEDS PRESENCE, NOT CARGO. A shipyard lists
// its asks to whoever stands at the waypoint; the hull reads, it does not carry. Probes are the
// cheap, expendable, genuinely-idle hulls — and one taken off station for a single hop costs one
// market's freshness, where a trade hull taken off a lane costs income.
//
// It stays an ALLOWLIST of exactly one tag: an undedicated hull carries "" (another controller is
// about to claim it), a contract hauler carries "contract", a trade hull carries "trade". None of
// them equals this, so a tag invented tomorrow is refused by default rather than silently admitted.
//
// The tag is now taken as a CROSS-PACKAGE CONST rather than re-spelled here. The allowlist and the
// tag-writer must agree exactly or the eligible set silently empties again — which is precisely the
// failure this bead fixed — so they are made to be the same symbol rather than trusted to match.
const heavyPricingErrandFleet = parkedsensing.SensingParkedFleetTag

// KnownHeavyYard is one KNOWN heavy-selling yard as the errand policy sees it — PRICED OR NOT.
//
// PurchasePrice 0 is the whole point of this type: it is the availability-only catalogue row that
// every priced read filters away, and it is exactly the row that needs a hull sent to it.
// Hops is the gate-jump distance from the nearest system we hold a hull in (0 = a system we are
// already in); Reachable=false means no gate path within the heavy reach bound was found, and such
// a yard is never targeted — a hull cannot be sent where it cannot fly.
type KnownHeavyYard struct {
	SystemSymbol   string
	WaypointSymbol string
	ShipType       string
	PurchasePrice  int64
	Hops           int
	Reachable      bool
}

// PricingErrandHull is one candidate carrier, carrying the RAW durable facts and no verdict.
//
// The eligibility judgement lives in this package (pricingErrandCarrier), never in the adapter
// that reads the rows: a precomputed "eligible" flag would move the one rule that protects the
// sensing fleet out of reach of the tests that pin it.
//
// Location is where the hull STANDS, or — while InTransit — where it is FLYING TO. The domain
// model sets currentLocation to the destination on StartTransit, which is what makes "an errand is
// already under way" a pure read of durable rows rather than stored intent.
type PricingErrandHull struct {
	Symbol string
	// Fleet is the dedicated_fleet tag. Only heavyPricingErrandFleet may fly the errand.
	Fleet     string
	Location  string
	Idle      bool
	InTransit bool
	// CargoCapacity is REPORTED AND DELIBERATELY NOT JUDGED. Pricing a yard needs presence at the
	// waypoint, not a hold, and every carrier the errand now draws on is a zero-cargo probe — so a
	// hold predicate here would exclude exactly the pool the errand exists to use (sp-gmfvw). It is
	// still carried because it is a raw durable fact of the hull and the adapter's contract is to
	// report facts rather than verdicts; a future policy may want it, and a policy that wants it
	// should not have to re-open the port to get it.
	CargoCapacity int
	// MannedScoutPost is true when a LIVE scout post names this hull in one of its manning slots.
	//
	// It is the one fact that keeps the errand out of the scout coordinator's hulls now that both
	// draw from the same probe pool. A manning hull is usually mid-tour and therefore not Idle, but
	// BETWEEN tours it is idle and still owned — that window is the whole reason this is a separate
	// fact rather than a corollary of Idle. It is raw (a scout_posts row names this ship symbol),
	// not a verdict: the refusal itself lives in pricingErrandCarrier where the tests pin it.
	MannedScoutPost bool
}

// HeavyYardCatalogReader lists every KNOWN heavy-selling yard this era, priced or not, annotated
// with gate reach from the fleet's current systems. It is the ONLY read that can see the
// availability-only rows; every other heavy-yard read filters them out by design.
type HeavyYardCatalogReader interface {
	KnownHeavyYards(ctx context.Context, playerID int) ([]KnownHeavyYard, error)
}

// HeavyPricingErrandPort is the errand's two halves: who could go, and sending them.
//
// SendToYard NAVIGATES only. Presence in orbit is enough for a shipyard listing to price, and the
// purchase path docks on its own account, so the errand never docks, never quotes and never spends.
type HeavyPricingErrandPort interface {
	// ErrandHulls reports every hull that could conceivably carry an errand, with the raw facts
	// the policy judges. Implementations must NOT pre-filter on fleet tag — the allowlist is the
	// policy's, and a filter here would hide a mis-tagged hull from the test that pins it out.
	//
	// An implementation that cannot establish one of the facts must REFUSE with an error rather
	// than report the hull with that fact zeroed: an unreadable scout-post roster reported as
	// MannedScoutPost=false everywhere reads to the policy as "no post mans anything", which is
	// exactly the shape that pulls a working scout off station.
	ErrandHulls(ctx context.Context, playerID int) ([]PricingErrandHull, error)
	// SendToYard flies one hull to a yard waypoint so its listing prices on the next scan.
	SendToYard(ctx context.Context, playerID int, shipSymbol, waypointSymbol string) error
}

// heavyPricingErrand is one dispatch decision: this hull, to that yard.
type heavyPricingErrand struct {
	Ship string
	Yard string
}

// pricingErrandDecline NAMES why a tick sent no hull.
//
// It exists because the errand's silence was the defect behind the defect (sp-gmfvw): it declined
// on every tick of an entire era with no log line at all, so "the mechanism is running and waiting"
// and "the mechanism was never wired" were indistinguishable, and only production-log archaeology
// separated them. Every value below is a WAIT, not a failure — but a wait an operator can act on
// differently depending on which one it is, which is the whole point of naming it.
type pricingErrandDecline string

const (
	// pricingErrandDispatch is the non-decline: a hull is going.
	pricingErrandDispatch pricingErrandDecline = ""
	// pricingErrandCensusBlind — the owned-heavy census could not be read, so whether a heavy is
	// wanted at all is unknown. Fails toward moving nothing.
	pricingErrandCensusBlind pricingErrandDecline = "owned_heavy_census_unreadable"
	// pricingErrandAtHeavyCap — the fleet already holds its cap, so no price is needed.
	pricingErrandAtHeavyCap pricingErrandDecline = "already_at_heavy_cap"
	// pricingErrandNoYardKnown — no heavy-selling yard is in the catalogue at all. The free,
	// presence-less sweep keeps looking and costs no hull; this is the cold state, not a stall.
	pricingErrandNoYardKnown pricingErrandDecline = "no_heavy_yard_known"
	// pricingErrandNothingInReach — every known heavy yard is already priced (success) or lies
	// outside the gate reach bound (a genuine wait for the map to grow). The counts on the line
	// separate the two.
	pricingErrandNothingInReach pricingErrandDecline = "no_unpriced_yard_in_reach"
	// pricingErrandAlreadyInFlight — a hull is standing at, or flying to, an unpriced heavy yard.
	pricingErrandAlreadyInFlight pricingErrandDecline = "errand_already_in_flight"
	// pricingErrandNoCarrier — nothing free to send. THE LIVE FAILURE MODE: this is the state the
	// old trade-pool allowlist sat in permanently.
	pricingErrandNoCarrier pricingErrandDecline = "no_eligible_carrier"
)

// planHeavyPricingErrand decides the tick's errand, if there is one, and NAMES the reason when
// there is not. PURE — no clock, no I/O.
//
// Every "no errand" answer is a WAIT rather than a failure: nothing is known, everything known is
// priced or out of reach, an errand is already under way, or no eligible carrier is free. A wait
// costs nothing and is retried next tick; taking a hull we should not have taken is not
// recoverable. The order of the checks is the order of the questions an operator would ask.
func planHeavyPricingErrand(yards []KnownHeavyYard, hulls []PricingErrandHull) (heavyPricingErrand, pricingErrandDecline) {
	if len(yards) == 0 {
		return heavyPricingErrand{}, pricingErrandNoYardKnown
	}
	unpriced := unpricedHeavyYards(yards)
	if len(unpriced) == 0 {
		return heavyPricingErrand{}, pricingErrandNothingInReach
	}
	if errandsInFlight(unpriced, hulls) >= heavyPricingErrandsInFlight {
		return heavyPricingErrand{}, pricingErrandAlreadyInFlight
	}
	carrier, ok := pricingErrandCarrier(hulls)
	if !ok {
		return heavyPricingErrand{}, pricingErrandNoCarrier
	}
	return heavyPricingErrand{Ship: carrier, Yard: unpriced[0].WaypointSymbol}, pricingErrandDispatch
}

// unpricedHeavyYards is the errand's candidate list: known, reachable, and carrying no usable ask,
// ordered NEAREST FIRST with the waypoint symbol as the tiebreak.
//
// The order is TOTAL and stable — hops then symbol, and no two rows share a waypoint symbol for
// one ship type — so the same fleet state picks the same yard on every tick and across restarts.
// An unstable order would let two consecutive ticks each start an errand to a different yard and
// call the first one "already in flight", which is how a bound of one becomes a bound of none.
//
// A non-positive price is what "unpriced" means, and a negative one is treated identically: a
// nonsense ask is not a usable ask, and reading it as priced would leave the yard permanently
// un-errand-able while its row can never feed a guard.
func unpricedHeavyYards(yards []KnownHeavyYard) []KnownHeavyYard {
	out := make([]KnownHeavyYard, 0, len(yards))
	for _, y := range yards {
		if !y.Reachable || y.WaypointSymbol == "" || y.PurchasePrice > 0 {
			continue
		}
		out = append(out, y)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Hops != out[j].Hops {
			return out[i].Hops < out[j].Hops
		}
		return out[i].WaypointSymbol < out[j].WaypointSymbol
	})
	return out
}

// errandsInFlight counts the hulls already committed to pricing an unpriced heavy yard — standing
// at one, or flying to one.
//
// STANDING COUNTS, not just flying. A hull that has arrived but whose scan has not yet written the
// price is the same errand, one step further along; excluding it would dispatch a second hull on
// every tick of the arrival window and turn a one-at-a-time bound into a convoy.
func errandsInFlight(unpriced []KnownHeavyYard, hulls []PricingErrandHull) int {
	targets := make(map[string]struct{}, len(unpriced))
	for _, y := range unpriced {
		targets[y.WaypointSymbol] = struct{}{}
	}
	n := 0
	for _, h := range hulls {
		if h.Location == "" {
			continue
		}
		if _, ok := targets[h.Location]; ok {
			n++
		}
	}
	return n
}

// pricingErrandCarrier picks the SPARE PROBE to send, or reports that nothing may move this tick.
//
// FOUR CONJUNCTIVE PREDICATES, and each one is load-bearing:
//
//   - Fleet == heavyPricingErrandFleet — the allowlist, now the parked-sensing pool. A trade hull
//     is earning, an undedicated hull is about to be claimed by whoever is looking for one, and a
//     contract hauler is mid-workstream; none of them is spare. Selecting by "not busy" instead
//     would admit every tag nobody has thought of yet.
//   - !MannedScoutPost — the standing owner rule. The probe pool is SHARED with the scout
//     coordinator, so the fleet tag alone no longer separates a spare hull from a working one:
//     a hull a live scout post NAMES is that post's, not this engine's, even in the idle gap
//     between two tours. This is the predicate that replaced the old zero-hold lock, and it
//     guards the same door the old trade-only allowlist did — from the correct side.
//   - Idle — a hull mid-tour is working; the errand is worth a spare hull, never a working one.
//   - !InTransit — an idle hull still flying has a destination it was sent to; re-routing it
//     would strand whatever sent it.
//
// CARGO CAPACITY IS DELIBERATELY NOT CONSULTED. It used to be the second lock, and it was the lock
// that made the corrected pool unusable: every probe has a zero hold, so a hold predicate refuses
// every carrier the errand now wants. Pricing needs presence, not a hold (sp-gmfvw).
//
// Ties break on ship symbol so the same fleet picks the same hull every tick — an arbitrary winner
// would let two ticks disagree about which hull is "the" errand and dispatch both.
func pricingErrandCarrier(hulls []PricingErrandHull) (string, bool) {
	chosen := ""
	for _, h := range hulls {
		if h.Symbol == "" || h.Fleet != heavyPricingErrandFleet || h.MannedScoutPost {
			continue
		}
		if !h.Idle || h.InTransit {
			continue
		}
		if chosen == "" || h.Symbol < chosen {
			chosen = h.Symbol
		}
	}
	return chosen, chosen != ""
}

// runHeavyPricingErrand is the tick step: read the catalogue, read the fleet, and send at most one
// hull to at most one unpriced heavy yard.
//
// GATED ON WANTING A HEAVY AT ALL. An unreadable census, or a fleet already at its heavy cap,
// sends nothing: the errand costs a market's freshness while its probe is away and an API read, and
// buying information about a purchase that cannot happen is pure loss. This mirrors the
// reservation's own gate, which is why both stand down together at the cap.
//
// Every read failure is a logged WAIT, never a fatal error. A tick that cannot see the catalogue
// simply does not dispatch, and the next tick tries again — the same shape the bootstrap errand
// takes, and for the same reason: nothing here spends money, so nothing here needs to fail closed
// against a spend.
func (h *RunFleetAutosizerCoordinatorHandler) runHeavyPricingErrand(
	ctx context.Context,
	cmd *RunFleetAutosizerCoordinatorCommand,
	cfg autosizerRunConfig,
	in tickInputs,
) {
	heavyPricingErrandTick(ctx, cmd.ContainerID, cmd.PlayerID,
		in.heaviesOwned, in.heaviesOwnedOK, cfg.HeavyCap, h.heavyYardCatalog, h.heavyErrand)
}

// heavyPricingErrandTick is the errand's tick step, and it takes FACTS rather than a coordinator.
// The errand belongs to the fleet's heavy buying, not to whichever coordinator currently owns that
// — and a second copy for a second driver is exactly how one of them would quietly stop honouring
// the one-hull-at-a-time bound or the standing-owner rule.
func heavyPricingErrandTick(
	ctx context.Context,
	containerID string,
	playerID int,
	heaviesOwned int,
	heaviesOwnedOK bool,
	heavyCap int,
	catalog HeavyYardCatalogReader,
	port HeavyPricingErrandPort,
) {
	// UNWIRED IS THE ONE SILENT PATH, and deliberately: the composition root already announces it
	// once at boot with a loud WARNING naming the missing capability, and a per-tick line for a
	// feature that is permanently absent buys nothing an operator can act on.
	if catalog == nil || port == nil {
		return
	}
	logger := common.LoggerFromContext(ctx)

	// No heavy wanted ⇒ no price needed. Fails toward doing nothing on a blind census, which is
	// the direction that never moves a hull on a signal we cannot see. Both shapes are NAMED
	// rather than returned quietly: "at the cap" is success and "the census is blind" is a
	// degraded read, and an operator would act on them differently.
	if !heaviesOwnedOK {
		logPricingErrandDecline(ctx, containerID, pricingErrandCensusBlind, nil, nil)
		return
	}
	if heaviesOwned >= heavyCap {
		logPricingErrandDecline(ctx, containerID, pricingErrandAtHeavyCap, nil, nil)
		return
	}

	yards, err := catalog.KnownHeavyYards(ctx, playerID)
	if err != nil {
		logger.Log("WARN", fmt.Sprintf("Heavy pricing errand: the known-heavy-yard catalogue is unreadable (%v) — no hull sent this tick, so an unpriced heavy yard stays unpriced and no reservation can form", err), map[string]interface{}{
			"action": "autosizer_heavy_pricing_catalogue_unreadable", "container_id": containerID,
		})
		return
	}

	hulls, err := port.ErrandHulls(ctx, playerID)
	if err != nil {
		logger.Log("WARN", fmt.Sprintf("Heavy pricing errand: the fleet is unreadable (%v) — no hull sent this tick", err), map[string]interface{}{
			"action": "autosizer_heavy_pricing_fleet_unreadable", "container_id": containerID,
		})
		return
	}

	errand, decline := planHeavyPricingErrand(yards, hulls)
	if decline != pricingErrandDispatch {
		logPricingErrandDecline(ctx, containerID, decline, yards, hulls)
		return
	}

	if err := port.SendToYard(ctx, playerID, errand.Ship, errand.Yard); err != nil {
		logger.Log("WARN", fmt.Sprintf("Heavy pricing errand: sending %s to %s failed (%v) — retrying on a later tick", errand.Ship, errand.Yard, err), map[string]interface{}{
			"action": "autosizer_heavy_pricing_dispatch_failed", "container_id": containerID,
			"ship": errand.Ship, "yard": errand.Yard,
		})
		return
	}

	logger.Log("INFO", fmt.Sprintf("Heavy pricing errand: sent %s to %s so the yard's presence-gated ask becomes readable — no buy this tick; the reservation forms once the price lands", errand.Ship, errand.Yard), map[string]interface{}{
		"action": "autosizer_heavy_pricing_dispatched", "container_id": containerID,
		"ship": errand.Ship, "yard": errand.Yard,
	})
}

// unpricedOutOfReachHeavyYards counts the yards that DO need pricing and cannot be flown to.
//
// It is the number that separates the two states hiding inside "nothing to price": every yard we
// know is priced (success), versus every yard we know is unpriced and beyond the gate reach bound
// (a wait for the map to grow, and the state staging sat in with ten of its eleven yard systems).
// Without it the same decline line describes both, and only one of them is a problem.
func unpricedOutOfReachHeavyYards(yards []KnownHeavyYard) int {
	n := 0
	for _, y := range yards {
		if y.WaypointSymbol == "" || y.PurchasePrice > 0 || y.Reachable {
			continue
		}
		n++
	}
	return n
}

// pricingErrandDeclineNarrative is the operator-facing sentence for each decline.
//
// The machine-readable reason rides in the metadata; this is the half that says what to DO about
// it, because the reason code alone ("no_eligible_carrier") does not distinguish "wait, this
// clears itself" from "the pool this engine draws on is empty and will stay empty".
func pricingErrandDeclineNarrative(reason pricingErrandDecline) string {
	switch reason {
	case pricingErrandCensusBlind:
		return "the owned-heavy census is unreadable, so whether a heavy is wanted at all is unknown"
	case pricingErrandAtHeavyCap:
		return "the fleet already holds its heavy cap, so no ask needs reading"
	case pricingErrandNoYardKnown:
		return "no heavy-selling yard is in the catalogue yet — the free presence-less sweep keeps looking and costs no hull"
	case pricingErrandNothingInReach:
		return "every heavy yard we know is already priced, or is unpriced and outside the gate reach bound"
	case pricingErrandAlreadyInFlight:
		return "a hull is already standing at, or flying to, an unpriced heavy yard"
	case pricingErrandNoCarrier:
		return "no spare parked sensing probe is free to fly it — every probe is manning a scout post, working, or already in transit"
	}
	return "unclassified"
}

// logPricingErrandDecline states WHY no hull went, every single tick that none does.
//
// THE SILENCE WAS THE DEFECT BEHIND THE DEFECT (sp-gmfvw). The errand declined with no line at all
// for an entire era while thirteen known heavy yards sat unpriced, so "running and waiting" read
// exactly like "never wired" and only production-log archaeology could tell them apart. The line is
// part of the mechanism, not decoration.
//
// Every count is recomputed from the tick's own inputs rather than threaded out of the planner:
// they are pure functions of data already in hand, and a decline path that carries state is a
// decline path that can disagree with the decision it is describing.
func logPricingErrandDecline(
	ctx context.Context,
	containerID string,
	reason pricingErrandDecline,
	yards []KnownHeavyYard,
	hulls []PricingErrandHull,
) {
	unpriced := unpricedHeavyYards(yards)
	outOfReach := unpricedOutOfReachHeavyYards(yards)
	inFlight := errandsInFlight(unpriced, hulls)
	_, haveCarrier := pricingErrandCarrier(hulls)
	nearest := "none"
	if len(unpriced) > 0 {
		nearest = unpriced[0].WaypointSymbol
	}
	common.LoggerFromContext(ctx).Log("INFO", fmt.Sprintf(
		"Heavy pricing errand: NO HULL SENT this tick [%s] — %s "+
			"(%d heavy yard(s) known, %d unpriced and in reach (nearest %s), %d unpriced but out of reach, "+
			"%d errand(s) already in flight, spare-probe carrier available=%v)",
		reason, pricingErrandDeclineNarrative(reason),
		len(yards), len(unpriced), nearest, outOfReach, inFlight, haveCarrier),
		map[string]interface{}{
			"action": "autosizer_heavy_pricing_declined", "container_id": containerID,
			"reason": string(reason), "known_yards": len(yards), "unpriced_yards": len(unpriced),
			"unpriced_out_of_reach": outOfReach, "in_flight": inFlight, "carrier_available": haveCarrier,
		})
}

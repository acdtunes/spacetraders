package commands

import (
	"context"
	"fmt"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// THE HEAVY-YARD PRICING ERRAND.
//
// A SpaceTraders shipyard prices its hulls only while a ship stands at the waypoint. A presence-less
// read still returns the ship-type CATALOGUE, persisted at purchase_price 0 — "this yard sells a
// heavy, at an ask nobody has read". Every money-facing consumer correctly ignores that row, so a
// fleet can KNOW where to buy a heavy and still never reserve, accumulate, or buy one. That is the
// state this errand exists to leave.
//
// When the price cannot be read it SENDS A HULL so the next scan reads it. This tick still buys
// nothing, so no money guard is weakened (RULINGS #4) — it makes the price readable, it does not
// bypass the price.
//
// Nothing is stored (RULINGS #2). Every question it asks is DERIVED from durable ship rows each
// tick — a hull's location IS its destination while in transit (Ship.StartTransit) — so there is no
// cursor to go stale, re-fire on restart, or leak across eras.

// heavyPricingErrandsInFlight bounds the errand to ONE hull at a time, fleet-wide.
//
// A const, not a knob (RULINGS #5). One hull is all it takes to price a yard, the catalogue is
// walked nearest-first so the most useful one goes first, and every extra simultaneous errand takes
// another spare probe off station to buy information we are about to get anyway.
const heavyPricingErrandsInFlight = 1

// heavyPricingErrandFleet is the dedication a hull MUST carry to fly a pricing errand: the PARKED
// SENSING pool — the fleet's spare hulls.
//
// IT MUST NOT NAME THE TRADE POOL. A trade hull is either flying a lane or docked mid-tour, so a
// trade-scoped allowlist is empty essentially always and the errand declines forever.
//
// A probe is the right carrier because PRICING A YARD NEEDS PRESENCE, NOT CARGO: the hull reads, it
// does not carry. One taken off station for a hop costs a market's freshness, where a trade hull
// taken off a lane costs income.
//
// It stays an ALLOWLIST of exactly one tag, so a tag invented tomorrow is refused by default rather
// than silently admitted — and it is the CROSS-PACKAGE CONST, not a re-spelling: allowlist and
// tag-writer must agree exactly or the eligible set silently empties.
const heavyPricingErrandFleet = parkedsensing.SensingParkedFleetTag

// KnownHeavyYard is one KNOWN heavy-selling yard as the errand policy sees it — PRICED OR NOT.
//
// PurchasePrice 0 is the whole point of this type: it is the availability-only catalogue row that
// every priced read filters away, and it is exactly the row that needs a hull sent to it.
// Hops/Reachable measure from the nearest system we hold ANY hull in, so they are a LOWER BOUND on
// the distance from the hull that will fly and a prefilter only: Reachable=false excludes the yard
// outright, Reachable=true still has to be proved from the chosen carrier (nearestRoutablePair).
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
	// hold predicate here would exclude exactly the pool the errand exists to use. It is
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

// HeavyPricingErrandPort is who could go, sending them, reading a yard we already occupy, and the
// reach that decides where. SendToYard NAVIGATES only: presence in orbit is enough for a listing to
// price and the purchase path docks on its own account, so the errand never docks, quotes or spends.
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
	// PriceYardInPlace reads the shipyard at waypointSymbol WHERE A HULL OF OURS ALREADY STANDS. It
	// is the errand's COMPLETION step: a yard is otherwise read only on ARRIVAL, and a hull that
	// arrived long ago produces no further arrivals, so presence bought with a trip goes unspent.
	// Implementations must make a METERED, DENIABLE read — discovery, not a money guard, so a
	// decline is a WAIT the next tick retries and never a stale price handed to a spender.
	PriceYardInPlace(ctx context.Context, playerID int, waypointSymbol string) error
	// HopsFrom reports the gate-jump distance from ONE origin system to each of toSystems, under
	// the bound the NAVIGATOR itself enforces; a target ABSENT from the map is not a candidate.
	//
	// THE ORIGIN IS THE POINT. The yard catalogue measures reach from the nearest system the FLEET
	// stands in — a lower bound on every particular hull's — but the flight starts from the
	// CARRIER's system and is refused there.
	HopsFrom(ctx context.Context, fromSystem string, toSystems []string) (map[string]int, error)
}

// heavyPricingErrand is one errand decision: this hull, that yard, and whether anything flies.
type heavyPricingErrand struct {
	Ship string
	Yard string
	// InPlace is set when Ship ALREADY STANDS at Yard, so the yard is READ where it sits: no flight,
	// no hull off station, nothing taken. The dispatching errand exists only to MANUFACTURE the
	// presence this case already has.
	InPlace bool
}

// carrierReach answers the navigator's own question for ONE origin system: how many gate jumps to
// each target, within the bound the executor enforces; an absent target has no nameable route. A
// FUNCTION rather than a port, so the decision stays deterministic in its inputs.
type carrierReach func(fromSystem string, toSystems []string) (map[string]int, error)

// pricingErrandDecline NAMES why a tick sent no hull.
//
// Without a named reason "running and waiting" and "never wired" are indistinguishable from
// outside. Every value below is a WAIT, not a failure — but a wait an operator acts on differently
// depending on which one it is, which is the whole point of naming it.
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
	// pricingErrandAlreadyInFlight — a hull is FLYING to an unpriced heavy yard. A hull that has
	// already ARRIVED is not this: it is the free errand, and it leaves through the dispatch path.
	pricingErrandAlreadyInFlight pricingErrandDecline = "errand_already_in_flight"
	// pricingErrandNoRoutableYard — carriers are free and yards need pricing, but none can be routed
	// to within the bound the NAVIGATOR enforces. pricingErrandNothingInReach is the same verdict
	// measured fleet-wide; this one says the yards are in the FLEET's reach and out of THIS hull's.
	pricingErrandNoRoutableYard pricingErrandDecline = "no_yard_routable_from_a_carrier"
	// pricingErrandReachUnreadable — the stored gate adjacency could not be read, so no reach can be
	// proved. Fails toward moving nothing, and is named apart from a genuine absence of routes.
	pricingErrandReachUnreadable pricingErrandDecline = "carrier_reach_unreadable"
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
func planHeavyPricingErrand(yards []KnownHeavyYard, hulls []PricingErrandHull, reach carrierReach) (heavyPricingErrand, pricingErrandDecline) {
	if len(yards) == 0 {
		return heavyPricingErrand{}, pricingErrandNoYardKnown
	}
	unpriced := unpricedHeavyYards(yards)
	if len(unpriced) == 0 {
		return heavyPricingErrand{}, pricingErrandNothingInReach
	}
	// THE FREE ERRAND FIRST, before any question about carriers or routes. A hull already standing
	// on an unpriced yard IS the presence a dispatch spends a trip to manufacture, and reading the
	// yard where it sits costs no flight, no hull off station and no reach proof.
	if standing, ok := standingPricingErrand(unpriced, hulls); ok {
		return standing, pricingErrandDispatch
	}
	if errandsInFlight(unpriced, hulls) >= heavyPricingErrandsInFlight {
		return heavyPricingErrand{}, pricingErrandAlreadyInFlight
	}
	carriers := pricingErrandCarriers(hulls)
	if len(carriers) == 0 {
		return heavyPricingErrand{}, pricingErrandNoCarrier
	}
	errand, err := nearestRoutablePair(unpriced, carriers, reach)
	if err != nil {
		return heavyPricingErrand{}, pricingErrandReachUnreadable
	}
	if errand.Ship == "" {
		return heavyPricingErrand{}, pricingErrandNoRoutableYard
	}
	return errand, pricingErrandDispatch
}

// standingPricingErrand finds a hull ALREADY STANDING on an unpriced heavy yard — the errand that
// has nothing left to do but read.
//
// IT JUDGES NO CARRIER ELIGIBILITY, deliberately. Every predicate in pricingErrandCarriers protects
// a hull from being TAKEN — off its station, off a scout post, off a lane. This takes nothing: the
// only fact that matters is that something of ours stands at the waypoint, which is the whole
// precondition a shipyard puts on quoting. Demanding a spare probe here would refuse a free price
// because the hull standing there was earning.
//
// IN TRANSIT IS NOT STANDING. A hull's location is its DESTINATION while it flies
// (Ship.StartTransit), so location alone cannot tell them apart — and reading a yard nothing has
// reached yet persists another price-0 row and spends the read that would have worked on arrival.
//
// The unpriced list arrives nearest-first, so the choice is stable across ticks.
func standingPricingErrand(unpriced []KnownHeavyYard, hulls []PricingErrandHull) (heavyPricingErrand, bool) {
	standing := make(map[string]string, len(hulls))
	for _, h := range hulls {
		if h.Symbol == "" || h.Location == "" || h.InTransit {
			continue
		}
		// Ties break on ship symbol so the line names the same hull every tick.
		if cur, seen := standing[h.Location]; !seen || h.Symbol < cur {
			standing[h.Location] = h.Symbol
		}
	}
	for _, y := range unpriced {
		if ship, ok := standing[y.WaypointSymbol]; ok {
			return heavyPricingErrand{Ship: ship, Yard: y.WaypointSymbol, InPlace: true}, true
		}
	}
	return heavyPricingErrand{}, false
}

// nearestRoutablePair chooses the CARRIER AND THE YARD TOGETHER, ranked by the distance between
// them, and reports an empty errand when no pair can be routed at all.
//
// CHOOSING THEM SEPARATELY IS WRONG. The lowest-symbol spare probe and, independently, the
// catalogue's nearest unpriced yard answer two different questions and are paired by nothing: the
// catalogue's distance is a multi-source walk from every system the FLEET stands in, a lower bound
// on any PARTICULAR hull's and silent about the one that will fly.
//
// A yard whose distance from a carrier is ABSENT is not a candidate for it — absence means either
// beyond the bound or an adjacency never cached, and a hull about to be committed cannot tell them
// apart. That never strands a cold map: a yard in the carrier's OWN system still measures zero.
//
// Ties break on hops, then yard symbol, then ship symbol — a total order, so two consecutive ticks
// cannot each start a different errand and call the other one "already in flight".
func nearestRoutablePair(unpriced []KnownHeavyYard, carriers []PricingErrandHull, reach carrierReach) (heavyPricingErrand, error) {
	if reach == nil {
		return heavyPricingErrand{}, fmt.Errorf("no reach oracle: a carrier's route to a heavy yard cannot be proved")
	}
	targets := distinctYardSystems(unpriced)
	// One walk per distinct ORIGIN, not per carrier: probes sharing a system share an answer, and
	// asking twice would only cost a second store read to learn the same thing.
	byOrigin := make(map[string]map[string]int, len(carriers))
	best := heavyPricingErrand{}
	bestHops := -1
	for _, c := range carriers {
		from := systemOf(c.Location)
		if from == "" {
			continue
		}
		hops, seen := byOrigin[from]
		if !seen {
			answer, err := reach(from, targets)
			if err != nil {
				return heavyPricingErrand{}, err
			}
			hops = answer
			byOrigin[from] = answer
		}
		for _, y := range unpriced {
			d, routable := hops[y.SystemSymbol]
			if !routable {
				continue
			}
			if bestHops >= 0 && !closerPair(d, y.WaypointSymbol, c.Symbol, bestHops, best) {
				continue
			}
			best, bestHops = heavyPricingErrand{Ship: c.Symbol, Yard: y.WaypointSymbol}, d
		}
	}
	return best, nil
}

// closerPair is the total order over candidate pairs: fewer hops, then yard symbol, then ship.
func closerPair(hops int, yard, ship string, bestHops int, best heavyPricingErrand) bool {
	if hops != bestHops {
		return hops < bestHops
	}
	if yard != best.Yard {
		return yard < best.Yard
	}
	return ship < best.Ship
}

// distinctYardSystems is the target set one reach walk is asked about: deduplicated, first-seen order.
func distinctYardSystems(yards []KnownHeavyYard) []string {
	seen := make(map[string]struct{}, len(yards))
	out := make([]string, 0, len(yards))
	for _, y := range yards {
		if y.SystemSymbol == "" {
			continue
		}
		if _, ok := seen[y.SystemSymbol]; ok {
			continue
		}
		seen[y.SystemSymbol] = struct{}{}
		out = append(out, y.SystemSymbol)
	}
	return out
}

// systemOf takes the shared waypoint→system projection rather than re-deriving one: a second
// parsing rule is a second opinion about whether a hull can fly.
func systemOf(waypointSymbol string) string {
	if waypointSymbol == "" {
		return ""
	}
	return shared.ExtractSystemSymbol(waypointSymbol)
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

// errandsInFlight counts the hulls FLYING to an unpriced heavy yard — the errands whose outcome is
// still in the air.
//
// STANDING DELIBERATELY DOES NOT COUNT. Reading it as "the same errand, one step further along" is
// the wedge: nothing COMPLETES that errand, because a yard is read on ARRIVAL and a hull that
// arrived long ago produces no further arrivals — so the wait has no end and one parked probe holds
// the mechanism shut while every known heavy yard sits at 0. The convoy this guards against stays
// impossible for a better reason: a standing hull is answered BEFORE the count is taken
// (standingPricingErrand), so the arrival window reads rather than dispatching.
func errandsInFlight(unpriced []KnownHeavyYard, hulls []PricingErrandHull) int {
	targets := make(map[string]struct{}, len(unpriced))
	for _, y := range unpriced {
		targets[y.WaypointSymbol] = struct{}{}
	}
	n := 0
	for _, h := range hulls {
		if h.Location == "" || !h.InTransit {
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
// FOUR CONJUNCTIVE PREDICATES, each load-bearing:
//
//   - Fleet == heavyPricingErrandFleet — the allowlist. Selecting by "not busy" instead would admit
//     every tag nobody has thought of yet.
//   - !MannedScoutPost — the standing-owner rule. The probe pool is SHARED with the scout
//     coordinator, so the tag alone cannot separate a spare hull from a working one: a hull a live
//     post NAMES is that post's, even in the idle gap between two tours.
//   - Idle — a hull mid-tour is working; the errand is worth a spare hull, never a working one.
//   - !InTransit — an idle hull still flying was sent somewhere; re-routing strands whatever sent it.
//
// CARGO CAPACITY IS DELIBERATELY NOT CONSULTED: every probe has a zero hold, so a hold predicate
// refuses every carrier this errand wants.
//
// IT RETURNS EVERY ELIGIBLE CARRIER, not the best one — which probe should fly is a property of the
// PAIR it forms with a yard, and no yard is in view here. Sorted, so that pairing has a stable
// tiebreak.
func pricingErrandCarriers(hulls []PricingErrandHull) []PricingErrandHull {
	out := make([]PricingErrandHull, 0, len(hulls))
	for _, h := range hulls {
		if h.Symbol == "" || h.Fleet != heavyPricingErrandFleet || h.MannedScoutPost {
			continue
		}
		if !h.Idle || h.InTransit {
			continue
		}
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Symbol < out[j].Symbol })
	return out
}

// pricingErrandCarrier reports whether ANY hull may be taken, for the decline line's evidence.
// It is the same predicate, asked as a yes/no — never a second copy of it.
func pricingErrandCarrier(hulls []PricingErrandHull) (string, bool) {
	carriers := pricingErrandCarriers(hulls)
	if len(carriers) == 0 {
		return "", false
	}
	return carriers[0].Symbol, true
}

// runHeavyPricingErrand is the tick step: read the catalogue, read the fleet, and send at most one
// hull to at most one unpriced heavy yard.
//
// IT HANGS OFF THE FLEET'S HEAVY BUYER, and off exactly one of them: a second driver is a second
// opinion about the one-hull-at-a-time bound, and two coordinators each believing no errand is in
// flight is how a bound of one becomes a convoy.
//
// GATED ON WANTING A HEAVY AT ALL. An unreadable census, or a fleet already at its cap, sends
// nothing — the errand costs a market's freshness while its probe is away, and buying information
// about a purchase that cannot happen is pure loss. It mirrors the reservation's own gate, which is
// why both stand down together at the cap.
//
// Every read failure is a logged WAIT, never fatal: nothing here spends money, so nothing here needs
// to fail closed against a spend.
func (h *RunFleetGrowthCoordinatorHandler) runHeavyPricingErrand(
	ctx context.Context,
	cmd *RunFleetGrowthCoordinatorCommand,
	cfg growthRunConfig,
	in growthTickInputs,
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

	reach := func(fromSystem string, toSystems []string) (map[string]int, error) {
		return port.HopsFrom(ctx, fromSystem, toSystems)
	}
	errand, decline := planHeavyPricingErrand(yards, hulls, reach)
	if decline != pricingErrandDispatch {
		logPricingErrandDecline(ctx, containerID, decline, yards, hulls)
		return
	}

	// THE FREE ERRAND: the presence a dispatch spends a trip to buy already exists, so the yard is
	// read where the hull stands. Nothing flies, so there is nothing to fail halfway.
	if errand.InPlace {
		if err := port.PriceYardInPlace(ctx, playerID, errand.Yard); err != nil {
			logger.Log("WARN", fmt.Sprintf("Heavy pricing errand: reading %s where %s already stands failed (%v) — retrying on a later tick", errand.Yard, errand.Ship, err), map[string]interface{}{
				"action": "autosizer_heavy_pricing_in_place_read_failed", "container_id": containerID,
				"ship": errand.Ship, "yard": errand.Yard,
			})
			return
		}
		logger.Log("INFO", fmt.Sprintf("Heavy pricing errand: read %s where %s already stands — a presence we already had, so no hull moved and the ask is now readable", errand.Yard, errand.Ship), map[string]interface{}{
			"action": "autosizer_heavy_pricing_read_in_place", "container_id": containerID,
			"ship": errand.Ship, "yard": errand.Yard,
		})
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
// A silent decline makes "running and waiting" read exactly like "never wired", so the line is part
// of the mechanism, not decoration.
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

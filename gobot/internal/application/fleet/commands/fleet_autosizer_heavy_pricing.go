package commands

import (
	"context"
	"fmt"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
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
// every additional simultaneous errand would take another working trade hull off its lane to buy
// information we are about to get anyway. Raising it could only ever trade income for latency.
const heavyPricingErrandsInFlight = 1

// heavyPricingErrandFleet is the dedication a hull MUST carry to be eligible to fly a pricing
// errand: the trade pool — the very fleet the heavy is being bought for.
//
// It is an ALLOWLIST of exactly one tag, deliberately, and it is the mechanism that makes the
// standing "never re-task a parked sensing probe" rule structural rather than remembered. A parked
// sensing probe carries parkedsensing.SensingParkedFleetTag; an undedicated hull carries ""; a
// contract hauler carries "contract". None of them equals this, so none of them can be selected —
// and a fleet tag invented tomorrow is refused by default rather than silently admitted.
//
// It is NOT written here as a cross-package import on purpose: the sensing tag is not this
// package's to depend on, and an allowlist that names only what IS eligible cannot rot when a new
// tag appears. It mirrors the trade coordinator's own pool predicate (tradeFleet).
const heavyPricingErrandFleet = "trade"

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
	Fleet         string
	Location      string
	Idle          bool
	InTransit     bool
	CargoCapacity int
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
	ErrandHulls(ctx context.Context, playerID int) ([]PricingErrandHull, error)
	// SendToYard flies one hull to a yard waypoint so its listing prices on the next scan.
	SendToYard(ctx context.Context, playerID int, shipSymbol, waypointSymbol string) error
}

// heavyPricingErrand is one dispatch decision: this hull, to that yard.
type heavyPricingErrand struct {
	Ship string
	Yard string
}

// planHeavyPricingErrand decides the tick's errand, if there is one. PURE — no clock, no I/O.
//
// It answers "no errand" for five distinct reasons, and every one of them is a WAIT rather than a
// failure: nothing is known, everything known is already priced, an errand is already under way,
// no eligible carrier is free, or the only carriers are hulls we refuse to take. A wait costs
// nothing and is retried next tick; taking a hull we should not have taken is not recoverable.
func planHeavyPricingErrand(yards []KnownHeavyYard, hulls []PricingErrandHull) (heavyPricingErrand, bool) {
	unpriced := unpricedHeavyYards(yards)
	if len(unpriced) == 0 {
		return heavyPricingErrand{}, false
	}
	if errandsInFlight(unpriced, hulls) >= heavyPricingErrandsInFlight {
		return heavyPricingErrand{}, false
	}
	carrier, ok := pricingErrandCarrier(hulls)
	if !ok {
		return heavyPricingErrand{}, false
	}
	return heavyPricingErrand{Ship: carrier, Yard: unpriced[0].WaypointSymbol}, true
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

// pricingErrandCarrier picks the hull to send, or reports that nothing may move this tick.
//
// FOUR CONJUNCTIVE PREDICATES, and each one is load-bearing:
//
//   - Fleet == heavyPricingErrandFleet — the allowlist. This is what makes it IMPOSSIBLE to take a
//     parked sensing probe, an undedicated hull another controller is about to claim, or a
//     contract hauler mid-workstream. Selecting by "not sensing" instead would admit every tag
//     nobody has thought of yet.
//   - CargoCapacity > 0 — the second, independent lock on the same door. A probe hull has a
//     zero hold, so even a MIS-TAGGED probe (a tag write that landed on the wrong hull, the exact
//     shape the sensing engine documents as recoverable) can never be selected. Two locks because
//     the cost of being wrong is a load-bearing sensing hull pulled off station, and the cost of
//     being over-strict is one tick of waiting.
//   - Idle — a hull mid-tour is earning; the errand is worth a spare hull, never a working one.
//   - !InTransit — an idle hull still flying has a destination it was sent to; re-routing it
//     would strand whatever sent it.
//
// Ties break on ship symbol so the same fleet picks the same hull every tick — an arbitrary winner
// would let two ticks disagree about which hull is "the" errand and dispatch both.
func pricingErrandCarrier(hulls []PricingErrandHull) (string, bool) {
	chosen := ""
	for _, h := range hulls {
		if h.Symbol == "" || h.Fleet != heavyPricingErrandFleet || h.CargoCapacity <= 0 {
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
// sends nothing: the errand costs a working hull's time and an API read, and buying information
// about a purchase that cannot happen is pure loss. This mirrors the reservation's own gate, which
// is why both stand down together at the cap.
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
	if h.heavyYardCatalog == nil || h.heavyErrand == nil {
		return
	}
	logger := common.LoggerFromContext(ctx)

	// No heavy wanted ⇒ no price needed. Fails toward doing nothing on a blind census, which is
	// the direction that never moves a working hull on a signal we cannot see.
	if !in.heaviesOwnedOK || in.heaviesOwned >= cfg.HeavyCap {
		return
	}

	yards, err := h.heavyYardCatalog.KnownHeavyYards(ctx, cmd.PlayerID)
	if err != nil {
		logger.Log("WARN", fmt.Sprintf("Autosizer heavy pricing errand: the known-heavy-yard catalogue is unreadable (%v) — no hull sent this tick, so an unpriced heavy yard stays unpriced and no reservation can form", err), map[string]interface{}{
			"action": "autosizer_heavy_pricing_catalogue_unreadable", "container_id": cmd.ContainerID,
		})
		return
	}

	hulls, err := h.heavyErrand.ErrandHulls(ctx, cmd.PlayerID)
	if err != nil {
		logger.Log("WARN", fmt.Sprintf("Autosizer heavy pricing errand: the fleet is unreadable (%v) — no hull sent this tick", err), map[string]interface{}{
			"action": "autosizer_heavy_pricing_fleet_unreadable", "container_id": cmd.ContainerID,
		})
		return
	}

	errand, ok := planHeavyPricingErrand(yards, hulls)
	if !ok {
		h.logNoPricingErrand(ctx, cmd, yards, hulls)
		return
	}

	if err := h.heavyErrand.SendToYard(ctx, cmd.PlayerID, errand.Ship, errand.Yard); err != nil {
		logger.Log("WARN", fmt.Sprintf("Autosizer heavy pricing errand: sending %s to %s failed (%v) — retrying on a later tick", errand.Ship, errand.Yard, err), map[string]interface{}{
			"action": "autosizer_heavy_pricing_dispatch_failed", "container_id": cmd.ContainerID,
			"ship": errand.Ship, "yard": errand.Yard,
		})
		return
	}

	logger.Log("INFO", fmt.Sprintf("Autosizer heavy pricing errand: sent %s to %s so the yard's presence-gated ask becomes readable — no buy this tick; the reservation forms once the price lands", errand.Ship, errand.Yard), map[string]interface{}{
		"action": "autosizer_heavy_pricing_dispatched", "container_id": cmd.ContainerID,
		"ship": errand.Ship, "yard": errand.Yard,
	})
}

// logNoPricingErrand names WHY no hull went, because "unpriced heavy yards exist and nothing is
// moving" is otherwise indistinguishable from "the feature is not wired". The unpriced count and
// the eligible-carrier count are both reported: together they separate "already under way" from
// "nothing free to send", which are the two waits an operator would act on differently.
func (h *RunFleetAutosizerCoordinatorHandler) logNoPricingErrand(
	ctx context.Context,
	cmd *RunFleetAutosizerCoordinatorCommand,
	yards []KnownHeavyYard,
	hulls []PricingErrandHull,
) {
	unpriced := unpricedHeavyYards(yards)
	if len(unpriced) == 0 {
		return // every known heavy yard is priced: the errand has nothing to do, which is success.
	}
	inFlight := errandsInFlight(unpriced, hulls)
	_, haveCarrier := pricingErrandCarrier(hulls)
	common.LoggerFromContext(ctx).Log("INFO", fmt.Sprintf(
		"Autosizer heavy pricing errand: %d known heavy yard(s) still unpriced (nearest %s), %d errand(s) already in flight, eligible idle trade carrier available=%v — no hull sent this tick",
		len(unpriced), unpriced[0].WaypointSymbol, inFlight, haveCarrier), map[string]interface{}{
		"action": "autosizer_heavy_pricing_waiting", "container_id": cmd.ContainerID,
		"unpriced_yards": len(unpriced), "in_flight": inFlight, "carrier_available": haveCarrier,
	})
}

package queries

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
)

// THE HEAVY TARGET — one question, one answer, two consumers.
//
// The heavy reservation's most important invariant is that the fleet autosizer (which SPENDS the
// accumulation) and the sensing buy-floor (which WITHHOLDS it) never disagree about the number.
// common.HeavyReserve already pins the ARITHMETIC to one definition; this pins its PRICE TERM to
// one implementation, because two callers each deriving "the price of the next heavy" from the
// same tables is exactly how a reservation silently drifts.
//
// WHAT CHANGED AND WHY (sp-fwk8z): the price term used to be the CHEAPEST KNOWN priced heavy yard
// across the whole map. That is the wrong number, in the unsafe direction. The purchase path buys
// at the NEAREST reachable yard, not the cheapest one, so reserving the cheapest under-reserves
// whenever the two differ — treasury tops out below the price we will actually be asked, probe
// buying resumes too early, and the heavy is never bought. That is the very failure this feature
// exists to end. Reserving the TARGET's price holds back at least what we will pay, which is the
// stricter direction and the only one that can complete a purchase (RULINGS #4: a money guard may
// only get stricter).
//
// Nothing is stored (RULINGS #2). The target is recomputed from durable tables every tick, so a
// newly-discovered nearer yard IS the target on the next tick and the reservation moves with it —
// the design's re-targeting property falls out rather than being written.

// HeavyTarget is the yard the heavy purchase path would target this tick, and its ask.
//
// CapabilityOpen and Priced are DELIBERATELY SEPARATE, and the gap between them is where this
// feature used to die. CapabilityOpen answers "does any known yard sell a heavy at all" — it
// counts AVAILABILITY-ONLY rows (purchase_price 0), the catalogue a presence-less shipyard read
// leaves behind. Priced answers "can a money guard act on it". A yard can be known for days with
// CapabilityOpen true and Priced false, and that state — capability open, nothing reservable — is
// precisely what the pricing errand exists to resolve. Collapsing the two would either hide the
// yard from the errand or let a zero reach a price guard.
type HeavyTarget struct {
	CapabilityOpen bool
	Priced         bool
	SystemSymbol   string
	WaypointSymbol string
	ShipType       string
	PurchasePrice  int64
}

// heavyAvailabilityReader answers the availability half — the one read with NO price predicate.
// Satisfied by shipyard.InventoryRepository.
type heavyAvailabilityReader interface {
	HasAnyOfTypes(ctx context.Context, playerID int, shipTypes []string) (bool, error)
}

// heavyYardRanker is the priced, reach-bounded rank the purchase path itself consumes. Satisfied
// by *ReachableYardFinder.
type heavyYardRanker interface {
	NearestYardsSelling(ctx context.Context, playerID int, shipTypes []string, fromSystems []string) ([]YardCandidate, error)
}

// heavyFleetReader reports where the player's hulls stand — the reference set reach is measured
// from. Satisfied by navigation.ShipRepository.
type heavyFleetReader interface {
	FindAllByPlayer(ctx context.Context, playerID shared.PlayerID) ([]*navigation.Ship, error)
}

// HeavyTargetFinder resolves the heavy target for whoever asks. ONE instance serves both the
// autosizer and the sensing reserve, which is what makes the price term a single source rather
// than two near-copies.
type HeavyTargetFinder struct {
	availability heavyAvailabilityReader
	ranker       heavyYardRanker
	fleet        heavyFleetReader
	types        []string
}

// NewHeavyTargetFinder wires the finder. An empty type set falls back to the documented heavy
// classes, so a caller that lost its configuration can never silently resolve a target for an
// unrelated hull.
func NewHeavyTargetFinder(availability heavyAvailabilityReader, ranker heavyYardRanker, fleet heavyFleetReader, heavyTypes []string) *HeavyTargetFinder {
	if len(heavyTypes) == 0 {
		heavyTypes = shipyard.DefaultHeavyShipTypes
	}
	return &HeavyTargetFinder{availability: availability, ranker: ranker, fleet: fleet, types: heavyTypes}
}

// HeavyTarget resolves the yard the purchase path would target, and its ask.
//
// TYPE PREFERENCE MIRRORS THE BUY PATH, deliberately and exactly. resolveHullPrice asks the
// preferred heavy class FIRST and only falls back to the next class when the preferred one cannot
// be priced anywhere; this walks the same ordered class list and stops at the first class with a
// priced reachable yard. Ranking across the whole heavy set at once would happily return a nearer
// BULK_FREIGHTER while the buy targeted a HEAVY_FREIGHTER, and the reservation would then be held
// against a hull nobody is buying.
//
// WITHIN a class the rank is the purchase path's OWN rank — nearest first, the ask breaking ties
// inside a hop tier, the waypoint symbol breaking what is left so the order is total and stable.
// The tiebreak is kept rather than reduced to the symbol alone for two reasons: it is the rank the
// buy already uses, so the reserve tracks the yard the buy targets instead of a second opinion;
// and at IDENTICAL distance the cheaper yard is strictly better and keeps the proximity-premium
// guard clear, whereas a symbol-ordered pick could hand the guard a premium it must then block.
// "No need to find the cheapest" is honoured where it bites — the rank is NEAREST-first, never
// cheapest-first, and a far-away bargain never delays a purchase.
//
// AN ERROR IS AN ERROR, never a zero. Both consumers must be able to tell "no heavy yard is known"
// from "we could not look", because the first is a fact about the map and the second is a fact
// about the database, and only one of them means the reservation should stand down.
func (f *HeavyTargetFinder) HeavyTarget(ctx context.Context, playerID int) (HeavyTarget, error) {
	if f == nil || f.availability == nil || f.ranker == nil || f.fleet == nil {
		return HeavyTarget{}, fmt.Errorf("heavy target: the finder is not fully wired")
	}

	// AVAILABILITY FIRST, and it is NOT conditional on a price existing. HasAnyOfTypes carries no
	// price predicate, so an availability-only catalogue row opens the capability on its own —
	// which is what tells the pricing errand there is a yard worth flying to.
	open, err := f.availability.HasAnyOfTypes(ctx, playerID, f.types)
	if err != nil {
		return HeavyTarget{}, fmt.Errorf("heavy target: read heavy-yard availability: %w", err)
	}
	if !open {
		return HeavyTarget{}, nil
	}

	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return HeavyTarget{}, fmt.Errorf("heavy target: %w", err)
	}
	ships, err := f.fleet.FindAllByPlayer(ctx, pid)
	if err != nil {
		return HeavyTarget{}, fmt.Errorf("heavy target: read the fleet to measure yard reach: %w", err)
	}
	from := fleetSystems(ships)

	for _, shipType := range f.types {
		candidates, rerr := f.ranker.NearestYardsSelling(ctx, playerID, []string{shipType}, from)
		if rerr != nil {
			return HeavyTarget{}, fmt.Errorf("heavy target: rank yards selling %s: %w", shipType, rerr)
		}
		if len(candidates) == 0 {
			continue
		}
		best := candidates[0]
		return HeavyTarget{
			CapabilityOpen: true,
			Priced:         true,
			SystemSymbol:   best.SystemSymbol,
			WaypointSymbol: best.WaypointSymbol,
			ShipType:       best.ShipType,
			PurchasePrice:  int64(best.PurchasePrice),
		}, nil
	}

	// Known, but nothing priced or nothing reachable: the capability is open and there is no
	// number to reserve against. The pricing errand acts on exactly this state.
	return HeavyTarget{CapabilityOpen: true}, nil
}

// fleetSystems is the distinct set of systems the player holds a hull in — the reference set gate
// reach is measured from. A hull with no recorded location contributes nothing rather than an
// empty system symbol, which would make every yard read as 0 hops away.
func fleetSystems(ships []*navigation.Ship) []string {
	seen := make(map[string]struct{}, len(ships))
	out := make([]string, 0, len(ships))
	for _, sh := range ships {
		if sh == nil {
			continue
		}
		loc := sh.CurrentLocation()
		if loc == nil || loc.Symbol == "" {
			continue
		}
		system := shared.ExtractSystemSymbol(loc.Symbol)
		if system == "" {
			continue
		}
		if _, ok := seen[system]; ok {
			continue
		}
		seen[system] = struct{}{}
		out = append(out, system)
	}
	return out
}

package grpc

import (
	"cmp"
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipyardQueries "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/hullbuy"
	"github.com/andrescamacho/spacetraders-go/internal/domain/marketscan"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// shipyardWaypointLister is the narrow waypoint-repo slice the yard-price walk
// needs (SHIPYARD-trait waypoints per system). Satisfied by
// *persistence.GormWaypointRepository; an interface so the reader is testable
// against fakes at the port boundary.
type shipyardWaypointLister interface {
	ListBySystemWithTrait(ctx context.Context, systemSymbol, trait string) ([]*shared.Waypoint, error)
}

// scannedYardRanker is the nearest-reachable-yard signal off the persisted
// shipyard-inventory scans, ranked hops-then-price. Satisfied by
// *shipyardQueries.ReachableYardFinder.
type scannedYardRanker interface {
	NearestYardsSelling(ctx context.Context, playerID int, shipTypes []string, fromSystems []string) ([]shipyardQueries.YardCandidate, error)
}

type fleetYardPriceReader struct {
	med          common.Mediator
	shipRepo     navigation.ShipRepository
	waypointRepo shipyardWaypointLister
	// scannedYards is the heavy-yard fallback: when the live in-system
	// walk finds no priced listing (the branch that has ALWAYS failed closed),
	// the HEAVY class may open on a scout-scanned, gate-reachable yard. Nil-safe;
	// nil or an empty scan store keeps the historical fail-closed behavior.
	scannedYards scannedYardRanker
	posts        scoutPostRoster // the borrowed-probe restraint roster; unwired ⇒ no probe is borrowed
}

// PriceFor prices the ship type at the cheapest yard a CLAIMABLE hull of ours already stands on,
// alongside the cheapest ask KNOWN anywhere; readable=false leaves the price guard fail-closed.
//
// TWO NUMBERS, TWO QUESTIONS. price is the TARGET's ask, and restricting it to yards we occupy can
// RAISE it — correct, because the cheapest ask on the map under-reserves whenever the yard we can
// transact at is dearer. cheapest is guardPrice's DENOMINATOR, so it stays the cheapest ask known,
// occupied or not: narrowing it would raise the premium ceiling (RULINGS #4).
//
// PRESENCE GATES THE READ. A shipyard hands back its priced `ships` array only while a hull of ours
// is there; without one it answers with its catalogue alone. So walking an unoccupied counter spends
// an Earning request — no trait filter, no rescan window, never declined, blocking on a contended
// PriorityLow token behind every trade call — to learn a price the API was never going to give. The
// yards dropped here could not have moved either number, which is what keeps the denominator intact.
func (r *fleetYardPriceReader) PriceFor(ctx context.Context, playerID int, class hullbuy.HullClass, shipTypes []string, preferProximal bool) (map[string]hullbuy.YardAsk, error) {
	if r.waypointRepo == nil || len(shipTypes) == 0 {
		return unreadableAsks(shipTypes), nil
	}
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return unreadableAsks(shipTypes), nil
	}
	ships, err := r.shipRepo.FindAllByPlayer(ctx, pid)
	if err != nil {
		return unreadableAsks(shipTypes), err
	}
	walks := make(map[string]*typeWalk, len(shipTypes))
	for _, shipType := range shipTypes {
		walks[shipType] = &typeWalk{}
	}
	manned, rosterOK := buyerRoster(ctx, r.posts, playerID)
	buyers := purchaseBuyers(ships, manned, rosterOK)
	present := presentHullWaypoints(ships)
	candidates, scanErr := r.scannedYardCandidates(ctx, playerID, class, shipTypes, ships)
	byType := candidatesByType(candidates)

	for _, system := range distinctShipSystems(ships) {
		waypoints, werr := r.waypointRepo.ListBySystemWithTrait(ctx, system, "SHIPYARD")
		if werr != nil {
			continue
		}
		for _, wp := range waypoints {
			if wp == nil || !present[wp.Symbol] {
				continue
			}
			asks := r.asksAtShipyard(ctx, system, wp.Symbol, shipTypes, pid)
			_, standing := standingBuyerAt(buyers, wp.Symbol)
			for shipType, price := range asks {
				walks[shipType].observe(wp.Symbol, price, standing)
			}
		}
	}

	out := make(map[string]hullbuy.YardAsk, len(shipTypes))
	var failure error
	for _, shipType := range shipTypes {
		w := walks[shipType]
		cheapest := lowerAsk(w.cheapest, cheapestCandidateAsk(byType[shipType]))
		if w.targetYard != "" {
			out[shipType] = hullbuy.YardAsk{Price: w.target, Cheapest: cheapest, Yard: w.targetYard, Readable: true}
			continue
		}
		ask, err := r.scannedYardFallback(byType[shipType], buyers, cheapest, scanErr)
		out[shipType], failure = ask, cmp.Or(failure, err)
	}
	return out, failure
}

// typeWalk accumulates ONE candidate type's answer as the shared yard walk proceeds: the cheapest ask
// seen anywhere, and the cheapest at a yard a claimable hull is standing on.
type typeWalk struct {
	cheapest, target         int64
	cheapestYard, targetYard string
}

func (w *typeWalk) observe(waypoint string, price int64, standing bool) {
	if w.cheapestYard == "" || price < w.cheapest {
		w.cheapest, w.cheapestYard = price, waypoint
	}
	if !standing {
		return
	}
	if w.targetYard == "" || price < w.target {
		w.target, w.targetYard = price, waypoint
	}
}

// unreadableAsks is the fail-closed answer for every type asked, so a caller can index the result
// without distinguishing "no entry" from "no price".
func unreadableAsks(shipTypes []string) map[string]hullbuy.YardAsk {
	out := make(map[string]hullbuy.YardAsk, len(shipTypes))
	for _, shipType := range shipTypes {
		out[shipType] = hullbuy.YardAsk{}
	}
	return out
}

// scannedYardCandidates is the persisted scan surface for the types, or nil when it cannot be a
// source. HEAVY-ONLY, matching scannedYardFallback's gate: opening the light tier to remote yards is
// a policy change this seam does not make. A nil ranker, another class or a read error yields nil
// and PriceFor walks fully; the error travels beside it, so a miss is never read as an absence.
// Asked from the OCCUPIED SYSTEMS, which narrowing would shrink.
func (r *fleetYardPriceReader) scannedYardCandidates(ctx context.Context, playerID int, class hullbuy.HullClass, shipTypes []string, ships []*navigation.Ship) ([]shipyardQueries.YardCandidate, error) {
	if class != hullbuy.HullClassHeavy || r.scannedYards == nil {
		return nil, nil
	}
	candidates, err := r.scannedYards.NearestYardsSelling(ctx, playerID, shipTypes, distinctShipSystems(ships))
	if err != nil {
		return nil, err
	}
	return candidates, nil
}

// candidatesByType splits the one store read per type, so no type's reference or fallback can ever be
// fed by another's ask.
func candidatesByType(candidates []shipyardQueries.YardCandidate) map[string][]shipyardQueries.YardCandidate {
	out := make(map[string][]shipyardQueries.YardCandidate)
	for _, c := range candidates {
		out[c.ShipType] = append(out[c.ShipType], c)
	}
	return out
}

// cheapestCandidateAsk is the minimum PRICED ask; 0 means none. A zero reference makes guardPrice
// SKIP the premium test, so unpriced rows are dropped.
func cheapestCandidateAsk(candidates []shipyardQueries.YardCandidate) int64 {
	var cheapest int64
	for _, c := range candidates {
		if c.PurchasePrice <= 0 {
			continue
		}
		if cheapest == 0 || int64(c.PurchasePrice) < cheapest {
			cheapest = int64(c.PurchasePrice)
		}
	}
	return cheapest
}

// lowerAsk folds two asks with 0 meaning "unknown", so the reference can only ever move DOWN.
func lowerAsk(a, b int64) int64 {
	if a <= 0 {
		return b
	}
	if b <= 0 || a < b {
		return a
	}
	return b
}

// PriceForSystem finds the cheapest priced listing for the ship type at a SHIPYARD-trait waypoint
// scoped to a SINGLE named system (sp-fihvy: the depot stocker hull viability fix). Unlike PriceFor
// (which walks every system the player already operates in), the depot stocker buy-fallback must never
// price a hull outside the warehouse's home system — RULINGS #14 (the stocker is intra-system) — so this
// reader takes the home system as an explicit argument instead of discovering it from ship locations.
// No remote-yard/scanned fallback: a depot stocker hull is either bought at home or not bought at all.
func (r *fleetYardPriceReader) PriceForSystem(ctx context.Context, playerID int, shipType, system string) (int64, string, bool, error) {
	if r.waypointRepo == nil {
		return 0, "", false, nil
	}
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return 0, "", false, nil
	}
	waypoints, werr := r.waypointRepo.ListBySystemWithTrait(ctx, system, "SHIPYARD")
	if werr != nil {
		return 0, "", false, nil
	}
	var cheapest int64
	var cheapestYard string
	for _, wp := range waypoints {
		if wp == nil {
			continue
		}
		price, ok := r.priceAtShipyard(ctx, system, wp.Symbol, shipType, pid)
		if !ok {
			continue
		}
		if cheapestYard == "" || price < cheapest {
			cheapest, cheapestYard = price, wp.Symbol
		}
	}
	if cheapestYard == "" {
		return 0, "", false, nil
	}
	return cheapest, cheapestYard, true, nil
}

// scannedYardFallback opens the HEAVY price signal from the persisted shipyard scans when the live
// in-system walk found no priced listing WE ALREADY STAND ON. Price = the ask at the nearest OCCUPIED
// candidate, Cheapest = the reference PriceFor already folded; presence decides which is TARGETED.
// IT DOES NOT FETCH: candidates, cheapest and scanErr all arrive from PriceFor's single store read,
// so an empty list means either nothing to say or nothing readable — and scanErr says which.
func (r *fleetYardPriceReader) scannedYardFallback(candidates []shipyardQueries.YardCandidate, buyers []purchaseBuyer, cheapest int64, scanErr error) (hullbuy.YardAsk, error) {
	if len(candidates) == 0 {
		return hullbuy.YardAsk{}, scanErr // empty or unreadable scan surface → the price guard stays closed
	}
	for _, c := range candidates {
		if _, standing := standingBuyerAt(buyers, c.WaypointSymbol); !standing {
			continue
		}
		return hullbuy.YardAsk{Price: int64(c.PurchasePrice), Cheapest: cheapest, Yard: c.WaypointSymbol, Readable: true}, nil
	}
	return hullbuy.YardAsk{}, nil // known yards, none we stand on → stay fail-closed
}

// asksAtShipyard reads ONE yard and takes every requested type's ask out of the same response, which
// is what makes a multi-type decision cost one request per counter rather than one per type.
func (r *fleetYardPriceReader) asksAtShipyard(ctx context.Context, system, waypoint string, shipTypes []string, pid shared.PlayerID) map[string]int64 {
	// The autosizer's fail-closed price guard reads this before it authorises a
	// hull buy, so it is Earning: metered, never denied (RULINGS #4).
	q := &shipyardQueries.GetShipyardListingsQuery{SystemSymbol: system, WaypointSymbol: waypoint, PlayerID: pid, Class: marketscan.Earning}
	resp, err := r.med.Send(ctx, q)
	if err != nil {
		return nil
	}
	out, ok := resp.(*shipyardQueries.GetShipyardListingsResponse)
	if !ok || out == nil {
		return nil
	}
	asks := make(map[string]int64, len(shipTypes))
	for _, shipType := range shipTypes {
		if listing, found := out.Shipyard.FindListingByType(shipType); found {
			asks[shipType] = int64(listing.PurchasePrice)
		}
	}
	return asks
}

// priceAtShipyard is the single-type read PriceForSystem needs — the home-scoped walk prices one hull
// class and has no candidate set to spread across.
func (r *fleetYardPriceReader) priceAtShipyard(ctx context.Context, system, waypoint, shipType string, pid shared.PlayerID) (int64, bool) {
	price, found := r.asksAtShipyard(ctx, system, waypoint, []string{shipType}, pid)[shipType]
	return price, found
}

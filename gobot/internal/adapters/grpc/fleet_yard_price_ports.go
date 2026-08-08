package grpc

import (
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

// PriceFor finds the priced listing the purchase path would TARGET for the ship type: the cheapest
// at a SHIPYARD-trait waypoint WHERE A CLAIMABLE HULL OF OURS ALREADY STANDS. When the live walk
// finds no such listing the HEAVY class falls back to the scout-scanned inventory under the same
// rule; readable=false (price guard fails closed) when neither surface knows a yard the fleet can
// both price and transact at.
//
// TWO NUMBERS, TWO QUESTIONS. price is the TARGET's ask, and restricting it to yards we occupy can
// RAISE it — correct, because the cheapest ask on the map under-reserves whenever the yard we can
// transact at is dearer. cheapest stays the cheapest ask KNOWN, occupied or not: narrowing it too
// would raise the premium ceiling (RULINGS #4) and hide the presence premium from its own metric.
func (r *fleetYardPriceReader) PriceFor(ctx context.Context, playerID int, class hullbuy.HullClass, shipType string, preferProximal bool) (int64, int64, string, bool, error) {
	if r.waypointRepo == nil {
		return 0, 0, "", false, nil
	}
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return 0, 0, "", false, nil
	}
	ships, err := r.shipRepo.FindAllByPlayer(ctx, pid)
	if err != nil {
		return 0, 0, "", false, nil
	}
	manned, rosterOK := buyerRoster(ctx, r.posts, playerID)
	buyers := purchaseBuyers(ships, manned, rosterOK)
	var cheapest, target int64
	var cheapestYard, targetYard string
	for _, system := range distinctShipSystems(ships) {
		waypoints, werr := r.waypointRepo.ListBySystemWithTrait(ctx, system, "SHIPYARD")
		if werr != nil {
			continue
		}
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
			if _, standing := standingBuyerAt(buyers, wp.Symbol); !standing {
				continue
			}
			if targetYard == "" || price < target {
				target, targetYard = price, wp.Symbol
			}
		}
	}
	if targetYard == "" {
		// The scanned surface knows yards outside this walk; the live cheapest goes with it so the
		// premium reference still spans everything known.
		return r.scannedYardFallback(ctx, playerID, class, shipType, ships, buyers, cheapest)
	}
	return target, cheapest, targetYard, true, nil
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
// in-system walk found no priced listing WE ALREADY STAND ON. Heavy ONLY: heavy hulls are the class
// whose yards are routinely out-of-system; lights keep buying in-system, so widening them to remote
// yards would be a buy-policy change this seam deliberately does not make. price = the ask at the
// nearest OCCUPIED candidate, cheapest = the true minimum across every candidate, folded with the
// live walk's own. Nothing readable, or nothing we stand on ⇒ readable=false, as it always was. The
// rank is still asked from the fleet's OCCUPIED SYSTEMS — narrowing it would shrink cheapest and so
// quietly raise the premium ceiling; presence is applied after, to which candidate is TARGETED.

func (r *fleetYardPriceReader) scannedYardFallback(ctx context.Context, playerID int, class hullbuy.HullClass, shipType string, ships []*navigation.Ship, buyers []purchaseBuyer, liveCheapest int64) (int64, int64, string, bool, error) {
	if class != hullbuy.HullClassHeavy || r.scannedYards == nil {
		return 0, 0, "", false, nil
	}
	candidates, err := r.scannedYards.NearestYardsSelling(ctx, playerID, []string{shipType}, distinctShipSystems(ships))
	if err != nil || len(candidates) == 0 {
		return 0, 0, "", false, nil // unreadable or empty scan surface → the price guard stays closed
	}
	cheapest := liveCheapest
	for _, c := range candidates {
		if cheapest == 0 || int64(c.PurchasePrice) < cheapest {
			cheapest = int64(c.PurchasePrice)
		}
	}
	for _, c := range candidates {
		if _, standing := standingBuyerAt(buyers, c.WaypointSymbol); !standing {
			continue
		}
		return int64(c.PurchasePrice), cheapest, c.WaypointSymbol, true, nil
	}
	return 0, 0, "", false, nil // known yards, none we stand on → stay fail-closed
}

func (r *fleetYardPriceReader) priceAtShipyard(ctx context.Context, system, waypoint, shipType string, pid shared.PlayerID) (int64, bool) {
	// The autosizer's fail-closed price guard reads this before it authorises a
	// hull buy, so it is Earning: metered, never denied (RULINGS #4).
	q := &shipyardQueries.GetShipyardListingsQuery{SystemSymbol: system, WaypointSymbol: waypoint, PlayerID: pid, Class: marketscan.Earning}
	resp, err := r.med.Send(ctx, q)
	if err != nil {
		return 0, false
	}
	out, ok := resp.(*shipyardQueries.GetShipyardListingsResponse)
	if !ok || out == nil {
		return 0, false
	}
	if listing, found := out.Shipyard.FindListingByType(shipType); found {
		return int64(listing.PurchasePrice), true
	}
	return 0, false
}

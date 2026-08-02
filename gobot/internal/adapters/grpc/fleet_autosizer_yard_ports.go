package grpc

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	fleetCmd "github.com/andrescamacho/spacetraders-go/internal/application/fleet/commands"
	shipyardQueries "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/queries"
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

type autosizerYardPriceReader struct {
	med          common.Mediator
	shipRepo     navigation.ShipRepository
	waypointRepo shipyardWaypointLister
	// scannedYards is the heavy-yard fallback: when the live in-system
	// walk finds no priced listing (the branch that has ALWAYS failed closed),
	// the HEAVY class may open on a scout-scanned, gate-reachable yard. Nil-safe;
	// nil or an empty scan store keeps the historical fail-closed behavior.
	scannedYards scannedYardRanker
}

// PriceFor finds the cheapest priced listing for the ship type at a SHIPYARD-trait waypoint in a
// system where the player operates. When that live in-system walk finds nothing, the HEAVY class
// falls back to the scout-scanned shipyard inventory: the nearest gate-reachable scanned
// yard, ranked hops-then-price — the availability signal the fail-closed heavy branch was designed
// to consume. Returns readable=false (price guard fails closed) when neither surface knows a priced
// yard. The demand-proximal preference is a later refinement (banked) — cheapest is returned now.
func (r *autosizerYardPriceReader) PriceFor(ctx context.Context, playerID int, class fleetCmd.HullClass, shipType string, preferProximal bool) (int64, int64, string, bool, error) {
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
	var cheapest int64
	var cheapestYard string
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
		}
	}
	if cheapestYard == "" {
		return r.scannedYardFallback(ctx, playerID, class, shipType, ships)
	}
	return cheapest, cheapest, cheapestYard, true, nil
}

// PriceForSystem finds the cheapest priced listing for the ship type at a SHIPYARD-trait waypoint
// scoped to a SINGLE named system (sp-fihvy: the depot stocker hull viability fix). Unlike PriceFor
// (which walks every system the player already operates in), the depot stocker buy-fallback must never
// price a hull outside the warehouse's home system — RULINGS #14 (the stocker is intra-system) — so this
// reader takes the home system as an explicit argument instead of discovering it from ship locations.
// No remote-yard/scanned fallback: a depot stocker hull is either bought at home or not bought at all.
func (r *autosizerYardPriceReader) PriceForSystem(ctx context.Context, playerID int, shipType, system string) (int64, string, bool, error) {
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

// scannedYardFallback opens the HEAVY price signal from the persisted shipyard
// scans when the live in-system walk found no priced listing. Heavy
// ONLY: heavy hulls are the class whose yards are routinely out-of-system (the
// branch that has always failed closed for lack of this signal); lights keep
// buying in-system, so widening them to remote yards would be a buy-policy
// change this seam deliberately does not make. price = the NEAREST candidate's
// ask (hops first, then price — the finder's rank), cheapest = the true minimum
// across reachable candidates so the premium guard judges the proximity premium
// honestly. No candidates / no ranker wired / rank read failure ⇒ readable=false:
// exactly the historical fail-closed behavior.
func (r *autosizerYardPriceReader) scannedYardFallback(ctx context.Context, playerID int, class fleetCmd.HullClass, shipType string, ships []*navigation.Ship) (int64, int64, string, bool, error) {
	if class != fleetCmd.HullClassHeavy || r.scannedYards == nil {
		return 0, 0, "", false, nil
	}
	candidates, err := r.scannedYards.NearestYardsSelling(ctx, playerID, []string{shipType}, distinctShipSystems(ships))
	if err != nil || len(candidates) == 0 {
		return 0, 0, "", false, nil // unreadable or empty scan surface → the price guard stays closed
	}
	nearest := candidates[0]
	cheapest := int64(nearest.PurchasePrice)
	for _, c := range candidates[1:] {
		if int64(c.PurchasePrice) < cheapest {
			cheapest = int64(c.PurchasePrice)
		}
	}
	return int64(nearest.PurchasePrice), cheapest, nearest.WaypointSymbol, true, nil
}

func (r *autosizerYardPriceReader) priceAtShipyard(ctx context.Context, system, waypoint, shipType string, pid shared.PlayerID) (int64, bool) {
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

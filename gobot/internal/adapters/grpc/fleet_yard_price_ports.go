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
// transact at is dearer. cheapest stays the cheapest ask KNOWN, occupied or not: it is guardPrice's
// DENOMINATOR, so narrowing it would raise the premium ceiling (RULINGS #4).
//
// PRESENCE GATES THE READ, NOT MERELY THE RANKING. Pricing every SHIPYARD-trait waypoint live and
// only then asking which one a buyer stands on charges the discovery fan-out to the buy decision:
// these are Earning reads (no trait filter, no rescan window, never declined) on a PriorityLow
// endpoint, so each BLOCKS on a contended rate-limit token behind every trade call.
// THE NARROWING IS PER-YARD AND CONDITIONAL ON COVERAGE, which is the safety argument: a live read is
// dropped only where the scan surface ALREADY KNOWS AN ASK FOR THAT YARD, so every yard that fed the
// reference still feeds it. A walked yard the store never priced is read live; no ranker, an
// unreadable or cold store, or a non-heavy class leaves the walk as it was. FRESHNESS is traded, not
// coverage — the TARGET's ask stays live, re-verified before credits move.
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
	// ONE store read doing two jobs: the premium reference, and the per-yard licence to skip a read.
	candidates := r.scannedYardCandidates(ctx, playerID, class, shipType, ships)
	scanned := cheapestCandidateAsk(candidates)
	covered := scannedAskByYard(candidates)

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
			_, standing := standingBuyerAt(buyers, wp.Symbol)
			// Nothing stands here, so this yard can never be the target, and the surface has its ask.
			if _, known := covered[wp.Symbol]; known && !standing {
				continue
			}
			price, ok := r.priceAtShipyard(ctx, system, wp.Symbol, shipType, pid)
			if !ok {
				continue
			}
			if cheapestYard == "" || price < cheapest {
				cheapest, cheapestYard = price, wp.Symbol
			}
			if !standing {
				continue
			}
			if targetYard == "" || price < target {
				target, targetYard = price, wp.Symbol
			}
		}
	}
	cheapest = lowerAsk(cheapest, scanned)
	if targetYard == "" {
		return r.scannedYardFallback(candidates, buyers, cheapest)
	}
	return target, cheapest, targetYard, true, nil
}

// scannedYardCandidates is the persisted scan surface for the type, or nil when it cannot be a
// source. HEAVY-ONLY, matching scannedYardFallback's gate: opening the light tier to remote yards is
// a policy change this seam does not make. Nil ranker, a read error or another class yields nil and
// PriceFor walks fully. Asked from the OCCUPIED SYSTEMS, which narrowing would shrink.
func (r *fleetYardPriceReader) scannedYardCandidates(ctx context.Context, playerID int, class hullbuy.HullClass, shipType string, ships []*navigation.Ship) []shipyardQueries.YardCandidate {
	if class != hullbuy.HullClassHeavy || r.scannedYards == nil {
		return nil
	}
	candidates, err := r.scannedYards.NearestYardsSelling(ctx, playerID, []string{shipType}, distinctShipSystems(ships))
	if err != nil {
		return nil
	}
	return candidates
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

// scannedAskByYard indexes which WAYPOINTS the scan surface can already price — the licence to skip
// a live read, since the yard contributes its stored ask. An unpriced row is not coverage.
func scannedAskByYard(candidates []shipyardQueries.YardCandidate) map[string]int64 {
	out := make(map[string]int64, len(candidates))
	for _, c := range candidates {
		if c.WaypointSymbol == "" || c.PurchasePrice <= 0 {
			continue
		}
		if prior, seen := out[c.WaypointSymbol]; seen && prior <= int64(c.PurchasePrice) {
			continue
		}
		out[c.WaypointSymbol] = int64(c.PurchasePrice)
	}
	return out
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
// in-system walk found no priced listing WE ALREADY STAND ON. price = the ask at the nearest OCCUPIED
// candidate, cheapest = the reference PriceFor already folded. Nothing readable, or nothing we stand
// on ⇒ readable=false, as it always was; presence decides which candidate is TARGETED.
// IT DOES NOT FETCH: candidates and cheapest arrive from PriceFor's single store read, the same one
// that decides whether the walk may narrow, so an empty list means one thing — nothing to say.
func (r *fleetYardPriceReader) scannedYardFallback(candidates []shipyardQueries.YardCandidate, buyers []purchaseBuyer, cheapest int64) (int64, int64, string, bool, error) {
	if len(candidates) == 0 {
		return 0, 0, "", false, nil // unreadable or empty scan surface → the price guard stays closed
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

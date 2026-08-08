package grpc

import (
	"context"
	"sort"

	fleetCmd "github.com/andrescamacho/spacetraders-go/internal/application/fleet/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// THE BUYER/YARD PAIRING for hull purchases: which yard to buy at and which hull signs are ONE
// decision, and the yard is chosen from where we ALREADY STAND. A shipyard sells to any hull of ours
// docked at it and does not care which — a probe can sign for a heavy freighter — so a yard we
// occupy is one we can buy at now, with no navigation step that could fail. Presence qualifies a
// yard and price only ranks it; the match is WAYPOINT-EXACT, so same-system is still a flight.

// purchaseBuyer is one candidate signer plus the fact that decides how it may be taken: Borrowed
// marks a SENSING hull, used where it stands, over which the caller must hold a claim.
type purchaseBuyer struct {
	ship     *navigation.Ship
	Borrowed bool
}

// purchaseBuyers reports every hull that may sign a purchase, most-preferred first: the EXCLUSIVE
// purchasing hull, then any undedicated idle hull, then a SPARE parked-sensing probe borrowed where
// it stands. That third tier is what makes the engine able to fire at all — every hull standing on a
// priced heavy yard belongs to the sensing pool. BORROWING TAKES LESS THAN THE ERRAND ALREADY DOES:
// it FLIES a probe from this same pool to a yard, where this has one already there sign and hands it
// straight back. Nothing is poached and no tag changes — the claim is under the hull's OWN dedication, which is what ClaimShip
// admits (RULINGS #7). Spare is the errand's own predicate, CALLED rather than copied. AN UNREADABLE
// SCOUT-POST ROSTER REFUSES BORROWING ENTIRELY: "no post mans anything" would read as every probe
// being spare, the single reading that empties the sensing fleet — the other tiers stay live. NOT IN
// TRANSIT anywhere: a flying hull's location is its DESTINATION. Sorted within each tier, because
// row order is unspecified and this rule's readers must agree.
func purchaseBuyers(ships []*navigation.Ship, manned map[string]bool, rosterReadable bool) []purchaseBuyer {
	var dedicated, undedicated, borrowed []purchaseBuyer
	for _, s := range ships {
		if s == nil || !s.IsIdle() || s.IsInTransit() {
			continue
		}
		switch s.DedicatedFleet() {
		case navigation.PurchasingFleet:
			dedicated = append(dedicated, purchaseBuyer{ship: s})
		case "":
			undedicated = append(undedicated, purchaseBuyer{ship: s})
		default:
			if rosterReadable && spareSensingProbe(s, manned) {
				borrowed = append(borrowed, purchaseBuyer{ship: s, Borrowed: true})
			}
		}
	}
	bySymbol := func(hulls []purchaseBuyer) {
		sort.Slice(hulls, func(i, j int) bool { return hulls[i].ship.ShipSymbol() < hulls[j].ship.ShipSymbol() })
	}
	bySymbol(dedicated)
	bySymbol(undedicated)
	bySymbol(borrowed)
	return append(append(dedicated, undedicated...), borrowed...)
}

// spareSensingProbe projects a ship row onto the errand's facts and asks ITS question.
func spareSensingProbe(s *navigation.Ship, manned map[string]bool) bool {
	return fleetCmd.SpareSensingProbe(fleetCmd.PricingErrandHull{
		Symbol:          s.ShipSymbol(),
		Fleet:           s.DedicatedFleet(),
		Idle:            s.IsIdle(),
		InTransit:       s.IsInTransit(),
		MannedScoutPost: manned[s.ShipSymbol()],
	})
}

// standingBuyerAt names the hull that can sign here WITHOUT MOVING.
func standingBuyerAt(buyers []purchaseBuyer, waypointSymbol string) (purchaseBuyer, bool) {
	if waypointSymbol == "" {
		return purchaseBuyer{}, false
	}
	for _, b := range buyers {
		loc := b.ship.CurrentLocation()
		if loc != nil && loc.Symbol == waypointSymbol {
			return b, true
		}
	}
	return purchaseBuyer{}, false
}

// buyerRoster reads which hulls a live post names; unreadable or unwired ⇒ readable=false.
func buyerRoster(ctx context.Context, posts scoutPostRoster, playerID int) (map[string]bool, bool) {
	manned, err := mannedScoutPostHulls(ctx, posts, playerID)
	if err != nil {
		return nil, false
	}
	return manned, true
}

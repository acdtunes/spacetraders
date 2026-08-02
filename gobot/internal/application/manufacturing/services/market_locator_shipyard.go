package services

import (
	"context"
	"fmt"
	"strings"

	"github.com/andrescamacho/spacetraders-go/internal/domain/marketscan"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
)

// yardSource is what the locator needs from the fleet's shipyard scanner to price
// a hull without bypassing the shipyard-read budget: the persisted catalogue, a
// metered live read for yards the store has never seen, and the demand signal that
// keeps the yards we shop at priced. *ship.ShipyardScanner satisfies it.
type yardSource interface {
	OffersFor(ctx context.Context, playerID int, shipType string) ([]shipyard.ShipTypeAvailability, error)
	ReadShipyard(ctx context.Context, playerID uint, waypointSymbol string, class marketscan.Class) (*ports.ShipyardData, error)
	NoteDemand(shipType string)
}

// SetYardSource wires the metered shipyard reader the hull search runs through.
//
// A setter rather than a constructor argument because NewMarketLocator has two
// dozen test call sites that pass nil for everything but the market repository,
// and none of them exercise the hull path; widening the constructor would churn
// all of them to express nothing. The two production composition roots set it.
//
// WITHOUT IT THE HULL SEARCH FINDS NOTHING, deliberately. This search used to
// issue one live GET /shipyard per shipyard in the system, uncached and unmetered,
// from a path on the 30-second construction drain — one of the four bypasses that
// put shipyard reads at 44.7% of the server ceiling. Failing to find a
// hull is a recoverable planning outcome; quietly restoring an unmetered burst
// because a wiring was missed is not.
func (l *MarketLocator) SetYardSource(yards yardSource) {
	if yards == nil {
		return
	}
	l.yards = yards
}

// findShipyardSellingShip finds a shipyard that sells a specific ship type.
// Returns the shipyard with the lowest purchase price.
//
// STORE-FIRST: one store query covers every yard in the system, and only yards
// the store has no priced row for cost a request — a request the shipyard-read
// budget may decline, in which case that yard is simply not this tick's
// candidate. Fanning out instead — a live GET /shipyard per yard, off the store
// and outside the budget — is uncached and unmetered on a path reachable from
// the 30-second construction drain.
//
// A yard row with PurchasePrice 0 is catalogued but never priced (a presence-less
// GET sees what a counter sells but not what it charges), and it is NOT a free
// hull: it is skipped as a price, and it is exactly the row that makes the yard
// worth a live read.
func (l *MarketLocator) findShipyardSellingShip(
	ctx context.Context,
	shipType string,
	systemSymbol string,
	playerID int,
) (*MarketLocatorResult, error) {
	if l.yards == nil {
		return nil, fmt.Errorf("no shipyard reader wired; cannot search shipyards for %s", shipType)
	}

	shipyards, err := l.waypointRepo.ListBySystemWithTrait(ctx, systemSymbol, "SHIPYARD")
	if err != nil {
		return nil, fmt.Errorf("failed to find shipyards: %w", err)
	}

	if len(shipyards) == 0 {
		return nil, fmt.Errorf("no shipyards found in system %s", systemSymbol)
	}

	// Shopping for a hull IS the demand signal: it lifts every yard known to sell
	// this type to the top of the budget's rotation, so the next pass through here
	// finds more of them already priced.
	l.yards.NoteDemand(shipType)

	inSystem := make(map[string]bool, len(shipyards))
	for _, waypoint := range shipyards {
		inSystem[waypoint.Symbol] = true
	}

	var bestShipyard *MarketLocatorResult
	var bestPrice int
	consider := func(waypoint string, price int) {
		if price <= 0 {
			return
		}
		if bestShipyard == nil || price < bestPrice {
			bestPrice = price
			bestShipyard = &MarketLocatorResult{
				WaypointSymbol: waypoint,
				Activity:       "", // Shipyards don't have activity/supply metrics
				Supply:         "",
				Price:          price,
			}
		}
	}

	priced := make(map[string]bool, len(shipyards))
	rows, err := l.yards.OffersFor(ctx, playerID, shipType)
	if err == nil {
		for _, row := range rows {
			if !inSystem[row.WaypointSymbol] {
				continue
			}
			if row.PurchasePrice > 0 {
				priced[row.WaypointSymbol] = true
				consider(row.WaypointSymbol, row.PurchasePrice)
			}
		}
	}

	// Only the yards the store cannot price are worth a request.
	for _, waypoint := range shipyards {
		if priced[waypoint.Symbol] {
			continue
		}
		shipyardData, err := l.yards.ReadShipyard(ctx, uint(playerID), waypoint.Symbol, marketscan.Discretionary)
		if err != nil || shipyardData == nil {
			// Unreadable, or declined by the budget. Skip it this tick.
			continue
		}
		for _, listing := range shipyardData.Ships {
			if listing.Type == shipType {
				consider(waypoint.Symbol, listing.PurchasePrice)
			}
		}
	}

	if bestShipyard == nil {
		return nil, fmt.Errorf("no shipyard found selling %s in system %s", shipType, systemSymbol)
	}

	return bestShipyard, nil
}

// shipComponents are SHIP_-prefixed goods sold at regular markets, not hulls.
var shipComponents = map[string]bool{
	"SHIP_PARTS":   true,
	"SHIP_PLATING": true,
}

// isShipType returns true if the good is an actual ship type (not ship components like SHIP_PARTS).
// Ship types are manufactured at shipyards, while ship components are sold at regular markets.
func isShipType(good string) bool {
	if shipComponents[good] {
		return false
	}
	return strings.HasPrefix(good, "SHIP_")
}

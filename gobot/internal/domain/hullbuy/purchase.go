package hullbuy

import "context"

// BuyOrder is one approved hull purchase, dedicated to its class fleet at purchase time.
type BuyOrder struct {
	PlayerID      int
	Class         HullClass
	ShipType      string
	Yard          string
	ExpectedPrice int64
	// ContainerID owns any claim the buy holds over a borrowed signer. It must be a REAL containers row (the column carries a foreign key); empty legally means "may hold no claim".
	ContainerID string
}

// BuyResult reports the executed purchase. Dedicated says whether the hull was stamped with a
// fleet tag — false for the classes DedicatedFleet leaves untagged, not a failure.
type BuyResult struct {
	ShipSymbol string
	Price      int64
	Dedicated  bool
}

// YardPriceReader reads the purchase price for a ship type at the preferred yard (demand-proximal
// when preferProximal), plus the cheapest known yard ask (for the premium ceiling) and the yard
// waypoint the buy targets. readable=false ⇒ the price guards fail closed.
type YardPriceReader interface {
	PriceFor(ctx context.Context, playerID int, class HullClass, shipType string, preferProximal bool) (price, cheapest int64, yard string, readable bool, err error)
}

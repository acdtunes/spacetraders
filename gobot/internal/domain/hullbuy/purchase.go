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

// YardAsk is one candidate ship type's answer out of a yard walk: the ask at the yard the buy would
// target, the yard itself, and Readable=false ⇒ the price guards fail closed.
type YardAsk struct {
	Price int64
	// Cheapest is guardPrice's DENOMINATOR and spans every yard known, occupied or not: narrowing it
	// would raise the premium ceiling (RULINGS #4).
	Cheapest int64
	Yard     string
	Readable bool
}

// YardPriceReader prices candidate ship types at the yards the fleet can transact at, demand-proximal
// when preferProximal. Every requested type gets an entry, unreadable ones included.
//
// ONE CALL PRICES EVERY TYPE ASKED, because a shipyard read returns the whole listing array — a
// per-type call would re-walk the same counters and multiply a read a money guard is waiting on.
type YardPriceReader interface {
	PriceFor(ctx context.Context, playerID int, class HullClass, shipTypes []string, preferProximal bool) (map[string]YardAsk, error)
}

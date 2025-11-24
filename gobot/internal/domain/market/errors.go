package market

import "errors"

// Domain errors for market scouting operations

var (
	// ErrMarketNotFound is returned when a market cannot be found
	ErrMarketNotFound = errors.New("market not found")

	// ErrStaleMarketData is returned when market data is too old
	ErrStaleMarketData = errors.New("stale market data")

	// ErrInvalidTradeGood is returned when a trade good has invalid data
	ErrInvalidTradeGood = errors.New("invalid trade good")

	// ErrInvalidWaypointSymbol is returned when a waypoint symbol is empty or invalid
	ErrInvalidWaypointSymbol = errors.New("invalid waypoint symbol")

	// ErrInvalidGoodSymbol is returned when a good symbol is empty or invalid
	ErrInvalidGoodSymbol = errors.New("invalid good symbol")

	// ErrInvalidPlayerID is returned when a player ID is zero or invalid
	ErrInvalidPlayerID = errors.New("invalid player ID")

	// ErrInvalidPrice is returned when a price is negative
	ErrInvalidPrice = errors.New("invalid price")

	// ErrInvalidTradeVolume is returned when trade volume is negative
	ErrInvalidTradeVolume = errors.New("invalid trade volume")

	// ErrInvalidSupply is returned when a supply value is not in the valid set
	ErrInvalidSupply = errors.New("invalid supply value")

	// ErrInvalidActivity is returned when an activity value is not in the valid set
	ErrInvalidActivity = errors.New("invalid activity value")
)

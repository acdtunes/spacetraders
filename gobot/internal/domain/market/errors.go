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
)

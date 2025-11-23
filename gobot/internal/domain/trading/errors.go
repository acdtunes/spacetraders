package trading

import "errors"

var (
	// ErrNoOpportunitiesFound indicates no viable arbitrage opportunities were found
	ErrNoOpportunitiesFound = errors.New("no arbitrage opportunities found")

	// ErrInvalidMarginThreshold indicates the minimum margin threshold is invalid
	ErrInvalidMarginThreshold = errors.New("minimum margin threshold must be positive")

	// ErrInvalidCargoCapacity indicates the cargo capacity is invalid
	ErrInvalidCargoCapacity = errors.New("cargo capacity must be positive")

	// ErrMarketDataUnavailable indicates market data could not be retrieved
	ErrMarketDataUnavailable = errors.New("market data unavailable")

	// ErrInsufficientProfit indicates the opportunity does not meet minimum margin
	ErrInsufficientProfit = errors.New("profit margin below threshold")
)

package services

import "github.com/andrescamacho/spacetraders-go/internal/domain/market"

const (
	supplyScarce   = "SCARCE"
	supplyLimited  = "LIMITED"
	supplyModerate = "MODERATE"
	supplyHigh     = "HIGH"
	supplyAbundant = "ABUNDANT"

	activityWeak       = "WEAK"
	activityGrowing    = "GROWING"
	activityStrong     = "STRONG"
	activityRestricted = "RESTRICTED"
)

func isHighOrAbundant(supply string) bool {
	return supply == supplyHigh || supply == supplyAbundant
}

func supplyOrEmpty(tradeGood *market.TradeGood) string {
	if tradeGood.Supply() == nil {
		return ""
	}
	return *tradeGood.Supply()
}

func supplyOrModerate(tradeGood *market.TradeGood) string {
	if tradeGood.Supply() == nil {
		return supplyModerate
	}
	return *tradeGood.Supply()
}

func activityOrEmpty(tradeGood *market.TradeGood) string {
	if tradeGood.Activity() == nil {
		return ""
	}
	return *tradeGood.Activity()
}

func shortID(id string) string {
	return id[:8]
}

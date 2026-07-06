package services

import (
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

type activityWeightTable struct {
	weak       int
	growing    int
	strong     int
	restricted int
	unknown    int
}

func (w activityWeightTable) weightFor(activity string) int {
	switch market.ActivityLevel(activity) {
	case market.ActivityLevelWeak:
		return w.weak
	case market.ActivityLevelGrowing:
		return w.growing
	case market.ActivityLevelStrong:
		return w.strong
	case market.ActivityLevelRestricted:
		return w.restricted
	default:
		return w.unknown
	}
}

type supplyWeightTable struct {
	scarce   int
	limited  int
	moderate int
	high     int
	abundant int
	unknown  int
}

func (w supplyWeightTable) weightFor(supply string) int {
	switch manufacturing.SupplyLevel(supply) {
	case manufacturing.SupplyLevelScarce:
		return w.scarce
	case manufacturing.SupplyLevelLimited:
		return w.limited
	case manufacturing.SupplyLevelModerate:
		return w.moderate
	case manufacturing.SupplyLevelHigh:
		return w.high
	case manufacturing.SupplyLevelAbundant:
		return w.abundant
	default:
		return w.unknown
	}
}

type MarketScoringPolicy struct {
	activity activityWeightTable
	supply   supplyWeightTable
}

func (p MarketScoringPolicy) Score(activity, supply string) int {
	return p.activity.weightFor(activity) + p.supply.weightFor(supply)
}

var sellMarketScoringPolicy = MarketScoringPolicy{
	activity: activityWeightTable{weak: 10, growing: 30, strong: 50, restricted: 5, unknown: 20},
	supply:   supplyWeightTable{scarce: 10, limited: 20, moderate: 30, high: 40, abundant: 50, unknown: 15},
}

const collectionAbundantFactoryBonus = 100

var collectionSellMarketActivityBonus = activityWeightTable{weak: 100, growing: 300, strong: 500, restricted: 0, unknown: 0}

var collectionFactoryActivityBonus = activityWeightTable{weak: 200, growing: 100, strong: 50, restricted: 0, unknown: 0}

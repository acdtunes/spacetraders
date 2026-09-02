package mvt

// PoolSize is min(count of fat lanes, floor(hulls × fractionPct / 100)). At one hull and
// ten percent the floor is zero: no specialists.
func PoolSize(fatLanes, hulls, fractionPct int) int {
	if fatLanes < 0 || hulls < 0 || fractionPct < 0 {
		return 0
	}
	byFraction := hulls * fractionPct / 100
	if fatLanes < byFraction {
		return fatLanes
	}
	return byFraction
}

// IsFatLane reports whether a lane's margin per tranche, net of the measured jump cost,
// clears multiplePct/100 times the fleet's intra-system margin per tranche. With no
// intra-system baseline nothing qualifies.
func IsFatLane(marginPerTranche, transitSeconds, fleetCreditsPerSec float64, gateFee int64, intraMarginPerTranche float64, multiplePct int) bool {
	if intraMarginPerTranche <= 0 {
		return false
	}
	jumpCost := transitSeconds*fleetCreditsPerSec + float64(gateFee)
	return marginPerTranche-jumpCost > float64(multiplePct)/100*intraMarginPerTranche
}

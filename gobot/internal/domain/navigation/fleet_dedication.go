package navigation

// The trade coordinator's three dedication tags. All three name hulls of the SAME fleet —
// the tag selects a hull's tour path, not its owner — and they live here so the claim guards
// in adapters/api and adapters/grpc cannot drift from the coordinator that writes them.
const (
	TradeFleet     = "trade"
	TradeFleetMVT  = "trade-mvt"
	TradeFleetLane = "trade-lane"
)

// FleetDedicationPermits reports whether operation may NEWLY claim a hull pinned to
// dedicatedFleet: no pin, the same identity, or two tags of the trade family. Not string
// equality, because a tour claims under "trade" whatever trade tag the fleet coordinator
// wrote — equality parks every re-tagged hull for good. "" still reaches unpinned hulls only.
func FleetDedicationPermits(dedicatedFleet, operation string) bool {
	if dedicatedFleet == "" || dedicatedFleet == operation {
		return true
	}
	return IsTradeFleet(dedicatedFleet) && IsTradeFleet(operation)
}

func IsTradeFleet(tag string) bool {
	return tag == TradeFleet || tag == TradeFleetMVT || tag == TradeFleetLane
}

package depot

// DeliveryHullFleet is the DedicatedFleet tag a depot delivery hull carries.
// Assigning a hull as a depot delivery hull re-dedicates it to this fleet, which makes hull
// assignment ATOMIC: the tag is DISTINCT from the contract coordinator's own "contract" fleet
// on purpose, so a delivery hull is invisible to BOTH pools the coordinator draws from — the
// general idle-hauler pool (FindIdleLightHaulers excludes any DedicatedFleet != "") and the
// coordinator's own dedicated-fleet lookup (FindIdleShipsByFleet("contract") returns only
// "contract"-tagged hulls). The coordinator therefore can never re-grab a delivery hull for a
// general contract and pull it off its hub. A delivery hull is dispatched ONLY via
// routeContractViaDepot, whose claim runs under THIS same identity so ClaimShip's dedication
// guard (DedicatedFleet != "" && DedicatedFleet != operation) admits the depot route while still
// rejecting the general "contract" grab.
const DeliveryHullFleet = "depot-delivery"

// DistanceBetween resolves the travel distance between two waypoints for delivery-hull
// selection. ok=false means the position pair is unknown (a nil oracle, an uncharted /
// TTL-expired waypoint, or an unavailable graph) — the selector then treats that hull as
// non-preferred and falls open to config order.
type DistanceBetween func(fromWaypoint, toWaypoint string) (distance float64, ok bool)

// SelectDeliveryHull returns the pinned delivery hull the depot uses to fulfil a contract
// delivering to destinationSymbol: the hull whose parked hub is NEAREST to destinationSymbol.
// With a MULTI-hub delivery fleet this is what makes every cluster's contract
// deliver locally — each routes to its own nearest hull — instead of shuttling the single
// config-first hull to every destination (which only compressed the haul for destinations
// adjacent to where that one hull parked). distance is the SAME in-system coordinate separation
// the rest of the routing ranks pool candidates by (SelectClosestShip / Waypoint.DistanceTo).
//
// It is fail-open and regression-safe on every degenerate shape:
//   - no delivery hull    -> ok=false (no local delivery possible; caller keeps the long haul).
//   - exactly one hull    -> that hull, byte-identical (nearest-of-one is that one; the distance
//     oracle is never consulted).
//   - nil distance oracle -> the first configured hull (an un-wired / degraded deployment falls
//     back to config order).
//   - a hull at an uncharted hub (ok=false) never displaces a hull with a known, nearer position,
//     and ties keep config order — so the pick is deterministic pass-to-pass.
func (c *ContractDepot) SelectDeliveryHull(destinationSymbol string, distance DistanceBetween) (Element, bool) {
	if len(c.deliveryHulls) == 0 {
		return Element{}, false
	}
	if len(c.deliveryHulls) == 1 || distance == nil {
		return c.deliveryHulls[0], true
	}
	return c.nearestDeliveryHull(destinationSymbol, distance), true
}

// nearestDeliveryHull returns the delivery hull whose parked hub is closest to
// destinationSymbol, keeping config order for ties and for hulls whose position is unknown (so a
// stale-graph hull can never hijack the route from a known-nearer one).
func (c *ContractDepot) nearestDeliveryHull(destinationSymbol string, distance DistanceBetween) Element {
	best := c.deliveryHulls[0]
	bestDistance, bestKnown := distance(best.Waypoint, destinationSymbol)
	for _, candidate := range c.deliveryHulls[1:] {
		candidateDistance, candidateKnown := distance(candidate.Waypoint, destinationSymbol)
		if candidateKnown && (!bestKnown || candidateDistance < bestDistance) {
			best, bestDistance, bestKnown = candidate, candidateDistance, true
		}
	}
	return best
}

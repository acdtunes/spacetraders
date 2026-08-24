package contract

import (
	"fmt"
	"math"
	"strings"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// roleCommandHull is the registration role of the agent's command frigate.
const roleCommandHull = "COMMAND"

// DepotOperationWarehouse / DepotOperationStocker are the passive depot-role
// fleet identities the command frigate must NEVER fill (RULINGS #7). They are
// the canonical operation strings the warehouse and stocker launchers tag their
// hulls and claim under; the grpc-layer operation constants
// (operationWarehouse/operationStocker) are defined FROM these so the claim-time
// command-frigate guard (ship_repository.go ClaimShip) and the launchers can
// never name different strings and let the guard silently drift out of coverage.
const (
	DepotOperationWarehouse = "warehouse"
	DepotOperationStocker   = "stocker"
)

// IsCommandHull reports whether a ship is the command frigate, by registration
// role or by the conventional "*-1" symbol (e.g. "TORWIND-1"). Candidate
// discovery, cargo-fit selection and the selection log all share this one
// predicate so they agree on exactly which hull is treated as the command ship.
func IsCommandHull(ship *navigation.Ship) bool {
	return IsCommandHullSymbolRole(ship.ShipSymbol(), ship.Role())
}

// IsCommandHullSymbolRole is the raw command-frigate predicate behind
// IsCommandHull, taking the bare symbol + role so a persistence-layer guard
// holding a locked row model (not a domain Ship) shares the EXACT same rule and
// can never drift from it. Used by ClaimShip's depot-role command-frigate
// rejection, where reconstructing a full domain Ship inside the
// row-locked claim transaction would be needless.
func IsCommandHullSymbolRole(shipSymbol, role string) bool {
	return role == roleCommandHull || strings.HasSuffix(shipSymbol, "-1")
}

// IsDepotOperation reports whether a claim operation is a passive depot role
// (warehouse or stocker) — the roles the command frigate is rejected from at
// claim time on EVERY path (launch, grow, and restart recovery), so an orphaned
// depot container recovered from the registry can never re-claim the flagship
// (RULINGS #7). Every other operation (contract, gas, manufacturing,
// scouting, ...) is unaffected: the frigate stays a legitimate last-resort haul
// candidate there (SelectHullForCargo Tier 2/4).
func IsDepotOperation(operation string) bool {
	return operation == DepotOperationWarehouse || operation == DepotOperationStocker
}

// hullFit carries the per-candidate figures the cargo-fit ladder ranks on, so
// each candidate's distance/travel-time/trip math is computed exactly once.
type hullFit struct {
	ship       *navigation.Ship
	distance   float64
	travelTime float64 // ETA to the target: a supplied route ETA (possibly multi-hop/refuel/transit-inclusive) where one covers this hull, straight-line cruise time otherwise
	capacity   int     // clamped to >=1 so trip math never divides by zero
	trips      int     // ceil(cargoUnits / capacity): round trips to move the load
	onOwnSlot  bool    // true iff standing exactly on its own assigned standby slot
}

// SelectHullForCargo picks the hull whose hold matches the load - the shared
// cargo-fit selection policy for coordinators assigning haul work. A pure
// proximity-first ladder over-uses heavies on short legs; a pure smallest-fit
// ladder can strand a nearer adequate hull for a farther small one. This
// ladder ranks the ADEQUATE hulls by proximity and uses hold size only to
// break ties:
//
//	Tier 1: among regular hulls whose capacity fits the whole load, the
//	        NEAREST by travel time - a supplied ETA where the hull has one,
//	        straight-line cruise time otherwise. Equal travel times tie-break
//	        on the smallest fitting hold, so a nearer adequate hull beats a
//	        farther smaller one while two equidistant hulls still right-size.
//	        A further exact tie on both prefers the hull NOT parked on its
//	        own standby slot, leaving the correctly-homed one in place.
//	Tier 2: the command frigate, only when NO regular hull fits. It stays an
//	        eligible candidate but is drafted strictly last-resort - mirroring
//	        how IncludeCommandShip already gates its pool entry.
//	Tier 3: nothing fits in one trip - the regular hull needing the FEWEST
//	        round trips (largest effective hold), travel time as tie-break.
//	        The heavy is picked exactly when the load needs it.
//	Tier 4: the command frigate as the sole remaining candidate.
//
// The caller owns availability filtering (idle/claimable) and claiming; this
// function only ranks the candidates it is given. etas is nil-safe per
// candidate, falling back to the cruise estimate; deliveryFleet/standbySlots
// feed the Tier-1 ownership tiebreak, a no-op when empty.
func SelectHullForCargo(
	candidates []*navigation.Ship,
	target *shared.Waypoint,
	cargoUnits int,
	etas map[string]float64,
	deliveryFleet []string,
	standbySlots []string,
) (*SelectionResult, error) {
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no ships available for selection")
	}
	if target == nil {
		return nil, fmt.Errorf("target waypoint cannot be nil")
	}

	units := cargoUnits
	if units < 1 {
		units = 1
	}

	var regular, command []hullFit
	for _, ship := range candidates {
		fit := newHullFit(ship, target, units, etas, deliveryFleet, standbySlots)
		if IsCommandHull(ship) {
			command = append(command, fit)
		} else {
			regular = append(regular, fit)
		}
	}

	fitsWholeLoad := func(f hullFit) bool { return f.capacity >= units }

	// Tier 1: nearest adequate regular hull (smallest hold breaks a tie).
	if best, ok := minFit(filterFits(regular, fitsWholeLoad), byNearestThenSmallest); ok {
		return fitSelectionResult(best, fmt.Sprintf("nearest fitting hull (%d-hold for %d units)", best.capacity, units)), nil
	}
	// Tier 2: the command frigate fits and nothing else does.
	if best, ok := minFit(filterFits(command, fitsWholeLoad), bySmallestCapacity); ok {
		return fitSelectionResult(best, fmt.Sprintf("command frigate last resort: only hull fitting %d units", units)), nil
	}
	// Tier 3: nothing fits in one trip - fewest round trips wins.
	if best, ok := minFit(regular, byFewestTrips); ok {
		return fitSelectionResult(best, fmt.Sprintf("partial fit: fewest round trips (%d x %d-hold for %d units)", best.trips, best.capacity, units)), nil
	}
	// Tier 4: the command frigate is all that's left.
	if best, ok := minFit(command, byFewestTrips); ok {
		return fitSelectionResult(best, "command frigate last resort: only hull available"), nil
	}

	return nil, fmt.Errorf("no ships available for selection")
}

// newHullFit computes the ranking figures for one candidate hull; a supplied
// ETA replaces the straight-line estimate for a candidate it covers.
func newHullFit(ship *navigation.Ship, target *shared.Waypoint, units int, etas map[string]float64, deliveryFleet, standbySlots []string) hullFit {
	capacity := ship.CargoCapacity()
	if capacity < 1 {
		capacity = 1
	}
	distance := ship.CurrentLocation().DistanceTo(target)
	travelTime := float64(shared.FlightModeCruise.TravelTime(distance, ship.EngineSpeed()))
	if etas != nil {
		if eta, ok := etas[ship.ShipSymbol()]; ok {
			travelTime = eta
		}
	}
	return hullFit{
		ship:       ship,
		distance:   distance,
		travelTime: travelTime,
		capacity:   capacity,
		trips:      int(math.Ceil(float64(units) / float64(capacity))),
		onOwnSlot:  isOnOwnAssignedSlot(ship, deliveryFleet, standbySlots),
	}
}

// isOnOwnAssignedSlot reports whether ship is standing exactly on the slot
// AssignedSlot would give it against deliveryFleet/standbySlots. An empty
// roster or slot list answers false uniformly, so a caller's tiebreak on it
// degrades to a no-op rather than guessing.
func isOnOwnAssignedSlot(ship *navigation.Ship, deliveryFleet, standbySlots []string) bool {
	if len(deliveryFleet) == 0 || len(standbySlots) == 0 {
		return false
	}
	slot, owns := AssignedSlot(ship.ShipSymbol(), deliveryFleet, standbySlots)
	if !owns {
		return false
	}
	loc := ship.CurrentLocation()
	return loc != nil && loc.Symbol == slot
}

// filterFits returns the candidates satisfying the fit predicate.
func filterFits(fits []hullFit, keep func(hullFit) bool) []hullFit {
	var out []hullFit
	for _, f := range fits {
		if keep(f) {
			out = append(out, f)
		}
	}
	return out
}

// byNearestThenSmallest orders adequate hulls (Tier 1): shortest travel time
// first (etas override cruise time per candidate), smallest fitting hold second,
// standby-slot ownership breaking a further exact tie - proximity and size-fit
// both outrank it, so it never reorders candidates that differ on either.
func byNearestThenSmallest(a, b hullFit) bool {
	if a.travelTime != b.travelTime {
		return a.travelTime < b.travelTime
	}
	if a.capacity != b.capacity {
		return a.capacity < b.capacity
	}
	if a.onOwnSlot != b.onOwnSlot {
		return !a.onOwnSlot // prefer the displaced candidate over the one on its own slot
	}
	return false
}

// bySmallestCapacity orders fitting command hulls (Tier 2): smallest hold
// first, faster of two equal holds first. Tier 1 ranks by proximity first
// (byNearestThenSmallest); this last-resort frigate pool ranks by size first.
func bySmallestCapacity(a, b hullFit) bool {
	if a.capacity != b.capacity {
		return a.capacity < b.capacity
	}
	return a.travelTime < b.travelTime
}

// byFewestTrips orders partial-fit hulls: fewest round trips first, faster of
// two equal-trip hulls first.
func byFewestTrips(a, b hullFit) bool {
	if a.trips != b.trips {
		return a.trips < b.trips
	}
	return a.travelTime < b.travelTime
}

// minFit returns the best candidate under the given ordering, or ok=false for
// an empty slice.
func minFit(fits []hullFit, less func(a, b hullFit) bool) (hullFit, bool) {
	if len(fits) == 0 {
		return hullFit{}, false
	}
	best := fits[0]
	for _, f := range fits[1:] {
		if less(f, best) {
			best = f
		}
	}
	return best, true
}

func fitSelectionResult(fit hullFit, reason string) *SelectionResult {
	return &SelectionResult{
		Ship:     fit.ship,
		Distance: fit.distance,
		Reason:   fmt.Sprintf("%s, %.2f units away", reason, fit.distance),
	}
}

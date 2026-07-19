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

// IsCommandHull reports whether a ship is the command frigate, by registration
// role or by the conventional "*-1" symbol (e.g. "TORWIND-1"). Candidate
// discovery, cargo-fit selection and the selection log all share this one
// predicate so they agree on exactly which hull is treated as the command ship.
func IsCommandHull(ship *navigation.Ship) bool {
	return ship.Role() == roleCommandHull || strings.HasSuffix(ship.ShipSymbol(), "-1")
}

// hullFit carries the per-candidate figures the cargo-fit ladder ranks on, so
// each candidate's distance/travel-time/trip math is computed exactly once.
type hullFit struct {
	ship       *navigation.Ship
	distance   float64
	travelTime float64 // one-way cruise time to the target, speed-aware
	capacity   int     // clamped to >=1 so trip math never divides by zero
	trips      int     // ceil(cargoUnits / capacity): round trips to move the load
}

// SelectHullForCargo picks the hull whose hold matches the load - the shared
// cargo-fit selection policy for coordinators assigning haul work. A pure
// proximity-first ladder over-uses heavies on short legs; a pure smallest-fit
// ladder can strand a nearer adequate hull for a farther small one. This
// ladder ranks the ADEQUATE hulls by proximity and uses hold size only to
// break ties:
//
//	Tier 1: among regular hulls whose capacity fits the whole load, the
//	        NEAREST by cruise travel time. Equal travel times tie-break on the
//	        smallest fitting hold, so a nearer adequate hull beats a farther
//	        smaller one while two equidistant hulls still right-size. Travel
//	        time is speed-aware, so a fast hull that clears the leg sooner
//	        outranks a slow one nominally as close.
//	Tier 2: the command frigate, only when NO regular hull fits. It stays an
//	        eligible candidate but is drafted strictly last-resort - mirroring
//	        how IncludeCommandShip already gates its pool entry.
//	Tier 3: nothing fits in one trip - the regular hull needing the FEWEST
//	        round trips (largest effective hold), travel time as tie-break.
//	        The heavy is picked exactly when the load needs it.
//	Tier 4: the command frigate as the sole remaining candidate.
//
// The caller owns availability filtering (idle/claimable) and claiming; this
// function only ranks the candidates it is given.
func SelectHullForCargo(
	candidates []*navigation.Ship,
	target *shared.Waypoint,
	cargoUnits int,
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
		fit := newHullFit(ship, target, units)
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

// newHullFit computes the ranking figures for one candidate hull.
func newHullFit(ship *navigation.Ship, target *shared.Waypoint, units int) hullFit {
	capacity := ship.CargoCapacity()
	if capacity < 1 {
		capacity = 1
	}
	distance := ship.CurrentLocation().DistanceTo(target)
	return hullFit{
		ship:       ship,
		distance:   distance,
		travelTime: float64(shared.FlightModeCruise.TravelTime(distance, ship.EngineSpeed())),
		capacity:   capacity,
		trips:      int(math.Ceil(float64(units) / float64(capacity))),
	}
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

// byNearestThenSmallest orders adequate hulls (Tier 1): shortest cruise travel
// time first, smallest fitting hold breaking a tie. Proximity is the primary
// key so a nearer adequate hull beats a farther smaller one; size-fit is
// subordinated to distance, not the other way round.
func byNearestThenSmallest(a, b hullFit) bool {
	if a.travelTime != b.travelTime {
		return a.travelTime < b.travelTime
	}
	return a.capacity < b.capacity
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

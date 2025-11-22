package contract

import (
	"fmt"
	"math"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

const (
	// ProximityRadius defines the distance threshold (in units) for counting nearby ships.
	// Ships within this radius of a market are considered "covering" that market.
	ProximityRadius = 500.0

	// CoverageWeight determines how heavily coverage (number of nearby ships) affects
	// the balancing score. Higher weight means coverage gaps are prioritized over distance.
	CoverageWeight = 10.0

	// DistanceWeight determines how distance affects the balancing score.
	// Lower weight means fuel efficiency is a secondary concern after coverage.
	DistanceWeight = 0.1
)

// BalancingResult contains the result of balancing calculation
type BalancingResult struct {
	TargetMarket   *shared.Waypoint
	Score          float64
	NearbyHaulers  int     // Number of haulers already near this market
	Distance       float64 // Distance from ship to market
}

// ShipBalancer implements ship balancing logic to optimize fleet distribution
// across markets. Uses a Distance + Coverage Score algorithm to find the optimal
// repositioning target for idle ships.
type ShipBalancer struct{}

// NewShipBalancer creates a new ship balancer
func NewShipBalancer() *ShipBalancer {
	return &ShipBalancer{}
}

// SelectOptimalBalancingPosition selects the best market to send a ship to
// in order to optimize the overall fleet distribution.
//
// Algorithm:
//   For each market:
//     1. Count idle haulers within ProximityRadius (500 units)
//     2. Calculate distance from ship to market
//     3. Calculate score = (nearby_haulers × 10) + (distance × 0.1)
//   Return market with lowest score
//
// Business Rules:
//   - Prioritizes markets with fewer nearby ships (10× weight)
//   - Considers fuel efficiency as secondary factor (0.1× weight)
//   - Automatic tie-breaking: if multiple markets have same coverage, picks nearest
//
// Parameters:
//   - ship: The ship to reposition
//   - markets: Available markets in the system
//   - idleHaulers: All idle light hauler ships (for coverage calculation)
//
// Returns:
//   - BalancingResult with target market, score, and metrics
//   - Error if no markets available or ship is nil
func (b *ShipBalancer) SelectOptimalBalancingPosition(
	ship *navigation.Ship,
	markets []*shared.Waypoint,
	idleHaulers []*navigation.Ship,
) (*BalancingResult, error) {
	if ship == nil {
		return nil, fmt.Errorf("ship cannot be nil")
	}

	if len(markets) == 0 {
		return nil, fmt.Errorf("no markets available for balancing")
	}

	var bestMarket *shared.Waypoint
	bestScore := math.MaxFloat64
	var bestNearbyCount int
	var bestDistance float64

	shipLocation := ship.CurrentLocation()

	for _, market := range markets {
		nearbyCount := b.countNearbyHaulers(market, idleHaulers)
		distance := shipLocation.DistanceTo(market)
		score := b.calculateBalancingScore(nearbyCount, distance)

		if score < bestScore {
			bestScore = score
			bestMarket = market
			bestNearbyCount = nearbyCount
			bestDistance = distance
		}
	}

	if bestMarket == nil {
		return nil, fmt.Errorf("no suitable market found for balancing")
	}

	return &BalancingResult{
		TargetMarket:  bestMarket,
		Score:         bestScore,
		NearbyHaulers: bestNearbyCount,
		Distance:      bestDistance,
	}, nil
}

// countNearbyHaulers counts how many idle haulers are within ProximityRadius of the market
func (b *ShipBalancer) countNearbyHaulers(market *shared.Waypoint, idleHaulers []*navigation.Ship) int {
	count := 0
	for _, hauler := range idleHaulers {
		distance := market.DistanceTo(hauler.CurrentLocation())
		if distance <= ProximityRadius {
			count++
		}
	}
	return count
}

// calculateBalancingScore calculates the balancing score for a market
//
// Formula: (nearby_haulers × CoverageWeight) + (distance × DistanceWeight)
//
// Lower score is better. This formula prioritizes coverage gaps (markets with
// fewer nearby ships) while considering distance as a secondary factor.
func (b *ShipBalancer) calculateBalancingScore(nearbyHaulers int, distance float64) float64 {
	return (float64(nearbyHaulers) * CoverageWeight) + (distance * DistanceWeight)
}

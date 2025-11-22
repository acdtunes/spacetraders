package contract

import (
	"fmt"
	"math"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

const (
	// AssignmentWeight determines how heavily existing assignments affect the balancing score.
	// Higher weight ensures even distribution across markets by penalizing markets that already have ships.
	// Weight of 100.0 means assigning a second ship to a market costs ~100 distance units penalty.
	AssignmentWeight = 100.0

	// DistanceWeight determines how distance affects the balancing score.
	// Lower weight means distribution is prioritized over fuel efficiency.
	DistanceWeight = 0.1
)

// BalancingResult contains the result of balancing calculation
type BalancingResult struct {
	TargetMarket    *shared.Waypoint
	Score           float64
	AssignedShips   int     // Number of ships assigned to this market during balancing
	Distance        float64 // Distance from ship to market
}

// ShipBalancer implements ship balancing logic to optimize fleet distribution
// across markets. Uses global assignment tracking to ensure even distribution
// (1 ship per market ideally, then 2 per market, etc.) with distance as a tiebreaker.
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
//     1. Count ships already assigned to this market during this balancing session
//     2. Calculate distance from ship to market
//     3. Calculate score = (assigned_ships × 100) + (distance × 0.1)
//   Return market with lowest score
//
// Business Rules:
//   - Prioritizes even distribution (1 ship per market ideal, then 2 per market, etc.)
//   - Heavy penalty for markets with existing assignments (100× weight)
//   - Distance as tiebreaker when assignments are equal (0.1× weight)
//   - This is a single-ship decision (not batch processing)
//
// Parameters:
//   - ship: The ship to reposition
//   - markets: Available markets in the system
//   - idleHaulers: Not used in current implementation (kept for interface compatibility)
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
	var bestAssignedCount int
	var bestDistance float64

	shipLocation := ship.CurrentLocation()

	for _, market := range markets {
		// Count ships currently at this market (exact location match, not proximity)
		assignedCount := b.countShipsAtMarket(market, idleHaulers)
		distance := shipLocation.DistanceTo(market)
		score := b.calculateBalancingScore(assignedCount, distance)

		if score < bestScore {
			bestScore = score
			bestMarket = market
			bestAssignedCount = assignedCount
			bestDistance = distance
		}
	}

	if bestMarket == nil {
		return nil, fmt.Errorf("no suitable market found for balancing")
	}

	return &BalancingResult{
		TargetMarket:  bestMarket,
		Score:         bestScore,
		AssignedShips: bestAssignedCount,
		Distance:      bestDistance,
	}, nil
}

// countShipsAtMarket counts how many idle ships are currently at this market location.
// Uses exact location match (not proximity-based) to determine global distribution.
func (b *ShipBalancer) countShipsAtMarket(market *shared.Waypoint, idleHaulers []*navigation.Ship) int {
	count := 0
	for _, hauler := range idleHaulers {
		// Check if ship is at the exact market location
		if hauler.CurrentLocation().Symbol == market.Symbol {
			count++
		}
	}
	return count
}

// calculateBalancingScore calculates the balancing score for a market
//
// Formula: (assigned_ships × AssignmentWeight) + (distance × DistanceWeight)
//
// Lower score is better. This formula prioritizes even distribution (markets with
// fewer assigned ships) while considering distance as a tiebreaker.
func (b *ShipBalancer) calculateBalancingScore(assignedShips int, distance float64) float64 {
	return (float64(assignedShips) * AssignmentWeight) + (distance * DistanceWeight)
}

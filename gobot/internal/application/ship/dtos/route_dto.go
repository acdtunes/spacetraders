package dtos

import (
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// RouteSegmentDTO represents a single segment of a route for serialization.
// This is a simplified DTO used for gRPC communication and display purposes.
type RouteSegmentDTO struct {
	From       string // Origin waypoint symbol
	To         string // Destination waypoint symbol
	FlightMode string // CRUISE, DRIFT, BURN, STEALTH
	FuelCost   int    // Fuel required for this segment
	TravelTime int    // Time in seconds
}

// ShipRouteDTO represents a complete route for a ship including all segments.
// This is used for dry-run displays and route visualization.
type ShipRouteDTO struct {
	ShipSymbol string            // Ship identifier
	ShipType   string            // "miner" or "transport"
	Segments   []RouteSegmentDTO // Route segments
	TotalFuel  int               // Total fuel for entire route
	TotalTime  int               // Total time in seconds
}

// RouteSegmentToDTO converts a domain RouteSegment to a DTO for serialization.
// This maintains hexagonal architecture by keeping conversion logic in the application layer.
func RouteSegmentToDTO(seg *navigation.RouteSegment) RouteSegmentDTO {
	return RouteSegmentDTO{
		From:       seg.FromWaypoint.Symbol,
		To:         seg.ToWaypoint.Symbol,
		FlightMode: seg.FlightMode.String(),
		FuelCost:   seg.FuelRequired,
		TravelTime: seg.TravelTime,
	}
}

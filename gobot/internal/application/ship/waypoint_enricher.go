package ship

import (
	"context"
	"log"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// WaypointEnricher enriches graph waypoints with trait data from database
type WaypointEnricher struct {
	waypointRepo system.WaypointRepository
	converter    system.IWaypointConverter
}

// NewWaypointEnricher creates a new waypoint enricher
func NewWaypointEnricher(waypointRepo system.WaypointRepository, converter system.IWaypointConverter) *WaypointEnricher {
	return &WaypointEnricher{
		waypointRepo: waypointRepo,
		converter:    converter,
	}
}

// EnrichGraphWaypoints enriches NavigationGraph waypoints with trait data from database
//
// Args:
//
//	graph: NavigationGraph with waypoints
//	systemSymbol: System symbol for loading waypoint traits
//
// Returns:
//
//	Map of waypoint_symbol -> Waypoint objects with enriched trait data
func (e *WaypointEnricher) EnrichGraphWaypoints(
	ctx context.Context,
	graph *system.NavigationGraph,
	systemSymbol string,
) (map[string]*shared.Waypoint, error) {
	// 1. Load waypoint traits from database to potentially enrich graph waypoints
	waypointList, err := e.waypointRepo.ListBySystem(ctx, systemSymbol)
	var waypointTraits map[string]*shared.Waypoint
	if err != nil {
		log.Printf("Warning: failed to load waypoint traits: %v (using graph data only)", err)
		waypointTraits = make(map[string]*shared.Waypoint)
	} else {
		waypointTraits = make(map[string]*shared.Waypoint)
		for _, wp := range waypointList {
			waypointTraits[wp.Symbol] = wp
		}
	}

	// 2. Enrich graph waypoints with trait data from database
	enrichedWaypoints := make(map[string]*shared.Waypoint)
	for symbol, graphWaypoint := range graph.Waypoints {
		// Use database version if available (has traits), otherwise use graph version
		if dbWaypoint, exists := waypointTraits[symbol]; exists {
			enrichedWaypoints[symbol] = dbWaypoint
		} else {
			enrichedWaypoints[symbol] = graphWaypoint
		}
	}

	return enrichedWaypoints, nil
}

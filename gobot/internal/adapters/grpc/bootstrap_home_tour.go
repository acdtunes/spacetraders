package grpc

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// bootstrapHomeTourStarter puts the cold-start probes on a market tour of the home system.
//
// It calls ScoutMarkets — the SAME entry point the captain's `scout markets` verb reaches.
// That is the whole design: one code path with two callers, so the automatic DATA-phase tour
// and an operator's tour are the same object, refused by the same bootstrap-phase gate and
// stopped by the same graduation sweep. Nothing is declared, nothing is persisted as desired
// state, and no engine mans anything.
type bootstrapHomeTourStarter struct{ server *DaemonServer }

// StartHomeMarketTour flies every IDLE probe over the home system's marketplaces, returning
// how many it put on tour.
//
// Returning (0, nil) is a first-class answer, not a failure: a fleet whose probes are all
// busy, or a home system with no known marketplace yet, has nothing to start this tick and
// bootstrap simply re-observes on the next one. Only a genuine inability to read the fleet or
// to start the tour is an error, and the caller retries that too.
func (b *bootstrapHomeTourStarter) StartHomeMarketTour(ctx context.Context, playerID int, homeSystem string) (int, error) {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return 0, err
	}

	ships, err := b.server.shipRepo.FindAllByPlayer(ctx, pid)
	if err != nil {
		return 0, fmt.Errorf("read fleet: %w", err)
	}
	var probes []string
	for _, ship := range ships {
		// IDLE and AT HOME. A probe already flying is somebody's, and one parked elsewhere
		// would have to cross a gate to get here — travel this tour has no business ordering.
		if ship == nil || !ship.IsScoutType() || !ship.IsIdle() {
			continue
		}
		if loc := ship.CurrentLocation(); loc == nil || shared.ExtractSystemSymbol(loc.Symbol) != homeSystem {
			continue
		}
		probes = append(probes, ship.ShipSymbol())
	}
	if len(probes) == 0 {
		return 0, nil
	}

	waypoints, err := b.server.waypointRepo.ListBySystemWithTrait(ctx, homeSystem, marketplaceTrait)
	if err != nil {
		return 0, fmt.Errorf("list %s marketplaces: %w", homeSystem, err)
	}
	markets := make([]string, 0, len(waypoints))
	for _, wp := range waypoints {
		if wp != nil {
			markets = append(markets, wp.Symbol)
		}
	}
	if len(markets) == 0 {
		return 0, nil // the home system is not charted yet; the next tick re-reads
	}

	// Iterations -1: the tour re-flies its circuit until something stops it. What stops it is
	// the graduation sweep, which is the point — a finite count would end the tour early and
	// leave market data going stale for the rest of cold start with nothing to notice.
	started, _, _, err := b.server.ScoutMarkets(ctx, probes, homeSystem, markets, -1, playerID)
	if err != nil {
		return 0, err
	}
	return len(started), nil
}

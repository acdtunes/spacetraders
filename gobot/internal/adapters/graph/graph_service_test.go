package graph

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// frozenGraphRepo hands back one blob however often it is asked, which is what the
// system_graphs row does: it is written on first arrival and never rewritten.
type frozenGraphRepo struct {
	graph *system.NavigationGraph
}

func (r *frozenGraphRepo) Get(context.Context, string) (*system.NavigationGraph, error) {
	return r.graph, nil
}

func (r *frozenGraphRepo) Add(context.Context, string, *system.NavigationGraph) error { return nil }

// chartedWaypointRepo is the waypoints table after the charting path rewrote the row.
type chartedWaypointRepo struct {
	waypoint *shared.Waypoint
	lookups  int
}

func (r *chartedWaypointRepo) FindBySymbol(_ context.Context, symbol, _ string) (*shared.Waypoint, error) {
	r.lookups++
	if r.waypoint == nil || r.waypoint.Symbol != symbol {
		return nil, nil
	}
	return r.waypoint, nil
}

func (r *chartedWaypointRepo) ListBySystem(context.Context, string) ([]*shared.Waypoint, error) {
	return nil, nil
}

func (r *chartedWaypointRepo) ListBySystemWithTrait(context.Context, string, string) ([]*shared.Waypoint, error) {
	return nil, nil
}

func (r *chartedWaypointRepo) Add(context.Context, *shared.Waypoint) error { return nil }

// refusingGraphBuilder fails the test if the lookup falls all the way through to the
// API, which would mean the DB tier was skipped rather than consulted.
type refusingGraphBuilder struct{ t *testing.T }

func (b *refusingGraphBuilder) BuildSystemGraph(context.Context, string, int) (*system.NavigationGraph, error) {
	b.t.Fatal("the API was called for a waypoint the database already holds")
	return nil, nil
}

func mustWaypoint(t *testing.T, symbol string, traits []string, hasFuel bool) *shared.Waypoint {
	t.Helper()
	wp, err := shared.NewWaypoint(symbol, 0, 0)
	if err != nil {
		t.Fatalf("NewWaypoint(%s): %v", symbol, err)
	}
	wp.Type = "FUEL_STATION"
	wp.Traits = traits
	wp.HasFuel = hasFuel
	return wp
}

// TestGetWaypoint_UnchartedCacheEntryDoesNotShadowChartedRow pins the self-poisoning
// cache: the frozen blob overwrites tier 1 on every graph load, so an entry cached
// before charting would otherwise outlive the charted row forever.
func TestGetWaypoint_UnchartedCacheEntryDoesNotShadowChartedRow(t *testing.T) {
	const systemSymbol, waypointSymbol = "X1-TZ71", "X1-TZ71-C10D"

	uncharted := mustWaypoint(t, waypointSymbol, []string{"UNCHARTED"}, false)
	charted := mustWaypoint(t, waypointSymbol, []string{"MARKETPLACE"}, true)

	waypointRepo := &chartedWaypointRepo{waypoint: charted}
	service := NewGraphService(
		&frozenGraphRepo{graph: &system.NavigationGraph{
			SystemSymbol: systemSymbol,
			Waypoints:    map[string]*shared.Waypoint{waypointSymbol: uncharted},
		}},
		waypointRepo,
		&refusingGraphBuilder{t: t},
	)

	if _, err := service.GetGraph(context.Background(), systemSymbol, false, 1); err != nil {
		t.Fatalf("GetGraph: %v", err)
	}

	got, err := service.GetWaypoint(context.Background(), waypointSymbol, systemSymbol, 1)
	if err != nil {
		t.Fatalf("GetWaypoint: %v", err)
	}
	if !got.HasFuel {
		t.Fatalf("GetWaypoint returned the uncharted cache entry (traits %v), not the charted row", got.Traits)
	}
	if waypointRepo.lookups == 0 {
		t.Fatal("the database tier was never consulted - tier 1 answered with the uncharted entry")
	}
}

// TestGetWaypoint_ChartedCacheEntryIsServedFromTierOne is the calibration: only an
// uncharted entry falls through, or the in-memory tier has been disabled outright.
func TestGetWaypoint_ChartedCacheEntryIsServedFromTierOne(t *testing.T) {
	const systemSymbol, waypointSymbol = "X1-TZ71", "X1-TZ71-C10D"

	charted := mustWaypoint(t, waypointSymbol, []string{"MARKETPLACE"}, true)
	waypointRepo := &chartedWaypointRepo{waypoint: charted}
	service := NewGraphService(
		&frozenGraphRepo{graph: &system.NavigationGraph{
			SystemSymbol: systemSymbol,
			Waypoints:    map[string]*shared.Waypoint{waypointSymbol: charted},
		}},
		waypointRepo,
		&refusingGraphBuilder{t: t},
	)

	if _, err := service.GetGraph(context.Background(), systemSymbol, false, 1); err != nil {
		t.Fatalf("GetGraph: %v", err)
	}

	if _, err := service.GetWaypoint(context.Background(), waypointSymbol, systemSymbol, 1); err != nil {
		t.Fatalf("GetWaypoint: %v", err)
	}
	if waypointRepo.lookups != 0 {
		t.Fatalf("a charted cache entry cost %d database lookups, want 0", waypointRepo.lookups)
	}
}

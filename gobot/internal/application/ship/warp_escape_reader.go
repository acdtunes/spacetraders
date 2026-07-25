package ship

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// WarpDestinationEscape is what a system a warp would land in offers a hull that
// arrives with a near-empty tank: somewhere to buy fuel, or a built jump gate to
// leave through (a jump costs cooldown, not fuel). A destination offering NEITHER
// is a one-way trip.
type WarpDestinationEscape struct {
	SellsFuel    bool
	HasBuiltGate bool
}

// IsDeadEnd reports whether the destination has no way out at all.
func (e WarpDestinationEscape) IsDeadEnd() bool {
	return !e.SellsFuel && !e.HasBuiltGate
}

// WarpEscapeReader reads the escape state of a system a warp would land in. This
// is the ONE strand question the SpaceTraders server does not answer: it refuses a
// leg the hull cannot afford, but happily lands one somewhere it can never leave.
type WarpEscapeReader interface {
	EscapeOptions(ctx context.Context, systemSymbol string, playerID shared.PlayerID) (WarpDestinationEscape, error)
}

// gateAdjacencyReader is the narrow slice of system.GateEdgeRepository the escape
// reader needs (satisfied by the persisted gate-edge store). Narrowed so the
// reader's tests need no database.
type gateAdjacencyReader interface {
	Adjacency(ctx context.Context) (map[string][]system.GateEdge, error)
}

// SystemEscapeReader is the production WarpEscapeReader. It resolves the fuel half
// from the system's waypoints - through the SAME fetch-through source the warp verb
// resolves destinations with, so a system the fleet has never charted still answers
// truthfully instead of reading as empty - and the gate half from the stored gate
// adjacency.
type SystemEscapeReader struct {
	waypoints systemWaypointSource
	gates     gateAdjacencyReader
}

// NewSystemEscapeReader wires the production reader over the waypoint source and
// the gate-edge store.
func NewSystemEscapeReader(waypoints systemWaypointSource, gates gateAdjacencyReader) *SystemEscapeReader {
	return &SystemEscapeReader{waypoints: waypoints, gates: gates}
}

// EscapeOptions resolves both ways out of systemSymbol. Every failure is returned
// as an error rather than a negative fact: the caller refuses on error, so an
// unreadable destination can never be mistaken for a surveyed-and-safe one.
func (r *SystemEscapeReader) EscapeOptions(ctx context.Context, systemSymbol string, playerID shared.PlayerID) (WarpDestinationEscape, error) {
	if r.waypoints == nil || r.gates == nil {
		return WarpDestinationEscape{}, fmt.Errorf("warp escape reader is not fully wired: cannot read the escape state of %s", systemSymbol)
	}
	sellsFuel, err := r.sellsFuel(ctx, systemSymbol, playerID)
	if err != nil {
		return WarpDestinationEscape{}, err
	}
	hasBuiltGate, err := r.hasBuiltGate(ctx, systemSymbol)
	if err != nil {
		return WarpDestinationEscape{}, err
	}
	return WarpDestinationEscape{SellsFuel: sellsFuel, HasBuiltGate: hasBuiltGate}, nil
}

// sellsFuel reports whether any waypoint of the system sells fuel.
func (r *SystemEscapeReader) sellsFuel(ctx context.Context, systemSymbol string, playerID shared.PlayerID) (bool, error) {
	waypoints, err := r.waypoints.ChartWaypoints(ctx, systemSymbol, playerID)
	if err != nil {
		return false, fmt.Errorf("cannot read the waypoints of %s: %w", systemSymbol, err)
	}
	for _, waypoint := range waypoints {
		if waypoint != nil && waypoint.HasFuel {
			return true, nil
		}
	}
	return false, nil
}

// hasBuiltGate reports whether the system's OWN jump gate is built - the reverse
// lookup over the adjacency, since an edge carries the build state of the system it
// points AT. Being gate-CONNECTED is not enough: the near-miss that motivated this
// guard was a system whose only gate was still under construction, which no hull can
// leave through. A stale row's build state is unverified, so it is not an escape
// either.
func (r *SystemEscapeReader) hasBuiltGate(ctx context.Context, systemSymbol string) (bool, error) {
	adjacency, err := r.gates.Adjacency(ctx)
	if err != nil {
		return false, fmt.Errorf("cannot read the gate adjacency for %s: %w", systemSymbol, err)
	}
	for _, edges := range adjacency {
		for _, edge := range edges {
			if edge.ConnectedSystem == systemSymbol && !edge.UnderConstruction && !edge.Stale {
				return true, nil
			}
		}
	}
	return false, nil
}

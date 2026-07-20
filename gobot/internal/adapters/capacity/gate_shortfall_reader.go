package capacity

import (
	"context"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// jumpGateWaypointType is the waypoint type of a jump gate — the GATE-site discovery
// scans the home system for it (mirrors bootstrap_ports_gate.go).
const jumpGateWaypointType = "JUMP_GATE"

// GateHomeSystemFunc resolves the player's home system symbol ("" ⇒ unknown ⇒ no gate
// demand). Production wires a ships-table resolver (NewShipHomeSystemFunc); tests pass a
// stub.
type GateHomeSystemFunc func(ctx context.Context, playerID int) string

// gateWaypointLister lists a system's waypoints — satisfied by the Gorm waypoint repo.
type gateWaypointLister interface {
	ListBySystem(ctx context.Context, systemSymbol string) ([]*shared.Waypoint, error)
}

// gateConstructionFinder reads a construction site live — satisfied by the construction
// site API repository (required/fulfilled is API-only, hence a live boundary).
type gateConstructionFinder interface {
	FindByWaypoint(ctx context.Context, waypointSymbol string, playerID int) (*manufacturing.ConstructionSite, error)
}

// GateShortfallAPIReader is the production GateShortfallReader: it locates the home
// system's INCOMPLETE jump gate and reports its outstanding materials, mirroring the
// bootstrap gate-snapshot discovery (readBootstrapGateSnapshot) — list the home system,
// find the JUMP_GATE, skip a finished gate (a prior era's built gate is not the target),
// and read UnfulfilledMaterials live. Every miss (no home system, no gate, complete gate,
// construction-read error) fails closed to an empty shortfall so the reconciler never
// fabricates a depot; only a waypoint-list error surfaces (the sensor logs it and leaves
// the gate family empty).
type GateShortfallAPIReader struct {
	homeSystem   GateHomeSystemFunc
	waypoints    gateWaypointLister
	construction gateConstructionFinder
}

var _ GateShortfallReader = (*GateShortfallAPIReader)(nil)

// NewGateShortfallReader assembles the production gate-shortfall reader over the home-
// system resolver, the waypoint repository, and the live construction-site repository.
func NewGateShortfallReader(homeSystem GateHomeSystemFunc, waypoints gateWaypointLister, construction gateConstructionFinder) *GateShortfallAPIReader {
	return &GateShortfallAPIReader{homeSystem: homeSystem, waypoints: waypoints, construction: construction}
}

// GateShortfall resolves the home system's incomplete jump gate's outstanding materials.
func (r *GateShortfallAPIReader) GateShortfall(ctx context.Context, playerID int) (GateShortfall, error) {
	homeSystem := r.homeSystem(ctx, playerID)
	if homeSystem == "" {
		return GateShortfall{}, nil
	}
	waypoints, err := r.waypoints.ListBySystem(ctx, homeSystem)
	if err != nil {
		return GateShortfall{}, err
	}
	for _, wp := range waypoints {
		if wp == nil || wp.Type != jumpGateWaypointType {
			continue
		}
		site, err := r.construction.FindByWaypoint(ctx, wp.Symbol, playerID)
		if err != nil || site == nil {
			continue // a gate whose construction can't be read is skipped, never fatal
		}
		if site.IsComplete() {
			continue // a finished gate is not the fill target (mirrors the bootstrap snapshot)
		}
		return gateShortfallFromSite(wp.Symbol, site), nil
	}
	return GateShortfall{}, nil
}

// gateShortfallFromSite projects a construction site's unfulfilled materials into a
// GateShortfall. No outstanding materials ⇒ an empty shortfall (the depot dissolves).
func gateShortfallFromSite(waypoint string, site *manufacturing.ConstructionSite) GateShortfall {
	var materials []GateMaterialShortfall
	for _, mat := range site.UnfulfilledMaterials() {
		materials = append(materials, GateMaterialShortfall{TradeSymbol: mat.TradeSymbol(), Remaining: mat.Remaining()})
	}
	if len(materials) == 0 {
		return GateShortfall{}
	}
	return GateShortfall{GateWaypoint: waypoint, Materials: materials}
}

// NewShipHomeSystemFunc resolves the player's home system from any of its ships' current
// location — the same source the bootstrap observer uses (deriveHomeSystemFromShips).
// Best-effort: any miss (DB error, no located ship) ⇒ "" ⇒ no gate demand.
func NewShipHomeSystemFunc(db *gorm.DB) GateHomeSystemFunc {
	return func(ctx context.Context, playerID int) string {
		if db == nil {
			return ""
		}
		var rows []struct{ LocationSymbol string }
		err := db.WithContext(ctx).
			Table("ships").
			Select("location_symbol").
			Where("player_id = ? AND location_symbol <> ''", playerID).
			Limit(1).
			Scan(&rows).Error
		if err != nil || len(rows) == 0 {
			return ""
		}
		return shared.ExtractSystemSymbol(rows[0].LocationSymbol)
	}
}

// Package contractscaler is the pure brain of the dedicated contract auto-scaler
// (epic sp-9le3x): the per-era role lookup and the fixed fleet plan. The contract
// op is STATIONARY — home-system geometry (letter→distance bands) and the
// sink→good demand distribution are invariant across eras — so the optimal fleet
// arrangement is a FIXED plan computed at design time, not re-sensed each tick.
// The ONLY per-era resolution is which THIS-era waypoints fill the fixed roles,
// and that is a LOOKUP (geometry band + market trade role), not a solve.
package contractscaler

import (
	"math"
	"sort"
)

// centralBandRadius is the geometry cut that separates the inner "central"
// contract-sink cluster from the far source ring + J outlier (star at origin).
// Validated eras 1-3: the central cluster sits ~100-150 units out, the far ring
// ~330, the J outlier ~720 — so any cut in the wide 300-330 gap classifies them.
const centralBandRadius = 300.0

// WaypointMarket is one home-system waypoint's geometry + market role — the input
// to the per-era role lookup. Exports/Imports are the good symbols the waypoint
// EXPORTs / IMPORTs (EXCHANGE goods, being neither produced nor consumed, are
// omitted by the caller). Coordinates are relative to the system star at (0,0).
// IsMarketplace is the DURABLE charted MARKETPLACE trait (set from waypoint charting,
// independent of whether the per-good market has been dock-scanned this pass): it is
// what keeps an inner-band contract sink in the central-park set even before its
// import goods are scanned, so idle delivery hulls spread across ALL distinct central
// waypoints instead of piling on the handful currently scanned as importers.
//
// Type / Traits / HasFuel are the rest of the DURABLE charted record — the waypoint's
// generator type (PLANET / MOON / ORBITAL_STATION / ASTEROID_BASE …), its charted trait
// symbols, and whether it sells fuel on site. They carry the era-invariant standby
// anchors (see anchors.go): the universe regenerates every era with new NAMES and NUMBERS
// but the SAME generator template, so an anchor keyed on composition + trait + radius
// resolves identically era after era while a symbol never does.
type WaypointMarket struct {
	Symbol        string
	X, Y          float64
	Exports       []string
	Imports       []string
	IsMarketplace bool
	Type          string
	Traits        []string
	HasFuel       bool
}

// EraRoles is the resolved per-era topology: which of THIS era's waypoints fill
// the fixed roles. Names and numbers regenerate every era, so the roles are keyed
// on geometry + market role, never on a letter or number. CentralParks and
// FarSources are returned in a stable symbol order; FarSink is the single far
// outlier consumer (served LIVE — no J depot), "" when the system has none.
// Anchors are the four era-invariant standby placement anchors (anchors.go), each ""
// when this era's charted template did not produce it.
type EraRoles struct {
	CentralParks []string
	FarSources   []string
	FarSink      string
	Anchors      EraAnchors
}

// ResolveRoles classifies home-system waypoints into the fixed roles by geometry
// band and market trade role — "a lookup, not a solve":
//   - central parks = inner-band contract sinks (isCentralSink): the delivery targets.
//   - far sources   = far-band exporters: where the fleet sources ores/precious/drugs.
//   - far sink      = the far-outlier consumer: the DURABLE pirate-base template when this
//     era charted one, else the farthest far-band importer (the original rule).
//   - anchors       = the four era-invariant standby placement anchors (anchors.go).
//
// The classification is a pure function of position + charted facts + trade role, so a
// renamed era with identical geometry resolves to identical roles (the stationarity the
// whole design rests on).
func ResolveRoles(markets []WaypointMarket) EraRoles {
	roles := EraRoles{}
	var farSinkDist float64
	for _, m := range markets {
		dist := math.Hypot(m.X, m.Y)
		importer := len(m.Imports) > 0
		exporter := len(m.Exports) > 0
		if dist <= centralBandRadius {
			if isCentralSink(m) {
				roles.CentralParks = append(roles.CentralParks, m.Symbol)
			}
			continue
		}
		// Far band.
		if exporter {
			roles.FarSources = append(roles.FarSources, m.Symbol)
		}
		if importer && (roles.FarSink == "" || dist > farSinkDist || (dist == farSinkDist && m.Symbol < roles.FarSink)) {
			roles.FarSink, farSinkDist = m.Symbol, dist
		}
	}
	roles.Anchors = resolveAnchors(markets)
	// REUSE, not duplicate: the far sink is ONE role. The durable template (a charted
	// PIRATE_BASE marketplace, independent of any dock scan) wins whenever this era has
	// one; the scanned farthest-importer rule above stays as the fallback for an era that
	// does not, so the field is never emptied by the change.
	if roles.Anchors.FarSink != "" {
		roles.FarSink = roles.Anchors.FarSink
	}
	roles.CentralParks = pruneAnchorCoLocated(roles.CentralParks, markets, roles.Anchors)
	sort.Strings(roles.CentralParks)
	sort.Strings(roles.FarSources)
	return roles
}

// isCentralSink reports whether an inner-band waypoint is a contract-delivery sink —
// a central park. It keys on the DURABLE marketplace fact, not transient scanned
// import goods, so an inner-band MARKETPLACE whose per-good market has not been
// dock-scanned this pass is STILL a park. Requiring scanned imports instead admits only
// the handful of currently-scanned central sinks to the standby set (the A/K/G/H pile) and
// leaves the unscanned E/F/D/C central bands empty, piling N idle hulls onto ~4 waypoints
// instead of spreading one-per-band.
//
//   - A scanned importer is a sink.
//   - An unscanned marketplace (charted trait, or trade goods already observed) is
//     ASSUMED a sink — inner-band marketplaces receive contract deliveries; the far
//     source ring is the only exporter cluster, and it is beyond centralBandRadius.
//   - A KNOWN pure exporter (exports, no imports) is a SOURCE, not a sink, excluded
//     even in the inner band (parks are consumers, not factories).
//   - A non-market waypoint (no trait, no trade goods) is never a park.
func isCentralSink(m WaypointMarket) bool {
	hasMarket := m.IsMarketplace || len(m.Imports) > 0 || len(m.Exports) > 0
	if !hasMarket {
		return false
	}
	if len(m.Imports) > 0 {
		return true
	}
	return len(m.Exports) == 0
}

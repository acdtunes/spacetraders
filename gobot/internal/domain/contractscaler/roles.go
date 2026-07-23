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
type WaypointMarket struct {
	Symbol        string
	X, Y          float64
	Exports       []string
	Imports       []string
	IsMarketplace bool
}

// EraRoles is the resolved per-era topology: which of THIS era's waypoints fill
// the fixed roles. Names and numbers regenerate every era, so the roles are keyed
// on geometry + market role, never on a letter or number. CentralParks and
// FarSources are returned in a stable symbol order; FarSink is the single far
// outlier consumer (served LIVE — no J depot), "" when the system has none.
type EraRoles struct {
	CentralParks []string
	FarSources   []string
	FarSink      string
}

// ResolveRoles classifies home-system waypoints into the fixed roles by geometry
// band and market trade role — "a lookup, not a solve":
//   - central parks = inner-band contract sinks (isCentralSink): the delivery targets.
//   - far sources   = far-band exporters: where the fleet sources ores/precious/drugs.
//   - far sink      = the FARTHEST importer: the J-outlier consumer, served live.
//
// The classification is a pure function of position + trade role, so a renamed
// era with identical geometry resolves to identical roles (the stationarity the
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
	sort.Strings(roles.CentralParks)
	sort.Strings(roles.FarSources)
	return roles
}

// isCentralSink reports whether an inner-band waypoint is a contract-delivery sink —
// a central park. It keys on the DURABLE marketplace fact, not transient scanned
// import goods, so an inner-band MARKETPLACE whose per-good market has not been
// dock-scanned this pass is STILL a park (sp-ojp32). This is the fix for the live
// idle-hull pile-up: when classification required scanned imports, only the handful
// of currently-scanned central sinks joined the standby set (the A/K/G/H pile) while
// the unscanned E/F/D/C central bands sat empty, so N idle hulls clustered on ~4
// waypoints instead of spreading one-per-band.
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

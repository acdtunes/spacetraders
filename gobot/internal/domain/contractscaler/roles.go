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
type WaypointMarket struct {
	Symbol  string
	X, Y    float64
	Exports []string
	Imports []string
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
//   - central parks = inner-band sinks (importers): the contract-delivery targets.
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
			if importer {
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

// sortedCopy returns a symbol-sorted copy — a test helper kept in the package so
// role-order assertions do not depend on input order.
func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

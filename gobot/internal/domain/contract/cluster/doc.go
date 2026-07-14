// Package cluster models the "contract cluster" — a first-class, fully
// parametrized entity that localizes the contract-fulfillment supply chain to a
// region so the dominant source->destination HAUL-LEG moves OFF the serialized
// contract critical path and onto parallel stockers.
//
// A contract cluster is the tuple:
//
//	{ source hubs, destination warehouse(s), stocker(s), pinned delivery hull(s) }
//
// EVERY position and count is a PARAMETER (bead sp-u9xa). Nothing about
// placement, sizing, waypoints, or the good-whitelist is hardcoded:
//
//   - hull count, warehouse count (incl. multiple co-located), stocker count,
//     hub count — all arbitrary parameters.
//   - the waypoint of every hub / warehouse / stocker / delivery hull — an
//     arbitrary parameter.
//   - co-location of a delivery hull with its warehouse is EXPECTED (it is what
//     the economy-analyst policy typically recommends because it minimizes
//     cycle-time) but it is a POLICY OUTPUT fed in as config, NOT a design
//     constraint. The mechanism parks each element wherever config says.
//
// Acceptance invariant: instantiating a cluster at a different waypoint, or with
// different hull / warehouse / stocker counts, requires ZERO code change —
// config/params only.
//
// The economic rationale (SpaceTraders travel time is LOAD-INDEPENDENT — empty
// and loaded ships cover the same distance in the same time) is what makes the
// co-located pinned delivery hull the load-bearing element: only a hull already
// AT the destination cluster can deliver locally (~0 haul) while the stocker
// paid the long haul in the background.
package cluster

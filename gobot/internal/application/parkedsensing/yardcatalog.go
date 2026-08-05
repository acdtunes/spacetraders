package parkedsensing

import (
	"context"
	"fmt"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// yardcatalog.go is the FREE half of shipyard discovery: learning what every
// known shipyard SELLS without sending a hull to stand at it.
//
// `GET /systems/{s}/waypoints/{w}/shipyard` answers with TWO things: `shipTypes`,
// what the counter sells, which comes back with NO ship near the waypoint, and
// `ships`, the priced listings, which appear only when a hull of ours is present.
// So this pass reads the CATALOGUE for the yards we hold no reading for while the
// scan rotation (scanner.go) does the PRICING at the yards we occupy. The two are
// complementary, not redundant: availability makes a yard visible at all, and a
// price makes it buyable.
//
// RULINGS #2 — it holds NO cross-tick state. The outstanding set is re-derived
// from the store every tick, so a restart resumes mid-drain with nothing lost and
// a system charted later is picked up on the tick after it lands. RULINGS #4 —
// reading a catalogue spends no credits and touches no purchase guard; the bound
// below is an API-burst pace, never an economic one.

// MaxYardCatalogReads bounds how many shipyard catalogues ONE tick may read. A
// plain const rather than a knob, the same class as MaxExpansionActions: it paces
// a burst of API calls, not economics. These are pure READS — they spend no
// credits, move no hull, write no placement and cannot wedge anything — so the
// only cost being paced is the burst itself, and the pass goes silent on its own
// once every known yard has a catalogue, since those are never enumerated again.
const MaxYardCatalogReads = 8

// OutstandingYard is one CHARTED shipyard waypoint whose catalogue we do not
// hold — a counter we know exists and have never asked what it sells.
type OutstandingYard struct {
	// Waypoint is the shipyard to read.
	Waypoint string
	// System is the system it sits in, carried for logging and for the caller's
	// own bookkeeping; the read itself derives the system from the waypoint.
	System string
	// Frontier ranks how far OUT this yard sits, greater first. It is the adapter's
	// judgement, not this package's; the engine requires only that it be stable
	// within a tick. This queue DRAINS — every successful read removes its yard
	// from the outstanding set permanently — so ordering exists to make a tick's
	// pick reproducible from the store alone, not to protect a head from starving.
	Frontier int
}

// YardCatalogFrontier enumerates the shipyards whose catalogue we do not hold.
// The set is a DIFFERENCE the adapter computes locally — charted SHIPYARD-trait
// waypoints minus the ones already carrying a stored reading — so this port never
// costs an API call and does not grow with how much of the map is already known.
type YardCatalogFrontier interface {
	OutstandingYards(ctx context.Context, playerID int) ([]OutstandingYard, error)
}

// YardCatalogReader reads what ONE shipyard sells and persists it. NO PRESENCE IS
// REQUIRED and none is checked: the availability list survives a presence-less
// GET, which is the whole point of this pass.
//
// It adds no persistence logic of its own — the fleet's shipyard scanner owns what
// a yard reading writes, including the once-per-era heavy-yard milestone — since
// duplicating it here would give the sensing engine a second, divergent definition
// of what a shipyard read records. The SAME port is handed to the scan rotation
// (scanner.go), so a yard read taken with a probe standing on it and one taken
// from across the map write through one code path and can never disagree.
type YardCatalogReader interface {
	ReadCatalog(ctx context.Context, playerID int, waypoint string) error
}

// YardCatalogPorts is everything ReadYardCatalogues needs from the outside world.
type YardCatalogPorts struct {
	Frontier YardCatalogFrontier
	Catalog  YardCatalogReader
}

// YardCatalogReport is one pass's accounting, for the heartbeat.
type YardCatalogReport struct {
	// Outstanding is how many yards were waiting at the start of the pass.
	Outstanding int
	// Read counts the catalogues successfully recorded this tick.
	Read int
	// Failed counts the reads that were attempted and refused. They still cost
	// budget, so they are charged against the per-tick bound.
	Failed int
}

// ReadYardCatalogues records what the known-but-unread shipyards sell, bounded to
// MaxYardCatalogReads reads per tick. An unreadable yard is SKIPPED, not fatal:
// one shipyard the API will not answer for must not cost the tick, nor the other
// yards queued behind it. Only a failure to ENUMERATE fails the pass — that is not
// a finding about any yard but the pass being unable to see its own work, and
// proceeding from it would silently report an empty backlog.
func ReadYardCatalogues(ctx context.Context, p YardCatalogPorts, playerID int) (YardCatalogReport, error) {
	outstanding, err := p.Frontier.OutstandingYards(ctx, playerID)
	if err != nil {
		return YardCatalogReport{}, fmt.Errorf("failed to list the shipyards whose catalogue we do not hold: %w", err)
	}

	queue := orderYardReads(outstanding)
	rep := YardCatalogReport{Outstanding: len(queue)}

	// ATTEMPTS are what the bound counts, not successes. A yard the API refuses has
	// already spent its call, so charging it to the same budget keeps a failing
	// frontier from turning one tick into an unbounded retry storm.
	for attempts := 0; attempts < MaxYardCatalogReads && attempts < len(queue); attempts++ {
		yard := queue[attempts]
		if err := p.Catalog.ReadCatalog(ctx, playerID, yard.Waypoint); err != nil {
			rep.Failed++
			// Named rather than silent: this pass is the only route to a
			// never-visited yard's catalogue, so a yard failing every tick would
			// otherwise be invisible and simply never learned.
			common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf(
				"Failed to read the shipyard catalogue at %s; it stays outstanding: %v", yard.Waypoint, err),
				map[string]interface{}{
					"action":        "parked_sensing_yard_catalog_read_failed",
					"waypoint":      yard.Waypoint,
					"system_symbol": yard.System,
				})
			continue
		}
		rep.Read++
	}
	return rep, nil
}

// orderYardReads puts the outstanding yards in the order this tick should read
// them: FRONTIER-FIRST, with a waypoint-symbol tie-break.
//
// The tie-break is what makes the order TOTAL, and it is load-bearing rather than
// tidy: sort.Slice is not stable and equal frontier ranks are the common case, so
// without it the head of the queue would vary between two runs over the same rows
// and the bounded pick would depend on the order the store handed its rows back
// in. A waypoint listed twice claims one read — nothing in the port CONTRACT
// forbids a duplicate, and one would spend two of the tick's calls on one counter.
func orderYardReads(yards []OutstandingYard) []OutstandingYard {
	seen := make(map[string]bool, len(yards))
	out := make([]OutstandingYard, 0, len(yards))
	for _, yard := range yards {
		if yard.Waypoint == "" || seen[yard.Waypoint] {
			continue
		}
		seen[yard.Waypoint] = true
		out = append(out, yard)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Frontier != out[j].Frontier {
			return out[i].Frontier > out[j].Frontier
		}
		return out[i].Waypoint < out[j].Waypoint
	})
	return out
}

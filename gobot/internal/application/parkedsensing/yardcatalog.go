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
// It exists because the engine treated a shipyard read as presence-gated when
// the API gives half of it away. `GET /systems/{s}/waypoints/{w}/shipyard`
// answers with TWO things: `shipTypes` — what the counter sells — which comes
// back with NO ship anywhere near the waypoint, and `ships` — the priced
// listings — which only appears when a hull of ours is present. Treating the
// whole record as presence-only made "what does this yard sell" a question the
// fleet could only answer by flying somewhere, so it mostly never answered it:
// measured live, 76 waypoints carried a SHIPYARD trait, 23 had ever been
// recorded, and 44 had never been read at all — including three yards selling
// the SHIP_HEAVY_FREIGHTER the fleet was actively hunting.
//
// So this pass reads the CATALOGUE for the yards we hold no reading for, and the
// scan rotation (scanner.go) keeps doing the PRICING at the yards we occupy. The
// two are complementary, not redundant: availability is what makes a yard
// visible at all, and a price is what makes it buyable. Neither substitutes for
// the other.
//
// RULINGS #2 — it holds NO cross-tick state. The outstanding set is re-derived
// from the store every tick, so a restart resumes mid-drain with nothing lost,
// and a system charted three ticks from now is picked up on the tick after it
// lands with no operator action. RULINGS #4 — reading a catalogue spends no
// credits and touches no purchase guard; the bound below is an API-burst pace,
// never an economic one.

// MaxYardCatalogReads bounds how many shipyard catalogues ONE tick may read.
//
// A plain const, deliberately not a knob — the same class as MaxExpansionActions
// and DefaultMaxPlacementActions, and for the same reason: it paces a burst of
// API calls, not economics, and nothing downstream benefits from tuning it.
//
// Eight rather than five or six because these are pure READS. They spend no
// credits, move no hull, write no placement and cannot wedge anything, so the
// only cost being paced is the API burst itself — which is why the number sits
// just above the placement/expansion action caps rather than below them. At the
// 30s sensing tick that is 16 reads a minute, so the 44-yard backlog measured
// live drains in about six ticks and the pass then goes silent on its own: a
// yard whose catalogue we hold is never enumerated again.
const MaxYardCatalogReads = 8

// OutstandingYard is one CHARTED shipyard waypoint whose catalogue we do not
// hold — a counter we know exists and have never asked what it sells.
type OutstandingYard struct {
	// Waypoint is the shipyard to read.
	Waypoint string
	// System is the system it sits in, carried for logging and for the caller's
	// own bookkeeping; the read itself derives the system from the waypoint.
	System string
	// Frontier ranks how far OUT this yard sits, greater first. It is the
	// adapter's judgement, not this package's: the engine only requires that it
	// be stable within a tick, so the ordering below is total and reproducible.
	//
	// Ordering matters less here than anywhere else in the engine, because this
	// queue DRAINS — every successful read removes its yard from the outstanding
	// set permanently — so unlike the PENDING screen sweep there is no permanent
	// head to starve behind. It is still ordered rather than arbitrary so that a
	// tick's pick is reproducible from the store alone.
	Frontier int
}

// YardCatalogFrontier enumerates the shipyards whose catalogue we do not hold.
//
// The set is a DIFFERENCE the adapter computes locally — charted SHIPYARD-trait
// waypoints minus the ones already carrying a stored reading — so this port
// never costs an API call and its cost does not grow with how much of the map is
// already known.
type YardCatalogFrontier interface {
	OutstandingYards(ctx context.Context, playerID int) ([]OutstandingYard, error)
}

// YardCatalogReader reads what ONE shipyard sells and persists it. NO PRESENCE
// IS REQUIRED and none is checked: the availability list survives a
// presence-less GET, which is the whole point of this pass.
//
// It adds no persistence logic of its own — the fleet's shipyard scanner owns
// what a yard reading writes, including the once-per-era heavy-yard milestone —
// for the same reason MarketScanRunner delegates to the market scanner:
// duplicating it here would give the sensing engine a second, divergent
// definition of what a shipyard read records.
//
// The SAME port is handed to the scan rotation (scanner.go), so a yard read
// taken with a probe standing on it and one taken from across the map write
// through one code path and can never disagree.
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
	// Outstanding is how many yards were waiting at the start of the pass — the
	// number an operator watches fall to zero.
	Outstanding int
	// Read counts the catalogues successfully recorded this tick.
	Read int
	// Failed counts the reads that were attempted and refused. They still cost
	// budget, so they are charged against the per-tick bound.
	Failed int
}

// ReadYardCatalogues records what the known-but-unread shipyards sell, bounded to
// MaxYardCatalogReads reads per tick.
//
// An unreadable yard is SKIPPED, not fatal: one shipyard the API will not answer
// for must not cost the tick, and must not cost the other yards queued behind it
// in the same tick either. Only a failure to ENUMERATE fails the pass — that one
// is not a finding about any yard, it is the pass being unable to see its own
// work, and proceeding from it would silently report an empty backlog.
func ReadYardCatalogues(ctx context.Context, p YardCatalogPorts, playerID int) (YardCatalogReport, error) {
	outstanding, err := p.Frontier.OutstandingYards(ctx, playerID)
	if err != nil {
		return YardCatalogReport{}, fmt.Errorf("failed to list the shipyards whose catalogue we do not hold: %w", err)
	}

	queue := orderYardReads(outstanding)
	rep := YardCatalogReport{Outstanding: len(queue)}

	// ATTEMPTS are what the bound counts, not successes. A yard the API refuses
	// has already spent its call, so paying for it out of the same budget is what
	// keeps a systematically failing frontier from turning one tick into an
	// unbounded retry storm against an API that is already saying no.
	for attempts := 0; attempts < MaxYardCatalogReads && attempts < len(queue); attempts++ {
		yard := queue[attempts]
		if err := p.Catalog.ReadCatalog(ctx, playerID, yard.Waypoint); err != nil {
			rep.Failed++
			// Named rather than silent: this pass is the only route to a
			// never-visited yard's catalogue, so a yard failing every tick is
			// otherwise invisible and simply never learned.
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
// The tie-break is what makes the order TOTAL, and it is load-bearing rather
// than tidy. sort.Slice is not stable and equal frontier ranks are the common
// case, so without it the head of the queue would vary between two runs over the
// same rows — untestable, and it would make the bounded pick depend on the order
// the store happened to hand its rows back in.
//
// A waypoint listed twice claims one read. No adapter returns duplicates today,
// but nothing in the port CONTRACT promises it, and a duplicate would spend two
// of the tick's eight calls on one counter.
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

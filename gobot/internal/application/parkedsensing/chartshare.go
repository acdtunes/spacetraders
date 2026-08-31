package parkedsensing

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	domainSystem "github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// chartshare.go decides which of a dark system's outstanding stops each hull of
// its charting crew owns, and in what order it works them.
//
// THE TOUR IS SOLVED ONCE PER CREW AND STORED. A charting tour runs for hours and
// a hull picks its next stop off its share every turn, so a share that could be
// re-derived differently between turns is a share two hulls fight over — and, for a
// lone hull, a walk that oscillates between two waypoints and never finishes. The
// stored assignment is therefore the durable input the tour reads, and the only
// things that invalidate it are facts about the CREW: a hull joining, a hull
// leaving, or a stop no hull owns.
//
// A LONE HULL IS ROUTED AND STORED ON THE SAME PATH, though not by the same solver.
// Partitioning is not routing: one hull has no partition to cut, but its walk is
// still a travelling-salesman problem, and the catalog's (tier, symbol) order
// answers it with the alphabet. The systems earning one hull pay most for that: one
// is what a long walk earns.

// chartPartitionTimeout bounds the fleet-partition call. A plain constant like
// MaxExpansionActions and for the same reason — it paces the tick. It is a real
// CUT-OFF and not only a hang-catcher: the solver's cost grows with the stop list,
// so a large enough system runs past this and takes the declared sector fallback.
const chartPartitionTimeout = time.Minute

// chartTourUnkinkPasses bounds the crossing-removal sweeps over a lone hull's walk,
// and legEpsilon is the leg difference below which two routings count as the same
// one. Both pace the tick rather than the economics: the sweep already stops on the
// first pass that improves nothing, so the bound only ever catches float noise
// trading two orderings back and forth.
const (
	chartTourUnkinkPasses = 8
	legEpsilon            = 1e-9
)

// errNoFleetPartitioner is an unwired partitioner reported as the same case as an
// unreachable one, so the fallback has ONE trigger rather than two.
var errNoFleetPartitioner = errors.New("no fleet partitioner is wired")

// shareBook is the tick's view of the stored crew partitions. It is read lazily —
// most systems are worked by one hull and never ask — and mutated by every
// re-solve, so a later pass of the same tick reads the partition the earlier one
// wrote rather than solving again.
type shareBook struct {
	byShip map[string]ChartShare
	loaded bool
}

func newShareBook() *shareBook {
	return &shareBook{byShip: map[string]ChartShare{}}
}

// replace records a freshly solved partition over the shares it supersedes.
func (b *shareBook) replace(shares []ChartShare) {
	for _, share := range shares {
		b.byShip[share.Ship] = share
	}
}

// fresh reports whether the stored partition still describes THIS crew over THESE
// stops: every hull aboard holds a share solved for this exact membership, and no
// outstanding stop is owned by nobody. Both failures leave work undone — a hull
// with no share stands down with the system still dark, an unowned stop is never
// charted at all and pins the system's count above zero forever.
func (b *shareBook) fresh(system string, crew []string, stops []ChartStop) bool {
	key := crewKey(crew)
	owned := make(map[string]bool, len(stops))
	for _, ship := range crew {
		share, held := b.byShip[ship]
		if !held || share.System != system || share.CrewKey != key {
			return false
		}
		for _, waypoint := range share.Waypoints {
			owned[waypoint] = true
		}
	}
	for _, stop := range stops {
		if !owned[stop.Waypoint] {
			return false
		}
	}
	return true
}

// own is the live remainder of one hull's stored share: its own stops, in its own
// order, minus everything already charted. A stop leaving the outstanding set is
// the benign already-charted path — charted by this hull, by a crewmate, or by
// anybody else — and it never moves a stop between hulls.
func (b *shareBook) own(ship string, stops []ChartStop) []string {
	share, held := b.byShip[ship]
	if !held {
		return nil
	}
	live := make(map[string]bool, len(stops))
	for _, stop := range stops {
		live[stop.Waypoint] = true
	}
	out := make([]string, 0, len(share.Waypoints))
	for _, waypoint := range share.Waypoints {
		if live[waypoint] {
			out = append(out, waypoint)
		}
	}
	return out
}

// shareFor is the outstanding stops `ship` owns of `system`, in working order.
//
// A HULL OFF THE CREW OWNS NOTHING. The roster is what grants a share, so a stale
// errand can neither fall through to the whole system nor keep flying a share the
// crew it was solved for no longer holds it to.
//
// A LONE HULL OWNS THE WHOLE CATALOG, and takes it through the same freshness test
// and the same stored row a crew does: its crew of one is a crew and its share is a
// share. Only how the order is arrived at differs (routeShares).
func (t *expandTick) shareFor(ctx context.Context, system, ship string, stops []ChartStop) ([]string, error) {
	crew := t.roster.crew(system)
	if !contains(crew, ship) {
		return nil, nil
	}
	if err := t.loadShares(ctx); err != nil {
		return nil, err
	}
	if !t.shares.fresh(system, crew, stops) {
		solved := t.solveShares(ctx, system, crew, stops)
		if err := t.p.Ledger.SetChartShares(ctx, t.playerID, system, solved); err != nil {
			return nil, fmt.Errorf("failed to record the charting partition of %q: %w", system, err)
		}
		t.shares.replace(solved)
	}
	return t.shares.own(ship, stops), nil
}

// loadShares reads the stored partitions once per tick.
func (t *expandTick) loadShares(ctx context.Context) error {
	if t.shares.loaded {
		return nil
	}
	stored, err := t.p.Ledger.ChartShares(ctx, t.playerID)
	if err != nil {
		return fmt.Errorf("failed to read the stored charting partitions: %w", err)
	}
	t.shares.replace(stored)
	t.shares.loaded = true
	return nil
}

// solveShares cuts a crew's stops into per-hull tours.
func (t *expandTick) solveShares(ctx context.Context, system string, crew []string, stops []ChartStop) []ChartShare {
	byShip := t.routeShares(ctx, system, crew, stops)
	key := crewKey(crew)
	shares := make([]ChartShare, 0, len(crew))
	for _, ship := range crew {
		shares = append(shares, ChartShare{
			Ship: ship, System: system, Waypoints: byShip[ship], CrewKey: key,
		})
	}
	return shares
}

// routeShares puts each hull's stops in the order it works them.
//
// A CREW ASKS THE VRP, because cutting the work between hulls is the part no local
// walk can do, and its answer falls open to angular sectors — named once so a
// routing service down for good cannot pass for a working solve. Fail-open is the
// right polarity for the same reason it is on the route ETA: a tour is a scheduling
// nicety, and refusing to produce one would stop the fleet charting at all.
//
// A LONE HULL IS ROUTED HERE INSTEAD, and never asks the VRP at all. There is no
// partition to cut, so the only question left is the walk — and the partitioner
// answers no call in under half a minute and degrades sharply past that (sp-ev79y),
// which the serial sensing tick cannot absorb for an answer a local walk gets within
// a few percent of.
func (t *expandTick) routeShares(
	ctx context.Context, system string, crew []string, stops []ChartStop,
) map[string][]string {
	if len(crew) == 1 {
		return map[string][]string{crew[0]: orderByTier(t.soloTour(ctx, crew[0], stops), stops)}
	}
	byShip, err := t.solvePartition(ctx, system, crew, stops)
	if err != nil {
		logging.LoggerFromContext(ctx).Log("WARN", fmt.Sprintf(
			"charting crew partitioned by angular sector because the fleet partitioner could not answer: %v", err),
			map[string]interface{}{
				"action": "parked_sensing_chart_partition_fallback",
				"system": system,
				"crew":   len(crew),
				"stops":  len(stops),
				"error":  err.Error(),
			})
		byShip = partitionBySector(stops, crew)
	}
	return byShip
}

// soloTour is one hull's walk of a whole system: nearest neighbour from where it
// stands, then the crossings taken back out.
//
// A HULL WE CANNOT LOCATE WALKS FROM THE SYSTEM CENTRE. Stop coordinates are
// system-relative, so the origin is a real point on the same plane: an arbitrary
// start to a walk that is still geometric, where the catalog's order is not.
func (t *expandTick) soloTour(ctx context.Context, ship string, stops []ChartStop) []string {
	var x, y float64
	if pos, err := t.p.Ships.ShipAt(ctx, t.playerID, ship); err == nil && pos.Found {
		x, y = pos.X, pos.Y
	}
	return waypointsOf(unkink(nearestNeighbourWalk(stops, x, y)))
}

// nearestNeighbourWalk orders stops into a walk that always steps to the nearest one
// not yet taken, from (x, y).
//
// EVERY TIE BREAKS ON SYMBOL, which is what makes the walk a function of the stop SET
// rather than of the order it arrived in: candidates are ranked by symbol first and
// only a STRICTLY nearer one displaces the incumbent, so equidistant stops resolve
// the same way on every tick. Without that a tour re-derived next tick could lead
// with a different waypoint and leave the hull flying between two of them forever.
func nearestNeighbourWalk(stops []ChartStop, x, y float64) []ChartStop {
	remaining := append([]ChartStop(nil), stops...)
	sort.Slice(remaining, func(i, j int) bool { return remaining[i].Waypoint < remaining[j].Waypoint })

	out := make([]ChartStop, 0, len(remaining))
	for len(remaining) > 0 {
		next, nearest := 0, squaredRange(remaining[0], x, y)
		for i := 1; i < len(remaining); i++ {
			if reach := squaredRange(remaining[i], x, y); reach < nearest {
				next, nearest = i, reach
			}
		}
		step := remaining[next]
		out = append(out, step)
		x, y = step.X, step.Y
		remaining = append(remaining[:next], remaining[next+1:]...)
	}
	return out
}

// unkink takes the crossings back out of a walk: wherever two legs cross, flying the
// span between them backwards is shorter. A nearest-neighbour walk always leaves
// some, because the stops it passed over early are exactly what its last legs must
// come back for.
//
// DETERMINISTIC BY CONSTRUCTION: the spans are scanned in one fixed order, only a
// STRICT improvement is taken, and the sweep is bounded — so the same walk polishes
// to the same walk, and a pair the arithmetic cannot separate is left alone rather
// than flipped back and forth.
func unkink(tour []ChartStop) []ChartStop {
	out := append([]ChartStop(nil), tour...)
	for pass := 0; pass < chartTourUnkinkPasses; pass++ {
		improved := false
		for i := 0; i+2 < len(out); i++ {
			for j := i + 2; j+1 < len(out); j++ {
				if legLength(out[i], out[i+1])+legLength(out[j], out[j+1]) >
					legLength(out[i], out[j])+legLength(out[i+1], out[j+1])+legEpsilon {
					reverseSpan(out[i+1 : j+1])
					improved = true
				}
			}
		}
		if !improved {
			break
		}
	}
	return out
}

func reverseSpan(span []ChartStop) {
	for l, r := 0, len(span)-1; l < r; l, r = l+1, r-1 {
		span[l], span[r] = span[r], span[l]
	}
}

func legLength(from, to ChartStop) float64 { return math.Hypot(to.X-from.X, to.Y-from.Y) }

// squaredRange compares distances without the square root, which orders them
// identically and leaves no rounding for two evaluations of the same pair to
// disagree over.
func squaredRange(stop ChartStop, x, y float64) float64 {
	dx, dy := stop.X-x, stop.Y-y
	return dx*dx + dy*dy
}

// solvePartition asks the VRP for the crew's assignment and returns it only if it
// is a partition of exactly these stops over exactly this crew.
//
// THE CHECK IS THE CONTRACT. The solver drops a node it cannot place rather than
// failing, so an unchecked answer can leave a stop owned by nobody — never
// charted, the system's count never reaching zero, and the system re-seeded for
// as long as the era lasts.
func (t *expandTick) solvePartition(
	ctx context.Context, system string, crew []string, stops []ChartStop,
) (map[string][]string, error) {
	if t.p.Partitioner == nil {
		return nil, errNoFleetPartitioner
	}
	configs, err := t.crewConfigs(ctx, crew)
	if err != nil {
		return nil, err
	}

	bounded, cancel := context.WithTimeout(ctx, chartPartitionTimeout)
	defer cancel()
	resp, err := t.p.Partitioner.PartitionFleet(bounded, &routing.VRPRequest{
		SystemSymbol:    system,
		ShipSymbols:     crew,
		MarketWaypoints: waypointsOf(stops),
		ShipConfigs:     configs,
		AllWaypoints:    waypointDataOf(stops),
	})
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, errors.New("the fleet partitioner returned no assignment")
	}
	return checkedPartition(resp, crew, stops)
}

// crewConfigs is where each hull of the crew stands, with the hull's own flight
// characteristics, so the solver prices the walk each of them actually faces. A
// hull the ships table cannot locate refuses the whole solve rather than being
// dropped: a crew short one hull would be partitioned as a smaller crew, and the
// missing hull would then own nothing and stand down mid-tour.
func (t *expandTick) crewConfigs(ctx context.Context, crew []string) (map[string]*routing.ShipConfigData, error) {
	configs := make(map[string]*routing.ShipConfigData, len(crew))
	for _, ship := range crew {
		pos, err := t.p.Ships.ShipAt(ctx, t.playerID, ship)
		if err != nil {
			return nil, fmt.Errorf("failed to locate charting hull %q: %w", ship, err)
		}
		if !pos.Found {
			return nil, fmt.Errorf("charting hull %q is not in the ships table", ship)
		}
		configs[ship] = &routing.ShipConfigData{
			CurrentLocation: pos.Waypoint,
			FuelCapacity:    pos.FuelCapacity,
			EngineSpeed:     pos.EngineSpeed,
		}
	}
	return configs, nil
}

// checkedPartition turns a solved assignment into per-hull tours, refusing any
// answer that is not a partition: a stop owned twice, a stop owned by nobody, or
// work handed to a hull the roster does not have aboard.
//
// A hull with an EMPTY tour is legitimate and comes back missing from the answer
// entirely — a crew larger than the work left. Its share is empty, it finishes on
// its next turn, and the crew shrinks.
func checkedPartition(resp *routing.VRPResponse, crew []string, stops []ChartStop) (map[string][]string, error) {
	aboard := make(map[string]bool, len(crew))
	for _, ship := range crew {
		aboard[ship] = true
	}
	outstanding := make(map[string]bool, len(stops))
	for _, stop := range stops {
		outstanding[stop.Waypoint] = true
	}

	owner := make(map[string]string, len(stops))
	byShip := make(map[string][]string, len(crew))
	for ship, tour := range resp.Assignments {
		if !aboard[ship] {
			return nil, fmt.Errorf("the partition gives work to %q, which is not on the crew", ship)
		}
		if tour == nil {
			continue
		}
		for _, waypoint := range tour.Waypoints {
			if !outstanding[waypoint] {
				return nil, fmt.Errorf("the partition gives %q a waypoint that is not outstanding: %s", ship, waypoint)
			}
			if held, taken := owner[waypoint]; taken {
				return nil, fmt.Errorf("the partition gives %s to both %q and %q", waypoint, held, ship)
			}
			owner[waypoint] = ship
			byShip[ship] = append(byShip[ship], waypoint)
		}
	}
	if len(owner) != len(outstanding) {
		return nil, fmt.Errorf("the partition leaves %d of %d outstanding stops unowned",
			len(outstanding)-len(owner), len(outstanding))
	}
	for ship := range byShip {
		byShip[ship] = orderByTier(byShip[ship], stops)
	}
	return byShip, nil
}

// orderByTier re-groups one hull's tour by charting VALUE TIER, keeping the
// solved order inside each tier. The solver optimises travel time and knows
// nothing of what a waypoint type is worth, so without this a hull walks its
// share's shipyard past on the way to an asteroid; with it, the walk within a
// tier is still the solver's.
func orderByTier(tour []string, stops []ChartStop) []string {
	priority := make(map[string]int, len(stops))
	for _, stop := range stops {
		priority[stop.Waypoint] = stop.Priority
	}
	ordered := append([]string(nil), tour...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return priority[ordered[i]] < priority[ordered[j]]
	})
	return ordered
}

func waypointsOf(stops []ChartStop) []string {
	out := make([]string, 0, len(stops))
	for _, stop := range stops {
		out = append(out, stop.Waypoint)
	}
	return out
}

// waypointDataOf is the coordinate set the solver prices arcs over. An uncharted
// waypoint's traits are unknown until it is charted, so none of them is offered as
// a refuelling stop — the honest reading, and the conservative one.
func waypointDataOf(stops []ChartStop) []*domainSystem.WaypointData {
	out := make([]*domainSystem.WaypointData, 0, len(stops))
	for _, stop := range stops {
		out = append(out, &domainSystem.WaypointData{Symbol: stop.Waypoint, X: stop.X, Y: stop.Y})
	}
	return out
}

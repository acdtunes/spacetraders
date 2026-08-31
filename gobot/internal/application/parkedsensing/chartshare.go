package parkedsensing

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	domainSystem "github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// chartshare.go decides which of a dark system's outstanding stops each hull of
// its charting crew owns, and in what order it works them.
//
// THE PARTITION IS SOLVED ONCE PER CREW AND STORED. A charting tour runs for hours
// and a hull picks its next stop off its share every turn, so a share that could
// be re-derived differently between turns is a share two hulls fight over. The
// stored assignment is therefore the durable input the tour reads, and the only
// things that invalidate it are facts about the CREW: a hull joining, a hull
// leaving, or a stop no hull owns.
//
// A ONE-HULL SYSTEM NEVER REACHES ANY OF THIS. Its hull owns the whole catalog in
// the catalog's own order, so there is nothing to solve and nothing to store.

// chartPartitionTimeout bounds the fleet-partition call. A plain constant like
// MaxExpansionActions and for the same reason — it paces the tick. It is a real
// CUT-OFF and not only a hang-catcher: the solver's cost grows with the stop list,
// so a large enough system runs past this and takes the declared sector fallback.
const chartPartitionTimeout = time.Minute

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
// A LONE HULL OWNS THE WHOLE CATALOG, and no partition is solved or stored for it:
// that is the single-hull tour, and the hull budget's cap of one is this branch for
// every system in the fleet.
func (t *expandTick) shareFor(ctx context.Context, system, ship string, stops []ChartStop) ([]string, error) {
	crew := t.roster.crew(system)
	if !contains(crew, ship) {
		return nil, nil
	}
	if len(crew) == 1 {
		return waypointsOf(stops), nil
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
//
// The fleet-partitioning VRP is asked first and its answer is checked to BE a
// partition before it is trusted; anything else — an unwired or unreachable
// service, a refused solve, an answer that drops or double-books a stop — falls
// open to angular sectors, named once so a service down for good cannot pass for
// a working solve. Fail-open is the right polarity here for the same reason it is
// on the route ETA: a partition is a scheduling nicety, and refusing to produce
// one would stop the fleet charting at all.
func (t *expandTick) solveShares(ctx context.Context, system string, crew []string, stops []ChartStop) []ChartShare {
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
	key := crewKey(crew)
	shares := make([]ChartShare, 0, len(crew))
	for _, ship := range crew {
		shares = append(shares, ChartShare{
			Ship: ship, System: system, Waypoints: byShip[ship], CrewKey: key,
		})
	}
	return shares
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

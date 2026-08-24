package routing

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// flightMode carries the two constants every leg cost derives from: seconds per
// distance unit at unit engine speed, and fuel per distance unit.
type flightMode struct {
	name     string
	timeMul  int
	fuelRate float64
}

// Mode order is the order a hop's alternatives are generated in, which is also
// the order equal-cost candidates are explored in.
const (
	modeBurn = iota
	modeCruise
	modeDrift
	modeNone = -1
)

var flightModes = [...]flightMode{
	modeBurn:   {name: "BURN", timeMul: 15, fuelRate: 2.0},
	modeCruise: {name: "CRUISE", timeMul: 31, fuelRate: 1.0},
	modeDrift:  {name: "DRIFT", timeMul: 26, fuelRate: 0.003},
}

const (
	// fuelSafetyMargin is the reserve a hop must leave in the tank. Only the last
	// hop, the one that lands on the goal, may spend the tank down to empty.
	fuelSafetyMargin = 4

	// driftTimePenalty ranks a DRIFT hop below every fuelled alternative on the
	// search key without removing it, so drifting stays reachable as the last
	// resort out of a starved tank and is never chosen while fuel can move the
	// hull instead. FuelEfficient drops the penalty for hulls that would rather
	// spend hours than fuel.
	driftTimePenalty = 100000

	// orbitalHopSeconds prices a hop between waypoints sharing coordinates, which
	// costs no fuel and no real distance but is not free in time.
	orbitalHopSeconds = 1

	// startRefuelFraction tops the tank up at the origin whenever it sits below
	// this share of capacity, so a route does not begin by spending its margin.
	startRefuelFraction = 0.9

	// fuelStateBucket coarsens the fuel axis of the search state. Fuel levels
	// within one bucket reach the same destinations, so collapsing them bounds
	// the state space at a size that stays searchable on a full system graph.
	fuelStateBucket = 10

	// ctxCheckInterval bounds how long the search runs past a cancelled context.
	// Checking every pop costs more than the search itself on a normal graph.
	ctxCheckInterval = 512

	defaultFlightMode = "CRUISE"
)

// FuelStatePlanner plans a fuel-constrained route as a Dijkstra search over
// (waypoint, fuel) states: every hop may be flown in any mode the tank affords,
// and standing at a fuel-selling waypoint is itself an edge that refills the tank
// at zero time cost. Minimising total travel time therefore chooses the refuel
// stops and the per-hop flight modes together rather than in sequence.
//
// The graph is complete — every waypoint is reachable from every other — so
// "neighbour" means "every other waypoint in the system", and the only thing
// bounding a hop is whether the tank covers it.
//
// Pure and stateless: the whole graph arrives in the request, so a planner value
// is safe to share across goroutines and holds nothing between calls.
type FuelStatePlanner struct{}

// NewFuelStatePlanner returns the in-process route planner.
func NewFuelStatePlanner() *FuelStatePlanner { return &FuelStatePlanner{} }

// PlanRoute implements RoutePlanner. An unreachable goal, an unknown endpoint or
// a cancelled context are errors; a start that already equals the goal succeeds
// with an empty step list.
func (p *FuelStatePlanner) PlanRoute(ctx context.Context, req *RouteRequest) (*RouteResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("routing failed: nil route request")
	}
	plan, err := planFuelStateRoute(ctx, req)
	if err != nil {
		return nil, err
	}
	steps := make([]*RouteStepData, len(plan.Steps))
	for i, step := range plan.Steps {
		mode := step.Mode
		if mode == "" {
			mode = defaultFlightMode
		}
		steps[i] = &RouteStepData{
			Action:      step.Action,
			Waypoint:    step.Waypoint,
			FuelCost:    step.FuelCost,
			TimeSeconds: step.TimeSeconds,
			Mode:        mode,
		}
	}
	return &RouteResponse{
		Steps:            steps,
		TotalFuelCost:    plan.TotalFuelCost,
		TotalTimeSeconds: plan.TotalTimeSeconds,
		TotalDistance:    plan.TotalDistance,
	}, nil
}

// routeStep is one step of a planned route carrying every field the search
// derives, including the per-step distance and refuel amount that the
// RouteResponse projection drops.
type routeStep struct {
	Action       RouteAction
	Waypoint     string
	FuelCost     int
	TimeSeconds  int
	Distance     float64
	Mode         string
	RefuelAmount int
}

// plannedRoute is the search result before projection onto the port's DTO.
type plannedRoute struct {
	Steps            []routeStep
	TotalFuelCost    int
	TotalTimeSeconds int
	TotalDistance    float64
}

func planFuelStateRoute(ctx context.Context, req *RouteRequest) (*plannedRoute, error) {
	graph := newPlannerGraph(req.Waypoints)
	start, startKnown := graph.index[req.StartWaypoint]
	goal, goalKnown := graph.index[req.GoalWaypoint]
	if !startKnown || !goalKnown {
		return nil, noRouteError(req)
	}
	if start == goal {
		return &plannedRoute{}, nil
	}
	// A hull with no tank (a probe) neither burns fuel nor can refuel, so the
	// direct hop is always its optimal route and the fuel search has nothing to
	// decide.
	if req.FuelCapacity == 0 {
		return directRoute(graph, start, goal, req.EngineSpeed), nil
	}
	return newRouteSearch(graph, req).run(ctx, start, goal)
}

// routeSearch holds one PlanRoute call's search state.
type routeSearch struct {
	graph *plannerGraph
	req   *RouteRequest

	queue routeQueue
	links []pathLink
	seq   int64

	// buckets is the fuel axis of the state space; a state's slot is
	// node*buckets + fuel/fuelStateBucket.
	buckets int
	// expanded marks the states already popped and explored. A state is explored
	// at most once because pops come out in non-decreasing time order, so the
	// first pop of a state is its cheapest.
	expanded []bool
	// queuedAt is the cheapest time yet queued for each state, and the reason the
	// search stays small: a candidate no cheaper than one already queued for the
	// same state cannot survive its own pop, so it is never queued at all.
	queuedAt []int
}

func newRouteSearch(graph *plannerGraph, req *RouteRequest) *routeSearch {
	tank := req.FuelCapacity
	if req.CurrentFuel > tank {
		tank = req.CurrentFuel
	}
	buckets := tank/fuelStateBucket + 2
	slots := len(graph.symbols) * buckets
	search := &routeSearch{
		graph:    graph,
		req:      req,
		queue:    make(routeQueue, 0, 1024),
		links:    make([]pathLink, 0, 1024),
		buckets:  buckets,
		expanded: make([]bool, slots),
		queuedAt: make([]int, slots),
	}
	for i := range search.queuedAt {
		search.queuedAt[i] = math.MaxInt
	}
	return search
}

func (s *routeSearch) slot(node, fuel int) int {
	bucket := fuel / fuelStateBucket
	if bucket < 0 {
		bucket = 0
	} else if bucket >= s.buckets {
		bucket = s.buckets - 1
	}
	return node*s.buckets + bucket
}

// push queues a candidate reached from parent by link, unless the goal is
// already reachable no later by a candidate queued for the same state. The
// dominance test runs before the link is recorded, so a rejected candidate costs
// nothing at all.
func (s *routeSearch) push(entry searchEntry, parent int32, link pathLink, goal int) {
	if entry.node != goal {
		slot := s.slot(entry.node, entry.fuel)
		if entry.totalTime >= s.queuedAt[slot] {
			return
		}
		s.queuedAt[slot] = entry.totalTime
	}
	link.parent = parent
	s.links = append(s.links, link)
	entry.path = int32(len(s.links) - 1)
	entry.seq = s.seq
	s.seq++
	s.queue.push(entry)
}

func (s *routeSearch) run(ctx context.Context, start, goal int) (*plannedRoute, error) {
	graph, req := s.graph, s.req
	s.queue.push(searchEntry{node: start, fuel: req.CurrentFuel, path: -1})
	s.seq++
	s.queuedAt[s.slot(start, req.CurrentFuel)] = 0

	for pops := 0; s.queue.len() > 0; pops++ {
		if pops%ctxCheckInterval == 0 && ctx != nil {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		entry := s.queue.pop()

		if entry.node == goal {
			return s.buildPlan(start, entry), nil
		}
		slot := s.slot(entry.node, entry.fuel)
		if s.expanded[slot] {
			continue
		}
		s.expanded[slot] = true

		// Leaving the origin on a part tank is the one case decided ahead of the
		// search rather than by it: top up first whenever the tank cannot cruise
		// straight to the goal or is merely below its start threshold, and explore
		// nothing else from this state.
		if entry.node == start && entry.pathLen == 0 && graph.hasFuel[entry.node] && entry.fuel < req.FuelCapacity {
			cruiseToGoal := fuelCost(graph.distance(entry.node, goal), flightModes[modeCruise])
			if entry.fuel < cruiseToGoal || entry.fuel < int(float64(req.FuelCapacity)*startRefuelFraction) {
				s.pushRefuel(entry, goal)
				continue
			}
		}

		if graph.hasFuel[entry.node] && entry.fuel < req.FuelCapacity {
			s.pushRefuel(entry, goal)
		}

		row := graph.distanceRow(entry.node)
		for next, distance := range row {
			if next == entry.node {
				continue
			}
			if distance == 0 {
				s.push(searchEntry{
					totalTime: entry.totalTime + orbitalHopSeconds,
					node:      next,
					fuel:      entry.fuel,
					fuelUsed:  entry.fuelUsed,
					pathLen:   entry.pathLen + 1,
				}, entry.path, pathLink{
					action:  int8(RouteActionTravel),
					node:    int32(next),
					mode:    modeCruise,
					seconds: orbitalHopSeconds,
				}, goal)
				continue
			}

			options, count := viableModes(distance, entry.fuel, next == goal, req.PreferCruise)
			for _, option := range options[:count] {
				seconds := travelTime(distance, flightModes[option.mode], req.EngineSpeed)
				if option.mode == modeDrift && !req.FuelEfficient {
					seconds += driftTimePenalty
				}
				s.push(searchEntry{
					totalTime: entry.totalTime + seconds,
					node:      next,
					fuel:      entry.fuel - option.cost,
					fuelUsed:  entry.fuelUsed + option.cost,
					pathLen:   entry.pathLen + 1,
				}, entry.path, pathLink{
					action:   int8(RouteActionTravel),
					node:     int32(next),
					mode:     int8(option.mode),
					fuelCost: int32(option.cost),
					seconds:  int32(seconds),
				}, goal)
			}
		}
	}

	return nil, noRouteError(s.req)
}

func (s *routeSearch) pushRefuel(entry searchEntry, goal int) {
	s.push(searchEntry{
		totalTime: entry.totalTime,
		node:      entry.node,
		fuel:      s.req.FuelCapacity,
		fuelUsed:  entry.fuelUsed,
		pathLen:   entry.pathLen + 1,
	}, entry.path, pathLink{
		action: int8(RouteActionRefuel),
		node:   int32(entry.node),
		mode:   modeNone,
		refuel: int32(s.req.FuelCapacity - entry.fuel),
	}, goal)
}

// buildPlan walks the winning entry's parent chain back to the origin. A travel
// step's distance is recovered from the pair of waypoints it joins rather than
// carried through the search.
func (s *routeSearch) buildPlan(start int, entry searchEntry) *plannedRoute {
	chain := make([]pathLink, entry.pathLen)
	link := entry.path
	for i := int(entry.pathLen) - 1; i >= 0; i-- {
		chain[i] = s.links[link]
		link = s.links[link].parent
	}

	plan := &plannedRoute{
		Steps:            make([]routeStep, len(chain)),
		TotalFuelCost:    entry.fuelUsed,
		TotalTimeSeconds: entry.totalTime,
	}
	from := start
	for i, hop := range chain {
		action := RouteAction(hop.action)
		step := routeStep{
			Action:       action,
			Waypoint:     s.graph.symbols[hop.node],
			FuelCost:     int(hop.fuelCost),
			TimeSeconds:  int(hop.seconds),
			RefuelAmount: int(hop.refuel),
		}
		if hop.mode != modeNone {
			step.Mode = flightModes[hop.mode].name
		}
		if action == RouteActionTravel {
			step.Distance = s.graph.distance(from, int(hop.node))
			plan.TotalDistance += step.Distance
			from = int(hop.node)
		}
		plan.Steps[i] = step
	}
	return plan
}

// modeOption is one flight mode the tank can afford for a hop, with its cost.
type modeOption struct {
	mode int
	cost int
}

// viableModes ranks the modes a hop may be flown in. BURN and CRUISE each need
// the safety margin on top of their own fuel, relaxed to bare sufficiency for the
// hop that lands on the goal — arriving on empty is acceptable, stranding
// mid-route is not. DRIFT appears only when nothing else fits, so a route reaches
// for it exactly when the alternative is not moving at all.
func viableModes(distance float64, fuel int, isGoal, preferCruise bool) ([2]modeOption, int) {
	var options [2]modeOption
	count := 0
	affords := func(cost int) bool { return fuel >= cost+fuelSafetyMargin || (isGoal && fuel >= cost) }

	if !preferCruise {
		if burn := fuelCost(distance, flightModes[modeBurn]); affords(burn) {
			options[count] = modeOption{mode: modeBurn, cost: burn}
			count++
		}
	}
	if cruise := fuelCost(distance, flightModes[modeCruise]); affords(cruise) {
		options[count] = modeOption{mode: modeCruise, cost: cruise}
		count++
	}
	if count == 0 {
		if drift := fuelCost(distance, flightModes[modeDrift]); fuel >= drift {
			options[count] = modeOption{mode: modeDrift, cost: drift}
			count++
		}
	}
	return options, count
}

// directRoute is the whole route for a hull that carries no fuel.
func directRoute(graph *plannerGraph, start, goal, engineSpeed int) *plannedRoute {
	distance := graph.distance(start, goal)
	seconds := orbitalHopSeconds
	if distance != 0 {
		seconds = travelTime(distance, flightModes[modeCruise], engineSpeed)
	}
	return &plannedRoute{
		Steps: []routeStep{{
			Action:      RouteActionTravel,
			Waypoint:    graph.symbols[goal],
			TimeSeconds: seconds,
			Distance:    distance,
			Mode:        flightModes[modeCruise].name,
		}},
		TotalTimeSeconds: seconds,
		TotalDistance:    distance,
	}
}

func noRouteError(req *RouteRequest) error {
	return fmt.Errorf("routing failed: no path found from %s to %s", req.StartWaypoint, req.GoalWaypoint)
}

// fuelCost is the fuel a hop of this distance burns in this mode. A hop that
// moves the hull at all costs at least one unit.
func fuelCost(distance float64, mode flightMode) int {
	if distance == 0 {
		return 0
	}
	if cost := int(math.Ceil(distance * mode.fuelRate)); cost > 1 {
		return cost
	}
	return 1
}

// travelTime is the seconds a hop of this distance takes in this mode. A hop that
// moves the hull at all takes at least one second.
func travelTime(distance float64, mode flightMode, engineSpeed int) int {
	if distance == 0 {
		return 0
	}
	if engineSpeed < 1 {
		engineSpeed = 1
	}
	if seconds := int(distance * float64(mode.timeMul) / float64(engineSpeed)); seconds > 1 {
		return seconds
	}
	return 1
}

// plannerGraph is the request's waypoint list indexed for the search. Symbols are
// held in lexicographic order and that order IS the neighbour scan order, which
// is what makes an equal-cost tie resolve the same way on every run: the search
// key breaks ties on generation order, and generation order follows the symbol
// order rather than any map iteration.
type plannerGraph struct {
	symbols []string
	xs      []float64
	ys      []float64
	hasFuel []bool
	index   map[string]int
	rows    [][]float64
}

func newPlannerGraph(waypoints []*system.WaypointData) *plannerGraph {
	byName := make(map[string]*system.WaypointData, len(waypoints))
	for _, waypoint := range waypoints {
		if waypoint == nil {
			continue
		}
		byName[waypoint.Symbol] = waypoint
	}
	symbols := make([]string, 0, len(byName))
	for symbol := range byName {
		symbols = append(symbols, symbol)
	}
	sort.Strings(symbols)

	graph := &plannerGraph{
		symbols: symbols,
		xs:      make([]float64, len(symbols)),
		ys:      make([]float64, len(symbols)),
		hasFuel: make([]bool, len(symbols)),
		index:   make(map[string]int, len(symbols)),
		rows:    make([][]float64, len(symbols)),
	}
	for i, symbol := range symbols {
		waypoint := byName[symbol]
		graph.xs[i] = waypoint.X
		graph.ys[i] = waypoint.Y
		graph.hasFuel[i] = waypoint.HasFuel
		graph.index[symbol] = i
	}
	return graph
}

// distance is the straight-line separation of two waypoints. The sqrt-of-sum
// form is deliberate over math.Hypot: on integer coordinates it is the correctly
// rounded result, while Hypot's scaled form lands an ulp away often enough to
// flip the ceiling and truncation that fuel and time are derived from.
func (g *plannerGraph) distance(from, to int) float64 {
	dx := g.xs[to] - g.xs[from]
	dy := g.ys[to] - g.ys[from]
	return math.Sqrt(dx*dx + dy*dy)
}

// distanceRow memoises one waypoint's distance to every other. A waypoint is
// expanded once per reachable fuel level, so the row is paid for once instead of
// once per expansion.
func (g *plannerGraph) distanceRow(from int) []float64 {
	if row := g.rows[from]; row != nil {
		return row
	}
	row := make([]float64, len(g.symbols))
	for to := range row {
		row[to] = g.distance(from, to)
	}
	g.rows[from] = row
	return row
}

// pathLink is one step of a route plus the link back to the step before it, so
// candidate routes share their common prefix instead of copying it per candidate.
type pathLink struct {
	parent   int32
	node     int32
	fuelCost int32
	seconds  int32
	refuel   int32
	action   int8
	mode     int8
}

// searchEntry is one queued (waypoint, fuel) state with the route that reached it.
type searchEntry struct {
	totalTime int
	seq       int64
	node      int
	fuel      int
	fuelUsed  int
	path      int32
	pathLen   int32
}

// routeQueue is a min-heap ordered by total time, then by generation order. The
// sequence number makes the ordering a total one, so equal-cost candidates are
// explored in the order they were generated and the search is reproducible.
type routeQueue []searchEntry

func (q *routeQueue) len() int { return len(*q) }

func (q *routeQueue) push(entry searchEntry) {
	*q = append(*q, entry)
	items := *q
	child := len(items) - 1
	for child > 0 {
		parent := (child - 1) / 2
		if !items[child].before(items[parent]) {
			break
		}
		items[child], items[parent] = items[parent], items[child]
		child = parent
	}
}

func (q *routeQueue) pop() searchEntry {
	items := *q
	top := items[0]
	last := len(items) - 1
	items[0] = items[last]
	items = items[:last]
	*q = items

	parent := 0
	for {
		left := 2*parent + 1
		if left >= last {
			break
		}
		smallest := left
		if right := left + 1; right < last && items[right].before(items[left]) {
			smallest = right
		}
		if !items[smallest].before(items[parent]) {
			break
		}
		items[parent], items[smallest] = items[smallest], items[parent]
		parent = smallest
	}
	return top
}

func (e searchEntry) before(other searchEntry) bool {
	if e.totalTime != other.totalTime {
		return e.totalTime < other.totalTime
	}
	return e.seq < other.seq
}

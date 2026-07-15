package capacity

// The DIFF escalation ladder (st-zr0, spec: DIFF + CONVERGE). Gap = desired −
// actual, closed by the CHEAPEST lever able to close it — this ordering is
// what preserves per-hull-sustained $/hr:
//
//	tier 1  reuse idle hulls        (reassign_hull)                    FREE
//	tier 2  rebalance / reposition  (reposition_hull/rebalance_workers) FREE
//	tier 3  buffer whitelist + caps (adjust_buffer_whitelist/_cap)     cheap
//	tier 4  add cluster / buy hull  (add_cluster/buy_hull)             capital
//
// Pure topology arithmetic: no I/O, no clocks, no randomness — identical
// inputs produce the identical action list, so the stateless-per-tick loop
// re-derives the same plan until the actual topology moves (and a stable plan
// keeps proposal identities stable across re-files).
//
// v1 scope decisions (deliberate, documented):
//   - Tier-1 eligibility is Idle && DedicatedFleet == "" && not already
//     serving a cluster role. Own-fleet reuse is deferred: the differ carries
//     no fleet identity, and reassigning only undedicated hulls can never
//     poach another operation's pinned hull (the fleet-killer failure mode).
//   - Only WAREHOUSES reposition (tier 2): they are the stationary buffer
//     anchors. Stockers roam to sources and workers fly deliveries — their
//     instantaneous waypoints are duty, not misplacement, and the verb
//     vocabulary itself has rebalance_workers but no rebalance_stockers.
//   - Worker shortfalls — on covered AND uncovered hubs alike — draw on the
//     fleet's own worker surplus (over-covered desired hubs AND clusters the
//     plan dropped) via ONE rebalance_workers action per shortfall hub,
//     BEFORE a single capital worker is proposed: an uncovered hub's
//     add_cluster HullDelta carries only the workers no free lever could
//     cover (proposing capital for hulls the fleet already owns idle would
//     be a permanent over-buy). The worker-rebalancer primitive owns the
//     actual moves.
//   - Buffer whitelist/cap gaps are only actionable where a warehouse
//     already exists; an uncovered hub's whitelist self-heals on a later
//     tick, after its add_cluster lands (reconciler convergence, not
//     single-tick omniscience).
//   - The spec's tier-3 gate ("adjust buffer whitelist + caps — auto, GATED
//     if it forces a new stocker") is implemented as WITHHOLDING, not
//     relabeling: while a hub's desired StockerCount exceeds its actual
//     stockers (the planner sizes StockerCount to the buffered volume, so
//     the divergence itself is the "forces a new stocker" signal — no
//     Calibration re-derivation of the planner's model), demand-EXPANDING
//     adjustments (whitelist adds, cap raises) are not emitted; they
//     self-heal after the stocker capacity lands, which is itself tier-4
//     approval-gated where capital. Demand-SHEDDING adjustments (cap
//     reductions, de-whitelists) always flow. Relabeling the buffer verbs to
//     tier 4 is not viable: converge's canonical verb→tier check refuses a
//     mislabeled action.
//   - No teardown: surplus hulls and undesired clusters emit no actions (no
//     removal verb exists in v1); an empty desired topology emits ZERO
//     actions — the no-op-planner invariant the foundation's inert chain
//     relies on.
//   - ProjectedPerHullCrHr stays 0 on capital actions: Diff's inputs carry no
//     economics, and a zero projection makes the governor's derived gain
//     non-positive ⇒ payback UNDEFINED ⇒ capital is PROPOSAL-ONLY. Fail-safe
//     by construction; the planner can add a projection additively later.

import (
	"context"
	"fmt"
	"sort"
)

// DefaultHullCostEstimateCredits is the conservative-HIGH per-hull capital
// estimate the ladder stamps on tier-4 actions (EstimatedCostCredits =
// HullDelta × estimate). Deliberately high (a SHIP_LIGHT_HAULER lists around
// 300-400k): overestimating cost makes the GOVERN budget gates (reserve
// floor, per-decision cap, approval threshold) refuse MORE, never less —
// fail-closed. The real price is discovered at execution time by the
// actuator's purchase machinery, which carries its own treasury guards; this
// estimate only paces GOVERN.
const DefaultHullCostEstimateCredits int64 = 400_000

// LadderDiffer implements the Differ port via the escalation ladder.
type LadderDiffer struct {
	// HullCostEstimateCredits prices one hull for tier-4 cost stamping.
	// Zero or negative falls back to DefaultHullCostEstimateCredits — a
	// zero-cost capital action would slip under a raised approval threshold,
	// so the fallback fails CLOSED.
	HullCostEstimateCredits int64
}

// NewLadderDiffer builds the production ladder differ with the documented
// conservative cost estimate.
func NewLadderDiffer() LadderDiffer {
	return LadderDiffer{HullCostEstimateCredits: DefaultHullCostEstimateCredits}
}

// Diff turns desired-vs-actual divergence into the cheapest-first action list.
// An empty desired topology yields ZERO actions (the no-op-planner invariant).
func (d LadderDiffer) Diff(_ context.Context, desired DesiredTopology, actual TopologySignals, _ Calibration) ([]Action, error) {
	if desired.IsEmpty() {
		return nil, nil
	}
	converge := newConvergence(d.hullCost(), desired, actual)
	for _, hub := range desired.Hubs {
		converge.convergeHub(hub)
	}
	return converge.ascending(), nil
}

func (d LadderDiffer) hullCost() int64 {
	if d.HullCostEstimateCredits > 0 {
		return d.HullCostEstimateCredits
	}
	return DefaultHullCostEstimateCredits
}

// ---- one tick's convergence pass ---------------------------------------------

// convergence accumulates one Diff pass: the shared pools (idle hulls, worker
// surplus) drain as hubs consume them in desired order, and every action
// lands in its tier bucket so emission is ascending-tier by construction.
type convergence struct {
	hullCost int64
	clusters map[string]ClusterState
	idle     *idlePool
	// workerSurplus counts fleet workers beyond their hubs' desired counts
	// (including workers on clusters the plan dropped) — the free tier-2
	// supply for worker shortfalls.
	workerSurplus int
	tiers         map[Tier][]Action
}

func newConvergence(hullCost int64, desired DesiredTopology, actual TopologySignals) *convergence {
	return &convergence{
		hullCost:      hullCost,
		clusters:      clustersByHub(actual),
		idle:          newIdlePool(actual),
		workerSurplus: reusableWorkerSurplus(desired, actual),
		tiers:         map[Tier][]Action{},
	}
}

func (c *convergence) convergeHub(hub DesiredHub) {
	cluster, covered := c.clusters[hub.HubSymbol]
	if !covered {
		c.convergeUncoveredHub(hub)
		return
	}
	c.convergeCoveredHub(hub, cluster)
}

// convergeUncoveredHub stands up a hub with no cluster at all: idle hulls
// fill roles first (free), the fleet's own worker surplus covers workers via
// the tier-2 rebalance rung (free — never buy a hull the fleet already owns),
// and ONE add_cluster carries whatever remains — its HullDelta is exactly the
// net hull count the cluster adds, decomposed per role.
func (c *convergence) convergeUncoveredHub(hub DesiredHub) {
	warehousesLeft := c.reassignIdleHulls(hub, roleWarehouse, hub.WarehouseCount, 0)
	stockersLeft := c.reassignIdleHulls(hub, roleStocker, hub.StockerCount, 0)
	workersLeft := c.reassignIdleHulls(hub, roleWorker, hub.WorkerCount, 0)
	workersLeft = c.rebalanceWorkers(hub, hub.WorkerCount, 0, workersLeft)
	hullDelta := warehousesLeft + stockersLeft + workersLeft
	if hullDelta == 0 {
		return
	}
	gap := Gap{
		Kind:      GapHubUncovered,
		HubSymbol: hub.HubSymbol,
		Want:      hub.WarehouseCount + hub.StockerCount + hub.WorkerCount,
		Have:      0,
		Detail: fmt.Sprintf("add cluster: %d warehouse(s), %d stocker(s), %d worker(s)",
			warehousesLeft, stockersLeft, workersLeft),
	}
	c.add(Action{
		Tier:                 TierCapital,
		Verb:                 VerbAddCluster,
		GapKind:              GapHubUncovered,
		HubSymbol:            hub.HubSymbol,
		HullDelta:            hullDelta,
		WarehouseDelta:       warehousesLeft,
		StockerDelta:         stockersLeft,
		WorkerDelta:          workersLeft,
		EstimatedCostCredits: int64(hullDelta) * c.hullCost,
		Reason:               describeGap(gap),
	})
}

// convergeCoveredHub drives an existing cluster toward its desired shape:
// reposition misplaced warehouses, top up short roles, and converge the
// buffer whitelist.
func (c *convergence) convergeCoveredHub(hub DesiredHub, cluster ClusterState) {
	c.repositionWarehouses(hub, cluster)
	c.topUpRole(hub, roleWarehouse, hub.WarehouseCount, len(cluster.Warehouses))
	c.topUpRole(hub, roleStocker, hub.StockerCount, len(cluster.Stockers))
	c.topUpWorkers(hub, len(cluster.Workers))
	c.adjustBuffers(hub, cluster)
}

// repositionWarehouses converges each warehouse onto the hub's desired
// anchor waypoint (tier 2). A warehouse with an unknown position is left
// alone — acting on missing data would thrash.
func (c *convergence) repositionWarehouses(hub DesiredHub, cluster ClusterState) {
	anchor := waypointOr(hub.WarehouseWaypoint, hub.HubSymbol)
	for _, warehouse := range cluster.Warehouses {
		if warehouse.Waypoint == "" || warehouse.Waypoint == anchor {
			continue
		}
		gap := Gap{
			Kind:      GapHullMisplaced,
			HubSymbol: hub.HubSymbol,
			Detail:    fmt.Sprintf("warehouse %s at %s, want %s", warehouse.ShipSymbol, warehouse.Waypoint, anchor),
		}
		c.add(Action{
			Tier:           TierRebalance,
			Verb:           VerbRepositionHull,
			GapKind:        GapHullMisplaced,
			HubSymbol:      hub.HubSymbol,
			ShipSymbol:     warehouse.ShipSymbol,
			TargetWaypoint: anchor,
			Reason:         describeGap(gap),
		})
	}
}

// topUpRole closes a warehouse/stocker shortfall: idle hulls first (free),
// then one buy_hull per remaining hull — each buy is its own capital decision
// so the per-decision cap judges hulls one at a time.
func (c *convergence) topUpRole(hub DesiredHub, role hullRole, want, have int) {
	remaining := c.reassignIdleHulls(hub, role, want, have)
	c.buyHulls(hub, role, want, have, remaining)
}

// topUpWorkers closes a worker shortfall one rung at a time: idle hulls,
// then the fleet's own worker surplus via the worker-rebalancer (free), then
// capital for what free levers cannot cover.
func (c *convergence) topUpWorkers(hub DesiredHub, have int) {
	want := hub.WorkerCount
	remaining := c.reassignIdleHulls(hub, roleWorker, want, have)
	remaining = c.rebalanceWorkers(hub, want, have, remaining)
	c.buyHulls(hub, roleWorker, want, have, remaining)
}

// reassignIdleHulls emits one tier-1 reassign per eligible idle hull, up to
// the role's shortfall, and returns the unfilled remainder.
func (c *convergence) reassignIdleHulls(hub DesiredHub, role hullRole, want, have int) int {
	remaining := want - have
	if remaining <= 0 {
		return 0
	}
	for remaining > 0 {
		hull, ok := c.idle.take()
		if !ok {
			return remaining
		}
		gap := role.shortGap(hub, want, have, "reassign idle hull "+hull.ShipSymbol)
		c.add(Action{
			Tier:           TierReuseIdle,
			Verb:           VerbReassignHull,
			GapKind:        role.shortKind,
			HubSymbol:      hub.HubSymbol,
			ShipSymbol:     hull.ShipSymbol,
			TargetWaypoint: role.waypointOf(hub),
			Reason:         describeGap(gap),
		})
		remaining--
	}
	return 0
}

// rebalanceWorkers covers as much of a worker shortfall as the fleet surplus
// allows with ONE rebalance_workers action toward the shortfall hub, and
// returns the uncovered remainder. Count carries the moved-worker quantity
// machine-readably for the actuator.
func (c *convergence) rebalanceWorkers(hub DesiredHub, want, have, remaining int) int {
	if remaining <= 0 || c.workerSurplus <= 0 {
		return remaining
	}
	moved := min(remaining, c.workerSurplus)
	c.workerSurplus -= moved
	gap := roleWorker.shortGap(hub, want, have, fmt.Sprintf("rebalance %d worker(s) from fleet surplus", moved))
	c.add(Action{
		Tier:           TierRebalance,
		Verb:           VerbRebalanceWorkers,
		GapKind:        GapWorkerShort,
		HubSymbol:      hub.HubSymbol,
		TargetWaypoint: roleWorker.waypointOf(hub),
		Count:          moved,
		Reason:         describeGap(gap),
	})
	return remaining - moved
}

// buyHulls escalates a role shortfall to capital: one buy_hull per missing
// hull, each carrying the governor's ROI arithmetic (HullDelta 1, estimated
// cost) and its role machine-readably (GapKind + the role's delta).
func (c *convergence) buyHulls(hub DesiredHub, role hullRole, want, have, remaining int) {
	for i := 0; i < remaining; i++ {
		gap := role.shortGap(hub, want, have, "buy 1 "+role.name+" hull")
		action := Action{
			Tier:                 TierCapital,
			Verb:                 VerbBuyHull,
			GapKind:              role.shortKind,
			HubSymbol:            hub.HubSymbol,
			TargetWaypoint:       role.waypointOf(hub),
			HullDelta:            1,
			EstimatedCostCredits: c.hullCost,
			Reason:               describeGap(gap),
		}
		role.stampDelta(&action, 1)
		c.add(action)
	}
}

// adjustBuffers converges the hub's buffer whitelist + caps (tier 3) against
// the desired set. Only actionable where a warehouse exists to configure —
// an uncovered hub's whitelist self-heals after its cluster lands.
//
// The spec's tier-3 gate ("gated if it forces a new stocker"): while the
// hub's desired StockerCount exceeds its actual stockers, demand-EXPANDING
// adjustments (whitelist adds, cap raises) are WITHHELD — the planner sized
// that stocker capacity FOR the desired buffer set, so expanding buffered
// demand ahead of the (approval-gated) stocker would draw restock load the
// hub cannot serve. Withheld adjustments self-heal once the stockers land.
// Demand-SHEDDING adjustments (cap reductions, de-whitelists) always flow.
func (c *convergence) adjustBuffers(hub DesiredHub, cluster ClusterState) {
	if len(cluster.Warehouses) == 0 {
		return
	}
	stockerShort := hub.StockerCount > len(cluster.Stockers)
	caps := mergedGoodCaps(cluster)
	desired := map[string]bool{}
	for _, good := range hub.BufferedGoods {
		desired[good.Good] = true
		c.adjustBufferGood(hub, good, caps, stockerShort)
	}
	for _, good := range sortedGoods(caps) {
		if desired[good] {
			continue
		}
		c.removeBufferGood(hub, good, caps[good])
	}
}

func (c *convergence) adjustBufferGood(hub DesiredHub, good DesiredBufferedGood, caps map[string]int, stockerShort bool) {
	have, whitelisted := caps[good.Good]
	if !whitelisted {
		if stockerShort {
			return // withheld: a whitelist ADD would force stocker capacity that has not landed yet
		}
		gap := Gap{Kind: GapBufferGoodMissing, HubSymbol: hub.HubSymbol, Good: good.Good,
			Want: good.UnitsCap, Have: 0, Detail: "whitelist at cap"}
		c.add(bufferAction(VerbAdjustBufferWhitelist, hub, good.Good, good.UnitsCap, gap))
		return
	}
	if have == good.UnitsCap {
		return
	}
	if good.UnitsCap > have && stockerShort {
		return // withheld: a cap RAISE expands buffered demand the missing stocker was sized for
	}
	gap := Gap{Kind: GapBufferCapWrong, HubSymbol: hub.HubSymbol, Good: good.Good,
		Want: good.UnitsCap, Have: have, Detail: "correct cap"}
	c.add(bufferAction(VerbAdjustBufferCap, hub, good.Good, good.UnitsCap, gap))
}

func (c *convergence) removeBufferGood(hub DesiredHub, good string, have int) {
	gap := Gap{Kind: GapBufferGoodExtra, HubSymbol: hub.HubSymbol, Good: good,
		Want: 0, Have: have, Detail: "de-whitelist"}
	c.add(bufferAction(VerbAdjustBufferWhitelist, hub, good, 0, gap))
}

func (c *convergence) add(action Action) {
	c.tiers[action.Tier] = append(c.tiers[action.Tier], action)
}

// ascending flattens the tier buckets cheapest-first — the ladder's emission
// contract.
func (c *convergence) ascending() []Action {
	var actions []Action
	for _, tier := range []Tier{TierReuseIdle, TierRebalance, TierBufferAdjust, TierCapital} {
		actions = append(actions, c.tiers[tier]...)
	}
	return actions
}

// ---- hull roles ----------------------------------------------------------------

// hullRole names one cluster role and knows its gap kind, desired anchor,
// and which per-role delta field it stamps on a capital action.
type hullRole struct {
	name       string
	shortKind  GapKind
	waypointOf func(DesiredHub) string
	stampDelta func(*Action, int)
}

var (
	roleWarehouse = hullRole{"warehouse", GapWarehouseShort,
		func(hub DesiredHub) string { return waypointOr(hub.WarehouseWaypoint, hub.HubSymbol) },
		func(action *Action, count int) { action.WarehouseDelta = count }}
	roleStocker = hullRole{"stocker", GapStockerShort,
		func(hub DesiredHub) string { return waypointOr(hub.StockerWaypoint, hub.HubSymbol) },
		func(action *Action, count int) { action.StockerDelta = count }}
	roleWorker = hullRole{"worker", GapWorkerShort,
		func(hub DesiredHub) string { return waypointOr(hub.WorkerWaypoint, hub.HubSymbol) },
		func(action *Action, count int) { action.WorkerDelta = count }}
)

func (r hullRole) shortGap(hub DesiredHub, want, have int, detail string) Gap {
	return Gap{Kind: r.shortKind, HubSymbol: hub.HubSymbol, Want: want, Have: have, Detail: detail}
}

// ---- pools + pure lookups --------------------------------------------------------

// idlePool is the tier-1 supply: hulls verified reuse-eligible (idle, not
// pinned to any fleet, not already serving a cluster role), consumed in
// input order so the pass is deterministic.
type idlePool struct {
	hulls []HullUtilization
	next  int
}

func newIdlePool(actual TopologySignals) *idlePool {
	serving := servingShipSymbols(actual)
	taken := map[string]bool{}
	var eligible []HullUtilization
	for _, hull := range actual.IdleHulls {
		if !reusable(hull, serving, taken) {
			continue
		}
		taken[hull.ShipSymbol] = true
		eligible = append(eligible, hull)
	}
	return &idlePool{hulls: eligible}
}

// reusable applies the never-poach guard: only a genuinely idle, undedicated
// hull not already holding a cluster role (and not listed twice) may be
// reassigned.
func reusable(hull HullUtilization, serving, taken map[string]bool) bool {
	if !hull.Idle {
		return false
	}
	if hull.DedicatedFleet != "" {
		return false
	}
	if serving[hull.ShipSymbol] {
		return false
	}
	return !taken[hull.ShipSymbol]
}

func (p *idlePool) take() (HullUtilization, bool) {
	if p.next >= len(p.hulls) {
		return HullUtilization{}, false
	}
	hull := p.hulls[p.next]
	p.next++
	return hull, true
}

// servingShipSymbols indexes every hull already holding a cluster role — an
// over-filled IdleHulls slice must never let the ladder "reuse" one.
func servingShipSymbols(actual TopologySignals) map[string]bool {
	serving := map[string]bool{}
	for _, cluster := range actual.Clusters {
		for _, warehouse := range cluster.Warehouses {
			serving[warehouse.ShipSymbol] = true
		}
		for _, stocker := range cluster.Stockers {
			serving[stocker.ShipSymbol] = true
		}
		for _, worker := range cluster.Workers {
			serving[worker.ShipSymbol] = true
		}
	}
	return serving
}

// reusableWorkerSurplus counts fleet workers beyond their hubs' desired
// counts — including every worker on a cluster the plan no longer wants —
// the free supply the worker-rebalancer can redistribute.
func reusableWorkerSurplus(desired DesiredTopology, actual TopologySignals) int {
	desiredWorkers := map[string]int{}
	for _, hub := range desired.Hubs {
		desiredWorkers[hub.HubSymbol] = hub.WorkerCount
	}
	surplus := 0
	for _, cluster := range actual.Clusters {
		extra := len(cluster.Workers) - desiredWorkers[cluster.HubSymbol]
		if extra > 0 {
			surplus += extra
		}
	}
	return surplus
}

// clustersByHub indexes the actual clusters by hub symbol (first cluster wins
// on a duplicate symbol — deterministic; the sensor emits one per hub).
func clustersByHub(actual TopologySignals) map[string]ClusterState {
	clusters := map[string]ClusterState{}
	for _, cluster := range actual.Clusters {
		if _, exists := clusters[cluster.HubSymbol]; exists {
			continue
		}
		clusters[cluster.HubSymbol] = cluster
	}
	return clusters
}

// mergedGoodCaps sums each good's cap across the hub's warehouses — the
// hub-level buffered capacity DIFF compares against the desired cap.
func mergedGoodCaps(cluster ClusterState) map[string]int {
	caps := map[string]int{}
	for _, warehouse := range cluster.Warehouses {
		for good, unitsCap := range warehouse.GoodCaps {
			caps[good] += unitsCap
		}
	}
	return caps
}

// sortedGoods orders a cap map's goods alphabetically so extra-good removal
// emits deterministically (Go map iteration is randomized).
func sortedGoods(caps map[string]int) []string {
	goods := make([]string, 0, len(caps))
	for good := range caps {
		goods = append(goods, good)
	}
	sort.Strings(goods)
	return goods
}

func bufferAction(verb ActionVerb, hub DesiredHub, good string, unitsCap int, gap Gap) Action {
	return Action{
		Tier:      TierBufferAdjust,
		Verb:      verb,
		GapKind:   gap.Kind,
		HubSymbol: hub.HubSymbol,
		Good:      good,
		UnitsCap:  unitsCap,
		Reason:    describeGap(gap),
	}
}

// waypointOr defaults a role position to the hub itself — co-location is the
// cycle-time lever (spec PLAN: positions default to the hub when empty).
func waypointOr(waypoint, hubSymbol string) string {
	if waypoint != "" {
		return waypoint
	}
	return hubSymbol
}

// describeGap renders one gap as the action's audit line: which divergence,
// which arithmetic, which lever.
func describeGap(gap Gap) string {
	subject := gap.HubSymbol
	if gap.Good != "" {
		subject += " " + gap.Good
	}
	return fmt.Sprintf("%s %s: want %d, have %d — %s", gap.Kind, subject, gap.Want, gap.Have, gap.Detail)
}

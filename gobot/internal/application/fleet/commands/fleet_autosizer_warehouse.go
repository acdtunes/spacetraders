package commands

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// The WAREHOUSE (storage-follows-durable-chain) demand + DISPATCH model (sp-1j3f, the sp-1txd M7
// scope split out as the storage-stranding root-cause fix). vdld (the siting coordinator, DEPLOYED)
// moves and retires factory chains every tick; MANUAL warehouse placement strands the instant it
// resites — live evidence (2026-07-11) had all three warehouses co-located with ZERO running chains
// while ten producing chains had none, so the C1 basis-draw benefit was zero and every factory output
// sold at the laddered export ask (the export-ask-subsidy leak, fully open).
//
// The fix makes warehouses FOLLOW the durable top-K chains automatically:
//
//   - TICK-PERSISTENCE HYSTERESIS (warehouse_min_chain_tick_persistence): a chain must sit in the
//     running portfolio for min consecutive ticks before a warehouse chases it — so a warehouse never
//     lunges at a chain vdld retires on the very next tick.
//   - PAY GATE (warehouse_min_chain_realized_per_hour, from rh2z chain_pnl NetPerHour): only a chain
//     genuinely EARNING above the floor pulls a warehouse; the subsidy only pays back on a real earner.
//   - CO-EXPORT DEDUPE: chains that export from the SAME waypoint share ONE warehouse hull buffering
//     the union of their goods — demand counts distinct export waypoints, not chains.
//   - DISPATCH: a warehouse-dedicated hull is navigated to a durable export waypoint and parked as a
//     warehouse workflow with the co-exported goods list; a hull STRANDED on a retired chain is moved
//     to a newly-uncovered durable chain — never left on the dead one.
//
// The BUY side rides the shared fail-closed guard stack exactly like lights/heavies: demand feeds
// sizeClass, which buys ONE hull through EvaluateGuards and dedicates it to the "warehouse" fleet at
// purchase (dedicate-at-purchase). The DISPATCH side is a separate, always-run placement step — a
// hull must be re-sited when a chain retires even on a tick that buys nothing. Both fail CLOSED on an
// unreadable portfolio or hull read: a missing signal must never spend a credit OR move a hull.
//
// SCOPE (sp-1j3f): demand + dispatch. The warehouse capacity ladder (module hydration / slots·price
// rungs) is the separate M8 and is deliberately OUT here.

// --- read/act ports (wired by the daemon at boot; every one nil-safe, fail-closed on unread) ------

// PortfolioChain is one running factory chain in the current durable-chain portfolio: the good it
// produces, the waypoint it EXPORTS from (the warehouse co-locates here), and its realized $/hr
// (rh2z chain_pnl NetPerHour) with a readability flag. The concrete source (M6) joins vdld's
// running-chains with the export-market lookup and the chain-P&L reader; tests inject fixtures.
type PortfolioChain struct {
	Good            string
	ExportWaypoint  string
	RealizedPerHour float64
	// RealizedReadable is false when this chain's realized $/hr could not be read (e.g. a
	// pre-realization chain with no P&L yet). Such a chain fails the pay gate CLOSED — an unproven
	// earner never pulls a warehouse.
	RealizedReadable bool
}

// WarehousePortfolioSource yields the current running-chain portfolio for a player. readable=false
// (or an error) fails the WHOLE warehouse pass closed: no demand, no dispatch, no stranding — a
// missing portfolio must never move a hull.
type WarehousePortfolioSource interface {
	RunningChains(ctx context.Context, playerID int) (chains []PortfolioChain, readable bool, err error)
}

// WarehouseHull is one warehouse-dedicated hull and the waypoint it is currently parked at ("" when
// idle / unplaced). Read from the ship repo filtered by the "warehouse" fleet dedication.
type WarehouseHull struct {
	ShipSymbol     string
	ParkedWaypoint string
}

// WarehouseHullSource reads the player's warehouse-dedicated hulls. An error fails the buy path
// closed (the current pool is unknowable, so a shortfall cannot be sized without risking over-buy).
type WarehouseHullSource interface {
	WarehouseHulls(ctx context.Context, playerID int) ([]WarehouseHull, error)
}

// WarehouseDispatchPort navigates a warehouse hull to an export waypoint and parks it as a warehouse
// workflow buffering goods. Idempotent by contract: start the container if the hull is idle, or
// re-target it if it is parked on the wrong (retired) waypoint. Moving an already-owned hull is not a
// credit spend — the fail-open/closed split is handled by the caller (dispatch runs only on a
// readable portfolio).
type WarehouseDispatchPort interface {
	DispatchWarehouse(ctx context.Context, playerID int, shipSymbol, waypoint string, goods []string) error
}

// --- provider ---------------------------------------------------------------

// WarehouseDemandProvider sizes the warehouse pool to the durable top-K chain portfolio and drives
// the anti-stranding dispatch. It is a registered singleton (one instance serves every player's
// ticks), so the only in-memory state is the per-player tick-persistence bookkeeping and the plan
// stashed by the tick's Demand() read for the same tick's DispatchPlanned() to apply.
type WarehouseDemandProvider struct {
	portfolio WarehousePortfolioSource
	hulls     WarehouseHullSource
	dispatch  WarehouseDispatchPort

	mu sync.Mutex
	// persist[playerID][good@waypoint] is the count of consecutive ticks that chain has been in the
	// portfolio — the hysteresis clock. Incremented for chains present this tick, dropped for those
	// absent.
	persist map[int]map[string]int
	// lastPlan[playerID] is the durable-target plan computed by this tick's Demand(), consumed by the
	// same tick's DispatchPlanned(). Keyed by player so concurrent per-player ticks stay isolated.
	lastPlan map[int]warehousePlan
}

// NewWarehouseDemandProvider wires the provider over its portfolio + hull read sources and the
// dispatch port. A nil dispatch port makes DispatchPlanned a logged no-op (an unwired mis-config,
// surfaced loudly — never a silent strand).
func NewWarehouseDemandProvider(portfolio WarehousePortfolioSource, hulls WarehouseHullSource, dispatch WarehouseDispatchPort) *WarehouseDemandProvider {
	return &WarehouseDemandProvider{
		portfolio: portfolio,
		hulls:     hulls,
		dispatch:  dispatch,
		persist:   make(map[int]map[string]int),
		lastPlan:  make(map[int]warehousePlan),
	}
}

// Class identifies this provider as the warehouse sizer.
func (p *WarehouseDemandProvider) Class() HullClass { return HullClassWarehouse }

// warehouseTarget is one durable co-export group: the export waypoint one warehouse hull co-locates
// at, the union of goods the co-exported chains produce, and the top realized $/hr among them (the
// dispatch/cap priority).
type warehouseTarget struct {
	Waypoint    string
	Goods       []string
	TopRealized float64
}

// warehousePlan is the per-tick durable-target computation stashed for the dispatch step. readable is
// false on a fail-closed pass, which makes DispatchPlanned a no-op (no hull is moved).
type warehousePlan struct {
	readable bool
	targets  []warehouseTarget
	hulls    []WarehouseHull
}

// Demand reads the portfolio, advances the tick-persistence hysteresis, and returns the sized
// warehouse demand: the count of DURABLE (persisted min ticks) + PAYING (realized >= floor) co-export
// target waypoints, capped at max_warehouse_hulls, against the count of those already covered by a
// co-located hull. It fails CLOSED (Readable=false, no buy) on an unreadable portfolio or hull read.
// It also stashes the computed plan for the same tick's DispatchPlanned().
func (p *WarehouseDemandProvider) Demand(ctx context.Context, playerID int, params DemandParams) (ClassDemand, error) {
	chains, readable, err := p.portfolio.RunningChains(ctx, playerID)
	if err != nil || !readable {
		p.stashPlan(playerID, warehousePlan{readable: false})
		reason := "portfolio unreadable — warehouse pass fails closed (no buy, no dispatch)"
		if err != nil {
			reason = fmt.Sprintf("portfolio read error: %v — warehouse pass fails closed", err)
		}
		return unreadableWarehouse(reason), nil
	}

	hulls, herr := p.hulls.WarehouseHulls(ctx, playerID)
	if herr != nil {
		p.stashPlan(playerID, warehousePlan{readable: false})
		return unreadableWarehouse(fmt.Sprintf("warehouse-hull count unreadable: %v — fail closed", herr)), nil
	}

	targets := p.durableTargets(playerID, chains, params.WarehouseMinTickPersistence, params.WarehouseMinRealizedPerHour)
	if params.MaxWarehouseHulls > 0 && len(targets) > params.MaxWarehouseHulls {
		targets = targets[:params.MaxWarehouseHulls]
	}

	p.stashPlan(playerID, warehousePlan{readable: true, targets: targets, hulls: hulls})

	// BUY side: Current is the warehouse POOL SIZE (how many warehouse hulls exist), not how many
	// targets are covered — each durable co-export group needs one hull, so the shortfall to BUY is
	// (targets − pool). An idle-but-unplaced hull is part of the pool and must not trigger a buy; the
	// dispatch step places it. Coverage is the dispatch step's concern, tracked separately.
	covered := coveredCount(targets, hulls)
	return ClassDemand{
		Class:    HullClassWarehouse,
		Demand:   len(targets),
		Current:  len(hulls),
		Readable: true,
		Reason:   fmt.Sprintf("%d durable co-export target(s) (persist>=%d, realized>=%.0f); pool %d, %d covered", len(targets), params.WarehouseMinTickPersistence, params.WarehouseMinRealizedPerHour, len(hulls), covered),
	}, nil
}

// durableTargets advances the per-player persistence clock against the current portfolio, then
// returns the durable + paying co-export target groups sorted by top realized $/hr (descending, then
// waypoint) so the cap and dispatch priority are deterministic.
func (p *WarehouseDemandProvider) durableTargets(playerID int, chains []PortfolioChain, minPersist int, payFloor float64) []warehouseTarget {
	persisted := p.advancePersistence(playerID, chains)
	if minPersist < 1 {
		minPersist = 1
	}

	// Group qualifying chains by export waypoint (co-export dedupe). A chain qualifies iff it has
	// persisted min ticks AND its realized $/hr was readable AND clears the floor.
	byWaypoint := make(map[string]*warehouseTarget)
	order := []string{} // preserve first-seen order for a stable pre-sort
	for _, c := range chains {
		key := chainKey(c.Good, c.ExportWaypoint)
		if persisted[key] < minPersist {
			continue
		}
		if !c.RealizedReadable || c.RealizedPerHour < payFloor {
			continue
		}
		t := byWaypoint[c.ExportWaypoint]
		if t == nil {
			t = &warehouseTarget{Waypoint: c.ExportWaypoint}
			byWaypoint[c.ExportWaypoint] = t
			order = append(order, c.ExportWaypoint)
		}
		t.Goods = append(t.Goods, c.Good)
		if c.RealizedPerHour > t.TopRealized {
			t.TopRealized = c.RealizedPerHour
		}
	}

	targets := make([]warehouseTarget, 0, len(order))
	for _, wp := range order {
		t := byWaypoint[wp]
		t.Goods = sortedUnique(t.Goods)
		targets = append(targets, *t)
	}
	// Highest-earning target first, waypoint as the deterministic tiebreak.
	sort.SliceStable(targets, func(i, j int) bool {
		if targets[i].TopRealized != targets[j].TopRealized {
			return targets[i].TopRealized > targets[j].TopRealized
		}
		return targets[i].Waypoint < targets[j].Waypoint
	})
	return targets
}

// advancePersistence increments the tick clock for every chain present in the portfolio this tick and
// drops the clock for chains no longer present (a retired chain's hysteresis resets to zero, so it
// must re-earn its persistence if it returns). Returns the updated per-key counts.
func (p *WarehouseDemandProvider) advancePersistence(playerID int, chains []PortfolioChain) map[string]int {
	p.mu.Lock()
	defer p.mu.Unlock()

	prev := p.persist[playerID]
	if prev == nil {
		prev = map[string]int{}
	}
	next := make(map[string]int, len(chains))
	for _, c := range chains {
		key := chainKey(c.Good, c.ExportWaypoint)
		next[key] = prev[key] + 1
	}
	p.persist[playerID] = next
	return next
}

// DispatchPlanned applies the plan stashed by this tick's Demand(): it navigates idle and STRANDED
// warehouse hulls to the highest-earning uncovered durable targets (each carrying that waypoint's
// co-exported goods list), and leaves hulls already serving a durable target in place. It is a no-op
// on a fail-closed (unreadable) plan — a missing portfolio never moves a hull. A dry run logs the
// would-dispatch but calls nothing. Returns the tick's placement tally for the coordinator to log.
func (p *WarehouseDemandProvider) DispatchPlanned(ctx context.Context, playerID int, dryRun bool) WarehouseDispatchResult {
	logger := common.LoggerFromContext(ctx)

	p.mu.Lock()
	plan := p.lastPlan[playerID]
	p.mu.Unlock()

	if !plan.readable {
		return WarehouseDispatchResult{}
	}

	covered := coverageSet(plan.targets, plan.hulls)

	// Available hulls: idle ("") or parked on a non-target (retired) waypoint, plus any redundant
	// extra hull already sitting on a covered target. These are the hulls free to (re)dispatch.
	targetWaypoints := map[string]bool{}
	for _, t := range plan.targets {
		targetWaypoints[t.Waypoint] = true
	}
	usedCover := map[string]bool{}
	available := []WarehouseHull{}
	stranded := 0
	for _, h := range plan.hulls {
		if h.ParkedWaypoint != "" && targetWaypoints[h.ParkedWaypoint] && !usedCover[h.ParkedWaypoint] {
			usedCover[h.ParkedWaypoint] = true // this hull covers its target; keep it in place
			continue
		}
		if h.ParkedWaypoint != "" && !targetWaypoints[h.ParkedWaypoint] {
			stranded++ // parked on a retired/non-target chain
		}
		available = append(available, h)
	}

	// Uncovered targets, already sorted highest-realized-first by durableTargets.
	uncovered := []warehouseTarget{}
	for _, t := range plan.targets {
		if !covered[t.Waypoint] {
			uncovered = append(uncovered, t)
		}
	}

	res := WarehouseDispatchResult{Stranded: stranded, Uncovered: len(uncovered)}
	n := len(available)
	if len(uncovered) < n {
		n = len(uncovered)
	}
	for i := 0; i < n; i++ {
		h, t := available[i], uncovered[i]
		if dryRun {
			logger.Log("WARN", fmt.Sprintf("Autosizer warehouse DRY-RUN: WOULD DISPATCH %s → %s buffering %v (set dry_run=false to arm)", h.ShipSymbol, t.Waypoint, t.Goods), map[string]interface{}{
				"action": "autosizer_warehouse_dry_run_would_dispatch", "ship": h.ShipSymbol, "waypoint": t.Waypoint,
			})
			continue
		}
		if p.dispatch == nil {
			logger.Log("WARN", fmt.Sprintf("Autosizer warehouse: %s WOULD move to durable chain %s but no dispatch port is wired (mis-wire: cannot re-site a stranded hull)", h.ShipSymbol, t.Waypoint), map[string]interface{}{
				"action": "autosizer_warehouse_no_dispatch", "ship": h.ShipSymbol, "waypoint": t.Waypoint,
			})
			continue
		}
		if err := p.dispatch.DispatchWarehouse(ctx, playerID, h.ShipSymbol, t.Waypoint, t.Goods); err != nil {
			logger.Log("ERROR", fmt.Sprintf("Autosizer warehouse dispatch of %s → %s failed: %v", h.ShipSymbol, t.Waypoint, err), map[string]interface{}{
				"action": "autosizer_warehouse_dispatch_error", "ship": h.ShipSymbol, "waypoint": t.Waypoint,
			})
			continue
		}
		res.Dispatched++
		logger.Log("INFO", fmt.Sprintf("Autosizer warehouse DISPATCHED %s → %s buffering %v (follows durable chain; was %s)", h.ShipSymbol, t.Waypoint, t.Goods, parkedLabel(h.ParkedWaypoint)), map[string]interface{}{
			"action": "autosizer_warehouse_dispatched", "ship": h.ShipSymbol, "waypoint": t.Waypoint, "from": h.ParkedWaypoint,
		})
	}
	return res
}

// WarehouseDispatchResult tallies one tick's warehouse placement for the coordinator log/metrics:
// hulls moved onto durable targets, hulls still stranded on retired chains, and durable targets left
// uncovered (the shortfall the buy side fills over subsequent ticks).
type WarehouseDispatchResult struct {
	Dispatched int
	Stranded   int
	Uncovered  int
}

// stashPlan stores the tick's plan for DispatchPlanned under the player key.
func (p *WarehouseDemandProvider) stashPlan(playerID int, plan warehousePlan) {
	p.mu.Lock()
	p.lastPlan[playerID] = plan
	p.mu.Unlock()
}

// coveredCount returns how many target waypoints have at least one hull parked at them.
func coveredCount(targets []warehouseTarget, hulls []WarehouseHull) int {
	return len(coverageSet(targets, hulls))
}

// coverageSet is the set of target waypoints served by at least one parked warehouse hull.
func coverageSet(targets []warehouseTarget, hulls []WarehouseHull) map[string]bool {
	parked := map[string]bool{}
	for _, h := range hulls {
		if h.ParkedWaypoint != "" {
			parked[h.ParkedWaypoint] = true
		}
	}
	covered := map[string]bool{}
	for _, t := range targets {
		if parked[t.Waypoint] {
			covered[t.Waypoint] = true
		}
	}
	return covered
}

// unreadableWarehouse is the fail-closed warehouse demand (the portfolio or hull count could not be
// read).
func unreadableWarehouse(reason string) ClassDemand {
	return ClassDemand{Class: HullClassWarehouse, Readable: false, Reason: reason}
}

func chainKey(good, waypoint string) string { return good + "@" + waypoint }

func parkedLabel(waypoint string) string {
	if waypoint == "" {
		return "idle"
	}
	return waypoint
}

// sortedUnique returns the input sorted with duplicates removed (the deterministic co-export goods
// list a warehouse buffers).
func sortedUnique(in []string) []string {
	if len(in) == 0 {
		return in
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

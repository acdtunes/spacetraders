package commands

import (
	"context"
	"sort"
	"time"
)

// Plan-time serialization of the absorption netting.
//
// A tour planner nets OTHERS' outstanding depth out of its plan, solves, and only THEN
// reserves what it chose. Between that read and that write it is invisible, so planners
// launched on the same coordinator tick all net the same pre-reservation snapshot, all rank
// the same sink best, and converge on it — each plan well-priced, all of them on one
// waypoint. The ledger sees it only afterwards, as a cap breach and a re-plan.
//
// The gate makes release -> net -> solve -> reserve one critical section, so a planner reads
// the depth the previous one just committed and steers off it WHILE planning. It covers the
// release too: that briefly shows an incumbent's own held sink free.
//
// Dispersal is only obtainable by taking turns — a planner cannot be steered off a sink
// nobody has chosen yet — so the gate is not widened, it is SPLIT. Turns are taken per
// CONTENTION DOMAIN: the systems of the tour graph the plan may reserve sinks in. Two
// planners whose graphs share no system have no sink to converge on, so serializing them
// buys nothing and costs the fleet one whole solve of latency per hull. A planner holds
// every domain its graph touches, taken in a fixed order so two overlapping graphs can
// never deadlock on each other.
//
// The gates are mutual-exclusion tokens, not state — a restart just starts with them free.
// The ledger's in-transaction cap check remains the hard backstop, and still is the only
// one for the engines that do not pass through here.

// tourPlanGateWait bounds how long a planner queues for its planning gates. It is
// generous next to a normal plan (a snapshot build plus one solve) and past the routing
// service's own solve timeout, so only a genuinely wedged queue trips it.
const tourPlanGateWait = 90 * time.Second

// defaultTourPlanConcurrency bounds how many of a player's planners may be inside the
// gates at once. The bound exists because the routing service searches on a fixed worker
// pool and its search budget is WALL CLOCK: offered more concurrent solves than it has
// workers, every solve still returns on time, having searched proportionally less. So the
// bound belongs BELOW the pool width, and a fleet of disjoint domains queues rather than
// silently trading plan quality for apparent throughput.
const defaultTourPlanConcurrency = 6

// planDomain is one contention domain: a player's system. Sinks live at waypoints, and a
// waypoint belongs to exactly one system, so two plans over disjoint system sets can never
// name the same sink.
type planDomain struct {
	playerID int
	system   string
}

// planConcurrencyLimit resolves the configured bound, flooring at one planner.
func (h *RunTourCoordinatorHandler) planConcurrencyLimit() int {
	if h.planConcurrency <= 0 {
		return defaultTourPlanConcurrency
	}
	return h.planConcurrency
}

// SetPlanConcurrency bounds how many of a player's tour planners hold planning gates at
// once. The daemon injects the configured value at boot; 0 leaves the in-code default. One
// reproduces the single fleet-wide token — every planner takes its turn however disjoint
// its sink universe is — which is the kill switch for the split.
func (h *RunTourCoordinatorHandler) SetPlanConcurrency(n int) {
	h.planConcurrency = n
}

// planGate returns a contention domain's gate, creating it on first use. A one-slot
// buffered channel is the lock; holding the token is holding the gate.
func (h *RunTourCoordinatorHandler) planGate(domain planDomain) chan struct{} {
	h.planGateMu.Lock()
	defer h.planGateMu.Unlock()
	if h.planGates == nil {
		h.planGates = map[planDomain]chan struct{}{}
	}
	gate, ok := h.planGates[domain]
	if !ok {
		gate = make(chan struct{}, 1)
		h.planGates[domain] = gate
	}
	return gate
}

// planSlot returns the player's concurrency semaphore, sized once on first use.
func (h *RunTourCoordinatorHandler) planSlot(playerID int) chan struct{} {
	h.planGateMu.Lock()
	defer h.planGateMu.Unlock()
	if h.planSlots == nil {
		h.planSlots = map[int]chan struct{}{}
	}
	slots, ok := h.planSlots[playerID]
	if !ok {
		slots = make(chan struct{}, h.planConcurrencyLimit())
		h.planSlots[playerID] = slots
	}
	return slots
}

// planDomainKeys projects a plan's allowed systems onto its contention domains, deduped and
// ORDERED — the order is what makes multi-domain acquisition deadlock-free, since every
// planner takes overlapping domains in the same sequence. A plan with no resolved system
// still takes one domain, so it is serialized against its own kind rather than running free.
func planDomainKeys(playerID int, systems []string) []planDomain {
	seen := make(map[string]bool, len(systems))
	ordered := make([]string, 0, len(systems))
	for _, s := range systems {
		if !seen[s] {
			seen[s] = true
			ordered = append(ordered, s)
		}
	}
	if len(ordered) == 0 {
		ordered = []string{""}
	}
	sort.Strings(ordered)
	domains := make([]planDomain, 0, len(ordered))
	for _, s := range ordered {
		domains = append(domains, planDomain{playerID: playerID, system: s})
	}
	return domains
}

// acquirePlanGate takes a concurrency slot and then every contention domain the plan's sink
// universe touches, returning the release to defer. It fails CLOSED (ok=false) on a
// cancelled context or a queue that never drains: a planner that cannot be serialized
// against its peers has no honest depth to plan against, and planning anyway is planning
// blind into a sink another hull is mid-way through taking. The caller exits the tour
// infeasible and the fleet coordinator relaunches the hull, which is also what staggers the
// batch. Everything taken before a timeout is handed back, so a refusal leaves no gate held.
func (h *RunTourCoordinatorHandler) acquirePlanGate(ctx context.Context, playerID int, systems []string) (func(), bool) {
	wait := h.planGateWait
	if wait <= 0 {
		wait = tourPlanGateWait
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()

	held := make([]chan struct{}, 0, len(systems)+1)
	release := func() {
		for i := len(held) - 1; i >= 0; i-- {
			<-held[i]
		}
	}
	take := func(gate chan struct{}) bool {
		select {
		case gate <- struct{}{}:
			held = append(held, gate)
			return true
		default:
		}
		select {
		case gate <- struct{}{}:
			held = append(held, gate)
			return true
		case <-ctx.Done():
			return false
		case <-timer.C:
			return false
		}
	}

	// The slot comes first and is the only resource taken out of domain order, so a planner
	// waiting for it holds no domain another planner needs to finish.
	if !take(h.planSlot(playerID)) {
		release()
		return nil, false
	}
	for _, domain := range planDomainKeys(playerID, systems) {
		if !take(h.planGate(domain)) {
			release()
			return nil, false
		}
	}
	return release, true
}

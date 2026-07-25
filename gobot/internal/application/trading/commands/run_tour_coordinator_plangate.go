package commands

import (
	"context"
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
// The gate makes release -> net -> solve -> reserve one critical section per player, so a
// planner reads the depth the previous one just committed and steers off it WHILE planning.
// It covers the release too: that briefly shows an incumbent's own held sink free.
//
// It is a mutual-exclusion token, not state — a restart just starts with it free. The
// ledger's in-transaction cap check remains the hard backstop, and still is the only one
// for the engines that do not pass through here.

// tourPlanGateWait bounds how long a planner queues for its player's planning gate. It is
// generous next to a normal plan (a snapshot build plus one solve) and past the routing
// service's own solve timeout, so only a genuinely wedged queue trips it.
const tourPlanGateWait = 90 * time.Second

// planGate returns the player's planning gate, creating it on first use. A one-slot
// buffered channel is the lock; holding the token is holding the gate.
func (h *RunTourCoordinatorHandler) planGate(playerID int) chan struct{} {
	h.planGateMu.Lock()
	defer h.planGateMu.Unlock()
	if h.planGates == nil {
		h.planGates = map[int]chan struct{}{}
	}
	gate, ok := h.planGates[playerID]
	if !ok {
		gate = make(chan struct{}, 1)
		h.planGates[playerID] = gate
	}
	return gate
}

// acquirePlanGate takes the player's planning gate and returns the release to defer.
// It fails CLOSED (ok=false) on a cancelled context or a queue that never drains: a
// planner that cannot be serialized against its peers has no honest depth to plan
// against, and planning anyway is planning blind into a sink another hull is mid-way
// through taking. The caller exits the tour infeasible and the fleet coordinator
// relaunches the hull, which is also what staggers the batch.
func (h *RunTourCoordinatorHandler) acquirePlanGate(ctx context.Context, playerID int) (func(), bool) {
	gate := h.planGate(playerID)
	select {
	case gate <- struct{}{}:
		return func() { <-gate }, true
	default:
	}

	wait := h.planGateWait
	if wait <= 0 {
		wait = tourPlanGateWait
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case gate <- struct{}{}:
		return func() { <-gate }, true
	case <-ctx.Done():
		return nil, false
	case <-timer.C:
		return nil, false
	}
}

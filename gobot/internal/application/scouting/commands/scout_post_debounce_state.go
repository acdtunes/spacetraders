package commands

import (
	"fmt"
	"time"
)

// repositionBackedOff reports whether a reposition dispatch for key is currently within
// its backoff window. A nil/absent entry reads false.
func (h *RunScoutPostCoordinatorHandler) repositionBackedOff(key string) bool {
	h.repositionMu.Lock()
	defer h.repositionMu.Unlock()
	until, ok := h.repositionBackoffUntil[key]
	return ok && h.clock.Now().Before(until)
}

// noteRepositionDispatch arms the per-slot dispatch backoff so the next dispatch for
// this slot waits out repositionRetryBackoff — the anti-hot-loop bound.
func (h *RunScoutPostCoordinatorHandler) noteRepositionDispatch(key string) {
	h.repositionMu.Lock()
	defer h.repositionMu.Unlock()
	if h.repositionBackoffUntil == nil {
		h.repositionBackoffUntil = make(map[string]time.Time)
	}
	h.repositionBackoffUntil[key] = h.clock.Now().Add(repositionRetryBackoff)
}

// noteRepositionFailure records a FAILED reposition relay for key: it increments the
// consecutive-failure streak and arms the LONG failure cooldown — OVERRIDING the short dispatch
// floor — and returns the new streak count for the failure log. Rotation to the next candidate
// falls out of the backed-off slot being skipped without consuming the shared probe.
func (h *RunScoutPostCoordinatorHandler) noteRepositionFailure(key string, cooldown time.Duration) int {
	h.repositionMu.Lock()
	defer h.repositionMu.Unlock()
	if h.repositionBackoffUntil == nil {
		h.repositionBackoffUntil = make(map[string]time.Time)
	}
	if h.repositionFailures == nil {
		h.repositionFailures = make(map[string]int)
	}
	h.repositionFailures[key]++
	h.repositionBackoffUntil[key] = h.clock.Now().Add(cooldown)
	return h.repositionFailures[key]
}

// resetRepositionFailures clears a post's failure streak, cooldown, and once-logged marker
// — called when a relay COMPLETES, so a post that finally succeeded starts clean and
// the next failure is counted from one. delete on a nil map is a no-op, so this is safe on the
// struct-literal handler the tests build.
func (h *RunScoutPostCoordinatorHandler) resetRepositionFailures(key string) {
	h.repositionMu.Lock()
	defer h.repositionMu.Unlock()
	delete(h.repositionFailures, key)
	delete(h.repositionBackoffUntil, key)
	delete(h.repositionBackoffLoggedUntil, key)
}

// noteRepositionBackoffLogged reports whether the CURRENT backoff episode for key has not yet
// been announced, and marks it announced. It keys the marker on the exact backoff deadline,
// so each distinct cooldown window logs its skip reason exactly once rather than every tick.
// A new failure arms a later deadline, which reads as a new episode and logs once more.
func (h *RunScoutPostCoordinatorHandler) noteRepositionBackoffLogged(key string) bool {
	h.repositionMu.Lock()
	defer h.repositionMu.Unlock()
	until, ok := h.repositionBackoffUntil[key]
	if !ok {
		return false
	}
	if logged, ok := h.repositionBackoffLoggedUntil[key]; ok && logged.Equal(until) {
		return false // this episode already announced
	}
	if h.repositionBackoffLoggedUntil == nil {
		h.repositionBackoffLoggedUntil = make(map[string]time.Time)
	}
	h.repositionBackoffLoggedUntil[key] = until
	return true
}

// backoffKey scopes the reposition backoff to (playerID, system, slot) so one player's
// relay to system S never rate-limits another player's post in the same-named system, and
// each slot of a multi-probe post repositions independently. The PRIMARY slot keeps the
// un-suffixed key, so a single-hull post's key shape is unchanged.
func backoffKey(playerID int, system string, slotIndex int) string {
	if slotIndex < 0 {
		return fmt.Sprintf("%d|%s", playerID, system)
	}
	return fmt.Sprintf("%d|%s|%d", playerID, system, slotIndex)
}

// driftKey scopes market-drift debounce tracking to (playerID, system) — the same
// un-suffixed shape as backoffKey's primary-slot form, since drift is a whole-post
// property (the market SET, not any one slot).
func driftKey(playerID int, system string) string {
	return fmt.Sprintf("%d|%s", playerID, system)
}

// noteDriftPending records the FIRST tick a post's market set was seen drifting
// and returns how long the drift episode has been pending. A key already
// tracked keeps its original timestamp — the age accumulates across ticks until the
// re-cut fires or the drift resolves on its own.
func (h *RunScoutPostCoordinatorHandler) noteDriftPending(key string) time.Duration {
	h.driftMu.Lock()
	defer h.driftMu.Unlock()
	if h.driftPendingSince == nil {
		h.driftPendingSince = make(map[string]time.Time)
	}
	now := h.clock.Now()
	since, ok := h.driftPendingSince[key]
	if !ok {
		h.driftPendingSince[key] = now
		return 0
	}
	return now.Sub(since)
}

// clearDriftPending forgets a post's pending-drift episode: called once a
// re-cut resolves it, the drift resolves on its own, or the post is no longer
// partitioned (reverted to single-hull). A nil/absent entry is a harmless no-op.
func (h *RunScoutPostCoordinatorHandler) clearDriftPending(key string) {
	h.driftMu.Lock()
	defer h.driftMu.Unlock()
	delete(h.driftPendingSince, key)
}

// budgetChangeState is one post's pending hull-budget change: the new budget the
// sizer wants (differing from the post's cut partition) and how many consecutive reconcile
// cycles it has persisted. The re-partition fires only when cycles reaches the debounce.
type budgetChangeState struct {
	budget int
	cycles int
}

// noteBudgetChangePending records that the sizer wants newBudget for a post whose cut
// partition is a DIFFERENT size, and returns how many CONSECUTIVE reconcile cycles that SAME
// new budget has now persisted. A first sighting — or a change to yet another budget
// — restarts the count at 1, so a budget that keeps flapping between values never accumulates
// toward the re-partition threshold; only a STABLE new budget does. The single-value dedupe
// (state.budget != newBudget resets) is what makes the debounce absorb an OSCILLATION, not
// just a one-shot blip. Lazily initializes the map so the struct-literal test handlers (which
// never call the constructor) are safe, mirroring noteDriftPending.
func (h *RunScoutPostCoordinatorHandler) noteBudgetChangePending(key string, newBudget int) int {
	h.budgetChangeMu.Lock()
	defer h.budgetChangeMu.Unlock()
	if h.budgetChangePending == nil {
		h.budgetChangePending = make(map[string]budgetChangeState)
	}
	state, ok := h.budgetChangePending[key]
	if !ok || state.budget != newBudget {
		h.budgetChangePending[key] = budgetChangeState{budget: newBudget, cycles: 1}
		return 1
	}
	state.cycles++
	h.budgetChangePending[key] = state
	return state.cycles
}

// clearBudgetChangePending forgets a post's pending budget change: called the moment
// its budget matches the cut partition again (a swing reverted, or a real change was applied),
// so a later change starts a FRESH count rather than inheriting a stale one. A nil/absent entry
// is a harmless no-op.
func (h *RunScoutPostCoordinatorHandler) clearBudgetChangePending(key string) {
	h.budgetChangeMu.Lock()
	defer h.budgetChangeMu.Unlock()
	delete(h.budgetChangePending, key)
}

// singleHullSnapshot returns the market set a single-hull post was last (re-)manned
// with, and whether one is recorded yet. Absent after a fresh handler
// (daemon restart) or before the post's first successful manning.
func (h *RunScoutPostCoordinatorHandler) singleHullSnapshot(key string) ([]string, bool) {
	h.singleHullMu.Lock()
	defer h.singleHullMu.Unlock()
	markets, ok := h.singleHullMarketSnapshot[key]
	return markets, ok
}

// setSingleHullSnapshot records the market set a single-hull post is now toured
// against — called once when the post is freshly (re-)manned (pass 2a), and
// again when ensureSingleHullFreshness adopts a post's current markets as a fresh
// baseline (e.g. after a restart cleared the in-memory snapshot).
func (h *RunScoutPostCoordinatorHandler) setSingleHullSnapshot(key string, markets []string) {
	h.singleHullMu.Lock()
	defer h.singleHullMu.Unlock()
	if h.singleHullMarketSnapshot == nil {
		h.singleHullMarketSnapshot = make(map[string][]string)
	}
	h.singleHullMarketSnapshot[key] = markets
}

// clearSingleHullSnapshot forgets a single-hull post's freshness baseline:
// called once a drift-triggered respawn resolves it. The post's next manning
// (pass 2a) sets a fresh one immediately, so this is momentary.
func (h *RunScoutPostCoordinatorHandler) clearSingleHullSnapshot(key string) {
	h.singleHullMu.Lock()
	defer h.singleHullMu.Unlock()
	delete(h.singleHullMarketSnapshot, key)
}

// noteSingleHullDriftPending records the FIRST tick a single-hull post's market set
// was seen drifting from its snapshot and returns how long the drift episode
// has been pending — the single-hull mirror of noteDriftPending, backed by a SEPARATE
// map (see singleHullDriftPendingSince's doc comment on the handler struct for why it
// cannot share driftPendingSince).
func (h *RunScoutPostCoordinatorHandler) noteSingleHullDriftPending(key string) time.Duration {
	h.singleHullMu.Lock()
	defer h.singleHullMu.Unlock()
	if h.singleHullDriftPendingSince == nil {
		h.singleHullDriftPendingSince = make(map[string]time.Time)
	}
	now := h.clock.Now()
	since, ok := h.singleHullDriftPendingSince[key]
	if !ok {
		h.singleHullDriftPendingSince[key] = now
		return 0
	}
	return now.Sub(since)
}

// clearSingleHullDriftPending forgets a single-hull post's pending-drift episode:
// called once a respawn resolves it, the drift resolves on its own, or the
// post is no longer single-hull-standing. A nil/absent entry is a harmless no-op.
func (h *RunScoutPostCoordinatorHandler) clearSingleHullDriftPending(key string) {
	h.singleHullMu.Lock()
	defer h.singleHullMu.Unlock()
	delete(h.singleHullDriftPendingSince, key)
}

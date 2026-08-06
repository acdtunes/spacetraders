package commands

import (
	"context"
	"fmt"
	"sync"

	"github.com/andrescamacho/spacetraders-go/internal/application/health"
)

// fleet_autosizer_stall.go turns each class's tick into the three-way verdict the escalation
// layer consumes (internal/application/health/stall.go): PROGRESS, IDLE, or BLOCKED(reason).
//
// The autosizer is the coordinator this lane was written for. Measured live on agent TORWIND:
// eighteen consecutive failed heavy purchases, three whole ticks that accomplished nothing, and
// a heavy decision BLOCKED on four guards every single tick — for HOURS, with nothing raised.
// The guard stack was doing its job perfectly; the problem is that a refusal and a rest look
// identical from outside.
//
// The verdict is reported ONCE PER CLASS PER TICK, from the reconcile loop, because the streak
// IS the tick count: a second report inflates it and a skipped one stalls it.

// autosizerStallCoordinator names this coordinator in the stall key, metric labels and event
// payload. A stable identifier, never a formatted string.
const autosizerStallCoordinator = "fleet_autosizer"

const (
	// stallReasonDemandError is an infra fault reading one class's demand. The tick survives it
	// (the other classes still size) but this class did NOT get to decide — a block, not a rest.
	stallReasonDemandError health.StallReason = "demand_error"
	// stallReasonDemandUnreadable is the fail-closed unreadable demand signal: the class wanted
	// to know its shortfall and could not find out. Distinct from "no shortfall" precisely
	// because it is the case that MASQUERADES as no shortfall.
	stallReasonDemandUnreadable health.StallReason = "demand_unreadable"
	// stallReasonUnattributedNoBuy is the coarse fallback: unmet demand produced no hull, and
	// the blocking guard could not be attributed to this tick. It covers the refusals the guard
	// stack never sees (the heavy anti-thrash hold, the per-tick purchase cap, an unwired
	// purchaser, a failed buy) and the case where no tap is installed at all. Honest rather than
	// precise: naming a guard that did not block would be worse than admitting we do not know.
	stallReasonUnattributedNoBuy health.StallReason = "unmet_demand_no_buy"
)

// classStallVerdict maps ONE class's tick to its verdict. Pure: it judges facts the reconcile
// loop already holds, and it can therefore never influence what that loop does.
//
// blockedGuard is the first failing guard when the tap could attribute one; ok=false falls back
// to the coarse reason above.
func classStallVerdict(d ClassDemand, demandErr error, bought, unmetNoBuy bool, blockedGuard GuardName, guardKnown bool) health.TickOutcome {
	if demandErr != nil {
		return health.TickBlocked(stallReasonDemandError, demandErr.Error())
	}
	if !d.Readable {
		return health.TickBlocked(stallReasonDemandUnreadable, d.Reason)
	}
	if bought {
		return health.TickProgress()
	}
	if !unmetNoBuy {
		// No shortfall: the class is satisfied. Nothing to do, and that is CORRECT — this is the
		// branch that has to stay silent forever or the alarm is worthless.
		return health.TickIdle()
	}
	if guardKnown {
		return health.TickBlocked(health.StallReason(blockedGuard), fmt.Sprintf("shortfall %d unmet; first failing guard %s", d.Shortfall(), blockedGuard))
	}
	return health.TickBlocked(stallReasonUnattributedNoBuy, fmt.Sprintf("shortfall %d unmet and no hull bought", d.Shortfall()))
}

// observeClassStall reports one class's verdict to the escalator, keyed per container AND per
// class so a blocked heavy is never closed out by a healthy light.
func (h *RunFleetAutosizerCoordinatorHandler) observeClassStall(ctx context.Context, cmd *RunFleetAutosizerCoordinatorCommand, class HullClass, outcome health.TickOutcome) {
	if h.stall == nil {
		return
	}
	h.stall.Observe(ctx, health.StallKey{
		Coordinator: autosizerStallCoordinator,
		ContainerID: cmd.ContainerID,
		Scope:       string(class),
		PlayerID:    cmd.PlayerID,
	}, outcome)
}

// --- the first-failing-guard tap ---

// blockedGuardTap is a pass-through MetricsSink decorator that also captures the FIRST FAILING
// GUARD of each class's decision, so the reconcile loop can name it in the tick's stall verdict.
//
// WHY A TAP AND NOT A RETURN VALUE. The guard verdict is produced inside the coordinator's ACT
// step (fleet_autosizer_act.go), which is owned by a concurrent lane and must not be edited here.
// sizeClass already PUBLISHES the blocking guard on exactly one seam — MetricsSink.RecordBlocked
// — so this taps that existing channel rather than opening a new one. Nothing is re-evaluated and
// no port is read twice: the guard reported is the very one the decision log printed.
//
// IT CHANGES NO VERDICT AND NO SPEND. Every method forwards to the inner sink unchanged, and the
// captured guard is only ever read by the observability path (RULINGS #4 untouched).
//
// ATTRIBUTION IS FAIL-SAFE. The autosizer handler is a registered singleton serving every
// player's ticks, so two containers can be in the act path at once. Each class slot records the
// container that most recently claimed it; take() returns a guard ONLY to that claimant, so an
// interleaved sibling degrades the reason to the coarse fallback rather than pinning one
// container's refusal on another's streak.
type blockedGuardTap struct {
	inner MetricsSink

	mu    sync.Mutex
	slots map[HullClass]tapSlot
}

type tapSlot struct {
	owner  string
	guard  GuardName
	sealed bool // a verdict has been recorded for this claim
}

// NewBlockedGuardTap wraps a MetricsSink so the coordinator can attribute blocked decisions. The
// inner sink may be nil (metrics disabled): every forward is nil-safe.
func NewBlockedGuardTap(inner MetricsSink) MetricsSink {
	return &blockedGuardTap{inner: inner, slots: make(map[HullClass]tapSlot)}
}

// expect claims a class's slot for one container's tick, discarding anything left in it.
func (t *blockedGuardTap) expect(owner string, class HullClass) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.slots[class] = tapSlot{owner: owner}
}

// take consumes the guard recorded since the claimant's expect, if the claim still stands.
func (t *blockedGuardTap) take(owner string, class HullClass) (GuardName, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	slot, ok := t.slots[class]
	if !ok || slot.owner != owner || !slot.sealed {
		return "", false
	}
	delete(t.slots, class)
	return slot.guard, true
}

func (t *blockedGuardTap) RecordDemand(class HullClass, demand, current int) {
	if t.inner != nil {
		t.inner.RecordDemand(class, demand, current)
	}
}

func (t *blockedGuardTap) RecordPurchase(class HullClass) {
	if t.inner != nil {
		t.inner.RecordPurchase(class)
	}
}

func (t *blockedGuardTap) RecordBlocked(class HullClass, guard GuardName) {
	t.mu.Lock()
	if slot, ok := t.slots[class]; ok && !slot.sealed {
		slot.guard, slot.sealed = guard, true
		t.slots[class] = slot
	}
	t.mu.Unlock()

	if t.inner != nil {
		t.inner.RecordBlocked(class, guard)
	}
}

func (t *blockedGuardTap) RecordZeroEffectAlarm() {
	if t.inner != nil {
		t.inner.RecordZeroEffectAlarm()
	}
}

func (t *blockedGuardTap) RecordHeavyReserve(playerID string, reserve, target int64, owned, capacity int) {
	if t.inner != nil {
		t.inner.RecordHeavyReserve(playerID, reserve, target, owned, capacity)
	}
}

func (t *blockedGuardTap) RecordSizingEnabled(playerID string, enabled bool) {
	if t.inner != nil {
		t.inner.RecordSizingEnabled(playerID, enabled)
	}
}

func (t *blockedGuardTap) ObserveHeavyPricePremium(playerID string, paid, cheapestKnown int64) {
	if t.inner != nil {
		t.inner.ObserveHeavyPricePremium(playerID, paid, cheapestKnown)
	}
}

// blockedGuardFor claims and then reads this class's slot around one sizeClass call. Returns
// ok=false whenever no tap is installed or the claim did not survive, which the verdict maps to
// the coarse fallback reason.
func (h *RunFleetAutosizerCoordinatorHandler) expectBlockedGuard(cmd *RunFleetAutosizerCoordinatorCommand, class HullClass) {
	if tap, ok := h.metrics.(*blockedGuardTap); ok {
		tap.expect(cmd.ContainerID, class)
	}
}

func (h *RunFleetAutosizerCoordinatorHandler) takeBlockedGuard(cmd *RunFleetAutosizerCoordinatorCommand, class HullClass) (GuardName, bool) {
	tap, ok := h.metrics.(*blockedGuardTap)
	if !ok {
		return "", false
	}
	return tap.take(cmd.ContainerID, class)
}

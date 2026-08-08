package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/health"
)

// This file turns each class's tick into the three-way verdict the escalation layer consumes
// (internal/application/health/stall.go): PROGRESS, IDLE, or BLOCKED(reason).
//
// It exists because a guard stack doing its job perfectly and a coordinator with nothing to do look
// identical from outside: a buyer can refuse every tick for hours, correctly, and raise nothing.
// The verdict is what separates a refusal from a rest.
//
// It is reported ONCE PER CLASS PER TICK, from the reconcile loop, because the streak IS the tick
// count: a second report inflates it and a skipped one stalls it.

// The coordinator names carried in the stall key, metric labels and event payload. Stable
// identifiers, never formatted strings: a renamed one silently starts a fresh streak and closes
// out whatever the old name was escalating.
const (
	growthStallCoordinator = "fleet_growth"
)

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

// observeClassStallOn reports one class's verdict to the escalator, keyed per COORDINATOR, per
// container AND per class — so a blocked heavy is never closed out by a healthy light, and two
// coordinators watching the same class never share one streak.
func observeClassStallOn(ctx context.Context, stall health.StallObserver, coordinator, containerID string, playerID int, class HullClass, outcome health.TickOutcome) {
	if stall == nil {
		return
	}
	stall.Observe(ctx, health.StallKey{
		Coordinator: coordinator,
		ContainerID: containerID,
		Scope:       string(class),
		PlayerID:    playerID,
	}, outcome)
}

func (h *RunFleetGrowthCoordinatorHandler) observeClassStall(ctx context.Context, containerID string, playerID int, class HullClass, outcome health.TickOutcome) {
	observeClassStallOn(ctx, h.stall, growthStallCoordinator, containerID, playerID, class, outcome)
}

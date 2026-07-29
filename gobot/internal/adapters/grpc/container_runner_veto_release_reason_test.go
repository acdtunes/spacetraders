package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// PRODUCTION REPRODUCTION (agent TORWIND, 4 containers). A tour reaches its end still
// holding cargo it bought and could not sell:
//
//	completion refused (honest-completion contract): stranded cargo: 18 ANTIMATTER
//	still aboard at X1-MY3-EE1D (tour-bought, unsold) - reporting failure
//
// The refusal is correct and stays. The defect is what happens NEXT. The runner knows
// exactly why the run failed — it holds the veto reason — and then throws that away at
// the release, stamping the hull with the same generic "failed" a routing error or a
// planner outage leaves. The strand therefore survives nowhere in durable state, so the
// next tick cannot tell a hull that needs re-routing from one that just had a bad lane:
// the trade fleet coordinator reads the hull's release reason, sees "failed", and
// relaunches an identical tour onto the identical dead ground, which inherits the same
// unsold obligation and dies the same way.
//
// The veto must be legible in durable ship state (RULINGS #2 — the next tick derives
// its decision from what is persisted, and holds nothing across ticks itself).
func TestHonestCompletionVetoStampsTheVetoOnTheHullNotAGenericFailure(t *testing.T) {
	playerID := 2
	hull := newIdleTradeShip(t, "TORWIND-19", playerID)

	const containerID = "tour-TORWIND-19-stranded"
	require.NoError(t, hull.AssignToContainer(containerID, shared.NewRealClock()))
	repo := &tradeRouteShipRepo{ships: map[string]*navigation.Ship{"TORWIND-19": hull}}

	entity := container.NewContainer(containerID, container.ContainerTypeTrading, playerID, 1, nil,
		map[string]interface{}{"ship_symbol": "TORWIND-19"}, nil)
	require.NoError(t, entity.Start())

	runner := NewContainerRunner(entity, &reporterMediator{resp: &reporterResponse{
		ok:     false,
		reason: "stranded cargo: 18 ANTIMATTER still aboard at X1-MY3-EE1D (tour-bought, unsold) - reporting failure",
	}}, nil, noopLogRepo{}, nil, repo, nil)

	runIterationsAndFinish(t, runner)

	// The honest-completion contract is UNCHANGED: the run still refuses to report
	// success, and still terminalizes FAILED.
	require.Equal(t, container.ContainerStatusFailed, entity.Status(),
		"the honest-completion veto must still refuse a false success")

	// ...and the hull now carries WHY, so a later tick can route it out.
	require.False(t, hull.IsAssigned(), "the veto path still releases the hull")
	assignment := hull.Assignment()
	require.NotNil(t, assignment)
	require.NotNil(t, assignment.ReleaseReason())
	require.Equal(t, common.ReleaseReasonHonestCompletionVeto, *assignment.ReleaseReason(),
		"a hull released by the honest-completion veto must be distinguishable from any other failure")
}

// The new release reason must be scoped to the VETO. An ordinary run failure (a real Go
// error — routing down, a planner outage) still releases as a generic failure, so the
// re-routing the veto reason triggers is never applied to a hull whose lane was simply
// having a bad day.
func TestOrdinaryFailureStillReleasesAsAGenericFailure(t *testing.T) {
	playerID := 2
	hull := newIdleTradeShip(t, "TORWIND-20", playerID)

	const containerID = "tour-TORWIND-20-errored"
	require.NoError(t, hull.AssignToContainer(containerID, shared.NewRealClock()))
	repo := &tradeRouteShipRepo{ships: map[string]*navigation.Ship{"TORWIND-20": hull}}

	entity := container.NewContainer(containerID, container.ContainerTypeTrading, playerID, 1, nil,
		map[string]interface{}{"ship_symbol": "TORWIND-20"}, nil)
	require.NoError(t, entity.Start())

	runner := NewContainerRunner(entity, &reporterMediator{resp: &reporterResponse{ok: true}},
		nil, noopLogRepo{}, nil, repo, nil)
	runner.handleError(context.DeadlineExceeded)
	runner.releaseShipAssignments("failed")

	assignment := hull.Assignment()
	require.NotNil(t, assignment)
	require.NotNil(t, assignment.ReleaseReason())
	require.NotEqual(t, common.ReleaseReasonHonestCompletionVeto, *assignment.ReleaseReason(),
		"only the honest-completion veto may claim the veto release reason")
}

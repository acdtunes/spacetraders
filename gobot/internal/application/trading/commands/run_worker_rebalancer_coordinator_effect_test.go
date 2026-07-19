package commands

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/health"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// These tests pin the wiring at the worker-rebalancer's effect self-check: a pass
// that keeps finding a real, jump-routable vacancy (would-ferry > 0) yet dispatches nothing
// — the "dry-run survived a day" class the error-streak monitor cannot see because the loop
// never errors — WARNs exactly once per episode, naming the mode flag that explains it,
// while a pass that actually ferries stays silent.

// countRebalancerLogs counts captured log messages containing sub.
func countRebalancerLogs(l *tradeCaptureLogger, sub string) int {
	n := 0
	for _, m := range l.messages {
		if strings.Contains(m, sub) {
			n++
		}
	}
	return n
}

// rebalancerDryRunVacancyHandler builds the canonical single-vacancy scenario
// (TestRebalancer_Vacancy_AllConditionsMet_Ferries): DP51 is a 20m-old factory with no
// in-system light and a 2-idle-light source routable 1 hop away — a genuine would-ferry.
// Because dry-run persists/claims nothing, every reconcile pass re-derives the SAME
// vacancy from unchanged state, so desired stays 1 and ferried stays 0 tick after tick.
func rebalancerDryRunVacancyHandler(t *testing.T) (*RunWorkerRebalancerCoordinatorHandler, *fakeRebalancerDaemonClient) {
	t.Helper()
	clock := clockAt(0)
	shipRepo := &fakeRebalancerShipRepo{clock: clock, ships: []*navigation.Ship{
		rebalancerLight(t, "SRC-A", "X1-SRC-A1"),
		rebalancerLight(t, "SRC-B", "X1-SRC-A2"),
	}}
	cq := &fakeRebalancerContainerQuery{factories: []ActiveFactoryContainer{
		{SystemSymbol: "X1-DP51", StartedAt: baseTime.Add(-20 * time.Minute)},
	}}
	daemonClient := &fakeRebalancerDaemonClient{}
	graph := &fakeHopGraph{hops: map[string]int{"X1-SRC->X1-DP51": 1}}
	handler := newTestRebalancerHandler(shipRepo, daemonClient, cq, ferryDefaultMarket(), graph, clock)
	return handler, daemonClient
}

// TestRebalancerEffect_DryRunSuppressesFerries_WarnsOnce pins the pathology: a dry-run pass
// that would-ferry a real vacancy yet dispatches nothing, sustained over the self-check
// horizon, emits exactly ONE WARNING naming dry_run — not one per tick.
func TestRebalancerEffect_DryRunSuppressesFerries_WarnsOnce(t *testing.T) {
	const horizon = 3
	handler, daemonClient := rebalancerDryRunVacancyHandler(t)
	logger := &tradeCaptureLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	cmd := rebalancerTestCmd()
	cmd.DryRun = true
	cmd.EffectSelfcheckTicks = horizon
	effMon := health.NewEffectTracker(cmd.effectSelfcheckTicks())

	for i := 1; i <= horizon*2; i++ {
		ferried, desired, err := handler.reconcileOnce(ctx, cmd)
		require.NoError(t, err)
		require.Equal(t, 0, ferried, "tick %d: dry-run dispatches nothing", i)
		require.Equal(t, 1, desired, "tick %d: the routable vacancy is a would-ferry candidate", i)
		handler.noteEffect(ctx, cmd, effMon, desired, ferried)
	}

	require.Empty(t, daemonClient.ferried, "dry-run never persists a ferry")
	require.Equal(t, 1, countRebalancerLogs(logger, "dispatched nothing for"),
		"a sustained dry-run inert episode WARNs exactly once, not per tick")
	require.True(t, logger.loggedContaining("dry_run is on", "sp-57g9"),
		"the WARN names dry_run as the cause")
}

// TestRebalancerEffect_BelowHorizon_NeverWarns pins no premature alarm: fewer inert ticks
// than the horizon stay silent.
func TestRebalancerEffect_BelowHorizon_NeverWarns(t *testing.T) {
	const horizon = 5
	handler, _ := rebalancerDryRunVacancyHandler(t)
	logger := &tradeCaptureLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	cmd := rebalancerTestCmd()
	cmd.DryRun = true
	cmd.EffectSelfcheckTicks = horizon
	effMon := health.NewEffectTracker(cmd.effectSelfcheckTicks())

	for i := 1; i < horizon; i++ {
		ferried, desired, err := handler.reconcileOnce(ctx, cmd)
		require.NoError(t, err)
		handler.noteEffect(ctx, cmd, effMon, desired, ferried)
	}
	require.Zero(t, countRebalancerLogs(logger, "dispatched nothing for"),
		"an inert streak shorter than the horizon must not warn")
}

// TestRebalancerEffect_RealFerry_NeverWarns pins that a pass that actually dispatches
// (dry-run off) is healthy: the would-ferry becomes a real ferry, so the effect self-check
// resets and never warns however long the loop runs.
func TestRebalancerEffect_RealFerry_NeverWarns(t *testing.T) {
	const horizon = 3
	handler, _ := rebalancerDryRunVacancyHandler(t) // dry-run OFF below → real ferries
	logger := &tradeCaptureLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	cmd := rebalancerTestCmd() // DryRun defaults false
	cmd.EffectSelfcheckTicks = horizon
	effMon := health.NewEffectTracker(cmd.effectSelfcheckTicks())

	for i := 1; i <= horizon*2; i++ {
		ferried, desired, err := handler.reconcileOnce(ctx, cmd)
		require.NoError(t, err)
		handler.noteEffect(ctx, cmd, effMon, desired, ferried)
	}
	require.Zero(t, countRebalancerLogs(logger, "dispatched nothing for"),
		"a coordinator taking real effect-actions (or idle for want of a source) never warns")
}

package commands

// run_trade_fleet_coordinator_coldstart_reserve_test.go — sp-9bacx.
//
// Live staging (TORWINDSTG2, X1-SS66, 2026-08-20): treasury 147505 after the probe seed,
// every tour "Tour max-spend = 0 of 0 deployable (treasury 147505, reserve 150000)" →
// capital_denied, escalating to a 10-minute cooldown, treasury flat for 19+ minutes. The
// tour cannot spend because treasury is below its reserve, and the treasury cannot grow
// because it cannot trade: a permanent deadlock.
//
// These tests drive the reserve each tour launch CARRIES, at both ends of the arc — the
// cold-start crunch that must break out, and the mature economy that must not move.

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// coldStartTreasury is the exact balance the deadlock was observed at.
const coldStartTreasury = 147_505

// matureTreasury stands in for a post-gate EXPANSION economy — orders of magnitude clear of
// every reserve tier.
const matureTreasury = 4_000_000

// launchOneTour runs a reconcile pass over a single idle, past-cooldown trade hull and
// returns the spec the coordinator handed the daemon.
func launchOneTour(t *testing.T, cmd *RunTradeFleetCoordinatorCommand, treasury *fakeTreasury) TourLaunchSpec {
	t.Helper()
	repo := &fakeTradeShipRepo{ships: []*navigation.Ship{parkedTradeHull(t, "TORWINDSTG2-1", 0, "capital_denied")}}
	launcher := &fakeTourLauncher{}
	h := newTradeHandler(repo, launcher, clockAt(600))
	if treasury != nil {
		h.SetTreasuryReader(treasury)
	}

	launched, err := h.reconcileOnce(tradeCtx(&tradeCaptureLogger{}), cmd)

	require.NoError(t, err)
	require.Equal(t, 1, launched)
	require.Len(t, launcher.launches, 1)
	return launcher.launches[0]
}

// tourMaxSpend is what the tour's dynamic budget resolves to under a given reserve — the
// same arithmetic tradeCapitalBudget logs, so a test asserting on it is asserting on the
// live symptom rather than on a proxy for it. reserve 0 is the launch-config absence the
// tour resolves to its own default.
func tourMaxSpend(t *testing.T, treasury, reserve int64) int64 {
	t.Helper()
	resolved := (&RunTourCoordinatorHandler{}).resolveWorkingCapitalReserve(
		&RunTourCoordinatorCommand{WorkingCapitalReserve: reserve}, &tradeCaptureLogger{})
	trade, _ := common.CapitalSplit(common.TradeCapitalSharePct, common.CapitalDeployable(treasury, resolved), true, true)
	return trade
}

// THE BUG. At the cold-start treasury the mature 150k default deploys nothing, so the tour
// is denied capital before a market is looked at. The coordinator must hand the launch the
// immutable 50k anti-stall floor instead (RULINGS #5), which leaves real headroom.
func TestTradeReconcile_ColdStartCrunch_LaunchesOnTheImmutableFloor(t *testing.T) {
	spec := launchOneTour(t, tradeCmd(), &fakeTreasury{credits: coldStartTreasury})

	require.Equal(t, int64(common.ImmutableReserveFloor), spec.WorkingCapitalReserve)
	require.Positive(t, tourMaxSpend(t, coldStartTreasury, spec.WorkingCapitalReserve),
		"the launched tour must have capital to deploy — a 0 budget is the capital_denied deadlock")
}

// ACCEPTANCE 2. A coordinator launched normally at EXPANSION is untouched: the launch carries
// no reserve, exactly as before, and the tour resolves that to the 150k mature default.
func TestTradeReconcile_MatureTreasury_KeepsThe150kDefault(t *testing.T) {
	spec := launchOneTour(t, tradeCmd(), &fakeTreasury{credits: matureTreasury})

	require.Zero(t, spec.WorkingCapitalReserve, "the mature launch must stay byte-identical")
	require.Equal(t, int64(common.NonContractWorkingCapitalFloor),
		(&RunTourCoordinatorHandler{}).resolveWorkingCapitalReserve(
			&RunTourCoordinatorCommand{WorkingCapitalReserve: spec.WorkingCapitalReserve}, &tradeCaptureLogger{}))
}

// The boundary is where the mature default stops protecting anything: at or below it the
// deployable pool is empty and every tour is denied, above it nothing changes.
func TestTradeReconcile_ReserveBoundary(t *testing.T) {
	for _, tc := range []struct {
		name     string
		treasury int64
		want     int64
	}{
		{"one credit under the mature floor is a crunch", common.NonContractWorkingCapitalFloor - 1, common.ImmutableReserveFloor},
		{"exactly at the mature floor still deploys nothing", common.NonContractWorkingCapitalFloor, common.ImmutableReserveFloor},
		{"one credit over it clears", common.NonContractWorkingCapitalFloor + 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, launchOneTour(t, tradeCmd(), &fakeTreasury{credits: tc.treasury}).WorkingCapitalReserve)
		})
	}
}

// A configured [trade_fleet].working_capital_reserve is the captain's number and outranks
// the crunch fallback — the coordinator never overrides an explicit reserve (RULINGS #5).
func TestTradeReconcile_ConfiguredReserve_SurvivesTheCrunch(t *testing.T) {
	cmd := tradeCmd()
	cmd.WorkingCapitalReserve = 250_000
	treasury := &fakeTreasury{credits: coldStartTreasury}

	require.Equal(t, int64(250_000), launchOneTour(t, cmd, treasury).WorkingCapitalReserve)
	require.Zero(t, treasury.calls, "an explicit reserve settles it — no treasury read at all")
}

// Fail closed both ways: with no reader wired, and with a reader whose read FAILS, the
// launch is exactly what it is today (0 → the tour's 150k default). A balance that could
// not be read never lowers a money guard (RULINGS #4).
func TestTradeReconcile_UnreadableTreasury_FailsClosed(t *testing.T) {
	require.Zero(t, launchOneTour(t, tradeCmd(), nil).WorkingCapitalReserve)
	require.Zero(t, launchOneTour(t, tradeCmd(), &fakeTreasury{err: errors.New("ledger down")}).WorkingCapitalReserve)
}

// One read per reconcile pass, not one per hull: three idle hulls launch three tours off a
// single balance, and the read is re-done on the NEXT pass (RULINGS #2 — no cached cursor).
func TestTradeReconcile_TreasuryReadIsOncePerPass(t *testing.T) {
	repo := &fakeTradeShipRepo{ships: []*navigation.Ship{
		parkedTradeHull(t, "TORWINDSTG2-1", 0, "capital_denied"),
		parkedTradeHull(t, "TORWINDSTG2-2", 0, "capital_denied"),
		parkedTradeHull(t, "TORWINDSTG2-3", 0, "capital_denied"),
	}}
	launcher := &fakeTourLauncher{}
	treasury := &fakeTreasury{credits: coldStartTreasury}
	h := newTradeHandler(repo, launcher, clockAt(600))
	h.SetTreasuryReader(treasury)
	ctx := tradeCtx(&tradeCaptureLogger{})

	launched, err := h.reconcileOnce(ctx, tradeCmd())
	require.NoError(t, err)
	require.Equal(t, 3, launched)
	require.Equal(t, 1, treasury.calls)

	_, err = h.reconcileOnce(ctx, tradeCmd())
	require.NoError(t, err)
	require.Equal(t, 2, treasury.calls, "each pass re-derives the reserve from live treasury")
}

// An idle fleet costs no treasury read: the resolve is lazy, so a coordinator ticking every
// 30s over hulls that are all mid-tour never touches the ledger.
func TestTradeReconcile_NothingToLaunch_ReadsNoTreasury(t *testing.T) {
	repo := &fakeTradeShipRepo{ships: []*navigation.Ship{runningTradeHull(t, "TORWINDSTG2-9")}}
	treasury := &fakeTreasury{credits: coldStartTreasury}
	h := newTradeHandler(repo, &fakeTourLauncher{}, clockAt(600))
	h.SetTreasuryReader(treasury)

	launched, err := h.reconcileOnce(tradeCtx(&tradeCaptureLogger{}), tradeCmd())

	require.NoError(t, err)
	require.Zero(t, launched)
	require.Zero(t, treasury.calls)
}

package grpc

// A warp refused for insufficient fuel (HTTP 400, code 4203) is a PERMANENT verdict for
// that (ship, destination, fuel-state) triple: no amount of retrying changes it, yet a
// retry burns real API budget on a call that cannot succeed and holds the ship claim for
// the whole restart window, blocking every other operation on that hull.
//
// These tests pin the classifier's effect on the WARP container: a deterministic 4xx (or
// one of the warp path's own pre-typed terminal refusals) terminalizes on the FIRST
// refusal — FAILED, claim released, restart budget untouched — while a transient failure
// (5xx/network/rate-limit) still rides out the full restart budget, and every other
// one-shot ship-op container type is unaffected.

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipApp "github.com/andrescamacho/spacetraders-go/internal/application/ship"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// scriptedMediator answers every Send with the same err, counting calls. It stands in
// for the whole dock/orbit/refuel/warp iteration a real WarpShipCommand drives, so its
// call count IS the container's API-call count at this boundary: one call per iteration
// attempt, whether the first or a restart.
type scriptedMediator struct {
	mu    sync.Mutex
	err   error
	calls int
}

func (m *scriptedMediator) Send(_ context.Context, _ common.Request) (common.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	return nil, m.err
}
func (m *scriptedMediator) Register(reflect.Type, common.RequestHandler) error { return nil }
func (m *scriptedMediator) RegisterMiddleware(common.Middleware)               {}

func (m *scriptedMediator) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// newWarpFailureTestRunner builds a started WARP container entity claiming TORWIND-F6
// plus a real ContainerRunner around it — the exact shape DaemonServer.WarpShip builds
// (container_ops_ship.go) — over an in-memory ship repo, so the restart-vs-terminalize
// decision under test runs the SAME path production does. The hull is pre-assigned
// (bypassing the claim-retry machinery, which is not what this file is testing) so
// releaseShipAssignments has something real to release.
func newWarpFailureTestRunner(t *testing.T, med common.Mediator, clock shared.Clock) (*ContainerRunner, *navigation.Ship, *releasableShipRepo) {
	t.Helper()
	const containerID = "warp-TORWIND-F6-test"
	const playerID = 1

	hull := newIdleTradeShip(t, "TORWIND-F6", playerID)
	require.NoError(t, hull.AssignToContainer(containerID, shared.NewRealClock()))
	shipRepo := &releasableShipRepo{&tradeRouteShipRepo{ships: map[string]*navigation.Ship{"TORWIND-F6": hull}}}

	entity := container.NewContainer(containerID, container.ContainerTypeWarp, playerID, 1, nil,
		map[string]interface{}{"ship_symbol": "TORWIND-F6", "destination": "X1-TY66-A1", captainManualAuthorityKey: true}, clock)
	require.NoError(t, entity.Start())

	cmd := &shipNav.WarpShipCommand{ShipSymbol: "TORWIND-F6", Destination: "X1-TY66-A1", PlayerID: shared.MustNewPlayerID(playerID)}
	runner := NewContainerRunner(entity, med, cmd, noopLogRepo{}, nil, shipRepo, clock)
	return runner, hull, shipRepo
}

// warpFuelRefusalAPIError reproduces the API's pre-flight 4203 warp refusal verbatim,
// wrapped exactly as the API client wraps every terminal 4xx.
func warpFuelRefusalAPIError() error {
	body := `{"error":{"message":"Warp request failed. Ship TORWIND-F6 requires 31 more fuel for navigation.","code":4203,"data":{"fuelRequired":831,"fuelAvailable":800}}}`
	return fmt.Errorf("warp to X1-TY66-A1 failed: %w", &domainPorts.APIError{StatusCode: 400, Body: body})
}

// TestWarpContainer_PermanentRefusal_TerminalizesOnFirstAttempt is the core acceptance
// clause: "A warp refused with a deterministic 4xx (e.g. 4203 insufficient fuel)
// terminalizes on the FIRST refusal: the container goes to FAILED and releases its ship
// assignment without consuming further restart attempts, and issues no further API
// calls." Each case is a shape the warp path can hand the runner: a raw permanent
// APIError (any non-fuel 4xx bubbles up this way), and the three pre-typed terminal
// refusals the warp executor itself produces (route_executor_warp_errors.go) — including
// ErrWarpWouldStrand with the EXACT numbers observed live.
func TestWarpContainer_PermanentRefusal_TerminalizesOnFirstAttempt(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{
			name: "deterministic 4xx API error (4203 insufficient fuel, unwrapped)",
			err:  warpFuelRefusalAPIError(),
		},
		{
			name: "ErrWarpWouldStrand — the observed 831-required/800-available refusal",
			err: fmt.Errorf("warp of TORWIND-F6: %w", &shipApp.ErrWarpWouldStrand{
				ShipSymbol: "TORWIND-F6", From: "X1-YY85-ZD2D", To: "X1-TY66-A1",
				Required: 831, Available: 800, Capacity: 800,
				Reason: "leg costs more than a full tank",
			}),
		},
		{
			name: "ErrWarpDeadEnd — destination with no way out",
			err: fmt.Errorf("warp of TORWIND-F6: %w", &shipApp.ErrWarpDeadEnd{
				ShipSymbol: "TORWIND-F6", From: "X1-YY85-ZD2D", To: "X1-TY66-A1", System: "X1-TY66",
				Reason: "no waypoint sells fuel and no built jump gate leads out",
			}),
		},
		{
			name: "ErrShipHasNoWarpDrive — ineligible hull",
			err:  fmt.Errorf("warp of TORWIND-F6: %w", &shipApp.ErrShipHasNoWarpDrive{ShipSymbol: "TORWIND-F6"}),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			med := &scriptedMediator{err: tc.err}
			clock := &recordingClock{current: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
			runner, hull, _ := newWarpFailureTestRunner(t, med, clock)

			runner.execute()

			require.Equal(t, 1, med.callCount(),
				"a deterministic refusal must terminalize on the FIRST attempt — issuing no further API calls")
			require.Equal(t, container.ContainerStatusFailed, runner.containerEntity.Status(),
				"the container must still terminalize FAILED (fail-CLOSED — only HOW FAST it gives up changes)")
			require.Equal(t, 0, runner.containerEntity.RestartCount(),
				"no restart attempt may be consumed by a refusal that retrying cannot fix")
			require.False(t, hull.IsAssigned(),
				"the ship claim must be released immediately, not held for a doomed retry window")
		})
	}
}

// TestSubsequentClaimOnSameHullSucceedsImmediatelyAfterPermanentWarpRefusal pins the
// downstream acceptance clause: a subsequent operation on the same hull succeeds
// immediately after a permanent refusal, rather than losing a claim-handoff race to a
// hull still held for a doomed restart window.
func TestSubsequentClaimOnSameHullSucceedsImmediatelyAfterPermanentWarpRefusal(t *testing.T) {
	med := &scriptedMediator{err: fmt.Errorf("warp of TORWIND-F6: %w", &shipApp.ErrWarpWouldStrand{
		ShipSymbol: "TORWIND-F6", From: "X1-YY85-ZD2D", To: "X1-TY66-A1",
		Required: 831, Available: 800, Capacity: 800, Reason: "leg costs more than a full tank",
	})}
	clock := &recordingClock{current: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	runner, hull, shipRepo := newWarpFailureTestRunner(t, med, clock)

	runner.execute()
	require.Equal(t, 1, med.callCount(),
		"precondition: the release below must be reachable on the FIRST attempt, not merely by the time the full (virtual-clock-compressed) restart budget exhausts")
	require.False(t, hull.IsAssigned(), "precondition: the doomed warp released its claim")

	second := container.NewContainer("warp-TORWIND-F6-second", container.ContainerTypeWarp, 1, 1, nil,
		map[string]interface{}{"ship_symbol": "TORWIND-F6", "destination": "X1-TY66-A1", captainManualAuthorityKey: true}, clock)
	require.NoError(t, second.Start())
	secondRunner := NewContainerRunner(second, &scriptedMediator{}, nil, noopLogRepo{}, nil, shipRepo, clock)

	require.NoError(t, secondRunner.createShipAssignments(),
		"a hull released by a terminalized permanent refusal must be claimable on the FIRST attempt — no claim-handoff race")
}

// TestWarpContainer_TransientFailure_StillExhaustsFullRestartBudget is the other half of
// the acceptance contract: "A TRANSIENT failure (5xx/network/rate-limit) still retries as
// it does today — proven by a test for each class." None of these three shapes is (or
// wraps) a *ports.APIError — matching production exactly, where the API client's retry
// ladder absorbs 429/503/5xx/network internally and only ever surfaces "max retries
// exceeded" (see retry_policy.go), never a typed APIError — so the classifier must fall
// through to "not permanent" and the existing restart behavior is untouched.
func TestWarpContainer_TransientFailure_StillExhaustsFullRestartBudget(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{
			name: "5xx exhausts the retry ladder",
			err:  fmt.Errorf("warp to X1-TY66-A1 failed: %w", errors.New("max retries exceeded: server error (503)")),
		},
		{
			name: "network error exhausts the retry ladder",
			err:  fmt.Errorf("warp to X1-TY66-A1 failed: %w", errors.New("max retries exceeded: network error: dial tcp: connection refused")),
		},
		{
			name: "rate limit exhausts the retry ladder",
			err:  fmt.Errorf("warp to X1-TY66-A1 failed: %w", errors.New("max retries exceeded: rate limited (429)")),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			med := &scriptedMediator{err: tc.err}
			clock := &recordingClock{current: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
			runner, hull, _ := newWarpFailureTestRunner(t, med, clock)

			runner.execute()

			require.Equal(t, container.MaxRestartAttempts+1, med.callCount(),
				"a transient failure must still retry across the FULL restart budget, exactly as today")
			require.Equal(t, container.ContainerStatusFailed, runner.containerEntity.Status())
			require.Equal(t, container.MaxRestartAttempts, runner.containerEntity.RestartCount())
			require.False(t, hull.IsAssigned(), "still released once the budget is genuinely exhausted")
		})
	}
}

// TestNonWarpContainer_PermanentAPIError_KeepsRetryingUnaffectedByThisFix is the scope
// guard: the permanent-vs-transient distinction applies only to ContainerTypeWarp. Every
// other one-shot ship-op container type (navigate/route/dock/orbit/refuel/jettison/
// scout_fleet_assignment) shares this exact restart-vs-terminalize code path and would
// show the identical pattern for a deterministic 4xx of its own, but that is unverified
// per type — so a non-warp container must keep burning its full restart budget on a 4xx,
// unchanged.
func TestNonWarpContainer_PermanentAPIError_KeepsRetryingUnaffectedByThisFix(t *testing.T) {
	med := &scriptedMediator{err: fmt.Errorf("dock failed: %w", &domainPorts.APIError{
		StatusCode: 400, Body: `{"error":{"message":"ship is already docked","code":4214}}`,
	})}
	clock := &recordingClock{current: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}

	entity := container.NewContainer("dock-TORWIND-F6-test", container.ContainerTypeDock, 1, 1, nil, nil, clock)
	require.NoError(t, entity.Start())
	runner := NewContainerRunner(entity, med, nil, noopLogRepo{}, nil, nil, clock)

	runner.execute()

	require.Equal(t, container.MaxRestartAttempts+1, med.callCount(),
		"a non-WARP container must be unaffected and keep burning its full restart budget")
	require.Equal(t, container.MaxRestartAttempts, runner.containerEntity.RestartCount())
}

// TestIsPermanentWarpFailure pins the classifier itself: the isPermanentGateAbsence
// shape (errors.As on *ports.APIError + IsClientError()) for a raw API refusal, plus the
// warp path's own pre-typed terminal refusals, and nothing else.
func TestIsPermanentWarpFailure(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"400 API error is permanent", fmt.Errorf("warp to X failed: %w", &domainPorts.APIError{StatusCode: 400, Body: "bad request"}), true},
		{"404 API error is permanent", fmt.Errorf("warp to X failed: %w", &domainPorts.APIError{StatusCode: 404, Body: "not found"}), true},
		{"499 API error is permanent", &domainPorts.APIError{StatusCode: 499, Body: "client closed request"}, true},
		{"500 API error is not permanent (the client never actually constructs one; defensive boundary)", &domainPorts.APIError{StatusCode: 500, Body: "server error"}, false},
		{"wrapped ErrWarpWouldStrand is permanent", fmt.Errorf("warp of TORWIND-F6: %w", &shipApp.ErrWarpWouldStrand{ShipSymbol: "TORWIND-F6", Required: 831, Available: 800, Capacity: 800, Reason: "leg costs more than a full tank"}), true},
		{"wrapped ErrWarpDeadEnd is permanent", fmt.Errorf("warp of TORWIND-F6: %w", &shipApp.ErrWarpDeadEnd{ShipSymbol: "TORWIND-F6", System: "X1-TY66", Reason: "dead end"}), true},
		{"wrapped ErrShipHasNoWarpDrive is permanent", fmt.Errorf("warp of TORWIND-F6: %w", &shipApp.ErrShipHasNoWarpDrive{ShipSymbol: "TORWIND-F6"}), true},
		{"exhausted transient retries is not permanent", fmt.Errorf("warp to X failed: %w", errors.New("max retries exceeded: server error (503)")), false},
		{"plain unrelated error is not permanent", errors.New("some unrelated failure"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, shipApp.IsPermanentWarpFailure(tc.err))
		})
	}
}

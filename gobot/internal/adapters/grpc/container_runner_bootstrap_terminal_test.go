package grpc

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	bootstrapCmd "github.com/andrescamacho/spacetraders-go/internal/application/bootstrap/commands"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
)

// The boot-standing bootstrap coordinator is launched with maxIterations=-1 on the
// premise that its Handle() owns the whole run internally and never returns — but the
// terminal-EXPANSION exit makes Handle() RETURN once the gate is built and the standing
// economy is handed off. The runner-loop model re-enters an infinite container's handler
// the moment it returns, with no sleep in between, so on a mature fleet the coordinator's
// "exiting" turned into a same-second full-fleet-refresh spin (a double-digit share of the
// account-wide request budget). These tests pin the two halves of the contract at the
// runner/response seam the handler-level tests cannot see:
//
//  1. TERMINAL: a response that reports the run terminal must COMPLETE the container —
//     never re-enter Handle(), whatever maxIterations says.
//  2. PACING (defence in depth): an infinite bootstrap container whose handler returns
//     WITHOUT reporting terminal must still be paced at the bootstrap tick between
//     re-entries — a broken terminal signal degrades to the old standing-loop cadence,
//     never to a same-second spin.

// bootstrapTermObserver serves a fixed mature-fleet observation: the home jump gate reads
// COMPLETE and the fleet autosizer is already running — exactly what a mid-era daemon
// restart on an already-bootstrapped fleet observes.
type bootstrapTermObserver struct{ obs bootstrapCmd.Observation }

func (o *bootstrapTermObserver) Observe(_ context.Context, _ int) (bootstrapCmd.Observation, error) {
	return o.obs, nil
}

// bootstrapTermHandoff confirms every hand-off launch instantly (the standing economy is
// already live on a mature fleet; each launch is idempotent at the adapter).
type bootstrapTermHandoff struct{}

func (h *bootstrapTermHandoff) LaunchAutosizer(_ context.Context, _ int, _ string) error { return nil }
func (h *bootstrapTermHandoff) LaunchStandingCoordinators(_ context.Context, _ int, _ string) error {
	return nil
}
func (h *bootstrapTermHandoff) LaunchContractScaler(_ context.Context, _ int, _ string) error {
	return nil
}
func (h *bootstrapTermHandoff) LaunchTradeFleetCoordinator(_ context.Context, _ int, _ string) error {
	return nil
}

type bootstrapTermRefresher struct{}

func (r *bootstrapTermRefresher) RefreshFleet(_ context.Context, _ int) error { return nil }

// matureFleetObservation is an era-5-realistic already-bootstrapped world: gate COMPLETE,
// autosizer running, steady-state treasury/income, probes at target.
func matureFleetObservation() bootstrapCmd.Observation {
	return bootstrapCmd.Observation{
		HomeSystem:           "X1-DY16",
		MarketsTotal:         20,
		MarketsCovered:       20,
		Treasury:             248_000_000,
		IncomePerHour:        9_800_000,
		ProbeCount:           12,
		ProbesScouting:       12,
		GateSite:             "X1-DY16-E1",
		ConstructionStarted:  true,
		ConstructionComplete: true,
		ConstructionPercent:  100,
		ManufacturingRunning: true,
		ManufacturingAdopted: true,
		AutosizerRunning:     true,
		Readable:             true,
	}
}

// bootstrapDispatchMediator routes the runner's Send to the REAL bootstrap handler — the
// full production Handle() loop over a mature-fleet observation — counting invocations so
// a test can prove the runner never re-enters a finished run.
type bootstrapDispatchMediator struct {
	handler *bootstrapCmd.RunBootstrapCoordinatorHandler

	mu    sync.Mutex
	calls int
}

func (m *bootstrapDispatchMediator) Send(ctx context.Context, req common.Request) (common.Response, error) {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()
	return m.handler.Handle(ctx, req)
}
func (m *bootstrapDispatchMediator) Register(_ reflect.Type, _ common.RequestHandler) error {
	return nil
}
func (m *bootstrapDispatchMediator) RegisterMiddleware(_ common.Middleware) {}

func (m *bootstrapDispatchMediator) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// newMatureBootstrapHandler wires the real coordinator handler over a mature-fleet world:
// Handle() derives EXPANSION on tick 1, confirms the hand-off, and returns done.
func newMatureBootstrapHandler() *bootstrapCmd.RunBootstrapCoordinatorHandler {
	h := bootstrapCmd.NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(&bootstrapTermRefresher{})
	h.SetWorldObserver(&bootstrapTermObserver{obs: matureFleetObservation()})
	h.SetHandoffLauncher(&bootstrapTermHandoff{})
	return h
}

// bootstrapRunnerCommand is the real launch command shape the boot-standing path builds
// (identity only; every knob resolves to its documented default).
func bootstrapRunnerCommand(containerID string) *bootstrapCmd.RunBootstrapCoordinatorCommand {
	return &bootstrapCmd.RunBootstrapCoordinatorCommand{
		PlayerID:    4,
		ContainerID: containerID,
		AgentSymbol: "AGENT-4",
	}
}

// A mature-fleet bootstrap run whose response says done must TERMINATE the iteration
// loop: the container completes after exactly one Handle() and is never re-entered —
// even though the boot-standing launch path creates it with maxIterations=-1. This is
// the live regression: the handler logged its terminal exit and returned, and the runner
// re-executed it forever at full speed.
func TestBootstrapTerminalResponseCompletesInfiniteContainer(t *testing.T) {
	rec := &fakeRecorder{}
	SetCaptainEventRecorder(rec)
	defer SetCaptainEventRecorder(nil)

	clock := &recordingClock{current: time.Date(2026, 7, 25, 0, 0, 0, 0, time.UTC)}
	med := &bootstrapDispatchMediator{handler: newMatureBootstrapHandler()}

	const containerID = "bootstrap-player-4-terminal-test"
	entity := container.NewContainer(containerID, container.ContainerTypeBootstrapCoordinator, 4,
		-1, // the boot-standing launch value under test: infinite budget
		nil, map[string]interface{}{"container_id": containerID, "agent_symbol": "AGENT-4"}, clock)
	require.NoError(t, entity.Start())
	r := NewContainerRunner(entity, med, bootstrapRunnerCommand(containerID), noopLogRepo{}, nil, nil, clock)

	done := make(chan struct{})
	go func() {
		r.execute()
		close(done)
	}()

	// The loop must settle: execute() returns after exactly one Handle(). Observing a
	// second Send is the spin, and the test fails immediately rather than waiting out
	// the deadline. Entity state is only read AFTER execute() has returned.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-done:
			require.Equal(t, container.ContainerStatusCompleted, entity.Status(),
				"a done bootstrap run must complete the container")
			require.Equal(t, 1, med.callCount(),
				"the whole bootstrap run is one Handle(); a completed run must never be re-entered")
			return
		case <-deadline:
			r.cancelFunc()
			<-done
			t.Fatalf("bootstrap container never completed (%d Handle calls after 5s) — the done response must terminalize the container", med.callCount())
		case <-time.After(time.Millisecond):
			if med.callCount() > 1 {
				r.cancelFunc() // unwind the spinning goroutine before failing
				<-done
				t.Fatalf("the runner re-entered a finished bootstrap run (%d Handle calls) — terminal response did not stop the iteration loop", med.callCount())
			}
		}
	}
}

// bootstrapTickFloor pins the runner's defence-in-depth pacing floor for an infinite
// bootstrap container to the coordinator's own 45s tick default. A deliberate change to
// the bootstrap tick must update this pin consciously.
const bootstrapTickFloor = 45 * time.Second

// nonTerminalBootstrapMediator returns the real bootstrap response shape WITHOUT the
// terminal signal — modeling a coordinator that keeps standing (or a regressed terminal
// seam) — instantly, so any runner re-entry pacing is fully visible on the clock.
type nonTerminalBootstrapMediator struct {
	mu    sync.Mutex
	calls int
}

func (m *nonTerminalBootstrapMediator) Send(_ context.Context, _ common.Request) (common.Response, error) {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()
	return &bootstrapCmd.RunBootstrapCoordinatorResponse{Ticks: 1}, nil
}
func (m *nonTerminalBootstrapMediator) Register(_ reflect.Type, _ common.RequestHandler) error {
	return nil
}
func (m *nonTerminalBootstrapMediator) RegisterMiddleware(_ common.Middleware) {}

func (m *nonTerminalBootstrapMediator) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// DEFENCE IN DEPTH: an infinite bootstrap container whose handler returns WITHOUT
// reporting the run terminal must still be paced at the bootstrap tick between
// re-entries. Whatever breaks the terminal signal, a standing container degrades to the
// 45s reconcile cadence — never to a same-second full-fleet-refresh spin.
func TestBootstrapInfiniteContainerPacesIterationsAtTick(t *testing.T) {
	rec := &fakeRecorder{}
	SetCaptainEventRecorder(rec)
	defer SetCaptainEventRecorder(nil)

	clock := &recordingClock{current: time.Date(2026, 7, 25, 0, 0, 0, 0, time.UTC)}
	med := &nonTerminalBootstrapMediator{}

	const containerID = "bootstrap-player-4-pacing-test"
	entity := container.NewContainer(containerID, container.ContainerTypeBootstrapCoordinator, 4,
		-1, nil, map[string]interface{}{"container_id": containerID}, clock)
	require.NoError(t, entity.Start())
	r := NewContainerRunner(entity, med, bootstrapRunnerCommand(containerID), noopLogRepo{}, nil, nil, clock)

	done := make(chan struct{})
	go func() {
		r.execute()
		close(done)
	}()

	// Let three iterations run, then stop the loop.
	require.Eventually(t, func() bool { return med.callCount() >= 3 }, 5*time.Second, time.Millisecond,
		"the infinite container never reached three iterations")
	r.cancelFunc()
	<-done

	// Every re-entry must have been preceded by a full bootstrap-tick pacing sleep: the
	// first recorded sleep is the startup jitter; each subsequent one is the pacing floor.
	sleeps := clock.recorded()
	require.GreaterOrEqual(t, len(sleeps), 3,
		"expected the startup jitter plus a pacing sleep before each re-entry, got %v", sleeps)
	paced := 0
	for _, d := range sleeps[1:] {
		if d == bootstrapTickFloor {
			paced++
		}
	}
	require.GreaterOrEqual(t, paced, 2,
		"an infinite bootstrap container must sleep the %s tick between iterations (defence in depth against the same-second spin); recorded sleeps: %v", bootstrapTickFloor, sleeps)
}

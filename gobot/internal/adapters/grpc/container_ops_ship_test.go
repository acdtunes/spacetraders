package grpc

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// capturingLogRepo records every runner log line so a test can assert the
// captain-authority override audit line (sp-sg35) actually fires. Only Log is
// exercised by the ContainerRunner; the embedded interface backs the rest.
type capturingLogRepo struct {
	persistence.ContainerLogRepository
	mu      sync.Mutex
	entries []capturedLogLine
}

type capturedLogLine struct {
	level    string
	message  string
	metadata map[string]interface{}
}

func (r *capturingLogRepo) Log(_ context.Context, _ string, _ int, message, level string, metadata map[string]interface{}) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, capturedLogLine{level: level, message: message, metadata: metadata})
	return nil
}

func (r *capturingLogRepo) find(level, substr string) (capturedLogLine, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.entries {
		if e.level == level && strings.Contains(e.message, substr) {
			return e, true
		}
	}
	return capturedLogLine{}, false
}

// snapshot returns a copy of every captured line under the lock. Callers that want
// to show the sink in a failure message MUST use this and never read the entries
// slice directly: ContainerRunner.Log persists each line on a detached goroutine
// (container_runner.go), so an unsynchronized read of entries races that writer —
// the -race trip the single read at the assertion caused (sp-yon7).
func (r *capturingLogRepo) snapshot() []capturedLogLine {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]capturedLogLine, len(r.entries))
	copy(out, r.entries)
	return out
}

// waitForLog blocks until a captured line at the given level whose message contains
// substr appears, or the deadline elapses. ContainerRunner.Log persists each line
// asynchronously (so the claim path never blocks on the log sink), so an audit line
// lands shortly AFTER the synchronous Start() that triggered it returns; a single
// read of the sink races that goroutine and intermittently sees it empty (sp-yon7).
// Polling a mutex-guarded read with a bounded deadline removes the ordering flake
// without weakening the assertion — the line must still appear.
func (r *capturingLogRepo) waitForLog(t *testing.T, level, substr string, timeout time.Duration) capturedLogLine {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if line, ok := r.find(level, substr); ok {
			return line
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for a %s log containing %q; got: %+v",
				timeout, level, substr, r.snapshot())
			return capturedLogLine{} // unreachable: Fatalf stops the test
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// sp-sg35 part 3 — the captain-authority exemption (the permit half). A deliberate
// captain CLI manual op (navigate/dock/orbit/refuel/jettison) carries the
// captainManualAuthorityKey flag, so it may operate a fleet-dedicated hull without
// unassigning first (the captain ruled unassign-first is non-atomic during the
// high-restart window). The override is PERMITTED and AUDITED — one WARNING line
// naming ship, op and the overridden dedication.
func TestCaptainManualAuthorityOverridesForeignDedication(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)

	hull := newIdleTradeShip(t, "TORWIND-19", playerID)
	hull.SetDedicatedFleet("trade")
	shipRepo := &tradeRouteShipRepo{ships: map[string]*navigation.Ship{"TORWIND-19": hull}}
	logs := &capturingLogRepo{}

	// A navigate container carries the captain-authority flag and NO operation key
	// (legacy path) — exactly what container_ops_ship.go now stamps.
	const containerID = "navigate-TORWIND-19"
	entity := container.NewContainer(containerID, container.ContainerTypeNavigate, playerID, 1, nil,
		map[string]interface{}{"ship_symbol": "TORWIND-19", captainManualAuthorityKey: true}, nil)
	require.NoError(t, s.containerRepo.Add(context.Background(), entity, "navigate_ship"))

	runner := NewContainerRunner(entity, s.mediator, nil, logs, s.containerRepo, shipRepo, s.clock)
	defer runner.cancelFunc()

	err := runner.Start()

	require.NoError(t, err, "a captain manual op must be permitted to operate a fleet-dedicated hull")
	requireContainerState(t, db, containerID, "RUNNING", "")
	require.True(t, hull.IsAssigned())
	require.Equal(t, containerID, hull.ContainerID())

	// Every override MUST be audited: one WARNING line naming ship, op, dedication.
	// The audit line persists on Log's detached goroutine, so wait for it instead of
	// reading the sink once (sp-yon7): the single read raced that goroutine — seeing an
	// empty sink, and tripping -race on the unsynchronized entries read in the message.
	line := logs.waitForLog(t, "WARNING", "Captain-authority override", 2*time.Second)
	require.Equal(t, "TORWIND-19", line.metadata["ship_symbol"])
	require.Equal(t, "NAVIGATE", line.metadata["op"])
	require.Equal(t, "trade", line.metadata["dedicated_fleet"])
}

// sp-sg35 part 3 — the exemption must NOT leak. An automated claim on the legacy
// path (here a coordinator-type container with neither the captain-authority flag
// nor an operation key) is STILL rejected on a foreign-dedicated hull, and emits
// no override audit line. This is the guarantee that the flag is load-bearing and
// scoped strictly to the captain CLI path.
func TestWithoutCaptainAuthorityForeignDedicationStillRejected(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)

	hull := newIdleTradeShip(t, "TORWIND-19", playerID)
	hull.SetDedicatedFleet("trade")
	shipRepo := &tradeRouteShipRepo{ships: map[string]*navigation.Ship{"TORWIND-19": hull}}
	logs := &capturingLogRepo{}

	// A coordinator-type container WITHOUT the captain flag (models any automated
	// legacy claim). The exemption must not reach it.
	const containerID = "automated-TORWIND-19"
	entity := container.NewContainer(containerID, container.ContainerTypeTrading, playerID, 1, nil,
		map[string]interface{}{"ship_symbol": "TORWIND-19"}, nil)
	require.NoError(t, s.containerRepo.Add(context.Background(), entity, "some_coordinator"))

	runner := NewContainerRunner(entity, s.mediator, nil, logs, s.containerRepo, shipRepo, s.clock)

	err := runner.Start()

	require.Error(t, err, "without the captain-authority flag a foreign-dedicated hull must still be rejected")
	var dedErr *shared.ShipDedicatedToOtherFleetError
	require.ErrorAs(t, err, &dedErr)
	requireContainerState(t, db, containerID, "FAILED", "claim_failed")
	require.False(t, hull.IsAssigned())
	if _, overrode := logs.find("WARNING", "Captain-authority override"); overrode {
		t.Fatalf("no override may be logged without the captain-authority flag: %+v", logs.snapshot())
	}
}

// sp-sg35 part 3 — the wiring: every captain CLI manual op (navigate/dock/orbit/
// refuel/jettison) must stamp the captain-authority flag into its launch config,
// or the guard would reject the captain from operating a dedicated hull. Persisted
// synchronously by each op before its async runner starts, so it is readable here.
func TestCaptainManualCLIOpsStampAuthorityFlag(t *testing.T) {
	ops := []struct {
		name string
		call func(s *DaemonServer, ship string, pid int) (string, error)
	}{
		{"navigate", func(s *DaemonServer, ship string, pid int) (string, error) {
			return s.NavigateShip(context.Background(), ship, "X1-TR-A1", pid)
		}},
		{"route", func(s *DaemonServer, ship string, pid int) (string, error) {
			return s.RouteShip(context.Background(), ship, "X1-TR-A1", pid)
		}},
		{"dock", func(s *DaemonServer, ship string, pid int) (string, error) {
			return s.DockShip(context.Background(), ship, pid)
		}},
		{"orbit", func(s *DaemonServer, ship string, pid int) (string, error) {
			return s.OrbitShip(context.Background(), ship, pid)
		}},
		{"refuel", func(s *DaemonServer, ship string, pid int) (string, error) {
			return s.RefuelShip(context.Background(), ship, pid, nil)
		}},
		{"jettison", func(s *DaemonServer, ship string, pid int) (string, error) {
			return s.JettisonCargo(context.Background(), ship, pid, "IRON_ORE", 1)
		}},
	}

	for _, op := range ops {
		t.Run(op.name, func(t *testing.T) {
			s, db, playerID := newRecoveryTestServer(t)
			// Undedicated hull → the claim succeeds cleanly; we only care that the
			// persisted config carries the authority flag.
			hull := newIdleTradeShip(t, "SHIP-X", playerID)
			s.shipRepo = &tradeRouteShipRepo{ships: map[string]*navigation.Ship{"SHIP-X": hull}}

			containerID, err := op.call(s, "SHIP-X", playerID)
			require.NoError(t, err)
			if r := s.registeredRunner(containerID); r != nil {
				defer r.cancelFunc()
			}

			var model persistence.ContainerModel
			require.NoError(t, db.First(&model, "id = ?", containerID).Error)
			require.Contains(t, model.Config, `"captain_manual_authority":true`,
				"every captain CLI manual op must stamp the authority flag into its launch config")
		})
	}
}

// sp-sfoe — the captain-context reservation pass-through (the permit half). A
// captain reservation (sp-i1ku) locks coordinators out of a hull the captain is
// operating by hand, but a deliberate captain CLI manual op (navigate/dock/orbit/
// refuel/jettison — the only setters of captainManualAuthorityKey) must be able to
// operate that reserved hull WITHOUT dropping the reservation. The claim is SKIPPED
// rather than converting the reservation into a container assignment: the hull stays
// assignment_owner=captain / container_id="" for the whole op, so every coordinator
// claim path stays locked out before, during, AND after, and the release path
// (FindByContainer, keyed on container_id) finds nothing to release. The pass-through
// is PERMITTED and AUDITED — one WARNING line naming ship and op. This is the exact
// bug the bead filed: a captain navigate on a reserved hull died on "reserved by the
// captain", forcing raw curls; sp-i1ku's whole purpose was captain manual ops without
// them.
func TestCaptainContextOpPermittedOnCaptainReservedHull(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)

	hull := newIdleTradeShip(t, "TORWIND-19", playerID)
	require.NoError(t, hull.ReserveByCaptain("captain manual survey", shared.NewRealClock()))
	shipRepo := &tradeRouteShipRepo{ships: map[string]*navigation.Ship{"TORWIND-19": hull}}
	logs := &capturingLogRepo{}

	// A navigate container carries the captain-authority flag and NO operation key
	// (legacy path) — exactly what container_ops_ship.go stamps for a captain CLI op.
	const containerID = "navigate-TORWIND-19"
	entity := container.NewContainer(containerID, container.ContainerTypeNavigate, playerID, 1, nil,
		map[string]interface{}{"ship_symbol": "TORWIND-19", captainManualAuthorityKey: true}, nil)
	require.NoError(t, s.containerRepo.Add(context.Background(), entity, "navigate_ship"))

	runner := NewContainerRunner(entity, s.mediator, nil, logs, s.containerRepo, shipRepo, s.clock)
	defer runner.cancelFunc()

	err := runner.Start()

	require.NoError(t, err, "a captain manual op must be permitted to operate a captain-reserved hull")
	requireContainerState(t, db, containerID, "RUNNING", "")
	// The reservation must SURVIVE untouched: the hull was NOT converted into a
	// container claim (container_id stays empty), so coordinators remain locked out
	// after the op just as before it.
	require.True(t, hull.IsReservedByCaptain(), "the captain reservation must be preserved, not consumed by the op")
	require.Empty(t, hull.ContainerID(), "a captain-context op must not convert the reservation into a container claim")

	// Every pass-through MUST be audited: one WARNING line naming ship and op. The
	// audit line persists on Log's detached goroutine, so wait for it instead of
	// reading the sink once (sp-yon7).
	line := logs.waitForLog(t, "WARNING", "Captain-context override", 2*time.Second)
	require.Equal(t, "TORWIND-19", line.metadata["ship_symbol"])
	require.Equal(t, "NAVIGATE", line.metadata["op"])
}

// sp-sfoe — the pass-through must NOT leak. An automated claim on the legacy path
// (here a coordinator-type container with neither the captain-authority flag nor an
// operation key) is STILL rejected on a captain-reserved hull, emits no pass-through
// audit line, and leaves the reservation intact. This is the guarantee that the
// permit is load-bearing and scoped strictly to the captain CLI path — the whole
// point of reserve is to lock coordinators out.
func TestWithoutCaptainAuthorityCaptainReservationStillRejected(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)

	hull := newIdleTradeShip(t, "TORWIND-19", playerID)
	require.NoError(t, hull.ReserveByCaptain("captain manual survey", shared.NewRealClock()))
	shipRepo := &tradeRouteShipRepo{ships: map[string]*navigation.Ship{"TORWIND-19": hull}}
	logs := &capturingLogRepo{}

	// A coordinator-type container WITHOUT the captain flag (models any automated
	// legacy claim). The permit must not reach it.
	const containerID = "automated-TORWIND-19"
	entity := container.NewContainer(containerID, container.ContainerTypeTrading, playerID, 1, nil,
		map[string]interface{}{"ship_symbol": "TORWIND-19"}, nil)
	require.NoError(t, s.containerRepo.Add(context.Background(), entity, "some_coordinator"))

	runner := NewContainerRunner(entity, s.mediator, nil, logs, s.containerRepo, shipRepo, s.clock)

	err := runner.Start()

	require.Error(t, err, "without the captain-authority flag a captain-reserved hull must still be rejected")
	var resErr *shared.ShipReservedByCaptainError
	require.ErrorAs(t, err, &resErr, "must be the standing captain-reservation rejection")
	requireContainerState(t, db, containerID, "FAILED", "claim_failed")
	require.True(t, hull.IsReservedByCaptain(), "the captain reservation must be untouched by a rejected coordinator claim")
	require.Empty(t, hull.ContainerID())
	if _, passed := logs.find("WARNING", "Captain-context override"); passed {
		t.Fatalf("no pass-through may be logged without the captain-authority flag: %+v", logs.snapshot())
	}
}

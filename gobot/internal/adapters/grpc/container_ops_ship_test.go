package grpc

import (
	"context"
	"strings"
	"sync"
	"testing"

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
	line, ok := logs.find("WARNING", "Captain-authority override")
	require.True(t, ok, "every captain override must log one audit line; got: %+v", logs.entries)
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
		t.Fatalf("no override may be logged without the captain-authority flag: %+v", logs.entries)
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

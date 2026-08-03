package commands

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// drainSettled runs one drain tick and JOINS the supply workers it dispatched, folding their
// completions into the tick's report. The drain deliberately does not join its own workers — that is
// what keeps activation off a haul's cadence — so a test asserting on a delivered outcome (task
// status, pipeline progress, producer calls) must settle here first: the join is also the
// happens-before edge that makes the worker's writes safe to read.
//
// Tests that assert on work still IN FLIGHT drive drainOnce directly instead.
func drainSettled(t *testing.T, handler *RunConstructionCoordinatorHandler, ctx context.Context, cmd *RunConstructionCoordinatorCommand) (*RunConstructionCoordinatorResponse, error) {
	t.Helper()
	resp, err := handler.drainOnce(ctx, cmd)
	completed := handler.awaitSupplies(cmd.ContainerID)
	if resp != nil {
		resp.TasksDrained += completed
	}
	return resp, err
}

// Test helpers shared by the construction-coordinator tests, their sole users.
// Names are kept verbatim so the existing
// construction tests reference them unchanged.

const (
	testSystem          = "X1-TEST"
	testFactoryWaypoint = "X1-TEST-FACTORY"
)

type factoryFakeClock struct{}

func (c *factoryFakeClock) Now() time.Time        { return time.Now() }
func (c *factoryFakeClock) Sleep(d time.Duration) {}

func newTestHauler(t *testing.T, symbol string, inventory []*shared.CargoItem) *navigation.Ship {
	t.Helper()

	units := 0
	for _, item := range inventory {
		units += item.Units
	}
	cargo, err := shared.NewCargo(40, units, inventory)
	if err != nil {
		t.Fatalf("failed to build cargo: %v", err)
	}
	fuel, err := shared.NewFuel(100, 100)
	if err != nil {
		t.Fatalf("failed to build fuel: %v", err)
	}
	waypoint, err := shared.NewWaypoint(testFactoryWaypoint, 0, 0)
	if err != nil {
		t.Fatalf("failed to build waypoint: %v", err)
	}
	ship, err := navigation.NewShip(
		symbol,
		shared.MustNewPlayerID(1),
		waypoint,
		fuel,
		100,
		40,
		cargo,
		30,
		"FRAME_LIGHT_FREIGHTER",
		"HAULER",
		nil,
		navigation.NavStatusDocked,
	)
	if err != nil {
		t.Fatalf("failed to build ship: %v", err)
	}
	return ship
}

// newTestHaulerAt builds an idle HAULER at a chosen waypoint (the system is derived from the symbol
// exactly as production does), for tests that place a hull outside the factory's own system.
func newTestHaulerAt(t *testing.T, symbol, waypointSymbol string) *navigation.Ship {
	t.Helper()
	cargo, err := shared.NewCargo(40, 0, nil)
	if err != nil {
		t.Fatalf("failed to build cargo: %v", err)
	}
	fuel, err := shared.NewFuel(100, 100)
	if err != nil {
		t.Fatalf("failed to build fuel: %v", err)
	}
	waypoint, err := shared.NewWaypoint(waypointSymbol, 0, 0)
	if err != nil {
		t.Fatalf("failed to build waypoint: %v", err)
	}
	ship, err := navigation.NewShip(
		symbol, shared.MustNewPlayerID(1), waypoint, fuel, 100, 40, cargo, 30,
		"FRAME_LIGHT_FREIGHTER", "HAULER", nil, navigation.NavStatusDocked,
	)
	if err != nil {
		t.Fatalf("failed to build ship: %v", err)
	}
	return ship
}

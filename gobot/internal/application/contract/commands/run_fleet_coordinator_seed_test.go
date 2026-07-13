package commands

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipAssignment "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/assignment"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// These tests cover the restart-resurrection defect (sp-86vb): a hull deliberately
// removed from the contract fleet (`fleet remove` clears its live dedicated_fleet
// tag) must STAY removed across a daemon restart, even though the immutable
// --dedicated-ships launch seed still lists it. The seed is applied ONCE on genuine
// first boot; a persisted first-boot marker gates the replay on every later restart
// so the live tag stays authoritative (RULINGS #2).
//
// The harness is deliberately faithful: AssignShipFleetCommand is routed to the REAL
// assign handler over an in-memory ship store, so a "removal" and a "seed" mutate the
// same observable dedication tag the production write path mutates — the tests assert
// that tag, never a mock call.

// fakeSeedMarker is an in-memory DedicatedFleetSeedMarker. It records the seeded
// flag per container so a test can simulate persist-then-reload across a restart by
// reading isSeeded() back into the next boot's DedicatedShipsSeeded.
type fakeSeedMarker struct {
	seeded    map[string]bool
	markCount int
	markErr   error
}

func newFakeSeedMarker() *fakeSeedMarker {
	return &fakeSeedMarker{seeded: map[string]bool{}}
}

func (f *fakeSeedMarker) MarkDedicatedShipsSeeded(_ context.Context, containerID string, _ int) error {
	f.markCount++
	if f.markErr != nil {
		return f.markErr
	}
	f.seeded[containerID] = true
	return nil
}

func (f *fakeSeedMarker) isSeeded(containerID string) bool { return f.seeded[containerID] }

// inMemoryFleetRepo is a minimal ShipRepository holding real ships in a map, so
// AssignFleet mutates and FindBySymbol reflects the LIVE dedication tag — the
// source-of-truth store the fix must honor across a simulated restart. Every method
// the assign handler does not touch is left to the embedded nil interface (panics if
// unexpectedly called).
type inMemoryFleetRepo struct {
	navigation.ShipRepository
	ships map[string]*navigation.Ship
}

func (r *inMemoryFleetRepo) FindBySymbol(_ context.Context, symbol string, _ shared.PlayerID) (*navigation.Ship, error) {
	s, ok := r.ships[symbol]
	if !ok {
		return nil, fmt.Errorf("ship %s not found", symbol)
	}
	return s, nil
}

func (r *inMemoryFleetRepo) AssignFleet(_ context.Context, symbol string, fleet string, _ shared.PlayerID) error {
	s, ok := r.ships[symbol]
	if !ok {
		return fmt.Errorf("ship %s not found", symbol)
	}
	s.SetDedicatedFleet(fleet)
	return nil
}

func (r *inMemoryFleetRepo) FindAllByPlayer(_ context.Context, _ shared.PlayerID) ([]*navigation.Ship, error) {
	out := make([]*navigation.Ship, 0, len(r.ships))
	for _, s := range r.ships {
		out = append(out, s)
	}
	return out, nil
}

func (r *inMemoryFleetRepo) fleetTag(t *testing.T, symbol string) string {
	t.Helper()
	s, ok := r.ships[symbol]
	if !ok {
		t.Fatalf("ship %s not in repo", symbol)
	}
	return s.DedicatedFleet()
}

// realAssignMediator routes AssignShipFleetCommand to the REAL assign handler, so a
// seed and a removal both flow through the single production write path for the tag.
type realAssignMediator struct {
	handler *shipAssignment.AssignShipFleetHandler
}

func (m *realAssignMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	return m.handler.Handle(ctx, request)
}

func (m *realAssignMediator) Register(reflect.Type, common.RequestHandler) error { return nil }

func (m *realAssignMediator) RegisterMiddleware(common.Middleware) {}

// newHaulerShip builds a docked freighter with cargo capacity (a genuine contract
// hauler — clears the r6f1 cargo floor) and an optional starting dedication tag.
func newHaulerShip(t *testing.T, symbol string, currentFleet string) *navigation.Ship {
	t.Helper()
	cargo, err := shared.NewCargo(40, 0, nil)
	if err != nil {
		t.Fatalf("build cargo: %v", err)
	}
	fuel, err := shared.NewFuel(100, 100)
	if err != nil {
		t.Fatalf("build fuel: %v", err)
	}
	wp, err := shared.NewWaypoint("X1-TW-A2", 0, 0)
	if err != nil {
		t.Fatalf("build waypoint: %v", err)
	}
	ship, err := navigation.NewShip(
		symbol,
		shared.MustNewPlayerID(2),
		wp,
		fuel,
		100,
		40,
		cargo,
		30,
		"FRAME_FREIGHTER",
		"HAULER",
		nil,
		navigation.NavStatusDocked,
	)
	if err != nil {
		t.Fatalf("build ship: %v", err)
	}
	if currentFleet != "" {
		ship.SetDedicatedFleet(currentFleet)
	}
	return ship
}

// newSeedTestHarness wires the in-memory ship store to the real assign handler via a
// routing mediator, the production write path for the dedication tag.
func newSeedTestHarness(t *testing.T, ships ...*navigation.Ship) (*inMemoryFleetRepo, *realAssignMediator) {
	t.Helper()
	repo := &inMemoryFleetRepo{ships: map[string]*navigation.Ship{}}
	for _, s := range ships {
		repo.ships[s.ShipSymbol()] = s
	}
	med := &realAssignMediator{handler: shipAssignment.NewAssignShipFleetHandler(repo, nil)}
	return repo, med
}

// removeFromFleet models the operator's `fleet remove`: clear the live dedication tag
// through the single production write path (AssignShipFleetCommand with Fleet="").
func removeFromFleet(t *testing.T, med common.Mediator, symbol string, playerID int) {
	t.Helper()
	pid := playerID
	if _, err := med.Send(context.Background(), &shipAssignment.AssignShipFleetCommand{
		ShipSymbol: symbol,
		Fleet:      "",
		PlayerID:   &pid,
		Assigner:   "cli",
		Manual:     true,
	}); err != nil {
		t.Fatalf("live-remove %s: %v", symbol, err)
	}
}

// TestReconcile_LiveRemovedHull_NotResurrectedOnRestart is the primary defect: a hull
// in the launch seed whose live tag was cleared by `fleet remove` must NOT be
// re-tagged when the coordinator reboots. seeded=true models a restart whose first
// boot already consumed the seed (the persisted marker was reloaded), so the replay
// must be skipped and the removal must survive.
func TestReconcile_LiveRemovedHull_NotResurrectedOnRestart(t *testing.T) {
	repo, med := newSeedTestHarness(t, newHaulerShip(t, "TORWIND-7", "")) // already live-removed → tag ""
	marker := newFakeSeedMarker()
	logger := &completionCapturingLogger{}

	// Restart: the seed lists TORWIND-7, but the first boot already consumed the
	// seed (seeded=true, reloaded from the persisted marker).
	seedDedicatedFleetIfFirstBoot(context.Background(), logger, med, marker,
		shared.MustNewPlayerID(2), "cc-1", []string{"TORWIND-7"}, true, "contract",
		"contract-coordinator-reconcile:cc-1")

	if got := repo.fleetTag(t, "TORWIND-7"); got != "" {
		t.Fatalf("live-removed hull resurrected on restart: expected dedicated_fleet \"\", got %q", got)
	}
	if marker.markCount != 0 {
		t.Fatalf("an already-seeded restart must not re-persist the marker, got %d writes", marker.markCount)
	}
}

// TestReconcile_FirstBoot_StillSeeds proves the fix preserves genuine first-boot
// seeding: seeded=false with no prior state tags the seed hull into the contract
// fleet AND persists the first-boot marker so the next restart skips the replay.
func TestReconcile_FirstBoot_StillSeeds(t *testing.T) {
	repo, med := newSeedTestHarness(t, newHaulerShip(t, "TORWIND-7", "")) // fresh, untagged
	marker := newFakeSeedMarker()
	logger := &completionCapturingLogger{}

	seedDedicatedFleetIfFirstBoot(context.Background(), logger, med, marker,
		shared.MustNewPlayerID(2), "cc-1", []string{"TORWIND-7"}, false, "contract",
		"contract-coordinator-reconcile:cc-1")

	if got := repo.fleetTag(t, "TORWIND-7"); got != "contract" {
		t.Fatalf("first boot must seed the hull into the contract fleet, got dedicated_fleet %q", got)
	}
	if !marker.isSeeded("cc-1") {
		t.Fatalf("first boot must persist the seeded marker so a restart skips the replay")
	}
}

// TestReconcile_ConsumedRemoval_SurvivesRestart exercises the full persist→reload
// cycle across a simulated restart (RULINGS #2): first boot seeds two hulls and
// persists the marker; the operator removes one live; the restart reloads
// seeded=true from the persisted marker and skips the replay, so the removal sticks
// and the surviving hull keeps its tag.
func TestReconcile_ConsumedRemoval_SurvivesRestart(t *testing.T) {
	repo, med := newSeedTestHarness(t,
		newHaulerShip(t, "TORWIND-7", ""),
		newHaulerShip(t, "TORWIND-8", ""),
	)
	marker := newFakeSeedMarker()
	logger := &completionCapturingLogger{}
	seed := []string{"TORWIND-7", "TORWIND-8"}

	// First boot: seed applies (marker reads false), both hulls tagged, marker set.
	seedDedicatedFleetIfFirstBoot(context.Background(), logger, med, marker,
		shared.MustNewPlayerID(2), "cc-1", seed, marker.isSeeded("cc-1"), "contract",
		"contract-coordinator-reconcile:cc-1")
	if repo.fleetTag(t, "TORWIND-7") != "contract" || repo.fleetTag(t, "TORWIND-8") != "contract" {
		t.Fatalf("first boot must seed both hulls, got %q / %q",
			repo.fleetTag(t, "TORWIND-7"), repo.fleetTag(t, "TORWIND-8"))
	}

	// Operator removes TORWIND-8 from the contract fleet while the coordinator runs.
	removeFromFleet(t, med, "TORWIND-8", 2)

	// Restart: reload the persisted marker (seeded=true) — the seed must NOT replay.
	seedDedicatedFleetIfFirstBoot(context.Background(), logger, med, marker,
		shared.MustNewPlayerID(2), "cc-1", seed, marker.isSeeded("cc-1"), "contract",
		"contract-coordinator-reconcile:cc-1")

	if got := repo.fleetTag(t, "TORWIND-8"); got != "" {
		t.Fatalf("consumed removal did not survive the restart: TORWIND-8 dedicated_fleet %q (want \"\")", got)
	}
	if got := repo.fleetTag(t, "TORWIND-7"); got != "contract" {
		t.Fatalf("a still-dedicated hull must keep its live tag across the restart, got %q", got)
	}
}

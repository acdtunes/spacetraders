package assignment

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// assignStubShipRepo embeds the domain interface so only the two methods the
// handler touches (FindBySymbol for the eligibility read, AssignFleet for the
// write) need concrete implementations; any unexpected call panics on a
// nil-method deref.
type assignStubShipRepo struct {
	navigation.ShipRepository

	// FindBySymbol behavior (sp-r6f1: the handler loads the hull to read its
	// frame + cargo for the eligibility gate and the assigner-named audit line).
	ship    *navigation.Ship
	findErr error

	// AssignFleet behavior + capture.
	assignErr      error
	assignedSymbol string
	assignedFleet  string
	assignedPlayer shared.PlayerID
	assignCalled   int
}

func (s *assignStubShipRepo) FindBySymbol(_ context.Context, _ string, _ shared.PlayerID) (*navigation.Ship, error) {
	if s.findErr != nil {
		return nil, s.findErr
	}
	return s.ship, nil
}

func (s *assignStubShipRepo) AssignFleet(_ context.Context, shipSymbol string, fleet string, playerID shared.PlayerID) error {
	s.assignCalled++
	s.assignedSymbol = shipSymbol
	s.assignedFleet = fleet
	s.assignedPlayer = playerID
	return s.assignErr
}

// newFleetTestShip builds a docked hull with a given frame, cargo capacity and
// (optionally) an existing dedication, the minimum surface the assign handler
// inspects when gating a fleet write (sp-r6f1). A 0-cargo hull models the
// mispinned satellite (TORWIND-24/25); a positive-cargo hull models a hauler.
func newFleetTestShip(t *testing.T, symbol, frame, role string, cargoCap int, currentFleet string) *navigation.Ship {
	t.Helper()
	cargo, err := shared.NewCargo(cargoCap, 0, nil)
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
		cargoCap,
		cargo,
		30,
		frame,
		role,
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

// logLine captures one emitted log entry for assertions.
type logLine struct {
	level    string
	message  string
	metadata map[string]interface{}
}

// captureLogger records every Log call so a test can assert the assigner-named
// audit line (sp-r6f1 finding #3) and the block/warn lines actually fire.
type captureLogger struct {
	lines []logLine
}

func (c *captureLogger) Log(level, message string, metadata map[string]interface{}) {
	c.lines = append(c.lines, logLine{level: level, message: message, metadata: metadata})
}

func (c *captureLogger) find(level, substr string) (logLine, bool) {
	for _, l := range c.lines {
		if l.level == level && strings.Contains(l.message, substr) {
			return l, true
		}
	}
	return logLine{}, false
}

// The single write path for the dedication tag (sp-l7h2): the command must
// deliver exactly the symbol, fleet name and resolved player to the
// repository, and echo the persisted state back to the caller.
func TestAssignShipFleet_AssignsFleetAndEchoesPersistedState(t *testing.T) {
	repo := &assignStubShipRepo{ship: newFleetTestShip(t, "TORWIND-19", "FRAME_FREIGHTER", "HAULER", 40, "")}
	handler := NewAssignShipFleetHandler(repo, nil)

	pid := 7
	resp, err := handler.Handle(context.Background(), &AssignShipFleetCommand{
		ShipSymbol: "TORWIND-19",
		Fleet:      "bulk_circuit",
		PlayerID:   &pid,
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	assignResp, ok := resp.(*AssignShipFleetResponse)
	if !ok {
		t.Fatalf("unexpected response type: %T", resp)
	}
	if repo.assignCalled != 1 {
		t.Fatalf("expected exactly one AssignFleet call, got %d", repo.assignCalled)
	}
	if repo.assignedSymbol != "TORWIND-19" {
		t.Fatalf("expected TORWIND-19 assigned, got %q", repo.assignedSymbol)
	}
	if repo.assignedFleet != "bulk_circuit" {
		t.Fatalf("expected fleet bulk_circuit, got %q", repo.assignedFleet)
	}
	if repo.assignedPlayer.Value() != 7 {
		t.Fatalf("expected player 7, got %d", repo.assignedPlayer.Value())
	}
	if assignResp.ShipSymbol != "TORWIND-19" || assignResp.Fleet != "bulk_circuit" {
		t.Fatalf("expected response to echo TORWIND-19/bulk_circuit, got %q/%q", assignResp.ShipSymbol, assignResp.Fleet)
	}
}

// Fleet == "" is the unassign path (the CLI's `fleet unassign` sends exactly
// this): it must reach the repository as an empty string — clearing the tag —
// not be rejected as a missing argument.
func TestAssignShipFleet_EmptyFleetClearsDedication(t *testing.T) {
	repo := &assignStubShipRepo{ship: newFleetTestShip(t, "TORWIND-19", "FRAME_PROBE", "SATELLITE", 0, "contract")}
	handler := NewAssignShipFleetHandler(repo, nil)

	pid := 1
	resp, err := handler.Handle(context.Background(), &AssignShipFleetCommand{
		ShipSymbol: "TORWIND-19",
		Fleet:      "",
		PlayerID:   &pid,
	})
	if err != nil {
		t.Fatalf("expected clearing a dedication to succeed, got: %v", err)
	}

	if repo.assignCalled != 1 {
		t.Fatalf("expected exactly one AssignFleet call, got %d", repo.assignCalled)
	}
	if repo.assignedFleet != "" {
		t.Fatalf("expected empty fleet to reach the repository (clear), got %q", repo.assignedFleet)
	}
	if resp.(*AssignShipFleetResponse).Fleet != "" {
		t.Fatalf("expected response fleet to be empty after clearing, got %q", resp.(*AssignShipFleetResponse).Fleet)
	}
}

// Guard against a silently-empty dedication: no symbol means nothing to
// assign, and the repository must never be called.
func TestAssignShipFleet_RequiresShipSymbol(t *testing.T) {
	repo := &assignStubShipRepo{}
	handler := NewAssignShipFleetHandler(repo, nil)

	pid := 1
	_, err := handler.Handle(context.Background(), &AssignShipFleetCommand{
		Fleet:    "contract",
		PlayerID: &pid,
	})
	if err == nil {
		t.Fatalf("expected an error for missing ship_symbol")
	}
	if repo.assignCalled != 0 {
		t.Fatalf("expected no assignment attempt without a ship symbol, got %d", repo.assignCalled)
	}
}

// A repository failure (unknown ship, dead DB) must surface to the caller
// wrapped with command context — the CLI and the coordinator's reconcile
// WARNING both print this error verbatim.
func TestAssignShipFleet_RepositoryErrorIsWrappedAndPropagated(t *testing.T) {
	repo := &assignStubShipRepo{
		ship:      newFleetTestShip(t, "TORWIND-GONE", "FRAME_FREIGHTER", "HAULER", 40, ""),
		assignErr: fmt.Errorf("ship TORWIND-GONE not found for player 1"),
	}
	handler := NewAssignShipFleetHandler(repo, nil)

	pid := 1
	_, err := handler.Handle(context.Background(), &AssignShipFleetCommand{
		ShipSymbol: "TORWIND-GONE",
		Fleet:      "contract",
		PlayerID:   &pid,
	})
	if err == nil {
		t.Fatalf("expected the repository error to propagate")
	}
	if !strings.Contains(err.Error(), "failed to assign ship fleet") {
		t.Fatalf("expected command-context wrapping, got: %v", err)
	}
	if !strings.Contains(err.Error(), "TORWIND-GONE not found") {
		t.Fatalf("expected the underlying cause preserved, got: %v", err)
	}
}

// sp-r6f1 THE FIX — auto path. An automated assigner (the contract coordinator's
// reconcile) trying to pin a 0-cargo satellite into the "contract" hauling fleet
// is BLOCKED: no write reaches the repository, and the error names the ineligible
// hull. This is what stops the reconcile from re-pinning TORWIND-24/25 after the
// harbormaster unpins them.
func TestAssignShipFleet_AutoAssignBlocksZeroCargoIntoContractFleet(t *testing.T) {
	log := &captureLogger{}
	ctx := common.WithLogger(context.Background(), log)
	repo := &assignStubShipRepo{ship: newFleetTestShip(t, "TORWIND-25", "FRAME_PROBE", "SATELLITE", 0, "")}
	handler := NewAssignShipFleetHandler(repo, nil)

	pid := 2
	_, err := handler.Handle(ctx, &AssignShipFleetCommand{
		ShipSymbol: "TORWIND-25",
		Fleet:      "contract",
		PlayerID:   &pid,
		Assigner:   "contract-coordinator-reconcile:9dfedc12",
		Manual:     false,
	})
	if err == nil {
		t.Fatalf("expected an auto-assign of a 0-cargo hull into contract to be blocked")
	}
	if repo.assignCalled != 0 {
		t.Fatalf("a blocked auto-assign must never write to the repository, got %d writes", repo.assignCalled)
	}
	if !strings.Contains(err.Error(), "TORWIND-25") {
		t.Fatalf("expected the block error to name the hull, got: %v", err)
	}
	line, ok := log.find("ERROR", "BLOCKED")
	if !ok {
		t.Fatalf("expected a loud ERROR block line, got lines: %+v", log.lines)
	}
	if !strings.Contains(line.message, "contract-coordinator-reconcile:9dfedc12") {
		t.Fatalf("expected the block line to name the assigner, got: %q", line.message)
	}
}

// sp-r6f1 regression — a genuine hauler is still auto-assigned into the contract
// fleet exactly as before: the eligibility floor excludes only 0-cargo hulls.
func TestAssignShipFleet_AutoAssignAllowsHaulerIntoContractFleet(t *testing.T) {
	repo := &assignStubShipRepo{ship: newFleetTestShip(t, "TORWIND-8", "FRAME_FREIGHTER", "HAULER", 40, "")}
	handler := NewAssignShipFleetHandler(repo, nil)

	pid := 2
	_, err := handler.Handle(context.Background(), &AssignShipFleetCommand{
		ShipSymbol: "TORWIND-8",
		Fleet:      "contract",
		PlayerID:   &pid,
		Assigner:   "contract-coordinator-reconcile:9dfedc12",
		Manual:     false,
	})
	if err != nil {
		t.Fatalf("expected a hauler to be assignable into contract, got: %v", err)
	}
	if repo.assignCalled != 1 {
		t.Fatalf("expected the hauler to be written once, got %d", repo.assignCalled)
	}
	if repo.assignedFleet != "contract" {
		t.Fatalf("expected fleet contract, got %q", repo.assignedFleet)
	}
}

// sp-r6f1 THE FIX — manual path. The captain may deliberately pin anything: a
// manual (CLI) assign of a 0-cargo hull into contract WARNS loudly but is NOT
// blocked — the write still reaches the repository (the selection side already
// refuses to dispatch it, so it is dead weight, not a crash).
func TestAssignShipFleet_ManualAssignWarnsButAllowsZeroCargoIntoContractFleet(t *testing.T) {
	log := &captureLogger{}
	ctx := common.WithLogger(context.Background(), log)
	repo := &assignStubShipRepo{ship: newFleetTestShip(t, "TORWIND-25", "FRAME_PROBE", "SATELLITE", 0, "")}
	handler := NewAssignShipFleetHandler(repo, nil)

	pid := 2
	_, err := handler.Handle(ctx, &AssignShipFleetCommand{
		ShipSymbol: "TORWIND-25",
		Fleet:      "contract",
		PlayerID:   &pid,
		Assigner:   "cli",
		Manual:     true,
	})
	if err != nil {
		t.Fatalf("expected a manual assign to proceed on operator authority, got: %v", err)
	}
	if repo.assignCalled != 1 {
		t.Fatalf("expected the manual assign to still write, got %d", repo.assignCalled)
	}
	line, ok := log.find("WARNING", "TORWIND-25")
	if !ok {
		t.Fatalf("expected a loud WARNING on the manual ineligible pin, got lines: %+v", log.lines)
	}
	if !strings.Contains(line.message, "cli") {
		t.Fatalf("expected the warn line to name the assigner, got: %q", line.message)
	}
}

// sp-r6f1 scoping — a 0-cargo satellite is perfectly valid in a fleet with no
// cargo floor (scouts/tours legitimately fly 0-cargo hulls). Only cargo-required
// hauling fleets gate on capacity, so a scout pin is never blocked or warned.
func TestAssignShipFleet_AutoAssignAllowsZeroCargoIntoNonCargoFleet(t *testing.T) {
	log := &captureLogger{}
	ctx := common.WithLogger(context.Background(), log)
	repo := &assignStubShipRepo{ship: newFleetTestShip(t, "TORWIND-25", "FRAME_PROBE", "SATELLITE", 0, "")}
	handler := NewAssignShipFleetHandler(repo, nil)

	pid := 2
	_, err := handler.Handle(ctx, &AssignShipFleetCommand{
		ShipSymbol: "TORWIND-25",
		Fleet:      "scout",
		PlayerID:   &pid,
		Assigner:   "cli",
		Manual:     false,
	})
	if err != nil {
		t.Fatalf("expected a 0-cargo hull to be assignable into a non-cargo fleet, got: %v", err)
	}
	if repo.assignCalled != 1 {
		t.Fatalf("expected the scout pin to be written, got %d", repo.assignCalled)
	}
	if _, blocked := log.find("ERROR", "BLOCKED"); blocked {
		t.Fatalf("a non-cargo fleet pin must never emit a block line: %+v", log.lines)
	}
	if _, warned := log.find("WARNING", "cannot haul"); warned {
		t.Fatalf("a non-cargo fleet pin must never emit an ineligibility warn: %+v", log.lines)
	}
}

// sp-r6f1 deployment safety — a hull ALREADY carrying the contract tag (the live
// state of TORWIND-24/25 today) is an idempotent no-op re-touch, not a new write:
// the reconcile must not error-spam on every restart while the mispin persists.
// The gate fires only on a real change (an actual re-pin after an unpin).
func TestAssignShipFleet_AutoAssignNoOpOnAlreadyPinnedZeroCargoIsNotBlocked(t *testing.T) {
	repo := &assignStubShipRepo{ship: newFleetTestShip(t, "TORWIND-25", "FRAME_PROBE", "SATELLITE", 0, "contract")}
	handler := NewAssignShipFleetHandler(repo, nil)

	pid := 2
	_, err := handler.Handle(context.Background(), &AssignShipFleetCommand{
		ShipSymbol: "TORWIND-25",
		Fleet:      "contract",
		PlayerID:   &pid,
		Assigner:   "contract-coordinator-reconcile:9dfedc12",
		Manual:     false,
	})
	if err != nil {
		t.Fatalf("an idempotent no-op re-touch of an already-pinned hull must not error, got: %v", err)
	}
	if repo.assignCalled != 1 {
		t.Fatalf("expected the idempotent AssignFleet call to still occur, got %d", repo.assignCalled)
	}
}

// sp-r6f1 finding #3 — every actual dedication write emits one assigner-named
// audit line carrying the hull's frame and cargo, so the next mispin names its
// culprit in a single grep. (The daemon.log at incident time had NO such line.)
func TestAssignShipFleet_WriteEmitsAssignerNamedAuditLine(t *testing.T) {
	log := &captureLogger{}
	ctx := common.WithLogger(context.Background(), log)
	repo := &assignStubShipRepo{ship: newFleetTestShip(t, "TORWIND-8", "FRAME_FREIGHTER", "HAULER", 40, "")}
	handler := NewAssignShipFleetHandler(repo, nil)

	pid := 2
	_, err := handler.Handle(ctx, &AssignShipFleetCommand{
		ShipSymbol: "TORWIND-8",
		Fleet:      "contract",
		PlayerID:   &pid,
		Assigner:   "contract-coordinator-reconcile:9dfedc12",
		Manual:     false,
	})
	if err != nil {
		t.Fatalf("expected the write to succeed, got: %v", err)
	}
	line, ok := log.find("INFO", "TORWIND-8")
	if !ok {
		t.Fatalf("expected an INFO audit line for the write, got lines: %+v", log.lines)
	}
	if !strings.Contains(line.message, "contract-coordinator-reconcile:9dfedc12") {
		t.Fatalf("audit line must name the assigner, got: %q", line.message)
	}
	// Frame + cargo must be present so the culprit's hull class is grep-able.
	if line.metadata["frame"] != "FRAME_FREIGHTER" {
		t.Fatalf("audit line must carry the frame, got metadata: %+v", line.metadata)
	}
	if line.metadata["cargo_capacity"] != 40 {
		t.Fatalf("audit line must carry cargo capacity, got metadata: %+v", line.metadata)
	}
}

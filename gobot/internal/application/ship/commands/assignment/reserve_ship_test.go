package assignment

import (
	"context"
	"errors"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// reserveStubShipRepo embeds the domain interface so only the methods the
// handler exercises need concrete implementations; any unexpected call panics
// on a nil-method deref, surfacing accidental cache reads.
type reserveStubShipRepo struct {
	navigation.ShipRepository

	reserveErr     error
	reservedSymbol string
	reservedReason string
	reserveCalled  int

	// PreemptForCaptain behavior + capture (sp-w3yd: the --force path). preemptedFrom
	// is the container id the atomic swap reports it revoked the claim from ("" when
	// the hull was idle).
	preemptErr      error
	preemptedSymbol string
	preemptedReason string
	preemptedFrom   string
	preemptCalled   int

	shipToReturn *navigation.Ship
	findErr      error

	idleShips []*navigation.Ship
	idleErr   error
}

func (s *reserveStubShipRepo) ReserveForCaptain(_ context.Context, shipSymbol string, reason string, _ shared.PlayerID) error {
	s.reserveCalled++
	s.reservedSymbol = shipSymbol
	s.reservedReason = reason
	return s.reserveErr
}

func (s *reserveStubShipRepo) PreemptForCaptain(_ context.Context, shipSymbol string, reason string, _ shared.PlayerID) (string, error) {
	s.preemptCalled++
	s.preemptedSymbol = shipSymbol
	s.preemptedReason = reason
	return s.preemptedFrom, s.preemptErr
}

func (s *reserveStubShipRepo) FindBySymbol(_ context.Context, _ string, _ shared.PlayerID) (*navigation.Ship, error) {
	return s.shipToReturn, s.findErr
}

func (s *reserveStubShipRepo) FindIdleByPlayer(_ context.Context, _ shared.PlayerID) ([]*navigation.Ship, error) {
	return s.idleShips, s.idleErr
}

// newReserveTestShip builds a minimal idle ship of the given role, used both
// as the "just reserved" ship and as entries in the remaining-idle-fleet list.
func newReserveTestShip(t *testing.T, symbol, role string) *navigation.Ship {
	t.Helper()
	location, _ := shared.NewWaypoint("X1-AU21-K82", 0, 0)
	fuel, err := shared.NewFuel(0, 0)
	if err != nil {
		t.Fatalf("NewFuel: %v", err)
	}
	cargo, err := shared.NewCargo(40, 0, nil)
	if err != nil {
		t.Fatalf("NewCargo: %v", err)
	}
	ship, err := navigation.NewShip(
		symbol,
		shared.MustNewPlayerID(1),
		location,
		fuel,
		0,
		40,
		cargo,
		9,
		"FRAME_HAULER",
		role,
		nil,
		navigation.NavStatusInOrbit,
	)
	if err != nil {
		t.Fatalf("NewShip: %v", err)
	}
	return ship
}

// A plain reserve with sibling idle capacity of the same role remaining: no
// idle-critical warning, and the reservation itself must reach the repository
// with the exact symbol/reason/player the caller supplied.
func TestReserveShip_ReservesShipAndReturnsSuccess(t *testing.T) {
	repo := &reserveStubShipRepo{
		shipToReturn: newReserveTestShip(t, "TORWIND-7", "HAULER"),
		idleShips:    []*navigation.Ship{newReserveTestShip(t, "TORWIND-8", "HAULER")}, // another hauler remains idle
	}
	handler := NewReserveShipHandler(repo, nil)

	pid := 1
	resp, err := handler.Handle(context.Background(), &ReserveShipCommand{
		ShipSymbol: "TORWIND-7",
		Reason:     "manual gate-supply errand",
		PlayerID:   &pid,
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	reserveResp, ok := resp.(*ReserveShipResponse)
	if !ok {
		t.Fatalf("unexpected response type: %T", resp)
	}
	if repo.reserveCalled != 1 {
		t.Fatalf("expected exactly one ReserveForCaptain call, got %d", repo.reserveCalled)
	}
	if repo.preemptCalled != 0 {
		t.Fatalf("a plain (non-force) reserve must never preempt, got %d PreemptForCaptain calls", repo.preemptCalled)
	}
	if repo.reservedSymbol != "TORWIND-7" {
		t.Fatalf("expected TORWIND-7 reserved, got %q", repo.reservedSymbol)
	}
	if repo.reservedReason != "manual gate-supply errand" {
		t.Fatalf("expected reason passed through, got %q", repo.reservedReason)
	}
	if reserveResp.ShipSymbol != "TORWIND-7" {
		t.Fatalf("expected response ship symbol TORWIND-7, got %q", reserveResp.ShipSymbol)
	}
	if reserveResp.Warning != "" {
		t.Fatalf("expected no idle-critical warning when another hauler remains idle, got %q", reserveResp.Warning)
	}
}

// The motivating acceptance criterion: reserving the last idle ship of a role
// (e.g. the last hauler) must still succeed but surface a soft warning.
func TestReserveShip_WarnsWhenReservingLastIdleShipOfItsRole(t *testing.T) {
	repo := &reserveStubShipRepo{
		shipToReturn: newReserveTestShip(t, "TORWIND-7", "HAULER"),
		idleShips:    []*navigation.Ship{newReserveTestShip(t, "TORWIND-9", "EXCAVATOR")}, // no other HAULER idle
	}
	handler := NewReserveShipHandler(repo, nil)

	pid := 1
	resp, err := handler.Handle(context.Background(), &ReserveShipCommand{
		ShipSymbol: "TORWIND-7",
		PlayerID:   &pid,
	})
	if err != nil {
		t.Fatalf("expected no error (a soft warning must not block reservation), got: %v", err)
	}

	reserveResp := resp.(*ReserveShipResponse)
	if reserveResp.Warning == "" {
		t.Fatalf("expected an idle-critical warning: TORWIND-7 was the last idle HAULER")
	}
}

// A ship already claimed by a container must not be silently stolen by a
// captain reservation — the atomic repository rejects it, and the handler
// must propagate that error (not swallow or reword it) so the CLI can report
// exactly why the reservation failed.
func TestReserveShip_RejectsWhenShipHeldByContainer(t *testing.T) {
	repo := &reserveStubShipRepo{
		reserveErr: shared.NewShipAlreadyAssignedError("TORWIND-7", "mfg-coordinator-abc123"),
	}
	handler := NewReserveShipHandler(repo, nil)

	pid := 1
	_, err := handler.Handle(context.Background(), &ReserveShipCommand{
		ShipSymbol: "TORWIND-7",
		PlayerID:   &pid,
	})
	if err == nil {
		t.Fatalf("expected an error when the ship is already claimed by a container")
	}
	var alreadyAssigned *shared.ShipAlreadyAssignedError
	if !errors.As(err, &alreadyAssigned) {
		t.Fatalf("expected ShipAlreadyAssignedError, got: %T %v", err, err)
	}
}

// The sp-w3yd fix: `ship reserve --force` (Force=true) must route through the
// atomic PREEMPT path, revoking a coordinator's live claim, NOT the plain
// reserve path that rejects a claimed hull. The handler must surface the
// preemption (Preempted + the revoked container) so the operator is told what
// was taken back — and must NOT touch the non-force ReserveForCaptain path.
func TestReserveShip_ForcePreemptsContainerClaim(t *testing.T) {
	repo := &reserveStubShipRepo{
		shipToReturn:  newReserveTestShip(t, "TORWIND-8", "HAULER"),
		idleShips:     []*navigation.Ship{newReserveTestShip(t, "TORWIND-7", "HAULER")},
		preemptedFrom: "goods_factory-FAB_MATS-a6984433",
	}
	handler := NewReserveShipHandler(repo, nil)

	pid := 1
	resp, err := handler.Handle(context.Background(), &ReserveShipCommand{
		ShipSymbol: "TORWIND-8",
		Reason:     "reclaim stranded FAB_MATS",
		Force:      true,
		PlayerID:   &pid,
	})
	if err != nil {
		t.Fatalf("expected no error on a force preempt, got: %v", err)
	}

	if repo.preemptCalled != 1 {
		t.Fatalf("expected exactly one PreemptForCaptain call on --force, got %d", repo.preemptCalled)
	}
	if repo.reserveCalled != 0 {
		t.Fatalf("--force must not fall through to the plain reserve path, got %d ReserveForCaptain calls", repo.reserveCalled)
	}
	if repo.preemptedSymbol != "TORWIND-8" || repo.preemptedReason != "reclaim stranded FAB_MATS" {
		t.Fatalf("preempt must receive the exact symbol/reason, got %q / %q", repo.preemptedSymbol, repo.preemptedReason)
	}

	reserveResp, ok := resp.(*ReserveShipResponse)
	if !ok {
		t.Fatalf("unexpected response type: %T", resp)
	}
	if !reserveResp.Preempted {
		t.Fatalf("expected Preempted=true when a live container claim was revoked")
	}
	if reserveResp.PreemptedFrom != "goods_factory-FAB_MATS-a6984433" {
		t.Fatalf("expected the revoked container reported, got %q", reserveResp.PreemptedFrom)
	}
}

// A force reserve of an IDLE hull is a plain reservation, not a preemption: the
// repository reports no revoked container, so Preempted must be false (the CLI
// then prints the ordinary "reserved" message, not "preempted from ...").
func TestReserveShip_ForceOnIdleHullReportsNoPreemption(t *testing.T) {
	repo := &reserveStubShipRepo{
		shipToReturn:  newReserveTestShip(t, "TORWIND-8", "HAULER"),
		idleShips:     []*navigation.Ship{newReserveTestShip(t, "TORWIND-7", "HAULER")},
		preemptedFrom: "", // idle hull: nothing to preempt
	}
	handler := NewReserveShipHandler(repo, nil)

	pid := 1
	resp, err := handler.Handle(context.Background(), &ReserveShipCommand{
		ShipSymbol: "TORWIND-8",
		Force:      true,
		PlayerID:   &pid,
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	reserveResp := resp.(*ReserveShipResponse)
	if reserveResp.Preempted {
		t.Fatalf("expected Preempted=false for a force reserve of an idle hull")
	}
	if reserveResp.PreemptedFrom != "" {
		t.Fatalf("expected no revoked container for an idle hull, got %q", reserveResp.PreemptedFrom)
	}
}

// Guard against a silently-empty reservation: no symbol means nothing to
// reserve, and the repository must never be called.
func TestReserveShip_RequiresShipSymbol(t *testing.T) {
	repo := &reserveStubShipRepo{}
	handler := NewReserveShipHandler(repo, nil)

	pid := 1
	_, err := handler.Handle(context.Background(), &ReserveShipCommand{
		PlayerID: &pid,
	})
	if err == nil {
		t.Fatalf("expected an error for missing ship_symbol")
	}
	if repo.reserveCalled != 0 {
		t.Fatalf("expected no reservation attempt without a ship symbol, got %d", repo.reserveCalled)
	}
}

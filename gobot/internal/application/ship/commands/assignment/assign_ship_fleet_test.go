package assignment

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// assignStubShipRepo embeds the domain interface so only AssignFleet needs a
// concrete implementation; any unexpected call panics on a nil-method deref.
type assignStubShipRepo struct {
	navigation.ShipRepository

	assignErr      error
	assignedSymbol string
	assignedFleet  string
	assignedPlayer shared.PlayerID
	assignCalled   int
}

func (s *assignStubShipRepo) AssignFleet(_ context.Context, shipSymbol string, fleet string, playerID shared.PlayerID) error {
	s.assignCalled++
	s.assignedSymbol = shipSymbol
	s.assignedFleet = fleet
	s.assignedPlayer = playerID
	return s.assignErr
}

// The single write path for the dedication tag (sp-l7h2): the command must
// deliver exactly the symbol, fleet name and resolved player to the
// repository, and echo the persisted state back to the caller.
func TestAssignShipFleet_AssignsFleetAndEchoesPersistedState(t *testing.T) {
	repo := &assignStubShipRepo{}
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
	repo := &assignStubShipRepo{}
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

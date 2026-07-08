package assignment

import (
	"context"
	"errors"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// releaseStubShipRepo embeds the domain interface so only the methods the
// handler exercises need concrete implementations; any unexpected call panics
// on a nil-method deref, surfacing accidental cache reads.
type releaseStubShipRepo struct {
	navigation.ShipRepository

	releaseErr     error
	releasedSymbol string
	releasedReason string
	releaseCalled  int
}

func (s *releaseStubShipRepo) ReleaseCaptainReservation(_ context.Context, shipSymbol string, reason string, _ shared.PlayerID) error {
	s.releaseCalled++
	s.releasedSymbol = shipSymbol
	s.releasedReason = reason
	return s.releaseErr
}

// The happy path: releasing a captain-reserved hull clears it via the atomic
// repository method and confirms with the ship symbol.
func TestReleaseShip_ReleasesReservationAndReturnsSuccess(t *testing.T) {
	repo := &releaseStubShipRepo{}
	handler := NewReleaseShipHandler(repo, nil)

	pid := 1
	resp, err := handler.Handle(context.Background(), &ReleaseShipCommand{
		ShipSymbol: "TORWIND-7",
		PlayerID:   &pid,
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	releaseResp, ok := resp.(*ReleaseShipResponse)
	if !ok {
		t.Fatalf("unexpected response type: %T", resp)
	}
	if repo.releaseCalled != 1 {
		t.Fatalf("expected exactly one ReleaseCaptainReservation call, got %d", repo.releaseCalled)
	}
	if repo.releasedSymbol != "TORWIND-7" {
		t.Fatalf("expected TORWIND-7 released, got %q", repo.releasedSymbol)
	}
	if releaseResp.ShipSymbol != "TORWIND-7" {
		t.Fatalf("expected response ship symbol TORWIND-7, got %q", releaseResp.ShipSymbol)
	}
}

// Release is audit-trailed even when the caller gives no reason: the handler
// must still record something non-empty rather than persisting a blank field.
func TestReleaseShip_DefaultsReasonWhenNotProvided(t *testing.T) {
	repo := &releaseStubShipRepo{}
	handler := NewReleaseShipHandler(repo, nil)

	pid := 1
	_, err := handler.Handle(context.Background(), &ReleaseShipCommand{
		ShipSymbol: "TORWIND-7",
		PlayerID:   &pid,
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if repo.releasedReason == "" {
		t.Fatalf("expected a non-empty default release reason to be recorded in the audit trail")
	}
}

// Releasing a hull that isn't captain-reserved (never reserved, or already
// released) must fail with the typed error, not silently succeed.
func TestReleaseShip_RejectsWhenNotReserved(t *testing.T) {
	repo := &releaseStubShipRepo{
		releaseErr: shared.NewShipNotReservedError("TORWIND-7"),
	}
	handler := NewReleaseShipHandler(repo, nil)

	pid := 1
	_, err := handler.Handle(context.Background(), &ReleaseShipCommand{
		ShipSymbol: "TORWIND-7",
		PlayerID:   &pid,
	})
	if err == nil {
		t.Fatalf("expected an error when the ship is not reserved")
	}
	var notReserved *shared.ShipNotReservedError
	if !errors.As(err, &notReserved) {
		t.Fatalf("expected ShipNotReservedError, got: %T %v", err, err)
	}
}

// Guard against a silently-empty release: no symbol means nothing to
// release, and the repository must never be called.
func TestReleaseShip_RequiresShipSymbol(t *testing.T) {
	repo := &releaseStubShipRepo{}
	handler := NewReleaseShipHandler(repo, nil)

	pid := 1
	_, err := handler.Handle(context.Background(), &ReleaseShipCommand{
		PlayerID: &pid,
	})
	if err == nil {
		t.Fatalf("expected an error for missing ship_symbol")
	}
	if repo.releaseCalled != 0 {
		t.Fatalf("expected no release attempt without a ship symbol, got %d", repo.releaseCalled)
	}
}

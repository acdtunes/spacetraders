package queries

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// refreshStubShipRepo embeds the domain interface so only the methods the
// handler exercises need concrete implementations; any unexpected call panics
// on a nil-method deref, surfacing accidental cache reads.
type refreshStubShipRepo struct {
	navigation.ShipRepository

	syncedShip      *navigation.Ship
	syncCalledCount int
	findCalledCount int
	savedShips      []*navigation.Ship
}

func (s *refreshStubShipRepo) SyncShipFromAPI(_ context.Context, _ string, _ shared.PlayerID) (*navigation.Ship, error) {
	s.syncCalledCount++
	return s.syncedShip, nil
}

func (s *refreshStubShipRepo) FindBySymbol(_ context.Context, _ string, _ shared.PlayerID) (*navigation.Ship, error) {
	s.findCalledCount++
	return nil, nil
}

// Save captures the freed hull so reconciliation tests can assert on the
// persisted, released aggregate.
func (s *refreshStubShipRepo) Save(_ context.Context, ship *navigation.Ship) error {
	s.savedShips = append(s.savedShips, ship)
	return nil
}

// stubContainerStatusReader reports a fixed container status so the handler's
// orphaned-claim predicate can be exercised for each owner state.
type stubContainerStatusReader struct {
	status         string
	found          bool
	err            error
	callCount      int
	askedContainer string
}

func (r *stubContainerStatusReader) ContainerStatus(_ context.Context, containerID string, _ shared.PlayerID) (string, bool, error) {
	r.callCount++
	r.askedContainer = containerID
	return r.status, r.found, r.err
}

// newAssignedRefreshTestShip builds a cargo-laden ship already claimed by a
// container, mirroring TORWIND-3's deadlocked state (18 units aboard, claimed by
// a trade-route container) in sp-vjwb.
func newAssignedRefreshTestShip(t *testing.T, symbol, containerID string) *navigation.Ship {
	t.Helper()
	location, _ := shared.NewWaypoint("X1-AU21-K82", 0, 0)
	ship := newRefreshTestShip(t, symbol, location, 18)
	if err := ship.AssignToContainer(containerID, shared.NewRealClock()); err != nil {
		t.Fatalf("AssignToContainer: %v", err)
	}
	return ship
}

// newCaptainReservedRefreshTestShip builds a ship reserved by the captain
// directly (sp-i1ku) rather than claimed by any container.
func newCaptainReservedRefreshTestShip(t *testing.T, symbol, reason string) *navigation.Ship {
	t.Helper()
	location, _ := shared.NewWaypoint("X1-AU21-K82", 0, 0)
	ship := newRefreshTestShip(t, symbol, location, 0)
	if err := ship.ReserveByCaptain(reason, shared.NewRealClock()); err != nil {
		t.Fatalf("ReserveByCaptain: %v", err)
	}
	return ship
}

func newRefreshTestShip(t *testing.T, symbol string, location *shared.Waypoint, cargoUnits int) *navigation.Ship {
	t.Helper()
	fuel, err := shared.NewFuel(0, 0)
	if err != nil {
		t.Fatalf("NewFuel: %v", err)
	}
	var inventory []*shared.CargoItem
	if cargoUnits > 0 {
		item, err := shared.NewCargoItem("IRON_ORE", "Iron Ore", "", cargoUnits)
		if err != nil {
			t.Fatalf("NewCargoItem: %v", err)
		}
		inventory = []*shared.CargoItem{item}
	}
	cargo, err := shared.NewCargo(40, cargoUnits, inventory)
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
		"HAULER",
		nil,
		navigation.NavStatusInOrbit,
	)
	if err != nil {
		t.Fatalf("NewShip: %v", err)
	}
	return ship
}

// Reproduces the phantom-cargo desync: the daemon cache holds 40/40 IRON_ORE the
// server says is 0. RefreshShip must force a write-through fetch (SyncShipFromAPI)
// and return the server-true state, never serving the stale cache via FindBySymbol.
func TestRefreshShip_ForcesWriteThroughAndReturnsServerState(t *testing.T) {
	location, _ := shared.NewWaypoint("X1-AU21-K82", 0, 0)
	serverTrue := newRefreshTestShip(t, "TORWIND-1", location, 0)

	repo := &refreshStubShipRepo{syncedShip: serverTrue}

	handler := NewRefreshShipHandler(repo, nil, nil, nil)

	pid := 1
	resp, err := handler.Handle(context.Background(), &RefreshShipQuery{
		ShipSymbol: "TORWIND-1",
		PlayerID:   &pid,
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	refreshResp, ok := resp.(*RefreshShipResponse)
	if !ok {
		t.Fatalf("unexpected response type: %T", resp)
	}
	if repo.syncCalledCount != 1 {
		t.Fatalf("expected exactly one write-through sync from API, got %d", repo.syncCalledCount)
	}
	if repo.findCalledCount != 0 {
		t.Fatalf("expected no stale cache read, got %d FindBySymbol calls", repo.findCalledCount)
	}
	if refreshResp.Ship.CargoUnits() != 0 {
		t.Fatalf("expected reconciled cargo of 0 units, got %d", refreshResp.Ship.CargoUnits())
	}
}

// dispatchRefresh runs the handler and returns the refreshed ship, failing the
// test on any error or unexpected response type.
func dispatchRefresh(t *testing.T, handler *RefreshShipHandler, shipSymbol string) *navigation.Ship {
	t.Helper()
	pid := 1
	resp, err := handler.Handle(context.Background(), &RefreshShipQuery{
		ShipSymbol: shipSymbol,
		PlayerID:   &pid,
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	refreshResp, ok := resp.(*RefreshShipResponse)
	if !ok {
		t.Fatalf("unexpected response type: %T", resp)
	}
	return refreshResp.Ship
}

// The trade-route CLI runner died after persisting a claim, then its PENDING
// container row was reaped — the ships row references a container that no longer
// exists. Refresh must recognise the dangling reference as orphaned and free the
// hull (sp-vjwb).
func TestRefreshShip_ClearsClaimWhenOwningContainerIsGone(t *testing.T) {
	ship := newAssignedRefreshTestShip(t, "TORWIND-3", "trade-route-TORWIND-3-78dd6806")
	repo := &refreshStubShipRepo{syncedShip: ship}
	reader := &stubContainerStatusReader{found: false} // container row does not exist

	handler := NewRefreshShipHandler(repo, nil, reader, nil)

	refreshed := dispatchRefresh(t, handler, "TORWIND-3")

	if reader.callCount != 1 {
		t.Fatalf("expected the owning container to be checked once, got %d", reader.callCount)
	}
	if reader.askedContainer != "trade-route-TORWIND-3-78dd6806" {
		t.Fatalf("expected the ship's claimed container to be checked, got %q", reader.askedContainer)
	}
	if len(repo.savedShips) != 1 {
		t.Fatalf("expected the freed hull to be persisted exactly once, got %d Save calls", len(repo.savedShips))
	}
	if refreshed.IsAssigned() {
		t.Fatalf("expected the claim cleared, but ship is still assigned to %q", refreshed.ContainerID())
	}
}

// The classic sp-vjwb deadlock: the ships row still references the trade-route
// container, and that container row survives but is PENDING — never adopted by
// the daemon, invisible to restart recovery, a dead CLI-runner artifact. Refresh
// must treat PENDING-with-claim as orphaned and free the hull.
func TestRefreshShip_ClearsClaimWhenOwningContainerIsPending(t *testing.T) {
	ship := newAssignedRefreshTestShip(t, "TORWIND-3", "trade-route-TORWIND-3-78dd6806")
	repo := &refreshStubShipRepo{syncedShip: ship}
	reader := &stubContainerStatusReader{status: "PENDING", found: true}

	handler := NewRefreshShipHandler(repo, nil, reader, nil)

	refreshed := dispatchRefresh(t, handler, "TORWIND-3")

	if len(repo.savedShips) != 1 {
		t.Fatalf("expected the freed hull to be persisted exactly once, got %d Save calls", len(repo.savedShips))
	}
	if refreshed.IsAssigned() {
		t.Fatalf("expected the PENDING-owned claim cleared, but ship is still assigned to %q", refreshed.ContainerID())
	}
}

// Safety: a RUNNING container is a live daemon worker actively using the ship.
// Refresh must NOT clear its claim — doing so would rip the hull out from under
// an active worker mid-operation.
func TestRefreshShip_KeepsClaimWhenOwningContainerIsRunning(t *testing.T) {
	ship := newAssignedRefreshTestShip(t, "TORWIND-5", "gas_siphon_worker-TORWIND-5")
	repo := &refreshStubShipRepo{syncedShip: ship}
	reader := &stubContainerStatusReader{status: "RUNNING", found: true}

	handler := NewRefreshShipHandler(repo, nil, reader, nil)

	refreshed := dispatchRefresh(t, handler, "TORWIND-5")

	if len(repo.savedShips) != 0 {
		t.Fatalf("a live RUNNING worker's ship must never be released, got %d Save calls", len(repo.savedShips))
	}
	if !refreshed.IsAssigned() {
		t.Fatalf("expected the live claim preserved, but ship was released")
	}
	if refreshed.ContainerID() != "gas_siphon_worker-TORWIND-5" {
		t.Fatalf("expected the ship to keep its container claim, got %q", refreshed.ContainerID())
	}
}

// Safety regression (sp-9xc0): INTERRUPTED means the container was RUNNING when
// the daemon stopped and is resurrected by restart recovery — it is a live,
// recoverable owner, not a terminal one. The terminal-state extension below must
// not sweep INTERRUPTED in alongside COMPLETED/FAILED/STOPPED.
func TestRefreshShip_KeepsClaimWhenOwningContainerIsInterrupted(t *testing.T) {
	ship := newAssignedRefreshTestShip(t, "TORWIND-8", "mfg_coordinator-TORWIND-8")
	repo := &refreshStubShipRepo{syncedShip: ship}
	reader := &stubContainerStatusReader{status: "INTERRUPTED", found: true}

	handler := NewRefreshShipHandler(repo, nil, reader, nil)

	refreshed := dispatchRefresh(t, handler, "TORWIND-8")

	if len(repo.savedShips) != 0 {
		t.Fatalf("a recoverable INTERRUPTED owner's ship must never be released, got %d Save calls", len(repo.savedShips))
	}
	if !refreshed.IsAssigned() {
		t.Fatalf("expected the claim preserved for an INTERRUPTED (recoverable) owner, but ship was released")
	}
}

// sp-9xc0: dock-TORWIND-6-e592be41 reached COMPLETED without ever firing its
// claim release, so ship T6 stayed pinned at F45 with 43 FAB_MATS aboard —
// forever, since 'container stop' also refuses a container already in a
// terminal state, leaving no manual escape. A terminal container (COMPLETED,
// FAILED, or STOPPED) can never resume running, so any claim it still owns is
// stale by definition, exactly like the GONE and PENDING cases above. Refresh
// must free the hull for each terminal status.
func TestRefreshShip_ClearsClaimWhenOwningContainerIsCompleted(t *testing.T) {
	assertTerminalContainerClaimCleared(t, "COMPLETED")
}

func TestRefreshShip_ClearsClaimWhenOwningContainerIsFailed(t *testing.T) {
	assertTerminalContainerClaimCleared(t, "FAILED")
}

func TestRefreshShip_ClearsClaimWhenOwningContainerIsStopped(t *testing.T) {
	assertTerminalContainerClaimCleared(t, "STOPPED")
}

// assertTerminalContainerClaimCleared drives RefreshShip for a ship whose
// claiming container reports the given terminal status and asserts the claim
// was released exactly once (sp-9xc0).
func assertTerminalContainerClaimCleared(t *testing.T, status string) {
	t.Helper()
	ship := newAssignedRefreshTestShip(t, "TORWIND-6", "dock-TORWIND-6-e592be41")
	repo := &refreshStubShipRepo{syncedShip: ship}
	reader := &stubContainerStatusReader{status: status, found: true}

	handler := NewRefreshShipHandler(repo, nil, reader, nil)

	refreshed := dispatchRefresh(t, handler, "TORWIND-6")

	if len(repo.savedShips) != 1 {
		t.Fatalf("expected the freed hull to be persisted exactly once for a %s owner, got %d Save calls", status, len(repo.savedShips))
	}
	if refreshed.IsAssigned() {
		t.Fatalf("expected the %s-owned claim cleared, but ship is still assigned to %q", status, refreshed.ContainerID())
	}
}

// An idle, unassigned ship has no claim to reconcile: refresh must not even look
// up a container, and must never write the ship back.
func TestRefreshShip_IdleShipUnaffectedByReconciliation(t *testing.T) {
	location, _ := shared.NewWaypoint("X1-AU21-K82", 0, 0)
	idle := newRefreshTestShip(t, "TORWIND-1", location, 0) // not assigned
	repo := &refreshStubShipRepo{syncedShip: idle}
	reader := &stubContainerStatusReader{status: "RUNNING", found: true}

	handler := NewRefreshShipHandler(repo, nil, reader, nil)

	refreshed := dispatchRefresh(t, handler, "TORWIND-1")

	if reader.callCount != 0 {
		t.Fatalf("an unassigned ship must not trigger a container-status lookup, got %d", reader.callCount)
	}
	if len(repo.savedShips) != 0 {
		t.Fatalf("an unassigned ship must not be written back, got %d Save calls", len(repo.savedShips))
	}
	if refreshed.IsAssigned() {
		t.Fatalf("expected the ship to remain unassigned")
	}
}

// This is the subtle safety-relevant case sp-i1ku exists to protect: a captain
// reservation has no container_id (it was never a container claim), so an
// unguarded reconciliation pass would ask the container reader about an empty
// ID, get back "not found", and treat that as orphan evidence exactly like the
// dead-CLI-runner case in TestRefreshShip_ClearsClaimWhenOwningContainerIsGone
// — reaping a reservation the captain is actively relying on. Reconciliation
// must recognise a captain reservation and never even perform the lookup.
func TestRefreshShip_KeepsCaptainReservationEvenWhenContainerLookupWouldOrphanIt(t *testing.T) {
	ship := newCaptainReservedRefreshTestShip(t, "TORWIND-7", "manual gate-supply errand")
	repo := &refreshStubShipRepo{syncedShip: ship}
	reader := &stubContainerStatusReader{found: false} // would read as orphaned if ever asked

	handler := NewRefreshShipHandler(repo, nil, reader, nil)

	refreshed := dispatchRefresh(t, handler, "TORWIND-7")

	if reader.callCount != 0 {
		t.Fatalf("a captain reservation must never trigger a container-status lookup, got %d", reader.callCount)
	}
	if len(repo.savedShips) != 0 {
		t.Fatalf("a captain reservation must survive reconciliation untouched, got %d Save calls", len(repo.savedShips))
	}
	if !refreshed.IsReservedByCaptain() {
		t.Fatalf("expected the captain reservation to remain intact across refresh")
	}
	if refreshed.CaptainReservationReason() != "manual gate-supply errand" {
		t.Fatalf("expected reservation reason preserved, got %q", refreshed.CaptainReservationReason())
	}
}

package commands

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	daemonPort "github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// --- test doubles (port boundaries only) -------------------------------------

type spawnShipSnapshot struct {
	containerID string
	assigned    bool
}

type spawnShipClaim struct {
	symbol      string
	containerID string
	operation   string
}

type spawnFakeShipRepo struct {
	navigation.ShipRepository

	ship     *navigation.Ship
	findErr  error
	saveErr  error
	claimErr error // injected ClaimShip rejection (e.g. fleet dedication)
	saves    []spawnShipSnapshot
	claims   []spawnShipClaim // successful ClaimShip calls, in order
}

func (r *spawnFakeShipRepo) FindBySymbol(_ context.Context, _ string, _ shared.PlayerID) (*navigation.Ship, error) {
	if r.findErr != nil {
		return nil, r.findErr
	}
	return r.ship, nil
}

func (r *spawnFakeShipRepo) Save(_ context.Context, ship *navigation.Ship) error {
	if r.saveErr != nil {
		return r.saveErr
	}
	r.saves = append(r.saves, spawnShipSnapshot{containerID: ship.ContainerID(), assigned: ship.IsAssigned()})
	return nil
}

// SaveWithRetry mirrors the real repository's non-conflict path (find → mutate →
// save) so the migrated gas pre-claim release (sp-wa7c) exercises its production
// closure while still honoring findErr/saveErr and routing through Save's snapshot
// tracking.
func (r *spawnFakeShipRepo) SaveWithRetry(ctx context.Context, symbol string, playerID shared.PlayerID, mutate navigation.ShipMutation) (*navigation.Ship, bool, error) {
	sh, err := r.FindBySymbol(ctx, symbol, playerID)
	if err != nil {
		return nil, false, err
	}
	changed, err := mutate(sh)
	if err != nil {
		return sh, false, err
	}
	if !changed {
		return sh, false, nil
	}
	if err := r.Save(ctx, sh); err != nil {
		return sh, false, err
	}
	return sh, true, nil
}

// ClaimShip records the atomic operation-checked claim at the port boundary
// (sp-l7h2 Phase 2). Guard logic itself lives in the real repository and is
// covered by ship_repository_claim_dedication_test.go; rejections here are
// injected via claimErr.
func (r *spawnFakeShipRepo) ClaimShip(_ context.Context, symbol string, containerID string, _ shared.PlayerID, operation string) error {
	if r.claimErr != nil {
		return r.claimErr
	}
	r.claims = append(r.claims, spawnShipClaim{symbol: symbol, containerID: containerID, operation: operation})
	return nil
}

func (r *spawnFakeShipRepo) lastSave(t *testing.T) spawnShipSnapshot {
	t.Helper()
	if len(r.saves) == 0 {
		t.Fatalf("expected at least one ship Save, got none")
	}
	return r.saves[len(r.saves)-1]
}

func (r *spawnFakeShipRepo) lastClaim(t *testing.T) spawnShipClaim {
	t.Helper()
	if len(r.claims) == 0 {
		t.Fatalf("expected at least one ClaimShip call, got none")
	}
	return r.claims[len(r.claims)-1]
}

type spawnFakeDaemonClient struct {
	daemon.DaemonClient

	persistErr error
	startErr   error

	persisted     []string
	started       []string
	stopped       []string
	persistedKind []daemon.ContainerKind
	startedKind   []daemon.ContainerKind
}

func (d *spawnFakeDaemonClient) PersistContainer(_ context.Context, kind daemon.ContainerKind, id string, _ uint, _ interface{}) error {
	d.persisted = append(d.persisted, id)
	d.persistedKind = append(d.persistedKind, kind)
	return d.persistErr
}

func (d *spawnFakeDaemonClient) StartContainer(_ context.Context, kind daemon.ContainerKind, id string) error {
	d.started = append(d.started, id)
	d.startedKind = append(d.startedKind, kind)
	return d.startErr
}

func (d *spawnFakeDaemonClient) StopContainer(_ context.Context, id string) error {
	d.stopped = append(d.stopped, id)
	return nil
}

func newSpawnTestShip(t *testing.T, symbol string) *navigation.Ship {
	t.Helper()
	location, err := shared.NewWaypoint("X1-TEST-A1", 0, 0)
	if err != nil {
		t.Fatalf("NewWaypoint: %v", err)
	}
	fuel, err := shared.NewFuel(100, 100)
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
		100,
		40,
		cargo,
		9,
		"FRAME_FRIGATE",
		"SIPHON",
		nil,
		navigation.NavStatusInOrbit,
	)
	if err != nil {
		t.Fatalf("NewShip: %v", err)
	}
	return ship
}

func newGasSpawnHandler(shipRepo navigation.ShipRepository, daemonClient *spawnFakeDaemonClient) *RunGasCoordinatorHandler {
	return &RunGasCoordinatorHandler{
		shipRepo:     shipRepo,
		daemonClient: daemonClient,
		clock:        shared.NewRealClock(),
	}
}

func gasSpawnCommand() *RunGasCoordinatorCommand {
	return &RunGasCoordinatorCommand{
		GasOperationID: "gas-op-1",
		PlayerID:       shared.MustNewPlayerID(1),
		GasGiant:       "X1-TEST-GG",
		ContainerID:    "gas-coordinator-1",
	}
}

// --- siphon worker pins ------------------------------------------------------

func TestSpawnSiphonWorker_HappyPath_PersistsAssignsStarts(t *testing.T) {
	ship := newSpawnTestShip(t, "AGENT-SIPHON-1")
	if err := ship.AssignToContainer("gas-coordinator-1", shared.NewRealClock()); err != nil {
		t.Fatalf("seed assignment: %v", err)
	}
	repo := &spawnFakeShipRepo{ship: ship}
	daemon := &spawnFakeDaemonClient{}
	handler := newGasSpawnHandler(repo, daemon)

	id, err := handler.spawnSiphonWorker(context.Background(), gasSpawnCommand(), "AGENT-SIPHON-1")
	if err != nil {
		t.Fatalf("expected happy path, got error: %v", err)
	}
	if !strings.HasPrefix(id, "siphon-worker-") {
		t.Fatalf("expected siphon-worker id prefix, got %q", id)
	}
	if len(daemon.persisted) != 1 || daemon.persisted[0] != id {
		t.Fatalf("expected persist of %q, got %v", id, daemon.persisted)
	}
	if len(daemon.started) != 1 || daemon.started[0] != id {
		t.Fatalf("expected start of %q, got %v", id, daemon.started)
	}
	if daemon.persistedKind[0] != daemonPort.ContainerKindGasSiphonWorker || daemon.startedKind[0] != daemonPort.ContainerKindGasSiphonWorker {
		t.Fatalf("expected gas siphon worker kind, got persist=%v start=%v", daemon.persistedKind, daemon.startedKind)
	}
	if snap := repo.lastSave(t); snap.containerID != id || !snap.assigned {
		t.Fatalf("expected ship saved assigned to %q, got %+v", id, snap)
	}
	if len(daemon.stopped) != 0 {
		t.Fatalf("expected no StopContainer on happy path, got %v", daemon.stopped)
	}
}

func TestSpawnSiphonWorker_PersistFails_NothingToRollBack(t *testing.T) {
	ship := newSpawnTestShip(t, "AGENT-SIPHON-1")
	_ = ship.AssignToContainer("gas-coordinator-1", shared.NewRealClock())
	repo := &spawnFakeShipRepo{ship: ship}
	daemon := &spawnFakeDaemonClient{persistErr: errors.New("db down")}
	handler := newGasSpawnHandler(repo, daemon)

	_, err := handler.spawnSiphonWorker(context.Background(), gasSpawnCommand(), "AGENT-SIPHON-1")
	if err == nil {
		t.Fatalf("expected error when persist fails")
	}
	if len(daemon.started) != 0 {
		t.Fatalf("expected no start when persist failed, got %v", daemon.started)
	}
	if len(daemon.stopped) != 0 {
		t.Fatalf("expected no StopContainer (nothing to roll back), got %v", daemon.stopped)
	}
	if len(repo.saves) != 0 {
		t.Fatalf("expected no ship save when persist failed, got %v", repo.saves)
	}
}

func TestSpawnSiphonWorker_SaveFails_ContainerStoppedNoShipLeak(t *testing.T) {
	ship := newSpawnTestShip(t, "AGENT-SIPHON-1")
	_ = ship.AssignToContainer("gas-coordinator-1", shared.NewRealClock())
	repo := &spawnFakeShipRepo{ship: ship, saveErr: errors.New("save failed")}
	daemon := &spawnFakeDaemonClient{}
	handler := newGasSpawnHandler(repo, daemon)

	_, err := handler.spawnSiphonWorker(context.Background(), gasSpawnCommand(), "AGENT-SIPHON-1")
	if err == nil {
		t.Fatalf("expected error when save fails")
	}
	if len(daemon.stopped) != 1 {
		t.Fatalf("expected container stopped exactly once on save failure, got %v", daemon.stopped)
	}
	if len(daemon.started) != 0 {
		t.Fatalf("expected worker not started on save failure, got %v", daemon.started)
	}
}

func TestSpawnSiphonWorker_StartFails_ShipTransferredBackToCoordinator(t *testing.T) {
	ship := newSpawnTestShip(t, "AGENT-SIPHON-1")
	_ = ship.AssignToContainer("gas-coordinator-1", shared.NewRealClock())
	repo := &spawnFakeShipRepo{ship: ship}
	daemon := &spawnFakeDaemonClient{startErr: errors.New("start boom")}
	handler := newGasSpawnHandler(repo, daemon)

	_, err := handler.spawnSiphonWorker(context.Background(), gasSpawnCommand(), "AGENT-SIPHON-1")
	if err == nil {
		t.Fatalf("expected error when start fails")
	}
	// Rollback returns the ship to the coordinator (no ship leak).
	if snap := repo.lastSave(t); snap.containerID != "gas-coordinator-1" || !snap.assigned {
		t.Fatalf("expected ship transferred back to coordinator, got %+v", snap)
	}
	if len(daemon.stopped) != 1 {
		t.Fatalf("expected persisted container stopped exactly once on start failure, got %v", daemon.stopped)
	}
}

// --- storage ship worker pins ------------------------------------------------

func TestSpawnStorageShipWorker_HappyPath_PersistsClaimsStarts(t *testing.T) {
	ship := newSpawnTestShip(t, "AGENT-STORAGE-1")
	_ = ship.AssignToContainer("old-container", shared.NewRealClock())
	repo := &spawnFakeShipRepo{ship: ship}
	daemon := &spawnFakeDaemonClient{}
	handler := newGasSpawnHandler(repo, daemon)

	id, err := handler.spawnStorageShipWorker(context.Background(), gasSpawnCommand(), "AGENT-STORAGE-1")
	if err != nil {
		t.Fatalf("expected happy path, got error: %v", err)
	}
	if !strings.HasPrefix(id, "storage-ship-") {
		t.Fatalf("expected storage-ship id prefix, got %q", id)
	}
	if len(daemon.persisted) != 1 || daemon.persisted[0] != id {
		t.Fatalf("expected persist of %q, got %v", id, daemon.persisted)
	}
	if len(daemon.started) != 1 || daemon.started[0] != id {
		t.Fatalf("expected start of %q, got %v", id, daemon.started)
	}
	if daemon.persistedKind[0] != daemonPort.ContainerKindStorageShip || daemon.startedKind[0] != daemonPort.ContainerKindStorageShip {
		t.Fatalf("expected storage ship kind, got persist=%v start=%v", daemon.persistedKind, daemon.startedKind)
	}
	// The stale claim from a previous run is force-released (persisted), and the
	// acquisition itself goes through the atomic operation-checked ClaimShip
	// (sp-l7h2 Phase 2) — not an AssignToContainer+Save read-modify-write.
	if snap := repo.lastSave(t); snap.assigned {
		t.Fatalf("expected the persisted Save to be the pre-claim release, got %+v", snap)
	}
	if claim := repo.lastClaim(t); claim.symbol != "AGENT-STORAGE-1" || claim.containerID != id || claim.operation != "gas" {
		t.Fatalf("expected atomic claim of AGENT-STORAGE-1 by %q under operation gas, got %+v", id, claim)
	}
	if len(daemon.stopped) != 0 {
		t.Fatalf("expected no StopContainer on happy path, got %v", daemon.stopped)
	}
}

// A genuinely idle storage hull needs no pre-claim release at all: the spawn
// must issue exactly one write — the atomic claim — and zero Saves, so there
// is no release-churn on hulls nobody held.
func TestSpawnStorageShipWorker_IdleHull_ClaimedWithoutPreRelease(t *testing.T) {
	repo := &spawnFakeShipRepo{ship: newSpawnTestShip(t, "AGENT-STORAGE-1")}
	daemon := &spawnFakeDaemonClient{}
	handler := newGasSpawnHandler(repo, daemon)

	id, err := handler.spawnStorageShipWorker(context.Background(), gasSpawnCommand(), "AGENT-STORAGE-1")
	if err != nil {
		t.Fatalf("expected happy path, got error: %v", err)
	}
	if len(repo.saves) != 0 {
		t.Fatalf("expected no Save for an idle hull (claim only), got %v", repo.saves)
	}
	if claim := repo.lastClaim(t); claim.containerID != id || claim.operation != "gas" {
		t.Fatalf("expected atomic claim by %q under operation gas, got %+v", id, claim)
	}
}

// sp-l7h2 Phase 2: a hull the captain dedicated to another fleet must be
// rejected at the acquisition boundary — spawn fails loudly, the persisted
// worker container is stopped, and the idle hull is never touched (no release
// write, no assignment).
func TestSpawnStorageShipWorker_DedicatedToOtherFleet_RejectedNotStomped(t *testing.T) {
	repo := &spawnFakeShipRepo{
		ship:     newSpawnTestShip(t, "AGENT-STORAGE-1"),
		claimErr: shared.NewShipDedicatedToOtherFleetError("AGENT-STORAGE-1", "contract", "gas"),
	}
	daemon := &spawnFakeDaemonClient{}
	handler := newGasSpawnHandler(repo, daemon)

	_, err := handler.spawnStorageShipWorker(context.Background(), gasSpawnCommand(), "AGENT-STORAGE-1")
	if err == nil {
		t.Fatalf("expected dedication rejection to fail the spawn")
	}
	var dedicated *shared.ShipDedicatedToOtherFleetError
	if !errors.As(err, &dedicated) {
		t.Fatalf("expected ShipDedicatedToOtherFleetError to surface verbatim, got %v", err)
	}
	if len(repo.saves) != 0 {
		t.Fatalf("expected the foreign-pinned idle hull untouched (no release/assign writes), got %v", repo.saves)
	}
	if len(daemon.stopped) != 1 {
		t.Fatalf("expected the persisted worker container stopped exactly once, got %v", daemon.stopped)
	}
	if len(daemon.started) != 0 {
		t.Fatalf("expected worker not started on claim rejection, got %v", daemon.started)
	}
}

func TestSpawnStorageShipWorker_PersistFails_NothingToRollBack(t *testing.T) {
	ship := newSpawnTestShip(t, "AGENT-STORAGE-1")
	_ = ship.AssignToContainer("old-container", shared.NewRealClock())
	repo := &spawnFakeShipRepo{ship: ship}
	daemon := &spawnFakeDaemonClient{persistErr: errors.New("db down")}
	handler := newGasSpawnHandler(repo, daemon)

	_, err := handler.spawnStorageShipWorker(context.Background(), gasSpawnCommand(), "AGENT-STORAGE-1")
	if err == nil {
		t.Fatalf("expected error when persist fails")
	}
	if len(daemon.started) != 0 {
		t.Fatalf("expected no start when persist failed, got %v", daemon.started)
	}
	if len(daemon.stopped) != 0 {
		t.Fatalf("expected no StopContainer (nothing persisted), got %v", daemon.stopped)
	}
}

func TestSpawnStorageShipWorker_SaveFails_ContainerStopped(t *testing.T) {
	ship := newSpawnTestShip(t, "AGENT-STORAGE-1")
	_ = ship.AssignToContainer("old-container", shared.NewRealClock())
	repo := &spawnFakeShipRepo{ship: ship, saveErr: errors.New("save failed")}
	daemon := &spawnFakeDaemonClient{}
	handler := newGasSpawnHandler(repo, daemon)

	_, err := handler.spawnStorageShipWorker(context.Background(), gasSpawnCommand(), "AGENT-STORAGE-1")
	if err == nil {
		t.Fatalf("expected error when save fails")
	}
	if len(daemon.stopped) != 1 {
		t.Fatalf("expected container stopped exactly once on save failure, got %v", daemon.stopped)
	}
	if len(daemon.started) != 0 {
		t.Fatalf("expected worker not started on save failure, got %v", daemon.started)
	}
}

func TestSpawnStorageShipWorker_StartFails_ShipReleased(t *testing.T) {
	ship := newSpawnTestShip(t, "AGENT-STORAGE-1")
	_ = ship.AssignToContainer("old-container", shared.NewRealClock())
	repo := &spawnFakeShipRepo{ship: ship}
	daemon := &spawnFakeDaemonClient{startErr: errors.New("start boom")}
	handler := newGasSpawnHandler(repo, daemon)

	_, err := handler.spawnStorageShipWorker(context.Background(), gasSpawnCommand(), "AGENT-STORAGE-1")
	if err == nil {
		t.Fatalf("expected error when start fails")
	}
	// Rollback force-releases the ship (no ship leak).
	if snap := repo.lastSave(t); snap.assigned {
		t.Fatalf("expected ship released on start failure, got %+v", snap)
	}
	if len(daemon.stopped) != 1 {
		t.Fatalf("expected persisted container stopped exactly once on start failure, got %v", daemon.stopped)
	}
}

// --- siphon pool acquisition (createPoolAssignments) --------------------------

// The gas pool is the operation's acquisition boundary for siphon hulls: every
// configured ship is claimed through the atomic operation-checked ClaimShip
// under the gas fleet identity (sp-l7h2 Phase 2). An idle hull takes exactly
// one write — the claim — with no gratuitous release Save in front of it.
func TestCreatePoolAssignments_IdleHull_ClaimedUnderGasOperation(t *testing.T) {
	repo := &spawnFakeShipRepo{ship: newSpawnTestShip(t, "AGENT-SIPHON-1")}
	handler := newGasSpawnHandler(repo, &spawnFakeDaemonClient{})

	err := handler.createPoolAssignments(context.Background(), []string{"AGENT-SIPHON-1"}, "gas-coordinator-1", shared.MustNewPlayerID(1))
	if err != nil {
		t.Fatalf("expected idle hull pooled, got error: %v", err)
	}
	if claim := repo.lastClaim(t); claim.symbol != "AGENT-SIPHON-1" || claim.containerID != "gas-coordinator-1" || claim.operation != "gas" {
		t.Fatalf("expected atomic claim by the coordinator under operation gas, got %+v", claim)
	}
	if len(repo.saves) != 0 {
		t.Fatalf("expected no Save for an idle hull (claim only), got %v", repo.saves)
	}
}

// Recovery semantics preserved: a configured hull still held by a previous
// run's container is force-taken — released (persisted) and then re-claimed
// atomically by the new coordinator.
func TestCreatePoolAssignments_StaleClaim_ForceTakenThenClaimed(t *testing.T) {
	ship := newSpawnTestShip(t, "AGENT-SIPHON-1")
	_ = ship.AssignToContainer("gas-coordinator-OLD", shared.NewRealClock())
	repo := &spawnFakeShipRepo{ship: ship}
	handler := newGasSpawnHandler(repo, &spawnFakeDaemonClient{})

	err := handler.createPoolAssignments(context.Background(), []string{"AGENT-SIPHON-1"}, "gas-coordinator-1", shared.MustNewPlayerID(1))
	if err != nil {
		t.Fatalf("expected stale claim force-taken, got error: %v", err)
	}
	if snap := repo.lastSave(t); snap.assigned {
		t.Fatalf("expected the persisted Save to be the stale-claim release, got %+v", snap)
	}
	if claim := repo.lastClaim(t); claim.containerID != "gas-coordinator-1" || claim.operation != "gas" {
		t.Fatalf("expected re-claim by the new coordinator under operation gas, got %+v", claim)
	}
}

// sp-l7h2 Phase 2: a configured hull the captain dedicated to another fleet is
// rejected inside ClaimShip's locked transaction — pooling fails loudly, and an
// idle foreign-pinned hull is never written to at all.
func TestCreatePoolAssignments_DedicatedToOtherFleet_Rejected(t *testing.T) {
	repo := &spawnFakeShipRepo{
		ship:     newSpawnTestShip(t, "AGENT-SIPHON-1"),
		claimErr: shared.NewShipDedicatedToOtherFleetError("AGENT-SIPHON-1", "contract", "gas"),
	}
	handler := newGasSpawnHandler(repo, &spawnFakeDaemonClient{})

	err := handler.createPoolAssignments(context.Background(), []string{"AGENT-SIPHON-1"}, "gas-coordinator-1", shared.MustNewPlayerID(1))
	if err == nil {
		t.Fatalf("expected dedication rejection to fail pooling")
	}
	var dedicated *shared.ShipDedicatedToOtherFleetError
	if !errors.As(err, &dedicated) {
		t.Fatalf("expected ShipDedicatedToOtherFleetError to surface verbatim, got %v", err)
	}
	if len(repo.saves) != 0 {
		t.Fatalf("expected the foreign-pinned idle hull untouched, got %v", repo.saves)
	}
}

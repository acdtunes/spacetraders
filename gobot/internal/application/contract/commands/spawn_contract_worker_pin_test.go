package commands

import (
	"context"
	"errors"
	"strings"
	"testing"

	contractServices "github.com/andrescamacho/spacetraders-go/internal/application/contract/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

type contractShipSnapshot struct {
	containerID string
	assigned    bool
}

type contractShipClaim struct {
	symbol      string
	containerID string
	operation   string
}

type spawnContractFakeShipRepo struct {
	navigation.ShipRepository

	ship     *navigation.Ship
	findErr  error
	saveErr  error
	claimErr error // injected ClaimShip rejection (e.g. fleet dedication)
	saves    []contractShipSnapshot
	claims   []contractShipClaim // successful ClaimShip calls, in order
}

func (r *spawnContractFakeShipRepo) FindBySymbol(_ context.Context, _ string, _ shared.PlayerID) (*navigation.Ship, error) {
	if r.findErr != nil {
		return nil, r.findErr
	}
	return r.ship, nil
}

func (r *spawnContractFakeShipRepo) Save(_ context.Context, ship *navigation.Ship) error {
	if r.saveErr != nil {
		return r.saveErr
	}
	r.saves = append(r.saves, contractShipSnapshot{containerID: ship.ContainerID(), assigned: ship.IsAssigned()})
	return nil
}

// SaveWithRetry mirrors the real repository's non-conflict path (find → mutate →
// save) so the migrated balance-release (sp-wa7c) exercises its production closure
// while still recording into saves.
func (r *spawnContractFakeShipRepo) SaveWithRetry(ctx context.Context, symbol string, playerID shared.PlayerID, mutate navigation.ShipMutation) (*navigation.Ship, bool, error) {
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
// (sp-lprs / sp-l7h2 Phase 2.5). The dedication/assignment guard logic itself
// lives in the real repository and is covered by
// ship_repository_claim_dedication_test.go; rejections here are injected via
// claimErr.
func (r *spawnContractFakeShipRepo) ClaimShip(_ context.Context, symbol string, containerID string, _ shared.PlayerID, operation string) error {
	if r.claimErr != nil {
		return r.claimErr
	}
	r.claims = append(r.claims, contractShipClaim{symbol: symbol, containerID: containerID, operation: operation})
	return nil
}

func (r *spawnContractFakeShipRepo) lastSave(t *testing.T) contractShipSnapshot {
	t.Helper()
	if len(r.saves) == 0 {
		t.Fatalf("expected at least one ship Save, got none")
	}
	return r.saves[len(r.saves)-1]
}

func (r *spawnContractFakeShipRepo) lastClaim(t *testing.T) contractShipClaim {
	t.Helper()
	if len(r.claims) == 0 {
		t.Fatalf("expected at least one ClaimShip call, got none")
	}
	return r.claims[len(r.claims)-1]
}

type spawnContractFakeDaemonClient struct {
	daemon.DaemonClient

	persistErr error
	startErr   error

	persisted     []string
	started       []string
	stopped       []string
	persistedKind []daemon.ContainerKind
	startedKind   []daemon.ContainerKind
}

func (d *spawnContractFakeDaemonClient) PersistContainer(_ context.Context, kind daemon.ContainerKind, id string, _ uint, _ interface{}) error {
	d.persisted = append(d.persisted, id)
	d.persistedKind = append(d.persistedKind, kind)
	return d.persistErr
}

func (d *spawnContractFakeDaemonClient) StartContainer(_ context.Context, kind daemon.ContainerKind, id string) error {
	d.started = append(d.started, id)
	d.startedKind = append(d.startedKind, kind)
	return d.startErr
}

func (d *spawnContractFakeDaemonClient) StopContainer(_ context.Context, id string) error {
	d.stopped = append(d.stopped, id)
	return nil
}

func newContractSpawnHandler(repo navigation.ShipRepository, daemonClient *spawnContractFakeDaemonClient) *RunFleetCoordinatorHandler {
	return &RunFleetCoordinatorHandler{
		workerLifecycleManager: contractServices.NewWorkerLifecycleManager(daemonClient, nil, repo),
		shipRepo:               repo,
		daemonClient:           daemonClient,
		clock:                  shared.NewRealClock(),
	}
}

func contractSpawnCommand() *RunFleetCoordinatorCommand {
	return &RunFleetCoordinatorCommand{
		PlayerID:    shared.MustNewPlayerID(1),
		ContainerID: "fleet-coordinator-1",
	}
}

// newCommandFrigateTestShip builds an idle, UNDEDICATED command frigate (role
// COMMAND, no DedicatedFleet tag) - the shape of a hull deliberately retired via
// `fleet unassign` (sp-sqq5). 115-cargo matches the era-2 upgraded frigate's
// real capacity; spawnContractWorker never applies the sp-uj6a cargo baseline
// (that gate is a separate, later, opt-in filter the caller runs against
// FindIdleLightHaulers' output, and this handler never calls it), so this
// exercises the last-resort/dedication path cleanly, regardless of cargo size.
func newCommandFrigateTestShip(t *testing.T) *navigation.Ship {
	t.Helper()
	location, err := shared.NewWaypoint("X1-TEST-A1", 0, 0)
	if err != nil {
		t.Fatalf("NewWaypoint: %v", err)
	}
	fuel, err := shared.NewFuel(400, 400)
	if err != nil {
		t.Fatalf("NewFuel: %v", err)
	}
	cargo, err := shared.NewCargo(115, 0, nil)
	if err != nil {
		t.Fatalf("NewCargo: %v", err)
	}
	ship, err := navigation.NewShip(
		"TORWIND-1", shared.MustNewPlayerID(1), location, fuel, 400, 115, cargo, 30,
		"FRAME_FRIGATE", "COMMAND", nil, navigation.NavStatusInOrbit,
	)
	if err != nil {
		t.Fatalf("NewShip: %v", err)
	}
	return ship
}

// sp-sqq5 claim-side backstop: when a regular hauler is available the main loop
// passes commandDraftAllowed=false, and spawnContractWorker must REFUSE to draft
// an undedicated command frigate for the contract haul - surfacing the typed
// ErrCommandFrigateNotLastResort, rolling back the persisted worker container,
// never claiming the hull, and never starting a worker. This is the atomic
// single-writer guard behind the discovery-time exclusion: even if discovery
// ever re-admits a retired frigate alongside a hauler, the frigate is not
// re-swept onto contracts here.
func TestSpawnContractWorker_UndedicatedCommandFrigate_NotLastResort_Refused(t *testing.T) {
	frigate := newCommandFrigateTestShip(t)
	repo := &spawnContractFakeShipRepo{ship: frigate}
	daemonClient := &spawnContractFakeDaemonClient{}
	handler := newContractSpawnHandler(repo, daemonClient)

	_, err := handler.spawnContractWorker(context.Background(), contractSpawnCommand(), "TORWIND-1", false)
	if err == nil {
		t.Fatalf("expected the undedicated command frigate draft to be refused while a hauler is available")
	}
	if !errors.Is(err, ErrCommandFrigateNotLastResort) {
		t.Fatalf("expected ErrCommandFrigateNotLastResort, got %v", err)
	}
	if len(repo.claims) != 0 {
		t.Fatalf("a refused command-frigate draft must never claim the hull, got %v", repo.claims)
	}
	if len(repo.saves) != 0 {
		t.Fatalf("a refused command-frigate draft must not write the hull, got %v", repo.saves)
	}
	if len(daemonClient.started) != 0 {
		t.Fatalf("a refused command-frigate draft must not start a worker, got %v", daemonClient.started)
	}
	if len(daemonClient.stopped) != 1 {
		t.Fatalf("the persisted worker container must be rolled back exactly once, got %v", daemonClient.stopped)
	}
}

// sp-sqq5 last-resort preserved (RULINGS #7: the command frigate CAN haul when
// it is the ONLY option): with commandDraftAllowed=true (no regular hauler was a
// candidate), the same undedicated command frigate is drafted normally - the
// exclusion is conditional, never an absolute ban. Guards the regression.
func TestSpawnContractWorker_CommandFrigate_LastResort_Drafted(t *testing.T) {
	frigate := newCommandFrigateTestShip(t)
	repo := &spawnContractFakeShipRepo{ship: frigate}
	daemonClient := &spawnContractFakeDaemonClient{}
	handler := newContractSpawnHandler(repo, daemonClient)

	id, err := handler.spawnContractWorker(context.Background(), contractSpawnCommand(), "TORWIND-1", true)
	if err != nil {
		t.Fatalf("the command frigate must be draftable as a last resort, got error: %v", err)
	}
	if claim := repo.lastClaim(t); claim.symbol != "TORWIND-1" || claim.operation != "contract" {
		t.Fatalf("expected the last-resort command frigate claimed under operation contract, got %+v", claim)
	}
	if len(daemonClient.started) != 1 || daemonClient.started[0] != id {
		t.Fatalf("expected the last-resort worker started, got %v", daemonClient.started)
	}
}

func TestSpawnContractWorker_HappyPath_PersistsAssignsStarts(t *testing.T) {
	ship := newNegotiateTestShip(t, navigation.NavStatusInOrbit)
	repo := &spawnContractFakeShipRepo{ship: ship}
	daemonClient := &spawnContractFakeDaemonClient{}
	handler := newContractSpawnHandler(repo, daemonClient)

	id, err := handler.spawnContractWorker(context.Background(), contractSpawnCommand(), "TORWIND-3", true)
	if err != nil {
		t.Fatalf("expected happy path, got error: %v", err)
	}
	if !strings.HasPrefix(id, "contract-work-") {
		t.Fatalf("expected contract-work id prefix, got %q", id)
	}
	if len(daemonClient.persisted) != 1 || daemonClient.persisted[0] != id {
		t.Fatalf("expected persist of %q, got %v", id, daemonClient.persisted)
	}
	if len(daemonClient.started) != 1 || daemonClient.started[0] != id {
		t.Fatalf("expected start of %q, got %v", id, daemonClient.started)
	}
	if daemonClient.persistedKind[0] != daemon.ContainerKindContractWorkflow || daemonClient.startedKind[0] != daemon.ContainerKindContractWorkflow {
		t.Fatalf("expected contract workflow kind, got persist=%v start=%v", daemonClient.persistedKind, daemonClient.startedKind)
	}
	// The acquisition goes through the atomic operation-checked ClaimShip under
	// the contract fleet identity (sp-lprs) — not an AssignToContainer+Save
	// read-modify-write — so the happy path issues exactly one claim (op
	// "contract") and no Save at all.
	if len(repo.saves) != 0 {
		t.Fatalf("expected no Save on the happy path (atomic claim only), got %v", repo.saves)
	}
	if claim := repo.lastClaim(t); claim.symbol != "TORWIND-3" || claim.containerID != id || claim.operation != "contract" {
		t.Fatalf("expected atomic claim of TORWIND-3 by %q under operation contract, got %+v", id, claim)
	}
	if len(daemonClient.stopped) != 0 {
		t.Fatalf("expected no StopContainer on happy path, got %v", daemonClient.stopped)
	}
}

func TestSpawnContractWorker_PersistFails_NothingToRollBack(t *testing.T) {
	ship := newNegotiateTestShip(t, navigation.NavStatusInOrbit)
	repo := &spawnContractFakeShipRepo{ship: ship}
	daemonClient := &spawnContractFakeDaemonClient{persistErr: errors.New("db down")}
	handler := newContractSpawnHandler(repo, daemonClient)

	_, err := handler.spawnContractWorker(context.Background(), contractSpawnCommand(), "TORWIND-3", true)
	if err == nil {
		t.Fatalf("expected error when persist fails")
	}
	if len(daemonClient.started) != 0 {
		t.Fatalf("expected no start when persist failed, got %v", daemonClient.started)
	}
	if len(daemonClient.stopped) != 0 {
		t.Fatalf("expected no StopContainer (nothing to roll back), got %v", daemonClient.stopped)
	}
	if len(repo.saves) != 0 {
		t.Fatalf("expected no ship save when persist failed, got %v", repo.saves)
	}
}

func TestSpawnContractWorker_ClaimFails_ContainerStoppedNoShipLeak(t *testing.T) {
	ship := newNegotiateTestShip(t, navigation.NavStatusInOrbit)
	repo := &spawnContractFakeShipRepo{ship: ship, claimErr: errors.New("ship already assigned to another container")}
	daemonClient := &spawnContractFakeDaemonClient{}
	handler := newContractSpawnHandler(repo, daemonClient)

	_, err := handler.spawnContractWorker(context.Background(), contractSpawnCommand(), "TORWIND-3", true)
	if err == nil {
		t.Fatalf("expected error when claim fails")
	}
	if len(daemonClient.stopped) != 1 {
		t.Fatalf("expected container stopped exactly once on claim failure, got %v", daemonClient.stopped)
	}
	if len(daemonClient.started) != 0 {
		t.Fatalf("expected worker not started on claim failure, got %v", daemonClient.started)
	}
	if len(repo.saves) != 0 {
		t.Fatalf("expected no ship Save when the claim is rejected (hull untouched), got %v", repo.saves)
	}
}

func TestSpawnContractWorker_StartFails_ShipReleased(t *testing.T) {
	ship := newNegotiateTestShip(t, navigation.NavStatusInOrbit)
	repo := &spawnContractFakeShipRepo{ship: ship}
	daemonClient := &spawnContractFakeDaemonClient{startErr: errors.New("start boom")}
	handler := newContractSpawnHandler(repo, daemonClient)

	_, err := handler.spawnContractWorker(context.Background(), contractSpawnCommand(), "TORWIND-3", true)
	if err == nil {
		t.Fatalf("expected error when start fails")
	}
	// Rollback releases the assignment so the ship returns to the idle pool.
	if snap := repo.lastSave(t); snap.assigned {
		t.Fatalf("expected ship released on start failure, got %+v", snap)
	}
	if len(daemonClient.stopped) != 1 {
		t.Fatalf("expected persisted container stopped exactly once on start failure, got %v", daemonClient.stopped)
	}
}

// sp-lprs (l7h2 Phase 2.5): a hull the captain pinned to another fleet — the
// command frigate's "command" pin is the poach vector this bead closes — is
// rejected inside ClaimShip's locked transaction. spawnContractWorker surfaces
// the rejection verbatim, stops the persisted worker container, never starts a
// worker, and never writes to the foreign-pinned hull. A contract-pinned or
// unpinned hull, by contrast, claims cleanly (op "contract") — see the happy
// path above; the real own-pin/unpinned/foreign-pin verdicts are exercised
// against the DB in ship_repository_claim_dedication_test.go.
func TestSpawnContractWorker_CommandPinnedFrigate_RejectedNotPoached(t *testing.T) {
	ship := newNegotiateTestShip(t, navigation.NavStatusInOrbit)
	repo := &spawnContractFakeShipRepo{
		ship:     ship,
		claimErr: shared.NewShipDedicatedToOtherFleetError("TORWIND-3", "command", "contract"),
	}
	daemonClient := &spawnContractFakeDaemonClient{}
	handler := newContractSpawnHandler(repo, daemonClient)

	_, err := handler.spawnContractWorker(context.Background(), contractSpawnCommand(), "TORWIND-3", true)
	if err == nil {
		t.Fatalf("expected the command-pin dedication to fail the spawn")
	}
	var dedicated *shared.ShipDedicatedToOtherFleetError
	if !errors.As(err, &dedicated) {
		t.Fatalf("expected ShipDedicatedToOtherFleetError to surface verbatim, got %v", err)
	}
	if len(repo.saves) != 0 {
		t.Fatalf("expected the command-pinned hull untouched (no writes), got %v", repo.saves)
	}
	if len(daemonClient.stopped) != 1 {
		t.Fatalf("expected the persisted worker container stopped exactly once, got %v", daemonClient.stopped)
	}
	if len(daemonClient.started) != 0 {
		t.Fatalf("expected worker not started on dedication rejection, got %v", daemonClient.started)
	}
}

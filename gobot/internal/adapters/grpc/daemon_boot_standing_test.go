package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	goodsCmd "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// countContainersOfType counts persisted containers of a given type for a player.
func countContainersOfType(t *testing.T, db *gorm.DB, playerID int, containerType container.ContainerType) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Model(&persistence.ContainerModel{}).
		Where("player_id = ? AND container_type = ?", playerID, string(containerType)).
		Count(&n).Error)
	return n
}

// sp-382j: Admiral-selected launch model (a) — the construction-supply drain must be a STANDING
// coordinator launched unconditionally at every daemon boot (like GoodsFactoryCoordinator /
// StartGoodsFactory and the other standing coordinators), not merely bootstrap-EnsureRunning-only.
// Before this, with no bootstrapper ever having run, the ConstructionCoordinator never started
// even once, so RecoverRunningContainers (which only re-adopts containers already PERSISTED as
// RUNNING) found nothing to recover — a live gate-construction pipeline could sit unsupplied
// forever with the drain never having run a single tick.

func TestDaemonBoot_LaunchesConstructionCoordinatorStanding(t *testing.T) {
	// (a) the coordinator must be declared in the boot standing set, so daemon Start() launches it
	// unconditionally rather than depending on a bootstrapper ever having run.
	found := false
	for _, ct := range bootStandingCoordinatorTypes {
		if ct == container.ContainerTypeConstructionCoordinator {
			found = true
		}
	}
	if !found {
		t.Fatalf("bootStandingCoordinatorTypes must include ConstructionCoordinator, got %v", bootStandingCoordinatorTypes)
	}

	// (b) launched with iterations=-1 (standing: loops forever inside Handle, never bounded) — the
	// same infinite-drain default the bootstrap gate's EnsureRunning launch already relies on, so
	// the boot-standing launch and the bootstrap-gate launch agree on the same defaulting path.
	s := newFactoryTestServer()
	built, err := s.buildCommandForType("construction_coordinator", map[string]interface{}{
		"container_id": "boot-standing-test",
	}, 1, "boot-standing-test")
	if err != nil {
		t.Fatalf("buildCommandForType(construction_coordinator) failed: %v", err)
	}
	cmd, ok := built.(*goodsCmd.RunConstructionCoordinatorCommand)
	if !ok {
		t.Fatalf("expected *RunConstructionCoordinatorCommand, got %T", built)
	}
	if cmd.MaxIterations != -1 {
		t.Fatalf("boot-standing launch must default to MaxIterations=-1 (infinite drain loop), got %d", cmd.MaxIterations)
	}
}

// TestEnsureBootStandingCoordinators_NoPanicOnGenesisEmptyDB is the sp-ls7x
// cold-boot guard. On a fresh DB with no player row, primaryPlayerID() returns
// 0 (FindOpenEra nil AND players empty). Passing that 0 through to the standing
// coordinators reaches MarketFreshnessSizerCoordinator(...,0,...) ->
// MustNewPlayerID(0), which panics ("player_id must be positive"). In prod the
// panic is caught by supervise.Guard so the daemon stays healthy, but it fires
// on every boot until a player exists — log-noisy and fragile. The standing
// coordinators are all player-scoped, so ensureBootStandingCoordinators must
// skip them entirely when no player exists yet (the next boot after
// registration launches them). Genesis-path only: with a player present
// (playerID>0) behavior is unchanged.
func TestEnsureBootStandingCoordinators_NoPanicOnGenesisEmptyDB(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	s := &DaemonServer{
		db:             db,
		containerRepo:  persistence.NewContainerRepository(db),
		containerSpecs: make(map[string]ContainerSpec),
	}
	s.registerContainerSpecs()

	require.NotPanics(t, func() {
		s.ensureBootStandingCoordinators(context.Background(), 0)
	}, "genesis cold-boot (playerID=0) must not panic: player-scoped standing coordinators must be skipped until a player row exists")
}

// sp-ov8z (epic sp-difa, Auto-pilot Phase 1): the ARMING half of zero-intervention cold start.
// The captain-bootstrap coordinator and the capacity reconciler must be BOOT-STANDING so an era
// transition + daemon boot self-starts the whole cold-start machine with no manual
// `workflow bootstrap` / `workflow capacity-reconciler` launch. Auto-arming the reconciler is only
// safe because sp-2jrz (stop-is-complete-retire) landed — a mid-era restart re-adopts a live one and
// a decommission STOP retires it cleanly.
func TestBootStandingSet_IncludesBootstrapAndReconciler(t *testing.T) {
	require.Contains(t, bootStandingCoordinatorTypes, container.ContainerTypeBootstrapCoordinator,
		"the captain-bootstrap coordinator must be boot-standing: it is the master switch that self-drives DATA→INCOME→GATE and hands off the mature economy (sp-ov8z)")
	require.Contains(t, bootStandingCoordinatorTypes, container.ContainerTypeCapacityReconciler,
		"the capacity reconciler must be boot-standing: it is the only standing brain with no other auto-launch path, restart-safe post-sp-2jrz (sp-ov8z)")
}

// sp-ov8z: the fleet autosizer is deliberately NOT boot-standing. The bootstrap coordinator's GATE
// hand-off (bootstrap_ports_gate.go LaunchAutosizer, idempotent) already launches it at the correct
// mature-economy phase; boot-standing it would launch it PREMATURELY during DATA/INCOME (before
// income exists) and duplicate the launch mechanism. Auto-arming bootstrap transitively guarantees
// the autosizer runs — so the zero-intervention intent is met without boot-standing it. This is the
// tripwire if a future change adds it to the boot set without removing the GATE hand-off.
func TestBootStandingSet_ExcludesFleetAutosizer(t *testing.T) {
	for _, ct := range bootStandingCoordinatorTypes {
		require.NotEqual(t, container.ContainerTypeFleetAutosizer, ct,
			"the fleet autosizer must NOT be boot-standing: the bootstrap GATE hand-off launches it at the mature-economy phase (sp-ov8z)")
	}
}

// sp-ov8z: on a boot with a player present and no standing bootstrap/reconciler yet, both must be
// launched exactly once. The bootstrap launch resolves the agent symbol from the player row (it
// threads into the GATE hand-off), so the persisted container carries it.
func TestEnsureBootStandingCoordinators_LaunchesBootstrapAndReconcilerWhenAbsent(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	s.playerRepo = persistence.NewGormPlayerRepository(db) // so the bootstrap launch resolves the agent symbol

	// The launched standing coordinators spawn background runners that block on the (blocking) test
	// mediator; a cancelable context lets them exit cleanly when the test ends.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s.ensureBootStandingCoordinators(ctx, playerID)

	require.Equal(t, int64(1), countContainersOfType(t, db, playerID, container.ContainerTypeBootstrapCoordinator),
		"boot must launch exactly one standing bootstrap coordinator when none is running")
	require.Equal(t, int64(1), countContainersOfType(t, db, playerID, container.ContainerTypeCapacityReconciler),
		"boot must launch exactly one standing capacity reconciler when none is running")

	// The bootstrap container must carry the resolved agent symbol (REC-AGENT from the test player row).
	var bootstrapModel persistence.ContainerModel
	require.NoError(t, db.Where("player_id = ? AND container_type = ?", playerID,
		string(container.ContainerTypeBootstrapCoordinator)).First(&bootstrapModel).Error)
	require.Contains(t, bootstrapModel.Config, "REC-AGENT",
		"the boot-standing bootstrap launch must resolve + persist the player's agent symbol for the GATE hand-off")
}

// sp-ov8z: idempotence — a warm restart must never double-launch. With a bootstrap and a reconciler
// already RUNNING (recovered from a prior boot), a second ensureBootStandingCoordinators pass must
// launch no duplicate of either, mirroring the market-freshness sizer's containerTypeRunning guard.
func TestEnsureBootStandingCoordinators_IdempotentForBootstrapAndReconciler(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	s.playerRepo = persistence.NewGormPlayerRepository(db)

	insertRunningContainer(t, db, "bootstrap-existing", "bootstrap",
		string(container.ContainerTypeBootstrapCoordinator), `{"container_id":"bootstrap-existing","agent_symbol":"REC-AGENT"}`, playerID, nil)
	insertRunningContainer(t, db, "capacity-reconciler-existing", "capacity_reconciler_coordinator",
		string(container.ContainerTypeCapacityReconciler), `{"container_id":"capacity-reconciler-existing"}`, playerID, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s.ensureBootStandingCoordinators(ctx, playerID)

	require.Equal(t, int64(1), countContainersOfType(t, db, playerID, container.ContainerTypeBootstrapCoordinator),
		"a warm restart must not launch a duplicate bootstrap coordinator when one is already RUNNING")
	require.Equal(t, int64(1), countContainersOfType(t, db, playerID, container.ContainerTypeCapacityReconciler),
		"a warm restart must not launch a duplicate capacity reconciler when one is already RUNNING")
}

// sp-9ujl (epic sp-difa, Auto-pilot Phase 1): the scout-post coordinator must be BOOT-STANDING. The
// MarketFreshnessSizer (already boot-standing) only DECLARES a standing freshness post; the scout-post
// coordinator is what MANS it — assigns a probe (SetAssignedHull), partitions the system, drives the
// P90 rescans + idle-probe re-tasking. Its only pre-fix launch path was the manual CLI, which a cold
// start never runs, so a zero-intervention boot left the declared post UNMANNED with no standing owner
// for market coverage. This tripwire fires if a future change drops it from the boot set.
func TestBootStandingSet_IncludesScoutPostCoordinator(t *testing.T) {
	require.Contains(t, bootStandingCoordinatorTypes, container.ContainerTypeScoutPostCoordinator,
		"the scout-post coordinator must be boot-standing: it MANS the freshness posts the MarketFreshnessSizer declares — without it a cold-start post stays UNMANNED (sp-9ujl)")
}

// sp-9ujl: on a boot with a player present and no standing scout-post coordinator yet, exactly one must
// be launched — the manner for the freshness posts the sizer declares.
func TestEnsureBootStandingCoordinators_LaunchesScoutPostCoordinatorWhenAbsent(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	s.playerRepo = persistence.NewGormPlayerRepository(db) // the co-launched bootstrap resolves the agent symbol

	// The launched standing coordinators spawn background runners that block on the (blocking) test
	// mediator; a cancelable context lets them exit cleanly when the test ends.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s.ensureBootStandingCoordinators(ctx, playerID)

	require.Equal(t, int64(1), countContainersOfType(t, db, playerID, container.ContainerTypeScoutPostCoordinator),
		"boot must launch exactly one standing scout-post coordinator when none is running (sp-9ujl)")
}

// sp-9ujl: idempotence — a warm restart must never double-launch. With a scout-post coordinator already
// RUNNING (recovered from a prior boot), a second ensureBootStandingCoordinators pass must launch no
// duplicate, mirroring the market-freshness sizer's containerTypeRunning guard. A twin reconcile loop
// would fight the first over the same posts and idle probes.
func TestEnsureBootStandingCoordinators_IdempotentForScoutPostCoordinator(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	s.playerRepo = persistence.NewGormPlayerRepository(db)

	insertRunningContainer(t, db, "scoutpost-existing", "scout_post_coordinator",
		string(container.ContainerTypeScoutPostCoordinator), `{"container_id":"scoutpost-existing"}`, playerID, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s.ensureBootStandingCoordinators(ctx, playerID)

	require.Equal(t, int64(1), countContainersOfType(t, db, playerID, container.ContainerTypeScoutPostCoordinator),
		"a warm restart must not launch a duplicate scout-post coordinator when one is already RUNNING (sp-9ujl)")
}
